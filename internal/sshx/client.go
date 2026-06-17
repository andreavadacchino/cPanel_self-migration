// Package sshx is the native SSH transport for the migration tool.
//
// It uses a single reusable *ssh.Client per host (channels are multiplexed over
// one TCP connection, so every remote command and every tar stream reuses one
// auth). Passwords are held only in memory and never appear in argv/ps/env.
//
// Host keys follow OpenSSH's "accept-new" policy: an unknown host is trusted
// and recorded; a host whose key has CHANGED is rejected with an error.
package sshx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"golang.org/x/crypto/ssh"
)

// Client connection lifecycle states (guarded by Client.mu).
const (
	stateLive   int32 = iota // transport healthy
	stateDead                // keepalive (or an op) saw the transport drop; heal on next use
	stateClosed              // Pool.Close()/Client.Close(): intentional shutdown, never redial
)

// ErrClientClosed is returned by an operation attempted after the client was
// intentionally closed (Pool.Close()/Client.Close()).
var ErrClientClosed = errors.New("ssh client closed")

// Client is a reusable SSH connection to one host. A keepalive-observed drop does
// not kill the run: the connection is transparently re-established (self-healed) on
// the next operation via the single newSession chokepoint — unless it was
// intentionally Closed, which is permanent.
type Client struct {
	name string // human label for logs/errors, e.g. "source"/"dest"

	mu     sync.Mutex    // guards cli, gen, state, stopKA; held across a redial, never across a live NewSession
	cli    *ssh.Client   // the live transport; swapped under mu on redial
	gen    uint64        // bumped on every successful (re)dial; the single-flight redial token
	state  int32         // stateLive | stateDead | stateClosed (read/written only under mu)
	stopKA chan struct{} // per-incarnation keepalive stop; replaced under mu on redial

	openSes atomic.Int64 // currently-open SSH sessions (channels) on this conn
	redials atomic.Int64 // count of successful redials (test seam + diagnostics)

	// Dial recipe, stashed once at construction and read under mu on redial so a heal
	// rebuilds the EXACT same authenticated connection (same TOFU host-key callback).
	// pass is held in memory for the connection's lifetime (in-process only — never
	// argv/ps/env); the live authenticated *ssh.Client already implies that exposure.
	addr      string
	user      string
	pass      string
	timeout   time.Duration
	keepalive time.Duration
	hostKeyCB ssh.HostKeyCallback
}

// current returns the live transport and its generation under the lock (no I/O),
// plus whether the client was intentionally closed.
func (c *Client) current() (cli *ssh.Client, gen uint64, closed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cli, c.gen, c.state == stateClosed
}

// markDead records that the transport observed under gotGen is dead: it transitions
// stateLive -> stateDead, stops that incarnation's keepalive loop, and tears down the
// underlying *ssh.Client. It is gen-guarded so a stale failure (a slow goroutine, or
// an old keepalive loop) can never kill a connection that has SINCE been redialed.
// Idempotent; never redials.
func (c *Client) markDead(gotGen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != stateLive || c.gen != gotGen {
		return // already dead/closed, or this failure was on a since-replaced transport
	}
	c.state = stateDead
	close(c.stopKA)   // stop this incarnation's keepalive loop
	_ = c.cli.Close() // unblock any in-flight reads on the dead transport
}

// heal redials ONCE if the transport died and the client was not intentionally
// closed, swapping c.cli under the lock. gotGen is the generation observed before the
// failure: exactly one goroutine finds c.gen == gotGen and dials (bumping gen); all
// others observe the advanced gen and reuse the fresh transport (single-flight). The
// lock is held across the bounded dial; the success-path NewSession never holds it.
func (c *Client) heal(ctx context.Context, gotGen uint64) (*ssh.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == stateClosed {
		return nil, ErrClientClosed
	}
	if c.gen != gotGen {
		return c.cli, nil // a concurrent caller already redialed since our snapshot
	}
	fresh, err := c.redialLocked(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconnect %s: %w", c.name, err) // leave state == stateDead
	}
	c.cli = fresh
	c.gen++
	c.state = stateLive
	c.stopKA = make(chan struct{})
	if c.keepalive > 0 {
		go c.keepaliveLoop(c.cli, c.keepalive, c.stopKA, c.gen)
	}
	c.redials.Add(1)
	// Warn (not Debug) so the operator sees the recovery that resolves the drop Warn
	// logged by keepaliveProbe — the sshx package has no operator-visible Info level.
	logx.Warn("reconnected to %s after a dropped connection", c.name)
	return c.cli, nil
}

