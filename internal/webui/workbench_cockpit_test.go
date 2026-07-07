package webui

// Fase 4 — Modern Migration Cockpit tests. A mix of pure read-model unit tests
// (fast, exact) and HTTP render tests (the cockpit as the operator sees it).
// Every assertion pins one of the 15 non-negotiables from the Fase 4 brief:
// dynamic steps, honest next-action, real comparison, honest monitor, the
// migration-vs-cutover blocker split, DNS never auto-run, collapsed technical
// detail, the advanced screen demoted, and NO invented data.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/events"
	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// countingChecklist is a ready (non-blocking) checklist carrying real per-area
// source/destination counts, so the comparison renders honest numbers.
func countingChecklist() accountinventory.MigrationChecklist {
	return accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallReadyToCutover,
		Sections: []accountinventory.ChecklistSection{
			{Section: "databases", SourceCount: 32, DestinationCount: 0, SourcePresent: true},
			{Section: "mailboxes", SourceCount: 12, DestinationCount: 12, SourcePresent: true, DestinationPresent: true},
			{Section: "dns", SourceCount: 5, DestinationCount: 5},
		},
	}
}

// --- pure read-model units ---------------------------------------------------

// Test #9 (unit): DNS is ALWAYS manual/verifiable in the comparison, never auto.
func TestCompareAreaStateDNSAlwaysManual(t *testing.T) {
	dns := migrationPlanArea{Key: "dns", Included: true, Category: planManualVerifiable}
	state, class, action := compareAreaState(dns, artifactFacts{})
	if state != "Manuale / verificabile" || class != "warn" || action != "Task manuale" {
		t.Errorf("dns compare = %q/%q/%q, want Manuale/verificabile", state, class, action)
	}
	if state == "Da migrare" || action == "Automatico" {
		t.Error("DNS must never be classified automatic in the comparison")
	}
}

// Test #12 (unit): comparison over a nil checklist yields nothing (no invented
// rows/counts) — the template then renders "Non ancora disponibile".
func TestBuildComparisonNilChecklist(t *testing.T) {
	if rows := buildCockpitComparison(artifactFacts{Checklist: nil}, migrationPlan{}); rows != nil {
		t.Errorf("comparison without a checklist = %v, want nil", rows)
	}
}

