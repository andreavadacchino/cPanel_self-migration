package sshx

import (
	"context"
	"errors"
	"time"
)

// DefaultStallTimeout is a sensible idle timeout for the tar/mysql stream
// transfers: if no bytes flow for this long the attempt is considered wedged and
// is aborted so the batch-retry machinery can re-try it. Long enough not to
// false-fire on a slow link, short enough to recover within the retry budget.
const DefaultStallTimeout = 2 * time.Minute

// ErrStalled is the cancellation cause set on the derived context when the idle
// watchdog aborts a transfer that made no progress for the timeout. Retrieve it
// with context.Cause(ctx). Without it, a watchdog-driven abort is reported as a
// generic "use of closed connection" — indistinguishable from a network drop or
// a user Ctrl-C (which cancels the PARENT with context.Canceled). Callers test
// for it to report the stall explicitly instead of as a nondescript failure.
var ErrStalled = errors.New("transfer stalled: no progress within the idle timeout")

// StallContext derives a context that is cancelled if reset is not called for
// `idle` — a stall (idle) watchdog for a streaming transfer. Call reset on every
// unit of progress (e.g. each onBytes callback) to restart the clock, so a
// long-but-healthy transfer is NEVER cancelled; only one that has made no progress
// for `idle` is. This is preferable to a fixed wall-clock deadline, which would
// kill a legitimately large/slow batch.
//
// The returned stop stops the watchdog and cancels the context; call it (e.g. via
// defer) when the operation finishes, to release resources. reset is safe to call
// frequently and from the single goroutine that reports progress.
//
// idle <= 0 disables the watchdog: the parent context is returned unchanged with
// no-op stop/reset, so a caller can opt out by passing 0.
func StallContext(parent context.Context, idle time.Duration) (ctx context.Context, stop context.CancelFunc, reset func()) {
	if idle <= 0 {
		return parent, func() {}, func() {}
	}
	ctx, cancel := context.WithCancelCause(parent)
	// AfterFunc fires once `idle` elapses with no reset, cancelling with the
	// ErrStalled cause so the caller can tell a watchdog abort apart from a user
	// Ctrl-C (context.Canceled). Each reset reschedules it. stop() halts the timer
	// and cancels with context.Canceled so the context is always released. cancel
	// is idempotent and the FIRST cause wins, so a reset/stop racing a fire is
	// harmless: a real stall (timer fires first) keeps the ErrStalled cause even
	// though stop() later cancels again.
	timer := time.AfterFunc(idle, func() { cancel(ErrStalled) })
	stop = func() {
		timer.Stop()
		cancel(context.Canceled)
	}
	reset = func() { timer.Reset(idle) }
	return ctx, stop, reset
}
