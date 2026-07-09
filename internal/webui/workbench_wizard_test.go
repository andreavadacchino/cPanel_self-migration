package webui

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// wizardHandler builds a ui handler with a session store rooted under dir.
func wizardHandler(t *testing.T, dir string) (http.Handler, *workbench.Store) {
	t.Helper()
	storeDir := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := workbench.NewStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(Options{Dir: dir, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	return h, store
}

func validWizardForm(csrf string) url.Values {
	return url.Values{
		"csrf":                 {csrf},
		"name":                 {"giorginisposi"},
		"primary_domain":       {"giorginisposi.it"},
		"notes":                {"prima prova"},
		"src_host":             {"192.168.1.193"},
		"src_port":             {"22"},
		"src_account":          {"giorginisposi"},
		"dst_host":             {"192.168.1.78"},
		"dst_port":             {"2222"},
		"dst_account":          {"giorginisposi"},
		"content_files":        {"1"},
		"content_databases":    {"1"},
		"content_email":        {"1"},
		"content_email_config": {"1"},
		"content_cron":         {"1"},
		// content_dns intentionally omitted — DNS is opt-in and separate.
	}
}

// TestWorkbenchListLinksToWizard: the sessions list must offer a discoverable
// entry point to the guided wizard.
func TestWorkbenchListLinksToWizard(t *testing.T) {
	dir := t.TempDir()
	h, _ := wizardHandler(t, dir)
	rr := doReq(h, http.MethodGet, "/workbench", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /workbench = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `href="/workbench/new"`) {
		t.Errorf("sessions list must link to /workbench/new")
	}
}

func TestWizardFormRenders(t *testing.T) {
	dir := t.TempDir()
	h, _ := wizardHandler(t, dir)
	rr := doReq(h, http.MethodGet, "/workbench/new", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /workbench/new = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	// Operator language, not engineering jargon.
	for _, want := range []string{"Nuova migrazione", "server", "Cosa vuoi migrare", "DNS", `name="csrf"`, `action="/workbench/new"`} {
		if !strings.Contains(body, want) {
			t.Errorf("wizard form missing %q", want)
		}
	}
	// The primary UI must not leak engineering vocabulary.
	for _, bad := range []string{"artifact", "policy", "acceptance", "SourceProfile"} {
		if strings.Contains(body, bad) {
			t.Errorf("wizard form leaks engineering term %q", bad)
		}
	}
}

func TestWizardCreatesSessionWithSetup(t *testing.T) {
	dir := t.TempDir()
	h, store := wizardHandler(t, dir)
	csrf := fetchCSRF(t, h)

	rr := doReq(h, http.MethodPost, "/workbench/new", validWizardForm(csrf))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("POST /workbench/new = %d, want 303; body: %s", rr.Code, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "/workbench/session/mig_") {
		t.Fatalf("redirect = %q, want /workbench/session/mig_...", loc)
	}

	sessions, _, _ := store.List()
	if len(sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(sessions))
	}
	s := sessions[0]
	if s.Name != "giorginisposi" {
		t.Errorf("name = %q", s.Name)
	}
	if s.Setup == nil {
		t.Fatal("Setup is nil")
	}
	if s.Setup.PrimaryDomain != "giorginisposi.it" || s.Setup.Notes != "prima prova" {
		t.Errorf("domain/notes wrong: %+v", s.Setup)
	}
	if s.Setup.Source.Host != "192.168.1.193" || s.Setup.Source.Port != 22 || s.Setup.Source.Account != "giorginisposi" {
		t.Errorf("source wrong: %+v", s.Setup.Source)
	}
	if s.Setup.Destination.Host != "192.168.1.78" || s.Setup.Destination.Port != 2222 {
		t.Errorf("dest wrong: %+v", s.Setup.Destination)
	}
	c := s.Setup.Content
	if !(c.Files && c.Databases && c.Email && c.EmailConfig && c.Cron) {
		t.Errorf("content flags wrong: %+v", c)
	}
	if c.DNS {
		t.Errorf("DNS must be false when not selected (not implied by other content): %+v", c)
	}
}

// TestWizardDNSIsSeparateOptIn: selecting ONLY dns yields DNS=true and every
// bulk content flag false — DNS is never implied by, nor implies, other areas.
func TestWizardDNSIsSeparateOptIn(t *testing.T) {
	dir := t.TempDir()
	h, store := wizardHandler(t, dir)
	csrf := fetchCSRF(t, h)

	form := url.Values{
		"csrf":        {csrf},
		"name":        {"onlydns"},
		"src_host":    {"1.1.1.1"},
		"src_account": {"acct"},
		"dst_host":    {"2.2.2.2"},
		"dst_account": {"acct"},
		"content_dns": {"1"},
	}
	rr := doReq(h, http.MethodPost, "/workbench/new", form)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("POST = %d, want 303; body: %s", rr.Code, rr.Body.String())
	}
	sessions, _, _ := store.List()
	s := sessions[0]
	c := s.Setup.Content
	if !c.DNS {
		t.Error("DNS should be true")
	}
	if c.Files || c.Databases || c.Email || c.EmailConfig || c.Cron {
		t.Errorf("no bulk content should be set: %+v", c)
	}
}

func TestWizardMissingFieldsReadableError(t *testing.T) {
	dir := t.TempDir()
	h, store := wizardHandler(t, dir)
	csrf := fetchCSRF(t, h)

	cases := []struct {
		name string
		drop string // form key to blank out
	}{
		{"missing name", "name"},
		{"missing src_host", "src_host"},
		{"missing src_account", "src_account"},
		{"missing dst_host", "dst_host"},
		{"missing dst_account", "dst_account"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := validWizardForm(csrf)
			form.Set(tc.drop, "")
			rr := doReq(h, http.MethodPost, "/workbench/new", form)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("%s: got %d, want 400", tc.name, rr.Code)
			}
			// A human-readable Italian error, not a stack trace.
			if !strings.Contains(rr.Body.String(), "obbligator") && !strings.Contains(rr.Body.String(), "manca") {
				t.Errorf("%s: error not readable: %q", tc.name, rr.Body.String())
			}
		})
	}
	if s, _, _ := store.List(); len(s) != 0 {
		t.Errorf("no session should be created on invalid submits, got %d", len(s))
	}
}