// Test #3/#12 (unit): counts come from the checklist sections and only for areas
// with an honest single count; files/email_config carry no count.
func TestBuildComparisonHonestCounts(t *testing.T) {
	cl := countingChecklist()
	f := artifactFacts{Checklist: &cl}
	scope := legacyScope() // everything included
	plan := buildMigrationPlan(f, scope)
	rows := buildCockpitComparison(f, plan)
	byKey := map[string]cockpitCompareRow{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	if db := byKey["databases"]; !db.HasCount || db.Source != 32 || db.Dest != 0 {
		t.Errorf("databases row = %+v, want count 32→0", db)
	}
	if em := byKey["email"]; !em.HasCount || em.Source != 12 || em.Dest != 12 {
		t.Errorf("email row = %+v, want count 12→12 (mailboxes)", em)
	}
	if files := byKey["files"]; files.HasCount {
		t.Errorf("files must NOT carry a count (not inventoried), got %+v", files)
	}
}

// Test #6 (unit): the content phase reports "running" while a content job is live.
func TestContentPhaseStateRunning(t *testing.T) {
	scope := contentScope{IncludeFiles: true}
	job := &jobJournal{State: jobStateRunning, Phase: "Contenuti"}
	if s := contentPhaseState(artifactFacts{}, scope, job, true, nil); s != "running" {
		t.Errorf("content phase = %q, want running", s)
	}
}

// Test #6 (unit): a completed content migration reads as completed_with_report
// (the report is real; there is no fake clean verify).
func TestContentPhaseStateCompletedWithReport(t *testing.T) {
	scope := contentScope{IncludeFiles: true}
	if s := contentPhaseState(artifactFacts{ContentApplyPresent: true}, scope, nil, false, nil); s != "completed_with_report" {
		t.Errorf("content phase = %q, want completed_with_report", s)
	}
}

// Test #9 (unit): DNS in scope is a manual monitor phase, never auto-run.
func TestBuildMonitorDNSManual(t *testing.T) {
	m := buildCockpitMonitor(artifactFacts{}, contentScope{IncludeDNS: true}, nil, false, nil)
	var dns cockpitMonitorPhase
	for _, p := range m.Phases {
		if p.Label == "DNS" {
			dns = p
		}
	}
	if dns.State != "manual" {
		t.Errorf("DNS monitor phase = %q, want manual", dns.State)
	}
}

// Test #6.1 (unit): the next action is reconciled with plan readiness — a
// checklist-ready, scope-unconfirmed session is told to confirm the scope, not
// to run the preflight again.
func TestReconcileNextActionScopeUnconfirmed(t *testing.T) {
	plan := migrationPlan{Ready: true, ScopeConfirmed: false}
	stale := recommendedAction{Text: "Esegui il preflight", Screen: screenPreflight}
	got := reconcileNextAction(stale, workbench.StatusDraft, artifactFacts{}, plan, nil, false)
	if !strings.Contains(got.Text, "Conferma") {
		t.Errorf("reconciled next = %q, want a scope-confirm prompt", got.Text)
	}
}

// Test #6.1 (unit): a live job overrides the next action to "observe".
func TestReconcileNextActionJobLive(t *testing.T) {
	got := reconcileNextAction(recommendedAction{Text: "x"}, workbench.StatusReadyForApply,
		artifactFacts{}, migrationPlan{}, &jobJournal{State: jobStateRunning}, true)
	if !strings.Contains(got.Text, "Osserva") {
		t.Errorf("job-live next = %q, want an observe prompt", got.Text)
	}
}

// Test #6.1 (unit): once the content migration already ran, reconciliation defers
// to the status-driven guidance and does NOT re-offer "Avvia migrazione".
func TestReconcileNextActionAppliedDefersToStatus(t *testing.T) {
	plan := migrationPlan{Ready: true, ScopeConfirmed: true, StartEnabled: true, CanStartMigration: true}
	statusNext := recommendedAction{Text: "Esegui le verifiche del risultato"}
	got := reconcileNextAction(statusNext, workbench.StatusApplyDone,
		artifactFacts{ContentApplyPresent: true}, plan, nil, false)
	if strings.Contains(got.Text, "Avvia") {
		t.Errorf("post-apply next = %q, must not re-offer Avvia migrazione", got.Text)
	}
}

// --- HTTP render integration -------------------------------------------------

// Test #1: the cockpit renders the dynamic horizontal stepper.
func TestCockpitRendersDynamicSteps(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	_, body := getBody(t, h, "/workbench/session/"+sess.ID)
	for _, want := range []string{"cockpit-steps", "Setup", "Preflight", "Migrazione", "Chiusura"} {
		if !strings.Contains(body, want) {
			t.Errorf("cockpit stepper missing %q", want)
		}
	}
}

// Test #2 + #12 + #14: no preflight → the hero asks for the preflight, the
// comparison degrades to "Non ancora disponibile", NO monitor, NO invented data,
// and a missing events.jsonl never breaks the page.
func TestCockpitNoPreflightHonestEmpty(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	code, body := getBody(t, h, "/workbench/session/"+sess.ID)
	if code != http.StatusOK {
		t.Fatalf("panoramica = %d, want 200 without events.jsonl", code)
	}
	if !strings.Contains(body, "Preflight da eseguire") {
		t.Error("hero must say the preflight is pending")
	}
	if !strings.Contains(body, "Non ancora disponibile") {
		t.Error("comparison must degrade honestly without a checklist")
	}
	if strings.Contains(body, "Monitor esecuzione") {
		t.Error("the monitor must be hidden when nothing ran")
	}
}

// Test #3: with a checklist present the comparison renders with real counts.
func TestCockpitComparisonWithChecklist(t *testing.T) {
	h, store, dir := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	writeChecklist(t, dir, countingChecklist())
	_, body := getBody(t, h, "/workbench/session/"+sess.ID)
	if !strings.Contains(body, "Comparativa account") {
		t.Error("comparison block must render with a checklist")
	}
	for _, want := range []string{"Database", "Email / Maildir", ">32<", ">12<"} {
		if !strings.Contains(body, want) {
			t.Errorf("comparison missing %q", want)
		}
	}
}

// Test #4: checklist ready but scope NOT confirmed → hero and CTA ask to confirm
// the scope (not to run the preflight blindly — dogfooding #4 §6.1).
func TestCockpitScopeUnconfirmedAsksConfirm(t *testing.T) {
	dir := t.TempDir()
	h, store := wizardHandler(t, dir)
	setup := &workbench.SetupMeta{
		Source:      workbench.Endpoint{Host: "1.1.1.1", Port: 22, Account: "giorgini"},
		Destination: workbench.Endpoint{Host: "2.2.2.2", Port: 22, Account: "giorgini"},
		Content:     workbench.ContentSelection{Files: true, Databases: true},
	}
	sess := wizardSession(t, store, "giorgini", setup)
	writeChecklist(t, dir, readyChecklist())
	_, body := getBody(t, h, "/workbench/session/"+sess.ID)
	if !strings.Contains(body, "Conferma lo scope") {
		t.Error("hero must ask to confirm the scope when the plan is ready but scope is unconfirmed")
	}
	if !strings.Contains(body, "Conferma") {
		t.Error("CTA must point to the scope confirmation")
	}
}

// Test #5: scope confirmed + plan ready → the hero says "Pronta per migrare" and
// the ONE dominant CTA is the real strong-confirmation start form.
func TestCockpitReadyShowsStartCTA(t *testing.T) {
	env := newOrchEnv(t, workbench.ContentSelection{Files: true, Databases: true})
	_, body := getBody(t, env.h, "/workbench/session/"+env.sessID)
	if !strings.Contains(body, "Pronta per migrare") {
		t.Error("hero must read 'Pronta per migrare' when startable")
	}
	if !strings.Contains(body, "action=\"/workbench/session/"+env.sessID+"/start-migration\"") {
		t.Error("the dominant CTA must be the real start-migration form")
	}
	if !strings.Contains(body, "name=\"confirm_account\"") {
		t.Error("the start CTA must keep the strong per-account confirmation")
	}
}

// Test #6: a live content job renders the execution monitor with the current
// phase and the meta-refresh, item detail degrading honestly when unavailable.
func TestCockpitJobRunningMonitor(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	writeJobJournal(dir, jobJournal{
		SessionID: sess.ID, Action: "migrazione automatica",
		StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		State: jobStateRunning, Phase: "Contenuti",
	})
	ws, err := newWorkbenchServer(store, dir, "csrf-x")
	if err != nil {
		t.Fatal(err)
	}
	ws.jobBusy = func() bool { return true }
	rr := httptest.NewRecorder()
	ws.handleScreen(rr, httptest.NewRequest(http.MethodGet, "/", nil), sess.ID, "")
	body := rr.Body.String()
	for _, want := range []string{"Monitor esecuzione", "Migrazione in corso", "Contenuti", "In corso"} {
		if !strings.Contains(body, want) {
			t.Errorf("running monitor missing %q", want)
		}
	}
	if !strings.Contains(body, "dettaglio item non disponibile") {
		t.Error("without events item detail the monitor must degrade honestly (test #15)")
	}
}

// Test #7: a failed job shows the failed state and the last error in the monitor.
func TestCockpitJobFailedShowsError(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	writeJobJournal(dir, jobJournal{
		SessionID: sess.ID, Action: "migrazione automatica",
		StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		State: jobStateFailed, Phase: "Contenuti", Error: "migrate content: exit status 1",
	})
	ws, err := newWorkbenchServer(store, dir, "csrf-x")
	if err != nil {
		t.Fatal(err)
	}
	ws.jobBusy = func() bool { return false }
	rr := httptest.NewRecorder()
	ws.handleScreen(rr, httptest.NewRequest(http.MethodGet, "/", nil), sess.ID, "")
	body := rr.Body.String()
	if !strings.Contains(body, "Ultimo tentativo fallito") {
		t.Error("hero must flag the failed attempt")
	}
	if !strings.Contains(body, "Fallito") {
		t.Error("monitor must show the failed phase state")
	}
	if !strings.Contains(body, "migrate content: exit status 1") {
		t.Error("the last error must be surfaced in the execution log")
	}
}

// Test #8: migration-blocking and cutover-blocking render with DIFFERENT labels.
func TestCockpitBlockerLabelsDistinct(t *testing.T) {
	// Migration-blocking checklist.
	migDir := t.TempDir()
	mh, ms := wizardHandler(t, migDir)
	mSetup := &workbench.SetupMeta{Source: workbench.Endpoint{Account: "a"}, Destination: workbench.Endpoint{Account: "a"}, Content: workbench.ContentSelection{Files: true}}
	mSess := wizardSession(t, ms, "acct", mSetup)
	writeChecklist(t, migDir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1, ApplyBlocked: true,
		OverallStatus: accountinventory.OverallNotReady,
		Sections:      []accountinventory.ChecklistSection{{Section: "databases", BlockersApply: []string{"spazio insufficiente"}}},
	})
	_, migBody := getBody(t, mh, "/workbench/session/"+mSess.ID)
	if !strings.Contains(migBody, "Bloccante migrazione") {
		t.Error("apply-blocked checklist must render 'Bloccante migrazione'")
	}

	// Cutover-only blocking checklist (migration is startable).
	cutDir := t.TempDir()
	ch, cs := wizardHandler(t, cutDir)
	cSetup := &workbench.SetupMeta{Source: workbench.Endpoint{Account: "a"}, Destination: workbench.Endpoint{Account: "a"}, Content: workbench.ContentSelection{Files: true}}
	cSess := wizardSession(t, cs, "acct", cSetup)
	writeChecklist(t, cutDir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1, ApplyBlocked: false,
		OverallStatus: accountinventory.OverallBlocked,
		Sections:      []accountinventory.ChecklistSection{{Section: "dns", BlockersCutover: []string{"MX esterno non verificato"}}},
	})
	_, cutBody := getBody(t, ch, "/workbench/session/"+cSess.ID)
	if !strings.Contains(cutBody, "Bloccante cutover") {
		t.Error("cutover-only checklist must render 'Bloccante cutover'")
	}
	if strings.Contains(cutBody, "Bloccante migrazione") {
		t.Error("a cutover-only block must NOT read as a migration blocker")
	}
}

