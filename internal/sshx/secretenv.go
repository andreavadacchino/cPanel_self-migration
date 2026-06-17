package sshx

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"
)

// secretEnvKeys is the security-policy allowlist of environment keys whose VALUE
// must never be inlined into a remote command STRING (argv, world-readable via
// `ps`/`/proc/PID/cmdline`). When a server rejects the SSH env channel
// (AcceptEnv), the streaming bridge delivers these keys through the command's
// STDIN with a `read`+`export` prologue (see secretStdinPrologue) instead of
// WithEnv, so the value lands in the remote process's ENVIRON (owner/root-only) —
// the same confidentiality posture as a successful Setenv. Keep this list small
// and audited: today only the MySQL password (dbmig's dump/import bridge) flows
// here. A non-secret env value (DB names, paths) is still fine to inline.
var secretEnvKeys = map[string]bool{
	"MYSQL_PWD": true,
}

// splitSecretEnv partitions env into the non-secret keys (safe to inline via
// WithEnv) and the secret keys (must be delivered off-argv via the stdin
// channel). Either returned map is nil when it has no entries.
func splitSecretEnv(env map[string]string) (public, secret map[string]string) {
	for k, v := range env {
		if secretEnvKeys[k] {
			if secret == nil {
				secret = make(map[string]string, 1)
			}
			secret[k] = v
		} else {
			if public == nil {
				public = make(map[string]string, len(env))
			}
			public[k] = v
		}
	}
	return public, secret
}

// secretStdinPrologue prepends a POSIX `read`+`export` prologue that pulls each
// secret key's value from the leading line(s) of the command's STDIN (one line
// per key, in keys order) and exports it, then runs cmd. `IFS= read -r` reads
// each value verbatim (no whitespace trimming, no backslash processing) and, on a
// pipe, consumes exactly one line WITHOUT over-reading — so the bytes after the
// secret line(s) reach cmd unchanged (e.g. the mysql import's SQL stream on the
// same fd 0). The secret values never enter the command string. keys must match
// (order and length) the lines secretStdinBytes emits.
func secretStdinPrologue(cmd string, keys []string) string {
	var b strings.Builder
	for _, k := range keys {
		// k is spliced unquoted into `read -r <k>; export <k>`, so it must be a valid
		// env var name; an invalid name is a call-site bug (see mustValidEnvName).
		mustValidEnvName("secretStdinPrologue", k)
		fmt.Fprintf(&b, "IFS= read -r %s; export %s; ", k, k)
	}
	b.WriteString(cmd)
	return b.String()
}

// secretStdinBytes serializes the secret values as newline-terminated lines in
// keys order, to be fed as the FIRST bytes of the command's stdin (consumed by
// secretStdinPrologue's `read`s). A value containing a CR/LF cannot be delivered
// line-wise (it would split across the read boundary and corrupt the trailing
// data stream), so it is rejected fail-closed rather than silently truncated.
func secretStdinBytes(secret map[string]string, keys []string) ([]byte, error) {
	var b bytes.Buffer
	for _, k := range keys {
		v := secret[k]
		if strings.ContainsAny(v, "\n\r") {
			return nil, fmt.Errorf("secret env %s contains a newline; cannot deliver it via the stdin channel", k)
		}
		b.WriteString(v)
		b.WriteByte('\n')
	}
	return b.Bytes(), nil
}

// sortedEnvKeys returns the map's keys in sorted order, for deterministic,
// testable prologue/stdin ordering.
func sortedEnvKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// prependSecretReader returns a reader that yields secret first, then rest. rest
// may be nil (the secret bytes are the whole stdin, e.g. the source mysqldump
// side, which otherwise reads no stdin).
func prependSecretReader(secret []byte, rest io.Reader) io.Reader {
	if rest == nil {
		return bytes.NewReader(secret)
	}
	return io.MultiReader(bytes.NewReader(secret), rest)
}
