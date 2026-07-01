// Package cpanel issues UAPI / api2 calls against a cPanel host over SSH and
// parses the JSON responses into typed Go structs.
//
// The tool runs on a third (bridge) machine, so the cPanel binaries (`uapi`,
// and the api2 `curl` to 127.0.0.1:2083) must execute ON the cPanel host. Each
// call is therefore shipped as a small remote shell snippet over SSH; responses
// are parsed with encoding/json into typed structs.
//
// Argument VALUES travel as environment variables (ARG_<i>), so they are never
// spliced into the SSH command string itself. Note, however, that the `uapi` CLI
// takes its parameters on the command line and has no stdin/--input mechanism, so
// the generated snippet does `uapi … key="$ARG_i"`: the shell expands the value
// into the argv of the spawned `uapi` PROCESS, where a secret (a MySQL password, a
// mailbox password_hash) is visible in /proc/<pid>/cmdline for the call's duration.
// This is bounded by /proc isolation (cPanel hosts typically run hidepid/CageFS, so
// co-tenants cannot see the process) and by the short call window; there is no
// argv-free alternative while using the uapi CLI. The token-authenticated api2
// `curl` path (addon.go) avoids it by reading the token from `curl --config -`
// (stdin). See uapiArgsScript and docs/USAGE.md for the residual-exposure note.
package cpanel

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// Runner is the subset of *sshx.Client this package needs (satisfied by it).
// It lets tests substitute a fake that returns canned JSON without a real
// cPanel.
type Runner interface {
	RunScript(ctx context.Context, script string, env map[string]string) ([]byte, error)
}

// uapiArgsScript builds a tiny bash snippet that invokes `uapi` with the given
// module/function and arg keys, reading each arg VALUE from an environment
// variable (ARG_<i>) so the value is never spliced into the SSH command string.
// The arg KEYS are fixed identifiers (k=v form), safe to inline.
//
// RESIDUAL EXPOSURE: the `uapi` CLI accepts parameters only on its command line
// (it has no stdin/--input mode), so `key="$ARG_i"` is expanded by the remote
// shell into the argv of the spawned `uapi` process. A secret argument (a MySQL
// password from create_user/set_password) is therefore visible in
// /proc/<uapi-pid>/cmdline for the brief duration of that call. There is no
// argv-free way to pass it through the uapi CLI; the exposure is bounded by /proc
// isolation (hidepid/CageFS on a typical cPanel host) and the short call window.
// See the package doc and docs/USAGE.md.
func uapiArgsScript(module, fn string, args map[string]string) (string, map[string]string) {
	env := map[string]string{}
	var b strings.Builder
	b.WriteString("uapi --output=json ")
	b.WriteString(module)
	b.WriteString(" ")
	b.WriteString(fn)
	// Deterministic order so the remote command is stable/testable.
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		ev := fmt.Sprintf("ARG_%d", i)
		env[ev] = args[k]
		// uapi key=value with the value taken from $ARG_i (quoted).
		fmt.Fprintf(&b, " %s=\"$%s\"", k, ev)
	}
	return b.String(), env
}

// RunUAPI executes a UAPI call on the host and unmarshals result.data into T.
// It returns an error if the SSH command fails, the JSON is unparseable, or
// the UAPI status is not success (1), including any reported error messages.
func RunUAPI[T any](ctx context.Context, c Runner, module, fn string, args map[string]string) (T, error) {
	var zero T
	script, env := uapiArgsScript(module, fn, args)
	out, err := c.RunScript(ctx, script, env)
	if err != nil {
		return zero, fmt.Errorf("%s::%s: %w", module, fn, err)
	}
	logx.Debug("%s::%s: UAPI call succeeded (%d bytes response)", module, fn, len(out))
	// Opt-in, OFF by default (see debug.go): the normal path logs only the length
	// because some success bodies carry a secret (e.g. create_full_access's token).
	// When explicitly enabled for a diagnostic run, also log the full body with
	// secret values redacted — enough to inspect the response SHAPE (e.g. whether
	// expires_at is present) without exposing the token.
	if rawResponseDebug {
		logx.Debug("%s::%s: raw response (secrets redacted): %s", module, fn, redactJSONForDebug(out))
	}
	return parseUAPI[T](module, fn, out)
}