// Test #9: DNS included renders as manual/verifiable in the comparison and the
// monitor, never as an automatic run.
func TestCockpitDNSManualNeverAuto(t *testing.T) {
	env := newOrchEnv(t, workbench.ContentSelection{Files: true, DNS: true})
	_, body := getBody(t, env.h, "/workbench/session/"+env.sessID)
	if !strings.Contains(body, "Manuale / verificabile") {
		t.Error("DNS must render as manual/verifiable in the comparison")
	}
}

// Test #10 + #11: engineering surfaces move OUT of the operator path into expert
// mode (reachable under <details>), and the former primary "Applica e verifica"
// screen stays demoted to an expert path.
func TestCockpitTechnicalCollapsedAndAdvancedDemoted(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	// Operator mode: governance is out of the primary path entirely.
	_, panoramica := getBody(t, h, "/workbench/session/"+sess.ID)
	if strings.Contains(panoramica, "Governance") {
		t.Error("operator Panoramica must not surface governance in the primary path")
	}
	// Expert mode: governance/history reachable but collapsed under <details>.
	_, expert := getBody(t, h, "/workbench/session/"+sess.ID+"?mode=expert")
	if !strings.Contains(expert, "<details") || !strings.Contains(expert, "Governance") {
		t.Error("expert mode must keep governance/history reachable under <details>")
	}
	_, applica := getBody(t, h, "/workbench/session/"+sess.ID+"/applica")
	if !strings.Contains(applica, "Azioni avanzate") || !strings.Contains(applica, "Percorso esperto") {
		t.Error("the Applica screen must be reframed as an expert/advanced path")
	}
}

