package webui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// TestNextActionTotalOverAllStatuses pins the state→action mapping: EVERY
// status in AllStatuses must yield a non-empty recommended action with a valid
// target screen. No status may fall through without guidance.
func TestNextActionTotalOverAllStatuses(t *testing.T) {
	validScreens := map[string]bool{
		screenPanoramica: true, screenPreflight: true, screenInventario: true,
		screenMigrazione: true, screenConferme: true, screenApplica: true,
		screenChiusura: true,
	}
	for _, st := range workbench.AllStatuses {
		got := nextAction(st, artifactFacts{})
		if strings.TrimSpace(got.Text) == "" {
			t.Errorf("status %q: empty recommended action text", st)
		}
		if !validScreens[got.Screen] {
			t.Errorf("status %q: invalid target screen %q", st, got.Screen)
		}
	}
}

// TestNextActionKeyStatuses pins the specific target screen for the statuses
// where the routing matters most for the guided path.
func TestNextActionKeyStatuses(t *testing.T) {
	cases := []struct {
		status workbench.Status
		screen string
	}{
		{workbench.StatusDraft, screenPreflight},
		{workbench.StatusPreflightRequired, screenPreflight},
		{workbench.StatusManualActionsRequired, screenConferme},
		{workbench.StatusReadyForApply, screenApplica},
		{workbench.StatusApplyDone, screenApplica},
		{workbench.StatusVerificationRequired, screenApplica},
		{workbench.StatusReadyForCutover, screenChiusura},
		{workbench.StatusCutoverDone, screenChiusura},
	}
	for _, c := range cases {
		got := nextAction(c.status, artifactFacts{})
		if got.Screen != c.screen {
			t.Errorf("status %q: screen = %q, want %q", c.status, got.Screen, c.screen)
		}
	}
}

// TestNextActionApplyBlockedRefinement: at ready_for_apply with an apply-blocked
// checklist, the action must signal the block (not a plain "apply now").
func TestNextActionApplyBlockedRefinement(t *testing.T) {
	f := artifactFacts{Checklist: &accountinventory.MigrationChecklist{ApplyBlocked: true}}
	got := nextAction(workbench.StatusReadyForApply, f)
	if !strings.Contains(strings.ToLower(got.Text+got.Detail), "blocc") {
		t.Errorf("ready_for_apply + ApplyBlocked: action should mention the block, got %+v", got)
	}
}

// TestNextActionVerificationListsMissing: at verification_required, the detail
// must name the missing verify areas.
func TestNextActionVerificationListsMissing(t *testing.T) {
	f := artifactFacts{
		DNS:   areaFacts{VerifyPresent: true, VerifyClean: true},
		Email: areaFacts{}, // missing
		Cron:  areaFacts{VerifyPresent: true, VerifyClean: false},
	}
	got := nextAction(workbench.StatusVerificationRequired, f)
	low := strings.ToLower(got.Detail)
	if !strings.Contains(low, "email") || !strings.Contains(low, "cron") {
		t.Errorf("verification_required: detail should list Email+Cron, got %q", got.Detail)
	}
	if strings.Contains(low, "dns") {
		t.Errorf("verification_required: DNS is clean, must not be listed, got %q", got.Detail)
	}
}

// TestMissingVerifies covers presence/cleanliness combinations.
func TestMissingVerifies(t *testing.T) {
	f := artifactFacts{
		DNS:   areaFacts{VerifyPresent: true, VerifyClean: true},  // ok
		Email: areaFacts{VerifyPresent: false},                    // missing
		Cron:  areaFacts{VerifyPresent: true, VerifyClean: false}, // not clean
	}
	got := missingVerifies(f)
	if len(got) != 2 {
		t.Fatalf("missingVerifies = %v, want 2 (Email, Cron)", got)
	}
	joined := strings.ToLower(strings.Join(got, ","))
	if !strings.Contains(joined, "email") || !strings.Contains(joined, "cron") {
		t.Errorf("missingVerifies = %v, want Email+Cron", got)
	}
}

