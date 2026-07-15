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

// TestReservationConflictUsesMatchingJournalPhase (§12): a coherent journal
// enriches the named conflict with its phase, and keeps working AFTER the live
// holder is released — the renderer uses the snapshot + journal, never the slot.
func TestReservationConflictUsesMatchingJournalPhase(t *testing.T) {
	dir := t.TempDir()
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	if err := writeJobJournal(dir, jobJournal{
		SessionID: "mig_x", Action: "migrate content", State: jobStateRunning,
		StartedAt: startedAt, UpdatedAt: startedAt, Phase: "rsync home",
	}); err != nil {
		t.Fatalf("writeJobJournal: %v", err)
	}
	j := newJobManager(dir, nil, nil)
	conflict := reserveThenConflict(t, j, "migrate content", startedAt)
	j.release() // the live holder is gone; the journal + snapshot remain

	msg := busyMessageForConflict(dir, conflict)
	if !strings.Contains(msg, "migrate content") {
		t.Errorf("409 does not name the conflict action: %q", msg)
	}
	if !strings.Contains(msg, "rsync home") {
		t.Errorf("coherent journal phase dropped after release: %q", msg)
	}
}

// TestReservationConflictIgnoresMismatchedJournal (§13): a running journal for a
// DIFFERENT run (action + started-at) never replaces the conflict identity nor
// leaks its phase.
func TestReservationConflictIgnoresMismatchedJournal(t *testing.T) {
	dir := t.TempDir()
	journalStart := time.Unix(1_700_009_999, 0).UTC()
	if err := writeJobJournal(dir, jobJournal{
		SessionID: "mig_x", Action: "migrate content", State: jobStateRunning,
		StartedAt: journalStart, UpdatedAt: journalStart, Phase: "rsync home",
	}); err != nil {
		t.Fatalf("writeJobJournal: %v", err)
	}
	j := newJobManager(dir, nil, nil)
	conflictStart := time.Unix(1_700_000_000, 0).UTC()
	conflict := reserveThenConflict(t, j, "dns verify", conflictStart)
	j.release()

	msg := busyMessageForConflict(dir, conflict)
	if !strings.Contains(msg, "dns verify") {
		t.Errorf("409 does not name the conflict action: %q", msg)
	}
	if strings.Contains(msg, "migrate content") {
		t.Errorf("409 leaked the mismatched journal action: %q", msg)
	}
	if strings.Contains(msg, "rsync home") {
		t.Errorf("409 leaked the mismatched journal phase: %q", msg)
	}
}

// TestReservationConflictAnalysisSurvivesCompletion (§14): the analysis pipeline
// holds the slot, a concurrent reservation is refused and snapshots the analysis
// blocker; the analysis then completes and frees the slot, but the immutable
// snapshot still renders "Un'analisi è in corso". White-box: the analysis state
// is set exactly as start() sets it (busy + running status, no reserved holder).
func TestReservationConflictAnalysisSurvivesCompletion(t *testing.T) {
	dir := t.TempDir()
	j := newJobManager(dir, nil, nil)

	j.mu.Lock()
	j.busy = true
	j.status = jobStatus{State: "running", StartedAt: "2026-01-01 12:00:00 UTC"}
	j.mu.Unlock()

	acquired, conflict := j.tryReserve()
	if acquired {
		t.Fatal("expected refusal while the analysis pipeline holds the slot")
	}
	if conflict.kind != slotHolderAnalysis {
		t.Fatalf("conflict kind = %v, want analysis", conflict.kind)
	}

	// The analysis completes and frees the slot under us.
	j.release()
	if j.running() {
		t.Fatal("precondition: slot should be free after release")
	}

	msg := busyMessageForConflict(dir, conflict)
	if !strings.Contains(msg, "Un'analisi è in corso") {
		t.Errorf("analysis conflict lost its identity after completion: %q", msg)
	}
	if !strings.Contains(msg, "12:00:00") {
		t.Errorf("analysis conflict lost its started-at: %q", msg)
	}
}
