package webui

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

func scopeFor(c workbench.ContentSelection) contentScope {
	return deriveContentScope(&workbench.Session{Setup: &workbench.SetupMeta{Content: c}})
}

func legacyScope() contentScope {
	return deriveContentScope(&workbench.Session{})
}

// lower joins text+detail lowercased for substring checks.
func naLower(a recommendedAction) string {
	return strings.ToLower(a.Text + " ‖ " + a.Detail)
}

// Case 1 — only File+Database: the apply banner must not cite email/cron/DNS.
func TestNextActionScopeFilesDBOnly(t *testing.T) {
	sc := scopeFor(workbench.ContentSelection{Files: true, Databases: true})
	got := nextAction(workbench.StatusReadyForApply, artifactFacts{}, sc)
	low := naLower(got)
	for _, bad := range []string{"email", "cron", "dns", "configurazioni email"} {
		if strings.Contains(low, bad) {
			t.Errorf("files+db only: apply banner must not cite %q, got %q", bad, got.Text+" / "+got.Detail)
		}
	}
	if !strings.Contains(low, "contenuti") {
		t.Errorf("files+db only: apply banner should mention contenuti, got %q", got.Text)
	}
}

// Case 3 — Cron excluded: no cron suggestion at apply / verify.
func TestNextActionScopeCronExcluded(t *testing.T) {
	sc := scopeFor(workbench.ContentSelection{Files: true}) // cron=false
	for _, st := range []workbench.Status{workbench.StatusReadyForApply, workbench.StatusApplyDone, workbench.StatusVerificationRequired} {
		got := nextAction(st, artifactFacts{}, sc)
		if strings.Contains(naLower(got), "cron") {
			t.Errorf("status %q, cron excluded: banner must not cite cron, got %q", st, got.Text+" / "+got.Detail)
		}
	}
}

// Case 4 — EmailConfig excluded (but Email content included): no email-config talk.
func TestNextActionScopeEmailConfigExcluded(t *testing.T) {
	sc := scopeFor(workbench.ContentSelection{Email: true}) // email content on, email_config off
	got := nextAction(workbench.StatusReadyForApply, artifactFacts{}, sc)
	low := naLower(got)
	for _, bad := range []string{"configurazioni email", "inoltri", "filtri", "autoresponder"} {
		if strings.Contains(low, bad) {
			t.Errorf("email_config excluded: banner must not cite %q, got %q", bad, got.Text+" / "+got.Detail)
		}
	}
}

// Case 4 — DNS excluded: no DNS anywhere in the apply/verify banners.
func TestNextActionScopeDNSExcluded(t *testing.T) {
	sc := scopeFor(workbench.ContentSelection{Files: true, Databases: true, Cron: true, EmailConfig: true})
	for _, st := range []workbench.Status{workbench.StatusReadyForApply, workbench.StatusApplyDone, workbench.StatusVerificationRequired} {
		got := nextAction(st, artifactFacts{
			// DNS verify missing on disk — must still NOT be listed because it's out of scope.
			DNS: areaFacts{},
		}, sc)
		if strings.Contains(naLower(got), "dns") {
			t.Errorf("status %q, DNS excluded: banner must not cite DNS, got %q", st, got.Text+" / "+got.Detail)
		}
	}
}

// Case 5 — DNS included: DNS may be cited, but with prudent language.
func TestNextActionScopeDNSIncludedPrudent(t *testing.T) {
	sc := scopeFor(workbench.ContentSelection{Files: true, DNS: true})
	got := nextAction(workbench.StatusReadyForApply, artifactFacts{}, sc)
	low := naLower(got)
	if !strings.Contains(low, "dns") {
		t.Errorf("DNS included: banner may cite DNS, got %q", got.Text+" / "+got.Detail)
	}
	if !strings.Contains(low, "delicata") {
		t.Errorf("DNS included: banner should use prudent language (area delicata), got %q", got.Text+" / "+got.Detail)
	}
}

