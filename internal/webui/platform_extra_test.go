package webui

// Platform UI V2 — audit follow-up tests (2026-07-07 skeptical re-examination).
// These close render-branch coverage gaps the first suite missed and pin the
// Italian-date invariant.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// Dates in the operator UI must be Italian (mockups show "20 mag 2025"), never
// English month abbreviations ("Jul"/"May").
func TestPlatformHumanTimeItalian(t *testing.T) {
	got := humanTime(time.Date(2026, 7, 7, 9, 16, 0, 0, time.UTC))
	if !strings.Contains(got, "lug") {
		t.Errorf("humanTime = %q, want an Italian month abbreviation (lug)", got)
	}
	if strings.Contains(got, "Jul") {
		t.Errorf("humanTime = %q, must not leak an English month into the Italian UI", got)
	}
	// May → mag (the trickiest: English 3-letter == Italian? no, mag).
	if m := humanTime(time.Date(2025, 5, 20, 8, 32, 0, 0, time.UTC)); !strings.Contains(m, "mag") || strings.Contains(m, "May") {
		t.Errorf("May date = %q, want 'mag'", m)
	}
}

// The /tasks screen must render the manual-action rows (not just the empty
// state): title, blocking-cutover badge, accepted state, and the pending count.
func TestPlatformTasksScreenWithRows(t *testing.T) {
	dir := t.TempDir()
	ps, store := newPlatformTest(t, dir, false)
	sess, _ := store.Create("giorgini", "acc@src", "acc@dst", time.Now())
	writeChecklist(t, dir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallReadyWithManualNotes,
		ManualActions: []accountinventory.ManualAction{
			{Title: "Verifica zone DNS", OperatorAction: "Confronta le zone.", Section: "dns",
				BlockingCutover: true, Acceptable: true, Accepted: false, Detail: "12 zone -> 12 zone"},
			{Title: "Verifica certificati SSL", OperatorAction: "Controlla i certificati.", Section: "ssl",
				BlockingCutover: false, Acceptable: true, Accepted: true},
		},
	})
	body := platformSessionBody(t, ps, sess.ID, "tasks")
	for _, want := range []string{"Verifica zone DNS", "Bloccante cutover", "12 zone -&gt; 12 zone",
		"Da verificare", "Confermato", "Verifica certificati SSL"} {
		if !strings.Contains(body, want) {
			t.Errorf("/tasks with rows missing %q", want)
		}
	}
	if strings.Contains(body, "Nessun task manuale") {
		t.Error("/tasks with rows must not show the empty state")
	}
}

// The /report screen for a completed migration must render the success banner
// with the duration, not the not-yet-completed callout.
func TestPlatformReportCompletedBanner(t *testing.T) {
	dir := t.TempDir()
	ps, store := newPlatformTest(t, dir, false)
	created := time.Date(2026, 7, 7, 9, 0, 0, 0, time.UTC)
	sess, _ := store.Create("giorgini", "acc@src", "acc@dst", created)
	// Force to cutover_done (a forced transition needs a >=10 char reason).
	if _, err := store.SetStatus(sess.ID, workbench.StatusCutoverDone, true, "test completamento migrazione", created.Add(28*time.Minute)); err != nil {
		t.Fatal(err)
	}
	body := platformSessionBody(t, ps, sess.ID, "report")
	if !strings.Contains(body, "Migrazione completata") {
		t.Error("completed report must show the success banner")
	}
	if strings.Contains(body, "non è ancora completata") {
		t.Error("completed report must not show the not-completed callout")
	}
	if !strings.Contains(body, "28 min") {
		t.Errorf("completed report must show a duration; body lacked it")
	}
}

// CSRF-negative parity: the platform wizard POST, the only mutating platform
// route, must reject a missing/wrong CSRF token with 403 and create no session
// (same guard as /config, /accept, /workbench/.../exec).
func TestPlatformWizardRequiresCSRF(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	h, err := New(Options{Dir: dir, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"csrf": {"not-the-real-token"}, "name": {"X"},
		"src_host": {"1.1.1.1"}, "src_account": {"a"},
		"dst_host": {"2.2.2.2"}, "dst_account": {"b"}, "content_files": {"1"},
	}
	rr := doReq(h, http.MethodPost, "/platform/migrations/new", form)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("wizard POST with bad csrf = %d, want 403", rr.Code)
	}
	if sessions, _, _ := store.List(); len(sessions) != 0 {
		t.Errorf("a CSRF-rejected wizard must not create a session, got %d", len(sessions))
	}
}

