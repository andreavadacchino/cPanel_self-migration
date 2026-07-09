package webui

// Platform UI V2 — read-model unit tests. Fast, exact, no HTTP. They pin the
// honest-data invariants: every status maps to exactly one dashboard bucket and
// one stepper position, the dashboard never derives per-session artifact facts,
// and the session read-model reuses the SAME start gate as the workbench
// cockpit (so the two can never disagree).

import (
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// Every status lands in exactly one of the four dashboard buckets, and the
// stat tiles partition the session set (counts sum to the total).
func TestStatusBucketPartition(t *testing.T) {
	valid := map[string]bool{"active": true, "waiting": true, "done": true, "manual": true}
	var sessions []workbench.Session
	for _, s := range workbench.AllStatuses {
		if b := statusBucket(s); !valid[b] {
			t.Errorf("status %q → bucket %q (not one of the four tiles)", s, b)
		}
		sessions = append(sessions, workbench.Session{Status: s})
	}
	stats := buildPlatformStats(sessions)
	total := 0
	for _, st := range stats {
		total += st.Count
	}
	if total != len(sessions) {
		t.Errorf("stat tiles count %d sessions, want %d (every session in exactly one bucket)", total, len(sessions))
	}
}

// The stepper position is always within 1..7 for every valid status.
func TestStatusStepIndexInRange(t *testing.T) {
	for _, s := range workbench.AllStatuses {
		idx := statusStepIndex(s)
		if idx < 1 || idx > len(platformStepDefs) {
			t.Errorf("status %q → step %d, out of 1..%d", s, idx, len(platformStepDefs))
		}
	}
}

// Every status yields a non-empty, operator-facing next-action label and a
// valid platform sub-route segment.
func TestDashboardNextActionTotal(t *testing.T) {
	valid := map[string]bool{"": true, "plan": true, "tasks": true, "report": true}
	for _, s := range workbench.AllStatuses {
		label, seg := dashboardNextAction(s)
		if label == "" {
			t.Errorf("status %q → empty next-action label", s)
		}
		if !valid[seg] {
			t.Errorf("status %q → invalid platform segment %q", s, seg)
		}
	}
}

// The dashboard rows carry honest fields: an em-dash for a missing profile, the
// domain falling back to the name, newest session first.
func TestBuildPlatformRowsHonest(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	sessions := []workbench.Session{
		{ID: "a", Name: "primo", Status: workbench.StatusDraft, CreatedAt: t0, UpdatedAt: t0}, // no profiles
		{ID: "b", Name: "secondo", SourceProfile: "acc@src", DestinationProfile: "acc@dst",
			Status: workbench.StatusApplyInProgress, CreatedAt: t0.Add(time.Hour), UpdatedAt: t0.Add(time.Hour),
			Setup: &workbench.SetupMeta{PrimaryDomain: "esempio.it"}},
	}
	rows := buildPlatformRows(sessions)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	// Newest first: store.List is ascending, buildPlatformRows reverses.
	if rows[0].ID != "b" {
		t.Errorf("newest-first broken: rows[0]=%q, want b", rows[0].ID)
	}
	if rows[0].Domain != "esempio.it" {
		t.Errorf("domain should prefer Setup.PrimaryDomain, got %q", rows[0].Domain)
	}
	if rows[1].Domain != "primo" {
		t.Errorf("domain should fall back to Name, got %q", rows[1].Domain)
	}
	if rows[1].Source != "—" || rows[1].Dest != "—" {
		t.Errorf("missing profile must render em-dash, got %q/%q", rows[1].Source, rows[1].Dest)
	}
}

// Activity aggregates timeline events across sessions, newest first, bounded.
func TestBuildActivityNewestFirstBounded(t *testing.T) {
	base := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	var sessions []workbench.Session
	for i := 0; i < 20; i++ {
		sessions = append(sessions, workbench.Session{
			ID:   "s",
			Name: "mig",
			Timeline: []workbench.TimelineEvent{
				{Timestamp: base.Add(time.Duration(i) * time.Minute), Action: "status_change", ToStatus: workbench.StatusPreflightRequired},
			},
		})
	}
	act := buildActivity(sessions)
	if len(act) != activityMaxRows {
		t.Fatalf("activity len = %d, want bounded to %d", len(act), activityMaxRows)
	}
	for i := 1; i < len(act); i++ {
		if act[i-1].When < act[i].When {
			t.Errorf("activity not newest-first at %d: %q before %q", i, act[i-1].When, act[i].When)
		}
	}
}

// A session with no checklist degrades honestly: the plan is not ready with a
// plain-language message, and the comparison is empty (no invented rows).
func TestBuildPlatformSessionNoChecklist(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "acc@src", "acc@dst", time.Now())

	page := buildPlatformSession(sess.ArtifactDir, "", "csrf-x", sess, false, "cockpit")
	if page.Plan.Ready {
		t.Error("plan must not be ready without a checklist")
	}
	if page.Plan.NotReadyMessage == "" {
		t.Error("a not-ready plan must carry an honest message")
	}
	if len(page.Compare) != 0 {
		t.Errorf("comparison must be empty without a checklist, got %d rows", len(page.Compare))
	}
	if len(page.Steps) != len(platformStepDefs) {
		t.Errorf("stepper must have %d steps, got %d", len(platformStepDefs), len(page.Steps))
	}
	if page.ExpertURL != "/workbench/session/"+sess.ID+"?mode=expert" {
		t.Errorf("expert URL = %q, want the workbench expert route", page.ExpertURL)
	}
}

