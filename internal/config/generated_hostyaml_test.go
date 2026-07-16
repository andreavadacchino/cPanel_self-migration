package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The Go half of the host.yaml contract with the Migration Platform's SSH
// workspace builder.
//
// The fixtures in testdata/generated_hostyaml are not hand-written: they are the
// byte-for-byte output of the platform's render_host_config, and the Python side
// (app/tests/test_ssh_workspace_contract.py) fails if the builder ever stops
// producing exactly these bytes. Together the two halves prove the real chain —
// PyYAML accepting its own output proves nothing about this parser, which is the
// only authority on what the engine consumes.
//
// This never dials, never reads a key file, and never starts a migration. Load
// does no I/O beyond reading the config itself, so it is a pure offline
// validator; internal/webui/webui.go:352-370 already relies on that same property.
//
// The values are inert placeholders, not credentials.

const fixtureDir = "testdata/generated_hostyaml"

func loadFixture(t *testing.T, name string) Config {
	t.Helper()
	cfg, err := Load(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("the platform-generated %s must parse, got: %v", name, err)
	}
	return cfg
}

func TestGeneratedHostYAMLPasswordAuth(t *testing.T) {
	cfg := loadFixture(t, "password.yaml")

	if cfg.Src.IP != "203.0.113.10" || cfg.Src.Port != 22 {
		t.Fatalf("coordinates not parsed: %+v", cfg.Src)
	}
	if cfg.Src.SSHUser != "srcuser" {
		t.Fatalf("ssh_user = %q", cfg.Src.SSHUser)
	}
	if cfg.Src.SSHPass == "" {
		t.Fatal("ssh_pass did not survive the round trip")
	}
	if cfg.Src.SSHKeyPath != "" {
		t.Fatalf("a password config must carry no key path, got %q", cfg.Src.SSHKeyPath)
	}
	// The generator emits `timeout: 30s` unquoted; yaml.v3 must still hand it to
	// time.Duration. A wrong shape here parses as 0 and validate() rejects it.
	if cfg.Src.Timeout != 30*time.Second {
		t.Fatalf("timeout = %v, want 30s", cfg.Src.Timeout)
	}
	if cfg.Src.AuthMethod() != "password" {
		t.Fatalf("AuthMethod() = %q", cfg.Src.AuthMethod())
	}
	if cfg.DestConfigured() {
		t.Fatal("a source-only config must not look dest-configured")
	}
}

func TestGeneratedHostYAMLPrivateKeyAuth(t *testing.T) {
	cfg := loadFixture(t, "private_key.yaml")

	if cfg.Src.SSHKeyPath != "/run/migration-ssh-fixture/source_key" {
		t.Fatalf("ssh_key_path = %q", cfg.Src.SSHKeyPath)
	}
	if cfg.Src.SSHPass != "" {
		t.Fatalf("a key config must carry no password, got a non-empty ssh_pass")
	}
	if cfg.Src.SSHKeyPassphrase != "" {
		t.Fatalf("no passphrase was generated, got a non-empty one")
	}
	if cfg.Src.AuthMethod() != "private_key" {
		t.Fatalf("AuthMethod() = %q", cfg.Src.AuthMethod())
	}
	// The builder writes an absolute path on purpose: a relative one would
	// resolve against host.yaml's directory, and the engine does no ~ expansion.
	if !filepath.IsAbs(cfg.Src.SSHKeyPath) {
		t.Fatal("the generated key path must be absolute")
	}
}

func TestGeneratedHostYAMLPrivateKeyWithPassphrase(t *testing.T) {
	cfg := loadFixture(t, "private_key_passphrase.yaml")

	if cfg.Src.SSHKeyPassphrase == "" {
		t.Fatal("ssh_key_passphrase did not survive the round trip")
	}
	if cfg.Src.SSHKeyPath == "" {
		t.Fatal("a passphrase without a key path would be rejected by validate")
	}
}

func TestGeneratedHostYAMLNonStandardPort(t *testing.T) {
	cfg := loadFixture(t, "nonstandard_port.yaml")

	if cfg.Src.Port != 2222 {
		t.Fatalf("port = %d, want 2222", cfg.Src.Port)
	}
}

func TestGeneratedHostYAMLSrcAndDest(t *testing.T) {
	cfg := loadFixture(t, "src_and_dest.yaml")

	if !cfg.DestConfigured() {
		t.Fatal("a fully populated dest must be seen as configured")
	}
	if cfg.Dest.IP != "198.51.100.20" || cfg.Dest.Port != 2222 {
		t.Fatalf("dest coordinates not parsed: %+v", cfg.Dest)
	}
	if cfg.Dest.SSHUser != "destuser" {
		t.Fatalf("dest ssh_user = %q", cfg.Dest.SSHUser)
	}
	// Mixed methods across the two hosts are legal and must stay legal: the
	// platform resolves each endpoint's credential independently.
	if cfg.Src.AuthMethod() != "private_key" || cfg.Dest.AuthMethod() != "password" {
		t.Fatalf("mixed auth methods not preserved: src=%q dest=%q",
			cfg.Src.AuthMethod(), cfg.Dest.AuthMethod())
	}
}

// TestGeneratedHostYAMLEveryFixtureIsCovered fails when a fixture is added to the
// directory without a test, mirroring the execution-contract corpus's own rule.
func TestGeneratedHostYAMLEveryFixtureIsCovered(t *testing.T) {
	entries, err := os.ReadDir(fixtureDir)
	if err != nil {
		t.Fatalf("read fixture dir: %v", err)
	}
	known := map[string]bool{
		"password.yaml":               true,
		"private_key.yaml":            true,
		"private_key_passphrase.yaml": true,
		"nonstandard_port.yaml":       true,
		"src_and_dest.yaml":           true,
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !known[e.Name()] {
			t.Errorf("fixture %s exists on disk but no test loads it", e.Name())
		}
	}
	for name := range known {
		if _, err := os.Stat(filepath.Join(fixtureDir, name)); err != nil {
			t.Errorf("declared fixture %s is missing: %v", name, err)
		}
	}
}

// TestGeneratedHostYAMLFieldsAreLoadBearing removes one generated field at a time
// and requires Load to refuse the result. Without this, a field the platform
// emits could be decorative — or, worse, silently defaulted — and we would not
// know which of the two the parser actually does.
func TestGeneratedHostYAMLFieldsAreLoadBearing(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtureDir, "password.yaml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	for _, field := range []string{"ip:", "port:", "ssh_user:", "ssh_pass:", "timeout:"} {
		t.Run(strings.TrimSuffix(field, ":"), func(t *testing.T) {
			var kept []string
			for _, line := range strings.Split(string(raw), "\n") {
				if !strings.Contains(line, field) {
					kept = append(kept, line)
				}
			}
			p := writeTemp(t, strings.Join(kept, "\n"))
			if _, err := Load(p); err == nil {
				t.Fatalf("Load accepted a config with %s removed: the platform "+
					"must keep emitting it", field)
			}
		})
	}
}

// TestGeneratedHostYAMLRejectsAnExtraField pins the strictness the builder relies
// on: KnownFields(true) means an added key is a hard error, not a warning.
func TestGeneratedHostYAMLRejectsAnExtraField(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtureDir, "password.yaml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	p := writeTemp(t, string(raw)+"  known_hosts: /run/migration-ssh-fixture/.ssh/known_hosts\n")
	if _, err := Load(p); err == nil {
		t.Fatal("Load accepted an unknown field: the engine has no known_hosts " +
			"config field, and the workspace builder must not pretend it does")
	}
}