// The workbench emits Action "attach_artifact" (see workbench/artifacts.go),
// NOT "artifact_attached". The report timeline and the activity feed must
// localize it, never leak the raw enum into the Italian UI.
func TestPlatformAttachArtifactLocalized(t *testing.T) {
	te := workbench.TimelineEvent{Action: "attach_artifact"}
	if title, _ := activityTitle(te); title != "Report allegato" {
		t.Errorf("activityTitle(attach_artifact) = %q, want 'Report allegato'", title)
	}
	rows := buildReportTimeline([]workbench.TimelineEvent{te})
	if len(rows) != 1 || rows[0].Label != "Report allegato" {
		t.Fatalf("report timeline for attach_artifact = %+v, want label 'Report allegato'", rows)
	}
	if strings.Contains(rows[0].Label, "attach_artifact") {
		t.Error("raw enum attach_artifact leaked into the Italian report timeline")
	}
}

// Honesty: a non-acceptable manual action (Acceptable=false — e.g. a blocking
// cron recreation the engine says must be resolved, not waved through) can NEVER
// be marked done via acceptance, so it must count as REMAINING, never silently
// as "completed". Otherwise the "N task manuali / X/Y completati" summary
// overstates completion and contradicts the table below it.
func TestPlatformTasksPendingCountsNonAcceptable(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	writeChecklist(t, dir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallBlocked,
		ManualActions: []accountinventory.ManualAction{
			{Title: "Ricrea cron bloccante", Section: "cron", BlockingCutover: true, Acceptable: false, Accepted: false},
		},
	})
	page := buildPlatformSession(dir, "csrf", sess, false, "tasks")
	if page.TasksTotal != 1 {
		t.Fatalf("TasksTotal = %d, want 1", page.TasksTotal)
	}
	if page.TasksPending != 1 {
		t.Errorf("TasksPending = %d, want 1 — a non-acceptable blocking task must count as remaining, not completed", page.TasksPending)
	}
}

// Coverage: the comparison renders REAL source/dest counts (the HasCount==true
// numeric branch, never exercised elsewhere) and the /compare table/areas, not
// the empty state.
func TestPlatformCompareWithCounts(t *testing.T) {
	dir := t.TempDir()
	ps, store := newPlatformTest(t, dir, false)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	writeChecklist(t, dir, countingChecklist()) // databases 32/0, mailboxes 12/12, dns 5/5
	body := platformSessionBody(t, ps, sess.ID, "compare")
	for _, want := range []string{"Database", "32", "Confronto per area"} {
		if !strings.Contains(body, want) {
			t.Errorf("/compare with counts missing %q (HasCount-true numeric path)", want)
		}
	}
	if strings.Contains(body, "Confronto non ancora disponibile") {
		t.Error("/compare with a checklist must render the table, not the empty state")
	}
}

// Coverage: the cockpit's inline "Task manuali aperti" list renders rows.
func TestPlatformCockpitTaskListWithRows(t *testing.T) {
	dir := t.TempDir()
	ps, store := newPlatformTest(t, dir, false)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	writeChecklist(t, dir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallReadyWithManualNotes,
		ManualActions: []accountinventory.ManualAction{
			{Title: "Verifica instradamento MX", Section: "dns", BlockingCutover: true, Acceptable: true},
		},
	})
	body := platformSessionBody(t, ps, sess.ID, "cockpit")
	if !strings.Contains(body, "Verifica instradamento MX") {
		t.Error("cockpit 'Task manuali aperti' must list the manual action rows")
	}
}

// Honesty guard: manual_actions_required precedes ready_for_apply/apply, so the
// stepper must NOT render a later phase ("Migrazione") as completed — that would
// falsely claim the migration already ran. (This pins the reason the status is
// deliberately kept at the pre-migration "Scope" step rather than the later
// "Task manuali" step, even though its work is shown on the /tasks screen.)
func TestPlatformStepperNoPrematureMigrationDone(t *testing.T) {
	steps := buildPlatformSteps(workbench.StatusManualActionsRequired, "id")
	for _, s := range steps {
		if s.Label == "Migrazione" && s.State == "done" {
			t.Error("manual_actions_required must not render Migrazione as done before the migration ran")
		}
	}
}

