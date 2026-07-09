package webui

// Platform UI V2 — audit follow-up tests (2026-07-07 skeptical re-examination).
// These close render-branch coverage gaps the first suite missed and pin the
// Italian-date invariant.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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
	writeChecklist(t, sess.ArtifactDir, accountinventory.MigrationChecklist{
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
	writeChecklist(t, sess.ArtifactDir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallBlocked,
		ManualActions: []accountinventory.ManualAction{
			{Title: "Ricrea cron bloccante", Section: "cron", BlockingCutover: true, Acceptable: false, Accepted: false},
		},
	})
	page := buildPlatformSession(sess.ArtifactDir, "csrf", sess, false, "tasks")
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
	writeChecklist(t, sess.ArtifactDir, countingChecklist()) // databases 32/0, mailboxes 12/12, dns 5/5
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
	writeChecklist(t, sess.ArtifactDir, accountinventory.MigrationChecklist{
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

// The platform header must reuse the workbench risk badge, not only the raw
// governance status. A cutover-only blocker is a warning, not a migration
// blocker, so the primary platform path must spell that distinction out.
func TestPlatformHeaderRendersRiskBadge(t *testing.T) {
	dir := t.TempDir()
	ps, store := newPlatformTest(t, dir, false)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	writeChecklist(t, sess.ArtifactDir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallBlocked,
		ApplyBlocked:  false,
	})
	body := platformSessionBody(t, ps, sess.ID, "cockpit")
	if !strings.Contains(body, "Bloccante cutover") {
		t.Error("platform header must render the reconciled risk badge")
	}
	if strings.Contains(body, "Bloccante migrazione") {
		t.Error("cutover-only blockers must not be presented as migration blockers")
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

// No dead-end: a blocked session's cockpit CTA must stay on the cockpit, where
// the new UI now exposes the governance controls directly.
func TestPlatformCockpitCTANoSelfLoop(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	blocked, err := store.SetStatus(sess.ID, workbench.StatusBlocked, true, "blocco amministrativo di test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	page := buildPlatformSession(blocked.ArtifactDir, "csrf", blocked, false, "cockpit")
	self := "/platform/migrations/" + sess.ID
	if page.HeroCTAURL != self {
		t.Errorf("blocked cockpit CTA = %q, want cockpit route %q", page.HeroCTAURL, self)
	}
	if !page.Governance.CanRecover {
		t.Error("blocked cockpit must expose recovery governance in the new UI")
	}
}

func TestPlatformFailedOrchestratorCTAStaysOnCockpit(t *testing.T) {
	env := newOrchEnv(t, workbench.ContentSelection{Files: true, Databases: true})
	sess, err := env.store.Get(env.sessID)
	if err != nil {
		t.Fatal(err)
	}
	writeJobJournal(sess.ArtifactDir, jobJournal{
		SessionID: env.sessID, Action: orchestratorAction,
		StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		State: jobStateFailed, Phase: "Contenuti", Error: "migrate content: exit status 1",
	})
	page := buildPlatformSession(sess.ArtifactDir, env.csrf, sess, false, "cockpit")
	if page.Cockpit.CTA.Kind != "link" {
		t.Fatalf("failed orchestrator CTA kind = %q, want link", page.Cockpit.CTA.Kind)
	}
	if page.HeroCTAURL != "/platform/migrations/"+env.sessID {
		t.Errorf("failed orchestrator HeroCTAURL = %q, want cockpit route", page.HeroCTAURL)
	}
	if strings.Contains(page.HeroCTAURL, "/plan") {
		t.Errorf("failed orchestrator CTA must not bounce to /plan, got %q", page.HeroCTAURL)
	}
}

func TestPlatformFailedStatusCTAStaysOnCockpit(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	failed, err := store.SetStatus(sess.ID, workbench.StatusFailed, true, "errore persistente di test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	page := buildPlatformSession(failed.ArtifactDir, "csrf", failed, false, "cockpit")
	if page.Cockpit.CTA.Kind != "link" {
		t.Fatalf("failed status CTA kind = %q, want link", page.Cockpit.CTA.Kind)
	}
	if page.HeroCTAURL != "/platform/migrations/"+sess.ID {
		t.Errorf("failed status HeroCTAURL = %q, want cockpit route", page.HeroCTAURL)
	}
	if page.HeroCTAURL == page.ExpertURL {
		t.Errorf("failed status CTA must not bounce to expert by default, got %q", page.HeroCTAURL)
	}
}

func TestPlatformBlockedCTAExplainsPlatformRecovery(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	blocked, err := store.SetStatus(sess.ID, workbench.StatusBlocked, true, "blocco amministrativo di test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	page := buildPlatformSession(blocked.ArtifactDir, "csrf", blocked, false, "cockpit")
	if page.HeroCTAURL != "/platform/migrations/"+sess.ID {
		t.Fatalf("blocked HeroCTAURL = %q, want cockpit route", page.HeroCTAURL)
	}
	if !strings.Contains(strings.ToLower(page.Cockpit.CTA.Label), "blocco") {
		t.Errorf("blocked CTA label = %q, want explicit blocked-state wording", page.Cockpit.CTA.Label)
	}
}

func TestPlatformCockpitRendersGovernanceControls(t *testing.T) {
	dir := t.TempDir()
	ps, store := newPlatformTest(t, dir, false)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	blocked, err := store.SetStatus(sess.ID, workbench.StatusBlocked, true, "blocco amministrativo di test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	body := renderPlatformPage(t, ps, "platform_cockpit.html", buildPlatformSession(blocked.ArtifactDir, "csrf-x", blocked, false, "cockpit"))
	for _, want := range []string{
		"Gestione stato",
		`action="/platform/migrations/` + sess.ID + `/status"`,
		`name="gov_action" value="recover"`,
		"Aggiorna stato",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("blocked cockpit missing governance control %q", want)
		}
	}
}

func TestPlatformStatusBlockStaysInPlatform(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	h, err := New(Options{Dir: dir, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	csrf := fetchCSRF(t, h)
	rr := doReq(h, http.MethodPost, "/platform/migrations/"+sess.ID+"/status",
		url.Values{"csrf": {csrf}, "gov_action": {"block"}, "reason": {"blocco test"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("platform status block = %d, want 303\n%s", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); loc != "/platform/migrations/"+sess.ID+"?gov=blocked" {
		t.Errorf("status block redirect = %q, want cockpit flash", loc)
	}
	got, err := store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != workbench.StatusBlocked {
		t.Errorf("session status after block = %q, want %q", got.Status, workbench.StatusBlocked)
	}
}

func TestPlatformStatusRecoverStaysInPlatform(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	blocked, err := store.SetStatus(sess.ID, workbench.StatusBlocked, true, "blocco test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Options{Dir: dir, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	csrf := fetchCSRF(t, h)
	rr := doReq(h, http.MethodPost, "/platform/migrations/"+blocked.ID+"/status",
		url.Values{"csrf": {csrf}, "gov_action": {"recover"}, "status": {string(workbench.StatusPreflightRequired)}, "reason": {"ripresa operativa di test"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("platform status recover = %d, want 303\n%s", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); loc != "/platform/migrations/"+blocked.ID+"?gov=updated" {
		t.Errorf("status recover redirect = %q, want cockpit flash", loc)
	}
	got, err := store.Get(blocked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != workbench.StatusPreflightRequired {
		t.Errorf("session status after recover = %q, want %q", got.Status, workbench.StatusPreflightRequired)
	}
}

func TestPlatformVerificationCTAExplainsExpertMode(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	done, err := store.SetStatus(sess.ID, workbench.StatusApplyDone, true, "test applicazione completata", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	page := buildPlatformSession(done.ArtifactDir, "csrf", done, false, "cockpit")
	if page.HeroCTAURL != page.ExpertURL {
		t.Fatalf("apply-done HeroCTAURL = %q, want expert route", page.HeroCTAURL)
	}
	if !strings.Contains(page.Cockpit.CTA.Label, "eseguire le verifiche") {
		t.Errorf("verification CTA label = %q, want explicit verification wording", page.Cockpit.CTA.Label)
	}
	if !strings.Contains(strings.ToLower(page.Cockpit.CTA.Detail), "modalità avanzata") {
		t.Errorf("verification CTA detail = %q, want it to explain the expert route", page.Cockpit.CTA.Detail)
	}
}

func TestPlatformPlanRendersRunAnalysisAction(t *testing.T) {
	dir := t.TempDir()
	ps, store := newPlatformTest(t, dir, false)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	body := platformSessionBody(t, ps, sess.ID, "plan")
	for _, want := range []string{
		`action="/platform/migrations/` + sess.ID + `/exec"`,
		`name="action" value="run_pipeline"`,
		"Esegui controllo iniziale",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("plan missing run-analysis control %q", want)
		}
	}
}

func TestPlatformTasksRenderVerifyActions(t *testing.T) {
	dir := t.TempDir()
	ps, _ := newPlatformTest(t, dir, false)
	sess := &workbench.Session{
		ID: "mig-test", Name: "giorgini", ArtifactDir: dir,
		Setup: &workbench.SetupMeta{Content: workbench.ContentSelection{DNS: true, EmailConfig: true, Cron: true}},
	}
	writeChecklist(t, sess.ArtifactDir, countingChecklist())
	mustWrite(t, filepath.Join(sess.ArtifactDir, "dns_import_plan.json"), []byte(`{}`))
	mustWrite(t, filepath.Join(sess.ArtifactDir, "email_apply_plan.json"), []byte(`{}`))
	mustWrite(t, filepath.Join(sess.ArtifactDir, "cron_apply_plan.json"), []byte(`{}`))
	page := buildPlatformSession(sess.ArtifactDir, "csrf", sess, false, "tasks")
	body := renderPlatformPage(t, ps, "platform_tasks.html", page)
	for _, want := range []string{"Verifica DNS", "Verifica configurazioni email", "Verifica cron"} {
		if !strings.Contains(body, want) {
			t.Errorf("tasks missing verify action %q", want)
		}
	}
}

func TestPlatformExecRunPipelineStaysInPlatform(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	fr := &fakeRunner{}
	h, err := New(Options{Dir: dir, Runner: fr.run, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	csrf := fetchCSRF(t, h)
	rr := doReq(h, http.MethodPost, "/platform/migrations/"+sess.ID+"/exec",
		url.Values{"csrf": {csrf}, "action": {"run_pipeline"}, "return_to": {"plan"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("platform exec run_pipeline = %d, want 303\n%s", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); loc != "/platform/migrations/"+sess.ID+"/plan" {
		t.Errorf("run_pipeline redirect = %q, want /platform/.../plan", loc)
	}
}

func TestPlatformExecVerifyStaysInPlatform(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	fr := &fakeRunner{}
	h, err := New(Options{Dir: dir, Runner: fr.run, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	csrf := fetchCSRF(t, h)
	rr := doReq(h, http.MethodPost, "/platform/migrations/"+sess.ID+"/exec",
		url.Values{"csrf": {csrf}, "action": {"dns_verify"}, "return_to": {"tasks"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("platform exec dns_verify = %d, want 303\n%s", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); loc != "/platform/migrations/"+sess.ID+"/tasks" {
		t.Errorf("dns_verify redirect = %q, want /platform/.../tasks", loc)
	}
}

// Operator-first: confirming the scope from the platform Piano screen keeps the
// operator ON /platform (returns to the cockpit), never bouncing to the expert
// workbench, and actually persists the confirmation.
func TestPlatformConfirmScopeStaysInPlatform(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "acc@src", "acc@dst", time.Now())
	writeChecklist(t, sess.ArtifactDir, countingChecklist())
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
	if loc := rr.Header().Get("Location"); loc != "/platform/migrations/"+sess.ID+"?scope=updated" {
		t.Errorf("scope redirect = %q, want the platform cockpit (not the workbench)", loc)
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
	if loc := rr.Header().Get("Location"); loc != "/platform/migrations/"+sess.ID+"?scope=need_area" {
		t.Errorf("DNS-only scope redirect = %q, want ...?scope=need_area", loc)
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

// Isolation guard: a stale/global artifact in the UI root must not freeze the
// scope of a fresh session whose own artifact dir is still clean.
func TestPlatformConfirmScopeIgnoresGlobalArtifacts(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	if err := os.WriteFile(filepath.Join(dir, "report.json"), []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
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
	if loc := rr.Header().Get("Location"); loc != "/platform/migrations/"+sess.ID+"?scope=updated" {
		t.Errorf("scope redirect = %q, want ...?scope=updated", loc)
	}
}

// The Piano screen renders the scope form in-platform (posting to the platform
// scope endpoint), not a link out to the workbench.
func TestPlatformPlanRendersScopeForm(t *testing.T) {
	dir := t.TempDir()
	ps, store := newPlatformTest(t, dir, false)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	writeChecklist(t, sess.ArtifactDir, countingChecklist())
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

// Operator-first: starting the migration runs the platform smart-start flow and
// returns to the platform cockpit — never the workbench.
func TestPlatformStartMigrationInPlatform(t *testing.T) {
	env := newOrchEnv(t, workbench.ContentSelection{Files: true, Databases: true})
	rr := doReq(env.h, http.MethodPost, "/platform/migrations/"+env.sessID+"/smart-start",
		url.Values{"csrf": {env.csrf}, "confirm_start": {"1"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("start POST = %d, want 303\n%s", rr.Code, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "/platform/migrations/"+env.sessID+"?migrate=") {
		t.Errorf("start redirect = %q, want the platform cockpit (?migrate=…)", loc)
	}
	if strings.Contains(loc, "/workbench/") {
		t.Error("start must return to the platform, never the workbench")
	}
	// The run is asynchronous now: wait for it to settle so the goroutine's
	// writes finish before the tempdir is cleaned up.
	sess, err := env.store.Get(env.sessID)
	if err != nil {
		t.Fatal(err)
	}
	waitJobSettled(t, sess.ArtifactDir)
}

// The in-platform start keeps CSRF plus an explicit server-side confirmation
// marker: missing marker → 403, bad csrf → 403, GET → 405.
func TestPlatformStartMigrationGatesPreserved(t *testing.T) {
	env := newOrchEnv(t, workbench.ContentSelection{Files: true, Databases: true})
	path := "/platform/migrations/" + env.sessID + "/smart-start"
	if rr := doReq(env.h, http.MethodPost, path, url.Values{"csrf": {env.csrf}}); rr.Code != http.StatusForbidden {
		t.Errorf("missing confirm_start = %d, want 403", rr.Code)
	}
	if rr := doReq(env.h, http.MethodPost, path, url.Values{"csrf": {"nope"}, "confirm_start": {"1"}}); rr.Code != http.StatusForbidden {
		t.Errorf("bad csrf = %d, want 403", rr.Code)
	}
	if rr := doReq(env.h, http.MethodGet, path, nil); rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET start = %d, want 405", rr.Code)
	}
	// The session was never started (no journal, still startable).
	if got, _ := env.store.Get(env.sessID); got.Status != workbench.StatusReadyForApply {
		t.Errorf("a refused start must not change the session status, got %q", got.Status)
	}
}

func TestPlatformSmartStartRunsPreflightThenMigration(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	setup := &workbench.SetupMeta{
		Content: workbench.ContentSelection{Files: true, Databases: true},
	}
	sess, err := store.CreateWithSetup("giorginisposi", "src", "dst", setup, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmScope(sess.ID, setup.Content, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := writeValidatedConfigAt(sess.ArtifactDir, yamlConfig{
		Src:  yamlHost{IP: "1.2.3.4", Port: 22, SSHUser: "srcacct", SSHPass: "src-secret", Timeout: "15s"},
		Dest: yamlHost{IP: "5.6.7.8", Port: 22, SSHUser: "dstacct", SSHPass: "dst-secret", Timeout: "15s"},
	}); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRunner{onCall: func(name string) {
		if name == "inventory checklist" {
			writeChecklist(t, sess.ArtifactDir, readyChecklist())
		}
	}}
	h, err := New(Options{Dir: dir, Runner: fr.run, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	csrf := fetchCSRF(t, h)
	rr := doReq(h, http.MethodPost, "/platform/migrations/"+sess.ID+"/smart-start",
		url.Values{"csrf": {csrf}, "confirm_start": {"1"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("smart-start = %d, want 303\n%s", rr.Code, rr.Body.String())
	}
	// The migration runs in the background now: wait for it to settle before
	// asserting the recorded runner calls (and before tempdir cleanup).
	waitJobSettled(t, sess.ArtifactDir)
	names := []string{}
	for _, c := range fr.recorded() {
		names = append(names, c.name)
	}
	for _, want := range []string{"account inventory", "inventory diff", "inventory policy", "inventory checklist", "migrate content"} {
		if !hasCall(names, want) {
			t.Fatalf("smart-start calls = %v, missing %q", names, want)
		}
	}
	if strings.Contains(rr.Header().Get("Location"), "/workbench/") {
		t.Fatal("smart-start must return to the platform")
	}
	waitJobSettled(t, sess.ArtifactDir)
}

// newSmartStartReady builds a session ready for the platform smart-start with a
// caller-supplied runner and a pre-written ready checklist, so the run skips the
// preflight and goes straight to the orchestrator phase ("migrate content") —
// the surface the async tests below exercise.
func newSmartStartReady(t *testing.T, fr *fakeRunner) (h http.Handler, dir, id string) {
	t.Helper()
	dir = t.TempDir()
	store := mustStore(t, dir)
	setup := &workbench.SetupMeta{Content: workbench.ContentSelection{Files: true, Databases: true}}
	sess, err := store.CreateWithSetup("giorginisposi", "src", "dst", setup, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmScope(sess.ID, setup.Content, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := writeValidatedConfigAt(sess.ArtifactDir, yamlConfig{
		Src:  yamlHost{IP: "1.2.3.4", Port: 22, SSHUser: "srcacct", SSHPass: "src-secret", Timeout: "15s"},
		Dest: yamlHost{IP: "5.6.7.8", Port: 22, SSHUser: "dstacct", SSHPass: "dst-secret", Timeout: "15s"},
	}); err != nil {
		t.Fatal(err)
	}
	writeChecklist(t, sess.ArtifactDir, readyChecklist())
	h, err = New(Options{Dir: dir, Runner: fr.run, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	return h, sess.ArtifactDir, sess.ID
}

// The smart-start is asynchronous: the 303 fires immediately with ?migrate=started
// (not the outcome), and the real result is persisted in the job journal and then
// surfaced as the cockpit flash even though the reloaded URL still says started.
func TestPlatformSmartStartIsAsyncWithPersistedOutcome(t *testing.T) {
	fr := &fakeRunner{}
	h, dir, id := newSmartStartReady(t, fr)
	csrf := fetchCSRF(t, h)
	rr := doReq(h, http.MethodPost, "/platform/migrations/"+id+"/smart-start",
		url.Values{"csrf": {csrf}, "confirm_start": {"1"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("smart-start = %d, want 303\n%s", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); loc != "/platform/migrations/"+id+"?migrate=started" {
		t.Errorf("redirect = %q, want an immediate ?migrate=started (async)", loc)
	}
	jj := waitJobSettled(t, dir)
	if jj.State != jobStateCompleted {
		t.Fatalf("journal state = %q, want completed", jj.State)
	}
	if !strings.Contains(jj.Outcome, "completata") {
		t.Errorf("journal outcome = %q, want the human completion message", jj.Outcome)
	}
	body := doReq(h, http.MethodGet, "/platform/migrations/"+id+"?migrate=started", nil).Body.String()
	if !strings.Contains(body, "completata") {
		t.Error("cockpit must show the terminal outcome, not the stale 'started' flash")
	}
}

// The single-writer slot is reserved SYNCHRONOUSLY before the 303, so a second
// start while the background job runs is a readable 409, never a concurrent run.
func TestPlatformSmartStartConcurrentReturns409(t *testing.T) {
	fr := &fakeRunner{gate: make(chan struct{})} // each step blocks until closed
	h, dir, id := newSmartStartReady(t, fr)
	csrf := fetchCSRF(t, h)
	path := "/platform/migrations/" + id + "/smart-start"
	form := url.Values{"csrf": {csrf}, "confirm_start": {"1"}}
	if rr := doReq(h, http.MethodPost, path, form); rr.Code != http.StatusSeeOther {
		t.Fatalf("first start = %d, want 303", rr.Code)
	}
	if rr := doReq(h, http.MethodPost, path, form); rr.Code != http.StatusConflict {
		t.Errorf("concurrent start = %d, want 409 (slot held by the running job)", rr.Code)
	}
	close(fr.gate) // let the first job finish and release the slot
	waitJobSettled(t, dir)
}

// A panic in the background goroutine must be recovered into a failed journal —
// it must never take down the ui process (net/http only recovers per-connection).
func TestPlatformSmartStartPanicRecovered(t *testing.T) {
	fr := &fakeRunner{onCall: func(name string) { panic("boom in " + name) }}
	h, dir, id := newSmartStartReady(t, fr)
	csrf := fetchCSRF(t, h)
	rr := doReq(h, http.MethodPost, "/platform/migrations/"+id+"/smart-start",
		url.Values{"csrf": {csrf}, "confirm_start": {"1"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("smart-start = %d, want 303", rr.Code)
	}
	jj := waitJobSettled(t, dir) // the process being alive to reach here is the point
	if jj.State != jobStateFailed {
		t.Errorf("journal state = %q, want failed (panic recovered)", jj.State)
	}
}

// Operator-first: accepting a manual action happens in-platform and returns to
// the platform Task screen, reusing the shared saveAcceptTo (persists to
// acceptances.json).
func TestPlatformAcceptInPlatform(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	fr := &fakeRunner{}
	h, err := New(Options{Dir: dir, Runner: fr.run, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	writeChecklist(t, sess.ArtifactDir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallManualActionRequired,
		ManualActions: []accountinventory.ManualAction{
			{ID: "MA-1", Key: "AK-test", Title: "Verifica MX", Section: "dns", Acceptable: true},
		},
	})
	csrf := fetchCSRF(t, h)
	rr := doReq(h, http.MethodPost, "/platform/migrations/"+sess.ID+"/accept",
		url.Values{"csrf": {csrf}, "action_key": {"AK-test"}, "operator": {"Mario"}, "reason": {"verificato ok"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("accept POST = %d, want 303\n%s", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); loc != "/platform/migrations/"+sess.ID+"/tasks" {
		t.Errorf("accept redirect = %q, want the platform Task screen", loc)
	}
	if _, err := os.Stat(filepath.Join(sess.ArtifactDir, "acceptances.json")); err != nil {
		t.Error("acceptance must be persisted to acceptances.json")
	}
}

// The /tasks screen renders the in-platform accept form for an acceptable action.
func TestPlatformTasksRendersAcceptForm(t *testing.T) {
	dir := t.TempDir()
	ps, store := newPlatformTest(t, dir, false)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	writeChecklist(t, sess.ArtifactDir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallManualActionRequired,
		ManualActions: []accountinventory.ManualAction{
			{Key: "AK-x", Title: "Verifica MX", Section: "dns", Acceptable: true},
		},
	})
	body := platformSessionBody(t, ps, sess.ID, "tasks")
	for _, want := range []string{
		`action="/platform/migrations/` + sess.ID + `/accept"`,
		`name="action_key"`, "AK-x", `name="operator"`, `name="reason"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/tasks accept form missing %q", want)
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
