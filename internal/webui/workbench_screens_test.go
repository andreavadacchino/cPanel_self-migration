package webui

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// advanceTo steps a session through the legal forward chain to target.
func advanceTo(t *testing.T, store *workbench.Store, id string, target workbench.Status) {
	t.Helper()
	chain := []workbench.Status{
		workbench.StatusPreflightRequired,
		workbench.StatusInventoryReady,
		workbench.StatusChecklistReady,
		workbench.StatusReadyForApply,
		workbench.StatusApplyInProgress,
		workbench.StatusApplyDone,
		workbench.StatusVerificationRequired,
		workbench.StatusReadyForCutover,
	}
	for _, s := range chain {
		if _, err := store.SetStatus(id, s, false, "", time.Now()); err != nil {
			t.Fatalf("SetStatus %s: %v", s, err)
		}
		if s == target {
			return
		}
	}
}

func getBody(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rr := doWorkbenchReq(h, http.MethodGet, path, nil)
	return rr.Code, rr.Body.String()
}

// TestScreenRoutesAllRender: every guided-path route returns 200 with the nav.
func TestScreenRoutesAllRender(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	segments := []string{"", "preflight", "inventario", "migrazione", "conferme", "applica", "chiusura"}
	for _, seg := range segments {
		path := "/workbench/session/" + sess.ID
		if seg != "" {
			path += "/" + seg
		}
		code, body := getBody(t, h, path)
		if code != 200 {
			t.Errorf("GET %s = %d, want 200", path, code)
		}
		if !strings.Contains(body, "Panoramica") || !strings.Contains(body, "Chiusura") {
			t.Errorf("GET %s: missing guided nav", path)
		}
	}
}

// TestPanoramicaIsCockpit: the base route renders the Fase 4 cockpit — hero
// state, the journey stepper, the comparison and plan blocks. "Bozza" confirms
// the persistent status badge still shows. The engineering surfaces ("Stato per
// fase") are now operator-hidden and reachable only in expert mode.
func TestPanoramicaIsCockpit(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	_, body := getBody(t, h, "/workbench/session/"+sess.ID)
	for _, want := range []string{"Stato migrazione", "Comparativa account", "Piano di migrazione", "Bozza"} {
		if !strings.Contains(body, want) {
			t.Errorf("cockpit panoramica missing %q", want)
		}
	}
	// The technical "Stato per fase" widget moved behind expert mode.
	if strings.Contains(body, "Stato per fase") {
		t.Error("operator Panoramica must not show the technical 'Stato per fase' widget")
	}
	_, expert := getBody(t, h, "/workbench/session/"+sess.ID+"?mode=expert")
	if !strings.Contains(expert, "Stato per fase") {
		t.Error("expert Panoramica must still expose 'Stato per fase'")
	}
}

// TestPostToScreenRouteIs404: the GET sub-views must not accept POST.
func TestPostToScreenRouteIs404(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sess.ID+"/preflight", url.Values{})
	if rr.Code != http.StatusNotFound {
		t.Errorf("POST /preflight = %d, want 404 (GET-only sub-view)", rr.Code)
	}
}

// TestMigrazioneCoverageGlyphs: coverage screen shows ✅/🟡/⚪ by class.
func TestMigrazioneCoverageGlyphs(t *testing.T) {
	h, store, dir := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	cl := accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		CoverageManifest: []accountinventory.CoverageArea{
			{Area: "dns", State: accountinventory.CoverageCovered},
			{Area: "email_filters", State: accountinventory.CoverageCovered},
			{Area: "quota_package", State: accountinventory.CoverageRootOnly, Note: "WHM territory"},
		},
		ManualActions: []accountinventory.ManualAction{
			{Section: "email_filters", Acceptable: true, Accepted: false},
		},
	}
	writeChecklist(t, dir, cl)
	_, body := getBody(t, h, "/workbench/session/"+sess.ID+"/migrazione")
	if !strings.Contains(body, "✅") || !strings.Contains(body, "🟡") || !strings.Contains(body, "⚪") {
		t.Errorf("migrazione must show all three glyphs; body:\n%s", body)
	}
	if !strings.Contains(body, "competenza WHM") {
		t.Error("root_only note must be shown in Italian")
	}
	if strings.Contains(body, "WHM territory") {
		t.Error("English coverage note leaked into the Italian table")
	}
}

// TestConfermeAcceptForm: pending acceptable action shows the accept form with
// its stable key; an accepted action shows the attribution.
func TestConfermeAcceptForm(t *testing.T) {
	h, store, dir := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	cl := accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		ManualActions: []accountinventory.ManualAction{
			{Key: "AK-pending", Section: "email_routing", Title: "Conferma routing", OperatorAction: "verifica MX",
				Acceptable: true, Accepted: false, BlockingCutover: true},
			{Key: "AK-done", Section: "dns", Title: "Già accettata", OperatorAction: "fatto",
				Acceptable: true, Accepted: true, AcceptedBy: "andrea", AcceptedAt: "2026-07-05"},
		},
	}
	writeChecklist(t, dir, cl)
	_, body := getBody(t, h, "/workbench/session/"+sess.ID+"/conferme")
	if !strings.Contains(body, `name="action_key" value="AK-pending"`) {
		t.Error("pending action must expose an accept form with its key")
	}
	if !strings.Contains(body, "Conferma fatto") {
		t.Error("accept button label missing")
	}
	if !strings.Contains(body, "andrea") {
		t.Error("accepted action must show attribution")
	}
}

