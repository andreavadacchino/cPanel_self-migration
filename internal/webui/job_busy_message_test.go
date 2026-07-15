package webui

import (
	"strings"
	"testing"
	"time"
)

// busyMessage precedence tests. The single-writer slot's 409 must always name
// the action REALLY holding the slot. Two live sources can disagree:
//
//   - the in-memory reservedHolder, published atomically with the busy flag;
//   - the on-disk job journal, whose terminal writes are best-effort and can
//     therefore be STALE (a previous action whose finishJobJournal write lost).
//
// The live holder is the truth during a live process; the journal is only the
// richer, durable view when it is the SAME run (action + started-at match).

// TestBusyMessagePrefersLiveHolderOverStaleRunningJournal reproduces finding F1:
// action A wrote a running journal, terminated, but its terminal journal write
// was lost (best-effort); the slot was then re-acquired by action B. A
// concurrent 409 must name B (the real holder), never the stale journal A.
//
// Pre-fix (journal consulted before the live holder) this FAILS: the message
// names "dns verify" (stale A). Post-fix it names "migrate content" (live B).
func TestBusyMessagePrefersLiveHolderOverStaleRunningJournal(t *testing.T) {
	dir := t.TempDir()

	// Stale journal: action A left a running record on disk (its terminal write
	// was best-effort and never landed).
	staleStart := time.Unix(1_700_000_000, 0).UTC()
	if err := writeJobJournal(dir, jobJournal{
		SessionID: "mig_x", Action: "dns verify", State: jobStateRunning,
		StartedAt: staleStart, UpdatedAt: staleStart, Phase: "dns verify",
	}); err != nil {
		t.Fatalf("writeJobJournal: %v", err)
	}

	// Live holder: action B took the slot with a DIFFERENT started-at.
	j := newJobManager(dir, nil, nil)
	liveStart := time.Unix(1_700_009_999, 0).UTC()
	if !j.tryReserveFor("migrate content", liveStart) {
		t.Fatal("tryReserveFor on a free slot must succeed")
	}

	msg := busyMessage(dir, j)

	if !strings.Contains(msg, "migrate content") {
		t.Errorf("409 does not name the live holder B: %q", msg)
	}
	if strings.Contains(msg, "dns verify") {
		t.Errorf("409 names the STALE journal action A instead of the live holder B: %q", msg)
	}
}

// TestBusyMessageUsesMatchingJournalPhaseForLiveHolder proves the flip side: a
// running journal that IS the same run as the live holder (same action + same
// started-at) is still trusted to supply the richer phase, so the fix does not
// regress the "«…» già in corso (fase …)" detail for the normal path.
func TestBusyMessageUsesMatchingJournalPhaseForLiveHolder(t *testing.T) {
	dir := t.TempDir()

	// The orchestrator/exec reserves and journals with the SAME started-at, then
	// journalPhaseRunning advances the phase (here: "rsync home").
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	if err := writeJobJournal(dir, jobJournal{
		SessionID: "mig_x", Action: "migrate content", State: jobStateRunning,
		StartedAt: startedAt, UpdatedAt: startedAt, Phase: "rsync home",
	}); err != nil {
		t.Fatalf("writeJobJournal: %v", err)
	}

	j := newJobManager(dir, nil, nil)
	if !j.tryReserveFor("migrate content", startedAt) {
		t.Fatal("tryReserveFor on a free slot must succeed")
	}

	msg := busyMessage(dir, j)

	if !strings.Contains(msg, "migrate content") {
		t.Errorf("409 does not name the running action: %q", msg)
	}
	if !strings.Contains(msg, "rsync home") {
		t.Errorf("coherent journal phase dropped — fell back to the phase-less live holder: %q", msg)
	}
}

// TestBusyMessageEmptyHolderActionFallsThrough documents the fail-closed guard:
// a reserved holder with an EMPTY action is not nameable, so busyMessage does
// not print «» and instead falls through to the next source (here a running
// journal). tryReserveFor stays permissive because every production call site
// passes a validated non-empty constant (actionRegistry name / orchestratorAction);
// this guard is the defensive backstop at the display layer.
func TestBusyMessageEmptyHolderActionFallsThrough(t *testing.T) {
	dir := t.TempDir()
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	if err := writeJobJournal(dir, jobJournal{
		SessionID: "mig_x", Action: "dns apply", State: jobStateRunning,
		StartedAt: startedAt, UpdatedAt: startedAt, Phase: "dns apply",
	}); err != nil {
		t.Fatalf("writeJobJournal: %v", err)
	}

	j := newJobManager(dir, nil, nil)
	if !j.tryReserveFor("", startedAt) { // empty action (defensive)
		t.Fatal("tryReserveFor on a free slot must succeed")
	}

	msg := busyMessage(dir, j)

	if strings.Contains(msg, "«»") {
		t.Errorf("empty holder action rendered as «» instead of falling through: %q", msg)
	}
	if !strings.Contains(msg, "dns apply") {
		t.Errorf("empty holder action did not fall through to the running journal: %q", msg)
	}
}
