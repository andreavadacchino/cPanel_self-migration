package webui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newExecTestEnv creates a temp dir with a valid session and returns the
// handler + session ID + CSRF token + the fake runner.
func newExecTestEnv(t *testing.T) (http.Handler, string, string, *fakeRunner) {
	t.Helper()
	dir := t.TempDir()

	// Create a session store in a subdir
	storeDir := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := workbench.NewStore(storeDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// Create a session and advance it to ready_for_apply
	sess, err := store.Create("giorginisposi", "source193", "dest78", time.Now())
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	// Advance through states to ready_for_apply
	transitions := []workbench.Status{
		workbench.StatusPreflightRequired,
		workbench.StatusInventoryReady,
		workbench.StatusChecklistReady,
		workbench.StatusReadyForApply,
	}
	for _, s := range transitions {
		if _, err := store.SetStatus(sess.ID, s, false, "", time.Now()); err != nil {
			t.Fatalf("SetStatus %s: %v", s, err)
		}
	}

	// Write a minimal host.yaml so commands find a config
	hostYAML := filepath.Join(dir, "host.yaml")
	if err := os.WriteFile(hostYAML, []byte("source:\n  ip: 1.2.3.4\n"), 0600); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRunner{}
	h, err := New(Options{
		Dir:          dir,
		Runner:       fr.run,
		SessionStore: store,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	csrf := fetchCSRF(t, h)
	return h, sess.ID, csrf, fr
}

// doWorkbenchReq performs a request to a workbench exec endpoint.
func doWorkbenchReq(h http.Handler, method, path string, form url.Values) *httptest.ResponseRecorder {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Host = testHost
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// ---------------------------------------------------------------------------
// Test: write action WITHOUT strong confirmation → 403
// ---------------------------------------------------------------------------

func TestExecWriteWithoutConfirmation(t *testing.T) {
	h, sessID, csrf, fr := newExecTestEnv(t)

	// POST with valid CSRF but NO confirm_account field
	form := url.Values{
		"csrf":   {csrf},
		"action": {"dns_apply"},
	}
	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)
	if rr.Code != http.StatusForbidden {
		t.Errorf("missing confirm_account: got %d, want 403", rr.Code)
	}

	// Verify NO subprocess was invoked
	if len(fr.recorded()) > 0 {
		t.Error("subprocess invoked despite missing confirmation")
	}
}

// ---------------------------------------------------------------------------
// Test: write action with WRONG confirm_account → 403
// ---------------------------------------------------------------------------

func TestExecWriteWrongConfirmation(t *testing.T) {
	h, sessID, csrf, fr := newExecTestEnv(t)

	form := url.Values{
		"csrf":            {csrf},
		"action":          {"dns_apply"},
		"confirm_account": {"wrong_account_name"},
	}
	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)
	if rr.Code != http.StatusForbidden {
		t.Errorf("wrong confirm_account: got %d, want 403", rr.Code)
	}

	if len(fr.recorded()) > 0 {
		t.Error("subprocess invoked despite wrong confirmation")
	}
}

// ---------------------------------------------------------------------------
// Test: write action without CSRF → 403
// ---------------------------------------------------------------------------

func TestExecWriteWithoutCSRF(t *testing.T) {
	h, sessID, _, fr := newExecTestEnv(t)

	form := url.Values{
		"action":          {"dns_apply"},
		"confirm_account": {"giorginisposi"},
	}
	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)
	if rr.Code != http.StatusForbidden {
		t.Errorf("missing csrf: got %d, want 403", rr.Code)
	}

	if len(fr.recorded()) > 0 {
		t.Error("subprocess invoked despite missing CSRF")
	}
}

// ---------------------------------------------------------------------------
// Test: read-only action works with just CSRF (no confirm_account needed)
// ---------------------------------------------------------------------------

func TestExecReadOnlyNoConfirmation(t *testing.T) {
	h, sessID, csrf, fr := newExecTestEnv(t)

	form := url.Values{
		"csrf":   {csrf},
		"action": {"dns_verify"},
	}
	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)
	// Should be accepted (302 redirect or 200) — not 403
	if rr.Code == http.StatusForbidden {
		t.Errorf("read-only action rejected: got %d", rr.Code)
	}

	// Verify subprocess WAS invoked
	calls := fr.recorded()
	if len(calls) == 0 {
		t.Error("no subprocess invoked for read-only action")
	}
}

// ---------------------------------------------------------------------------
// Test: write action with CORRECT confirmation → subprocess invoked
// ---------------------------------------------------------------------------

