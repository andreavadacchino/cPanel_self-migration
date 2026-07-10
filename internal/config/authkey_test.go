package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The SSH auth model is "exactly one of ssh_pass OR ssh_key_path per host". These
// tests pin every branch of that rule plus the key-path resolution semantics; they
// complement (do not replace) the password-only cases in config_test.go.

// #1 source password-only stays valid (regression: the pre-key behaviour).
func TestLoadSourcePasswordOnly(t *testing.T) {
	const yaml = `
src: { ip: 1.1.1.1, port: 22, ssh_user: u, ssh_pass: p, timeout: 5s }`
	if _, err := Load(writeTemp(t, yaml)); err != nil {
		t.Fatalf("password-only source must load: %v", err)
	}
}

// #2 source key-only valid; #11/#12 path resolution checked separately.
func TestLoadSourceKeyOnly(t *testing.T) {
	const yaml = `
src: { ip: 1.1.1.1, port: 22, ssh_user: u, ssh_key_path: /keys/id_ed25519, timeout: 5s }`
	cfg, err := Load(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("key-only source must load: %v", err)
	}
	if cfg.Src.SSHKeyPath != "/keys/id_ed25519" {
		t.Errorf("src key path = %q, want preserved absolute path", cfg.Src.SSHKeyPath)
	}
	if cfg.Src.AuthMethod() != "private_key" {
		t.Errorf("src AuthMethod() = %q, want private_key", cfg.Src.AuthMethod())
	}
}

// #3 destination key-only is recognised by DestConfigured (must not be tied to
// ssh_pass) and #9 a complete key-only dest is valid.
func TestLoadDestKeyOnlyConfigured(t *testing.T) {
	const yaml = `
src:  { ip: 1.1.1.1, port: 22, ssh_user: u, ssh_pass: p, timeout: 5s }
dest: { ip: 2.2.2.2, port: 22, ssh_user: d, ssh_key_path: /keys/dest, timeout: 5s }`
	cfg, err := Load(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("key-only dest must load: %v", err)
	}
	if !cfg.DestConfigured() {
		t.Error("DestConfigured() = false for a key-only destination, want true")
	}
	if cfg.Dest.AuthMethod() != "private_key" {
		t.Errorf("dest AuthMethod() = %q, want private_key", cfg.Dest.AuthMethod())
	}
}

// #4 password AND key together on one host is rejected (no implicit precedence).
func TestLoadRejectsPasswordAndKeyTogether(t *testing.T) {
	const yaml = `
src: { ip: 1.1.1.1, port: 22, ssh_user: u, ssh_pass: sekritvalue, ssh_key_path: /keys/id, timeout: 5s }`
	_, err := Load(writeTemp(t, yaml))
	if err == nil {
		t.Fatal("a host with BOTH ssh_pass and ssh_key_path must be rejected")
	}
	assertNoSecret(t, err, "sekritvalue")
}

// #5 source with no auth method at all is rejected.
func TestLoadRejectsSourceWithoutAuth(t *testing.T) {
	const yaml = `
src: { ip: 1.1.1.1, port: 22, ssh_user: u, timeout: 5s }`
	if _, err := Load(writeTemp(t, yaml)); err == nil {
		t.Fatal("a host with neither ssh_pass nor ssh_key_path must be rejected")
	}
}

// #6 passphrase without a key path is a misconfiguration.
func TestLoadRejectsPassphraseWithoutKey(t *testing.T) {
	const yaml = `
src: { ip: 1.1.1.1, port: 22, ssh_user: u, ssh_pass: p, ssh_key_passphrase: secretphrase, timeout: 5s }`
	_, err := Load(writeTemp(t, yaml))
	if err == nil {
		t.Fatal("ssh_key_passphrase without ssh_key_path must be rejected")
	}
	assertNoSecret(t, err, "secretphrase")
}

// A passphrase alongside a key path is allowed (encrypted key case) — the file
// itself is not read at parse time.
func TestLoadAllowsPassphraseWithKey(t *testing.T) {
	const yaml = `
src: { ip: 1.1.1.1, port: 22, ssh_user: u, ssh_key_path: /keys/id, ssh_key_passphrase: secretphrase, timeout: 5s }`
	cfg, err := Load(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("key + passphrase must load: %v", err)
	}
	if cfg.Src.SSHKeyPassphrase != "secretphrase" {
		t.Errorf("passphrase not preserved: %q", cfg.Src.SSHKeyPassphrase)
	}
}

// #7 a fully-blank destination stays valid (source-only mode).
func TestLoadDestFullyBlankValid(t *testing.T) {
	const yaml = `
src: { ip: 1.1.1.1, port: 22, ssh_user: u, ssh_key_path: /keys/id, timeout: 5s }`
	cfg, err := Load(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("blank dest must be valid: %v", err)
	}
	if cfg.DestConfigured() {
		t.Error("DestConfigured() = true for a blank dest, want false")
	}
}

// #8 a destination with only a key path but no ip/user is a partial dest -> error.
func TestLoadRejectsPartialKeyOnlyDest(t *testing.T) {
	const yaml = `
src:  { ip: 1.1.1.1, port: 22, ssh_user: u, ssh_pass: p, timeout: 5s }
dest: { ssh_key_path: /keys/dest }`
	if _, err := Load(writeTemp(t, yaml)); err == nil {
		t.Fatal("a dest with only ssh_key_path (no ip/user) must fail loudly")
	}
}

// #10 source and destination may use DIFFERENT methods.
func TestLoadMixedMethodsValid(t *testing.T) {
	const yaml = `
src:  { ip: 1.1.1.1, port: 22, ssh_user: u, ssh_key_path: /keys/src, timeout: 5s }
dest: { ip: 2.2.2.2, port: 22, ssh_user: d, ssh_pass: destpw, timeout: 5s }`
	cfg, err := Load(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("mixed methods must load: %v", err)
	}
	if cfg.Src.AuthMethod() != "private_key" || cfg.Dest.AuthMethod() != "password" {
		t.Errorf("methods = src:%s dest:%s, want private_key/password", cfg.Src.AuthMethod(), cfg.Dest.AuthMethod())
	}
}

// #11 a RELATIVE key path resolves against the host.yaml directory, independent of
// the process working directory.
func TestLoadRelativeKeyPathResolvedAgainstConfigDir(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "host.yaml")
	const yaml = `
src: { ip: 1.1.1.1, port: 22, ssh_user: u, ssh_key_path: keys/id_ed25519, timeout: 5s }`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	// Run from a DIFFERENT working directory to prove CWD-independence.
	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(dir, "keys", "id_ed25519")
	if cfg.Src.SSHKeyPath != want {
		t.Errorf("relative key path resolved to %q, want %q (relative to host.yaml dir, not CWD)", cfg.Src.SSHKeyPath, want)
	}
}

// #12 an ABSOLUTE key path is preserved verbatim.
func TestLoadAbsoluteKeyPathPreserved(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "id_ed25519")
	yaml := "src: { ip: 1.1.1.1, port: 22, ssh_user: u, ssh_key_path: " + abs + ", timeout: 5s }"
	cfg, err := Load(writeTemp(t, yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Src.SSHKeyPath != abs {
		t.Errorf("absolute key path = %q, want %q (unchanged)", cfg.Src.SSHKeyPath, abs)
	}
}

// #15 no error surfaced by the auth validation may contain a password or passphrase.
func assertNoSecret(t *testing.T, err error, secret string) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), secret) {
		t.Errorf("error message leaks the secret %q: %v", secret, err)
	}
}
