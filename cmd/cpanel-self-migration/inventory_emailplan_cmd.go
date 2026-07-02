package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

// runInventoryEmailPlanCmd implements `cpanel-self-migration inventory
// email-plan`: a fully offline builder of the email apply plan (PR 2B-1).
// It never connects to any server and never writes anything but the two
// report files. The policy report is context, never a gate. Exit codes:
// 0 = plan generated, 1 = invalid input or write failure, 2 = flags.
func runInventoryEmailPlanCmd(args []string) int {
	fs := flag.NewFlagSet("inventory email-plan", flag.ContinueOnError)
	source := fs.String("source", "", "path to the source inventory JSON (required)")
	destination := fs.String("destination", "", "path to the destination inventory JSON (required)")
	policyPath := fs.String("policy", "", "optional policy_report.json for context cross-references")
	outJSON := fs.String("output-json", "email_apply_plan.json", "path for the machine-readable plan")
	outMD := fs.String("output-md", "email_apply_plan.md", "path for the human-readable plan")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration inventory email-plan --source SRC.json --destination DEST.json [--policy POLICY.json] [--output-json PATH] [--output-md PATH]")
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

	var policy *accountinventory.PolicyReport
	if *policyPath != "" {
		p, err := loadPolicyFile(*policyPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		policy = &p
	}

	plan := accountinventory.BuildEmailPlan(srcInv, destInv, policy)
	plan.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	plan.SourceFile, plan.DestinationFile, plan.PolicyFile = *source, *destination, *policyPath
	if plan.SourceSHA256, err = fileSHA256(*source); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if plan.DestinationSHA256, err = fileSHA256(*destination); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	if err := accountinventory.WriteEmailPlanJSON(*outJSON, plan); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := accountinventory.WriteEmailPlanMarkdown(*outMD, plan); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	fmt.Printf("email plan: %d op(s) — %d create, %d set, %d manual, %d skip, %d informational\n",
		len(plan.Ops), plan.Summary.Create, plan.Summary.Set, plan.Summary.Manual,
		plan.Summary.Skip, plan.Summary.Informational)
	fmt.Fprintf(os.Stderr, "wrote %s\nwrote %s\n", *outJSON, *outMD)
	return 0
}
