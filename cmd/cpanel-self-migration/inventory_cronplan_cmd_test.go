package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

func writeCronInventory(t *testing.T, dir, name, side, user string,
	jobs []accountinventory.CronJobEntry,
	envs []accountinventory.CronEnvEntry,
) string {
	t.Helper()
	inv := accountinventory.NewEmptyInventory(user, "192.0.2.1", side)
	inv.Cron.Available = true
	inv.Cron.Method = "ssh_crontab_l"
	inv.Cron.Jobs = jobs
	inv.Cron.Environment = envs
	b, err := json.MarshalIndent(inv, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInventoryCronPlanCmdFlagAndInputErrors(t *testing.T) {
	if code := runInventoryCronPlanCmd([]string{"--bogus"}); code != 2 {
		t.Errorf("unknown flag: code = %d, want 2", code)
	}
	if code := runInventoryCronPlanCmd([]string{}); code != 1 {
		t.Errorf("missing required flags: code = %d, want 1", code)
	}
	dir := t.TempDir()
	src := writeCronInventory(t, dir, "src.json", "source", "acct", nil, nil)
	if code := runInventoryCronPlanCmd([]string{"--source", src, "--destination", filepath.Join(dir, "nope.json")}); code != 1 {
		t.Errorf("nonexistent destination: code = %d, want 1", code)
	}
}

func TestInventoryCronPlanCmdNotAnInventory(t *testing.T) {
	dir := t.TempDir()
	bogus := filepath.Join(dir, "x.json")
	if err := os.WriteFile(bogus, []byte(`{"mode":"other"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code := runInventoryCronPlanCmd([]string{"--source", bogus, "--destination", bogus})
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}

func TestInventoryCronPlanCmdEndToEnd(t *testing.T) {
	dir := t.TempDir()
	srcJobs := []accountinventory.CronJobEntry{
		{
			Type: "schedule", Minute: "0", Hour: "3", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
			CommandRedacted: "/usr/bin/true", CommandClear: "/usr/bin/true",
			CommandSHA256: "sha256:abc", RawLine: "0 3 * * * /usr/bin/true",
			Enabled: true, CommandCollected: true,
		},
	}
	srcEnvs := []accountinventory.CronEnvEntry{
		{Name: "MAILTO", ValueRedacted: "test@example.com", ValueClear: "test@example.com", ValueCollected: true},
	}
	src := writeCronInventory(t, dir, "src.json", "source", "acct", srcJobs, srcEnvs)
	dest := writeCronInventory(t, dir, "dest.json", "destination", "acct", nil, nil)

	outJSON := filepath.Join(dir, "cron_apply_plan.json")
	outMD := filepath.Join(dir, "cron_apply_plan.md")

	code := runInventoryCronPlanCmd([]string{
		"--source", src, "--destination", dest,
		"--output-json", outJSON, "--output-md", outMD,
	})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}

	b, err := os.ReadFile(outJSON)
	if err != nil {
		t.Fatal(err)
	}
	var plan accountinventory.CronApplyPlan
	if err := json.Unmarshal(b, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Mode != "cron-apply-plan" || plan.FormatVersion != 1 {
		t.Errorf("mode/version = %q/%d", plan.Mode, plan.FormatVersion)
	}
	if plan.Summary.Create != 2 { // 1 job + 1 env
		t.Errorf("summary = %+v, want 2 create", plan.Summary)
	}
	// The embedded hashes must match the raw input files.
	srcSHA, err := fileSHA256(src)
	if err != nil {
		t.Fatal(err)
	}
	if plan.SourceSHA256 != srcSHA {
		t.Errorf("source_sha256 = %q, want %q", plan.SourceSHA256, srcSHA)
	}
	if plan.SourceUser != "acct" || plan.DestinationUser != "acct" {
		t.Errorf("users = %q/%q", plan.SourceUser, plan.DestinationUser)
	}
	if _, err := os.Stat(outMD); err != nil {
		t.Errorf("markdown plan missing: %v", err)
	}
}

func TestInventoryCronPlanCmdSkipIdentical(t *testing.T) {
	dir := t.TempDir()
	job := accountinventory.CronJobEntry{
		Type: "schedule", Minute: "0", Hour: "3", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		CommandRedacted: "/usr/bin/true", CommandClear: "/usr/bin/true",
		CommandSHA256: "sha256:abc", RawLine: "0 3 * * * /usr/bin/true",
		Enabled: true, CommandCollected: true,
	}
	src := writeCronInventory(t, dir, "src.json", "source", "acct",
		[]accountinventory.CronJobEntry{job}, nil)
	dest := writeCronInventory(t, dir, "dest.json", "destination", "acct",
		[]accountinventory.CronJobEntry{job}, nil)

	outJSON := filepath.Join(dir, "plan.json")
	code := runInventoryCronPlanCmd([]string{
		"--source", src, "--destination", dest,
		"--output-json", outJSON, "--output-md", filepath.Join(dir, "plan.md"),
	})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	b, _ := os.ReadFile(outJSON)
	var plan accountinventory.CronApplyPlan
	if err := json.Unmarshal(b, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Summary.Skip != 1 || plan.Summary.Create != 0 {
		t.Errorf("summary = %+v, want 1 skip + 0 create", plan.Summary)
	}
}

func TestInventoryCronPlanCmdDerivedMD(t *testing.T) {
	dir := t.TempDir()
	src := writeCronInventory(t, dir, "src.json", "source", "acct", nil, nil)
	dest := writeCronInventory(t, dir, "dest.json", "destination", "acct", nil, nil)

	outJSON := filepath.Join(dir, "my_plan.json")
	code := runInventoryCronPlanCmd([]string{
		"--source", src, "--destination", dest,
		"--output-json", outJSON,
	})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	// Without --output-md, the markdown path is derived from the JSON path.
	mdPath := filepath.Join(dir, "my_plan.md")
	if _, err := os.Stat(mdPath); err != nil {
		t.Errorf("derived markdown missing: %v", err)
	}
}
