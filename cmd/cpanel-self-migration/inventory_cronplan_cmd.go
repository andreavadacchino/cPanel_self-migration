package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

// runInventoryCronPlanCmd implements `cpanel-self-migration inventory
// cron-plan`: a fully offline builder of the cron apply plan (PR 2A).
// It never connects to any server and never writes anything but the two
// report files. Exit codes: 0 = plan generated, 1 = invalid input or
// write failure, 2 = flags.
func runInventoryCronPlanCmd(args []string) int {
	fs := flag.NewFlagSet("inventory cron-plan", flag.ContinueOnError)
	source := fs.String("source", "", "path to the source inventory JSON (required)")
	destination := fs.String("destination", "", "path to the destination inventory JSON (required)")
	outJSON := fs.String("output-json", "cron_apply_plan.json", "path for the machine-readable plan")
	outMD := fs.String("output-md", "", "path for the human-readable plan (default: derived from --output-json)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration inventory cron-plan --source SRC.json --destination DEST.json [--output-json PATH] [--output-md PATH]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *source == "" || *destination == "" {
		fmt.Fprintln(os.Stderr, "error: --source and --destination are required")
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

	plan := accountinventory.BuildCronPlan(srcInv, destInv)
	plan.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	plan.SourceFile, plan.DestinationFile = *source, *destination

	if plan.SourceSHA256, err = fileSHA256(*source); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if plan.DestinationSHA256, err = fileSHA256(*destination); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	// PlanTimeDestCrontab stays empty: the plan is fully offline and
	// cannot read the live destination crontab. The apply command reads
	// the crontab at apply time and records the hash into the backup for
	// the rollback guard. Future runs can supply --dest-crontab-hash
	// to lock the plan to a known state.

	mdPath := *outMD
	if mdPath == "" {
		mdPath = deriveMDPath(*outJSON)
	}

	if err := accountinventory.WriteCronPlanJSON(*outJSON, plan); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := accountinventory.WriteCronPlanMarkdown(mdPath, plan); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	fmt.Printf("cron plan: %d op(s) — %d create, %d skip, %d manual, %d informational\n",
		len(plan.Ops), plan.Summary.Create, plan.Summary.Skip, plan.Summary.Manual,
		plan.Summary.Informational)
	fmt.Fprintf(os.Stderr, "wrote %s\nwrote %s\n", *outJSON, mdPath)
	return 0
}