// Case 6 — legacy (Setup==nil): unchanged behaviour — the historical apply text.
func TestNextActionScopeLegacyUnchanged(t *testing.T) {
	sc := legacyScope()
	got := nextAction(workbench.StatusReadyForApply, artifactFacts{}, sc)
	want := "Applica le modifiche (contenuti, email, cron, DNS)"
	if got.Text != want {
		t.Errorf("legacy apply text = %q, want %q", got.Text, want)
	}
	if got.Detail != "" {
		t.Errorf("legacy apply detail should be empty (unchanged), got %q", got.Detail)
	}
}

// draft/preflight: for a scoped session the banner detail lists the included
// areas (Case 1 spirit) and excludes the others.
func TestNextActionScopePreflightIncludesList(t *testing.T) {
	sc := scopeFor(workbench.ContentSelection{Files: true, Databases: true})
	for _, st := range []workbench.Status{workbench.StatusDraft, workbench.StatusPreflightRequired} {
		got := nextAction(st, artifactFacts{}, sc)
		if !strings.Contains(got.Detail, "File del sito") || !strings.Contains(got.Detail, "Database") {
			t.Errorf("status %q: detail should list included areas, got %q", st, got.Detail)
		}
		low := strings.ToLower(got.Detail)
		for _, bad := range []string{"cron", "dns", "configurazioni email"} {
			if strings.Contains(low, bad) {
				t.Errorf("status %q: detail must not list excluded %q, got %q", st, bad, got.Detail)
			}
		}
	}
}

// legacy draft: detail unchanged (empty — no include list).
func TestNextActionScopeLegacyDraftNoList(t *testing.T) {
	got := nextAction(workbench.StatusDraft, artifactFacts{}, legacyScope())
	if got.Detail != "" {
		t.Errorf("legacy draft detail should stay empty, got %q", got.Detail)
	}
}

// missingVerifies must skip out-of-scope areas.
func TestMissingVerifiesScopeFiltered(t *testing.T) {
	f := artifactFacts{
		DNS:   areaFacts{}, // missing verify, but DNS out of scope
		Email: areaFacts{VerifyPresent: false},
		Cron:  areaFacts{VerifyPresent: false},
	}
	sc := scopeFor(workbench.ContentSelection{EmailConfig: true}) // only email in scope
	got := missingVerifies(f, sc)
	joined := strings.ToLower(strings.Join(got, ","))
	if strings.Contains(joined, "dns") || strings.Contains(joined, "cron") {
		t.Errorf("out-of-scope DNS/Cron must not be listed, got %v", got)
	}
	if !strings.Contains(joined, "email") {
		t.Errorf("in-scope Email missing verify should be listed, got %v", got)
	}
}

// --- render: the Panoramica banner reflects the scope ------------------------

func TestPanoramicaBannerScopeAwareApply(t *testing.T) {
	dir := t.TempDir()
	h, store := wizardHandler(t, dir)
	setup := &workbench.SetupMeta{
		Source:      workbench.Endpoint{Host: "1.1.1.1", Port: 22, Account: "a"},
		Destination: workbench.Endpoint{Host: "2.2.2.2", Port: 22, Account: "a"},
		Content:     workbench.ContentSelection{Files: true, Databases: true},
	}
	sess, err := store.CreateWithSetup("acct", "a@1", "a@2", setup, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetStatus(sess.ID, workbench.StatusReadyForApply, true, "test scope-aware banner", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	rr := doReq(h, http.MethodGet, "/workbench/session/"+sess.ID, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("panoramica = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Applica le modifiche (contenuti)") {
		t.Errorf("banner should show the scoped apply text 'Applica le modifiche (contenuti)'")
	}
	if strings.Contains(body, "Applica le modifiche (contenuti, email, cron, DNS)") {
		t.Errorf("banner must not show the legacy all-areas apply text for a scoped session")
	}
}
