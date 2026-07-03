package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
)

// --- stateful crontab stub ---------------------------------------------------

// cronStubScript is a STATEFUL bash stub that stores the crontab in a file
// at $CPSM_CRON_STATE/crontab.txt. It handles `crontab -l` (read) and
// `crontab -` (write from stdin) — the latter is called by InstallCrontab
// via a pipeline: `printf '%s' "$CRONTAB_CONTENT" | crontab - 2>&1`.
// bash interprets the pipeline, so this script receives `-` as $1 and the
// content on stdin.
//
// The stub outputs raw crontab content only (no markers): the real
// crontabScript in cron.go wraps the output with the __CRONTAB_RC marker.
const cronStubScript = `#!/bin/bash
# Stateful crontab stub for tests.
S="$CPSM_CRON_STATE"
CT="$S/crontab.txt"
touch "$CT"

case "$1" in
  -l)
    cat "$CT"
    ;;
  -)
    # Write: read stdin into the state file.
    cat > "$CT"
    ;;
  *)
    echo "stub: unknown crontab call: $*" >&2
    exit 1
    ;;
esac
`

// setupCronServer starts the in-process SSH server with the stateful
// crontab stub and writes a host.yaml whose DESTINATION points at it.
func setupCronServer(t *testing.T) (cfgPath, stateDir string) {
	t.Helper()
	tmp := t.TempDir()
	stubDir := filepath.Join(tmp, "bin")
	stateDir = filepath.Join(tmp, "state")
	for _, d := range []string{stubDir, stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stubDir, "crontab"), []byte(cronStubScript), 0o755); err != nil { // #nosec G306 -- stub must be executable
		t.Fatal(err)
	}
	t.Setenv("CPSM_CRON_STATE", stateDir)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", tmp)

	addr := sshtest.NewExecServer(t, tmp)
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	cfgPath = filepath.Join(tmp, "host.yaml")
	yaml := fmt.Sprintf(`src:
  ip: %[1]s
  port: %[2]s
  ssh_user: u
  ssh_pass: p
  timeout: 10s
dest:
  ip: %[1]s
  port: %[2]s
  ssh_user: u
  ssh_pass: p
  timeout: 10s
`, host, port)
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, stateDir
}

func setCronStubState(t *testing.T, stateDir, crontab string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(stateDir, "crontab.txt"),
		[]byte(crontab), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readCronStubState(t *testing.T, stateDir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stateDir, "crontab.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func buildCronTestPlan(t *testing.T, dir string) string {
	t.Helper()
	src := writeCronInventory(t, dir, "src.json", "source", "acct",
		[]accountinventory.CronJobEntry{
			{
				Type: "schedule", Minute: "0", Hour: "3", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
				CommandRedacted: "/usr/bin/true", CommandClear: "/usr/bin/true",
				CommandSHA256: "sha256:abc", RawLine: "0 3 * * * /usr/bin/true",
				Enabled: true, CommandCollected: true,
			},
		},
		[]accountinventory.CronEnvEntry{
			{Name: "MAILTO", ValueRedacted: "test@example.com", ValueClear: "test@example.com", ValueCollected: true},
		})
	dest := writeCronInventory(t, dir, "dest.json", "destination", "acct", nil, nil)
	planPath := filepath.Join(dir, "cron_apply_plan.json")
	if code := runInventoryCronPlanCmd([]string{
		"--source", src, "--destination", dest,
		"--output-json", planPath, "--output-md", filepath.Join(dir, "cron_apply_plan.md"),
	}); code != 0 {
		t.Fatalf("cron-plan: code = %d, want 0", code)
	}
	return planPath
}

func readCronApplyReport(t *testing.T, path string) accountinventory.CronApplyReport {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var r accountinventory.CronApplyReport
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("parse report: %v", err)
	}
	return r
}

// --- flag/input errors -------------------------------------------------------

