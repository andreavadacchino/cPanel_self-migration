package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/events"
	"github.com/tis24dev/cPanel_self-migration/internal/migrate"
)

// TestPhaseCollectorRecordsCompletedInOrderOnce pins the report.json
// phases_completed source of truth: only phase_completed events count, order
// is first-completion order, duplicates and run-level events are ignored.
func TestPhaseCollectorRecordsCompletedInOrderOnce(t *testing.T) {
	c := newPhaseCollector()
	feed := []events.Event{
		{Phase: events.PhaseConnect, Type: events.EventPhaseStarted},
		{Phase: events.PhaseConnect, Type: events.EventPhaseCompleted},
		{Phase: events.PhaseMigrateMail, Type: events.EventPhaseFailed},
		{Phase: events.PhaseCreateDomains, Type: events.EventPhaseCompleted},
		{Phase: events.PhaseCreateDomains, Type: events.EventPhaseCompleted}, // dup
		{Phase: "", Type: events.EventPhaseCompleted},                        // run-level: no phase
		{Type: events.EventRunCompleted},
	}
	for _, e := range feed {
		c.observe(e)
	}
	got := c.completed()
	want := []events.Phase{events.PhaseConnect, events.PhaseCreateDomains}
	if len(got) != len(want) {
		t.Fatalf("completed = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("completed[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestBuildRunReportIncludesPhasesAndArtifacts verifies the collected phases
// and artifacts land in the report, and that absent values stay JSON-friendly
// (empty slice, not null).
func TestBuildRunReportIncludesPhasesAndArtifacts(t *testing.T) {
	now := time.Now()
	opts := migrate.Options{Apply: true, DoMail: true, RunID: "run-x", Now: now}
	phases := []events.Phase{events.PhaseCreateDomains, events.PhaseMigrateMail}
	arts := map[string]string{"migration_report_log": "/out/logs/migration_report.log"}

	rpt := buildRunReport(opts, config.Config{}, now, now, nil, nil, phases, arts)

	if len(rpt.PhasesCompleted) != 2 || rpt.PhasesCompleted[0] != events.PhaseCreateDomains || rpt.PhasesCompleted[1] != events.PhaseMigrateMail {
		t.Errorf("PhasesCompleted = %v, want the collected phases in order", rpt.PhasesCompleted)
	}
	if rpt.Artifacts["migration_report_log"] != "/out/logs/migration_report.log" {
		t.Errorf("Artifacts = %v, want the migration report log", rpt.Artifacts)
	}

	empty := buildRunReport(opts, config.Config{}, now, now, nil, nil, nil, nil)
	if empty.PhasesCompleted == nil {
		t.Error("PhasesCompleted must be an empty slice (JSON []), not nil (JSON null)")
	}
}

// TestRunArtifactsChecksExistence pins the honesty rule: artifacts are
// recorded only when the file actually exists on disk.
func TestRunArtifactsChecksExistence(t *testing.T) {
	outDir := t.TempDir()

	if arts := runArtifacts(outDir, true, true); len(arts) != 0 {
		t.Errorf("no files on disk → artifacts = %v, want empty", arts)
	}

	logsDir := filepath.Join(outDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repLog := filepath.Join(logsDir, "migration_report.log")
	if err := os.WriteFile(repLog, []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	evPath := filepath.Join(outDir, "events.jsonl")
	if err := os.WriteFile(evPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	arts := runArtifacts(outDir, true, true)
	if arts["migration_report_log"] != repLog || arts["events_jsonl"] != evPath {
		t.Errorf("artifacts = %v, want both files recorded", arts)
	}

	// A dry-run must not claim the migration report log even if a stale one
	// exists from a previous apply; events.jsonl is only recorded when
	// --json-events was set.
	arts = runArtifacts(outDir, false, false)
	if len(arts) != 0 {
		t.Errorf("dry-run without --json-events → artifacts = %v, want empty", arts)
	}
}
