package sshx

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// noReplyServer completes the SSH handshake and serves session "exec" channels
// (so Run works), but NEVER replies to global requests — so a keepalive probe
// (SendRequest with WantReply) times out. Used to drive the keepalive failure path.
func noReplyServer(t *testing.T) string {
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
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				defer sconn.Close()
				go func() {
					for range reqs { // drain WITHOUT replying -> keepalive WantReply never answered
					}
				}()
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
	return ln.Addr().String()
}

// curGen reads the client's current generation under its lock (race-safe).
func curGen(c *Client) uint64 {
	_, gen, _ := c.current()
	return gen
}

// TestIsConnClosedErr pins the redial-trigger classifier: only a dead established
// transport heals; cancellation, a stall-kill, and unrelated errors do not.
func TestIsConnClosedErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context canceled", context.Canceled, false},
		{"context deadline", context.DeadlineExceeded, false},
		{"stalled", ErrStalled, false},
		{"wrapped stalled", fmt.Errorf("batch x: %w", ErrStalled), false},
		{"command failure (unrelated)", errors.New(`"mysqldump" failed: exit status 2`), false},
		{"permission denied", errors.New("permission denied"), false},

		{"net.ErrClosed", net.ErrClosed, true},
		{"wrapped net.ErrClosed", fmt.Errorf("write: %w", net.ErrClosed), true},
		{"io.EOF", io.EOF, true},
		{"unexpected EOF", io.ErrUnexpectedEOF, true},
		{"ECONNRESET", syscall.ECONNRESET, true},
		{"EPIPE", syscall.EPIPE, true},
		{"closed text", errors.New("read tcp: use of closed network connection"), true},
		{"channel open on dead mux", errors.New("ssh: unexpected packet in response to channel open: <nil>"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isConnClosedErr(c.err); got != c.want {
				t.Errorf("isConnClosedErr(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// Core regression: after a transport drop (here a keepalive-style markDead), the
// next operation transparently self-heals and succeeds.
func TestNewSessionHealsAfterTransportDrop(t *testing.T) {
	addr := newCmdServer(t, true, okHandler)
	c := dialTest(t, addr)
	defer c.Close()

	c.markDead(curGen(c)) // simulate a keepalive-observed drop

	if _, err := c.Run(context.Background(), "echo"); err != nil {
		t.Fatalf("Run after a transport drop must self-heal, got %v", err)
	}
	if got := c.redials.Load(); got != 1 {
		t.Errorf("redials = %d, want exactly 1", got)
	}
}

// Single-flight: N concurrent ops after a drop trigger exactly ONE redial; the
// rest reuse the fresh transport. Run under -race for the c.cli swap.
func TestRedialOnceUnderConcurrency(t *testing.T) {
	addr := newCmdServer(t, true, okHandler)
	c := dialTest(t, addr)
	defer c.Close()

	c.markDead(curGen(c))

	const N = 50
	var wg sync.WaitGroup
	errs := make([]error, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = c.Run(context.Background(), "echo")
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Errorf("goroutine %d failed: %v", i, e)
		}
	}
	if got := c.redials.Load(); got != 1 {
		t.Errorf("redials = %d, want exactly 1 (single-flight)", got)
	}
}

// An intentional Close is permanent: no operation redials after it.
func TestNoRedialAfterIntentionalClose(t *testing.T) {
	addr := newCmdServer(t, true, okHandler)
	c := dialTest(t, addr)

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := c.Run(context.Background(), "echo"); !errors.Is(err, ErrClientClosed) {
		t.Errorf("Run after Close = %v, want ErrClientClosed", err)
	}
	// A drop signal after Close must not resurrect the client.
	c.markDead(curGen(c))
	if _, err := c.Run(context.Background(), "echo"); !errors.Is(err, ErrClientClosed) {
		t.Errorf("Run after Close+markDead = %v, want ErrClientClosed", err)
	}
	if got := c.redials.Load(); got != 0 {
		t.Errorf("redials = %d, want 0 after intentional Close", got)
	}
}

// A cancelled context must not redial: the heal honors ctx and fails fast.
func TestNoRedialOnCtxCancel(t *testing.T) {
	addr := newCmdServer(t, true, okHandler)
	c := dialTest(t, addr)
	defer c.Close()

	c.markDead(curGen(c))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Run(ctx, "echo")
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Run on a cancelled ctx after a drop = %v, want context.Canceled", err)
	}
	if got := c.redials.Load(); got != 0 {
		t.Errorf("redials = %d, want 0 on a cancelled redial", got)
	}
}

// A redial to an unreachable host is bounded (DialRetries) and surfaces a clear
// reconnect error instead of hanging.
func TestRedialBoundedWhenDestStaysDown(t *testing.T) {
	addr := newCmdServer(t, true, okHandler)
	c := dialTest(t, addr)
	defer c.Close()
	withDialKnobs(t, 2, 0) // 2 attempts, no backoff

	// Point the stashed redial recipe at a refused port so the redial fails fast.
	c.addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(refusedPort(t)))
	c.markDead(curGen(c))

	err := withTimeout(t, deadlockTimeout, func() error {
		_, e := c.Run(context.Background(), "echo")
		return e
	})
	if err == nil {
		t.Fatal("a redial to a refused port must fail, not hang")
	}
	if !strings.Contains(err.Error(), "reconnect") {
		t.Errorf("error should name the reconnect failure, got %v", err)
	}
	if got := c.redials.Load(); got != 0 {
		t.Errorf("redials = %d, want 0 (the redial failed)", got)
	}
}

// keepaliveProbe REPORTS a timed-out probe (no reply) but must NOT itself kill the
// connection — keepaliveLoop tolerates transient misses (a busy transfer starves the
// reply) and only declares the drop after keepaliveMaxMisses. So a single probe timeout
// leaves the connection LIVE and usable, with no redial. Driven directly (keepalive=0, no
// loop) against a no-reply server so the probe times out deterministically.
func TestKeepaliveProbeReportsTimeoutWithoutKilling(t *testing.T) {
	addr := noReplyServer(t)
	c, err := Dial(context.Background(), "test", addr, "u", PasswordAuth("p"), 5*time.Second, 0, ssh.InsecureIgnoreHostKey())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	if res, _ := c.keepaliveProbe(c.cli, 100*time.Millisecond, c.stopKA); res != probeTimeout {
		t.Fatalf("keepaliveProbe against a no-reply server must report probeTimeout, got %v", res)
	}
	if got := c.redials.Load(); got != 0 {
		t.Errorf("a single probe timeout must NOT kill/redial the connection, redials = %d", got)
	}
	// The connection is still live: a Run succeeds WITHOUT a self-heal redial.
	if _, err := c.Run(context.Background(), "echo"); err != nil {
		t.Fatalf("Run on the still-live connection after one tolerated probe timeout: %v", err)
	}
	if got := c.redials.Load(); got != 0 {
		t.Errorf("Run must NOT have redialed (connection was never marked dead), redials = %d", got)
	}
}

// keepaliveLoop must TOLERATE transient missed keepalives and declare the connection dead
// only after keepaliveMaxMisses CONSECUTIVE misses — not on the first one (the bug fix:
// a large transfer starves the reply, but the connection is alive). Against a server that
// never replies to keepalives, the loop (short interval) must take at least ~maxMisses
// intervals before the transport is marked dead, then self-heal (DEAD, not CLOSED).
func TestKeepaliveLoopToleratesMissesBeforeMarkingDead(t *testing.T) {
	addr := noReplyServer(t)
	c, err := Dial(context.Background(), "test", addr, "u", PasswordAuth("p"), 5*time.Second, 0, ssh.InsecureIgnoreHostKey())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	isDead := func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.state == stateDead
	}

	const interval = 40 * time.Millisecond
	start := time.Now()
	go c.keepaliveLoop(c.cli, interval, c.stopKA, c.gen)

	deadline := time.Now().Add(3 * time.Second)
	for !isDead() {
		if time.Now().After(deadline) {
			t.Fatal("keepaliveLoop never marked the no-reply connection dead")
		}
		time.Sleep(2 * time.Millisecond)
	}
	// die-on-first-miss would be ~2 intervals; tolerance requires ~(maxMisses+1) intervals,
	// so the death must land clearly past a single miss.
	if elapsed, min := time.Since(start), keepaliveMaxMisses*interval; elapsed < min {
		t.Errorf("connection declared dead after %s; must tolerate ~%d consecutive misses (>= %s)", elapsed, keepaliveMaxMisses, min)
	}
	// Dead, not Closed: the next op self-heals.
	if _, err := c.Run(context.Background(), "echo"); err != nil {
		t.Fatalf("Run after the loop marked dead must self-heal: %v", err)
	}
	if got := c.redials.Load(); got < 1 {
		t.Errorf("redials = %d, want >= 1 after self-heal", got)
	}
}

