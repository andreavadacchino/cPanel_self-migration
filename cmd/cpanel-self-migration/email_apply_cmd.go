package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// runEmailApplyCmd implements `cpanel-self-migration email apply`: the
// FIRST config writer of the tool (PR 2B-1). It consumes an offline
// email_apply_plan.json and writes forwarders / default (catch-all)
// addresses onto the DESTINATION account only (sshx.DialDest — the source
// is never dialed, let alone written).
//
// House contract (docs/dev/PR2B_EMAIL_APPLY_DESIGN.md):
//   - without --yes-apply-writes: fully offline preview, ZERO connections;
//   - backup-or-nothing before the first write, bidirectionally paired
//     with the report;
//   - per-op freshness guard against a fresh re-list (already_present /
//     refused_precondition, no blanket section abort);
//   - unconditional per-op verify-after;
//   - --rollback <backup>: report-driven inverse ops, deletes ONLY the
//     tool's own applied creates.
//
// Exit codes: 0 ok; 1 input/runtime/write failure (report still written
// when the run got that far); 2 flags; 3 gated refusal (one or more
// refused_precondition ops, or a refused rollback item).
func runEmailApplyCmd(args []string) int {
	fs := flag.NewFlagSet("email apply", flag.ContinueOnError)
	planPath := fs.String("plan", "", "path to email_apply_plan.json (required unless --rollback)")
	cfgFlag := fs.String("config", "", "path to host.yaml (default: configs/host.yaml or host.yaml)")
	yes := fs.Bool("yes-apply-writes", false, "actually write to the DESTINATION (default: fully offline preview, zero connections)")
	rollbackPath := fs.String("rollback", "", "path to an email apply backup JSON: roll back that run instead of applying")
	reportFlag := fs.String("report", "", "with --rollback: explicit path of the paired apply report (overrides the backup's recorded pairing)")
	acceptReportLoss := fs.Bool("accept-report-loss", false, "with --rollback: proceed WITHOUT the paired report — documented degradation: forwarder rollback becomes MANUAL, only default-address restores run")
	backupFlag := fs.String("backup", "", "pre-write backup path (default: email_backup_<account>_<timestamp>.json in the report directory)")
	outJSON := fs.String("output-json", "", "report JSON path (default: email_apply_report.json, or email_rollback_report.json with --rollback)")
	outMD := fs.String("output-md", "", "report Markdown path (default: derived from --output-json)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration email apply --plan email_apply_plan.json [--yes-apply-writes] [--config host.yaml] [--backup PATH]")
		fmt.Fprintln(os.Stderr, "       cpanel-self-migration email apply --rollback email_backup_….json [--report REPORT.json|--accept-report-loss] [--yes-apply-writes]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *rollbackPath != "" {
		if *planPath != "" {
			fmt.Fprintln(os.Stderr, "error: --plan and --rollback are mutually exclusive (the rollback is driven by the backup+report pair)")
			return 2
		}
		return runEmailRollback(*rollbackPath, *reportFlag, *acceptReportLoss, *yes, *cfgFlag, *outJSON, *outMD)
	}
	if *reportFlag != "" || *acceptReportLoss {
		fmt.Fprintln(os.Stderr, "error: --report/--accept-report-loss only apply to --rollback")
		return 2
	}
	if *planPath == "" {
		fmt.Fprintln(os.Stderr, "error: --plan is required")
		fs.Usage()
		return 1
	}

	plan, err := loadEmailPlanFile(*planPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if plan.FormatVersion != 1 {
		fmt.Fprintf(os.Stderr, "error: %s: unsupported plan format_version %d (this build understands 1)\n", *planPath, plan.FormatVersion)
		return 1
	}

	if !*yes {
		printEmailApplyDryRun(plan)
		return 0
	}
	return runEmailApplyWrites(plan, *planPath, *cfgFlag, *backupFlag, *outJSON, *outMD)
}

// printEmailApplyDryRun is the offline preview: no config, no SSH, no
// artifact files — safe to run anywhere (house posture for writers).
func printEmailApplyDryRun(plan accountinventory.EmailApplyPlan) {
	fmt.Println("email apply — DRY-RUN (fully offline: no connection was opened, nothing was written).")
	fmt.Println("NOTE: this preview renders the PLAN-recorded destination state, not the live one —")
	fmt.Println("an op shown as `create` may resolve to `already_present` at apply time; the live")
	fmt.Println("preview is `email verify`.")
	fmt.Println()
	writes := 0
	for _, op := range plan.Ops {
		switch op.Action {
		case accountinventory.EmailActionCreate:
			fmt.Printf("  create forwarder  %s → %s\n", op.Key, op.Forward)
			writes++
		case accountinventory.EmailActionSet:
			fmt.Printf("  set default addr  %s → %s  (plan-time dest: %q)\n", op.Domain, op.Value, op.DestinationValue)
			writes++
		}
	}
	if writes == 0 {
		fmt.Println("  (no writable ops in this plan)")
	}
	fmt.Printf("\nplan summary: %d create, %d set, %d skip, %d manual, %d informational\n",
		plan.Summary.Create, plan.Summary.Set, plan.Summary.Skip, plan.Summary.Manual, plan.Summary.Informational)
	fmt.Println("to apply: re-run with --yes-apply-writes")
}

// dialEmailDest resolves the config and dials the DESTINATION.
func dialEmailDest(ctx context.Context, cfgFlag string) (*sshx.Client, error) {
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
		return nil, fmt.Errorf("the email commands need the DESTINATION host configured in %s", path)
	}
	client, err := sshx.DialDest(ctx, cfg, "")
	if err != nil {
		return nil, fmt.Errorf("dial destination: %w", err)
	}
	return client, nil
}

