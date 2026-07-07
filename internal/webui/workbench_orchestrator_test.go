package webui

// Fase 3 — Smart Migration Orchestrator tests. These drive the handler and the
// derivation with a scripted fake runner: no real server is dialled, no
// credentials, no real apply. Each test asserts one non-negotiable invariant.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// orchEnv bundles a ready orchestrator environment for a test.
type orchEnv struct {
	h      http.Handler
	store  *workbench.Store
	dir    string
	sessID string
	csrf   string
	fr     *fakeRunner
}

// readyChecklist is a valid, non-blocking checklist (plan can start).
func readyChecklist() accountinventory.MigrationChecklist {
	return accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallReadyToCutover,
	}
}

// newOrchEnv builds a session advanced to ready_for_apply with a CONFIRMED
// scope (Setup + ScopeConfirmedAt), a host.yaml, and a ready checklist. The
// caller writes any area plans (email_apply_plan.json / cron_apply_plan.json)
// it needs BEFORE posting — facts are re-read per request.
func newOrchEnv(t *testing.T, content workbench.ContentSelection) *orchEnv {
	return newOrchEnvRunner(t, content, &fakeRunner{})
}

// newOrchEnvRunner is newOrchEnv with a caller-supplied runner (e.g. one scripted
// to panic), so tests can exercise the sync start-migration failure paths.
func newOrchEnvRunner(t *testing.T, content workbench.ContentSelection, fr *fakeRunner) *orchEnv {
	t.Helper()
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := workbench.NewStore(storeDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	sess, err := store.Create("giorginisposi", "src", "dst", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []workbench.Status{
		workbench.StatusPreflightRequired,
		workbench.StatusInventoryReady,
		workbench.StatusChecklistReady,
		workbench.StatusReadyForApply,
	} {
		if _, err := store.SetStatus(sess.ID, s, false, "", time.Now()); err != nil {
			t.Fatalf("SetStatus %s: %v", s, err)
		}
	}
	if _, err := store.ConfirmScope(sess.ID, content, time.Now().UTC()); err != nil {
		t.Fatalf("ConfirmScope: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte("src:\n  ip: 1.2.3.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeChecklist(t, dir, readyChecklist())

	h, err := New(Options{Dir: dir, Runner: fr.run, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	return &orchEnv{h: h, store: store, dir: dir, sessID: sess.ID, csrf: fetchCSRF(t, h), fr: fr}
}

func (e *orchEnv) writePlan(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.dir, name), []byte(`{"format_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// start posts the start-migration form with the given confirmation string.
func (e *orchEnv) start(confirm string) *httptest.ResponseRecorder {
	return doWorkbenchReq(e.h, http.MethodPost, "/workbench/session/"+e.sessID+"/start-migration",
		url.Values{"csrf": {e.csrf}, "confirm_account": {confirm}})
}

// callNames returns the ordered list of runner step names invoked.
func (e *orchEnv) callNames() []string {
	var out []string
	for _, c := range e.fr.recorded() {
		out = append(out, c.name)
	}
	return out
}

func hasCall(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// waitJobSettled polls the job journal until the async smart-start reaches a
// terminal state, so a test can observe its outcome after the immediate 303 and
// not race the background goroutine's tempdir writes on cleanup. Fails the test
// if the run never settles.
func waitJobSettled(t *testing.T, dir string) *jobJournal {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if jj, ok := readJobJournal(dir); ok && jj.State != jobStateRunning {
			return jj
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("async smart-start never settled in %s", dir)
	return nil
}

// A panic in the SYNCHRONOUS workbench start-migration must close the journal as
// failed (not a dishonest "completed") and return 500 — mirroring the async path.
func TestWorkbenchStartMigrationPanicMarksJournalFailed(t *testing.T) {
	fr := &fakeRunner{onCall: func(name string) {
		if name == "migrate content" {
			panic("boom in migrate content")
		}
	}}
	env := newOrchEnvRunner(t, workbench.ContentSelection{Files: true, Databases: true}, fr)
	rr := env.start("giorginisposi") // strong confirmation = account name
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("panic in sync start = %d, want 500", rr.Code)
	}
	jj, ok := readJobJournal(env.dir)
	if !ok {
		t.Fatal("no job journal after a panicking start")
	}
	if jj.State != jobStateFailed {
		t.Errorf("journal state after panic = %q, want failed", jj.State)
	}
}

func argvFor(fr *fakeRunner, name string) []string {
	for _, c := range fr.recorded() {
		if c.name == name {
			return c.argv
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 1. scope not confirmed → refused, nothing runs
// ---------------------------------------------------------------------------

func TestOrchestratorRefusesUnconfirmedScope(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "migrations")
	os.MkdirAll(storeDir, 0o700)
	store, _ := workbench.NewStore(storeDir)
	// Setup present (wizard) but scope NOT confirmed post-preflight.
	sess, err := store.CreateWithSetup("giorginisposi", "src", "dst",
		&workbench.SetupMeta{Content: workbench.ContentSelection{Files: true}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	writeChecklist(t, dir, readyChecklist())
	fr := &fakeRunner{}
	h, _ := New(Options{Dir: dir, Runner: fr.run, SessionStore: store})
	csrf := fetchCSRF(t, h)

	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sess.ID+"/start-migration",
		url.Values{"csrf": {csrf}, "confirm_account": {"giorginisposi"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("unconfirmed scope = %d, want 303", rr.Code)
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "migrate=scope_unconfirmed") {
		t.Errorf("redirect = %q, want migrate=scope_unconfirmed", loc)
	}
	if len(fr.recorded()) != 0 {
		t.Errorf("no phase must run without a confirmed scope, got %v", fr.recorded())
	}
}

// ---------------------------------------------------------------------------
// 2. checklist ApplyBlocked / NOT_READY → refused
// ---------------------------------------------------------------------------

func TestOrchestratorRefusesBlockedChecklist(t *testing.T) {
	e := newOrchEnv(t, workbench.ContentSelection{Files: true, Databases: true})
	// Turn the checklist blocking.
	writeChecklist(t, e.dir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallNotReady, ApplyBlocked: true,
	})
	rr := e.start("giorginisposi")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("blocked start = %d, want 303", rr.Code)
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "migrate=blocked") {
		t.Errorf("redirect = %q, want migrate=blocked", loc)
	}
	if len(e.fr.recorded()) != 0 {
		t.Errorf("nothing must run when the checklist blocks apply, got %v", e.callNames())
	}
}

// ---------------------------------------------------------------------------
// 3. DNS-only / no automatic area → refused (no_auto)
// ---------------------------------------------------------------------------

func TestOrchestratorRefusesDNSOnly(t *testing.T) {
	e := newOrchEnv(t, workbench.ContentSelection{DNS: true})
	rr := e.start("giorginisposi")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("dns-only start = %d, want 303", rr.Code)
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "migrate=no_auto") {
		t.Errorf("redirect = %q, want migrate=no_auto", loc)
	}
	if len(e.fr.recorded()) != 0 {
		t.Errorf("DNS-only must never auto-run, got %v", e.callNames())
	}
}

// ---------------------------------------------------------------------------
// 4. scope site → only migrate_content with --file --db (no --mail)
// ---------------------------------------------------------------------------

func TestOrchestratorSiteScopeContentOnly(t *testing.T) {
	e := newOrchEnv(t, workbench.ContentSelection{Files: true, Databases: true})
	rr := e.start("giorginisposi")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("site start = %d (%s), want 303", rr.Code, rr.Body.String())
	}
	names := e.callNames()
	if len(names) != 1 || names[0] != "migrate content" {
		t.Fatalf("site scope must run exactly migrate content, got %v", names)
	}
	joined := strings.Join(argvFor(e.fr, "migrate content"), " ")
	if !strings.Contains(joined, "--file") || !strings.Contains(joined, "--db") {
		t.Errorf("site content argv missing --file/--db: %s", joined)
	}
	if strings.Contains(joined, "--mail") {
		t.Errorf("site scope must NOT pass --mail (email content excluded): %s", joined)
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "migrate=done") {
		t.Errorf("redirect = %q, want migrate=done", loc)
	}
}

// ---------------------------------------------------------------------------
// 5. scope email → migrate_content --mail + email_apply + email_verify(--fail-on-drift)
// ---------------------------------------------------------------------------

func TestOrchestratorEmailScopeRunsApplyVerify(t *testing.T) {
	e := newOrchEnv(t, workbench.ContentSelection{Email: true, EmailConfig: true})
	e.writePlan(t, "email_apply_plan.json") // makes email config auto/safe
	rr := e.start("giorginisposi")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("email start = %d (%s), want 303", rr.Code, rr.Body.String())
	}
	names := e.callNames()
	want := []string{"migrate content", "email apply", "email verify"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("email scope phases = %v, want %v", names, want)
	}
	content := strings.Join(argvFor(e.fr, "migrate content"), " ")
	if !strings.Contains(content, "--mail") {
		t.Errorf("email content must pass --mail: %s", content)
	}
	if strings.Contains(content, "--file") || strings.Contains(content, "--db") {
		t.Errorf("email-only content must not pass --file/--db: %s", content)
	}
	verify := strings.Join(argvFor(e.fr, "email verify"), " ")
	if !strings.Contains(verify, "--fail-on-drift") {
		t.Errorf("orchestrator email verify must gate on drift: %s", verify)
	}
}

// ---------------------------------------------------------------------------
// 6. cron included + plan present → cron_apply + cron_verify(--fail-on-drift)
// ---------------------------------------------------------------------------

func TestOrchestratorCronScopeRunsApplyVerify(t *testing.T) {
	e := newOrchEnv(t, workbench.ContentSelection{Files: true, Cron: true})
	e.writePlan(t, "cron_apply_plan.json")
	rr := e.start("giorginisposi")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("cron start = %d (%s), want 303", rr.Code, rr.Body.String())
	}
	names := e.callNames()
	want := []string{"migrate content", "cron apply", "cron verify"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("cron scope phases = %v, want %v", names, want)
	}
	verify := strings.Join(argvFor(e.fr, "cron verify"), " ")
	if !strings.Contains(verify, "--fail-on-drift") {
		t.Errorf("orchestrator cron verify must gate on drift: %s", verify)
	}
}

// A cron area IN SCOPE but WITHOUT a plan is not safe/automatic: it must NOT run.
func TestOrchestratorCronWithoutPlanNotRun(t *testing.T) {
	e := newOrchEnv(t, workbench.ContentSelection{Files: true, Cron: true})
	// no cron_apply_plan.json written
	rr := e.start("giorginisposi")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("start = %d, want 303", rr.Code)
	}
	if hasCall(e.callNames(), "cron apply") {
		t.Errorf("cron without a plan must not auto-run, got %v", e.callNames())
	}
}

// ---------------------------------------------------------------------------
// 7. DNS included never runs dns_apply / dns_verify
// ---------------------------------------------------------------------------

func TestOrchestratorNeverRunsDNS(t *testing.T) {
	e := newOrchEnv(t, workbench.ContentSelection{Files: true, DNS: true})
	rr := e.start("giorginisposi")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("start = %d, want 303", rr.Code)
	}
	names := e.callNames()
	for _, forbidden := range []string{"dns apply", "dns verify"} {
		if hasCall(names, forbidden) {
			t.Errorf("orchestrator must never run %q, got %v", forbidden, names)
		}
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "migrate=done_manual") {
		t.Errorf("DNS-included clean run should be done_manual, got %q", loc)
	}
}

// ---------------------------------------------------------------------------
// 8. strong-confirmation required ONCE (wrong → 403; one POST runs all phases)
// ---------------------------------------------------------------------------

func TestOrchestratorWrongConfirmation(t *testing.T) {
	e := newOrchEnv(t, workbench.ContentSelection{Files: true})
	rr := e.start("wrong-name")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("wrong confirmation = %d, want 403", rr.Code)
	}
	if len(e.fr.recorded()) != 0 {
		t.Errorf("wrong confirmation must run nothing, got %v", e.callNames())
	}
}

func TestOrchestratorSingleConfirmationRunsAllPhases(t *testing.T) {
	e := newOrchEnv(t, workbench.ContentSelection{Email: true, EmailConfig: true, Cron: true})
	e.writePlan(t, "email_apply_plan.json")
	e.writePlan(t, "cron_apply_plan.json")
	// A SINGLE POST with ONE confirm_account runs content + email + cron phases:
	// proof the orchestrator does not ask per-phase confirmation.
	rr := e.start("giorginisposi")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("start = %d (%s), want 303", rr.Code, rr.Body.String())
	}
	names := e.callNames()
	for _, want := range []string{"migrate content", "email apply", "email verify", "cron apply", "cron verify"} {
		if !hasCall(names, want) {
			t.Errorf("single confirmation should have run %q; phases=%v", want, names)
		}
	}
}

// ---------------------------------------------------------------------------
// 9. stop-on-first-failure: email_apply fails → cron never starts
// ---------------------------------------------------------------------------

func TestOrchestratorStopsOnApplyFailure(t *testing.T) {
	e := newOrchEnv(t, workbench.ContentSelection{Email: true, EmailConfig: true, Cron: true})
	e.writePlan(t, "email_apply_plan.json")
	e.writePlan(t, "cron_apply_plan.json")
	e.fr.fail = "email apply"
	rr := e.start("giorginisposi")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("start = %d, want 303", rr.Code)
	}
	names := e.callNames()
	if !hasCall(names, "migrate content") || !hasCall(names, "email apply") {
		t.Fatalf("phases before the failure must have run, got %v", names)
	}
	if hasCall(names, "email verify") {
		t.Errorf("verify must not run after a failed apply, got %v", names)
	}
	if hasCall(names, "cron apply") || hasCall(names, "cron verify") {
		t.Errorf("stop-on-first-failure: cron must not start after email apply failed, got %v", names)
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "migrate=partial") {
		t.Errorf("failed run should redirect partial, got %q", loc)
	}
}

// ---------------------------------------------------------------------------
// 10. verify failure stops subsequent phases
// ---------------------------------------------------------------------------

func TestOrchestratorStopsOnVerifyFailure(t *testing.T) {
	e := newOrchEnv(t, workbench.ContentSelection{Email: true, EmailConfig: true, Cron: true})
	e.writePlan(t, "email_apply_plan.json")
	e.writePlan(t, "cron_apply_plan.json")
	e.fr.fail = "email verify"
	rr := e.start("giorginisposi")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("start = %d, want 303", rr.Code)
	}
	names := e.callNames()
	if !hasCall(names, "email apply") || !hasCall(names, "email verify") {
		t.Fatalf("apply + verify must have been attempted, got %v", names)
	}
	if hasCall(names, "cron apply") {
		t.Errorf("a failed verify must stop the run before cron, got %v", names)
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "migrate=partial") {
		t.Errorf("verify failure should redirect partial, got %q", loc)
	}
}

// ---------------------------------------------------------------------------
// §14.3 — checklist that turns blocking MID-RUN stops before the next write
// ---------------------------------------------------------------------------

func TestOrchestratorGateReCheckedPerPhase(t *testing.T) {
	e := newOrchEnv(t, workbench.ContentSelection{Files: true, EmailConfig: true})
	e.writePlan(t, "email_apply_plan.json")
	// After the FIRST phase (migrate content) runs, flip the checklist to
	// blocking. The gate re-check before the email phase must stop the run.
	e.fr.onCall = func(name string) {
		if name == "migrate content" {
			writeChecklist(t, e.dir, accountinventory.MigrationChecklist{
				Mode: "migration-checklist", FormatVersion: 1,
				OverallStatus: accountinventory.OverallNotReady, ApplyBlocked: true,
			})
		}
	}
	rr := e.start("giorginisposi")
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("start = %d, want 303", rr.Code)
	}
	names := e.callNames()
	if !hasCall(names, "migrate content") {
		t.Fatalf("first phase should have run, got %v", names)
	}
	if hasCall(names, "email apply") {
		t.Errorf("gate re-check must stop before email apply once the checklist blocks, got %v", names)
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "migrate=gate_stopped") {
		t.Errorf("mid-run gate close should redirect gate_stopped, got %q", loc)
	}
}

// ---------------------------------------------------------------------------
// 11. busy slot → readable 409
// ---------------------------------------------------------------------------

func TestOrchestratorBusySlot409(t *testing.T) {
	e := newOrchEnv(t, workbench.ContentSelection{Files: true})
	e.fr.gate = make(chan struct{})
	done := make(chan struct{})
	go func() {
		e.start("giorginisposi") // blocks in the first phase holding the slot
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	got409 := false
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		rr := e.start("giorginisposi")
		if rr.Code == http.StatusConflict {
			got409 = true
			if !strings.Contains(rr.Body.String(), "in corso") {
				t.Errorf("busy 409 body not human: %q", rr.Body.String())
			}
			break
		}
	}
	close(e.fr.gate)
	<-done
	if !got409 {
		t.Fatal("second start never got a 409 while a run held the slot")
	}
}

// ---------------------------------------------------------------------------
// 12. timeline records the phase outcomes
// ---------------------------------------------------------------------------

func TestOrchestratorTimelineRecorded(t *testing.T) {
	e := newOrchEnv(t, workbench.ContentSelection{Files: true})
	if rr := e.start("giorginisposi"); rr.Code != http.StatusSeeOther {
		t.Fatalf("start = %d, want 303", rr.Code)
	}
	sess, err := e.store.Get(e.sessID)
	if err != nil {
		t.Fatal(err)
	}
	last := sess.Timeline[len(sess.Timeline)-1]
	if !strings.Contains(last.Reason, "avvio migrazione") {
		t.Errorf("timeline reason missing 'avvio migrazione': %q", last.Reason)
	}
	if !strings.Contains(last.Reason, "content=completed_with_report") {
		t.Errorf("timeline reason missing content outcome: %q", last.Reason)
	}
}

// ---------------------------------------------------------------------------
// 13. UI shows the active start button only when ready + scope confirmed
// ---------------------------------------------------------------------------

func TestOrchestratorUIShowsStartButtonWhenReady(t *testing.T) {
	e := newOrchEnv(t, workbench.ContentSelection{Files: true, Databases: true})
	code, body := getBody(t, e.h, "/workbench/session/"+e.sessID+"/migrazione")
	if code != 200 {
		t.Fatalf("migrazione = %d, want 200", code)
	}
	if !strings.Contains(body, "/start-migration") {
		t.Error("ready + confirmed plan must render the start-migration form")
	}
	if !strings.Contains(body, "confirm_account") {
		t.Error("start form must carry the strong-confirmation field")
	}
}

func TestOrchestratorUIHidesStartButtonWhenUnconfirmed(t *testing.T) {
	// Setup present but scope not confirmed → no active button.
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "migrations")
	os.MkdirAll(storeDir, 0o700)
	store, _ := workbench.NewStore(storeDir)
	sess, _ := store.CreateWithSetup("giorginisposi", "src", "dst",
		&workbench.SetupMeta{Content: workbench.ContentSelection{Files: true}}, time.Now())
	writeChecklist(t, dir, readyChecklist())
	h, _ := New(Options{Dir: dir, SessionStore: store})
	code, body := getBody(t, h, "/workbench/session/"+sess.ID+"/migrazione")
	if code != 200 {
		t.Fatalf("migrazione = %d, want 200", code)
	}
	if strings.Contains(body, "/start-migration") {
		t.Error("an unconfirmed scope must NOT render the active start button")
	}
	if !strings.Contains(body, "Conferma lo scope prima di avviare") {
		t.Error("unconfirmed CTA label missing")
	}
}

// ---------------------------------------------------------------------------
// 14. UI shows partial state after a failure (flash message)
// ---------------------------------------------------------------------------

func TestOrchestratorUIShowsPartialState(t *testing.T) {
	e := newOrchEnv(t, workbench.ContentSelection{Files: true})
	code, body := getBody(t, e.h, "/workbench/session/"+e.sessID+"/migrazione?migrate=partial")
	if code != 200 {
		t.Fatalf("migrazione = %d, want 200", code)
	}
	if !strings.Contains(body, "interrotta al primo errore") {
		t.Errorf("partial flash message not rendered; body lacks the human copy")
	}
	if !strings.Contains(body, "Nessun rollback automatico") {
		t.Errorf("partial flash must state no automatic rollback happened")
	}
}

// ---------------------------------------------------------------------------
// 15. legacy session without Setup is not startable
// ---------------------------------------------------------------------------

func TestOrchestratorLegacySessionNotStartable(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "migrations")
	os.MkdirAll(storeDir, 0o700)
	store, _ := workbench.NewStore(storeDir)
	sess, _ := store.Create("giorginisposi", "src", "dst", time.Now()) // Setup == nil
	writeChecklist(t, dir, readyChecklist())
	fr := &fakeRunner{}
	h, _ := New(Options{Dir: dir, Runner: fr.run, SessionStore: store})
	csrf := fetchCSRF(t, h)

	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sess.ID+"/start-migration",
		url.Values{"csrf": {csrf}, "confirm_account": {"giorginisposi"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("legacy start = %d, want 303", rr.Code)
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "migrate=needs_setup") {
		t.Errorf("redirect = %q, want migrate=needs_setup", loc)
	}
	if len(fr.recorded()) != 0 {
		t.Errorf("legacy session must never auto-run, got %v", fr.recorded())
	}
}

// ---------------------------------------------------------------------------
// Per-phase timeout: each step gets its OWN execTimeout clock, not one shared
// deadline across the whole run (a long content phase must never eat the budget
// of a later write and get it killed mid-flight).
// ---------------------------------------------------------------------------

func TestOrchestratorPerPhaseTimeout(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "migrations")
	os.MkdirAll(storeDir, 0o700)
	store, _ := workbench.NewStore(storeDir)
	sess, _ := store.Create("acct", "s", "d", time.Now())
	writeChecklist(t, dir, readyChecklist())

	var mu sync.Mutex
	var deadlines []time.Time
	var dones []<-chan struct{}
	runner := func(ctx context.Context, out io.Writer, name string, argv []string) error {
		mu.Lock()
		dl, ok := ctx.Deadline()
		if !ok {
			dl = time.Time{}
		}
		deadlines = append(deadlines, dl)
		dones = append(dones, ctx.Done())
		mu.Unlock()
		return nil
	}
	ws := &workbenchExecServer{
		store: store, runner: runner, base: context.Background(),
		job: newJobManager(dir, runner, context.Background()), dir: dir,
	}
	phases := []orchestratorPhase{
		{key: "a", label: "A", write: orchestratorStep{name: "step a", argv: []string{"x"}}, reportOnly: true},
		{key: "b", label: "B", write: orchestratorStep{name: "step b", argv: []string{"y"}}, reportOnly: true},
	}
	res := ws.runOrchestration(context.Background(), sess.ID, phases, time.Now().UTC())
	if res.Stopped {
		t.Fatalf("unexpected stop: %+v", res)
	}
	if len(deadlines) != 2 {
		t.Fatalf("want 2 steps executed, got %d", len(deadlines))
	}
	for i, dl := range deadlines {
		if dl.IsZero() {
			t.Errorf("step %d ran without a per-step deadline", i)
		}
	}
	// Distinct contexts: two separate WithTimeout scopes have different Done
	// channels — proof each phase got its own timeout, not one shared clock.
	if dones[0] == dones[1] {
		t.Error("both phases shared the same context — per-phase timeout not applied")
	}
}

// ---------------------------------------------------------------------------
// Missing CSRF is rejected (route goes through server.post).
// ---------------------------------------------------------------------------

func TestOrchestratorRequiresCSRF(t *testing.T) {
	e := newOrchEnv(t, workbench.ContentSelection{Files: true})
	rr := doWorkbenchReq(e.h, http.MethodPost, "/workbench/session/"+e.sessID+"/start-migration",
		url.Values{"confirm_account": {"giorginisposi"}}) // no csrf
	if rr.Code != http.StatusForbidden {
		t.Fatalf("missing csrf = %d, want 403", rr.Code)
	}
	if len(e.fr.recorded()) != 0 {
		t.Errorf("nothing must run without CSRF, got %v", e.callNames())
	}
}
