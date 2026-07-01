package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

// runInventoryPolicyCmd implements `cpanel-self-migration inventory
// policy`: a fully offline, deterministic classification of an
// inventory_diff.json into a migration-readiness report. It never
// connects to any server and never applies anything. Exit codes:
// 0 = report generated (blockers are NOT a process error), 1 = invalid
// input (missing required flags, unreadable file, bad JSON) or write
// failure, 2 = unparsable flags, 3 = --fail-on-blockers was set and the
// overall status is "blocked" (reports are still fully written first).
func runInventoryPolicyCmd(args []string) int {
	fs := flag.NewFlagSet("inventory policy", flag.ContinueOnError)
	diffPath := fs.String("diff", "", "path to the inventory_diff.json to classify (required)")
	outJSON := fs.String("output-json", "policy_report.json", "path for the machine-readable report")
	outMD := fs.String("output-md", "policy_report.md", "path for the human-readable report")
	failOnBlockers := fs.Bool("fail-on-blockers", false, "exit with code 3 when the overall status is blocked (for CI gating)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration inventory policy --diff DIFF.json [--output-json PATH] [--output-md PATH] [--fail-on-blockers]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *diffPath == "" {
		fmt.Fprintln(os.Stderr, "error: --diff is required")
		fs.Usage()
		return 1
	}

	d, err := loadDiffFile(*diffPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	r := accountinventory.EvaluatePolicy(d)
	r.InputDiff = *diffPath
	r.GeneratedAt = time.Now().UTC().Format(time.RFC3339)

	if err := accountinventory.WritePolicyJSON(*outJSON, r); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := accountinventory.WritePolicyMarkdown(*outMD, r); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	fmt.Printf("inventory policy: %s — %d blocker(s), %d review(s), %d warning(s), %d info\n",
		r.OverallStatus, r.Summary.Blockers, r.Summary.Reviews, r.Summary.Warnings, r.Summary.Info)
	fmt.Fprintf(os.Stderr, "wrote %s\nwrote %s\n", *outJSON, *outMD)
	if *failOnBlockers && r.OverallStatus == accountinventory.StatusBlocked {
		fmt.Fprintln(os.Stderr, "fail-on-blockers: overall status is blocked, exiting 3")
		return 3
	}
	return 0
}

// loadDiffFile reads and minimally validates an inventory diff JSON.
func loadDiffFile(path string) (accountinventory.InventoryDiff, error) {
	var d accountinventory.InventoryDiff
	b, err := os.ReadFile(path) // #nosec G304 -- user-supplied CLI input path, read-only
	if err != nil {
		return d, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return d, fmt.Errorf("parse %s: %w", path, err)
	}
	if d.Mode != "inventory-diff" {
		return d, fmt.Errorf("%s: not an inventory diff file (mode %q)", path, d.Mode)
	}
	return d, nil
}
