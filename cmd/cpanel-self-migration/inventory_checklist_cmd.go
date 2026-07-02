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
	acceptancesPath := fs.String("acceptances", "", "optional acceptances.json with operator-accepted manual actions (bound to stable action keys)")
	outJSON := fs.String("output-json", "migration_checklist.json", "path for the machine-readable checklist")
	outMD := fs.String("output-md", "migration_checklist.md", "path for the operator-facing checklist")
	failOnNotReady := fs.Bool("fail-on-not-ready", false, "exit with code 3 unless the overall status is READY_TO_CUTOVER or READY_WITH_MANUAL_NOTES (for CI gating)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration inventory checklist --source SRC.json --destination DEST.json --diff DIFF.json --policy POLICY.json [--dns-plan PLAN.json] [--migration-report REPORT.json] [--acceptances ACC.json] [--output-json PATH] [--output-md PATH] [--fail-on-not-ready]")
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
	var acceptances []accountinventory.OperatorAcceptance
	var acceptanceWarning string
	if *acceptancesPath != "" {
		accs, warning, err := loadAcceptancesFile(*acceptancesPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		acceptances, acceptanceWarning = accs, warning
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
	if *acceptancesPath != "" {
		if refs.Acceptances, err = checklistInputRef(*acceptancesPath); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	}

	// One single "now": the SSL-validity reference time and the report's
	// own Generated timestamp must agree for auditability.
	now := time.Now().UTC()
	c := accountinventory.BuildChecklist(accountinventory.ChecklistInput{
		Source: srcInv, Destination: destInv, Diff: d, Policy: p,
		DNSPlan: plan, MigrationReport: report, Acceptances: acceptances,
		InputRefs: refs,
		Now:       now,
	})
	c.GeneratedAt = now.Format(time.RFC3339)
	if acceptanceWarning != "" {
		c.Warnings = append(c.Warnings, acceptanceWarning)
	}

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

// loadAcceptancesFile reads and validates an acceptances.json. Structural
// problems (wrong mode, missing required fields, bad JSON) are hard errors.
// When the file names the checklist the operator reviewed (checklist_file),
// its sha256 is verified STRICTLY: a mismatch — or an unreadable file —
// rejects every acceptance (returned as a warning with zero entries), never
// silently applies them. The checklist is still generated either way.
func loadAcceptancesFile(path string) ([]accountinventory.OperatorAcceptance, string, error) {
	var f accountinventory.AcceptanceFile
	b, err := os.ReadFile(path) // #nosec G304 -- user-supplied CLI input path, read-only
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, "", fmt.Errorf("parse %s: %w", path, err)
	}
	if f.Mode != accountinventory.AcceptanceFileMode {
		return nil, "", fmt.Errorf("%s: not an operator acceptance file (mode %q)", path, f.Mode)
	}
	if f.ChecklistSHA256 == "" {
		return nil, "", fmt.Errorf("%s: checklist_sha256 is required (the sha256 of the checklist file the operator reviewed)", path)
	}
	for i, a := range f.Acceptances {
		switch {
		case a.ActionKey == "":
			return nil, "", fmt.Errorf("%s: acceptance %d: action_key is required", path, i+1)
		case a.Reason == "":
			return nil, "", fmt.Errorf("%s: acceptance %d (%s): reason is required", path, i+1, a.ActionKey)
		case a.AcceptedBy == "":
			return nil, "", fmt.Errorf("%s: acceptance %d (%s): accepted_by is required", path, i+1, a.ActionKey)
		case a.AcceptedAt == "":
			return nil, "", fmt.Errorf("%s: acceptance %d (%s): accepted_at is required", path, i+1, a.ActionKey)
		}
	}
	if f.ChecklistFile != "" {
		sum, err := fileSHA256(f.ChecklistFile)
		if err != nil {
			return nil, fmt.Sprintf(
				"acceptances REJECTED: the referenced checklist %s could not be hashed (%v) — nothing was accepted",
				f.ChecklistFile, err), nil
		}
		if sum != f.ChecklistSHA256 {
			return nil, fmt.Sprintf(
				"acceptances REJECTED: %s no longer matches the reviewed checklist (sha256 %s, file has %s) — re-review and re-accept; nothing was accepted",
				f.ChecklistFile, f.ChecklistSHA256, sum), nil
		}
	}
	return f.Acceptances, "", nil
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
