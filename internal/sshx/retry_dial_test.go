package sshx

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// authRejectServer starts a localhost SSH server that REJECTS every password, so
// a dial fails with the x/crypto auth sentinel (a deterministic, permanent error).
func authRejectServer(t *testing.T) string {
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
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			return nil, fmt.Errorf("password rejected")
		},
	}
	cfg.AddHostKey(signer)
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
				// The handshake fails on auth; NewServerConn returns an error and we
				// just drop the connection.
				_, _, _, _ = ssh.NewServerConn(conn, cfg)
			}()
		}
	}()
	return ln.Addr().String()
}

// withDialKnobs sets DialRetries and RetryBackoffBase for a test, restoring both on
// cleanup. backoff 0 disables the inter-attempt delay.
func withDialKnobs(t *testing.T, retries int, backoff time.Duration) {
	t.Helper()
	origRetries, origBackoff := DialRetries, RetryBackoffBase
	DialRetries, RetryBackoffBase = retries, backoff
	t.Cleanup(func() { DialRetries, RetryBackoffBase = origRetries, origBackoff })
}

// refusingDialer replaces dialContext with one that always fails with
// ECONNREFUSED (a transient error) and counts attempts. Restores on cleanup.
func refusingDialer(t *testing.T) *int32 {
	t.Helper()
	orig := dialContext
	t.Cleanup(func() { dialContext = orig })
	var calls int32
	dialContext = func(context.Context, string, string) (net.Conn, error) {
		atomic.AddInt32(&calls, 1)
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	}
	return &calls
}

// countingDialer replaces dialContext with one that counts TCP dials and
// delegates to the real dialer, restoring both dialContext and RetryBackoffBase
// (set to 0 for speed) on cleanup. Returns the live call counter.
func countingDialer(t *testing.T) *int32 {
	t.Helper()
	origDial := dialContext
	origBackoff := RetryBackoffBase
	t.Cleanup(func() { dialContext = origDial; RetryBackoffBase = origBackoff })
	RetryBackoffBase = 0
	var calls int32
	dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		atomic.AddInt32(&calls, 1)
		return origDial(ctx, network, address)
	}
	return &calls
}

// S1-11: a transient connect failure (connection refused) is retried, and a later
// attempt succeeds.
func TestDialRetriesTransientThenSucceeds(t *testing.T) {
	addr := newCmdServer(t, true, okHandler)

	origDial := dialContext
	origBackoff := RetryBackoffBase
	t.Cleanup(func() { dialContext = origDial; RetryBackoffBase = origBackoff })
	RetryBackoffBase = 0

	var calls int32
	dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ECONNREFUSED}
		}
		return origDial(ctx, network, address)
	}

	c, err := Dial(context.Background(), "test", addr, "u", "p", 5*time.Second, 0, ssh.InsecureIgnoreHostKey())
	if err != nil {
		t.Fatalf("dial must recover on the 2nd attempt: %v", err)
	}
	defer c.Close()
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("dialContext called %d times, want 2 (1 transient failure + 1 success)", got)
	}
}