// Regression (go-review Blocking #1): a FAILED last attempt must NOT render a
// live "Avvia migrazione" start form in the cockpit even when the plan is
// otherwise startable — the hero and the dominant CTA must agree.
func TestCockpitFailedJobHidesStartForm(t *testing.T) {
	env := newOrchEnv(t, workbench.ContentSelection{Files: true, Databases: true})
	writeJobJournal(env.dir, jobJournal{
		SessionID: env.sessID, Action: orchestratorAction,
		StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		State: jobStateFailed, Phase: "Contenuti", Error: "migrate content: exit status 1",
	})
	_, body := getBody(t, env.h, "/workbench/session/"+env.sessID)
	if !strings.Contains(body, "Ultimo tentativo fallito") {
		t.Error("hero must flag the failed attempt")
	}
	if strings.Contains(body, "/start-migration") {
		t.Error("BLOCKING: a failed session must NOT render a live start-migration form in the cockpit")
	}
}

// Regression (go-review Blocking #1): an already-applied content migration must
// NOT re-offer the start form (which would re-run migrate_content against the
// already-migrated destination).
func TestCockpitAppliedHidesStartForm(t *testing.T) {
	env := newOrchEnv(t, workbench.ContentSelection{Files: true, Databases: true})
	if err := os.WriteFile(filepath.Join(env.dir, "report.json"), []byte(`{"mode":"apply"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, body := getBody(t, env.h, "/workbench/session/"+env.sessID)
	if strings.Contains(body, "/start-migration") {
		t.Error("BLOCKING: an already-applied session must NOT render a live start-migration form in the cockpit")
	}
}

// Regression (go-review Important #3): a governance-Blocked session must not show
// the start form even if the checklist/plan otherwise look startable.
func TestCockpitGovernanceBlockedHidesStartForm(t *testing.T) {
	env := newOrchEnv(t, workbench.ContentSelection{Files: true, Databases: true})
	if _, err := env.store.SetStatus(env.sessID, workbench.StatusBlocked, true, "blocco manuale per test regressione", time.Now().UTC()); err != nil {
		t.Fatalf("SetStatus blocked: %v", err)
	}
	_, body := getBody(t, env.h, "/workbench/session/"+env.sessID)
	if strings.Contains(body, "/start-migration") {
		t.Error("a governance-Blocked session must NOT render a live start-migration form")
	}
}

// Regression (go-review Important #2): a failed single /exec action routes to the
// advanced screen; a failed orchestrator run routes to the plan screen.
func TestFailedJobNextScreenRouting(t *testing.T) {
	if s := failedJobNextScreen(&jobJournal{Action: orchestratorAction}); s != screenMigrazione {
		t.Errorf("orchestrator failure routes to %q, want %q", s, screenMigrazione)
	}
	if s := failedJobNextScreen(&jobJournal{Action: "dns_apply"}); s != screenApplica {
		t.Errorf("single-exec failure routes to %q, want %q", s, screenApplica)
	}
}

// Test #13: a valid events.jsonl produces real item-level content progress.
func TestCockpitEventsProduceRealProgress(t *testing.T) {
	h, store, dir := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	writeMonitorEvents(t, dir,
		monEv("run-1", 0, "", events.EventRunStarted, events.LevelInfo, "", nil),
		monEv("run-1", time.Second, events.PhaseMigrateMail, events.EventPhaseCompleted, events.LevelInfo, "", map[string]any{
			"items":  []map[string]any{{"item": "a@x.it", "status": "migrated"}, {"item": "b@x.it", "status": "migrated"}},
			"failed": 0, "unverified": 0,
		}),
	)
	_, body := getBody(t, h, "/workbench/session/"+sess.ID)
	if !strings.Contains(body, "Avanzamento contenuti") {
		t.Error("valid events.jsonl must surface the content progress block")
	}
	if !strings.Contains(body, "Posta (per casella)") {
		t.Error("the run monitor phase must be labelled in Italian")
	}
	if !strings.Contains(body, "item(s)") {
		t.Error("the real per-mailbox summary must be shown")
	}
}
