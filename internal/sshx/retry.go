package sshx

import (
	"context"
	"fmt"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"golang.org/x/crypto/ssh"
)

// TransferRetries bounds the per-batch retry attempts for a streaming tar transfer.
const TransferRetries = 4

// DialRetries bounds the attempts for the INITIAL per-host SSH dial. The dial is
// non-destructive (step 1 runs before any write), so retrying a transient connect
// failure is safe and only makes the tool resilient to a host still booting or a
// brief routing flap. It is a var (not a const) so tests can shrink it. Each
// attempt gets the full per-host timeout; permanent failures (auth rejected,
// changed host key, cancellation) are never retried — see IsTransientDialError.
var DialRetries = 3

// dialWithRetry dials one host (via dialAttempts) and, on success, wraps the
// connection in a *Client, stashing the dial recipe so a later keepalive-observed
// drop can self-heal (Client.heal), and starts the keepalive loop — preserving
// Dial's original contract.
func dialWithRetry(ctx context.Context, name, addr, user string, auth Authentication, timeout, keepalive time.Duration, hostKeyCB ssh.HostKeyCallback) (*Client, error) {
	cfg := newClientConfig(user, auth, hostKeyCB, timeout)
	cli, err := dialAttempts(ctx, name, addr, timeout, cfg)
	if err != nil {
		return nil, err
	}
	c := &Client{
		name:      name,
		cli:       cli,
		gen:       1,
		state:     stateLive,
		stopKA:    make(chan struct{}),
		addr:      addr,
		user:      user,
		auth:      auth,
		timeout:   timeout,
		keepalive: keepalive,
		hostKeyCB: hostKeyCB,
	}
	if keepalive > 0 {
		go c.keepaliveLoop(c.cli, keepalive, c.stopKA, c.gen)
	}
	return c, nil
}

// dialAttempts runs the bounded per-attempt dial loop: up to DialRetries calls to
// dialOnce (which bounds the whole TCP+handshake+auth phase), retrying only failures
// IsTransientDialError accepts and reusing the package backoff (BackoffSleep). It
// returns the raw *ssh.Client so both the initial dial (dialWithRetry) and a redial
// (Client.redialLocked) share the exact same retry/classify/backoff policy.
func dialAttempts(ctx context.Context, name, addr string, timeout time.Duration, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	// Clamp a misconfigured DialRetries (<= 0): always make at least one dial, never
	// skip the loop entirely and return an error wrapping a nil lastErr.
	attempts := DialRetries
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		logx.Debug("dial %s (%s): attempt %d/%d", name, addr, attempt, attempts)
		cli, err := dialOnce(ctx, addr, timeout, cfg)
		if err == nil {
			logx.Debug("dial %s (%s): success", name, addr)
			return cli, nil
		}
		lastErr = err
		// Honor a real cancellation immediately: a Ctrl-C is never retried.
		if ctx.Err() != nil {
			return nil, fmt.Errorf("ssh dial %s (%s): %w", name, addr, ctx.Err())
		}
		// A permanent failure (auth rejected, changed host key, handshake timeout)
		// will not improve on retry — surface it now with the original cause.
		if !IsTransientDialError(err) {
			return nil, fmt.Errorf("ssh dial %s (%s): %w", name, addr, err)
		}
		logx.Debug("dial %s (%s): attempt %d/%d failed (transient): %v", name, addr, attempt, attempts, err)
		if attempt < attempts {
			if berr := BackoffSleep(ctx, attempt); berr != nil {
				return nil, fmt.Errorf("ssh dial %s (%s): %w", name, addr, berr)
			}
		}
	}
	return nil, fmt.Errorf("ssh dial %s (%s): %w", name, addr, lastErr)
}

