package sshx_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
	"golang.org/x/crypto/ssh"
)

// signalClosedServer starts a minimal localhost SSH server that completes the
// handshake (so ssh.Dial succeeds) and then signals on `closed` when the client
// connection is torn down — used to wait DETERMINISTICALLY for the drain to close
// an orphaned client instead of guessing with a fixed sleep.
func signalClosedServer(t *testing.T) (addr string, closed <-chan struct{}) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil },
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	done := make(chan struct{}, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(reqs)
		go func() {
			for nc := range chans {
				_ = nc.Reject(ssh.Prohibited, "")
			}
		}()
		_ = sconn.Wait() // returns when the connection closes (the drain closing the orphan)
		select {
		case done <- struct{}{}:
		default:
		}
	}()
	return ln.Addr().String(), done
}

// TestDialCancelledCtxDrains guards the no-leak guarantee on a cancelled dial. The
// own-the-conn dial now honors the context at the TCP layer, so a pre-cancelled
// context fails BEFORE any SSH connection is established (no orphan to leak) and the
// error wraps context.Canceled. The complementary case — an abort that races a
// connection that did get established — is covered deterministically by the internal
// TestDialAbortClosesOwnedConn, which asserts the owned conn is closed. Run under
// -race to confirm the dialer/watchdog goroutines are synchronized.
func TestDialCancelledCtxDrains(t *testing.T) {
	addr, _ := signalClosedServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, err := sshx.Dial(ctx, "test", addr, "u", sshx.PasswordAuth("p"), 5*time.Second, 0, ssh.InsecureIgnoreHostKey())
	if err == nil || c != nil {
		t.Fatalf("cancelled ctx: want (nil, error), got (%v, %v)", c, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled dial must wrap context.Canceled, got %v", err)
	}
}

// TestBridgeSurfacesBothTarErrors guards the fix where, on a clean relay with BOTH
// tars exiting non-zero, only the source error was returned (dest failure dropped).
func TestBridgeSurfacesBothTarErrors(t *testing.T) {
	sshtest.RequireTools(t, "bash", "cat")
	home := t.TempDir()
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer dst.Close()
	err := sshx.BridgeProgress(context.Background(), src,
		"printf data; echo SRCFAIL >&2; exit 3", nil, nil, dst,
		"cat >/dev/null; echo DESTFAIL >&2; exit 4", nil, nil)
	if err == nil {
		t.Fatal("expected an error when both tars exit non-zero")
	}
	if msg := err.Error(); !strings.Contains(msg, "SRCFAIL") || !strings.Contains(msg, "DESTFAIL") {
		t.Errorf("bridge error must surface BOTH sides: %q", msg)
	}
}