// redialLocked builds a fresh authenticated *ssh.Client from the stashed dial recipe
// (bounded by ctx + DialRetries inside dialAttempts). The caller must hold c.mu.
func (c *Client) redialLocked(ctx context.Context) (*ssh.Client, error) {
	cfg := &ssh.ClientConfig{
		User:            c.user,
		Auth:            []ssh.AuthMethod{ssh.Password(c.pass)},
		HostKeyCallback: c.hostKeyCB,
		Timeout:         c.timeout,
	}
	return dialAttempts(ctx, c.name, c.addr, c.timeout, cfg)
}

// newSession opens a session and tracks the live count for debug diagnostics. It
// is the single chokepoint for every remote operation, so it is also where a
// dropped connection self-heals: if NewSession fails because the transport is gone
// (isConnClosedErr) and the client was not intentionally Closed, it redials ONCE
// (heal) and retries on the fresh transport. ctx bounds that redial. The SSH server
// limits concurrent sessions (MaxSessions, often 10); the open count is carried into
// the error so --log-level debug can reveal saturation.
func (c *Client) newSession(ctx context.Context, what string) (*ssh.Session, error) {
	// Respect an already-cancelled context before any I/O: return ctx.Err()
	// (context.Canceled / DeadlineExceeded) immediately and never attempt a
	// NewSession or a heal/redial on a dead transport for a caller that is
	// already shutting down.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cli, gen, closed := c.current()
	if closed {
		return nil, fmt.Errorf("%s: %w", c.name, ErrClientClosed)
	}
	sess, err := cli.NewSession() // NOT under c.mu — never hold the lock across this I/O
	if err != nil && isConnClosedErr(err) {
		// Transport dropped (e.g. after a keepalive close). Mark it dead and redial
		// once, then retry NewSession on the fresh transport. Single retry, never a loop:
		// a still-failing fresh connection surfaces to the normal retry/apply layer.
		c.markDead(gen)
		fresh, herr := c.heal(ctx, gen)
		if herr != nil {
			return nil, fmt.Errorf("%s: new session: %w", c.name, herr)
		}
		sess, err = fresh.NewSession()
	}
	if err != nil {
		open := c.openSes.Load()
		logx.Debug("%s: NewSession(%s) FAILED (open=%d): %v", c.name, what, open, err)
		// Carry the live open-session count into the error. An SSH session-open
		// failure here is most often the server's MaxSessions cap (often 10), which
		// the raw error does not name — so a transfer that fails repeatedly because
		// the connection is saturated otherwise looks like a phantom transfer bug.
		// This lets the operator correlate it without needing --log-level debug.
		if open > 0 {
			return nil, fmt.Errorf("%w (%d session(s) already open on this connection — possible MaxSessions limit)", err, open)
		}
		return nil, err
	}
	n := c.openSes.Add(1)
	logx.Debug("%s: session opened [%s] — now %d open", c.name, what, n)
	return sess, nil
}

// closeSession closes a tracked session and decrements the live count.
func (c *Client) closeSession(sess *ssh.Session, what string) {
	_ = sess.Close()
	n := c.openSes.Add(-1)
	logx.Debug("%s: session closed [%s] — now %d open", c.name, what, n)
}

// Dial opens a password-authenticated SSH connection to addr (host:port).
//
// hostKeyCB enforces the host-key policy (see AcceptNewHostKey). keepalive, if
// > 0, sends an SSH keepalive at that interval. timeout bounds the WHOLE
// connection setup — TCP connect, banner, key exchange and auth — not just the
// TCP connect, and a transient connect failure is retried up to DialRetries
// times. The actual dial+retry lives in dialWithRetry (retry.go).
func Dial(ctx context.Context, name, addr, user, pass string, timeout, keepalive time.Duration, hostKeyCB ssh.HostKeyCallback) (*Client, error) {
	return dialWithRetry(ctx, name, addr, user, pass, timeout, keepalive, hostKeyCB)
}