func TestCronApplyCmdFlagAndInputErrors(t *testing.T) {
	dir := t.TempDir()
	if code := runCronApplyCmd([]string{"--bogus"}); code != 2 {
		t.Errorf("unknown flag: code = %d, want 2", code)
	}
	if code := runCronApplyCmd([]string{}); code != 1 {
		t.Errorf("missing --plan: code = %d, want 1", code)
	}
	if code := runCronApplyCmd([]string{"--plan", "x.json", "--rollback", "y.json"}); code != 2 {
		t.Errorf("--plan + --rollback: code = %d, want 2", code)
	}
	badMode := filepath.Join(dir, "badmode.json")
	if err := os.WriteFile(badMode, []byte(`{"mode":"dns-import-plan"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runCronApplyCmd([]string{"--plan", badMode}); code != 1 {
		t.Errorf("wrong mode: code = %d, want 1", code)
	}
	badVer := filepath.Join(dir, "badver.json")
	if err := os.WriteFile(badVer, []byte(`{"mode":"cron-apply-plan","format_version":9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runCronApplyCmd([]string{"--plan", badVer}); code != 1 {
		t.Errorf("unknown format_version: code = %d, want 1", code)
	}
}

// The default dry-run is fully offline: no config, no connections.
func TestCronApplyCmdDryRunIsOffline(t *testing.T) {
	dir := t.TempDir()
	planPath := buildCronTestPlan(t, dir)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	if err := os.Chdir(empty); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	if code := runCronApplyCmd([]string{"--plan", planPath}); code != 0 {
		t.Fatalf("dry-run: code = %d, want 0 (offline, no config needed)", code)
	}
	entries, err := os.ReadDir(empty)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("dry-run wrote artifacts: %v", entries)
	}
}

// --- apply end-to-end --------------------------------------------------------

func TestCronApplyCmdEndToEnd(t *testing.T) {
	cfgPath, stateDir := setupCronServer(t)
	dir := t.TempDir()
	planPath := buildCronTestPlan(t, dir)
	// Set an existing crontab on the destination.
	setCronStubState(t, stateDir, "# existing crontab\n*/5 * * * * /usr/bin/existing\n")

	outJSON := filepath.Join(dir, "cron_apply_report.json")
	backupPath := filepath.Join(dir, "cron_backup_test.json")

	code := runCronApplyCmd([]string{
		"--plan", planPath, "--config", cfgPath, "--yes-apply-writes",
		"--backup", backupPath, "--output-json", outJSON,
	})
	if code != 0 {
		t.Fatalf("apply: code = %d, want 0", code)
	}

	rep := readCronApplyReport(t, outJSON)
	if rep.Summary.Applied != 2 || rep.Summary.Failed != 0 || rep.Summary.Refused != 0 {
		t.Fatalf("summary = %+v, want 2 applied (1 job + 1 env)", rep.Summary)
	}

	// The destination crontab contains the existing content PLUS new lines.
	ct := readCronStubState(t, stateDir)
	if !strings.Contains(ct, "/usr/bin/existing") {
		t.Error("existing crontab entry was lost during merge")
	}
	if !strings.Contains(ct, "0 3 * * * /usr/bin/true") {
		t.Error("new cron job was not installed")
	}
	if !strings.Contains(ct, "MAILTO=test@example.com") {
		t.Error("new env line was not installed")
	}

	// Bidirectional pairing: report <-> backup.
	if rep.BackupFile != backupPath || rep.BackupSHA256 == "" {
		t.Errorf("report backup pairing = %q/%q", rep.BackupFile, rep.BackupSHA256)
	}
	b, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	var backup accountinventory.CronApplyBackup
	if err := json.Unmarshal(b, &backup); err != nil {
		t.Fatal(err)
	}
	if backup.Mode != "cron-apply-backup" || backup.ReportFile != outJSON {
		t.Errorf("backup = mode %q report %q", backup.Mode, backup.ReportFile)
	}
	// The backup holds the PRE-write crontab.
	if !strings.Contains(backup.RawCrontab, "/usr/bin/existing") {
		t.Error("backup should contain the pre-write crontab")
	}
	if strings.Contains(backup.RawCrontab, "/usr/bin/true") {
		t.Error("backup should NOT contain the newly applied job")
	}
}

// No-op plan: no creates, no SSH, exit 0.
func TestCronApplyCmdNoCreates(t *testing.T) {
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
	planPath := filepath.Join(dir, "plan.json")
	if code := runInventoryCronPlanCmd([]string{
		"--source", src, "--destination", dest,
		"--output-json", planPath, "--output-md", filepath.Join(dir, "plan.md"),
	}); code != 0 {
		t.Fatalf("plan: code = %d", code)
	}

	outJSON := filepath.Join(dir, "report.json")
	// Apply with --yes: all-skip plan needs no SSH; should succeed
	// even without a config file.
	cwd, _ := os.Getwd()
	empty := t.TempDir()
	_ = os.Chdir(empty)
	defer func() { _ = os.Chdir(cwd) }()

	code := runCronApplyCmd([]string{
		"--plan", planPath, "--yes-apply-writes",
		"--output-json", outJSON,
	})
	if code != 0 {
		t.Fatalf("all-skip apply: code = %d, want 0", code)
	}
	rep := readCronApplyReport(t, outJSON)
	if rep.Summary.Skipped != 1 || rep.Summary.Applied != 0 {
		t.Errorf("summary = %+v, want 1 skipped", rep.Summary)
	}
}

// --- rollback ----------------------------------------------------------------

func TestCronApplyCmdRollbackEndToEnd(t *testing.T) {
	cfgPath, stateDir := setupCronServer(t)
	dir := t.TempDir()
	planPath := buildCronTestPlan(t, dir)
	setCronStubState(t, stateDir, "# original\n")

	outJSON := filepath.Join(dir, "cron_apply_report.json")
	backupPath := filepath.Join(dir, "cron_backup_test.json")
	if code := runCronApplyCmd([]string{
		"--plan", planPath, "--config", cfgPath, "--yes-apply-writes",
		"--backup", backupPath, "--output-json", outJSON,
	}); code != 0 {
		t.Fatalf("apply: code = %d", code)
	}

	// Rollback dry-run: offline.
	rbJSON := filepath.Join(dir, "cron_rollback_report.json")
	if code := runCronApplyCmd([]string{"--rollback", backupPath, "--output-json", rbJSON}); code != 0 {
		t.Fatalf("rollback dry-run: code = %d, want 0", code)
	}
	if _, err := os.Stat(rbJSON); !os.IsNotExist(err) {
		t.Error("rollback dry-run must not write a report")
	}

	// Real rollback.
	if code := runCronApplyCmd([]string{
		"--rollback", backupPath, "--config", cfgPath, "--yes-apply-writes",
		"--output-json", rbJSON,
	}); code != 0 {
		t.Fatalf("rollback: code = %d, want 0", code)
	}
	rb := readCronApplyReport(t, rbJSON)
	if rb.RunMode != "rollback" || rb.Summary.Applied != 1 {
		t.Fatalf("rollback report = run_mode %q summary %+v", rb.RunMode, rb.Summary)
	}
	// The crontab must be back to the original.
	ct := readCronStubState(t, stateDir)
	if !strings.Contains(ct, "# original") {
		t.Error("rollback did not restore the original crontab")
	}
	if strings.Contains(ct, "/usr/bin/true") {
		t.Error("rollback should have removed the applied job")
	}
}
