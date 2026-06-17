package cpanel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Raw UAPI response debugging.
//
// The normal logging path (RunUAPI / parseUAPI) deliberately NEVER prints a
// successful response body: it logs only its length. That is a security choice —
// some success bodies carry a secret (notably Tokens::create_full_access, whose
// data.token is a live API token). The cost is that we cannot, from the logs
// alone, see how a particular cPanel build SHAPES a response — for example
// whether create_full_access actually echoes an `expires_at` field, or omits it
// (which makes the tool's expiry hardening fail closed; see token.go and
// docs/DEBUGGING.md).
//
// This facility closes that gap without leaking secrets: when enabled, RunUAPI
// additionally logs the full response body with the value of every sensitive
// key redacted, preserving the STRUCTURE (which keys exist, of which type) — the
// part needed to diagnose a response-shape mismatch.
//
// It is OFF by default and MUST stay off in normal runs. Enable it only for a
// diagnostic run, two ways:
//   - operator: set CPSM_DEBUG_RAW_UAPI=1 (and pass --log-level debug) for one run;
//   - tests: assign rawResponseDebug = true directly (same package), preferably
//     with a defer to restore it.

// rawDebugEnvVar is the environment variable that turns raw-response debugging
// on for a single process. Declared as a const so the docs and the code agree.
const rawDebugEnvVar = "CPSM_DEBUG_RAW_UAPI"

// rawResponseDebug gates the redacted raw-body logging in RunUAPI. Initialized
// from the environment at startup; tests may set it directly.
var rawResponseDebug = rawDebugFromEnv()

// rawDebugFromEnv reports whether rawDebugEnvVar is set to a truthy value.
func rawDebugFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(rawDebugEnvVar))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// sensitiveKeySubstrings drive redaction: a JSON object key whose lower-cased,
// space-trimmed form CONTAINS any of these is treated as a secret and its
// non-empty value is replaced before logging. Substring (not exact) matching is
// deliberate so variants like access_token, auth_token, client_secret, apikey,
// privatekey, x-csrf-token, … are all caught. Over-redaction is harmless here; a
// leaked credential is not. None of the fields we actually need to SEE (name,
// expires_at, expires, expiry, create_time, status) contain any of these, so the
// diagnostic value is preserved.
var sensitiveKeySubstrings = []string{
	"token", "secret", "pass", "key", "auth", "cred", "cookie", "session", "bearer",
}

const redactedPlaceholder = "<redacted>"

// isSensitiveKey reports whether a JSON object key names a value to redact.
func isSensitiveKey(k string) bool {
	k = strings.ToLower(strings.TrimSpace(k))
	for _, sub := range sensitiveKeySubstrings {
		if strings.Contains(k, sub) {
			return true
		}
	}
	return false
}

// redactJSONForDebug parses out as JSON and returns a re-serialized copy in
// which the value of every sensitive key (see isSensitiveKey), at any depth, is
// replaced with redactedPlaceholder. The structure is preserved so a response
// SHAPE can be inspected (e.g. "is expires_at present and numeric?") without
// exposing secrets.
//
// LIMITATION: redaction is key-based. A secret echoed inside the VALUE of a
// non-secret field (e.g. a free-text "message" that quotes the token) is NOT
// detected. The responses this tool reads keep secrets in their own keyed fields
// (create_full_access's token is data.token), so this is safe in practice, but do
// not enable the facility against a host known to inline secrets into messages.
//
// It never returns the raw bytes: if out is not valid JSON, or cannot be
// re-marshaled after redaction, it returns a short, value-free placeholder. This
// guarantees a secret cannot leak through this path even on malformed input.
// An empty or null sensitive value is left untouched, because "" / null carries
// no secret and the empty/absent state is itself diagnostically useful.
func redactJSONForDebug(out []byte) string {
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		return fmt.Sprintf("<%d bytes, not valid JSON; withheld>", len(out))
	}
	// A bare top-level JSON scalar (string/number/bool) has no key to match on and
	// could itself be a secret. The real UAPI path always wraps data in an
	// envelope object, so anything else is unexpected — withhold it rather than
	// echo it. (redactInPlace only descends into objects/arrays.)
	if _, isObj := v.(map[string]any); !isObj {
		if _, isArr := v.([]any); !isArr {
			return fmt.Sprintf("<%d bytes, top-level JSON scalar; withheld>", len(out))
		}
	}
	redactInPlace(v)
	// Encode with HTML escaping off so the body stays human-readable in the log
	// (otherwise the angle brackets in the placeholder, and any '<'/'>'/'&' in the
	// data, come out as \uXXXX). Encode appends a newline; trim it.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprintf("<%d bytes; withheld (re-marshal failed)>", len(out))
	}
	return strings.TrimRight(buf.String(), "\n")
}

// redactInPlace walks an unmarshaled JSON value and redacts sensitive object
// values in place.
func redactInPlace(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if isSensitiveKey(k) && !isEmptyJSONValue(child) {
				t[k] = redactedPlaceholder
				continue
			}
			redactInPlace(child)
		}
	case []any:
		for _, child := range t {
			redactInPlace(child)
		}
	}
}

// isEmptyJSONValue reports whether a decoded JSON value carries no secret to
// hide: a JSON null, or an empty string.
func isEmptyJSONValue(v any) bool {
	if v == nil {
		return true
	}
	if s, ok := v.(string); ok {
		return s == ""
	}
	return false
}