// S1-11 + classifier: an authentication rejection is permanent and must NOT be
// retried (a single dial attempt).
func TestDialDoesNotRetryAuthFailure(t *testing.T) {
	addr := authRejectServer(t)
	calls := countingDialer(t)

	_, err := Dial(context.Background(), "test", addr, "u", "p", 5*time.Second, 0, ssh.InsecureIgnoreHostKey())
	if err == nil {
		t.Fatal("dial against an auth-rejecting server must fail")
	}
	if !strings.Contains(err.Error(), "unable to authenticate") {
		t.Errorf("error should carry the auth failure, got %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("an auth failure must NOT be retried: %d dial attempts, want 1", got)
	}
}

// S1-11 + classifier end-to-end: a CHANGED host key (real AcceptNewHostKey against
// a pre-seeded different key) must wrap ErrHostKeyChanged through NewClientConn's
// wrap and must NOT be retried.
func TestDialDoesNotRetryHostKeyMismatch(t *testing.T) {
	addr := newCmdServer(t, true, okHandler)
	kh := filepath.Join(t.TempDir(), "known_hosts")
	// Pre-seed a DIFFERENT key for this host, so the real server key mismatches.
	line := knownhosts.Line([]string{knownhosts.Normalize(addr)}, testPubKey(t))
	if err := os.WriteFile(kh, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cb, err := AcceptNewHostKey(kh)
	if err != nil {
		t.Fatal(err)
	}
	calls := countingDialer(t)

	_, err = Dial(context.Background(), "test", addr, "u", "p", 5*time.Second, 0, cb)
	if err == nil {
		t.Fatal("a changed host key must reject the connection")
	}
	if !errors.Is(err, ErrHostKeyChanged) {
		t.Errorf("error must wrap ErrHostKeyChanged for the classifier, got %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("a changed host key must NOT be retried: %d dial attempts, want 1", got)
	}
}

// S1-11 exhaustion: when every attempt fails transiently, dialWithRetry makes
// exactly DialRetries attempts and returns the last (transient) cause.
func TestDialRetriesExhaustsAndReturnsLastError(t *testing.T) {
	withDialKnobs(t, 3, 0)
	calls := refusingDialer(t)

	_, err := Dial(context.Background(), "test", "127.0.0.1:1", "u", "p", 5*time.Second, 0, ssh.InsecureIgnoreHostKey())
	if err == nil {
		t.Fatal("dial must fail after exhausting all retries")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Errorf("exhaustion error must wrap the transient cause, got %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 3 {
		t.Errorf("dialContext called %d times, want DialRetries=3", got)
	}
}

// S1-11 single-attempt equivalence: DialRetries=1 makes exactly one attempt.
func TestDialRetriesOneIsSingleAttempt(t *testing.T) {
	withDialKnobs(t, 1, 0)
	calls := refusingDialer(t)

	if _, err := Dial(context.Background(), "test", "127.0.0.1:1", "u", "p", 5*time.Second, 0, ssh.InsecureIgnoreHostKey()); err == nil {
		t.Fatal("dial must fail")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("DialRetries=1 must make exactly one attempt, got %d", got)
	}
}

// A misconfigured DialRetries <= 0 must be clamped to one attempt — never skip the
// dial loop entirely and return an error that wraps a nil cause.
func TestDialRetriesZeroClampedToOne(t *testing.T) {
	withDialKnobs(t, 0, 0)
	calls := refusingDialer(t)

	_, err := Dial(context.Background(), "test", "127.0.0.1:1", "u", "p", 5*time.Second, 0, ssh.InsecureIgnoreHostKey())
	if err == nil {
		t.Fatal("dial must fail")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("DialRetries=0 must be clamped to exactly one attempt, got %d", got)
	}
	if strings.Contains(err.Error(), "<nil>") {
		t.Errorf("error wraps a nil cause (the pre-clamp wrapped-nil path): %v", err)
	}
}

// S1-11: a context cancelled during the inter-attempt backoff returns promptly
// with context.Canceled and does not start the next attempt.
func TestDialRetryHonorsCtxCancelDuringBackoff(t *testing.T) {
	withDialKnobs(t, 3, 2*time.Second) // slow backoff so the cancel lands mid-wait
	orig := dialContext
	t.Cleanup(func() { dialContext = orig })
	ctx, cancel := context.WithCancel(context.Background())
	var calls int32
	dialContext = func(context.Context, string, string) (net.Conn, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			// Cancel shortly after the first failed attempt, during its backoff.
			go func() { time.Sleep(20 * time.Millisecond); cancel() }()
		}
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	}

	start := time.Now()
	_, err := Dial(ctx, "test", "127.0.0.1:1", "u", "p", 5*time.Second, 0, ssh.InsecureIgnoreHostKey())
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel during backoff must return context.Canceled, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("cancel during backoff must return promptly, took %v", elapsed)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("must not start a 2nd attempt after a cancel during backoff, got %d attempts", got)
	}
}
