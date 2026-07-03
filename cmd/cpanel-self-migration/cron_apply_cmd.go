package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// runCronApplyCmd implements `cpanel-self-migration cron apply`: the cron
// writer (PR 2A). It consumes an offline cron_apply_plan.json and installs
// new cron entries onto the DESTINATION account only (sshx.DialDest — the
// source is never dialed, let alone written).
//
// The write primitive is `crontab -` which replaces the ENTIRE crontab:
// the apply reads the current crontab, merges the planned create ops, and
// installs the merged result in one shot.
//
// House contract:
//   - without --yes-apply-writes: fully offline preview, ZERO connections;
//   - backup-or-nothing before the first write;
//   - PlanTimeDestCrontab guard: refuse when the crontab changed since plan
//     (currently inert — the plan builder does not yet populate the field;
//     activated when the plan carries a non-empty hash);
//   - verify-after: re-read and check installed lines present;
//   - --rollback <backup>: InstallCrontab with the backup content, verify.
//
// Exit codes: 0 ok; 1 input/runtime/write failure; 2 flags; 3 gated
// refusal (refused_precondition ops, stale crontab, or refused rollback).
func runCronApplyCmd(args []string) int {
	fs := flag.NewFlagSet("cron apply", flag.ContinueOnError)
	planPath := fs.String("plan", "", "path to cron_apply_plan.json (required unless --rollback)")
	cfgFlag := fs.String("config", "", "path to host.yaml (default: configs/host.yaml or host.yaml)")
	yes := fs.Bool("yes-apply-writes", false, "actually write to the DESTINATION (default: fully offline preview, zero connections)")
	rollbackPath := fs.String("rollback", "", "path to a cron apply backup JSON: restore the backed-up crontab")
	backupFlag := fs.String("backup", "", "pre-write backup path (default: cron_backup_<account>_<timestamp>.json)")
	outJSON := fs.String("output-json", "", "report JSON path (default: cron_apply_report.json, or cron_rollback_report.json with --rollback)")
	outMD := fs.String("output-md", "", "report Markdown path (default: derived from --output-json)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration cron apply --plan cron_apply_plan.json [--yes-apply-writes] [--config host.yaml] [--backup PATH]")
		fmt.Fprintln(os.Stderr, "       cpanel-self-migration cron apply --rollback cron_backup_….json [--yes-apply-writes]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *rollbackPath != "" {
		if *planPath != "" {
			fmt.Fprintln(os.Stderr, "error: --plan and --rollback are mutually exclusive")
			return 2
		}
		return runCronRollback(*rollbackPath, *yes, *cfgFlag, *outJSON, *outMD)
	}
	if *planPath == "" {
		fmt.Fprintln(os.Stderr, "error: --plan is required")
		fs.Usage()
		return 1
	}

	plan, err := loadCronPlanFile(*planPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if plan.FormatVersion != 1 {
		fmt.Fprintf(os.Stderr, "error: %s: unsupported plan format_version %d (this build understands 1)\n", *planPath, plan.FormatVersion)
		return 1
	}

	if !*yes {
		printCronApplyDryRun(plan)
		return 0
	}
	return runCronApplyWrites(plan, *planPath, *cfgFlag, *backupFlag, *outJSON, *outMD)
}

// printCronApplyDryRun is the offline preview: no config, no SSH, no
// artifact files.
func printCronApplyDryRun(plan accountinventory.CronApplyPlan) {
	fmt.Println("cron apply — DRY-RUN (fully offline: no connection was opened, nothing was written).")
	fmt.Println()
	writes := 0
	for _, op := range plan.Ops {
		switch op.Action {
		case accountinventory.CronActionCreate:
			fmt.Printf("  create %s  %s\n", op.Section, op.Key)
			writes++
		case accountinventory.CronActionSkip:
			fmt.Printf("  skip   %s  %s\n", op.Section, op.Key)
		case accountinventory.CronActionManual:
			fmt.Printf("  manual %s  %s  (%s)\n", op.Section, op.Key, op.Reason)
		}
	}
	if writes == 0 {
		fmt.Println("  (no writable ops in this plan)")
	}
	fmt.Printf("\nplan summary: %d create, %d skip, %d manual, %d informational\n",
		plan.Summary.Create, plan.Summary.Skip, plan.Summary.Manual, plan.Summary.Informational)
	fmt.Println("to apply: re-run with --yes-apply-writes")
}

// runCronApplyWrites is the write path: read current crontab, stale guard,
// merge, backup-or-nothing, install, verify-after, report.
func runCronApplyWrites(plan accountinventory.CronApplyPlan, planPath, cfgFlag, backupFlag, outJSON, outMD string) int {
	planSHA, err := fileSHA256(planPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if outJSON == "" {
		outJSON = "cron_apply_report.json"
	}
	if outMD == "" {
		outMD = deriveMDPath(outJSON)
	}

	// Count writable ops.
	var createOps []accountinventory.CronPlanOp
	for _, op := range plan.Ops {
		if op.Action == accountinventory.CronActionCreate {
			createOps = append(createOps, op)
		}
	}

	// Build results for non-create ops first.
	results := make([]accountinventory.CronApplyOpResult, 0, len(plan.Ops))
	createIndices := map[int]int{} // plan.Ops index → results index
	createIdx := 0
	for i, op := range plan.Ops {
		res := accountinventory.CronApplyOpResult{CronPlanOp: op}
		switch op.Action {
		case accountinventory.CronActionSkip:
			res.Status = accountinventory.CronOpSkipped
		case accountinventory.CronActionManual:
			res.Status = accountinventory.CronOpManual
		case accountinventory.CronActionCreate:
			res.Status = "" // will be set below
			createIndices[i] = len(results)
			createIdx++
		default:
			res.Status = accountinventory.CronOpRefused
			res.StatusReason = fmt.Sprintf("unknown plan action %q", op.Action)
		}
		results = append(results, res)
	}

	report := accountinventory.CronApplyReport{
		Mode: "cron-apply-report", FormatVersion: 1, RunMode: "apply",
		DestinationUser: plan.DestinationUser,
		PlanFile:        planPath, PlanSHA256: planSHA,
	}

	if len(createOps) == 0 {
		// Nothing to write: no connection needed.
		report.BackupNote = "no write was decided (every op skipped, manual, or refused) — nothing to back up"
		for i := range results {
			if results[i].Status == "" {
				results[i].Status = accountinventory.CronOpSkipped
			}
		}
		report.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
		report.Results = results
		report.Summary = accountinventory.SummarizeCronResults(results)
		return finishCronReport(report, outJSON, outMD)
	}

	// Dial destination.
	ctx := context.Background()
	client, err := dialCronDest(ctx, cfgFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer func() { _ = client.Close() }()

	// Read current crontab.
	currentCrontab, err := cpanel.ReadCrontabRaw(ctx, client)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: read crontab:", err)
		return 1
	}
	currentHash := accountinventory.CronPlanDestCrontabHash(currentCrontab)

	// Stale-crontab guard: refuse when the crontab changed since plan.
	if plan.PlanTimeDestCrontab != "" && plan.PlanTimeDestCrontab != currentHash {
		fmt.Fprintln(os.Stderr, "refused: the destination crontab changed since the plan was built — re-plan")
		// Refuse all create ops.
		for i, op := range plan.Ops {
			if op.Action == accountinventory.CronActionCreate {
				idx := createIndices[i]
				results[idx].Status = accountinventory.CronOpRefused
				results[idx].StatusReason = "crontab changed since plan (stale-plan guard)"
			}
		}
		report.BackupNote = "no write was attempted (stale-plan guard refused all creates)"
		report.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
		report.Results = results
		report.Summary = accountinventory.SummarizeCronResults(results)
		return finishCronReport(report, outJSON, outMD)
	}

	// Build merged crontab: current content + create ops' lines.
	// Env lines go first (before the job lines in the appended block).
	var envLines, jobLines []string
	for _, op := range createOps {
		switch op.Section {
		case accountinventory.CronSectionEnv:
			envLines = append(envLines, op.Line)
		case accountinventory.CronSectionJobs:
			jobLines = append(jobLines, op.Line)
		}
	}

	merged := currentCrontab
	if !strings.HasSuffix(merged, "\n") && merged != "" {
		merged += "\n"
	}
	for _, l := range envLines {
		merged += l + "\n"
	}
	for _, l := range jobLines {
		merged += l + "\n"
	}

	// Backup-or-nothing: written BEFORE the write.
	backupPath := backupFlag
	if backupPath == "" {
		backupPath = filepath.Join(filepath.Dir(outJSON),
			fmt.Sprintf("cron_backup_%s_%s.json", plan.DestinationUser, time.Now().UTC().Format("20060102-150405")))
	}
	backup := accountinventory.CronApplyBackup{
		Mode: "cron-apply-backup", FormatVersion: 1,
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		DestinationUser: plan.DestinationUser,
		PlanFile:        planPath, PlanSHA256: planSHA,
		ReportFile:    outJSON,
		RawCrontab:    currentCrontab,
		CrontabSHA256: currentHash,
	}
	if err := accountinventory.WriteCronApplyBackupJSON(backupPath, backup); err != nil {
		fmt.Fprintln(os.Stderr, "error: backup-or-nothing — backup write failed, NOTHING was written:", err)
		return 1
	}
	backupSHA, err := fileSHA256(backupPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: backup-or-nothing — cannot hash the backup, NOTHING was written:", err)
		return 1
	}
	report.BackupFile, report.BackupSHA256 = backupPath, backupSHA
	fmt.Fprintf(os.Stderr, "wrote %s (pre-write backup)\n", backupPath)

	// Install the merged crontab.
	if err := cpanel.InstallCrontab(ctx, client, merged); err != nil {
		fmt.Fprintln(os.Stderr, "error: install crontab:", err)
		for i, op := range plan.Ops {
			if op.Action == accountinventory.CronActionCreate {
				idx := createIndices[i]
				results[idx].Status = accountinventory.CronOpFailed
				results[idx].StatusReason = "install failed: " + err.Error()
			}
		}
		report.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
		report.Results = results
		report.Summary = accountinventory.SummarizeCronResults(results)
		return finishCronReport(report, outJSON, outMD)
	}

	// Verify-after: re-read and check installed lines are present.
	afterCrontab, err := cpanel.ReadCrontabRaw(ctx, client)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: verify-after read failed:", err)
		for i, op := range plan.Ops {
			if op.Action == accountinventory.CronActionCreate {
				idx := createIndices[i]
				results[idx].Status = accountinventory.CronOpFailed
				results[idx].StatusReason = "verify-after read failed: " + err.Error()
			}
		}
	} else {
		report.InstalledCrontab = afterCrontab
		for i, op := range plan.Ops {
			if op.Action != accountinventory.CronActionCreate {
				continue
			}
			idx := createIndices[i]
			if strings.Contains(afterCrontab, op.Line) {
				results[idx].Status = accountinventory.CronOpApplied
			} else {
				results[idx].Status = accountinventory.CronOpFailed
				results[idx].StatusReason = "install reported success but the line is not in the post-install crontab"
			}
		}
	}

	report.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	report.Results = results
	report.Summary = accountinventory.SummarizeCronResults(results)
	return finishCronReport(report, outJSON, outMD)
}

// runCronRollback restores the crontab from a backup.
func runCronRollback(backupPath string, yes bool, cfgFlag, outJSON, outMD string) int {
	backup, err := loadCronBackupFile(backupPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if backup.FormatVersion != 1 {
		fmt.Fprintf(os.Stderr, "error: %s: unsupported backup format_version %d\n", backupPath, backup.FormatVersion)
		return 1
	}

	if !yes {
		fmt.Println("cron rollback — DRY-RUN (fully offline: no connection was opened, nothing was written).")
		fmt.Printf("  would restore crontab from %s (%d bytes, sha256 %s)\n",
			backupPath, len(backup.RawCrontab), backup.CrontabSHA256)
		fmt.Println("to apply: re-run with --yes-apply-writes")
		return 0
	}

	if outJSON == "" {
		outJSON = "cron_rollback_report.json"
	}
	if outMD == "" {
		outMD = deriveMDPath(outJSON)
	}

	ctx := context.Background()
	client, err := dialCronDest(ctx, cfgFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer func() { _ = client.Close() }()

	report := accountinventory.CronApplyReport{
		Mode: "cron-apply-report", FormatVersion: 1, RunMode: "rollback",
		DestinationUser: backup.DestinationUser,
		BackupFile:      backupPath,
	}
	if report.BackupSHA256, err = fileSHA256(backupPath); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	if err := cpanel.InstallCrontab(ctx, client, backup.RawCrontab); err != nil {
		fmt.Fprintln(os.Stderr, "error: rollback install:", err)
		report.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
		report.Results = []accountinventory.CronApplyOpResult{{
			CronPlanOp:   accountinventory.CronPlanOp{Section: "rollback", Key: "crontab"},
			Status:       accountinventory.CronOpFailed,
			StatusReason: err.Error(),
		}}
		report.Summary = accountinventory.SummarizeCronResults(report.Results)
		return finishCronReport(report, outJSON, outMD)
	}

	// Verify the restore.
	afterCrontab, err := cpanel.ReadCrontabRaw(ctx, client)
	result := accountinventory.CronApplyOpResult{
		CronPlanOp: accountinventory.CronPlanOp{Section: "rollback", Key: "crontab"},
	}
	if err != nil {
		result.Status = accountinventory.CronOpFailed
		result.StatusReason = "verify-after read failed: " + err.Error()
	} else {
		afterHash := crontabSHA256(afterCrontab)
		backupHash := crontabSHA256(backup.RawCrontab)
		if afterHash == backupHash {
			result.Status = accountinventory.CronOpApplied
		} else {
			result.Status = accountinventory.CronOpFailed
			result.StatusReason = "installed crontab does not match the backup content"
		}
	}

	report.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	report.Results = []accountinventory.CronApplyOpResult{result}
	report.Summary = accountinventory.SummarizeCronResults(report.Results)
	return finishCronReport(report, outJSON, outMD)
}

// dialCronDest resolves the config and dials the DESTINATION.
func dialCronDest(ctx context.Context, cfgFlag string) (*sshx.Client, error) {
	path, alternates, err := resolveConfigPath(cfgFlag)
	if err != nil {
		return nil, err
	}
	for _, alt := range alternates {
		fmt.Fprintf(os.Stderr, "note: multiple host.yaml candidates, using %s (ignoring %s)\n", path, alt)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	if !cfg.DestConfigured() {
		return nil, fmt.Errorf("the cron commands need the DESTINATION host configured in %s", path)
	}
	client, err := sshx.DialDest(ctx, cfg, "")
	if err != nil {
		return nil, fmt.Errorf("dial destination: %w", err)
	}
	return client, nil
}

// finishCronReport writes both report artifacts and translates the
// summary into the exit code.
func finishCronReport(report accountinventory.CronApplyReport, outJSON, outMD string) int {
	if err := accountinventory.WriteCronApplyReportJSON(outJSON, report); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := accountinventory.WriteCronApplyReportMarkdown(outMD, report); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	s := report.Summary
	fmt.Printf("cron %s: %d applied, %d skipped, %d manual, %d failed, %d refused (precondition)\n",
		report.RunMode, s.Applied, s.Skipped, s.Manual, s.Failed, s.Refused)
	fmt.Fprintf(os.Stderr, "wrote %s\nwrote %s\n", outJSON, outMD)
	switch {
	case s.Failed > 0:
		fmt.Fprintln(os.Stderr, "one or more ops FAILED — exiting 1 (see the report)")
		return 1
	case s.Refused > 0:
		fmt.Fprintln(os.Stderr, "one or more ops were refused by the freshness guard — exiting 3 (re-plan and review)")
		return exitDriftGate
	}
	return 0
}

// loadCronPlanFile reads and minimally validates a cron apply plan.
func loadCronPlanFile(path string) (accountinventory.CronApplyPlan, error) {
	var p accountinventory.CronApplyPlan
	b, err := os.ReadFile(path) // #nosec G304 -- user-supplied CLI input path, read-only
	if err != nil {
		return p, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("parse %s: %w", path, err)
	}
	if p.Mode != "cron-apply-plan" {
		return p, fmt.Errorf("%s: not a cron apply plan (mode %q)", path, p.Mode)
	}
	return p, nil
}

// loadCronBackupFile reads and minimally validates a cron apply backup.
func loadCronBackupFile(path string) (accountinventory.CronApplyBackup, error) {
	var b accountinventory.CronApplyBackup
	raw, err := os.ReadFile(path) // #nosec G304 -- user-supplied CLI input path, read-only
	if err != nil {
		return b, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return b, fmt.Errorf("parse %s: %w", path, err)
	}
	if b.Mode != "cron-apply-backup" {
		return b, fmt.Errorf("%s: not a cron apply backup (mode %q)", path, b.Mode)
	}
	return b, nil
}

// crontabSHA256 returns the hex SHA256 of a crontab string.
func crontabSHA256(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}
