package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
)

// runEmailVerifyCmd implements `cpanel-self-migration email verify`:
// re-list the DESTINATION forwarders and default addresses named by an
// email_apply_plan.json (read-only) and report, per planned op, whether
// the live state matches the plan (PR 2B-1, mirror of `dns verify`). The
// source host is never dialed.
//
// Exit codes: 0 verify ran and reports were written (even with drift);
// 1 invalid input, config/SSH-dial failure or write failure; 2 flag
// parsing; 3 gated refusal (stale plan, or --fail-on-drift + not clean).
func runEmailVerifyCmd(args []string) int {
	fs := flag.NewFlagSet("email verify", flag.ContinueOnError)
	planPath := fs.String("plan", "", "path to email_apply_plan.json (required)")
	cfgFlag := fs.String("config", "", "path to host.yaml (default: configs/host.yaml or host.yaml)")
	srcInv := fs.String("source", "", "optional source inventory JSON: refuse the plan unless its embedded source_sha256 matches this file")
	destInv := fs.String("destination", "", "optional destination inventory JSON: refuse the plan unless its embedded destination_sha256 matches this file")
	outJSON := fs.String("output-json", "email_verify_report.json", "verify report JSON output path")
	outMD := fs.String("output-md", "email_verify_report.md", "verify report Markdown output path")
	failOnDrift := fs.Bool("fail-on-drift", false, "exit 3 unless the verify verdict is clean (no pending, drift, unavailable ops or manual sections)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration email verify --plan email_apply_plan.json [--config host.yaml] [--source src.json] [--destination dest.json] [--fail-on-drift]")
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

	plan, err := loadEmailPlanFile(*planPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if plan.FormatVersion != 1 {
		fmt.Fprintf(os.Stderr, "error: %s: unsupported plan format_version %d (this build understands 1)\n", *planPath, plan.FormatVersion)
		return 1
	}

	// Stale-plan gate (before any SSH): identical semantics to dns verify.
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

	// Domains verify must re-list: every 2B-1-section op + informational.
	domainSet := map[string]bool{}
	arDomainSet := map[string]bool{}
	needDefaults := false
	for _, op := range plan.Ops {
		switch op.Section {
		case accountinventory.EmailSectionForwarders:
			domainSet[op.Domain] = true
		case accountinventory.EmailSectionDefaultAddress:
			needDefaults = true
		case accountinventory.EmailSectionAutoresponders:
			arDomainSet[op.Domain] = true
		}
	}
	for _, info := range plan.Informational {
		switch info.Section {
		case accountinventory.EmailSectionForwarders:
			domainSet[info.Domain] = true
		case accountinventory.EmailSectionDefaultAddress:
			needDefaults = true
		case accountinventory.EmailSectionAutoresponders:
			arDomainSet[info.Domain] = true
		}
	}
	domains := make([]string, 0, len(domainSet))
	for d := range domainSet {
		domains = append(domains, d)
	}
	sort.Strings(domains)
	arDomains := make([]string, 0, len(arDomainSet))
	for d := range arDomainSet {
		arDomains = append(arDomains, d)
	}
	sort.Strings(arDomains)

	// Fetch the live state — only when there is something to re-list. A
	// manual-only plan needs no config and opens no SSH.
	live := accountinventory.EmailLiveState{
		ForwardersByDomain:      map[string][]accountinventory.ForwarderEntry{},
		ForwarderListErrors:     map[string]string{},
		AutorespondersByDomain:  map[string][]accountinventory.AutoresponderEntry{},
		AutoresponderListErrors: map[string]string{},
	}
	if len(domains) > 0 || needDefaults || len(arDomains) > 0 {
		ctx := context.Background()
		client, err := dialEmailDest(ctx, *cfgFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		defer func() { _ = client.Close() }()
		for _, d := range domains {
			entries, err := cpanel.ListForwarders(ctx, client, d)
			if err != nil {
				live.ForwarderListErrors[d] = err.Error()
				continue
			}
			live.ForwardersByDomain[d] = normalizeForwarders(entries, d)
		}
		if needDefaults {
			entries, err := cpanel.ListDefaultAddresses(ctx, client)
			if err != nil {
				live.DefaultsError = err.Error()
			} else {
				live.DefaultsListed = true
				live.Defaults = normalizeDefaults(entries)
			}
		}
		for _, d := range arDomains {
			entries, _, _, err := fetchAutorespondersWithRaw(ctx, client, d)
			if err != nil {
				live.AutoresponderListErrors[d] = err.Error()
				continue
			}
			live.AutorespondersByDomain[d] = entries
		}
	}

	rep := accountinventory.VerifyEmailPlan(plan, live)
	rep.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	rep.PlanFile = *planPath
	rep.PlanSHA256 = planSHA

	if err := accountinventory.WriteEmailVerifyJSON(*outJSON, rep); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := accountinventory.WriteEmailVerifyMarkdown(*outMD, rep); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	verdict := "CLEAN"
	if !rep.Clean {
		verdict = "NOT CLEAN"
	}
	fmt.Printf("email verify: %s — %d applied, %d unchanged, %d pending, %d drift, %d manual review, %d not checked, %d unavailable, %d untracked; %d manual section(s)\n",
		verdict, rep.Summary.Applied, rep.Summary.Unchanged, rep.Summary.Pending, rep.Summary.Drift,
		rep.Summary.ManualReview, rep.Summary.NotChecked, rep.Summary.Unavailable,
		rep.Summary.Untracked, rep.Summary.ManualSections)
	fmt.Fprintf(os.Stderr, "wrote %s\n", *outJSON)
	fmt.Fprintf(os.Stderr, "wrote %s\n", *outMD)

	if *failOnDrift && !rep.Clean {
		fmt.Fprintln(os.Stderr, "verdict not clean and --fail-on-drift is set — exiting 3")
		return exitDriftGate
	}
	return 0
}
