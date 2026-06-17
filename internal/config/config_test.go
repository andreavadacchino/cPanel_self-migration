package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "host.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return p
}

func TestLoadValid(t *testing.T) {
	const yaml = `
src:
  ip: "203.0.113.10"
  port: 22
  ssh_user: "srcacct"
  ssh_pass: "s3cr3t"
  timeout: "10s"
dest:
  ip: "203.0.113.20"
  port: 22
  ssh_user: "destacct"
  ssh_pass: "d3st"
  timeout: "12s"
`
	cfg, err := Load(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Src.IP != "203.0.113.10" || cfg.Src.Port != 22 {
		t.Errorf("src host mismatch: %+v", cfg.Src)
	}
	if cfg.Src.Timeout != 10*time.Second {
		t.Errorf("src timeout = %v, want 10s", cfg.Src.Timeout)
	}
	if !cfg.DestConfigured() {
		t.Error("DestConfigured() = false, want true")
	}
	if cfg.Dest.SSHUser != "destacct" {
		t.Errorf("dest user = %q", cfg.Dest.SSHUser)
	}
}

func TestLoadDatabasesSection(t *testing.T) {
	const yaml = `
src:
  ip: "10.0.0.1"
  port: 22
  ssh_user: "u"
  ssh_pass: "p"
  timeout: "5s"
databases:
  - name: "srcuser_orphan"
    user: "srcuser_special"
    password: "fromyaml"
  - name: "srcuser_nopw"
`
	cfg, err := Load(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ov := cfg.DBOverrides()
	if len(ov) != 2 {
		t.Fatalf("expected 2 overrides, got %d", len(ov))
	}
	if ov["srcuser_orphan"].Password != "fromyaml" || ov["srcuser_orphan"].User != "srcuser_special" {
		t.Errorf("orphan override wrong: %+v", ov["srcuser_orphan"])
	}
	// Partial entry (name only) is allowed; user/password empty.
	if ov["srcuser_nopw"].Password != "" {
		t.Errorf("expected empty password for name-only entry, got %+v", ov["srcuser_nopw"])
	}
}

// An override with no name overrides nothing and was silently dropped; Load must
// reject it so the operator notices the mistake.
func TestLoadDatabasesEmptyNameFailsLoudly(t *testing.T) {
	const yaml = `
src:
  ip: "10.0.0.1"
  port: 22
  ssh_user: "u"
  ssh_pass: "p"
  timeout: "5s"
databases:
  - user: "someuser"
    password: "x"
`
	if _, err := Load(writeTemp(t, yaml)); err == nil {
		t.Error("Load with a nameless databases entry = nil error, want error")
	}
}

// Two entries naming the same database are ambiguous (the second silently shadowed
// the first); Load must reject the duplicate.
func TestLoadDatabasesDuplicateNameFailsLoudly(t *testing.T) {
	const yaml = `
src:
  ip: "10.0.0.1"
  port: 22
  ssh_user: "u"
  ssh_pass: "p"
  timeout: "5s"
databases:
  - name: "srcuser_db"
    password: "first"
  - name: "srcuser_db"
    password: "second"
`
	if _, err := Load(writeTemp(t, yaml)); err == nil {
		t.Error("Load with duplicate databases names = nil error, want error")
	}
}

func TestLoadNoDatabasesSectionYieldsEmptyOverrides(t *testing.T) {
	const yaml = `
src:
  ip: "10.0.0.1"
  port: 22
  ssh_user: "u"
  ssh_pass: "p"
  timeout: "5s"
`
	cfg, err := Load(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.DBOverrides(); got == nil || len(got) != 0 {
		t.Errorf("DBOverrides() with no section = %v, want empty non-nil map", got)
	}
}

func TestLoadRejectsAdditionalYAMLDocument(t *testing.T) {
	const yaml = `
src:
  ip: "10.0.0.1"
  port: 22
  ssh_user: "u"
  ssh_pass: "p"
  timeout: "5s"
---
src:
  ip: "10.0.0.2"
  port: 22
  ssh_user: "ignored"
  ssh_pass: "ignored"
  timeout: "5s"
`
	_, err := Load(writeTemp(t, yaml))
	if err == nil {
		t.Fatal("Load with an additional YAML document = nil error, want error")
	}
	if !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("Load error = %v, want multiple YAML documents", err)
	}
}

func TestLoadAllowsSingleYAMLDocumentTrailingNoise(t *testing.T) {
	const base = `
src:
  ip: "10.0.0.1"
  port: 22
  ssh_user: "u"
  ssh_pass: "p"
  timeout: "5s"
`
	cases := map[string]string{
		"whitespace":            "\n\n",
		"trailing comment":      "\n# deployment marker\n",
		"trailing end marker":   "...\n",
		"end marker comment":    "...\n# deployment marker\n",
		"end marker whitespace": "...\n\n",
	}
	for name, suffix := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeTemp(t, base+suffix)); err != nil {
				t.Fatalf("Load with trailing %s: %v", name, err)
			}
		})
	}
}