// dialContext is the TCP-dial seam. It defaults to a plain context-aware dialer
// and is a package var so tests can inject transient connect failures.
var dialContext = (&net.Dialer{}).DialContext

// dialOnce performs ONE owned-connection SSH dial. It opens the TCP connection
// itself — so it OWNS the net.Conn — then runs the SSH handshake and auth,
// bounding the whole phase by timeout (and by ctx).
//
// Owning the conn is what makes a timeout or cancellation able to abort a wedged
// handshake: a watchdog Closes the conn, which unblocks the deadline-less reads
// inside x/crypto's version/kex exchange. ssh.Dial cannot do this — it applies
// timeout only to the TCP connect (net.DialTimeout) and never exposes its
// net.Conn, so a peer that accepts TCP but never speaks SSH would wedge forever.
//
// On a ctx-cancel or timeout abort, dialOnce returns ctx.Err() / DeadlineExceeded
// (never the raw "use of closed network connection" noise from the closed conn),
// and the distinction is deliberate: a real Ctrl-C cancels the PARENT ctx (so the
// caller sees context.Canceled and main.go can pick exit 130), while a pure
// handshake timeout only trips the child deadline and leaves the parent ctx.Err()
// nil (exit 1). Never report a handshake timeout as parent cancellation.
func dialOnce(ctx context.Context, addr string, timeout time.Duration, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	dctx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		dctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// TCP connect honors both ctx and the deadline; a pre-cancelled ctx returns
	// here before any network I/O. We now own conn.
	conn, err := dialContext(dctx, "tcp", addr)
	if err != nil {
		return nil, err
	}

	// Watchdog: closing the owned conn on dctx.Done() unblocks the handshake reads;
	// hsDone retires it once the handshake completes (success or failure).
	hsDone := make(chan struct{})
	go func() {
		select {
		case <-dctx.Done():
			_ = conn.Close()
		case <-hsDone:
		}
	}()

	type result struct {
		cli *ssh.Client
		err error
	}
	ch := make(chan result, 1)
	go func() {
		sc, chans, reqs, herr := ssh.NewClientConn(conn, addr, cfg)
		if herr != nil {
			ch <- result{nil, herr}
			return
		}
		// ssh.NewClient services global requests and channel opens exactly as
		// ssh.Dial would, so the returned *ssh.Client has the same lifecycle.
		ch <- result{ssh.NewClient(sc, chans, reqs), nil}
	}()

	// abortErr maps a dctx abort to the cause the caller must see for the right
	// exit code (see the doc comment).
	abortErr := func() error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return context.DeadlineExceeded
	}

	select {
	case <-dctx.Done():
		// Abandon the handshake. The watchdog already closed conn; drain ch so a
		// client that still materializes in the race window is Closed, not leaked.
		go func() {
			if r := <-ch; r.err == nil && r.cli != nil {
				_ = r.cli.Close()
			}
		}()
		return nil, abortErr()
	case r := <-ch:
		close(hsDone)
		// Symmetric with the dctx.Done() branch: if dctx expired or was cancelled as
		// the handshake finished, this result lost the race — report the abort cause
		// (not the closed-conn noise on failure, and never a live client on success),
		// closing any client we got so it cannot leak. On a pre-cancelled ctx the
		// select takes dctx.Done() above (ch is not ready yet), so this guard only
		// fires for the narrow TOCTOU where the handshake completes in the same
		// instant the deadline trips; it is cheap, defensive belt-and-suspenders.
		if dctx.Err() != nil {
			if r.cli != nil {
				_ = r.cli.Close()
			}
			return nil, abortErr()
		}
		if r.err != nil {
			return nil, r.err
		}
		return r.cli, nil
	}
}

// Name returns the client's human label.
func (c *Client) Name() string { return c.name }