func TestExecWriteCorrectConfirmation(t *testing.T) {
	h, sessID, csrf, fr := newExecTestEnv(t)

	form := url.Values{
		"csrf":            {csrf},
		"action":          {"dns_apply"},
		"confirm_account": {"giorginisposi"},
	}
	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)
	if rr.Code == http.StatusForbidden {
		t.Errorf("correct confirmation rejected: got %d, body: %s", rr.Code, rr.Body.String())
	}

	calls := fr.recorded()
	if len(calls) == 0 {
		t.Fatal("no subprocess invoked despite correct confirmation")
	}
}

// ---------------------------------------------------------------------------
// Test: golden argv — dns apply invocation matches expected args
// ---------------------------------------------------------------------------

func TestExecGoldenArgvDNSApply(t *testing.T) {
	h, sessID, csrf, fr := newExecTestEnv(t)

	form := url.Values{
		"csrf":            {csrf},
		"action":          {"dns_apply"},
		"confirm_account": {"giorginisposi"},
	}
	doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)

	calls := fr.recorded()
	if len(calls) == 0 {
		t.Fatal("no subprocess invoked")
	}

	argv := calls[0].argv
	// Must contain "dns", "apply", "--yes-apply-writes", "--backup"
	joined := strings.Join(argv, " ")
	for _, must := range []string{"dns", "apply", "--yes-apply-writes", "--backup"} {
		if !strings.Contains(joined, must) {
			t.Errorf("argv missing %q: got %v", must, argv)
		}
	}
	// Must NOT use shell interpolation (no "sh", no "-c")
	for _, forbidden := range []string{"sh", "-c", "bash"} {
		if argv[0] == forbidden {
			t.Errorf("argv[0] is %q — must be direct exec, not shell", forbidden)
		}
	}
}

// ---------------------------------------------------------------------------
// Test: golden argv — dns verify (read-only) does NOT have --yes-apply-writes
// ---------------------------------------------------------------------------

func TestExecGoldenArgvDNSVerify(t *testing.T) {
	h, sessID, csrf, fr := newExecTestEnv(t)

	form := url.Values{
		"csrf":   {csrf},
		"action": {"dns_verify"},
	}
	doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)

	calls := fr.recorded()
	if len(calls) == 0 {
		t.Fatal("no subprocess invoked")
	}

	argv := calls[0].argv
	joined := strings.Join(argv, " ")
	if strings.Contains(joined, "--yes-apply-writes") {
		t.Errorf("read-only verify must NOT have --yes-apply-writes: got %v", argv)
	}
	for _, must := range []string{"dns", "verify"} {
		if !strings.Contains(joined, must) {
			t.Errorf("argv missing %q: got %v", must, argv)
		}
	}
}

// ---------------------------------------------------------------------------
// Test: unknown action → 400
// ---------------------------------------------------------------------------

func TestExecUnknownAction(t *testing.T) {
	h, sessID, csrf, _ := newExecTestEnv(t)

	form := url.Values{
		"csrf":   {csrf},
		"action": {"hack_the_planet"},
	}
	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("unknown action: got %d, want 400", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Test: busy slot → 409
// ---------------------------------------------------------------------------

func TestExecBusySlot(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := workbench.NewStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create("giorginisposi", "source193", "dest78", time.Now())
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
	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte("source:\n  ip: 1.2.3.4\n"), 0600); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRunner{}
	h, err := New(Options{Dir: dir, Runner: fr.run, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	csrf := fetchCSRF(t, h)

	// Pre-acquire the exec slot to simulate a running exec
	// Access via the unexported field by extracting the handler
	// Instead, just make two quick successive requests where the first
	// uses a blocking runner.
	// Simplest approach: use the server struct's wbExec slot directly.
	// Since we can't access unexported fields from the test, we verify the
	// behavior by testing that the slot mechanism works at the exec level.

	// Alternative: use a gate that signals AFTER slot acquisition
	fr.gate = make(chan struct{})
	done := make(chan struct{})

	form := url.Values{"csrf": {csrf}, "action": {"dns_verify"}}
	go func() {
		doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sess.ID+"/exec", form)
		close(done)
	}()

	// Give the goroutine time to acquire the slot (race-detector safe:
	// we retry until we see 409 or timeout)
	deadline := time.Now().Add(5 * time.Second)
	got409 := false
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sess.ID+"/exec", form)
		if rr.Code == http.StatusConflict {
			got409 = true
			break
		}
	}
	close(fr.gate)
	<-done
	if !got409 {
		t.Fatal("never got 409 Conflict — slot never became busy")
	}
}

