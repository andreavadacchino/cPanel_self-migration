package webui

// Fase 2 — Scope Confirmation after Preflight. Tests for the preset mapping,
// the edit gate, the CTA copy and the confirm handler. Pure/table where
// possible; the handler tests use the in-memory workbench harness (no server,
// no network, no credentials).

import (
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

func form(kv map[string]string) url.Values {
	v := url.Values{}
	for k, val := range kv {
		v.Set(k, val)
	}
	return v
}

// Case 1-3 — presets map to the expected automatic areas; DNS is never part of
// a preset's automatic set.
func TestScopePresetMapping(t *testing.T) {
	cases := []struct {
		preset string
		want   workbench.ContentSelection
	}{
		{"all_safe", workbench.ContentSelection{Files: true, Databases: true, Email: true, EmailConfig: true, Cron: true}},
		{"site", workbench.ContentSelection{Files: true, Databases: true}},
		{"email", workbench.ContentSelection{Email: true, EmailConfig: true}},
		{"files", workbench.ContentSelection{Files: true}},
		{"databases", workbench.ContentSelection{Databases: true}},
	}
	for _, c := range cases {
		got, ok := presetContent(c.preset, url.Values{})
		if !ok {
			t.Errorf("preset %q not recognized", c.preset)
			continue
		}
		if got != c.want {
			t.Errorf("preset %q = %+v, want %+v", c.preset, got, c.want)
		}
		if got.DNS {
			t.Errorf("preset %q must not enable DNS automatically", c.preset)
		}
	}
}

// Case 4 — custom with DNS + files: DNS is honored as a (manual) inclusion.
func TestScopePresetCustomWithDNS(t *testing.T) {
	got, ok := presetContent("custom", form(map[string]string{"files": "1", "dns": "1"}))
	if !ok {
		t.Fatal("custom preset must be recognized")
	}
	if !got.Files || !got.DNS {
		t.Errorf("custom files+dns = %+v, want Files and DNS true", got)
	}
	if got.Databases || got.Email || got.Cron {
		t.Errorf("custom must not enable unchecked areas, got %+v", got)
	}
}

// A preset may still add DNS as a manual task via the independent checkbox.
func TestScopePresetSiteWithDNSManual(t *testing.T) {
	got, _ := presetContent("site", form(map[string]string{"dns": "1"}))
	if !got.Files || !got.Databases || !got.DNS {
		t.Errorf("site+dns = %+v, want Files, Databases and DNS true", got)
	}
}

// Invalid presets are rejected.
func TestScopePresetInvalid(t *testing.T) {
	if _, ok := presetContent("../etc", url.Values{}); ok {
		t.Error("arbitrary preset must be rejected")
	}
}

// Case 5 — DNS-only is not an automatic migration.
func TestHasAutomaticArea(t *testing.T) {
	if hasAutomaticArea(workbench.ContentSelection{DNS: true}) {
		t.Error("DNS-only must not count as an automatic area")
	}
	if !hasAutomaticArea(workbench.ContentSelection{Files: true}) {
		t.Error("files must count as an automatic area")
	}
	for _, c := range []workbench.ContentSelection{
		{Databases: true}, {Email: true}, {EmailConfig: true}, {Cron: true},
	} {
		if !hasAutomaticArea(c) {
			t.Errorf("%+v should count as automatic", c)
		}
	}
}

// Case 7 — the edit gate closes after an apply report exists or a job is live.
func TestCanEditScope(t *testing.T) {
	if !canEditScope(artifactFacts{}, false) {
		t.Error("no apply + no job → scope editable")
	}
	if canEditScope(artifactFacts{}, true) {
		t.Error("job live → scope not editable")
	}
	if canEditScope(artifactFacts{ContentApplyPresent: true}, false) {
		t.Error("content apply report present → scope not editable")
	}
	if canEditScope(artifactFacts{Email: areaFacts{ApplyPresent: true}}, false) {
		t.Error("an area apply report present → scope not editable")
	}
}

// Case 10 — the CTA copy reflects the state: blocked > not-confirmed > ready.
func TestMigrationCTALabel(t *testing.T) {
	if got := migrationCTALabel(migrationPlan{Ready: false}); !strings.Contains(strings.ToLower(got), "preflight") {
		t.Errorf("not-ready CTA should mention preflight, got %q", got)
	}
	if got := migrationCTALabel(migrationPlan{Ready: true, Blocked: true}); !strings.Contains(strings.ToLower(got), "blocc") {
		t.Errorf("blocked CTA should mention blocco, got %q", got)
	}
	if got := migrationCTALabel(migrationPlan{Ready: true, ScopeConfirmed: false}); !strings.Contains(strings.ToLower(got), "conferma") {
		t.Errorf("unconfirmed CTA should ask to confirm, got %q", got)
	}
	if got := migrationCTALabel(migrationPlan{Ready: true, ScopeConfirmed: true, CanStartMigration: true}); !strings.Contains(got, "Fase 3") {
		t.Errorf("confirmed+ready CTA should defer to Fase 3, got %q", got)
	}
}

// Case 9 + handler — a confirm POST updates the session scope and redirects.
func TestConfirmScopeHandlerUpdates(t *testing.T) {
	h, store, dir := newTestWorkbenchHandler(t)
	writeChecklist(t, dir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallReadyToCutover,
	})
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	csrf := extractCSRF(t, h, sess.ID)

	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sess.ID+"/scope",
		form(map[string]string{"csrf": csrf, "preset": "site"}))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("confirm scope = %d (%s), want 303", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "scope=updated") {
		t.Errorf("redirect = %q, want ?scope=updated", loc)
	}
	got, _ := store.Get(sess.ID)
	if got.Setup == nil || got.Setup.ScopeConfirmedAt == nil {
		t.Fatal("scope confirmation must be persisted (Setup + ScopeConfirmedAt)")
	}
	if !got.Setup.Content.Files || !got.Setup.Content.Databases || got.Setup.Content.Email || got.Setup.Content.DNS {
		t.Errorf("site preset persisted wrong content: %+v", got.Setup.Content)
	}
}

