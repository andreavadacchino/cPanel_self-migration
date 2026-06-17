package sshx

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStallContextSetsStalledCause(t *testing.T) {
	ctx, stop, _ := StallContext(context.Background(), 40*time.Millisecond)
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("stall watchdog did not fire")
	}
	// The cause must be ErrStalled — that is what lets a watchdog abort be told
	// apart from a user Ctrl-C (which cancels the parent with context.Canceled).
	if cause := context.Cause(ctx); !errors.Is(cause, ErrStalled) {
		t.Errorf("cause = %v, want ErrStalled", cause)
	}
}

func TestStallContextStopCauseIsNotStalled(t *testing.T) {
	ctx, stop, _ := StallContext(context.Background(), time.Hour)
	stop()
	if errors.Is(context.Cause(ctx), ErrStalled) {
		t.Error("a normal stop() must not be reported as a stall")
	}
}

func TestStallContextCancelsWhenNoProgress(t *testing.T) {
	ctx, stop, _ := StallContext(context.Background(), 40*time.Millisecond)
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("stall context did not cancel after the idle period with no progress")
	}
}

func TestStallContextResetKeepsItAlive(t *testing.T) {
	// Generous margins so the test is not flaky under heavy parallel CI load: a
	// reset arrives every ~20ms but the idle window is 200ms (10x), and the loop
	// runs for 500ms (well past idle) to prove resets — not luck — keep it alive.
	ctx, stop, reset := StallContext(context.Background(), 200*time.Millisecond)
	defer stop()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		reset()
		if ctx.Err() != nil {
			t.Fatal("stall context cancelled despite regular progress")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Once progress stops, it must cancel.
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("stall context did not cancel after progress stopped")
	}
}

func TestStallContextZeroIdleIsNoOp(t *testing.T) {
	ctx, stop, reset := StallContext(context.Background(), 0)
	defer stop()
	reset() // must not panic
	time.Sleep(50 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatal("idle<=0 must disable the watchdog (never cancel)")
	}
}

func TestStallContextStopCancels(t *testing.T) {
	ctx, stop, _ := StallContext(context.Background(), time.Hour)
	stop()
	if ctx.Err() == nil {
		t.Fatal("stop must cancel the derived context")
	}
}
