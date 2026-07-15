package webui

import (
	"strings"
	"testing"
	"time"
)

// Reservation-conflict identity (finding R3 — TOCTOU between refusal and 409
// rendering). tryReserveFor/tryReserve observe the slot busy under jobManager.mu
// and return, releasing the lock; writeBusy409 then takes a FRESH lock to read
// the holder. Between the two, the holder that caused the refusal can release or
// be replaced, so the 409 names the wrong operation (or goes generic). The
// contract: a 409 describes the snapshot of the holder that caused THIS
// refusal, not whoever holds the slot a few microseconds later.
//
// These two tests simulate the TOCTOU outcome through the current re-reading
// renderer (busyMessage). They FAIL on 73e83ff and are rewritten onto the atomic
// conflict-snapshot API by the fix.

// TestReservationConflictStillNamesHolderAfterRelease — scenario A: A holds, B is
// refused by A, A releases before B renders. The 409 must still name A, not fall
// back to the generic message.
func TestReservationConflictStillNamesHolderAfterRelease(t *testing.T) {
	dir := t.TempDir()
	j := newJobManager(dir, nil, nil)

	startedA := time.Unix(1_700_000_000, 0).UTC()
	j.tryReserveFor("dns verify", startedA) // A holds; a concurrent B is refused by A here
	// TOCTOU window: A finishes and frees the slot before the refused B renders.
	j.release()

	msg := busyMessage(dir, j) // re-reads the now-free slot

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
	j.tryReserveFor("dns verify", startedA) // A holds; B is refused by A here
	// TOCTOU window: A releases and C acquires before the refused B renders.
	j.release()
	j.tryReserveFor("migrate content", startedC) // C acquires

	msg := busyMessage(dir, j) // re-reads the current holder C

	if !strings.Contains(msg, "dns verify") {
		t.Errorf("409 for a request refused by A no longer names A: %q", msg)
	}
	if strings.Contains(msg, "migrate content") {
		t.Errorf("409 switched to the replacement holder C: %q", msg)
	}
}