// RetryBatch runs stream up to TransferRetries times, composing the two transfer
// robustness primitives in this package: a per-attempt STALL timeout (StallContext,
// reset on every onBytes chunk, so a healthy slow batch survives but a wedged remote
// is aborted and retried) and a bounded backoff between attempts (BackoffSleep).
//
// onBytes(n) is passed to stream and must be called for every n bytes relayed; it
// resets the stall watchdog and forwards to addBytes. On a FAILED attempt the bytes
// counted during it are rolled back via addBytes(-attemptBytes), so a progress bar
// does not over-count a re-send. addBytes may be nil (no progress tracking). label
// is used only for debug log lines.
//
// Returns nil on the first success, ctx.Err() if the context is cancelled (during
// an attempt or the backoff), else the last attempt's error. This is the single
// source for the maildir/webfiles per-batch retry loops.
//
// stopRetry (optional) lets the caller short-circuit the blind retry for an error
// class it handles itself: when stopRetry(err) is true, RetryBatch returns that
// error immediately instead of re-running the SAME stream up to TransferRetries
// times. The maildir copy uses it for "a source file vanished mid-copy" — re-running
// the stale file list cannot help, so the caller re-scans and re-plans instead of
// wasting attempts. When absent or it returns false, the normal retry applies.
func RetryBatch(ctx context.Context, label string, timeout time.Duration, addBytes func(int64), stream func(ctx context.Context, onBytes func(int64)) error, stopRetry ...func(error) bool) error {
	var lastErr error
	for attempt := 1; attempt <= TransferRetries; attempt++ {
		bctx, stop, resetStall := StallContext(ctx, timeout)
		var attemptBytes int64
		onBytes := func(n int64) {
			resetStall()
			attemptBytes += n
			if addBytes != nil {
				addBytes(n)
			}
		}
		logx.Debug("%s: attempt %d/%d", label, attempt, TransferRetries)
		err := stream(bctx, onBytes)
		stop()
		if err == nil {
			return nil
		}
		// A stall-kill cancels bctx (the per-attempt child) with the ErrStalled
		// cause, NOT the parent ctx — so without this branch it surfaces as a
		// generic "attempt failed: use of closed connection", indistinguishable
		// from a real network drop or a Ctrl-C. Detect it, log it AS a stall (with
		// the per-item label and the idle window), and wrap lastErr so the final
		// returned error names the stall instead of the unblocked-copy noise.
		if context.Cause(bctx) == ErrStalled && ctx.Err() == nil {
			logx.Debug("%s: attempt %d STALLED — no progress for %s; aborting and retrying (unblocked with: %v)",
				label, attempt, timeout, err)
			lastErr = fmt.Errorf("%w (no progress for %s; last I/O error: %v)", ErrStalled, timeout, err)
		} else {
			lastErr = err
			logx.Debug("%s: attempt %d failed: %v", label, attempt, err)
		}
		if addBytes != nil && attemptBytes > 0 {
			addBytes(-attemptBytes) // roll back the failed attempt so the bar doesn't over-count
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The caller signalled this error class is not worth a blind retry (it will
		// recover differently, e.g. by re-scanning the source). Return now instead of
		// re-running the identical attempt.
		if len(stopRetry) > 0 && stopRetry[0] != nil && stopRetry[0](lastErr) {
			logx.Debug("%s: attempt %d error handled by caller (no blind retry): %v", label, attempt, lastErr)
			return lastErr
		}
		if attempt < TransferRetries {
			logx.Debug("%s: backing off %s before attempt %d/%d", label, backoffDelay(attempt), attempt+1, TransferRetries)
			if err := BackoffSleep(ctx, attempt); err != nil {
				return err // ctx cancelled during the backoff
			}
		}
	}
	// All attempts exhausted. Log the give-up explicitly so a PERSISTENT fault
	// (e.g. dest disk full, retried TransferRetries times with the same error) is
	// distinguishable in the log from a one-off that happened to recover.
	logx.Debug("%s: giving up after %d attempts; last error: %v", label, TransferRetries, lastErr)
	return lastErr
}

// RetryBackoffBase is the base delay before a retried transfer attempt. It is a
// var (not a const) so tests that exercise the retry path can shrink it to 0 to
// stay fast.
var RetryBackoffBase = 500 * time.Millisecond

// retryBackoffMax caps the per-attempt backoff.
const retryBackoffMax = 30 * time.Second

// backoffDelay is the wait before the attempt following a failed attempt n
// (1-based): a bounded exponential backoff base, 2*base, 4*base, … capped at
// retryBackoffMax. Returns 0 when disabled (n<1 or base<=0). Pure; unit-tested.
func backoffDelay(n int) time.Duration {
	if n < 1 || RetryBackoffBase <= 0 {
		return 0
	}
	d := RetryBackoffBase << (n - 1)
	if d > retryBackoffMax || d <= 0 { // d<=0 guards the shift overflowing for a huge n
		d = retryBackoffMax
	}
	return d
}

// BackoffSleep waits backoffDelay(n) before retrying, so a fast-failing batch
// (e.g. the server's MaxSessions exhausted) does not hammer the SSH server with
// back-to-back attempts. It returns ctx.Err() if the context is cancelled during
// the wait, so Ctrl-C is honored promptly instead of after the full delay.
func BackoffSleep(ctx context.Context, n int) error {
	d := backoffDelay(n)
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