// DNS-only cannot be confirmed as a migration (redirect back with need_area).
func TestConfirmScopeHandlerRejectsDNSOnly(t *testing.T) {
	h, store, dir := newTestWorkbenchHandler(t)
	writeChecklist(t, dir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallReadyToCutover,
	})
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	csrf := extractCSRF(t, h, sess.ID)

	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sess.ID+"/scope",
		form(map[string]string{"csrf": csrf, "preset": "custom", "dns": "1"}))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("dns-only confirm = %d, want 303 redirect", rr.Code)
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "scope=need_area") {
		t.Errorf("dns-only redirect = %q, want ?scope=need_area", loc)
	}
	got, _ := store.Get(sess.ID)
	if got.Setup != nil && got.Setup.ScopeConfirmedAt != nil {
		t.Error("dns-only must NOT mark the scope confirmed")
	}
}

// Case 7 — scope cannot be edited once an apply report exists.
func TestConfirmScopeHandlerLockedAfterApply(t *testing.T) {
	h, store, dir := newTestWorkbenchHandler(t)
	writeChecklist(t, dir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallReadyToCutover,
	})
	// A content apply report freezes the scope.
	if err := os.WriteFile(filepath.Join(dir, "report.json"), []byte(`{"run_id":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	csrf := extractCSRF(t, h, sess.ID)

	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sess.ID+"/scope",
		form(map[string]string{"csrf": csrf, "preset": "site"}))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("locked scope confirm = %d, want 403", rr.Code)
	}
}

// A confirm POST without a valid CSRF token is rejected and does not mutate.
func TestConfirmScopeHandlerRejectsBadCSRF(t *testing.T) {
	h, store, dir := newTestWorkbenchHandler(t)
	writeChecklist(t, dir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallReadyToCutover,
	})
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())

	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sess.ID+"/scope",
		form(map[string]string{"csrf": "wrong-token", "preset": "site"}))
	if rr.Code == http.StatusSeeOther {
		t.Fatalf("bad CSRF must not succeed, got %d", rr.Code)
	}
	got, _ := store.Get(sess.ID)
	if got.Setup != nil && got.Setup.ScopeConfirmedAt != nil {
		t.Error("bad CSRF must not mutate the scope")
	}
}

// Render — the plan screen shows the scope-confirm block and the state-aware CTA.
func TestConfirmScopeScreenRenders(t *testing.T) {
	h, store, dir := newTestWorkbenchHandler(t)
	writeChecklist(t, dir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallReadyToCutover,
	})
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	code, body := getBody(t, h, "/workbench/session/"+sess.ID+"/migrazione")
	if code != 200 {
		t.Fatalf("migrazione = %d, want 200", code)
	}
	if !strings.Contains(body, "Conferma cosa vuoi migrare") {
		t.Error("plan screen must show the scope-confirm block")
	}
	// Unconfirmed legacy session → CTA asks to confirm the scope.
	if !strings.Contains(body, "Conferma lo scope prima di avviare") {
		t.Error("unconfirmed CTA text missing")
	}
	// The one-click start action must still NOT be wired.
	if strings.Contains(body, `value="start_migration"`) {
		t.Error("Fase 2 must not wire a one-click start action")
	}
}
