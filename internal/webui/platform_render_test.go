package webui

// Platform UI V2 — HTTP render tests. Each test pins one of the 11 mandatory
// cases from the brief: the dashboard/wizard/plan/cockpit/comparison render
// with real data and honest fallbacks, the expert-mode link is always present,
// the old workbench still works, no technical surface (host.yaml / SHA / state
// change) leaks into the primary path, and the start gate is single-sourced so
// the hero and the CTA never diverge.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

func newPlatformTest(t *testing.T, dir string, busy bool) (*platformServer, *workbench.Store) {
	t.Helper()
	store := mustStore(t, dir)
	ps, err := newPlatformServer(store, dir, "csrf-x", func() bool { return busy })
	if err != nil {
		t.Fatalf("newPlatformServer: %v", err)
	}
	return ps, store
}

func platformSessionBody(t *testing.T, ps *platformServer, id, screen string) string {
	t.Helper()
	rr := httptest.NewRecorder()
	ps.handleSession(rr, httptest.NewRequest(http.MethodGet, "/platform/migrations/"+id+"/"+screen, nil), id, screen)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET platform %s = %d, want 200", screen, rr.Code)
	}
	return rr.Body.String()
}

// Case 1: the dashboard renders with no sessions (no nil deref) and offers the
// primary CTA + an honest empty state.
func TestPlatformDashboardEmptyRenders(t *testing.T) {
	ps, _ := newPlatformTest(t, t.TempDir(), false)
	rr := httptest.NewRecorder()
	ps.handleDashboard(rr, httptest.NewRequest(http.MethodGet, "/platform/migrations", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("dashboard = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"Migrazioni", "Nuova migrazione", "Nessuna migrazione"} {
		if !strings.Contains(body, want) {
			t.Errorf("empty dashboard missing %q", want)
		}
	}
}

// Case 8: the dashboard lists real sessions with a status and a next action
// coherent with that status.
func TestPlatformDashboardListsSessionsCoherently(t *testing.T) {
	dir := t.TempDir()
	ps, store := newPlatformTest(t, dir, false)
	sess, _ := store.Create("giorgini", "acc@src", "acc@dst", time.Now())
	if _, err := store.SetStatus(sess.ID, workbench.StatusPreflightRequired, false, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	ps.handleDashboard(rr, httptest.NewRequest(http.MethodGet, "/platform/migrations", nil))
	body := rr.Body.String()
	for _, want := range []string{"giorgini", "Preflight richiesto", "Esegui preflight"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard row missing %q", want)
		}
	}
}

// Case 2: the wizard shows the clear numbered steps and the real form fields.
func TestPlatformWizardShowsSteps(t *testing.T) {
	ps, _ := newPlatformTest(t, t.TempDir(), false)
	rr := httptest.NewRecorder()
	ps.handleWizardForm(rr, httptest.NewRequest(http.MethodGet, "/platform/migrations/new", nil))
	body := rr.Body.String()
	for _, want := range []string{"Nome migrazione", "Sorgente", "Destinazione", "Contenuti", "Riepilogo",
		`name="csrf"`, `name="src_host"`, `name="dst_account"`, "Cosa vuoi migrare", "Crea migrazione"} {
		if !strings.Contains(body, want) {
			t.Errorf("wizard missing %q", want)
		}
	}
}

// The platform wizard reuses the shared validation and store, creating a
// session and redirecting INTO the platform cockpit.
func TestPlatformWizardCreatesSessionAndRedirects(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	h, err := New(Options{Dir: dir, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	csrf := fetchCSRF(t, h)
	form := url.Values{
		"csrf": {csrf}, "name": {"Migrazione X"},
		"src_host": {"1.2.3.4"}, "src_account": {"acc"},
		"dst_host": {"5.6.7.8"}, "dst_account": {"acc2"},
		"content_files": {"1"},
	}
	rr := doReq(h, http.MethodPost, "/platform/migrations/new", form)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("wizard POST = %d, want 303\n%s", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); !strings.HasPrefix(loc, "/platform/migrations/mig_") {
		t.Errorf("redirect = %q, want a platform cockpit route", loc)
	}
	sessions, _, _ := store.List()
	if len(sessions) != 1 {
		t.Fatalf("sessions after wizard = %d, want 1", len(sessions))
	}
}

// An invalid wizard submit re-renders with Italian errors and creates NO
// session (the shared validation path is enforced on the platform too).
func TestPlatformWizardInvalidReRenders(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	h, err := New(Options{Dir: dir, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	csrf := fetchCSRF(t, h)
	rr := doReq(h, http.MethodPost, "/platform/migrations/new", url.Values{"csrf": {csrf}}) // no name/host
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid wizard = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "obbligatorio") {
		t.Error("invalid wizard must show a readable Italian error")
	}
	if sessions, _, _ := store.List(); len(sessions) != 0 {
		t.Errorf("a rejected wizard must not create a session, got %d", len(sessions))
	}
}

// Case 3: the plan degrades honestly without a checklist and, with one, uses
// only real classification (no invented numbers).
func TestPlatformPlanHonestFallbackAndRealData(t *testing.T) {
	// No checklist → honest not-ready message.
	dir := t.TempDir()
	ps, store := newPlatformTest(t, dir, false)
	sess, _ := store.Create("giorgini", "acc@src", "acc@dst", time.Now())
	body := platformSessionBody(t, ps, sess.ID, "plan")
	if !strings.Contains(body, "Esegui prima il preflight") {
		t.Error("plan without a checklist must show the honest not-ready message")
	}

	// With a ready checklist + confirmed scope → real buckets.
	env := newOrchEnv(t, workbench.ContentSelection{Files: true, Databases: true})
	ps2, err := newPlatformServer(env.store, env.dir, env.csrf, func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	body2 := platformSessionBody(t, ps2, env.sessID, "plan")
	for _, want := range []string{"Piano migrazione", "Automatico", "Conferma scope"} {
		if !strings.Contains(body2, want) {
			t.Errorf("ready plan missing %q", want)
		}
	}
}

// Case 4: the cockpit shows honest progress. Without a job the monitor is
// explicitly inactive; with a running job it shows the phase and state.
func TestPlatformCockpitMonitorHonest(t *testing.T) {
	// No job → monitor inactive.
	dir := t.TempDir()
	ps, store := newPlatformTest(t, dir, false)
	sess, _ := store.Create("giorgini", "acc@src", "acc@dst", time.Now())
	if body := platformSessionBody(t, ps, sess.ID, "cockpit"); !strings.Contains(body, "Monitor non attivo") {
		t.Error("cockpit without a job must show the monitor as inactive (no invented progress)")
	}

	// Running job → active monitor with phase + state.
	dir2 := t.TempDir()
	ps2, store2 := newPlatformTest(t, dir2, true)
	sess2, _ := store2.Create("giorgini", "acc@src", "acc@dst", time.Now())
	writeJobJournal(dir2, jobJournal{
		SessionID: sess2.ID, Action: orchestratorAction,
		StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		State: jobStateRunning, Phase: "Contenuti",
	})
	body := platformSessionBody(t, ps2, sess2.ID, "cockpit")
	for _, want := range []string{"Monitor esecuzione", "Migrazione in corso"} {
		if !strings.Contains(body, want) {
			t.Errorf("running cockpit missing %q", want)
		}
	}
}

// Case 5: the comparison screen degrades honestly — no invented rows and an
// explicit "file-level detail unavailable" note.
func TestPlatformCompareDegradesHonestly(t *testing.T) {
	dir := t.TempDir()
	ps, store := newPlatformTest(t, dir, false)
	sess, _ := store.Create("giorgini", "acc@src", "acc@dst", time.Now())
	body := platformSessionBody(t, ps, sess.ID, "compare")
	if !strings.Contains(body, "Confronto non ancora disponibile") {
		t.Error("comparison without data must say so honestly")
	}
	if !strings.Contains(body, "non è disponibile in questa versione") {
		t.Error("the file-level detail panel must degrade honestly")
	}
}

// Case 6: the expert-mode link is present on every session screen.
func TestPlatformExpertLinkOnEveryScreen(t *testing.T) {
	dir := t.TempDir()
	ps, store := newPlatformTest(t, dir, false)
	sess, _ := store.Create("giorgini", "acc@src", "acc@dst", time.Now())
	want := "/workbench/session/" + sess.ID
	for _, screen := range []string{"cockpit", "plan", "tasks", "report", "compare"} {
		if body := platformSessionBody(t, ps, sess.ID, screen); !strings.Contains(body, want) {
			t.Errorf("screen %q missing the expert-mode link %q", screen, want)
		}
	}
}

// Case 7: the old workbench is not broken by the platform + wizard refactor.
func TestWorkbenchNotBrokenByPlatform(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "acc@src", "acc@dst", time.Now())
	h, err := New(Options{Dir: dir, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/workbench", "/workbench/session/" + sess.ID, "/workbench/new"} {
		if rr := doReq(h, http.MethodGet, path, nil); rr.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (workbench must keep working)", path, rr.Code)
		}
	}
	// The workbench wizard still redirects into /workbench (refactor regression).
	csrf := fetchCSRF(t, h)
	form := url.Values{
		"csrf": {csrf}, "name": {"WB"}, "src_host": {"1.1.1.1"}, "src_account": {"a"},
		"dst_host": {"2.2.2.2"}, "dst_account": {"b"}, "content_files": {"1"},
	}
	rr := doReq(h, http.MethodPost, "/workbench/new", form)
	if rr.Code != http.StatusSeeOther || !strings.HasPrefix(rr.Header().Get("Location"), "/workbench/session/") {
		t.Errorf("workbench wizard = %d loc=%q, want 303 → /workbench/session/", rr.Code, rr.Header().Get("Location"))
	}
}

// Case 9: no technical surface leaks into the primary platform path.
func TestPlatformNoTechnicalLeakage(t *testing.T) {
	env := newOrchEnv(t, workbench.ContentSelection{Files: true, Databases: true})
	forbidden := []string{"host.yaml", "SHA256", "sha256", "Forza transizione", "Cambia stato"}
	for _, path := range []string{
		"/platform/migrations",
		"/platform/migrations/" + env.sessID,
		"/platform/migrations/" + env.sessID + "/plan",
	} {
		rr := doReq(env.h, http.MethodGet, path, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rr.Code)
		}
		body := rr.Body.String()
		for _, bad := range forbidden {
			if strings.Contains(body, bad) {
				t.Errorf("primary path %s leaks technical surface %q", path, bad)
			}
		}
	}
}

// Case 10 + 11: the start gate is single-sourced. A startable session exposes
// the "Avvia migrazione" CTA pointing at the tested workbench start form and
// NEVER the raw strong-confirmation form; a draft session exposes neither, and
// the hero never diverges from the CTA.
func TestPlatformStartGateSingleSourced(t *testing.T) {
	// Startable session (ready_for_apply, confirmed scope, ready checklist).
	env := newOrchEnv(t, workbench.ContentSelection{Files: true, Databases: true})
	rr := doReq(env.h, http.MethodGet, "/platform/migrations/"+env.sessID, nil)
	body := rr.Body.String()
	startLink := "/workbench/session/" + env.sessID + "/migrazione"
	if !strings.Contains(body, "Avvia migrazione") || !strings.Contains(body, startLink) {
		t.Error("startable cockpit must show the Avvia migrazione CTA linking to the tested workbench form")
	}
	if strings.Contains(body, "confirm_account") {
		t.Error("the platform must NOT render the raw strong-confirmation start form (it delegates)")
	}

	// Draft session: not startable → no start link, hero not 'ready'.
	dir := t.TempDir()
	ps, store := newPlatformTest(t, dir, false)
	sess, _ := store.Create("giorgini", "acc@src", "acc@dst", time.Now())
	draft := platformSessionBody(t, ps, sess.ID, "cockpit")
	if strings.Contains(draft, "/workbench/session/"+sess.ID+"/migrazione") {
		t.Error("a draft session must not expose the start form link")
	}
	if strings.Contains(draft, "Pronta per migrare") {
		t.Error("a draft session hero must not claim it is ready to migrate")
	}
}