// Pool.Close racing a keepalive-style markDead: Close always wins (no redial
// survives a shutdown). Looped under -race.
func TestPoolCloseRacesKeepaliveNoRedial(t *testing.T) {
	for i := 0; i < 50; i++ {
		addr := newCmdServer(t, true, okHandler)
		c := dialTest(t, addr)
		gen := curGen(c)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); c.markDead(gen) }()
		go func() { defer wg.Done(); _ = c.Close() }()
		wg.Wait()

		if _, err := c.Run(context.Background(), "echo"); !errors.Is(err, ErrClientClosed) {
			t.Fatalf("iter %d: Run after Close+markDead = %v, want ErrClientClosed", i, err)
		}
		if got := c.redials.Load(); got != 0 {
			t.Fatalf("iter %d: redials = %d, want 0", i, got)
		}
	}
}

// A redial stops the old keepalive loop and starts exactly one new one, so repeated
// redials do not leak goroutines.
func TestKeepaliveLoopNotLeakedOnRedial(t *testing.T) {
	addr := newCmdServer(t, true, okHandler)
	c, err := Dial(context.Background(), "test", addr, "u", PasswordAuth("p"), 5*time.Second, 30*time.Millisecond, ssh.InsecureIgnoreHostKey())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	time.Sleep(50 * time.Millisecond) // let the first keepalive loop settle
	base := runtime.NumGoroutine()

	for i := 0; i < 5; i++ {
		c.markDead(curGen(c))
		if _, err := c.Run(context.Background(), "echo"); err != nil {
			t.Fatalf("redial %d: %v", i, err)
		}
	}
	if got := c.redials.Load(); got != 5 {
		t.Errorf("redials = %d, want 5", got)
	}
	// Poll for the goroutine count to settle: a per-redial keepalive-loop leak would
	// keep it at base+5 or more.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if n := runtime.NumGoroutine(); n <= base+4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines did not settle after 5 redials: have %d, base %d", runtime.NumGoroutine(), base)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A STALE markDead (for an already-superseded generation) must be a no-op: it must
// NOT tear down a connection that has since been redialed. This pins the gen guard
// in markDead — removing `c.gen != gotGen` makes this fail (the stale markDead kills
// the fresh transport, forcing an extra heal).
func TestStaleMarkDeadDoesNotKillFreshConnection(t *testing.T) {
	addr := newCmdServer(t, true, okHandler)
	c := dialTest(t, addr)
	defer c.Close()

	gen0 := curGen(c) // generation 1
	c.markDead(gen0)  // drop it
	if _, err := c.Run(context.Background(), "echo"); err != nil {
		t.Fatalf("first heal: %v", err) // heals to generation 2 (redials=1)
	}
	if got := c.redials.Load(); got != 1 {
		t.Fatalf("redials after first heal = %d, want 1", got)
	}

	// A stale markDead for generation 1 arrives AFTER the heal advanced to generation
	// 2 (e.g. an old keepalive probe completing late). It must not touch the fresh
	// transport.
	c.markDead(gen0)

	if _, err := c.Run(context.Background(), "echo"); err != nil {
		t.Fatalf("Run after a stale markDead must work on the fresh transport: %v", err)
	}
	if got := c.redials.Load(); got != 1 {
		t.Errorf("redials = %d, want still 1 (a stale markDead must not kill the fresh connection)", got)
	}
}

// End-to-end (spec §6 #10): a transfer batch whose connection drops mid-attempt is
// recovered by RetryBatch — the next attempt's newSession self-heals the transport
// and succeeds. Without the self-heal, every retry fails on the dead transport and
// the batch is exhausted.
func TestRetryBatchSelfHealsBridge(t *testing.T) {
	addr := newCmdServer(t, true, okHandler)
	c := dialTest(t, addr)
	defer c.Close()
	withDialKnobs(t, 3, 0) // DialRetries=3, RetryBackoffBase=0 (also speeds RetryBatch)

	var attempt int
	err := RetryBatch(context.Background(), "selfheal", 0, nil, func(bctx context.Context, onBytes func(int64)) error {
		attempt++
		if attempt == 1 {
			// Simulate the connection dropping mid-batch, then fail the attempt so
			// RetryBatch retries (a real mid-copy drop surfaces this way).
			c.markDead(curGen(c))
			return fmt.Errorf("simulated mid-batch drop: %w", net.ErrClosed)
		}
		_, e := c.Run(bctx, "echo") // attempt 2: must self-heal the transport
		return e
	})
	if err != nil {
		t.Fatalf("RetryBatch should recover on attempt 2 via self-heal, got %v", err)
	}
	if attempt != 2 {
		t.Errorf("attempts = %d, want 2 (fail-then-heal)", attempt)
	}
	if got := c.redials.Load(); got != 1 {
		t.Errorf("redials = %d, want 1 (healed once on the retry)", got)
	}
}

// End-to-end (spec §6 #11): a StallContext kill must NOT redial — a stalled-but-alive
// transfer is aborted and retried, not mistaken for a dropped transport.
func TestStallNotHealed(t *testing.T) {
	block := make(chan struct{})
	addr := newCmdServer(t, true, func(_ string, _ map[string]string, _ io.Reader, _, _ io.Writer) uint32 {
		<-block // never makes progress -> the stall watchdog fires
		return 0
	})
	t.Cleanup(func() { close(block) })
	c := dialTest(t, addr)
	defer c.Close()
	withDialKnobs(t, 3, 0) // RetryBackoffBase=0 so the exhausted-retries path is fast

	err := withTimeout(t, deadlockTimeout, func() error {
		return RetryBatch(context.Background(), "stall", 50*time.Millisecond, nil, func(bctx context.Context, onBytes func(int64)) error {
			_, e := c.Run(bctx, "block") // blocks until the stall-kill cancels bctx
			return e
		})
	})
	if err == nil {
		t.Fatal("a perpetually-stalling batch must fail")
	}
	if got := c.redials.Load(); got != 0 {
		t.Errorf("a stall-kill must NOT redial, got redials = %d", got)
	}
}

// A command's non-zero exit (a real *ssh.ExitError) must NOT be classified as a
// closed transport (so it is never mistaken for a redial trigger).
// Close() after a FAILED heal must return nil: markDead already tore down the
// transport (and the keepalive stop-chan), so the client is cleanly shut even though
// the heal could not redial — re-Closing the dead transport must NOT surface a
// spurious net.ErrClosed.
func TestCloseAfterFailedHealReturnsNil(t *testing.T) {
	addr := newCmdServer(t, true, okHandler)
	c := dialTest(t, addr)
	withDialKnobs(t, 2, 0) // 2 attempts, no backoff
	c.addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(refusedPort(t)))
	c.markDead(curGen(c)) // transport torn down; next op will try (and fail) to heal

	if _, e := c.Run(context.Background(), "echo"); e == nil {
		t.Fatal("Run should fail: the heal targets a refused port")
	}
	// The client is now stateDead with a failed heal. Close must be clean.
	if err := c.Close(); err != nil {
		t.Errorf("Close after a failed heal = %v, want nil (transport already torn down by markDead)", err)
	}
}

// Close() is idempotent AND returns nil on every call — the second Close must not
// re-Close the already-closed transport and surface a spurious net.ErrClosed.
func TestCloseIdempotentReturnsNil(t *testing.T) {
	addr := newCmdServer(t, true, okHandler)
	c := dialTest(t, addr)
	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second Close = %v, want nil (idempotent)", err)
	}
}

func TestIsConnClosedErrIgnoresCommandExit(t *testing.T) {
	addr := newCmdServer(t, true, func(_ string, _ map[string]string, _ io.Reader, _, _ io.Writer) uint32 {
		return 3 // non-zero exit -> sess.Run returns an *ssh.ExitError
	})
	c := dialTest(t, addr)
	defer c.Close()

	_, err := c.Run(context.Background(), "fail")
	if err == nil {
		t.Fatal("expected a non-zero exit error")
	}
	if isConnClosedErr(err) {
		t.Errorf("a command non-zero exit must not be treated as a closed transport: %v", err)
	}
}