// No dead-end: a blocked session's cockpit CTA prescribes a governance action
// that does not exist on the platform primary path, so the CTA must route to
// Modalità esperto — never self-loop back to the same cockpit page.
func TestPlatformCockpitCTANoSelfLoop(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	blocked, err := store.SetStatus(sess.ID, workbench.StatusBlocked, true, "blocco amministrativo di test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	page := buildPlatformSession(dir, "csrf", blocked, false, "cockpit")
	self := "/platform/migrations/" + sess.ID
	if page.HeroCTAURL == self || page.HeroCTAURL == "" {
		t.Errorf("blocked cockpit CTA = %q, must not self-loop / dead-end", page.HeroCTAURL)
	}
	if page.HeroCTAURL != page.ExpertURL {
		t.Errorf("blocked cockpit CTA = %q, want Modalità esperto %q", page.HeroCTAURL, page.ExpertURL)
	}
}

// Operator-first: confirming the scope from the platform Piano screen keeps the
// operator ON /platform (returns to /plan), never bouncing to the expert
// workbench, and actually persists the confirmation.
func TestPlatformConfirmScopeStaysInPlatform(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "acc@src", "acc@dst", time.Now())
	writeChecklist(t, dir, countingChecklist())
	h, err := New(Options{Dir: dir, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	csrf := fetchCSRF(t, h)
	rr := doReq(h, http.MethodPost, "/platform/migrations/"+sess.ID+"/scope",
		url.Values{"csrf": {csrf}, "preset": {"all_safe"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("scope POST = %d, want 303\n%s", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); loc != "/platform/migrations/"+sess.ID+"/plan?scope=updated" {
		t.Errorf("scope redirect = %q, want the platform Piano screen (not the workbench)", loc)
	}
	got, _ := store.Get(sess.ID)
	if got.Setup == nil || got.Setup.ScopeConfirmedAt == nil {
		t.Error("scope confirmation must be persisted")
	}
}

// A DNS-only scope is not an automatic migration: the platform bounces back to
// /plan with a human flash and makes no confirmation (reuses the shared rule).
func TestPlatformConfirmScopeNeedArea(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	h, err := New(Options{Dir: dir, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	csrf := fetchCSRF(t, h)
	rr := doReq(h, http.MethodPost, "/platform/migrations/"+sess.ID+"/scope",
		url.Values{"csrf": {csrf}, "preset": {"custom"}, "dns": {"1"}})
	if loc := rr.Header().Get("Location"); loc != "/platform/migrations/"+sess.ID+"/plan?scope=need_area" {
		t.Errorf("DNS-only scope redirect = %q, want ...plan?scope=need_area", loc)
	}
	if got, _ := store.Get(sess.ID); got.Setup != nil && got.Setup.ScopeConfirmedAt != nil {
		t.Error("a DNS-only scope must not be confirmed")
	}
}

// CSRF-negative + wrong-method parity for the platform scope POST.
func TestPlatformConfirmScopeGuards(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	h, err := New(Options{Dir: dir, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	if rr := doReq(h, http.MethodPost, "/platform/migrations/"+sess.ID+"/scope",
		url.Values{"csrf": {"wrong"}, "preset": {"all_safe"}}); rr.Code != http.StatusForbidden {
		t.Errorf("scope POST bad csrf = %d, want 403", rr.Code)
	}
	if rr := doReq(h, http.MethodGet, "/platform/migrations/"+sess.ID+"/scope", nil); rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET on scope action = %d, want 405", rr.Code)
	}
}

// The Piano screen renders the scope form in-platform (posting to the platform
// scope endpoint), not a link out to the workbench.
func TestPlatformPlanRendersScopeForm(t *testing.T) {
	dir := t.TempDir()
	ps, store := newPlatformTest(t, dir, false)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	writeChecklist(t, dir, countingChecklist())
	body := platformSessionBody(t, ps, sess.ID, "plan")
	for _, want := range []string{
		`action="/platform/migrations/` + sess.ID + `/scope"`,
		`name="preset"`, `name="csrf"`, "Tutto il migrabile",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/plan scope form missing %q", want)
		}
	}
}

// Guard: the platform never renders a raw 5xx template error to the client.
func TestPlatformSessionNotFound(t *testing.T) {
	dir := t.TempDir()
	ps, _ := newPlatformTest(t, dir, false)
	rr := httptest.NewRecorder()
	ps.handleSession(rr, httptest.NewRequest(http.MethodGet, "/platform/migrations/nope/cockpit", nil), "nope", "cockpit")
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown session = %d, want 404", rr.Code)
	}
}
