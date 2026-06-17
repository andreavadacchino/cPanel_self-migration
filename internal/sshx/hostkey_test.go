package sshx

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh/knownhosts"
)

// An unknown host is trusted on first use and recorded; a missing parent dir is
// created.
func TestAcceptNewHostKeyTOFUCreatesFileAndAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "known_hosts")
	cb, err := AcceptNewHostKey(path)
	if err != nil {
		t.Fatalf("AcceptNewHostKey: %v", err)
	}
	if err := cb("127.0.0.1:22", testAddr, testPubKey(t)); err != nil {
		t.Fatalf("TOFU accept: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || len(data) == 0 {
		t.Fatalf("known_hosts should be written: err=%v len=%d", err, len(data))
	}
}

// A remote address distinct from the hostname is recorded too (appendKnownHost's
// second-address branch).
func TestAcceptNewHostKeyTOFUDistinctRemote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	cb, err := AcceptNewHostKey(path)
	if err != nil {
		t.Fatal(err)
	}
	remote := &net.TCPAddr{IP: net.ParseIP("203.0.113.5"), Port: 22}
	if err := cb("myhost:22", remote, testPubKey(t)); err != nil {
		t.Fatalf("TOFU accept (distinct remote): %v", err)
	}
}

// appendKnownHost tolerates a nil remote (defensive: the real ssh callback always
// passes a non-nil one). Called directly because knownhosts.New panics on a nil
// addr, so this branch is unreachable through the callback.
func TestAppendKnownHostNilRemote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendKnownHost(path, "127.0.0.1:22", nil, testPubKey(t)); err != nil {
		t.Fatalf("appendKnownHost(nil remote): %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || len(data) == 0 {
		t.Fatalf("appendKnownHost should write a line: err=%v len=%d", err, len(data))
	}
}

// A host already recorded with this exact key is accepted.
func TestAcceptNewHostKeyMatchesRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	key := testPubKey(t)
	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{"127.0.0.1:22"}, key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cb, err := AcceptNewHostKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cb("127.0.0.1:22", testAddr, key); err != nil {
		t.Errorf("recorded key should match: %v", err)
	}
}

// A host recorded with one key must REJECT a different key (the security win).
func TestAcceptNewHostKeyMismatchRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	recorded, presented := testPubKey(t), testPubKey(t) // two distinct, freshly-generated keys
	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{"127.0.0.1:22"}, recorded)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cb, err := AcceptNewHostKey(path)
	if err != nil {
		t.Fatal(err)
	}
	err = cb("127.0.0.1:22", testAddr, presented) // a different key
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("changed host key must be refused, got %v", err)
	}
}