// TestCutoverReadinessYes: READY_TO_CUTOVER, no cutover blockers, no pending
// confirmations → CanShutdown true, but the 5 runbook decisions still shown.
func TestCutoverReadinessYes(t *testing.T) {
	f := artifactFacts{Checklist: &accountinventory.MigrationChecklist{
		OverallStatus: accountinventory.OverallReadyToCutover,
	}}
	v := cutoverReadiness(f)
	if !v.CanShutdown {
		t.Errorf("READY_TO_CUTOVER, no blockers: CanShutdown = false, want true")
	}
	if len(v.RunbookDecisions) != 5 {
		t.Errorf("runbook decisions = %d, want 5 (always shown)", len(v.RunbookDecisions))
	}
}

// TestCutoverReadinessNoOnForcedStatus: the decisive regression from review —
// a checklist BLOCKED overall must NOT yield SÌ even if (hypothetically) the
// governance status was forced ahead. The verdict is artifact-derived.
func TestCutoverReadinessNoWhenBlockedOverall(t *testing.T) {
	f := artifactFacts{Checklist: &accountinventory.MigrationChecklist{
		OverallStatus: accountinventory.OverallBlocked,
	}}
	v := cutoverReadiness(f)
	if v.CanShutdown {
		t.Errorf("OverallStatus BLOCKED: CanShutdown = true, want false (no false yes on forced status)")
	}
}

// TestCutoverReadinessNoListsCutoverBlockersAndPending: NO must enumerate the
// cutover blockers and the unaccepted blocking manual actions.
func TestCutoverReadinessNoListsBlockers(t *testing.T) {
	f := artifactFacts{Checklist: &accountinventory.MigrationChecklist{
		OverallStatus: accountinventory.OverallReadyWithManualNotes,
		Sections: []accountinventory.ChecklistSection{
			{Section: "dns", BlockersCutover: []string{"POL-DNS-NS-CHANGED"}},
		},
		ManualActions: []accountinventory.ManualAction{
			{Key: "AK-1", Section: "mailboxes", Title: "Mailbox rimossa", BlockingCutover: true, Accepted: false},
			{Key: "AK-2", Section: "cron", Title: "Cron accettato", BlockingCutover: true, Accepted: true}, // accepted → not pending
		},
	}}
	v := cutoverReadiness(f)
	if v.CanShutdown {
		t.Errorf("cutover blocker present: CanShutdown = true, want false")
	}
	if len(v.BlockersCutover) != 1 {
		t.Errorf("BlockersCutover = %v, want 1", v.BlockersCutover)
	}
	if len(v.PendingConfirmations) != 1 || v.PendingConfirmations[0].Key != "AK-1" {
		t.Errorf("PendingConfirmations = %+v, want only AK-1 (AK-2 is accepted)", v.PendingConfirmations)
	}
}

// TestReadArtifactFactsFailSoft: an empty dir yields zero-value facts, never a
// panic or error — the guided path must render on a fresh session.
func TestReadArtifactFactsFailSoft(t *testing.T) {
	f := readArtifactFacts(t.TempDir())
	if f.Checklist != nil || f.HostYAMLPresent || f.DNS.PlanPresent {
		t.Errorf("empty dir: expected zero facts, got %+v", f)
	}
}

// TestReadArtifactFactsReadsChecklistAndArtifacts: with a valid checklist +
// host.yaml + a clean dns verify report, the facts reflect them.
func TestReadArtifactFactsReadsArtifacts(t *testing.T) {
	dir := t.TempDir()
	cl := accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1, Account: "acct",
		OverallStatus: accountinventory.OverallReadyToCutover,
	}
	b, _ := json.Marshal(cl)
	mustWrite(t, filepath.Join(dir, "migration_checklist.json"), b)
	mustWrite(t, filepath.Join(dir, "host.yaml"), []byte("source:\n  ip: 1.2.3.4\n"))
	mustWrite(t, filepath.Join(dir, "dns_import_plan.json"), []byte(`{}`))
	mustWrite(t, filepath.Join(dir, "dns_verify_report.json"), []byte(`{"clean":true}`))

	f := readArtifactFacts(dir)
	if f.Checklist == nil || f.Checklist.OverallStatus != accountinventory.OverallReadyToCutover {
		t.Errorf("checklist not read: %+v", f.Checklist)
	}
	if !f.HostYAMLPresent {
		t.Error("host.yaml presence not detected")
	}
	if !f.DNS.PlanPresent {
		t.Error("dns plan presence not detected")
	}
	if !f.DNS.VerifyPresent || !f.DNS.VerifyClean {
		t.Errorf("dns verify clean not detected: %+v", f.DNS)
	}
}

