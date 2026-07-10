package sshx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A valid unencrypted key parses into a private_key Authentication.
func TestPrivateKeyAuthUnencrypted(t *testing.T) {
	keyPath, _ := genKeyFile(t, "")
	a, err := PrivateKeyAuth(keyPath, "")
	if err != nil {
		t.Fatalf("unencrypted key: %v", err)
	}
	if a.Method() != "private_key" {
		t.Errorf("Method() = %q, want private_key", a.Method())
	}
	if len(a.authMethods()) != 1 {
		t.Errorf("authMethods len = %d, want 1", len(a.authMethods()))
	}
}

// A valid encrypted key parses with the correct passphrase.
func TestPrivateKeyAuthEncryptedCorrectPassphrase(t *testing.T) {
	keyPath, _ := genKeyFile(t, "correct horse")
	if _, err := PrivateKeyAuth(keyPath, "correct horse"); err != nil {
		t.Fatalf("encrypted key with correct passphrase: %v", err)
	}
}

// An encrypted key with NO passphrase gives an actionable error and never leaks
// the key bytes.
func TestPrivateKeyAuthEncryptedMissingPassphrase(t *testing.T) {
	keyPath, _ := genKeyFile(t, "s3cr3t-phrase")
	_, err := PrivateKeyAuth(keyPath, "")
	if err == nil {
		t.Fatal("an encrypted key with no passphrase must error")
	}
	if !strings.Contains(err.Error(), "encrypted") {
		t.Errorf("error should mention the key is encrypted, got %v", err)
	}
	assertErrClean(t, err, "s3cr3t-phrase")
}

// A wrong passphrase errors with a message DISTINCT from a malformed-key error
// (so an operator can tell "bad passphrase" from "unsupported key format"), and the
// error must NOT contain the passphrase.
func TestPrivateKeyAuthWrongPassphrase(t *testing.T) {
	keyPath, _ := genKeyFile(t, "the-real-passphrase")
	_, err := PrivateKeyAuth(keyPath, "wrong-guess-123")
	if err == nil {
		t.Fatal("a wrong passphrase must error")
	}
	if !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("wrong-passphrase error should name the passphrase as the cause, got %v", err)
	}
	assertErrClean(t, err, "wrong-guess-123")
	assertErrClean(t, err, "the-real-passphrase")

	// The wrong-passphrase message must differ from a malformed-key message: pass a
	// passphrase to garbage bytes and confirm the two error strings are not identical.
	bogus := filepath.Join(t.TempDir(), "bogus")
	if werr := os.WriteFile(bogus, []byte("not a key"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	_, malformedErr := PrivateKeyAuth(bogus, "any-passphrase")
	if malformedErr == nil {
		t.Fatal("a malformed key must error")
	}
	if err.Error() == malformedErr.Error() {
		t.Errorf("wrong-passphrase and malformed-key errors must differ, both were %q", err.Error())
	}
}

// Garbage bytes are not a valid key; the error must not echo the file content.
func TestPrivateKeyAuthInvalidKey(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "bogus")
	const sentinel = "NOT-A-REAL-PEM-KEY-SENTINEL"
	if err := os.WriteFile(keyPath, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := PrivateKeyAuth(keyPath, "")
	if err == nil {
		t.Fatal("a malformed key must error")
	}
	assertErrClean(t, err, sentinel)
}

// A missing key file errors contextually.
func TestPrivateKeyAuthMissingFile(t *testing.T) {
	_, err := PrivateKeyAuth(filepath.Join(t.TempDir(), "does-not-exist"), "")
	if err == nil {
		t.Fatal("a missing key file must error")
	}
	if !strings.Contains(err.Error(), "read private key file") {
		t.Errorf("error should name the read failure, got %v", err)
	}
}

// An unreadable key file errors (skipped where perms don't restrict, e.g. root).
func TestPrivateKeyAuthUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: 0000 perms do not restrict reads")
	}
	keyPath, _ := genKeyFile(t, "")
	if err := os.Chmod(keyPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(keyPath, 0o600) })
	if _, err := PrivateKeyAuth(keyPath, ""); err == nil {
		t.Fatal("an unreadable key file must error")
	}
}

// PasswordAuth builds a password Authentication.
func TestPasswordAuthMethod(t *testing.T) {
	if a := PasswordAuth("x"); a.Method() != "password" {
		t.Errorf("Method() = %q, want password", a.Method())
	}
}

// authMethods returns a COPY: mutating the returned slice must not affect a later
// build (guards against a caller clobbering the shared auth recipe).
func TestAuthMethodsReturnsCopy(t *testing.T) {
	a := PasswordAuth("x")
	got := a.authMethods()
	got[0] = nil // clobber the copy
	if a.authMethods()[0] == nil {
		t.Error("authMethods() must return a fresh copy; the stored slice was mutated")
	}
}

// assertErrClean fails if err leaks a secret or (when nonEmpty) the key path.
func assertErrClean(t *testing.T, err error, secret string) {
	t.Helper()
	if err == nil {
		return
	}
	if secret != "" && strings.Contains(err.Error(), secret) {
		t.Errorf("error leaks the secret %q: %v", secret, err)
	}
	if strings.Contains(err.Error(), "PRIVATE KEY") {
		t.Errorf("error appears to contain PEM material: %v", err)
	}
}