// keepaliveLoop probes the connection it was started for (cli/stop/gen identify the
// incarnation) until that connection is stopped or observed dead. Each redial starts
// a fresh loop bound to the new transport, so an old loop never probes or marks a
// newer connection.
func (c *Client) keepaliveLoop(cli *ssh.Client, interval time.Duration, stop chan struct{}, gen uint64) {
	t := time.NewTicker(interval)
	defer t.Stop()
	misses := 0 // CONSECUTIVE timed-out probes; reset by any successful reply
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			switch res, err := c.keepaliveProbe(cli, interval, stop); res {
			case probeStopped:
				return
			case probeOK:
				misses = 0 // a reply arrived (possibly between data bursts) — the link is alive
			case probeError:
				// A real transport error (EOF / connection closed): the connection is
				// genuinely gone, so there is no point waiting for more misses — declare it
				// dead now and let the next op self-heal.
				logx.Warn("keepalive on %s failed (%v); the connection dropped and will be re-established on the next operation", c.name, err)
				c.markDead(gen)
				return
			case probeTimeout:
				// A single slow keepalive is expected while a large transfer saturates the
				// link and starves the reply past one interval; tolerate up to
				// keepaliveMaxMisses CONSECUTIVE misses (ServerAliveCountMax) before declaring
				// the connection dead, so a busy-but-alive connection is not torn down and
				// forced to re-stream its current batch.
				misses++
				if misses >= keepaliveMaxMisses {
					logx.Warn("keepalive on %s got no reply for %d consecutive probes (~%s); the connection dropped and will be re-established on the next operation",
						c.name, misses, time.Duration(misses)*interval)
					c.markDead(gen)
					return
				}
				logx.Debug("keepalive on %s: no reply within %s (miss %d/%d) — tolerating (likely a busy transfer saturating the link)",
					c.name, interval, misses, keepaliveMaxMisses)
			}
		}
	}
}

// probeResult is the outcome of one keepaliveProbe.
type probeResult int

const (
	probeOK      probeResult = iota // reply received: the connection is healthy
	probeTimeout                    // no reply within the window: tolerated up to keepaliveMaxMisses
	probeError                      // SendRequest returned an error: the transport is gone
	probeStopped                    // the keepalive stop signal fired
)

// keepaliveProbe sends one SSH keepalive and waits up to timeout for the reply,
// REPORTING the outcome — it does NOT itself mark the connection dead, because
// keepaliveLoop tolerates a few transient timeouts before declaring a drop (see
// keepaliveMaxMisses). The point of the keepalive is to detect a dead connection FAST so
// a transfer's io.Copy that is BLOCKED on it unblocks with a real error and the
// batch-retry machinery can act — but SendRequest is itself BLOCKING, so on a black-holed
// connection (silently gone, no RST) it would hang until the OS TCP timeout (often many
// minutes), defeating the whole purpose. Bounding it means a failed reply OR no reply
// within timeout is OBSERVED promptly. The send runs in a goroutine with a buffered result
// channel so it never blocks us, and Close() makes the pending SendRequest return so the
// goroutine does not leak.
func (c *Client) keepaliveProbe(cli *ssh.Client, timeout time.Duration, stop chan struct{}) (probeResult, error) {
	errc := make(chan error, 1)
	go func() {
		// Probe the captured incarnation, NOT c.cli: heal may swap c.cli concurrently,
		// and an old loop must never probe the new transport (also avoids a data race).
		_, _, err := cli.SendRequest("keepalive@openssh.com", true, nil)
		errc <- err
	}()
	select {
	case <-stop:
		return probeStopped, nil
	case err := <-errc:
		if err != nil {
			return probeError, err
		}
		return probeOK, nil
	case <-time.After(timeout):
		return probeTimeout, nil
	}
}

// Close intentionally and permanently shuts the client down: it latches stateClosed
// (so no future operation can redial — ensureLive/heal refuse) and, only when there is
// still something to tear down, stops the keepalive loop and closes the connection.
// Safe to call multiple times and safe to race with a keepalive-induced markDead
// (whichever sets the latch wins, under c.mu).
//
// The tear-down runs ONLY on the stateLive -> stateClosed transition, because that is
// the only state that still owns an open transport and a running keepalive loop:
//   - stateDead  : markDead already closed the keepalive stop-chan AND the transport
//     (so a failed heal leaves nothing to close here).
//   - stateClosed: a prior Close already did.
//
// In both of those cases re-closing the transport would return a spurious
// net.ErrClosed ("use of closed network connection"), so Close reports a clean nil.
func (c *Client) Close() error {
	c.mu.Lock()
	prev := c.state
	c.state = stateClosed
	cli := c.cli
	stop := c.stopKA
	c.mu.Unlock()
	if prev != stateLive {
		return nil
	}
	close(stop)        // only a live incarnation still has a running keepalive loop + open stop chan
	return cli.Close() // first and only close of the live transport
}