// TestReadArtifactFactsRejectsWrongMode: a non-checklist JSON must not populate
// Checklist (same guard as the dashboard: mode + format_version).
func TestReadArtifactFactsRejectsWrongMode(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "migration_checklist.json"), []byte(`{"mode":"something-else","format_version":1}`))
	f := readArtifactFacts(dir)
	if f.Checklist != nil {
		t.Errorf("wrong mode must be rejected, got %+v", f.Checklist)
	}
	if f.ChecklistErr == "" {
		t.Error("wrong mode should surface a ChecklistErr")
	}
}

// TestBuildCoverageJoin: covered→✅, covered+pending manual→🟡, root_only/
// not_collected→⚪ with note. Pins the Area==Section join invariant.
func TestBuildCoverageJoin(t *testing.T) {
	f := artifactFacts{Checklist: &accountinventory.MigrationChecklist{
		CoverageManifest: []accountinventory.CoverageArea{
			{Area: "dns", State: accountinventory.CoverageCovered},
			{Area: "email_filters", State: accountinventory.CoverageCovered},
			{Area: "quota_package", State: accountinventory.CoverageRootOnly, Note: "WHM territory"},
			{Area: "boxtrapper", State: accountinventory.CoverageNotCollected, Note: "not collected"},
		},
		ManualActions: []accountinventory.ManualAction{
			{Section: "email_filters", Acceptable: true, Accepted: false}, // → 🟡
			{Section: "dns", Acceptable: true, Accepted: true},            // accepted → dns stays ✅
		},
	}}
	rows := buildCoverage(f)
	byArea := map[string]coverageRow{}
	for _, r := range rows {
		byArea[r.Area] = r
	}
	if byArea[sectionLabelIT("dns")].Glyph != "✅" {
		t.Errorf("dns (accepted manual) should be ✅, got %q", byArea[sectionLabelIT("dns")].Glyph)
	}
	if byArea[sectionLabelIT("email_filters")].Glyph != "🟡" {
		t.Errorf("email_filters (pending manual) should be 🟡, got %q", byArea[sectionLabelIT("email_filters")].Glyph)
	}
	if byArea[sectionLabelIT("quota_package")].Glyph != "⚪" || byArea[sectionLabelIT("quota_package")].Note == "" {
		t.Errorf("quota_package should be ⚪ with note, got %+v", byArea[sectionLabelIT("quota_package")])
	}
	if byArea[sectionLabelIT("boxtrapper")].Glyph != "⚪" {
		t.Errorf("boxtrapper should be ⚪, got %q", byArea[sectionLabelIT("boxtrapper")].Glyph)
	}
}

// TestCoverageNoSnakeCaseLeak: EVERY area in the real coverage registry must
// translate to an Italian label — no snake_case English leaks into the table.
func TestCoverageNoSnakeCaseLeak(t *testing.T) {
	manifest := accountinventory.CoverageAreas()
	f := artifactFacts{Checklist: &accountinventory.MigrationChecklist{CoverageManifest: manifest}}
	rows := buildCoverage(f)
	if len(rows) != len(manifest) {
		t.Fatalf("coverage rows = %d, want %d (one per area)", len(rows), len(manifest))
	}
	for _, r := range rows {
		if strings.Contains(r.Area, "_") {
			t.Errorf("area label %q leaks a snake_case (untranslated) name", r.Area)
		}
	}
}

// TestStatusLabelITTotal: every status has a non-English, non-raw label.
func TestStatusLabelITTotal(t *testing.T) {
	for _, s := range workbench.AllStatuses {
		l := statusLabelIT(s)
		if l == "" || l == string(s) {
			t.Errorf("status %q: label %q is empty or raw enum", s, l)
		}
	}
}

