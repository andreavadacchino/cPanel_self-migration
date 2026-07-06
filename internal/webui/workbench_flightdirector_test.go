package webui

// Unit tests for the Flight Director presentation helpers (risk badge +
// timeline). These are pure, side-effect-free translations of facts already
// computed by the engine into an honest at-a-glance summary — no new operational
// logic, no writer/runner touched.

import (
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

func wizardScope(c workbench.ContentSelection) contentScope {
	return deriveContentScope(&workbench.Session{Setup: &workbench.SetupMeta{Content: c}})
}

// --- risk badge -------------------------------------------------------------

// A live job is the top operational signal, ahead of governance state.
func TestRiskBadgeJobRunning(t *testing.T) {
	job := &jobJournal{Action: "migrate_content", State: jobStateRunning}
	got := buildRiskBadge(workbench.StatusApplyInProgress, artifactFacts{}, legacyScope(), job, true)
	if got.Label != "Job in corso" {
		t.Errorf("running job risk = %q, want %q", got.Label, "Job in corso")
	}
}

// An interrupted job must surface as an error-level attention badge, never OK.
func TestRiskBadgeJobInterrupted(t *testing.T) {
	job := &jobJournal{Action: "migrate_content", State: jobStateInterrupted}
	got := buildRiskBadge(workbench.StatusApplyInProgress, artifactFacts{}, legacyScope(), job, false)
	if got.Class != "error" {
		t.Errorf("interrupted job risk class = %q, want error", got.Class)
	}
	if got.Label != "Job interrotto" {
		t.Errorf("interrupted job risk = %q, want %q", got.Label, "Job interrotto")
	}
}

// Setup present + host.yaml absent → "Configurazione richiesta" (test #8).
func TestRiskBadgeConfigRequired(t *testing.T) {
	sc := wizardScope(workbench.ContentSelection{Files: true})
	got := buildRiskBadge(workbench.StatusDraft, artifactFacts{HostYAMLPresent: false}, sc, nil, false)
	if got.Label != "Configurazione richiesta" {
		t.Errorf("wizard w/o host.yaml risk = %q, want %q", got.Label, "Configurazione richiesta")
	}
	if got.Class != "warn" {
		t.Errorf("config-required class = %q, want warn", got.Class)
	}
}

// A migration blocker (apply-blocked) outranks a merely-missing config and is
// labelled distinctly from a cutover-only block (dogfooding #4 §6.2).
func TestRiskBadgeBlocking(t *testing.T) {
	f := artifactFacts{HostYAMLPresent: true, Checklist: &accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1, ApplyBlocked: true,
	}}
	got := buildRiskBadge(workbench.StatusChecklistReady, f, legacyScope(), nil, false)
	if got.Label != "Bloccante migrazione" || got.Class != "error" {
		t.Errorf("apply-blocked risk = %+v, want Bloccante migrazione/error", got)
	}
}

// Cutover-only blocking (OverallBlocked but ApplyBlocked=false) must NOT read as
// a migration blocker: the migration is startable, so the badge is a milder
// "Bloccante cutover" warning, visibly distinct from the migration blocker.
func TestRiskBadgeCutoverBlockingDistinct(t *testing.T) {
	f := artifactFacts{HostYAMLPresent: true, Checklist: &accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		ApplyBlocked: false, OverallStatus: accountinventory.OverallBlocked,
	}}
	got := buildRiskBadge(workbench.StatusChecklistReady, f, legacyScope(), nil, false)
	if got.Label != "Bloccante cutover" || got.Class != "warn" {
		t.Errorf("cutover-only block risk = %+v, want Bloccante cutover/warn", got)
	}
	// And it must be a different label than the migration blocker.
	mig := buildRiskBadge(workbench.StatusChecklistReady, artifactFacts{HostYAMLPresent: true,
		Checklist: &accountinventory.MigrationChecklist{Mode: "migration-checklist", FormatVersion: 1, ApplyBlocked: true},
	}, legacyScope(), nil, false)
	if got.Label == mig.Label {
		t.Errorf("cutover and migration blockers share label %q", got.Label)
	}
}

