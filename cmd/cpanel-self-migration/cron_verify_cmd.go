package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
)

// CronVerifyOpResult is the per-op outcome of a cron verify run.
type CronVerifyOpResult struct {
	accountinventory.CronPlanOp
	Status       string `json:"status"`
	StatusReason string `json:"status_reason,omitempty"`
}

// CronVerifySummary counts verify results.
type CronVerifySummary struct {
	Applied    int `json:"applied"`
	Unchanged  int `json:"unchanged"`
	Pending    int `json:"pending"`
	Drift      int `json:"drift"`
	Manual     int `json:"manual"`
	NotChecked int `json:"not_checked"`
}

// CronVerifyReport records what a `cron verify` run found.
type CronVerifyReport struct {
	Mode          string               `json:"mode"` // "cron-verify-report"
	FormatVersion int                  `json:"format_version"`
	GeneratedAt   string               `json:"generated_at"`
	PlanFile      string               `json:"plan_file"`
	PlanSHA256    string               `json:"plan_sha256"`
	Clean         bool                 `json:"clean"`
	Results       []CronVerifyOpResult `json:"results"`
	Summary       CronVerifySummary    `json:"summary"`
}

// runCronVerifyCmd implements `cpanel-self-migration cron verify`: re-read
// the DESTINATION crontab and report, per planned op, whether the live
// crontab matches the plan (mirror of dns verify / email verify). The
// source host is never dialed.
//
// Exit codes: 0 verify ran and reports were written (even with drift);
// 1 invalid input, config/SSH-dial failure or write failure; 2 flag
// parsing; 3 gated refusal (stale plan, or --fail-on-drift + not clean).
func runCronVerifyCmd(args []string) int {
	fs := flag.NewFlagSet("cron verify", flag.ContinueOnError)
	planPath := fs.String("plan", "", "path to cron_apply_plan.json (required)")
	cfgFlag := fs.String("config", "", "path to host.yaml (default: configs/host.yaml or host.yaml)")
	srcInv := fs.String("source", "", "optional source inventory JSON: refuse the plan unless its embedded source_sha256 matches this file")
	destInv := fs.String("destination", "", "optional destination inventory JSON: refuse the plan unless its embedded destination_sha256 matches this file")
	outJSON := fs.String("output-json", "cron_verify_report.json", "verify report JSON output path")
	outMD := fs.String("output-md", "cron_verify_report.md", "verify report Markdown output path")
	failOnDrift := fs.Bool("fail-on-drift", false, "exit 3 unless the verify verdict is clean")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration cron verify --plan cron_apply_plan.json [--config host.yaml] [--source src.json] [--destination dest.json] [--fail-on-drift]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
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

	// Stale-plan gate (before any SSH).
	for _, in := range []struct{ flagName, path, embedded string }{
		{"--source", *srcInv, plan.SourceSHA256},
		{"--destination", *destInv, plan.DestinationSHA256},
	} {
		if in.path == "" {
			continue
		}
		sum, err := fileSHA256(in.path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		if in.embedded == "" {
			fmt.Fprintf(os.Stderr, "refused: the plan embeds no sha256 for %s — cannot prove it was built from %s (rebuild the plan)\n", in.flagName, in.path)
			return exitDriftGate
		}
		if sum != in.embedded {
			fmt.Fprintf(os.Stderr, "refused: stale plan — %s %s hashes to %s but the plan was built from %s\n", in.flagName, in.path, sum, in.embedded)
			return exitDriftGate
		}
	}

	planSHA, err := fileSHA256(*planPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	// Read live crontab — only when there are ops to verify. A
	// manual-only plan needs no config and opens no SSH.
	var liveCrontab string
	hasOps := false
	for _, op := range plan.Ops {
		if op.Action == accountinventory.CronActionCreate || op.Action == accountinventory.CronActionSkip {
			hasOps = true
			break
		}
	}

	if hasOps {
		ctx := context.Background()
		client, err := dialCronDest(ctx, *cfgFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		defer func() { _ = client.Close() }()
		liveCrontab, err = cpanel.ReadCrontabRaw(ctx, client)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: read crontab:", err)
			return 1
		}
	}

	// Verify each op against the live crontab.
	rep := verifyCronPlan(plan, liveCrontab)
	rep.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	rep.PlanFile = *planPath
	rep.PlanSHA256 = planSHA

	if err := writeCronVerifyJSON(*outJSON, rep); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := writeCronVerifyMarkdown(*outMD, rep); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	verdict := "CLEAN"
	if !rep.Clean {
		verdict = "NOT CLEAN"
	}
	fmt.Printf("cron verify: %s — %d applied, %d unchanged, %d pending, %d drift, %d manual, %d not checked\n",
		verdict, rep.Summary.Applied, rep.Summary.Unchanged, rep.Summary.Pending,
		rep.Summary.Drift, rep.Summary.Manual, rep.Summary.NotChecked)
	fmt.Fprintf(os.Stderr, "wrote %s\n", *outJSON)
	fmt.Fprintf(os.Stderr, "wrote %s\n", *outMD)

	if *failOnDrift && !rep.Clean {
		fmt.Fprintln(os.Stderr, "verdict not clean and --fail-on-drift is set — exiting 3")
		return exitDriftGate
	}
	return 0
}

// verifyCronPlan checks each plan op against the live crontab content.
func verifyCronPlan(plan accountinventory.CronApplyPlan, liveCrontab string) CronVerifyReport {
	rep := CronVerifyReport{
		Mode:          "cron-verify-report",
		FormatVersion: 1,
		Clean:         true,
	}

	for _, op := range plan.Ops {
		res := CronVerifyOpResult{CronPlanOp: op}
		switch op.Action {
		case accountinventory.CronActionCreate:
			if op.Line != "" && strings.Contains(liveCrontab, op.Line) {
				res.Status = "applied"
				rep.Summary.Applied++
			} else {
				res.Status = "pending"
				res.StatusReason = "create op line not found in the live crontab"
				rep.Summary.Pending++
				rep.Clean = false
			}
		case accountinventory.CronActionSkip:
			if op.Line != "" && strings.Contains(liveCrontab, op.Line) {
				res.Status = "unchanged"
				rep.Summary.Unchanged++
			} else {
				res.Status = "drift"
				res.StatusReason = "skip op line no longer present in the live crontab"
				rep.Summary.Drift++
				rep.Clean = false
			}
		case accountinventory.CronActionManual:
			res.Status = "manual"
			rep.Summary.Manual++
			rep.Clean = false
		default:
			res.Status = "not_checked"
			rep.Summary.NotChecked++
		}
		rep.Results = append(rep.Results, res)
	}

	return rep
}

// writeCronVerifyJSON writes the machine-readable verify report.
func writeCronVerifyJSON(path string, rep CronVerifyReport) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cron verify report: %w", err)
	}
	b = append(b, '\n')
	dir := strings.TrimRight(path, "/")
	if i := strings.LastIndex(dir, "/"); i >= 0 {
		if err := os.MkdirAll(dir[:i], 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, b, 0o600)
}

// writeCronVerifyMarkdown writes the human-readable verify report.
func writeCronVerifyMarkdown(path string, rep CronVerifyReport) error {
	dir := strings.TrimRight(path, "/")
	if i := strings.LastIndex(dir, "/"); i >= 0 {
		if err := os.MkdirAll(dir[:i], 0o755); err != nil {
			return err
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Cron Verify Report\n\n")
	fmt.Fprintf(&sb, "- **Plan**: %s (sha256 %s)\n", rep.PlanFile, rep.PlanSHA256)
	fmt.Fprintf(&sb, "- **Generated**: %s\n", rep.GeneratedAt)
	verdict := "CLEAN"
	if !rep.Clean {
		verdict = "NOT CLEAN"
	}
	fmt.Fprintf(&sb, "- **Verdict**: %s\n\n", verdict)
	fmt.Fprintf(&sb, "**Summary**: %d applied, %d unchanged, %d pending, %d drift, %d manual, %d not checked\n\n",
		rep.Summary.Applied, rep.Summary.Unchanged, rep.Summary.Pending,
		rep.Summary.Drift, rep.Summary.Manual, rep.Summary.NotChecked)

	if len(rep.Results) == 0 {
		sb.WriteString("No cron ops to verify.\n")
		return os.WriteFile(path, []byte(sb.String()), 0o600)
	}

	sb.WriteString("| Section | Key | Status | Note |\n|---------|-----|--------|------|\n")
	for _, res := range rep.Results {
		note := res.StatusReason
		if note == "" {
			note = res.Reason
		}
		fmt.Fprintf(&sb, "| %s | %s | %s | %s |\n",
			res.Section, res.Key, res.Status, note)
	}
	sb.WriteString("\n")

	return os.WriteFile(path, []byte(sb.String()), 0o600)
}
