package webui

// Fase 1 — Platform Migration Plan / Readiness. Tests for the read-only
// migration-plan read-model: it aggregates the artifact facts + wizard scope
// into a product view that answers "what happens if I press Avvia migrazione?".
// Pure function, no I/O, no server, no credentials.

import (
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// cleanChecklist is a minimal valid checklist whose verdict does NOT block apply.
func cleanChecklist() *accountinventory.MigrationChecklist {
	return &accountinventory.MigrationChecklist{
		Mode:          "migration-checklist",
		FormatVersion: 1,
		OverallStatus: accountinventory.OverallReadyToCutover,
		ApplyBlocked:  false,
	}
}

// planArea finds an area by key; fails the test if absent.
func planArea(t *testing.T, p migrationPlan, key string) migrationPlanArea {
	t.Helper()
	for _, a := range p.Areas {
		if a.Key == key {
			return a
		}
	}
	t.Fatalf("area %q not found in plan (areas=%d)", key, len(p.Areas))
	return migrationPlanArea{}
}

// Case 1 — no preflight artifacts: the plan is not ready, cannot start, and
// carries a human (non-technical) message instead of crashing.
func TestMigrationPlanNoArtifacts(t *testing.T) {
	p := buildMigrationPlan(artifactFacts{}, legacyScope())
	if p.Ready {
		t.Error("no checklist → plan must not be Ready")
	}
	if p.CanStartMigration {
		t.Error("no checklist → CanStartMigration must be false")
	}
	if p.NotReadyMessage == "" {
		t.Error("expected a human not-ready message when artifacts are missing")
	}
}

// Case 2 — scope only email: file/database/cron are excluded, email content is
// an automatic candidate (auto-runnable).
func TestMigrationPlanEmailOnlyScope(t *testing.T) {
	f := artifactFacts{Checklist: cleanChecklist()}
	sc := scopeFor(workbench.ContentSelection{Email: true, EmailConfig: true})
	p := buildMigrationPlan(f, sc)

	for _, key := range []string{"files", "databases", "cron"} {
		if a := planArea(t, p, key); a.Category != planExcluded || a.Included {
			t.Errorf("area %q must be excluded when out of scope, got category=%q included=%v", key, a.Category, a.Included)
		}
	}
	email := planArea(t, p, "email")
	if email.Category != planAutomatic {
		t.Errorf("email content in scope must be automatic, got %q", email.Category)
	}
	if !email.AutoRunnable {
		t.Error("email content in scope must be auto-runnable")
	}
}

// Case 3 — checklist blocks apply: cannot start, and the blocker is surfaced in
// human terms.
func TestMigrationPlanApplyBlocked(t *testing.T) {
	f := artifactFacts{Checklist: &accountinventory.MigrationChecklist{
		Mode:          "migration-checklist",
		FormatVersion: 1,
		OverallStatus: accountinventory.OverallNotReady,
		ApplyBlocked:  true,
		Sections: []accountinventory.ChecklistSection{
			{Section: "web_files", BlockersApply: []string{"spazio insufficiente sulla destinazione"}},
		},
	}}
	p := buildMigrationPlan(f, scopeFor(workbench.ContentSelection{Files: true}))
	if p.CanStartMigration {
		t.Error("apply blocked → CanStartMigration must be false")
	}
	if len(p.Blockers) == 0 {
		t.Error("apply blocked → expected at least one blocker surfaced")
	}
}

// Case 3b — a blocker for an EXCLUDED area is not shown among the in-scope
// blockers but is surfaced separately (never hidden), and it still keeps the
// migration from starting (the apply gate is global).
func TestMigrationPlanExcludedBlockerNotHidden(t *testing.T) {
	f := artifactFacts{Checklist: &accountinventory.MigrationChecklist{
		Mode:          "migration-checklist",
		FormatVersion: 1,
		OverallStatus: accountinventory.OverallNotReady,
		ApplyBlocked:  true,
		Sections: []accountinventory.ChecklistSection{
			{Section: "cron", BlockersApply: []string{"crontab non leggibile"}},
		},
	}}
	// Scope is files-only: cron is excluded.
	p := buildMigrationPlan(f, scopeFor(workbench.ContentSelection{Files: true}))
	if len(p.Blockers) != 0 {
		t.Errorf("cron blocker must not appear among in-scope blockers, got %v", p.Blockers)
	}
	if len(p.ExcludedBlockers) == 0 {
		t.Error("cron blocker must be surfaced as an excluded blocker, not hidden")
	}
	if p.CanStartMigration {
		t.Error("a global apply block must still prevent start even if the area is excluded")
	}
}

// Case 3c — a global/unknown section blocker (e.g. ssl) is never demoted to the
// excluded set, whatever the scope.
func TestMigrationPlanGlobalBlockerAlwaysShown(t *testing.T) {
	f := artifactFacts{Checklist: &accountinventory.MigrationChecklist{
		Mode:          "migration-checklist",
		FormatVersion: 1,
		OverallStatus: accountinventory.OverallNotReady,
		ApplyBlocked:  true,
		Sections: []accountinventory.ChecklistSection{
			{Section: "ssl", BlockersApply: []string{"certificato scaduto"}},
		},
	}}
	p := buildMigrationPlan(f, scopeFor(workbench.ContentSelection{Files: true}))
	if len(p.Blockers) == 0 {
		t.Error("a global/unknown section blocker must always be shown, never hidden by scope")
	}
}

// Case 4 — DNS included: DNS is manual/verifiable in the primary flow and is
// NEVER auto-runnable, even though a dns_apply writer exists.
func TestMigrationPlanDNSNeverAutomatic(t *testing.T) {
	f := artifactFacts{Checklist: cleanChecklist(), DNS: areaFacts{PlanPresent: true}}
	p := buildMigrationPlan(f, scopeFor(workbench.ContentSelection{Files: true, DNS: true}))
	dns := planArea(t, p, "dns")
	if dns.Category != planManualVerifiable {
		t.Errorf("DNS in the primary flow must be manual_verifiable, got %q", dns.Category)
	}
	if dns.AutoRunnable {
		t.Error("DNS must never be auto-runnable in the primary flow")
	}
}

// Case 4b — DNS-only scope: nothing is auto-runnable, so the migration cannot
// start automatically, but the message must be honest (manual-only), not scold
// the operator to pick file/db/email. The verdict must not contradict the real
// (unblocked) gate — it is conservative by product design, not by a false block.
func TestMigrationPlanDNSOnlyScope(t *testing.T) {
	p := buildMigrationPlan(artifactFacts{Checklist: cleanChecklist()},
		scopeFor(workbench.ContentSelection{DNS: true}))
	if p.CanStartMigration {
		t.Error("DNS-only scope has no automatic area → cannot start automatically")
	}
	if len(p.Blockers) != 0 {
		t.Error("DNS-only clean checklist has no blockers")
	}
	low := strings.ToLower(p.StartSummary)
	if !strings.Contains(low, "manual") {
		t.Errorf("DNS-only start summary should explain it is manual-only, got %q", p.StartSummary)
	}
	if strings.Contains(low, "scegli almeno") {
		t.Errorf("DNS-only start summary must not scold the operator, got %q", p.StartSummary)
	}
}

// Case 6 — an in-scope area with no plan yet is "informational" (not yet
// classifiable), never falsely automatic.
func TestMigrationPlanUnprovenAreaNotAutomatic(t *testing.T) {
	f := artifactFacts{Checklist: cleanChecklist()} // no email/cron plan present
	p := buildMigrationPlan(f, scopeFor(workbench.ContentSelection{EmailConfig: true, Cron: true}))
	for _, key := range []string{"email_config", "cron"} {
		a := planArea(t, p, key)
		if a.Category != planInformational {
			t.Errorf("%s without a plan must be informational, got %q", key, a.Category)
		}
		if a.AutoRunnable {
			t.Errorf("%s without a plan must not be auto-runnable", key)
		}
	}
}

// Case 7 — a legacy session emits an explicit "scope not explicit" warning.
func TestMigrationPlanLegacyWarning(t *testing.T) {
	p := buildMigrationPlan(artifactFacts{Checklist: cleanChecklist()}, legacyScope())
	if len(p.Warnings) == 0 {
		t.Error("legacy session must carry an explicit scope warning")
	}
}

// Case 5 — legacy session without Setup: conservative behaviour, no panic,
// scope reported as not explicit.
func TestMigrationPlanLegacyNoSetup(t *testing.T) {
	p := buildMigrationPlan(artifactFacts{Checklist: cleanChecklist()}, legacyScope())
	if p.HasSetup {
		t.Error("legacy session (nil Setup) → HasSetup must be false")
	}
	if len(p.Areas) == 0 {
		t.Error("legacy session must still list areas (all included), got none")
	}
	// A legacy session includes everything; DNS is still manual, never automatic.
	if dns := planArea(t, p, "dns"); dns.AutoRunnable {
		t.Error("legacy DNS must never be auto-runnable")
	}
}

// Render — the "Cosa verrà migrato" screen shows the plan block: ready verdict,
// the deferred (disabled) CTA, and the DNS manual note. A clean checklist yields
// "Pronto per migrare".
func TestMigrationPlanScreenRendersReady(t *testing.T) {
	h, store, dir := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	writeChecklist(t, dir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallReadyToCutover,
	})
	code, body := getBody(t, h, "/workbench/session/"+sess.ID+"/migrazione")
	if code != 200 {
		t.Fatalf("migrazione = %d, want 200", code)
	}
	// Fase 2: the CTA is state-aware. An unconfirmed scope (legacy session, no
	// ScopeConfirmedAt) shows the "confirm scope first" label, not the deferred
	// "disponibile nella Fase 3" (which appears only once the scope is confirmed).
	for _, want := range []string{"Piano migrazione", "Pronto per migrare", "Conferma lo scope prima di avviare", "DNS"} {
		if !strings.Contains(body, want) {
			t.Errorf("plan screen missing %q", want)
		}
	}
	// The one-click button itself must NOT be wired yet: no start action form.
	if strings.Contains(body, `name="action" value="start_migration"`) {
		t.Error("Fase 1 must not wire a one-click start action")
	}
}

// Render — an apply-blocking checklist shows "Non ancora pronto" and surfaces
// the blocker reason in human terms.
func TestMigrationPlanScreenRendersBlocked(t *testing.T) {
	h, store, dir := newTestWorkbenchHandler(t)
	sess, _ := store.Create("giorgini", "src", "dst", time.Now())
	writeChecklist(t, dir, accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		OverallStatus: accountinventory.OverallNotReady,
		ApplyBlocked:  true,
		Sections: []accountinventory.ChecklistSection{
			{Section: "web_files", BlockersApply: []string{"spazio insufficiente sulla destinazione"}},
		},
	})
	code, body := getBody(t, h, "/workbench/session/"+sess.ID+"/migrazione")
	if code != 200 {
		t.Fatalf("migrazione = %d, want 200", code)
	}
	if !strings.Contains(body, "Non ancora pronto") {
		t.Error("apply-blocked plan must render 'Non ancora pronto'")
	}
	if !strings.Contains(body, "spazio insufficiente sulla destinazione") {
		t.Error("apply-blocked plan must surface the blocker reason")
	}
}