// ready_for_cutover is the only genuinely green resting state.
func TestRiskBadgeReadyForCutover(t *testing.T) {
	got := buildRiskBadge(workbench.StatusReadyForCutover, artifactFacts{HostYAMLPresent: true}, legacyScope(), nil, false)
	if got.Class != "done" {
		t.Errorf("ready_for_cutover class = %q, want done", got.Class)
	}
}

// A plain draft must not overclaim "OK"/green.
func TestRiskBadgeDraftNotGreen(t *testing.T) {
	got := buildRiskBadge(workbench.StatusDraft, artifactFacts{}, legacyScope(), nil, false)
	if got.Class == "done" {
		t.Errorf("draft risk must not be green, got %+v", got)
	}
}

// A stale checklist blocker left on disk must NOT re-flag a session that has
// already reached cutover: the terminal state wins (review finding #1).
func TestRiskBadgeCutoverDoneIgnoresStaleBlocker(t *testing.T) {
	f := artifactFacts{Checklist: &accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1, ApplyBlocked: true,
	}}
	got := buildRiskBadge(workbench.StatusCutoverDone, f, legacyScope(), nil, false)
	if got.Label != "Cutover completato" || got.Class != "done" {
		t.Errorf("cutover_done + stale blocker risk = %+v, want Cutover completato/done", got)
	}
}

// Likewise an archived session reads "Archiviata", never "Bloccante".
func TestRiskBadgeArchivedIgnoresStaleBlocker(t *testing.T) {
	f := artifactFacts{Checklist: &accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1, ApplyBlocked: true,
	}}
	got := buildRiskBadge(workbench.StatusArchived, f, legacyScope(), nil, false)
	if got.Label != "Archiviata" {
		t.Errorf("archived + stale blocker risk = %+v, want Archiviata", got)
	}
	if got.Class == "error" {
		t.Errorf("archived session must not be flagged as error, got %+v", got)
	}
}

// A live/interrupted job must stay visible even on a terminal session: the
// exec path has no status gate, so the job signal outranks the terminal
// short-circuit (regression guard for the R2 finding).
func TestRiskBadgeJobBeatsTerminalStatus(t *testing.T) {
	running := &jobJournal{Action: "migrate_content", State: jobStateRunning}
	if got := buildRiskBadge(workbench.StatusArchived, artifactFacts{}, legacyScope(), running, true); got.Label != "Job in corso" {
		t.Errorf("archived + running job risk = %q, want 'Job in corso'", got.Label)
	}
	interrupted := &jobJournal{Action: "migrate_content", State: jobStateInterrupted}
	if got := buildRiskBadge(workbench.StatusCutoverDone, artifactFacts{}, legacyScope(), interrupted, false); got.Label != "Job interrotto" {
		t.Errorf("cutover_done + interrupted job risk = %q, want 'Job interrotto'", got.Label)
	}
}

// --- timeline ---------------------------------------------------------------

// The timeline exposes all seven guided phases in order, mapped to the real
// screen routes (no invented routes).
func TestTimelineStepsAndRoutes(t *testing.T) {
	steps := buildTimeline(screenPreflight, workbench.StatusDraft, artifactFacts{}, legacyScope(), nil, false)
	wantLabels := []string{"Panoramica", "Preflight", "Fotografia account", "Cosa verrà migrato", "Conferme operatore", "Applica e verifica", "Chiusura"}
	if len(steps) != len(wantLabels) {
		t.Fatalf("timeline has %d steps, want %d", len(steps), len(wantLabels))
	}
	wantRoutes := []string{screenPanoramica, screenPreflight, screenInventario, screenMigrazione, screenConferme, screenApplica, screenChiusura}
	for i, s := range steps {
		if s.Label != wantLabels[i] {
			t.Errorf("step %d label = %q, want %q", i, s.Label, wantLabels[i])
		}
		if s.Screen != wantRoutes[i] {
			t.Errorf("step %d route = %q, want %q", i, s.Screen, wantRoutes[i])
		}
	}
}

