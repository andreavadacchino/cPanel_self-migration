package sshx

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// trackConn records whether Close was called on the wrapped connection.
type trackConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *trackConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

// silentBannerServer accepts TCP connections but NEVER sends an SSH banner, so a
// dial wedges in x/crypto's version exchange (which has no read deadline). It
// drains whatever the client writes and signals on closed when the accepted
// connection is torn down — i.e. when the dial watchdog Closes the owned conn —
// so a leak test can wait deterministically instead of sleeping.
func silentBannerServer(t *testing.T) (addr string, closed <-chan struct{}) {
	t.Helper()
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
		// Read (and discard) the client's version line but never reply, so the
		// client blocks in readVersion. The Read returns once the client closes the
		// connection (the watchdog), which is our deterministic "closed" signal.
		buf := make([]byte, 256)
		for {
			if _, err := conn.Read(buf); err != nil {
				break
			}
		}
		_ = conn.Close()
		select {
		case done <- struct{}{}:
		default:
		}
	}()
	return ln.Addr().String(), done
}

// S1-01: a peer that accepts TCP but never speaks SSH must not wedge the dial
// past the timeout. The old code passed timeout only to net.DialTimeout, so the
// banner read blocked forever; now timeout bounds the whole handshake.
func TestDialSilentBannerAbortsWithinTimeout(t *testing.T) {
	addr, _ := silentBannerServer(t)
	start := time.Now()
	err := withTimeout(t, deadlockTimeout, func() error {
		_, e := Dial(context.Background(), "test", addr, "u", "p", 300*time.Millisecond, 0, ssh.InsecureIgnoreHostKey())
		return e
	})
	if err == nil {
		t.Fatal("dial against a silent-banner peer must fail, not hang")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("dial took %v, want it bounded near the 300ms timeout", elapsed)
	}
	// A handshake timeout is NOT a user interrupt: it must carry DeadlineExceeded,
	// not Canceled, so main.go reports exit 1 rather than 130.
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("handshake-timeout error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Errorf("handshake timeout must not classify as context.Canceled: %v", err)
	}
}

// S1-02 leg 1: the owned connection is actually reclaimed after a handshake
// timeout — no leaked goroutine/socket. Run under -race.
func TestDialSilentBannerReclaimsConn(t *testing.T) {
	addr, closed := silentBannerServer(t)
	_, err := Dial(context.Background(), "test", addr, "u", "p", 300*time.Millisecond, 0, ssh.InsecureIgnoreHostKey())
	if err == nil {
		t.Fatal("dial against a silent-banner peer must fail")
	}
	select {
	case <-closed: // the watchdog closed the owned conn
	case <-time.After(3 * time.Second):
		t.Error("owned connection was not reclaimed after the handshake timeout — leak")
	}
}