func TestLoadRejectsAdditionalYAMLDocumentVariants(t *testing.T) {
	const base = `
src:
  ip: "10.0.0.1"
  port: 22
  ssh_user: "u"
  ssh_pass: "p"
  timeout: "5s"
`

	cases := map[string]string{
		"empty document":              "---\n",
		"comment-only document":       "---\n# deployment marker\n",
		"multiple empty documents":    "---\n---\n# deployment marker\n",
		"end then empty document":     "...\n---\n",
		"empty mapping document":      "---\n{}\n",
		"empty sequence document":     "---\n[]\n",
		"explicit null document":      "---\nnull\n",
		"explicit shorthand null doc": "---\n~\n",
		"unknown-field document":      "---\nbogus: 1\n",
	}
	for name, suffix := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeTemp(t, base+suffix))
			if err == nil {
				t.Fatal("Load with an additional YAML document = nil error, want error")
			}
			if !strings.Contains(err.Error(), "multiple YAML documents") {
				t.Fatalf("Load error = %v, want multiple YAML documents", err)
			}
		})
	}
}

func TestLoadReportsMalformedAdditionalYAMLDocument(t *testing.T) {
	cases := map[string]string{
		"second document": `
src:
  ip: "10.0.0.1"
  port: 22
  ssh_user: "u"
  ssh_pass: "p"
  timeout: "5s"
---
:
`,
		"third document": `
src:
  ip: "10.0.0.1"
  port: 22
  ssh_user: "u"
  ssh_pass: "p"
  timeout: "5s"
---
{}
---
:
`,
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeTemp(t, yaml))
			if err == nil {
				t.Fatal("Load with malformed additional YAML document = nil error, want error")
			}
			if strings.Contains(err.Error(), "multiple YAML documents") {
				t.Fatalf("Load error = %v, want parse error before multiple-document rejection", err)
			}
		})
	}
}

func TestLoadDestOmitted(t *testing.T) {
	// A config with only the source is valid; the runner then stops after
	// analysis.
	const yaml = `
src:
  ip: "10.0.0.1"
  port: 22
  ssh_user: "u"
  ssh_pass: "p"
  timeout: "5s"
`
	cfg, err := Load(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DestConfigured() {
		t.Error("DestConfigured() = true, want false when dest omitted")
	}
}

func TestLoadInvalid(t *testing.T) {
	cases := map[string]string{
		"missing src ip": `
src: { port: 22, ssh_user: u, ssh_pass: p, timeout: 5s }`,
		"bad port": `
src: { ip: 1.1.1.1, port: 99999, ssh_user: u, ssh_pass: p, timeout: 5s }`,
		"missing timeout": `
src: { ip: 1.1.1.1, port: 22, ssh_user: u, ssh_pass: p }`,
		"unknown field": `
src: { ip: 1.1.1.1, port: 22, ssh_user: u, ssh_pass: p, timeout: 5s, bogus: 1 }`,
		"dest configured but bad port": `
src:  { ip: 1.1.1.1, port: 22, ssh_user: u, ssh_pass: p, timeout: 5s }
dest: { ip: 2.2.2.2, port: 0, ssh_user: u, ssh_pass: p, timeout: 5s }`,
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeTemp(t, yaml)); err == nil {
				t.Errorf("Load(%s) = nil error, want error", name)
			}
		})
	}
}

func TestLoadPartialDestFailsLoudly(t *testing.T) {
	// A destination that is partially filled (a forgotten ssh_pass, a typo'd or
	// missing field) must FAIL with a clear error — not be silently treated as "no
	// destination", which would run source-only with no migration and no warning.
	cases := map[string]string{
		"dest missing ssh_pass": `
src:  { ip: 1.1.1.1, port: 22, ssh_user: u, ssh_pass: p, timeout: 5s }
dest: { ip: 2.2.2.2, port: 22, ssh_user: u, timeout: 5s }`,
		"dest missing ip": `
src:  { ip: 1.1.1.1, port: 22, ssh_user: u, ssh_pass: p, timeout: 5s }
dest: { port: 22, ssh_user: u, ssh_pass: p, timeout: 5s }`,
		"dest only ssh_pass": `
src:  { ip: 1.1.1.1, port: 22, ssh_user: u, ssh_pass: p, timeout: 5s }
dest: { ssh_pass: p }`,
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeTemp(t, yaml)); err == nil {
				t.Errorf("Load(%s) = nil error, want error (a partial destination must fail loudly)", name)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load("/nonexistent/host.yaml"); err == nil {
		t.Error("Load(missing) = nil error, want error")
	}
}