// TestWizardNoContentSelectedRejected: a migration with nothing to move is a
// mistake, not an empty valid session.
func TestWizardNoContentSelectedRejected(t *testing.T) {
	dir := t.TempDir()
	h, _ := wizardHandler(t, dir)
	csrf := fetchCSRF(t, h)
	form := url.Values{
		"csrf":        {csrf},
		"name":        {"empty"},
		"src_host":    {"1.1.1.1"},
		"src_account": {"a"},
		"dst_host":    {"2.2.2.2"},
		"dst_account": {"a"},
	}
	rr := doReq(h, http.MethodPost, "/workbench/new", form)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("no content: got %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "contenuto") {
		t.Errorf("expected content-selection error, got: %q", rr.Body.String())
	}
}

func TestWizardBadPortRejected(t *testing.T) {
	dir := t.TempDir()
	h, _ := wizardHandler(t, dir)
	csrf := fetchCSRF(t, h)
	form := validWizardForm(csrf)
	form.Set("src_port", "99999")
	rr := doReq(h, http.MethodPost, "/workbench/new", form)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad port: got %d, want 400", rr.Code)
	}
}

func TestWizardRequiresCSRF(t *testing.T) {
	dir := t.TempDir()
	h, _ := wizardHandler(t, dir)
	form := validWizardForm("") // wrong/empty token
	rr := doReq(h, http.MethodPost, "/workbench/new", form)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("no csrf: got %d, want 403", rr.Code)
	}
}

