package sshx

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetryBatchSucceedsFirstTry(t *testing.T) {
	calls := 0
	err := RetryBatch(context.Background(), "t", 0, nil, func(context.Context, func(int64)) error {
		calls++
		return nil
	})
	if err != nil || calls != 1 {
		t.Errorf("err=%v calls=%d, want nil and exactly 1 call", err, calls)
	}
}

func TestRetryBatchRetriesThenSucceeds(t *testing.T) {
	old := RetryBackoffBase
	RetryBackoffBase = 0 // no sleeps between attempts
	defer func() { RetryBackoffBase = old }()

	calls := 0
	err := RetryBatch(context.Background(), "t", 0, nil, func(context.Context, func(int64)) error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Errorf("err=%v calls=%d, want nil after 3 attempts", err, calls)
	}
}

func TestRetryBatchAllFailReturnsLastErr(t *testing.T) {
	old := RetryBackoffBase
	RetryBackoffBase = 0
	defer func() { RetryBackoffBase = old }()

	want := errors.New("nope")
	calls := 0
	err := RetryBatch(context.Background(), "t", 0, nil, func(context.Context, func(int64)) error {
		calls++
		return want
	})
	if !errors.Is(err, want) || calls != TransferRetries {
		t.Errorf("err=%v calls=%d, want %v after %d attempts", err, calls, want, TransferRetries)
	}
}

func TestRetryBatchRollsBackFailedAttemptBytes(t *testing.T) {
	old := RetryBackoffBase
	RetryBackoffBase = 0
	defer func() { RetryBackoffBase = old }()

	var total int64
	calls := 0
	_ = RetryBatch(context.Background(), "t", 0, func(n int64) { total += n },
		func(_ context.Context, onBytes func(int64)) error {
			calls++
			onBytes(50) // counted during the attempt...
			if calls < 2 {
				return errors.New("fail") // ...and rolled back when the attempt fails
			}
			return nil
		})
	// Attempt 1: +50 then -50 (rollback). Attempt 2: +50 (kept). Net 50, no over-count.
	if total != 50 {
		t.Errorf("net bytes = %d, want 50 (the failed attempt's bytes must be rolled back)", total)
	}
}

func TestRetryBatchReportsStallDistinctly(t *testing.T) {
	old := RetryBackoffBase
	RetryBackoffBase = 0
	defer func() { RetryBackoffBase = old }()

	calls := 0
	err := RetryBatch(context.Background(), "t", 30*time.Millisecond, nil,
		func(bctx context.Context, _ func(int64)) error {
			calls++
			<-bctx.Done() // never make progress: the stall watchdog must fire
			// The generic noise a real io.Copy unblocks with once the session is
			// torn down — must NOT mask the fact that this was a stall.
			return errors.New("use of closed connection")
		})
	if !errors.Is(err, ErrStalled) {
		t.Errorf("err=%v, want it to wrap ErrStalled so a stall is distinguishable from a generic failure", err)
	}
	if calls != TransferRetries {
		t.Errorf("calls=%d, want %d (a stall must be retried like any other failure)", calls, TransferRetries)
	}
}

func TestRetryBatchHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := RetryBatch(ctx, "t", 0, nil, func(context.Context, func(int64)) error {
		calls++
		return errors.New("fail")
	})
	if err == nil {
		t.Error("a cancelled context must surface as an error")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 — a cancelled ctx must stop further attempts", calls)
	}
}

func TestRetryBatchStopRetryShortCircuits(t *testing.T) {
	old := RetryBackoffBase
	RetryBackoffBase = 0
	defer func() { RetryBackoffBase = old }()

	want := errors.New("vanished")
	calls := 0
	err := RetryBatch(context.Background(), "t", 0, nil,
		func(context.Context, func(int64)) error {
			calls++
			return want
		},
		func(e error) bool { return errors.Is(e, want) }) // caller handles this class itself
	if !errors.Is(err, want) || calls != 1 {
		t.Errorf("err=%v calls=%d, want %v after exactly 1 attempt (stopRetry must skip the blind retries)", err, calls, want)
	}
}

func TestRetryBatchStopRetryFalseStillRetries(t *testing.T) {
	old := RetryBackoffBase
	RetryBackoffBase = 0
	defer func() { RetryBackoffBase = old }()

	calls := 0
	err := RetryBatch(context.Background(), "t", 0, nil,
		func(context.Context, func(int64)) error {
			calls++
			return errors.New("transient")
		},
		func(error) bool { return false }) // not our class -> normal retry
	if err == nil || calls != TransferRetries {
		t.Errorf("calls=%d, want %d when stopRetry returns false (normal retry applies)", calls, TransferRetries)
	}
}

func TestBackoffDelayGrowsAndCaps(t *testing.T) {
	old := RetryBackoffBase
	defer func() { RetryBackoffBase = old }()

	RetryBackoffBase = 100 * time.Millisecond
	for n, want := range map[int]time.Duration{
		1: 100 * time.Millisecond,
		2: 200 * time.Millisecond,
		3: 400 * time.Millisecond,
	} {
		if got := backoffDelay(n); got != want {
			t.Errorf("backoffDelay(%d) = %v, want %v", n, got, want)
		}
	}
	if got := backoffDelay(100); got != retryBackoffMax {
		t.Errorf("a large n must cap at %v, got %v", retryBackoffMax, got)
	}

	RetryBackoffBase = 0
	if got := backoffDelay(2); got != 0 {
		t.Errorf("base<=0 must disable backoff, got %v", got)
	}
}

func TestBackoffSleepHonorsContext(t *testing.T) {
	old := RetryBackoffBase
	RetryBackoffBase = time.Hour // would block ~forever if ctx were ignored
	defer func() { RetryBackoffBase = old }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- BackoffSleep(ctx, 1) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("BackoffSleep must return the context error when cancelled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("BackoffSleep ignored the cancelled context (still sleeping)")
	}
}
