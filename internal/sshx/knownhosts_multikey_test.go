package sshx

// Test-only characterization of golang.org/x/crypto/ssh/knownhosts, offline:
// no socket, no DNS, no subprocess. It pins the fact that drives the Python
// workspace builder's collision rule (adapters/ssh_workspace.py):
//
// checkAddr walks every line whose address matches and returns nil as soon as
// ONE known key equals the presented key. Two records for the same normalized
// address carrying two different keys therefore authorize EITHER key — the
// file's trust is the union of the pins, not their intersection. A builder
// that writes both entries has silently widened one endpoint's allowlist with
// the other endpoint's key, which is why the Python side refuses that
// configuration before writing anything (WorkspaceSecurityError).

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh/knownhosts"
)

// Two entries, same address, two different keys: the real parser accepts both.
func TestKnownHostsSameAddressTwoKeysAuthorizesEither(t *testing.T) {
	k1, k2 := testPubKey(t), testPubKey(t) // distinct, freshly generated
	path := filepath.Join(t.TempDir(), "known_hosts")
	data := knownhosts.Line([]string{"shared.example.com:22"}, k1) + "\n" +
		knownhosts.Line([]string{"shared.example.com:22"}, k2) + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		t.Fatalf("knownhosts.New: %v", err)
	}
	if err := cb("shared.example.com:22", testAddr, k1); err != nil {
		t.Errorf("K1 should satisfy the lookup: %v", err)
	}
	if err := cb("shared.example.com:22", testAddr, k2); err != nil {
		t.Errorf("K2 satisfies the lookup too — the union the builder must "+
			"never produce: %v", err)
	}
}

// One entry per address — the invariant the Python builder now enforces — and
// a different key is a hard mismatch (KeyError with a non-empty Want), never a
// second acceptable identity.
func TestKnownHostsSingleEntryRejectsADifferentKey(t *testing.T) {
	pinned, presented := testPubKey(t), testPubKey(t)
	path := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{"shared.example.com:22"}, pinned) + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		t.Fatalf("knownhosts.New: %v", err)
	}
	if err := cb("shared.example.com:22", testAddr, pinned); err != nil {
		t.Errorf("the pinned key must be accepted: %v", err)
	}
	err = cb("shared.example.com:22", testAddr, presented)
	var keyErr *knownhosts.KeyError
	if !errors.As(err, &keyErr) || len(keyErr.Want) == 0 {
		t.Errorf("a different key must be a mismatch (KeyError with Want), got %v", err)
	}
}