// fetchEmailLiveState re-lists the touched sections on the destination
// (fresh state for the per-op guard) and, when collectRaw is true, also
// archives the verbatim responses for the backup.
func fetchEmailLiveState(ctx context.Context, client cpanel.Runner, domains []string, needDefaults bool) (accountinventory.EmailLiveState, map[string]accountinventory.EmailBackupSection, *accountinventory.EmailBackupSection) {
	live := accountinventory.EmailLiveState{
		ForwardersByDomain:  map[string][]accountinventory.ForwarderEntry{},
		ForwarderListErrors: map[string]string{},
	}
	fwdBackup := map[string]accountinventory.EmailBackupSection{}
	for _, d := range domains {
		entries, raw, err := cpanel.ListForwardersWithRaw(ctx, client, d)
		if err != nil {
			live.ForwarderListErrors[d] = err.Error()
			continue
		}
		live.ForwardersByDomain[d] = normalizeForwarders(entries, d)
		fwdBackup[d] = accountinventory.EmailBackupSection{
			RawUAPIResponse: json.RawMessage(raw),
			Forwarders:      live.ForwardersByDomain[d],
		}
	}
	var defBackup *accountinventory.EmailBackupSection
	if needDefaults {
		entries, raw, err := cpanel.ListDefaultAddressesWithRaw(ctx, client)
		if err != nil {
			live.DefaultsError = err.Error()
		} else {
			live.DefaultsListed = true
			live.Defaults = normalizeDefaults(entries)
			defBackup = &accountinventory.EmailBackupSection{
				RawUAPIResponse: json.RawMessage(raw),
				Defaults:        live.Defaults,
			}
		}
	}
	return live, fwdBackup, defBackup
}

func normalizeForwarders(entries []cpanel.ForwarderEntry, domain string) []accountinventory.ForwarderEntry {
	out := make([]accountinventory.ForwarderEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, accountinventory.ForwarderEntry{Source: e.Dest, Destination: e.Forward, Domain: domain})
	}
	return out
}

func normalizeDefaults(entries []cpanel.DefaultAddressEntry) []accountinventory.DefaultAddressEntry {
	out := make([]accountinventory.DefaultAddressEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, accountinventory.DefaultAddressEntry{Domain: e.Domain, DefaultAddress: e.DefaultAddress})
	}
	return out
}