// ---------------------------------------------------------------------------
// Test: session timeline records execution (integration with store)
// ---------------------------------------------------------------------------

func TestExecTimelineRecorded(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := workbench.NewStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	sess, err := store.Create("giorginisposi", "source193", "dest78", time.Now())
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

	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte("source:\n  ip: 1.2.3.4\n"), 0600); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRunner{}
	h, err := New(Options{Dir: dir, Runner: fr.run, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	csrf := fetchCSRF(t, h)

	form := url.Values{
		"csrf":   {csrf},
		"action": {"dns_verify"},
	}
	doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sess.ID+"/exec", form)

	// Check timeline has the exec event
	updated, err := store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range updated.Timeline {
		if strings.Contains(ev.Reason, "exec: dns verify") {
			found = true
			break
		}
	}
	if !found {
		t.Error("timeline does not contain exec event")
	}
}

// ---------------------------------------------------------------------------
// Test: artifact NOT attached on subprocess failure
// ---------------------------------------------------------------------------

func TestExecNoArtifactOnFailure(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := workbench.NewStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	sess, err := store.Create("giorginisposi", "source193", "dest78", time.Now())
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

	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte("source:\n  ip: 1.2.3.4\n"), 0600); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRunner{fail: "dns verify"}
	h, err := New(Options{Dir: dir, Runner: fr.run, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	csrf := fetchCSRF(t, h)

	form := url.Values{
		"csrf":   {csrf},
		"action": {"dns_verify"},
	}
	doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sess.ID+"/exec", form)

	// Verify: no artifact was attached
	updated, err := store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, art := range updated.Artifacts {
		if art.Kind == workbench.ArtifactDNSVerifyReport {
			t.Error("artifact attached despite subprocess failure")
		}
	}
}

// ---------------------------------------------------------------------------
// Test: migrate content requires scope selection
// ---------------------------------------------------------------------------

func TestExecMigrateContentRequiresScope(t *testing.T) {
	h, sessID, csrf, fr := newExecTestEnv(t)

	// migrate_content with NO scope checkboxes
	form := url.Values{
		"csrf":            {csrf},
		"action":          {"migrate_content"},
		"confirm_account": {"giorginisposi"},
	}
	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("migrate without scope: got %d, want 400", rr.Code)
	}
	if len(fr.recorded()) > 0 {
		t.Error("subprocess invoked despite missing scope")
	}
}

// ---------------------------------------------------------------------------
// Test: migrate content golden argv with scope
// ---------------------------------------------------------------------------

func TestExecGoldenArgvMigrateContent(t *testing.T) {
	h, sessID, csrf, fr := newExecTestEnv(t)

	form := url.Values{
		"csrf":            {csrf},
		"action":          {"migrate_content"},
		"confirm_account": {"giorginisposi"},
		"scope_mail":      {"1"},
		"scope_db":        {"1"},
	}
	doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)

	calls := fr.recorded()
	if len(calls) == 0 {
		t.Fatal("no subprocess invoked")
	}

	argv := calls[0].argv
	joined := strings.Join(argv, " ")
	for _, must := range []string{"--apply", "--mail", "--db", "--config", "--report-json", "--json-events"} {
		if !strings.Contains(joined, must) {
			t.Errorf("argv missing %q: got %v", must, argv)
		}
	}
	// --file should NOT be present (not selected)
	if strings.Contains(joined, "--file") {
		t.Errorf("argv has --file despite not selected: got %v", argv)
	}
}

// ---------------------------------------------------------------------------
// Test: run_pipeline action invokes multiple steps
// ---------------------------------------------------------------------------

func TestExecRunPipeline(t *testing.T) {
	h, sessID, csrf, fr := newExecTestEnv(t)

	form := url.Values{
		"csrf":   {csrf},
		"action": {"run_pipeline"},
	}
	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)
	if rr.Code == http.StatusForbidden || rr.Code == http.StatusBadRequest {
		t.Fatalf("run_pipeline rejected: %d %s", rr.Code, rr.Body.String())
	}

	calls := fr.recorded()
	if len(calls) < 5 {
		t.Fatalf("run_pipeline should invoke 5 steps, got %d", len(calls))
	}
	if calls[0].name != "account inventory" {
		t.Errorf("step 0 name = %q, want 'account inventory'", calls[0].name)
	}
	// dns-plan runs before the checklist so the first checklist already
	// carries the DNS import actions (dogfooding #2 N4).
	if calls[3].name != "inventory dns-plan" {
		t.Errorf("step 3 name = %q, want 'inventory dns-plan'", calls[3].name)
	}
	if calls[4].name != "inventory checklist" {
		t.Errorf("step 4 name = %q, want 'inventory checklist'", calls[4].name)
	}
}