// The session read-model reuses the SAME start gate as the workbench cockpit:
// for a startable session the dominant CTA is "start". The cockpit renders the
// strong-confirmation form inline (in-platform), so no link target is set.
func TestBuildPlatformSessionReusesStartGate(t *testing.T) {
	env := newOrchEnv(t, workbench.ContentSelection{Files: true, Databases: true})
	sess, err := env.store.Get(env.sessID)
	if err != nil {
		t.Fatal(err)
	}
	page := buildPlatformSession(sess.ArtifactDir, "", env.csrf, sess, false, "cockpit")
	if page.Cockpit.CTA.Kind != "start" {
		t.Fatalf("startable session must yield a start CTA, got kind %q (state %q)", page.Cockpit.CTA.Kind, page.Cockpit.StateLabel)
	}
	if page.HeroCTAURL != "" {
		t.Errorf("start CTA renders an inline form, so HeroCTAURL must be empty, got %q", page.HeroCTAURL)
	}
}

// A draft session is NOT startable: the CTA is never "start" and the hero never
// claims "Pronta per migrare" (hero and CTA both derive from the shared gate).
func TestBuildPlatformSessionDraftNotStartable(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "acc@src", "acc@dst", time.Now())
	page := buildPlatformSession(sess.ArtifactDir, "", "csrf-x", sess, false, "cockpit")
	if page.Cockpit.CTA.Kind == "start" {
		t.Error("a draft session must never expose a start CTA")
	}
	if page.Cockpit.StateLabel == "Pronta per migrare" {
		t.Error("a draft session must never claim it is ready to migrate")
	}
}

func TestBuildPlatformSessionReadyPlanButUnconfirmedScopeHighlightsScopeStep(t *testing.T) {
	dir := t.TempDir()
	h, store := wizardHandler(t, dir)
	_ = h
	setup := &workbench.SetupMeta{
		Source:      workbench.Endpoint{Host: "1.1.1.1", Account: "src"},
		Destination: workbench.Endpoint{Host: "2.2.2.2", Account: "dst"},
		Content:     workbench.ContentSelection{Files: true, Databases: true},
	}
	sess := wizardSession(t, store, "giorgini", setup)
	var err error
	sess, err = store.SetStatus(sess.ID, workbench.StatusPreflightRequired, true, "test preflight step", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	writeChecklist(t, sess.ArtifactDir, readyChecklist())
	page := buildPlatformSession(sess.ArtifactDir, "", "csrf-x", sess, false, "cockpit")
	if page.CurrentStepIndex != 4 {
		t.Fatalf("CurrentStepIndex = %d, want 4 when the plan is ready but the scope is not confirmed", page.CurrentStepIndex)
	}
	if !page.Steps[3].Current || page.Steps[3].Label != "Scope" {
		t.Fatalf("current step = %+v, want Scope", page.Steps[3])
	}
}