// Run executes a single command and returns its stdout. On a non-zero exit it
// returns an error that includes stderr (trimmed).
func (c *Client) Run(ctx context.Context, cmd string) ([]byte, error) {
	return c.run(ctx, cmd, nil, nil)
}

// RunScript runs a bash script over stdin (`bash -s`), injecting params as
// environment variables via the SSH session (NOT string interpolation). This
// is what makes `$6$…` password hashes safe to pass: they travel as $HASH,
// never spliced into a command line. Returns stdout.
func (c *Client) RunScript(ctx context.Context, script string, env map[string]string) ([]byte, error) {
	return c.run(ctx, "bash -s", env, bytes.NewReader([]byte(script)))
}

func (c *Client) run(ctx context.Context, cmd string, env map[string]string, stdin io.Reader) ([]byte, error) {
	sess, err := c.newSession(ctx, "run")
	if err != nil {
		return nil, fmt.Errorf("%s: new session: %w", c.name, err)
	}

	for k, v := range env {
		if err := sess.Setenv(k, v); err != nil {
			// Many sshd configs reject Setenv (AcceptEnv); fall back to a
			// safe inline `export`. IMPORTANT: close THIS session first, so we
			// never hold two sessions at once during the fallback (the server
			// MaxSessions limit is small — leaking here saturates it).
			logx.Debug("%s: Setenv rejected, using inline-env fallback", c.name)
			c.closeSession(sess, "run/setenv-fallback")
			return c.runWithInlineEnv(ctx, cmd, env, stdin)
		}
	}
	defer c.closeSession(sess, "run")

	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	if stdin != nil {
		sess.Stdin = stdin
	}

	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		return nil, fmt.Errorf("%s: %q: %w", c.name, cmd, ctx.Err())
	case err := <-done:
		if err != nil {
			return stdout.Bytes(), fmt.Errorf("%s: %q failed: %w (stderr: %s)",
				c.name, cmd, err, bytes.TrimSpace(stderr.Bytes()))
		}
		logx.Debug("%s: run %q success: %d bytes stdout", c.name, cmd, stdout.Len())
		return stdout.Bytes(), nil
	}
}

// runWithInlineEnv is the fallback when the server rejects SSH Setenv. It
// prepends `export KEY='value'` lines (single-quote escaped) to the script,
// preserving the "no secrets in argv" property.
func (c *Client) runWithInlineEnv(ctx context.Context, cmd string, env map[string]string, stdin io.Reader) ([]byte, error) {
	if stdin == nil {
		// Not a script command; we cannot inline env safely. Report clearly.
		return nil, fmt.Errorf("%s: server rejected Setenv and cmd is not a script", c.name)
	}
	logx.Debug("%s: runWithInlineEnv fallback: %d env var(s) to inline", c.name, len(env))
	body, err := io.ReadAll(stdin)
	if err != nil {
		return nil, fmt.Errorf("%s: read script: %w", c.name, err)
	}
	var pre bytes.Buffer
	for k, v := range env {
		fmt.Fprintf(&pre, "export %s='%s'\n", k, SingleQuoteEscape(v))
	}
	pre.Write(body)
	return c.runNoEnv(ctx, cmd, bytes.NewReader(pre.Bytes()))
}

func (c *Client) runNoEnv(ctx context.Context, cmd string, stdin io.Reader) ([]byte, error) {
	sess, err := c.newSession(ctx, "runNoEnv")
	if err != nil {
		return nil, fmt.Errorf("%s: new session: %w", c.name, err)
	}
	defer c.closeSession(sess, "runNoEnv")
	var stdout, stderr bytes.Buffer
	sess.Stdout = &stdout
	sess.Stderr = &stderr
	sess.Stdin = stdin
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		return nil, fmt.Errorf("%s: %q: %w", c.name, cmd, ctx.Err())
	case err := <-done:
		if err != nil {
			return stdout.Bytes(), fmt.Errorf("%s: %q failed: %w (stderr: %s)",
				c.name, cmd, err, bytes.TrimSpace(stderr.Bytes()))
		}
		return stdout.Bytes(), nil
	}
}
