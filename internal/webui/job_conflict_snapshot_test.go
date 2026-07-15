package webui

import (
	"strings"
	"testing"
	"time"
)

// Reservation-conflict identity (finding R3 — TOCTOU between refusal and 409
// rendering). tryReserveFor/tryReserve/start now capture the blocker into an
// IMMUTABLE slotConflict under the SAME lock that observes the slot busy, and the
// 409 renderer uses only that snapshot — never a fresh re-read of the jobManager.
// So a 409 describes the holder that caused THAT refusal, even if the slot is
// released or handed to another holder microseconds later.

// reserveThenConflict reserves the slot for (action, startedAt), then observes
// the conflict a second, refused reservation captures — exactly the immutable
// snapshot a concurrent 409 renders.
func reserveThenConflict(t *testing.T, j *jobManager, action string, startedAt time.Time) slotConflict {
	t.Helper()
	if acquired, _ := j.tryReserveFor(action, startedAt); !acquired {
		t.Fatalf("tryReserveFor(%q) on a free slot must succeed", action)
	}
	acquired, conflict := j.tryReserve()
	if acquired {
		t.Fatal("second reservation on a busy slot must be refused")
	}
	return conflict
}

// TestReservationConflictStillNamesHolderAfterRelease — scenario A: A holds, B is
// refused by A, A releases before B renders. The 409 must still name A, not fall
// back to the generic message.
func TestReservationConflictStillNamesHolderAfterRelease(t *testing.T) {
	dir := t.TempDir()
	j := newJobManager(dir, nil, nil)

	startedA := time.Unix(1_700_000_000, 0).UTC()
	conflict := reserveThenConflict(t, j, "dns verify", startedA) // B refused by A → snapshot of A
	j.release()                                                   // A frees the slot in the TOCTOU window

	msg := busyMessageForConflict(dir, conflict) // renders from the snapshot, not the live slot

	if !strings.Contains(msg, "dns verify") {
		t.Errorf("409 for a request refused by A no longer names A after A released: %q", msg)
	}
	if strings.Contains(msg, "Un'operazione è già in corso") {
		t.Errorf("409 for a request refused by A fell back to the generic message: %q", msg)
	}
}

// TestReservationConflictDoesNotSwitchToReplacementHolder — scenario B: A holds,
// B is refused by A, A releases and C acquires before B renders. The 409 must
// name A (who caused the refusal), never C (who took the slot afterwards).
func TestReservationConflictDoesNotSwitchToReplacementHolder(t *testing.T) {
	dir := t.TempDir()
	j := newJobManager(dir, nil, nil)

	startedA := time.Unix(1_700_000_000, 0).UTC()
	startedC := time.Unix(1_700_009_999, 0).UTC()
	conflict := reserveThenConflict(t, j, "dns verify", startedA) // B refused by A → snapshot of A
	j.release()                                                   // A frees
	if acquired, _ := j.tryReserveFor("migrate content", startedC); !acquired {
		t.Fatal("C must acquire the freed slot")
	}

	msg := busyMessageForConflict(dir, conflict) // renders A, not the replacement C

	if !strings.Contains(msg, "dns verify") {
		t.Errorf("409 for a request refused by A no longer names A: %q", msg)
	}
	if strings.Contains(msg, "migrate content") {
		t.Errorf("409 switched to the replacement holder C: %q", msg)
	}
}