// TestExecRunPipelineTolerantDNSPlanFailure pins the N4 tolerance on the
// workbench entry point: a dns-plan failure must not abort run_pipeline — the
// checklist step still runs so a partial checklist is produced.
func TestExecRunPipelineTolerantDNSPlanFailure(t *testing.T) {
	h, sessID, csrf, fr := newExecTestEnv(t)
	fr.fail = "inventory dns-plan"

	form := url.Values{
		"csrf":   {csrf},
		"action": {"run_pipeline"},
	}
	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)
	if rr.Code >= 400 {
		t.Fatalf("run_pipeline aborted on a tolerated dns-plan failure: %d %s", rr.Code, rr.Body.String())
	}

	calls := fr.recorded()
	if len(calls) != 5 {
		t.Fatalf("steps executed = %d, want 5 (dns-plan failure tolerated, pipeline continues)", len(calls))
	}
	if calls[4].name != "inventory checklist" {
		t.Errorf("last step = %q, want the checklist to run despite the dns-plan failure", calls[4].name)
	}
}

// ---------------------------------------------------------------------------
// Test: dns_plan action (read-only, no confirmation needed)
// ---------------------------------------------------------------------------

func TestExecDNSPlan(t *testing.T) {
	h, sessID, csrf, fr := newExecTestEnv(t)

	form := url.Values{
		"csrf":   {csrf},
		"action": {"dns_plan"},
	}
	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)
	if rr.Code == http.StatusForbidden || rr.Code == http.StatusBadRequest {
		t.Fatalf("dns_plan rejected: %d %s", rr.Code, rr.Body.String())
	}

	calls := fr.recorded()
	if len(calls) == 0 {
		t.Fatal("no subprocess invoked for dns_plan")
	}
	argv := calls[0].argv
	joined := strings.Join(argv, " ")
	for _, must := range []string{"inventory", "dns-plan", "--source", "--destination", "--output-json"} {
		if !strings.Contains(joined, must) {
			t.Errorf("dns_plan argv missing %q: got %v", must, argv)
		}
	}
}

// ---------------------------------------------------------------------------
// Test: rollback requires DOUBLE strong confirmation
// ---------------------------------------------------------------------------

func TestExecRollbackRequiresDoubleConfirmation(t *testing.T) {
	h, sessID, csrf, fr := newExecTestEnv(t)

	// rollback with single confirmation only
	form := url.Values{
		"csrf":            {csrf},
		"action":          {"dns_rollback"},
		"confirm_account": {"giorginisposi"},
	}
	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)
	if rr.Code != http.StatusForbidden {
		t.Errorf("rollback without double confirm: got %d, want 403", rr.Code)
	}
	if len(fr.recorded()) > 0 {
		t.Error("subprocess invoked despite missing double confirmation")
	}
}

func TestExecRollbackWithDoubleConfirmation(t *testing.T) {
	h, sessID, csrf, fr := newExecTestEnv(t)

	form := url.Values{
		"csrf":             {csrf},
		"action":           {"dns_rollback"},
		"confirm_account":  {"giorginisposi"},
		"confirm_rollback": {"giorginisposi"},
	}
	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)
	if rr.Code == http.StatusForbidden {
		t.Errorf("double-confirmed rollback rejected: %d %s", rr.Code, rr.Body.String())
	}

	calls := fr.recorded()
	if len(calls) == 0 {
		t.Fatal("no subprocess invoked for confirmed rollback")
	}
	argv := calls[0].argv
	joined := strings.Join(argv, " ")
	// Must have --rollback, --output-json (distinct from input report), --yes-apply-writes
	for _, must := range []string{"--rollback", "--output-json", "--yes-apply-writes", "dns_rollback_report.json"} {
		if !strings.Contains(joined, must) {
			t.Errorf("rollback argv missing %q: got %v", must, argv)
		}
	}
	// Output file must be DIFFERENT from the input report
	if strings.Count(joined, "dns_apply_report.json") != 1 {
		t.Errorf("rollback output should NOT reuse input report filename; argv: %v", argv)
	}
}