// The step matching the rendered screen is flagged Current; exactly one is.
func TestTimelineCurrentHighlight(t *testing.T) {
	steps := buildTimeline(screenApplica, workbench.StatusReadyForApply, artifactFacts{}, legacyScope(), nil, false)
	n := 0
	for _, s := range steps {
		if s.Current {
			n++
			if s.Screen != screenApplica {
				t.Errorf("current step = %q, want applica", s.Screen)
			}
		}
	}
	if n != 1 {
		t.Errorf("exactly one current step expected, got %d", n)
	}
}

// A checklist blocker marks the "Cosa verrà migrato" phase as attention.
func TestTimelineApplyBlockedWarn(t *testing.T) {
	f := artifactFacts{Checklist: &accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1, ApplyBlocked: true,
	}}
	steps := buildTimeline(screenPanoramica, workbench.StatusChecklistReady, f, legacyScope(), nil, false)
	mig := stepByRoute(steps, screenMigrazione)
	if mig == nil || mig.State != "warn" {
		t.Errorf("migrazione step state = %v, want warn (apply blocked)", mig)
	}
}

// A running job drives the Applica phase to "doing".
func TestTimelineJobRunningApplyDoing(t *testing.T) {
	job := &jobJournal{Action: "migrate_content", State: jobStateRunning}
	steps := buildTimeline(screenPanoramica, workbench.StatusApplyInProgress, artifactFacts{}, legacyScope(), job, true)
	app := stepByRoute(steps, screenApplica)
	if app == nil || app.State != "doing" {
		t.Errorf("applica step state = %v, want doing (job running)", app)
	}
}

// cutover_done closes the last phase.
func TestTimelineCutoverDone(t *testing.T) {
	steps := buildTimeline(screenChiusura, workbench.StatusCutoverDone, artifactFacts{}, legacyScope(), nil, false)
	cl := stepByRoute(steps, screenChiusura)
	if cl == nil || cl.State != "done" {
		t.Errorf("chiusura step state = %v, want done", cl)
	}
}

// A cutover_done session must not show a stale blocker as an "Attenzione" phase,
// and an archived session must keep "Chiusura" as done (review finding #1).
func TestTimelineTerminalStalenessResolved(t *testing.T) {
	f := artifactFacts{Checklist: &accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1, ApplyBlocked: true,
	}}
	steps := buildTimeline(screenChiusura, workbench.StatusCutoverDone, f, legacyScope(), nil, false)
	if mig := stepByRoute(steps, screenMigrazione); mig == nil || mig.State != "done" {
		t.Errorf("cutover_done migrazione state = %v, want done (stale blocker ignored)", mig)
	}
	arch := buildTimeline(screenChiusura, workbench.StatusArchived, artifactFacts{}, legacyScope(), nil, false)
	if cl := stepByRoute(arch, screenChiusura); cl == nil || cl.State != "done" {
		t.Errorf("archived chiusura state = %v, want done (session closed)", cl)
	}
}

// During StatusInventoryReady (inventory on disk, checklist not yet generated)
// the Fotografia phase is "In corso", not "Da fare", matching the inventory
// signal the "Stato per fase" widget uses (review finding #2).
func TestTimelineInventoryReadyDoing(t *testing.T) {
	f := artifactFacts{InventorySourcePresent: true, InventoryDestPresent: true} // no checklist
	steps := buildTimeline(screenPanoramica, workbench.StatusInventoryReady, f, legacyScope(), nil, false)
	inv := stepByRoute(steps, screenInventario)
	if inv == nil || inv.State != "doing" {
		t.Errorf("inventory-ready Fotografia state = %v, want doing", inv)
	}
}

func stepByRoute(steps []timelineStep, route string) *timelineStep {
	for i := range steps {
		if steps[i].Screen == route {
			return &steps[i]
		}
	}
	return nil
}
