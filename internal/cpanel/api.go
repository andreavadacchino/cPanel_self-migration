// Package cpanel issues UAPI / api2 calls against a cPanel host over SSH and
// parses the JSON responses into typed Go structs.
//
// The tool runs on a third (bridge) machine, so the cPanel binaries (`uapi`,
// and the api2 `curl` to 127.0.0.1:2083) must execute ON the cPanel host. Each
// call is therefore shipped as a small remote shell snippet over SSH; responses
// are parsed with encoding/json into typed structs, and arguments are passed as
// environment variables instead of being interpolated into a command line.
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
// variable (ARG_<i>) so nothing sensitive is interpolated into the command.
// The arg KEYS are fixed identifiers (k=v form), safe to inline.
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
		// On failure the response carries no token (Tokens::create_full_access only
		// returns one on status==1), so a bounded raw snippet here is safe — and it is
		// the only way to diagnose an exotic cPanel build that reports the failure in
		// `messages`, or in an `errors` shape errStrings cannot decode. Debug-only.
		logx.Debug("%s::%s: UAPI non-success status=%d errors=%v messages=%v raw=%s",
			module, fn, env.Result.Status, msgs, errStrings(env.Result.Messages), logx.Snippet(out, 300))
		return zero, fmt.Errorf("%s::%s: status=%d errors=%v", module, fn, env.Result.Status, msgs)
	}
	return env.Result.Data, nil
}