// TestPendingConfirmationsDetail exercises the 0/1/N branches (N covers itoa).
func TestPendingConfirmationsDetail(t *testing.T) {
	none := artifactFacts{Checklist: &accountinventory.MigrationChecklist{}}
	if pendingConfirmationsDetail(none) != "" {
		t.Error("no pending → empty detail")
	}
	mk := func(n int) artifactFacts {
		var acts []accountinventory.ManualAction
		for i := 0; i < n; i++ {
			acts = append(acts, accountinventory.ManualAction{BlockingCutover: true, Acceptable: true})
		}
		return artifactFacts{Checklist: &accountinventory.MigrationChecklist{ManualActions: acts}}
	}
	if got := pendingConfirmationsDetail(mk(1)); !strings.Contains(got, "1 conferma") {
		t.Errorf("1 pending → %q", got)
	}
	if got := pendingConfirmationsDetail(mk(3)); !strings.Contains(got, "3 conferme") {
		t.Errorf("3 pending → %q", got)
	}
}

// TestBuildPhases covers todo/partial/done/ready states.
func TestBuildPhases(t *testing.T) {
	f := artifactFacts{
		HostYAMLPresent:        true,
		InventorySourcePresent: true, InventoryDestPresent: true,
		Email: areaFacts{PlanPresent: true},                                          // partial
		DNS:   areaFacts{ApplyPresent: true, VerifyPresent: true, VerifyClean: true}, // done
	}
	byLabel := map[string]string{}
	for _, p := range buildPhases(f, workbench.StatusReadyForCutover) {
		byLabel[p.Label] = p.State
	}
	if byLabel["Connessioni"] != "ok" || byLabel["Inventario"] != "ok" {
		t.Errorf("connessioni/inventario: %+v", byLabel)
	}
	if byLabel["Email"] != "partial" || byLabel["DNS"] != "done" || byLabel["Cron"] != "todo" {
		t.Errorf("area states: %+v", byLabel)
	}
	if byLabel["Cutover"] != "ready" {
		t.Errorf("cutover ready: %q", byLabel["Cutover"])
	}
}

// TestOverallLabelITTranslatesAll: every overall status has a non-raw IT label.
func TestOverallLabelITTranslatesAll(t *testing.T) {
	for _, o := range []string{
		accountinventory.OverallBlocked, accountinventory.OverallManualActionRequired,
		accountinventory.OverallNotReady, accountinventory.OverallReadyWithManualNotes,
		accountinventory.OverallReadyToCutover,
	} {
		if l := overallLabelIT(o); l == "" || l == o {
			t.Errorf("overall %q: label %q is raw/empty", o, l)
		}
	}
}

// TestStepLabelITTotal: every operational step has a non-raw Italian label.
func TestStepLabelITTotal(t *testing.T) {
	for _, s := range workbench.AllSteps {
		l := stepLabelIT(s)
		if l == "" || l == string(s) {
			t.Errorf("step %q: label %q is empty or raw enum", s, l)
		}
	}
}

// TestCoverageNoteIT: known area → Italian note (not the raw English); an
// unmapped area falls back to the raw note.
func TestCoverageNoteIT(t *testing.T) {
	if got := coverageNoteIT("quota_package", "WHM territory"); got == "WHM territory" || got == "" {
		t.Errorf("quota_package note not translated: %q", got)
	}
	if got := coverageNoteIT("unknown_area_xyz", "raw fallback"); got != "raw fallback" {
		t.Errorf("unknown area should fall back to raw note, got %q", got)
	}
}

// TestCoverageNotesNoEnglishLeak: for the real registry, every root_only/
// not_collected row must carry an Italian (translated) note.
func TestCoverageNotesNoEnglishLeak(t *testing.T) {
	f := artifactFacts{Checklist: &accountinventory.MigrationChecklist{
		CoverageManifest: accountinventory.CoverageAreas(),
	}}
	for _, r := range buildCoverage(f) {
		if r.Glyph == "⚪" && r.Note != "" {
			if _, ok := coverageNotesIT[reverseAreaLookup(r.Area)]; !ok {
				t.Errorf("area %q note has no Italian translation: %q", r.Area, r.Note)
			}
		}
	}
}

// reverseAreaLookup maps an Italian area label back to its raw area key (test
// helper — buildCoverage translates the label, so we recover the key).
func reverseAreaLookup(label string) string {
	for k, v := range areaLabelsIT {
		if v == label {
			return k
		}
	}
	return label
}

func mustWrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
