package webui

import (
	"strings"
	"testing"
	"time"
)

// busyMessageForConflict rendering — the named-conflict enrichment rules. The
// blocker identity always comes from the immutable conflict snapshot; the on-disk
// journal may only ENRICH a named conflict with a phase when it is the same run.

// TestBusyMessagePrefersLiveHolderOverStaleRunningJournal: a stale running
// journal for a DIFFERENT action never replaces the conflict's named identity.
func TestBusyMessagePrefersLiveHolderOverStaleRunningJournal(t *testing.T) {
	dir := t.TempDir()

	// Stale journal: a previous action left a running record on disk.
	staleStart := time.Unix(1_700_000_000, 0).UTC()
	if err := writeJobJournal(dir, jobJournal{
		SessionID: "mig_x", Action: "dns verify", State: jobStateRunning,
		StartedAt: staleStart, UpdatedAt: staleStart, Phase: "dns verify",
	}); err != nil {
		t.Fatalf("writeJobJournal: %v", err)
	}

	// The conflict blocker is a different action with a different started-at.
	j := newJobManager(dir, nil, nil)
	liveStart := time.Unix(1_700_009_999, 0).UTC()
	conflict := reserveThenConflict(t, j, "migrate content", liveStart)

	msg := busyMessageForConflict(dir, conflict)

	if !strings.Contains(msg, "migrate content") {
		t.Errorf("409 does not name the conflict holder: %q", msg)
	}
	if strings.Contains(msg, "dns verify") {
		t.Errorf("409 named the STALE journal action instead of the conflict holder: %q", msg)
	}
}

// TestBusyMessageUsesMatchingJournalPhaseForLiveHolder: a journal that IS the
// same run as the conflict (action + started-at) still supplies the richer phase.
func TestBusyMessageUsesMatchingJournalPhaseForLiveHolder(t *testing.T) {
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

	msg := busyMessageForConflict(dir, conflict)

	if !strings.Contains(msg, "migrate content") {
		t.Errorf("409 does not name the running action: %q", msg)
	}
	if !strings.Contains(msg, "rsync home") {
		t.Errorf("coherent journal phase dropped — fell back to the phase-less conflict: %q", msg)
	}
}

// TestBusyMessageEmptyHolderActionIsAnonymous: a reserved holder with an EMPTY
// action is not nameable, so its conflict is anonymous and renders the generic
// message. The journal is NOT consulted to resurrect a previous run's identity
// for an unnamed holder (finding R3, §7). tryReserveFor stays permissive because
// every production call site passes a validated non-empty constant.
func TestBusyMessageEmptyHolderActionIsAnonymous(t *testing.T) {
	dir := t.TempDir()
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	if err := writeJobJournal(dir, jobJournal{
		SessionID: "mig_x", Action: "dns apply", State: jobStateRunning,
		StartedAt: startedAt, UpdatedAt: startedAt, Phase: "dns apply",
	}); err != nil {
		t.Fatalf("writeJobJournal: %v", err)
	}

	j := newJobManager(dir, nil, nil)
	conflict := reserveThenConflict(t, j, "", startedAt) // empty action (defensive)

	msg := busyMessageForConflict(dir, conflict)

	if strings.Contains(msg, "«»") {
		t.Errorf("empty holder action rendered as «»: %q", msg)
	}
	if strings.Contains(msg, "dns apply") {
		t.Errorf("anonymous conflict pulled an action from the journal: %q", msg)
	}
	if !strings.Contains(msg, "Un'operazione è già in corso") {
		t.Errorf("empty holder action did not render the generic message: %q", msg)
	}
}
