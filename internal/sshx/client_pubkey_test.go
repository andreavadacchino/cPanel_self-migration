package sshx

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
)

// authCounts records what a test SSH server observed, so a self-heal test can
// prove the REDIAL re-authenticated with the same method as the initial dial.
type authCounts struct {
	pubkeyConns   atomic.Int64 // completed handshakes on a key-only server == successful public-key auths
	passwordTries atomic.Int64 // PasswordCallback invocations — MUST stay 0 for a key-only client
}

// genKeyFile writes a fresh ed25519 private key to a 0600 temp file and returns its
// path plus the matching authorized public key. A non-empty passphrase encrypts it.
func genKeyFile(t *testing.T, passphrase string) (keyPath string, authorized ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	keyPath = filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	apk, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return keyPath, apk
}

// newKeyOnlyServer starts a localhost SSH server that accepts ONLY the authorized
// public key and REJECTS every password (counting the attempt). Each session runs an
// innocuous exec that exits 0. It returns the address and the observed auth counters.
func newKeyOnlyServer(t *testing.T, authorized ssh.PublicKey) (string, *authCounts) {
	t.Helper()
	counts := &authCounts{}
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), authorized.Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("public key not authorized")
		},
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			counts.passwordTries.Add(1)
			return nil, fmt.Errorf("password authentication is disabled")
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return // failed auth: not a completed handshake, not counted
				}
				counts.pubkeyConns.Add(1) // key-only server: a completed handshake == a public-key auth
				defer sconn.Close()
				go ssh.DiscardRequests(reqs)
				for newCh := range chans {
					if newCh.ChannelType() != "session" {
						_ = newCh.Reject(ssh.UnknownChannelType, "only session")
						continue
					}
					ch, creqs, err := newCh.Accept()
					if err != nil {
						return
					}
					go func() {
						for req := range creqs {
							if req.Type == "exec" {
								if req.WantReply {
									_ = req.Reply(true, nil)
								}
								_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{0}))
								_ = ch.Close()
								return
							}
							if req.WantReply {
								_ = req.Reply(false, nil)
							}
						}
					}()
				}
			}()
		}
	}()
	return ln.Addr().String(), counts
}

// keyAuth builds a private-key Authentication from a freshly generated, unencrypted
// key file (helper for the dial/redial tests).
func keyAuth(t *testing.T, keyPath string) Authentication {
	t.Helper()
	a, err := PrivateKeyAuth(keyPath, "")
	if err != nil {
		t.Fatalf("PrivateKeyAuth: %v", err)
	}
	return a
}

// Initial dial with a private key succeeds against a key-only server, opens a
// session that runs an innocuous command to completion, and never triggers the
// server's password path.
func TestDialWithPrivateKeyInitialDial(t *testing.T) {
	keyPath, authorized := genKeyFile(t, "")
	addr, counts := newKeyOnlyServer(t, authorized)

	c, err := Dial(context.Background(), "test", addr, "u", keyAuth(t, keyPath), 5*time.Second, 0, ssh.InsecureIgnoreHostKey()) // #nosec G106 -- test-only in-process server
	if err != nil {
		t.Fatalf("key-authenticated dial failed: %v", err)
	}
	defer c.Close()

	if _, err := c.Run(context.Background(), "echo hello"); err != nil {
		t.Fatalf("innocuous command over key-authenticated session failed: %v", err)
	}
	if got := counts.pubkeyConns.Load(); got != 1 {
		t.Errorf("public-key auths observed = %d, want 1", got)
	}
	if got := counts.passwordTries.Load(); got != 0 {
		t.Errorf("password callback was invoked %d time(s); a key client must never send a password", got)
	}
}

