package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

// --- helpers -----------------------------------------------------------------

func readCronVerifyReport(t *testing.T, path string) CronVerifyReport {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var rep CronVerifyReport
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatalf("parse report: %v", err)
	}
	return rep
}

// --- flag/input errors -------------------------------------------------------

func TestCronVerifyCmdFlagAndInputErrors(t *testing.T) {
	dir := t.TempDir()
	if code := runCronVerifyCmd([]string{"--definitely-not-a-flag"}); code != 2 {
		t.Errorf("unknown flag: code = %d, want 2", code)
	}
	if code := runCronVerifyCmd([]string{}); code != 1 {
		t.Errorf("missing --plan: code = %d, want 1", code)
	}
	if code := runCronVerifyCmd([]string{"--plan", filepath.Join(dir, "nope.json")}); code != 1 {
		t.Errorf("nonexistent plan: code = %d, want 1", code)
	}
	badMode := filepath.Join(dir, "badmode.json")
	if err := os.WriteFile(badMode, []byte(`{"mode":"dns-import-plan"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runCronVerifyCmd([]string{"--plan", badMode}); code != 1 {
		t.Errorf("wrong mode: code = %d, want 1", code)
	}
	badVer := filepath.Join(dir, "badver.json")
	if err := os.WriteFile(badVer, []byte(`{"mode":"cron-apply-plan","format_version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runCronVerifyCmd([]string{"--plan", badVer}); code != 1 {
		t.Errorf("unknown format_version: code = %d, want 1", code)
	}
}

// --- stale-plan gate ---------------------------------------------------------

func TestCronVerifyCmdStalePlanGate(t *testing.T) {
	dir := t.TempDir()
	src := writeCronInventory(t, dir, "src.json", "source", "acct", nil, nil)
	outJSON := filepath.Join(dir, "report.json")
	outMD := filepath.Join(dir, "report.md")

	t.Run("hash mismatch", func(t *testing.T) {
		plan := accountinventory.CronApplyPlan{
			Mode: "cron-apply-plan", FormatVersion: 1,
			SourceSHA256: "deadbeef",
		}
		planPath := filepath.Join(t.TempDir(), "plan.json")
		if err := accountinventory.WriteCronPlanJSON(planPath, plan); err != nil {
			t.Fatal(err)
		}
		code := runCronVerifyCmd([]string{"--plan", planPath, "--source", src,
			"--output-json", outJSON, "--output-md", outMD})
		if code != 3 {
			t.Fatalf("code = %d, want 3 (stale-plan refusal)", code)
		}
		if _, err := os.Stat(outJSON); !os.IsNotExist(err) {
			t.Error("a refused verify must not write a report")
		}
	})

	t.Run("no embedded hash", func(t *testing.T) {
		plan := accountinventory.CronApplyPlan{
			Mode: "cron-apply-plan", FormatVersion: 1,
			// SourceSHA256 intentionally empty
		}
		planPath := filepath.Join(t.TempDir(), "plan.json")
		if err := accountinventory.WriteCronPlanJSON(planPath, plan); err != nil {
			t.Fatal(err)
		}
		code := runCronVerifyCmd([]string{"--plan", planPath, "--source", src,
			"--output-json", outJSON, "--output-md", outMD})
		if code != 3 {
			t.Fatalf("code = %d, want 3 (no embedded hash)", code)
		}
	})
}

// --- manual-only plan (offline) ----------------------------------------------

func TestCronVerifyCmdManualOnlyOffline(t *testing.T) {
	dir := t.TempDir()
	plan := accountinventory.CronApplyPlan{
		Mode: "cron-apply-plan", FormatVersion: 1,
		SourceUser: "acct", DestinationUser: "acct",
		Ops: []accountinventory.CronPlanOp{
			{Section: accountinventory.CronSectionJobs, Action: accountinventory.CronActionManual,
				Key: "disabled-job", Reason: "disabled"},
		},
		Summary: accountinventory.CronPlanSummary{Manual: 1},
	}
	planPath := filepath.Join(dir, "plan.json")
	if err := accountinventory.WriteCronPlanJSON(planPath, plan); err != nil {
		t.Fatal(err)
	}

	outJSON := filepath.Join(dir, "report.json")
	outMD := filepath.Join(dir, "report.md")

	// A manual-only plan needs no config and opens no SSH.
	cwd, _ := os.Getwd()
	empty := t.TempDir()
	_ = os.Chdir(empty)
	defer func() { _ = os.Chdir(cwd) }()

	code := runCronVerifyCmd([]string{
		"--plan", planPath,
		"--output-json", outJSON, "--output-md", outMD,
	})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}

	rep := readCronVerifyReport(t, outJSON)
	if rep.Summary.Manual != 1 {
		t.Errorf("summary = %+v, want 1 manual", rep.Summary)
	}
	// Manual ops make the verdict not-clean.
	if rep.Clean {
		t.Error("manual ops should make the verdict not clean")
	}
}

// --- E2E verify after apply --------------------------------------------------

func TestCronVerifyCmdEndToEndAfterApply(t *testing.T) {
	cfgPath, stateDir := setupCronServer(t)
	dir := t.TempDir()
	planPath := buildCronTestPlan(t, dir)
	setCronStubState(t, stateDir, "# existing\n")

	// Apply first.
	applyJSON := filepath.Join(dir, "apply_report.json")
	if code := runCronApplyCmd([]string{
		"--plan", planPath, "--config", cfgPath, "--yes-apply-writes",
		"--backup", filepath.Join(dir, "backup.json"),
		"--output-json", applyJSON,
	}); code != 0 {
		t.Fatalf("apply: code = %d", code)
	}

	// Now verify: all create ops should show as applied.
	outJSON := filepath.Join(dir, "verify_report.json")
	outMD := filepath.Join(dir, "verify_report.md")
	code := runCronVerifyCmd([]string{
		"--plan", planPath, "--config", cfgPath,
		"--output-json", outJSON, "--output-md", outMD,
	})
	if code != 0 {
		t.Fatalf("verify: code = %d, want 0", code)
	}

	rep := readCronVerifyReport(t, outJSON)
	if rep.Summary.Applied != 2 { // 1 job + 1 env
		t.Errorf("summary = %+v, want 2 applied", rep.Summary)
	}
	if !rep.Clean {
		t.Error("post-apply verify should be clean")
	}
	if _, err := os.Stat(outMD); err != nil {
		t.Error("markdown report missing")
	}
}

// --- fail-on-drift -----------------------------------------------------------

func TestCronVerifyCmdFailOnDrift(t *testing.T) {
	cfgPath, stateDir := setupCronServer(t)
	dir := t.TempDir()
	planPath := buildCronTestPlan(t, dir)
	// Destination crontab does NOT contain the planned lines = pending/drift.
	setCronStubState(t, stateDir, "# empty destination\n")

	outJSON := filepath.Join(dir, "verify_report.json")
	outMD := filepath.Join(dir, "verify_report.md")

	// Without --fail-on-drift: exit 0 even with pending ops.
	code := runCronVerifyCmd([]string{
		"--plan", planPath, "--config", cfgPath,
		"--output-json", outJSON, "--output-md", outMD,
	})
	if code != 0 {
		t.Fatalf("verify without --fail-on-drift: code = %d, want 0", code)
	}

	// With --fail-on-drift: exit 3.
	code = runCronVerifyCmd([]string{
		"--plan", planPath, "--config", cfgPath, "--fail-on-drift",
		"--output-json", outJSON, "--output-md", outMD,
	})
	if code != 3 {
		t.Fatalf("verify with --fail-on-drift: code = %d, want 3", code)
	}
}