// runEmailApplyWrites is the write path: fresh re-list → per-op guard →
// backup-or-nothing → writes with unconditional per-op verify-after →
// paired report. The report is always written once the run reaches the
// evaluation stage — never sacrificed to the exit code.
func runEmailApplyWrites(plan accountinventory.EmailApplyPlan, planPath, cfgFlag, backupFlag, outJSON, outMD string) int {
	planSHA, err := fileSHA256(planPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if outJSON == "" {
		outJSON = "email_apply_report.json"
	}
	if outMD == "" {
		outMD = deriveMDPath(outJSON)
	}

	var createDomains []string
	seenDomain := map[string]bool{}
	needDefaults := false
	for _, op := range plan.Ops {
		switch {
		case op.Section == accountinventory.EmailSectionForwarders && op.Action == accountinventory.EmailActionCreate:
			if !seenDomain[op.Domain] {
				seenDomain[op.Domain] = true
				createDomains = append(createDomains, op.Domain)
			}
		case op.Section == accountinventory.EmailSectionDefaultAddress && op.Action == accountinventory.EmailActionSet:
			needDefaults = true
		}
	}
	sort.Strings(createDomains)

	ctx := context.Background()
	var client *sshx.Client
	if len(createDomains) > 0 || needDefaults {
		client, err = dialEmailDest(ctx, cfgFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		defer func() { _ = client.Close() }()
	}

	live, fwdBackup, defBackup := fetchEmailLiveState(ctx, client, createDomains, needDefaults)

	// Evaluate every actionable op against the fresh state.
	type pendingWrite struct {
		op  accountinventory.EmailPlanOp
		idx int
	}
	results := make([]accountinventory.EmailOpResult, 0, len(plan.Ops))
	var writes []pendingWrite
	for _, op := range plan.Ops {
		res := accountinventory.EmailOpResult{EmailPlanOp: op}
		switch op.Action {
		case accountinventory.EmailActionSkip:
			res.Status = accountinventory.EmailOpSkipped
		case accountinventory.EmailActionManual:
			res.Status = accountinventory.EmailOpManual
		case accountinventory.EmailActionCreate, accountinventory.EmailActionSet:
			decision, reason := accountinventory.EvaluateEmailOp(op, live, plan.DestinationUser)
			switch decision {
			case accountinventory.EmailDecisionAlready:
				res.Status = accountinventory.EmailOpAlready
			case accountinventory.EmailDecisionRefused:
				res.Status = accountinventory.EmailOpRefused
				res.StatusReason = reason
			case accountinventory.EmailDecisionWrite:
				res.Status = accountinventory.EmailOpPlanned // upgraded below
				writes = append(writes, pendingWrite{op: op, idx: len(results)})
			}
		default:
			res.Status = accountinventory.EmailOpRefused
			res.StatusReason = fmt.Sprintf("unknown plan action %q — malformed or hand-edited plan", op.Action)
		}
		results = append(results, res)
	}

	report := accountinventory.EmailApplyReport{
		Mode: "email-apply-report", FormatVersion: 1, RunMode: "apply",
		DestinationUser: plan.DestinationUser,
		PlanFile:        planPath, PlanSHA256: planSHA,
	}

	// Backup-or-nothing: written BEFORE the first write, recording the
	// paired report path; a backup failure aborts with zero writes.
	if len(writes) > 0 {
		backupPath := backupFlag
		if backupPath == "" {
			backupPath = filepath.Join(filepath.Dir(outJSON),
				fmt.Sprintf("email_backup_%s_%s.json", plan.DestinationUser, time.Now().UTC().Format("20060102-150405")))
		}
		backup := accountinventory.EmailBackup{
			Mode: "email-apply-backup", FormatVersion: 1,
			GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
			DestinationUser: plan.DestinationUser,
			PlanFile:        planPath, PlanSHA256: planSHA,
			ReportFile:         outJSON,
			ForwardersByDomain: fwdBackup,
			DefaultAddresses:   defBackup,
		}
		if err := accountinventory.WriteEmailBackupJSON(backupPath, backup); err != nil {
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
	} else {
		report.BackupNote = "no write was decided (every op skipped, manual, already present or refused) — nothing to back up"
	}

	// Execute the writes, each followed by its unconditional verify-after
	// (fresh section re-list; `applied` only if the outcome is observable).
	for _, w := range writes {
		op := w.op
		var writeErr error
		switch op.Action {
		case accountinventory.EmailActionCreate:
			writeErr = cpanel.AddForwarder(ctx, client, op.Domain, op.Email, op.Forward)
		case accountinventory.EmailActionSet:
			writeErr = cpanel.SetDefaultAddress(ctx, client, op.Domain, op.Value)
		}
		if writeErr != nil {
			results[w.idx].Status = accountinventory.EmailOpFailed
			results[w.idx].StatusReason = writeErr.Error()
			continue
		}
		fresh, verifyErr := refetchEmailSection(ctx, client, op)
		switch {
		case verifyErr != nil:
			results[w.idx].Status = accountinventory.EmailOpFailed
			results[w.idx].StatusReason = "verify-after re-list failed: " + verifyErr.Error()
		case accountinventory.EmailOutcomePresent(op, fresh, plan.DestinationUser):
			results[w.idx].Status = accountinventory.EmailOpApplied
		default:
			results[w.idx].Status = accountinventory.EmailOpFailed
			results[w.idx].StatusReason = "write reported success but the outcome is not observable in the fresh re-list"
		}
	}

	report.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	report.Results = results
	report.Summary = accountinventory.SummarizeEmailResults(results)
	return finishEmailReport(report, outJSON, outMD)
}

// refetchEmailSection re-lists just the section one op touched, for its
// verify-after.
func refetchEmailSection(ctx context.Context, client cpanel.Runner, op accountinventory.EmailPlanOp) (accountinventory.EmailLiveState, error) {
	live := accountinventory.EmailLiveState{
		ForwardersByDomain:  map[string][]accountinventory.ForwarderEntry{},
		ForwarderListErrors: map[string]string{},
	}
	switch op.Section {
	case accountinventory.EmailSectionForwarders:
		entries, err := cpanel.ListForwarders(ctx, client, op.Domain)
		if err != nil {
			return live, err
		}
		live.ForwardersByDomain[op.Domain] = normalizeForwarders(entries, op.Domain)
	case accountinventory.EmailSectionDefaultAddress:
		entries, err := cpanel.ListDefaultAddresses(ctx, client)
		if err != nil {
			return live, err
		}
		live.DefaultsListed = true
		live.Defaults = normalizeDefaults(entries)
	}
	return live, nil
}

// finishEmailReport writes both report artifacts and translates the
// summary into the exit code (reports are never sacrificed to it).
func finishEmailReport(report accountinventory.EmailApplyReport, outJSON, outMD string) int {
	if err := accountinventory.WriteEmailApplyReportJSON(outJSON, report); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := accountinventory.WriteEmailApplyReportMarkdown(outMD, report); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	s := report.Summary
	fmt.Printf("email %s: %d applied, %d already present, %d refused (precondition), %d failed, %d skipped, %d manual\n",
		report.RunMode, s.Applied, s.AlreadyPresent, s.Refused, s.Failed, s.Skipped, s.Manual)
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

func deriveMDPath(jsonPath string) string {
	if strings.HasSuffix(jsonPath, ".json") {
		return strings.TrimSuffix(jsonPath, ".json") + ".md"
	}
	return jsonPath + ".md"
}

// runEmailRollback drives `email apply --rollback <backup>`: the paired
// REPORT is the required input locating the ops that were ACTUALLY
// applied; --accept-report-loss opts into the documented degradation.
func runEmailRollback(backupPath, reportFlag string, acceptLoss, yes bool, cfgFlag, outJSON, outMD string) int {
	backup, err := loadEmailBackupFile(backupPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if backup.FormatVersion != 1 {
		fmt.Fprintf(os.Stderr, "error: %s: unsupported backup format_version %d\n", backupPath, backup.FormatVersion)
		return 1
	}
	if outJSON == "" {
		outJSON = "email_rollback_report.json"
	}
	if outMD == "" {
		outMD = deriveMDPath(outJSON)
	}

	reportPath := reportFlag
	if reportPath == "" {
		reportPath = backup.ReportFile
	}

	var ops []accountinventory.EmailRollbackOp
	var manualNotes []string
	applyReport, reportErr := loadEmailReportFile(reportPath)
	switch {
	case reportErr == nil:
		ops, err = accountinventory.ComputeEmailRollback(applyReport, backup)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	case acceptLoss:
		fmt.Fprintf(os.Stderr, "warning: paired report unavailable (%v) — DEGRADED rollback: forwarder rollback is MANUAL, only default-address restores run\n", reportErr)
		ops, manualNotes = accountinventory.ComputeEmailRollbackDegraded(backup)
	default:
		fmt.Fprintf(os.Stderr, "error: the paired apply report is a REQUIRED rollback input and could not be read (%v).\n", reportErr)
		fmt.Fprintln(os.Stderr, "Pass --report <path> if it lives elsewhere, or --accept-report-loss for the documented degradation (forwarders become manual).")
		return 1
	}

	if !yes {
		fmt.Println("email rollback — DRY-RUN (fully offline: no connection was opened, nothing was written).")
		for _, op := range ops {
			switch op.Kind {
			case accountinventory.EmailRollbackForwarderRemove:
				fmt.Printf("  remove forwarder  %s → %s (the tool's own applied create)\n", op.Address, op.Forwarder)
			case accountinventory.EmailRollbackDefaultRestore:
				fmt.Printf("  restore default   %s → %q (backup value)\n", op.Domain, op.Value)
			}
		}
		if len(ops) == 0 {
			fmt.Println("  (nothing to roll back: no applied ops in the report)")
		}
		for _, n := range manualNotes {
			fmt.Println("  MANUAL:", n)
		}
		fmt.Println("to roll back: re-run with --yes-apply-writes")
		return 0
	}

	ctx := context.Background()
	var client *sshx.Client
	if len(ops) > 0 {
		client, err = dialEmailDest(ctx, cfgFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		defer func() { _ = client.Close() }()
	}

	results := make([]accountinventory.EmailOpResult, 0, len(ops))
	for _, op := range ops {
		results = append(results, executeEmailRollbackOp(ctx, client, op, backup.DestinationUser))
	}
	for _, n := range manualNotes {
		results = append(results, accountinventory.EmailOpResult{
			EmailPlanOp: accountinventory.EmailPlanOp{
				Section: accountinventory.EmailSectionForwarders,
				Action:  accountinventory.EmailActionManual,
				Reason:  n,
			},
			Status: accountinventory.EmailOpManual,
		})
	}

	report := accountinventory.EmailApplyReport{
		Mode: "email-apply-report", FormatVersion: 1, RunMode: "rollback",
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		DestinationUser: backup.DestinationUser,
		BackupFile:      backupPath,
		Results:         results,
		Summary:         accountinventory.SummarizeEmailResults(results),
	}
	if sha, err := fileSHA256(backupPath); err == nil {
		report.BackupSHA256 = sha
	}
	return finishEmailReport(report, outJSON, outMD)
}

// executeEmailRollbackOp pre-checks one inverse op against the live state
// (rollback refuses an item whose current state diverged from the
// post-apply state), executes it, and verify-afters it.
func executeEmailRollbackOp(ctx context.Context, client cpanel.Runner, op accountinventory.EmailRollbackOp, destUser string) accountinventory.EmailOpResult {
	res := accountinventory.EmailOpResult{EmailPlanOp: accountinventory.EmailPlanOp{
		Section: accountinventory.EmailSectionForwarders,
		Action:  op.Kind,
		Domain:  op.Domain,
		Key:     op.Address,
	}}
	switch op.Kind {
	case accountinventory.EmailRollbackForwarderRemove:
		res.Forward = op.Forwarder
		entries, err := cpanel.ListForwarders(ctx, client, op.Domain)
		if err != nil {
			res.Status, res.StatusReason = accountinventory.EmailOpFailed, "pre-check re-list failed: "+err.Error()
			return res
		}
		if !cpanel.ForwarderExists(entries, op.Address, op.Forwarder) {
			res.Status = accountinventory.EmailOpAlready
			res.StatusReason = "pair already absent — nothing to remove"
			return res
		}
		if err := cpanel.DeleteForwarder(ctx, client, op.Address, op.Forwarder); err != nil {
			res.Status, res.StatusReason = accountinventory.EmailOpFailed, err.Error()
			return res
		}
		entries, err = cpanel.ListForwarders(ctx, client, op.Domain)
		switch {
		case err != nil:
			res.Status, res.StatusReason = accountinventory.EmailOpFailed, "verify-after re-list failed: "+err.Error()
		case cpanel.ForwarderExists(entries, op.Address, op.Forwarder):
			res.Status, res.StatusReason = accountinventory.EmailOpFailed, "delete reported success but the pair is still live"
		default:
			res.Status = accountinventory.EmailOpApplied
		}
		return res

	case accountinventory.EmailRollbackDefaultRestore:
		res.Section = accountinventory.EmailSectionDefaultAddress
		res.Key = op.Domain
		res.Value = op.Value
		entries, err := cpanel.ListDefaultAddresses(ctx, client)
		if err != nil {
			res.Status, res.StatusReason = accountinventory.EmailOpFailed, "pre-check re-list failed: "+err.Error()
			return res
		}
		var current string
		found := false
		for _, e := range entries {
			if strings.EqualFold(strings.TrimSpace(e.Domain), op.Domain) {
				current, found = e.DefaultAddress, true
				break
			}
		}
		if !found {
			res.Status, res.StatusReason = accountinventory.EmailOpRefused, "domain no longer appears in the destination default-address list"
			return res
		}
		if accountinventory.DefaultValuesEquivalent(op.Value, current, destUser) {
			res.Status = accountinventory.EmailOpAlready
			res.StatusReason = "default already carries the backup value"
			return res
		}
		if op.ExpectedCurrent != "" && !accountinventory.DefaultValuesEquivalent(op.ExpectedCurrent, current, destUser) {
			res.Status = accountinventory.EmailOpRefused
			res.StatusReason = fmt.Sprintf("current default %q diverged from the post-apply state %q — a human changed it since; resolve explicitly", current, op.ExpectedCurrent)
			return res
		}
		if err := cpanel.SetDefaultAddress(ctx, client, op.Domain, op.Value); err != nil {
			res.Status, res.StatusReason = accountinventory.EmailOpFailed, err.Error()
			return res
		}
		entries, err = cpanel.ListDefaultAddresses(ctx, client)
		if err != nil {
			res.Status, res.StatusReason = accountinventory.EmailOpFailed, "verify-after re-list failed: "+err.Error()
			return res
		}
		for _, e := range entries {
			if strings.EqualFold(strings.TrimSpace(e.Domain), op.Domain) &&
				accountinventory.DefaultValuesEquivalent(op.Value, e.DefaultAddress, destUser) {
				res.Status = accountinventory.EmailOpApplied
				return res
			}
		}
		res.Status, res.StatusReason = accountinventory.EmailOpFailed, "restore reported success but the value is not observable in the fresh re-list"
		return res
	}
	res.Status, res.StatusReason = accountinventory.EmailOpFailed, "unknown rollback op kind "+op.Kind
	return res
}

// loadEmailPlanFile reads and minimally validates an email apply plan.
func loadEmailPlanFile(path string) (accountinventory.EmailApplyPlan, error) {
	var p accountinventory.EmailApplyPlan
	b, err := os.ReadFile(path) // #nosec G304 -- user-supplied CLI input path, read-only
	if err != nil {
		return p, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("parse %s: %w", path, err)
	}
	if p.Mode != "email-apply-plan" {
		return p, fmt.Errorf("%s: not an email apply plan (mode %q)", path, p.Mode)
	}
	return p, nil
}

// loadEmailBackupFile reads and minimally validates an email apply backup.
func loadEmailBackupFile(path string) (accountinventory.EmailBackup, error) {
	var b accountinventory.EmailBackup
	raw, err := os.ReadFile(path) // #nosec G304 -- user-supplied CLI input path, read-only
	if err != nil {
		return b, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return b, fmt.Errorf("parse %s: %w", path, err)
	}
	if b.Mode != "email-apply-backup" {
		return b, fmt.Errorf("%s: not an email apply backup (mode %q)", path, b.Mode)
	}
	return b, nil
}

// loadEmailReportFile reads and minimally validates an email apply report.
func loadEmailReportFile(path string) (accountinventory.EmailApplyReport, error) {
	var r accountinventory.EmailApplyReport
	if path == "" {
		return r, fmt.Errorf("the backup records no paired report path")
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- user-supplied CLI input path, read-only
	if err != nil {
		return r, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, fmt.Errorf("parse %s: %w", path, err)
	}
	if r.Mode != "email-apply-report" {
		return r, fmt.Errorf("%s: not an email apply report (mode %q)", path, r.Mode)
	}
	return r, nil
}