// TestApplicaDangerZone: the DNS apply button is disabled until the out-of-band
// attestation checkbox is checked; strong confirmation still required.
func TestApplicaDangerZone(t *testing.T) {
	h, sessID, _, _ := newExecTestEnv(t)
	_, body := getBody(t, h, "/workbench/session/"+sessID+"/applica")
	if !strings.Contains(body, "Danger Zone") {
		t.Error("DNS block must be a visually separated danger zone")
	}
	if !strings.Contains(body, `id="dns-apply-btn"`) || !strings.Contains(body, "disabled") {
		t.Error("DNS apply button must start disabled")
	}
	if !strings.Contains(body, "dns-standalone-attest") {
		t.Error("out-of-band attestation checkbox must be present")
	}
	if !strings.Contains(body, "migrate_content") {
		t.Error("content migration block must be present")
	}
	// strong confirmation input for DNS apply present.
	if !strings.Contains(body, `name="confirm_account"`) {
		t.Error("strong confirmation input must remain")
	}
}

// TestChiusuraYes: READY_TO_CUTOVER checklist → SÌ, runbook decisions shown.
func TestChiusuraYes(t *testing.T) {
	h, store, dir := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	writeChecklist(t, dir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallReadyToCutover,
	})
	_, body := getBody(t, h, "/workbench/session/"+sess.ID+"/chiusura")
	if !strings.Contains(body, "SÌ") {
		t.Errorf("READY_TO_CUTOVER should yield SÌ; body:\n%s", body)
	}
	// all 5 runbook decisions present regardless of verdict.
	for _, d := range runbookDecisions {
		if !strings.Contains(body, d) {
			t.Errorf("runbook decision missing: %q", d)
		}
	}
}

// TestChiusuraNo: a cutover blocker + pending confirmation → NO with the list.
func TestChiusuraNo(t *testing.T) {
	h, store, dir := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	writeChecklist(t, dir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallReadyWithManualNotes,
		Sections: []accountinventory.ChecklistSection{
			{Section: "dns", BlockersCutover: []string{"POL-DNS-NS-CHANGED"}},
		},
		ManualActions: []accountinventory.ManualAction{
			{Key: "AK-x", Section: "mailboxes", Title: "Mailbox rimossa", OperatorAction: "ricrea",
				BlockingCutover: true, Accepted: false},
		},
	})
	_, body := getBody(t, h, "/workbench/session/"+sess.ID+"/chiusura")
	if !strings.Contains(body, "NO") {
		t.Error("cutover blocker present → NO")
	}
	if !strings.Contains(body, "POL-DNS-NS-CHANGED") {
		t.Error("cutover blocker must be listed")
	}
	if !strings.Contains(body, "Mailbox rimossa") {
		t.Error("pending confirmation must be listed")
	}
}

// TestWorkbenchAcceptRedirectsToConferme: the workbench accept POST returns to
// the Conferme screen; the dashboard /accept still returns to "/".
func TestWorkbenchAcceptRedirectsToConferme(t *testing.T) {
	dir := t.TempDir()
	store, err := workbench.NewStore(filepath.Join(dir, "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	// Fake runner so the synchronous checklist regeneration in saveAcceptTo
	// does not spawn the real binary (the dashboard accept test does the same).
	rec := &recordingRunner{}
	h, err := New(Options{Dir: dir, SessionStore: store, Runner: rec.run})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	writeChecklistWithActions(t, dir) // AK-accept01 is acceptable
	csrf := extractCSRF(t, h, sess.ID)

	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sess.ID+"/accept",
		acceptForm(csrf, "AK-accept01", "verificato col cliente", "andrea"))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("workbench accept = %d (%s), want 303", rr.Code, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	if loc != "/workbench/session/"+sess.ID+"/conferme" {
		t.Errorf("redirect = %q, want the Conferme screen", loc)
	}
}

// TestReadyForCutoverSessionRenders is the review-flagged regression: a session
// at ready_for_cutover with a real checklist must render Panoramica and Chiusura
// without a 500.
func TestReadyForCutoverSessionRenders(t *testing.T) {
	h, store, dir := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	advanceTo(t, store, sess.ID, workbench.StatusReadyForCutover)
	writeChecklist(t, dir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallReadyToCutover,
	})
	for _, seg := range []string{"", "/chiusura", "/applica"} {
		code, body := getBody(t, h, "/workbench/session/"+sess.ID+seg)
		if code != 200 {
			t.Errorf("ready_for_cutover GET %q = %d, want 200", seg, code)
		}
		if strings.Contains(body, "template error") {
			t.Errorf("ready_for_cutover GET %q: template error", seg)
		}
	}
}

// TestListShowsItalianBadge locks the shared statusBadge define: the sessions
// list must render the Italian status label (not the raw enum), proving the
// merged ParseFS set resolves the single (list Status Label) define.
func TestListShowsItalianBadge(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	store.Create("locktest", "src", "dst", time.Now())
	code, body := getBody(t, h, "/workbench")
	if code != 200 {
		t.Fatalf("list = %d", code)
	}
	if !strings.Contains(body, "Bozza") {
		t.Error("list must render the Italian status label 'Bozza' for a draft session")
	}
	if strings.Contains(body, ">draft<") {
		t.Error("raw status enum leaked into the sessions list badge")
	}
}

func writeChecklist(t *testing.T, dir string, cl accountinventory.MigrationChecklist) {
	t.Helper()
	b, err := json.Marshal(cl)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "migration_checklist.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}
