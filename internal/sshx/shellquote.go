package sshx

import (
	"fmt"
	"sort"
	"strings"
)

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