// api2ArgsScript builds a tiny bash snippet that invokes `cpapi2` with the
// given module/function and arg keys, analogous to uapiArgsScript but for the
// legacy cPanel API2 CLI. Used for read-only API2 calls that have no UAPI
// equivalent (e.g. ZoneEdit::fetchzone_records on cPanel < v136).
func api2ArgsScript(module, fn string, args map[string]string) (string, map[string]string) {
	env := map[string]string{}
	var b strings.Builder
	b.WriteString("cpapi2 --output=json ")
	b.WriteString(module)
	b.WriteString(" ")
	b.WriteString(fn)
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		ev := fmt.Sprintf("ARG_%d", i)
		env[ev] = args[k]
		fmt.Fprintf(&b, " %s=\"$%s\"", k, ev)
	}
	return b.String(), env
}

// RunAPI2 executes an API2 call via the cpapi2 CLI on the host and unmarshals
// cpanelresult.data into T. It returns an error if the SSH command fails, the
// JSON is unparseable, or the API2 event.result is not 1.
func RunAPI2[T any](ctx context.Context, c Runner, module, fn string, args map[string]string) (T, error) {
	var zero T
	script, env := api2ArgsScript(module, fn, args)
	out, err := c.RunScript(ctx, script, env)
	if err != nil {
		return zero, fmt.Errorf("cpapi2 %s::%s: %w", module, fn, err)
	}
	logx.Debug("cpapi2 %s::%s: API2 call succeeded (%d bytes response)", module, fn, len(out))
	if rawResponseDebug {
		logx.Debug("cpapi2 %s::%s: raw response (secrets redacted): %s", module, fn, redactJSONForDebug(out))
	}
	return parseAPI2[T](module, fn, out)
}

// parseAPI2 is the pure parsing half of RunAPI2, exposed for unit testing.
func parseAPI2[T any](module, fn string, out []byte) (T, error) {
	var zero T
	var env api2Envelope[T]
	if err := json.Unmarshal(out, &env); err != nil {
		return zero, fmt.Errorf("cpapi2 %s::%s: parse JSON (%d bytes): %w", module, fn, len(out), err)
	}
	if env.CPanelResult.Event.Result != 1 {
		errMsg := env.CPanelResult.Error
		if errMsg == "" {
			errMsg = "unknown API2 error"
		}
		return zero, fmt.Errorf("cpapi2 %s::%s: event.result=%d error=%s", module, fn, env.CPanelResult.Event.Result, errMsg)
	}
	return env.CPanelResult.Data, nil
}

// parseUAPI is the pure parsing half of RunUAPI, exposed for unit testing
// against fixture bytes.
func parseUAPI[T any](module, fn string, out []byte) (T, error) {
	var zero T
	var env envelope[T]
	if err := json.Unmarshal(out, &env); err != nil {
		// Report only the length, never the raw bytes: parseUAPI is generic and also
		// parses Tokens::create_full_access, whose response carries the API token — a
		// snippet here would leak it into the error (and thus logs/screen). The json
		// error itself echoes a syntax position/type, not values. (Matches token.go,
		// which logs length=%d, never the token.)
		return zero, fmt.Errorf("%s::%s: parse JSON (%d bytes): %w", module, fn, len(out), err)
	}
	if env.Result.Status != 1 {
		msgs := errStrings(env.Result.Errors)
		// Debug-only. The RAW body is REDACTED (redactJSONForDebug) before logging: a
		// failure response carries no token (Tokens::create_full_access returns one only on
		// status==1), but some cPanel builds echo request INPUT back into the body, so a
		// create_user/add_pop failure could carry the password/hash under a data field —
		// redaction strips sensitive KEYS (and withholds a non-JSON body) while keeping the
		// diagnostic shape. NOTE the residual: redaction is KEY-based, so a secret a build
		// interpolates INSIDE an errors/messages STRING is NOT detected — and those strings
		// are also logged here (errors=%v / messages=%v) and returned in the error below.
		// In practice cPanel error text describes the problem rather than echoing the
		// submitted value, but treat the errors/messages strings as un-redacted.
		logx.Debug("%s::%s: UAPI non-success status=%d errors=%v messages=%v raw=%s",
			module, fn, env.Result.Status, msgs, errStrings(env.Result.Messages), redactJSONForDebug(out))
		return zero, fmt.Errorf("%s::%s: status=%d errors=%v", module, fn, env.Result.Status, msgs)
	}
	return env.Result.Data, nil
}