// THE critical gate: after the underlying transport is really closed, the next
// operation self-heals via newSession -> markDead -> heal -> redialLocked and the
// REDIAL must re-authenticate with the SAME private key — never a fabricated
// password. Reverting redialLocked to ssh.Password makes this fail (the key-only
// server rejects the password: heal fails, and passwordTries > 0).
func TestPrivateKeyRedialSelfHeal(t *testing.T) {
	keyPath, authorized := genKeyFile(t, "")
	addr, counts := newKeyOnlyServer(t, authorized)
	withDialKnobs(t, 3, 0) // bound the redial, no backoff

	c, err := Dial(context.Background(), "test", addr, "u", keyAuth(t, keyPath), 5*time.Second, 0, ssh.InsecureIgnoreHostKey()) // #nosec G106 -- test-only in-process server
	if err != nil {
		t.Fatalf("initial key dial: %v", err)
	}
	defer c.Close()

	// First session succeeds on the initial connection.
	if _, err := c.Run(context.Background(), "echo one"); err != nil {
		t.Fatalf("first session: %v", err)
	}

	// Force the underlying transport closed WITHOUT marking the client dead, so the
	// NEXT newSession observes a dropped transport and drives the full self-heal path.
	cli, _, _ := c.current()
	_ = cli.Close()

	// Second session must self-heal (redial) and succeed.
	if _, err := c.Run(context.Background(), "echo two"); err != nil {
		t.Fatalf("second session must self-heal via a key redial, got %v", err)
	}

	if got := c.redials.Load(); got != 1 {
		t.Errorf("redials = %d, want exactly 1", got)
	}
	if got := counts.pubkeyConns.Load(); got != 2 {
		t.Errorf("public-key auths observed = %d, want 2 (initial dial + redial)", got)
	}
	if got := counts.passwordTries.Load(); got != 0 {
		t.Errorf("password callback invoked %d time(s); the redial must reuse the key, never a password", got)
	}
}

// Password regression via a real transport drop: the same self-heal path must keep
// working for password auth (the new abstraction must not break the existing case).
func TestPasswordRedialSelfHeal(t *testing.T) {
	addr := newCmdServer(t, true, okHandler) // accepts any password
	withDialKnobs(t, 3, 0)

	c, err := Dial(context.Background(), "test", addr, "u", PasswordAuth("p"), 5*time.Second, 0, ssh.InsecureIgnoreHostKey()) // #nosec G106 -- test-only in-process server
	if err != nil {
		t.Fatalf("initial password dial: %v", err)
	}
	defer c.Close()

	if _, err := c.Run(context.Background(), "echo one"); err != nil {
		t.Fatalf("first session: %v", err)
	}
	cli, _, _ := c.current()
	_ = cli.Close() // force a real transport drop
	if _, err := c.Run(context.Background(), "echo two"); err != nil {
		t.Fatalf("password self-heal failed: %v", err)
	}
	if got := c.redials.Load(); got != 1 {
		t.Errorf("redials = %d, want 1", got)
	}
}

// Mixed methods via DialBoth: source authenticates by private key, destination by
// password. Proves per-host auth is independent and both use the same host-key path.
func TestDialBothMixedMethods(t *testing.T) {
	keyPath, authorized := genKeyFile(t, "")
	srcAddr, srcCounts := newKeyOnlyServer(t, authorized)
	destAddr := newCmdServer(t, true, okHandler) // password-accepting

	kh := filepath.Join(t.TempDir(), "known_hosts")
	// AcceptNewHostKey is the SAME callback used for src and dest inside DialBoth.
	srcHost := cfgToHost(t, srcAddr)
	srcHost.SSHPass = ""
	srcHost.SSHKeyPath = keyPath
	destHost := cfgToHost(t, destAddr) // keeps SSHPass "p"

	pool, err := DialBoth(context.Background(), config.Config{Src: srcHost, Dest: destHost}, kh)
	if err != nil {
		t.Fatalf("DialBoth mixed methods: %v", err)
	}
	defer pool.Close()

	if _, err := pool.Src.Run(context.Background(), "echo"); err != nil {
		t.Fatalf("source (key) run: %v", err)
	}
	if _, err := pool.Dest.Run(context.Background(), "echo"); err != nil {
		t.Fatalf("dest (password) run: %v", err)
	}
	if got := srcCounts.pubkeyConns.Load(); got < 1 {
		t.Errorf("source public-key auths = %d, want >= 1", got)
	}
	if got := srcCounts.passwordTries.Load(); got != 0 {
		t.Errorf("source password tries = %d, want 0 (source is key-only)", got)
	}
}
