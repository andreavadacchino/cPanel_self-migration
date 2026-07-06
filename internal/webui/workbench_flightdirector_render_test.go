package webui

// HTTP-level render tests for the Flight Director shell: the persistent header,
// the timeline rail, the risk/DNS badges and the job status must be present and
// honest across wizard and legacy sessions, without breaking the existing
// forms/ids the exec path depends on.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// wizardSession creates a wizard-backed session (Setup != nil) in the served dir.
func wizardSession(t *testing.T, store *workbench.Store, name string, setup *workbench.SetupMeta) *workbench.Session {
	t.Helper()
	sess, err := store.CreateWithSetup(name, setup.Source.Account+"@src", setup.Destination.Account+"@dst", setup, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

// Test 1+2+11: the header shows source→destination, accounts, the primary
// domain, and the scope-aware next action.
func TestFDHeaderWizardIdentity(t *testing.T) {
	dir := t.TempDir()
	h, store := wizardHandler(t, dir)
	setup := &workbench.SetupMeta{
		PrimaryDomain: "giorginisposi.it",
		Source:        workbench.Endpoint{Host: "192.168.1.193", Port: 22, Account: "giorginisposi"},
		Destination:   workbench.Endpoint{Host: "192.168.1.78", Port: 2222, Account: "giorginisposi"},
		Content:       workbench.ContentSelection{Files: true, Databases: true},
	}
	sess := wizardSession(t, store, "giorginisposi", setup)
	_, body := getBody(t, h, "/workbench/session/"+sess.ID)

	for _, want := range []string{"giorginisposi.it", "192.168.1.193", "192.168.1.78", "fdhead"} {
		if !strings.Contains(body, want) {
			t.Errorf("FD header missing %q", want)
		}
	}
	// Scope-aware next action surfaced in the persistent header.
	if !strings.Contains(body, "Prossima azione") {
		t.Error("FD header must show the next recommended action")
	}
}

// Test 3: a legacy session (Setup==nil) renders without panic and falls back to
// the source/destination profile strings.
func TestFDHeaderLegacyFallback(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	sess, _ := store.Create("legacy-mig", "profilo-sorgente", "profilo-destinazione", time.Now())
	code, body := getBody(t, h, "/workbench/session/"+sess.ID)
	if code != http.StatusOK {
		t.Fatalf("legacy session = %d, want 200", code)
	}
	if !strings.Contains(body, "profilo-sorgente") || !strings.Contains(body, "profilo-destinazione") {
		t.Error("legacy header must fall back to source/destination profiles")
	}
	// No wizard-only DNS tag for a legacy session (nothing invented).
	if strings.Contains(body, "DNS non incluso") || strings.Contains(body, "DNS incluso — area delicata") {
		t.Error("legacy session must not render a wizard DNS scope tag")
	}
}

// Test 4+5: the timeline lists all seven phases and highlights the current one.
func TestFDTimelinePhasesAndCurrent(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	_, body := getBody(t, h, "/workbench/session/"+sess.ID+"/applica")
	for _, label := range []string{"Panoramica", "Preflight", "Fotografia account", "Cosa verrà migrato", "Conferme operatore", "Applica e verifica", "Chiusura"} {
		if !strings.Contains(body, label) {
			t.Errorf("timeline missing phase %q", label)
		}
	}
	if !strings.Contains(body, "fd-current") || !strings.Contains(body, `aria-current="step"`) {
		t.Error("current phase must be highlighted in the timeline")
	}
	if !strings.Contains(body, "fd-steps") {
		t.Error("timeline rail must render")
	}
}

// Test 6: a genuinely running job (slot busy) stays visible in the shell and the
// page keeps its meta-refresh.
func TestFDJobRunningVisible(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	writeJobJournal(dir, jobJournal{
		SessionID: sess.ID, Action: "migrate_content",
		StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		State: jobStateRunning, Phase: "migrate_content",
	})
	ws, err := newWorkbenchServer(store, dir, "csrf-x")
	if err != nil {
		t.Fatal(err)
	}
	ws.jobBusy = func() bool { return true } // slot held → journal stays running

	rr := httptest.NewRecorder()
	ws.handleScreen(rr, httptest.NewRequest(http.MethodGet, "/", nil), sess.ID, "")
	body := rr.Body.String()
	if !strings.Contains(body, "migrate_content") || !strings.Contains(body, "in corso") {
		t.Error("running job must stay visible in the shell header")
	}
	if !strings.Contains(body, `http-equiv="refresh"`) {
		t.Error("running job must keep the meta-refresh")
	}
	if !strings.Contains(body, "Job in corso") {
		t.Error("risk badge must read 'Job in corso' while running")
	}
}

// Test 7: an interrupted job stays visible and is marked as attention (never
// buried in a technical drawer).
func TestFDJobInterruptedAttention(t *testing.T) {
	dir := t.TempDir()
	h, store := wizardHandler(t, dir)
	setup := &workbench.SetupMeta{
		Source:      workbench.Endpoint{Host: "1.1.1.1", Port: 22, Account: "a"},
		Destination: workbench.Endpoint{Host: "2.2.2.2", Port: 22, Account: "a"},
		Content:     workbench.ContentSelection{Files: true},
	}
	sess := wizardSession(t, store, "acct", setup)
	writeJobJournal(dir, jobJournal{
		SessionID: sess.ID, Action: "migrate_content",
		StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		State: jobStateInterrupted, Phase: "migrate_content",
	})
	_, body := getBody(t, h, "/workbench/session/"+sess.ID)
	if !strings.Contains(body, "interrotta") {
		t.Error("interrupted job must stay visible")
	}
	if !strings.Contains(body, "Job interrotto") {
		t.Error("risk badge must flag the interrupted job as attention")
	}
	// Interrupted is terminal → no meta-refresh.
	if strings.Contains(body, `http-equiv="refresh"`) {
		t.Error("interrupted job must NOT trigger the meta-refresh")
	}
}

// Test 8: a wizard session without host.yaml shows the config-required risk badge.
func TestFDConfigRequiredBadge(t *testing.T) {
	dir := t.TempDir()
	h, store := wizardHandler(t, dir)
	setup := &workbench.SetupMeta{
		Source:      workbench.Endpoint{Host: "1.1.1.1", Port: 22, Account: "a"},
		Destination: workbench.Endpoint{Host: "2.2.2.2", Port: 22, Account: "a"},
		Content:     workbench.ContentSelection{Files: true},
	}
	sess := wizardSession(t, store, "acct", setup)
	_, body := getBody(t, h, "/workbench/session/"+sess.ID)
	if !strings.Contains(body, "Configurazione richiesta") {
		t.Error("wizard session without host.yaml must show 'Configurazione richiesta' risk badge")
	}
}

// Test 9: DNS included shows a prudent delicate-area tag.
func TestFDDNSIncludedPrudent(t *testing.T) {
	dir := t.TempDir()
	h, store := wizardHandler(t, dir)
	setup := &workbench.SetupMeta{
		Source:      workbench.Endpoint{Host: "1.1.1.1", Port: 22, Account: "a"},
		Destination: workbench.Endpoint{Host: "2.2.2.2", Port: 22, Account: "a"},
		Content:     workbench.ContentSelection{Files: true, DNS: true},
	}
	sess := wizardSession(t, store, "acct", setup)
	_, body := getBody(t, h, "/workbench/session/"+sess.ID)
	if !strings.Contains(body, "DNS incluso — area delicata") {
		t.Error("DNS included must render the prudent delicate-area tag")
	}
}

// Test 10: DNS excluded shows a neutral 'non incluso' tag — never a false
// operational warning.
func TestFDDNSExcludedNoFalseWarning(t *testing.T) {
	dir := t.TempDir()
	h, store := wizardHandler(t, dir)
	setup := &workbench.SetupMeta{
		Source:      workbench.Endpoint{Host: "1.1.1.1", Port: 22, Account: "a"},
		Destination: workbench.Endpoint{Host: "2.2.2.2", Port: 22, Account: "a"},
		Content:     workbench.ContentSelection{Files: true, Databases: true},
	}
	sess := wizardSession(t, store, "acct", setup)
	_, body := getBody(t, h, "/workbench/session/"+sess.ID)
	if !strings.Contains(body, "DNS non incluso") {
		t.Error("DNS excluded must render a neutral 'DNS non incluso' tag")
	}
	if strings.Contains(body, "DNS incluso — area delicata") {
		t.Error("DNS excluded must not show the delicate-area warning")
	}
}

// Test 12: the shell must not break the critical exec forms — the Applica screen
// keeps its migrate_content action and the strong per-account confirmation.
func TestFDApplicaFormsIntact(t *testing.T) {
	h, sessID, _, _ := newExecTestEnv(t)
	_, body := getBody(t, h, "/workbench/session/"+sessID+"/applica")
	for _, want := range []string{
		`action="/workbench/session/` + sessID + `/exec"`,
		`name="action" value="migrate_content"`,
		`name="confirm_account"`,
		`name="csrf"`,
		`id="dns-apply-btn"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Applica form lost required element %q", want)
		}
	}
}

func mustStore(t *testing.T, dir string) *workbench.Store {
	t.Helper()
	store, err := workbench.NewStore(dir + "/migrations")
	if err != nil {
		t.Fatal(err)
	}
	return store
}
