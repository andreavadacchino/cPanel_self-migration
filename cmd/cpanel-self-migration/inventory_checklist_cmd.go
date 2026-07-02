package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

// exitNotReadyGate is the process exit code emitted when
// --fail-on-not-ready is set and the overall status is neither
// READY_TO_CUTOVER nor READY_WITH_MANUAL_NOTES. Same CLI-contract rule as
// exitBlockedGate: tests assert the literal value on purpose.
const exitNotReadyGate = 3

// runInventoryChecklistCmd implements `cpanel-self-migration inventory
// checklist`: a fully offline composition of the pipeline's artifacts into
// the operator-facing migration checklist. It never connects to any server
// and never writes anything but the two report files. Exit codes:
// 0 = checklist generated (manual actions/blockers are findings, not
// process errors), 1 = invalid input or write failure, 2 = unparsable
// flags, 3 = --fail-on-not-ready was set and the overall status is not
// READY_* (reports are still fully written first).
func runInventoryChecklistCmd(args []string) int {
	fs := flag.NewFlagSet("inventory checklist", flag.ContinueOnError)
	source := fs.String("source", "", "path to the source inventory JSON (required)")
	destination := fs.String("destination", "", "path to the destination inventory JSON (required)")
	diffPath := fs.String("diff", "", "path to the inventory_diff.json (required)")
	policyPath := fs.String("policy", "", "path to the policy_report.json (required)")
	planPath := fs.String("dns-plan", "", "optional dns_import_plan.json for DNS expected-difference detection")
	reportPath := fs.String("migration-report", "", "optional report.json from an --apply run, used as migration evidence")
	outJSON := fs.String("output-json", "migration_checklist.json", "path for the machine-readable checklist")
	outMD := fs.String("output-md", "migration_checklist.md", "path for the operator-facing checklist")
	failOnNotReady := fs.Bool("fail-on-not-ready", false, "exit with code 3 unless the overall status is READY_TO_CUTOVER or READY_WITH_MANUAL_NOTES (for CI gating)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration inventory checklist --source SRC.json --destination DEST.json --diff DIFF.json --policy POLICY.json [--dns-plan PLAN.json] [--migration-report REPORT.json] [--output-json PATH] [--output-md PATH] [--fail-on-not-ready]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *source == "" || *destination == "" || *diffPath == "" || *policyPath == "" {
		fmt.Fprintln(os.Stderr, "error: --source, --destination, --diff and --policy are required")
		fs.Usage()
		return 1
	}

	srcInv, err := loadInventoryFile(*source)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	destInv, err := loadInventoryFile(*destination)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	d, err := loadDiffFile(*diffPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	p, err := loadPolicyFile(*policyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	var plan *accountinventory.DNSPlan
	if *planPath != "" {
		pl, err := loadDNSPlanFile(*planPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		plan = &pl
	}
	var report *accountinventory.MigrationReportInfo
	if *reportPath != "" {
		r, err := loadMigrationReportFile(*reportPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		report = &r
	}

	// Input refs (file + raw-byte sha256) are computed BEFORE building the
	// checklist: the engine verifies the provenance chain against them.
	var refs accountinventory.ChecklistInputs
	if refs.SourceInventory, err = checklistInputRef(*source); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if refs.DestinationInventory, err = checklistInputRef(*destination); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if refs.Diff, err = checklistInputRef(*diffPath); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if refs.Policy, err = checklistInputRef(*policyPath); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if *planPath != "" {
		if refs.DNSPlan, err = checklistInputRef(*planPath); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	}
	if *reportPath != "" {
		if refs.MigrationReport, err = checklistInputRef(*reportPath); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	}

	// One single "now": the SSL-validity reference time and the report's
	// own Generated timestamp must agree for auditability.
	now := time.Now().UTC()
	c := accountinventory.BuildChecklist(accountinventory.ChecklistInput{
		Source: srcInv, Destination: destInv, Diff: d, Policy: p,
		DNSPlan: plan, MigrationReport: report,
		InputRefs: refs,
		Now:       now,
	})
	c.GeneratedAt = now.Format(time.RFC3339)

	if err := accountinventory.WriteChecklistJSON(*outJSON, c); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := accountinventory.WriteChecklistMarkdown(*outMD, c); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	blocking := 0
	for _, a := range c.ManualActions {
		if a.BlockingCutover {
			blocking++
		}
	}
	fmt.Printf("migration checklist: %s — %d blocked section(s), %d manual action(s) (%d blocking), %d expected difference(s)\n",
		c.OverallStatus, c.Summary.Blocked, c.Summary.ManualActions, blocking, c.Summary.ExpectedDifferences)
	fmt.Fprintf(os.Stderr, "wrote %s\nwrote %s\n", *outJSON, *outMD)

	if *failOnNotReady &&
		c.OverallStatus != accountinventory.OverallReadyToCutover &&
		c.OverallStatus != accountinventory.OverallReadyWithManualNotes {
		fmt.Fprintf(os.Stderr, "fail-on-not-ready: overall status is %s, exiting %d\n", c.OverallStatus, exitNotReadyGate)
		return exitNotReadyGate
	}
	return 0
}

// checklistInputRef hashes one provided input file (raw bytes, same
// stale-input defense as the DNS plan).
func checklistInputRef(path string) (accountinventory.ChecklistInputRef, error) {
	sum, err := fileSHA256(path)
	if err != nil {
		return accountinventory.ChecklistInputRef{}, err
	}
	return accountinventory.ChecklistInputRef{File: path, SHA256: sum, Present: true}, nil
}

// loadDNSPlanFile reads and minimally validates a DNS import plan JSON.
func loadDNSPlanFile(path string) (accountinventory.DNSPlan, error) {
	var p accountinventory.DNSPlan
	b, err := os.ReadFile(path) // #nosec G304 -- user-supplied CLI input path, read-only
	if err != nil {
		return p, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("parse %s: %w", path, err)
	}
	if p.Mode != "dns-import-plan" {
		return p, fmt.Errorf("%s: not a DNS import plan (mode %q)", path, p.Mode)
	}
	return p, nil
}

// loadMigrationReportFile reads and minimally validates a report.json from
// a migration run. The mode is validated by the checklist engine (a
// dry-run report is accepted as input but ignored as evidence, with a
// warning); here we only reject files that are not run reports at all.
func loadMigrationReportFile(path string) (accountinventory.MigrationReportInfo, error) {
	var r accountinventory.MigrationReportInfo
	b, err := os.ReadFile(path) // #nosec G304 -- user-supplied CLI input path, read-only
	if err != nil {
		return r, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("parse %s: %w", path, err)
	}
	if r.Mode == "" || r.ExitStatus == "" {
		return r, fmt.Errorf("%s: not a migration run report (missing mode/exit_status)", path)
	}
	return r, nil
}
