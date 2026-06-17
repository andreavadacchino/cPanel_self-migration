package config

import (
	"os"
	"path/filepath"
	"testing"
)

// S4 fault-injection (parser/DoS robustness) for config.Load. The config path is
// operator-provided (not remote-untrusted), but a hand-edited or corrupted YAML
// must still degrade to a clean error, never a panic, hang, or OOM. yaml.v3 v3.0.1
// bounds alias expansion and rejects the malformed inputs that panicked older
// versions; these cases lock that in and add overflow/garbage shapes, plus a fuzz
// harness over the whole Load path. writeTemp lives in config_test.go.

// TestFaultSimLoadRejectsMalformedYAML feeds structurally broken or out-of-range
// YAML. Each must return an error (not panic).
func TestFaultSimLoadRejectsMalformedYAML(t *testing.T) {
	cases := map[string]string{
		"unclosed quote":  "src:\n  ip: \"1.1.1.1\n  port: 22\n",
		"tab indentation": "src:\n\tip: 1.1.1.1\n",
		"bad mapping":     "src: : :\n",
		"port overflows int": `
src: { ip: 1.1.1.1, port: 999999999999999999999, ssh_user: u, ssh_pass: p, timeout: 5s }`,
		"timeout garbage": `
src: { ip: 1.1.1.1, port: 22, ssh_user: u, ssh_pass: p, timeout: "not a duration" }`,
		"port wrong type": `
src: { ip: 1.1.1.1, port: [1,2], ssh_user: u, ssh_pass: p, timeout: 5s }`,
		"scalar root":   "just a scalar\n",
		"sequence root": "- a\n- b\n",
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Load panicked on %q: %v", name, r)
				}
			}()
			if cfg, err := Load(writeTemp(t, yaml)); err == nil {
				t.Errorf("Load(%s) = (%+v, nil), want an error", name, cfg)
			}
		})
	}
}

// TestFaultSimLoadAliasBombTerminates feeds a YAML "billion laughs" alias bomb. The
// patched yaml.v3 bounds alias expansion, so Load must RETURN (with an error here,
// since the document does not match the Config schema / has unknown keys) rather than
// hang or exhaust memory. The test passing at all == it terminated.
func TestFaultSimLoadAliasBombTerminates(t *testing.T) {
	const bomb = `
a: &a ["x","x","x","x","x","x","x","x","x"]
b: &b [*a,*a,*a,*a,*a,*a,*a,*a,*a]
c: &c [*b,*b,*b,*b,*b,*b,*b,*b,*b]
d: &d [*c,*c,*c,*c,*c,*c,*c,*c,*c]
e: &e [*d,*d,*d,*d,*d,*d,*d,*d,*d]
src: { ip: 1.1.1.1, port: 22, ssh_user: u, ssh_pass: p, timeout: 5s }
`
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Load panicked on the alias bomb: %v", r)
		}
	}()
	// Unknown keys (a..e) make KnownFields reject it; the point is that Load returns.
	if _, err := Load(writeTemp(t, bomb)); err == nil {
		t.Error("Load(alias bomb with unknown keys) = nil error, want an error (but it MUST terminate)")
	}
}

// FuzzConfigLoad asserts the full Load path never panics over arbitrary file
// contents. Run:
//
//	go test ./internal/config -run x -fuzz FuzzConfigLoad -fuzztime 60s
func FuzzConfigLoad(f *testing.F) {
	seeds := []string{
		"src:\n  ip: 1.1.1.1\n  port: 22\n  ssh_user: u\n  ssh_pass: p\n  timeout: 5s\n",
		"src: { ip: 1.1.1.1, port: 99999, ssh_user: u, ssh_pass: p, timeout: 5s }",
		"not yaml at all",
		"src:\n\tip: 1.1.1.1\n",
		"---\n---\n",
		"\x00\x00\x00",
		"",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, content string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Load panicked on %q: %v", content, r)
			}
		}()
		p := filepath.Join(t.TempDir(), "host.yaml")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Skip() // environmental write failure, not a parser fault
		}
		_, _ = Load(p)
	})
}