// S1-02 leg 1: when a connection IS established but the context is already done (a
// connect that wins the race against cancellation), dialOnce must NEVER hand back a
// live client — it aborts with the cancellation cause, and the owned conn always
// ends closed (by the watchdog, or by the result-branch guard / orphan drain,
// whichever side of the race wins; the watchdog usually pre-empts the others). The
// injected ctx-ignoring dialContext makes the connection get established despite the
// cancelled ctx, so the abort-and-close guarantee is exercised end to end.
func TestDialAbortClosesOwnedConn(t *testing.T) {
	addr := newCmdServer(t, true, okHandler) // completes the SSH handshake
	orig := dialContext
	t.Cleanup(func() { dialContext = orig })
	var tracked atomic.Pointer[trackConn]
	dialContext = func(_ context.Context, network, address string) (net.Conn, error) {
		raw, err := net.Dial(network, address) // ignore ctx: always connect
		if err != nil {
			return nil, err
		}
		tc := &trackConn{Conn: raw}
		tracked.Store(tc)
		return tc, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, err := Dial(ctx, "test", addr, "u", "p", 5*time.Second, 0, ssh.InsecureIgnoreHostKey())
	if err == nil || c != nil {
		t.Fatalf("cancelled ctx: want (nil, error), got (%v, %v)", c, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("an established-but-cancelled dial must wrap context.Canceled, got %v", err)
	}
	tc := tracked.Load()
	if tc == nil {
		t.Fatal("dialContext was never invoked")
	}
	// Closing is asynchronous (watchdog/drain goroutine); poll briefly.
	deadline := time.Now().Add(3 * time.Second)
	for !tc.closed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the owned connection was not closed — leaked")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// lateCloseConn makes Close a no-op so the dial's watchdog cannot break the SSH
// handshake: the handshake therefore SUCCEEDS even on an already-done ctx, so a live
// *ssh.Client materializes that the dctx.Done() branch's drain (and, in the rarer
// TOCTOU, the result-branch guard) must close rather than leak. The real conn is
// closed by the caller once the dial returns.
type lateCloseConn struct{ net.Conn }

func (lateCloseConn) Close() error { return nil }

// On a cancelled ctx, dialOnce must abort and never hand back a live client even
// when the handshake fully SUCCEEDS. With a no-op-close conn the handshake always
// completes, so a live client is produced and the dctx.Done() branch's drain closes
// it; the dial must still return (nil, context.Canceled). Looping under -race also
// exercises that orphan-client Close concurrently with the dial teardown. (On a
// pre-cancelled ctx the select always takes dctx.Done() — the symmetric
// result-branch guard handles only the narrower handshake-completes-as-deadline-
// trips race and is not separately reproducible here.)
func TestDialAbortNeverReturnsLiveClientOnDoneCtx(t *testing.T) {
	addr := newCmdServer(t, true, okHandler) // completes the SSH handshake
	orig := dialContext
	t.Cleanup(func() { dialContext = orig })

	for i := 0; i < 40; i++ {
		var raw net.Conn
		dialContext = func(_ context.Context, network, address string) (net.Conn, error) {
			c, err := net.Dial(network, address) // ignore ctx: always connect
			if err != nil {
				return nil, err
			}
			raw = c
			return lateCloseConn{Conn: c}, nil // no-op Close: handshake always succeeds
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c, err := Dial(ctx, "test", addr, "u", "p", 5*time.Second, 0, ssh.InsecureIgnoreHostKey())
		if raw != nil {
			_ = raw.Close() // release the real conn this iteration
		}
		if c != nil || err == nil {
			t.Fatalf("iter %d: a successful handshake on a done ctx must abort, got (%v, %v)", i, c, err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("iter %d: want context.Canceled, got %v", i, err)
		}
	}
}

// S1-02 exit-code: a parent-ctx cancellation during the handshake must classify
// as context.Canceled (exit 130), distinct from a handshake timeout (exit 1).
func TestDialParentCancelClassifiedAsCancel(t *testing.T) {
	addr, _ := silentBannerServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	err := withTimeout(t, deadlockTimeout, func() error {
		// timeout (5s) >> cancel delay (50ms), so the parent cancel wins.
		_, e := Dial(ctx, "test", addr, "u", "p", 5*time.Second, 0, ssh.InsecureIgnoreHostKey())
		return e
	})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancel during handshake must wrap context.Canceled, got %v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a user cancel must not be reported as a deadline: %v", err)
	}
}

// The watchdog approach must NOT leave a read/write deadline on a successfully
// dialed connection: steady-state I/O long after the dial timeout window must
// still work.
func TestDialSuccessNoLingeringDeadline(t *testing.T) {
	addr := newCmdServer(t, true, func(_ string, _ map[string]string, _ io.Reader, stdout, _ io.Writer) uint32 {
		_, _ = io.WriteString(stdout, "ok")
		return 0
	})
	c, err := Dial(context.Background(), "test", addr, "u", "p", 300*time.Millisecond, 0, ssh.InsecureIgnoreHostKey())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	time.Sleep(350 * time.Millisecond) // exceed the dial timeout window
	out, err := c.Run(context.Background(), "echo")
	if err != nil {
		t.Fatalf("steady-state Run after the dial-timeout window: %v", err)
	}
	if string(out) != "ok" {
		t.Errorf("Run stdout = %q, want ok", out)
	}
}