// TestWizardNoSecretLeaksToDisk: even if a rogue password field is smuggled
// into the form, it must never appear in the persisted session.json — the
// wizard collects no secrets by construction.
func TestWizardNoSecretLeaksToDisk(t *testing.T) {
	dir := t.TempDir()
	h, store := wizardHandler(t, dir)
	csrf := fetchCSRF(t, h)
	const canary = "SUPERSECRETcanary123"
	form := validWizardForm(csrf)
	form.Set("src_pass", canary)
	form.Set("password", canary)
	form.Set("ssh_pass", canary)
	rr := doReq(h, http.MethodPost, "/workbench/new", form)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("POST = %d, want 303", rr.Code)
	}
	sessions, _, _ := store.List()
	if len(sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(sessions))
	}
	// Scan every file under the session dir for the canary.
	sessDir := filepath.Join(dir, "migrations", sessions[0].ID)
	err := filepath.Walk(sessDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), canary) {
			t.Errorf("secret canary leaked into %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestWizardRedirectLandsOnPreflightNextAction: after creation the operator is
// taken to the session and the recommended next action is the preflight.
func TestWizardRedirectLandsOnPreflightNextAction(t *testing.T) {
	dir := t.TempDir()
	h, _ := wizardHandler(t, dir)
	csrf := fetchCSRF(t, h)
	rr := doReq(h, http.MethodPost, "/workbench/new", validWizardForm(csrf))
	loc := rr.Header().Get("Location")
	page := doReq(h, http.MethodGet, loc, nil)
	if page.Code != http.StatusOK {
		t.Fatalf("session page = %d, want 200", page.Code)
	}
	body := page.Body.String()
	if !strings.Contains(strings.ToLower(body), "preflight") {
		t.Errorf("Panoramica should point to preflight as next action")
	}
	// host.yaml is absent → the operator sees the NEUTRAL connection callout,
	// never the host.yaml file jargon (that lives only in expert mode).
	if strings.Contains(body, "host.yaml") {
		t.Errorf("operator landing must not leak host.yaml jargon")
	}
	if !strings.Contains(body, "Connessioni non configurate") {
		t.Errorf("expected the neutral 'Connessioni non configurate' callout when credentials not yet configured")
	}
}

// TestPanoramicaCalloutHiddenWhenHostYAMLPresent: once credentials exist the
// technical-config callout disappears, but the migration definition block stays.
func TestPanoramicaCalloutHiddenWhenHostYAMLPresent(t *testing.T) {
	dir := t.TempDir()
	h, _ := wizardHandler(t, dir)
	csrf := fetchCSRF(t, h)
	rr := doReq(h, http.MethodPost, "/workbench/new", validWizardForm(csrf))
	loc := rr.Header().Get("Location")
	// Simulate credentials configured.
	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte("src: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	page := doReq(h, http.MethodGet, loc, nil)
	body := page.Body.String()
	if strings.Contains(body, "Configurazione tecnica richiesta") {
		t.Errorf("callout should be hidden when host.yaml exists")
	}
	// The migration definition block is a technical surface — it now lives in
	// expert mode; the operator landing stays free of it.
	if strings.Contains(body, "Definizione della migrazione") {
		t.Errorf("operator landing must not render the technical definition block")
	}
	expert := doReq(h, http.MethodGet, loc+"?mode=expert", nil).Body.String()
	if !strings.Contains(expert, "Definizione della migrazione") {
		t.Errorf("setup definition block should render in expert mode")
	}
	if !strings.Contains(expert, "giorginisposi.it") {
		t.Errorf("primary domain should appear in the expert definition block")
	}
}

// TestLegacySessionRendersWithoutSetupBlock: a session created before the
// wizard (no Setup) renders the Panoramica with neither the callout nor the
// definition block — backward compatible.
func TestLegacySessionRendersWithoutSetupBlock(t *testing.T) {
	dir := t.TempDir()
	h, store := wizardHandler(t, dir)
	sess, err := store.Create("legacy", "src", "dst", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	page := doReq(h, http.MethodGet, "/workbench/session/"+sess.ID, nil)
	if page.Code != http.StatusOK {
		t.Fatalf("legacy session page = %d, want 200", page.Code)
	}
	body := page.Body.String()
	if strings.Contains(body, "Definizione della migrazione") {
		t.Errorf("legacy session must not render a setup block")
	}
	if strings.Contains(body, "Configurazione tecnica richiesta") {
		t.Errorf("legacy session must not render the host.yaml callout")
	}
}

// TestWizardGETonlyAndPOSTonly: /workbench/new answers GET (form) and POST
// (create); other methods are rejected.
func TestWizardMethodGuards(t *testing.T) {
	dir := t.TempDir()
	h, _ := wizardHandler(t, dir)
	rr := doReq(h, http.MethodDelete, "/workbench/new", nil)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE /workbench/new = %d, want 405", rr.Code)
	}
}
