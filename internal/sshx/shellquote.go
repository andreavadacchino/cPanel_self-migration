package sshx

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// envNameRe is the POSIX portable environment-variable name shape. Every env key in
// this codebase is a compile-time literal (e.g. "MYSQL_PWD", "DOM", "ARG_0"), never
// derived from data, so a key that fails this is a PROGRAMMING ERROR at a call site,
// not a runtime/data condition — hence the validators below panic rather than return
// an error. The check matters because a key is spliced UNQUOTED into a shell `export
// <name>=...` / `read -r <name>` (WithEnv, secretStdinPrologue); a malformed name
// (e.g. "x; rm -rf ~") would otherwise be command injection.
var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validEnvName reports whether name is a well-formed POSIX environment-variable name.
func validEnvName(name string) bool { return envNameRe.MatchString(name) }

// mustValidEnvName panics if name is not a well-formed env var name. Used by the
// shell-building helpers, where an invalid name is a call-site bug and must never be
// emitted into a command string.
func mustValidEnvName(where, name string) {
	if !validEnvName(name) {
		panic(fmt.Sprintf("sshx.%s: invalid environment variable name %q (must match [A-Za-z_][A-Za-z0-9_]*)", where, name))
	}
}

// WithEnv prepends `export VAR='value'; ` assignments (single-quote escaped) to a
// command, so each value reaches the remote command WITHOUT interpolation into the
// command body. It is the INLINE FALLBACK used when SSH Setenv is unavailable: the
// streaming bridge now prefers Setenv (see (*Client).trySetenv), which keeps a
// secret value out of the command string entirely, and only falls back to this
// prelude when the server rejects the env channel (AcceptEnv). Keys are emitted in
// sorted order for deterministic, testable output. An empty env returns cmd
// unchanged.
//
// The `export …; ` form (not the `VAR=val cmd` prefix) is required: a bare prefix
// applies the variable only to the next simple command, and `cd` is a builtin — the
// assignment would not survive into the `tar`/`mysql` that follows in the `&&` list.
//
// This is the single source for the maildir/webfiles/dbmig tar-bridge env prelude;
// it must stay consistent because the values (paths, DB names) are untrusted. A
// SECRET value (MYSQL_PWD) is deliberately NOT routed through here on the bridge
// fallback — it would land in argv; the streaming bridge delivers it via stdin
// instead (see secretEnvKeys / secretStdinPrologue).
//
// Each KEY is splice-unquoted into `export <key>=...`, so it must be a valid env var
// name; an invalid name is a call-site bug and panics (see mustValidEnvName). VALUES
// are single-quote escaped and may contain anything.
func WithEnv(cmd string, env map[string]string) string {
	if len(env) == 0 {
		return cmd
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		mustValidEnvName("WithEnv", k)
		fmt.Fprintf(&b, "export %s='%s'; ", k, SingleQuoteEscape(env[k]))
	}
	b.WriteString(cmd)
	return b.String()
}

// SingleQuoteEscape makes s safe inside a single-quoted bash string: every single
// quote becomes a close-quote, a backslash-escaped quote, then a reopen-quote.
func SingleQuoteEscape(s string) string {
	return strings.ReplaceAll(s, "'", `'\''`)
}
