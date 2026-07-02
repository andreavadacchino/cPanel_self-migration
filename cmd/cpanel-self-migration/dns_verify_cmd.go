package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// exitDriftGate is returned when --fail-on-drift is set and the verify
// verdict is not clean, or when the stale-plan gate refuses the plan
// (house convention: 3 = gated refusal, like --fail-on-blockers).
const exitDriftGate = 3

// runDNSVerifyCmd implements `cpanel-self-migration dns verify`: re-fetch
// the DESTINATION zones named by a dns_import_plan.json (read-only, UAPI →
// API2 fallback — the collector's own fetch) and report, per planned op,
// whether the live zone matches the plan (PR 6C). The source host is never
// dialed: it may already be decommissioned when verify runs.
//
// Exit codes: 0 verify ran and reports were written (even with drift);
// 1 invalid input, config/SSH-dial failure or write failure; 2 flag
// parsing; 3 gated refusal (stale plan, or --fail-on-drift + not clean).
func runDNSVerifyCmd(args []string) int {
	fs := flag.NewFlagSet("dns verify", flag.ContinueOnError)
	planPath := fs.String("plan", "", "path to dns_import_plan.json (required)")
	cfgFlag := fs.String("config", "", "path to host.yaml (default: configs/host.yaml or host.yaml)")
	srcInv := fs.String("source", "", "optional source inventory JSON: refuse the plan unless its embedded source_sha256 matches this file")
	destInv := fs.String("destination", "", "optional destination inventory JSON: refuse the plan unless its embedded destination_sha256 matches this file")
	outJSON := fs.String("output-json", "dns_verify_report.json", "verify report JSON output path")
	outMD := fs.String("output-md", "dns_verify_report.md", "verify report Markdown output path")
	failOnDrift := fs.Bool("fail-on-drift", false, "exit 3 unless the verify verdict is clean (no pending, drift, unavailable or manual zones)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration dns verify --plan dns_import_plan.json [--config host.yaml] [--source src.json] [--destination dest.json] [--fail-on-drift]")
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

	plan, err := loadDNSPlanFile(*planPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if plan.FormatVersion != 1 {
		fmt.Fprintf(os.Stderr, "error: %s: unsupported plan format_version %d (this build understands 1)\n", *planPath, plan.FormatVersion)
		return 1
	}

	// Stale-plan gate (before any SSH): when the operator points at the
	// inventories, their hashes must match the ones the plan was built
	// from. A plan with no embedded hash cannot be validated — fail-safe
	// refusal rather than a silent pass.
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

	// Fetch the live destination zones — only when the plan has zones to
	// verify. An all-manual plan needs no config and opens no SSH.
	ctx := context.Background()
	live := map[string]accountinventory.DNSZoneResult{}
	if len(plan.Zones) > 0 {
		path, alternates, err := resolveConfigPath(*cfgFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		for _, alt := range alternates {
			fmt.Fprintf(os.Stderr, "note: multiple host.yaml candidates, using %s (ignoring %s)\n", path, alt)
		}
		cfg, err := config.Load(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		if !cfg.DestConfigured() {
			fmt.Fprintln(os.Stderr, "error: dns verify needs the DESTINATION host configured in", path)
			return 1
		}
		client, err := sshx.DialDest(ctx, cfg, "")
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: dial destination:", err)
			return 1
		}
		defer func() { _ = client.Close() }()
		for _, pz := range plan.Zones {
			zone := strings.ToLower(pz.Zone)
			live[zone] = accountinventory.FetchDNSZone(ctx, client, zone)
		}
	}

	rep := accountinventory.VerifyDNSPlan(plan, live)
	rep.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	rep.PlanFile = *planPath
	rep.PlanSHA256 = planSHA

	if err := accountinventory.WriteDNSVerifyJSON(*outJSON, rep); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := accountinventory.WriteDNSVerifyMarkdown(*outMD, rep); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	verdict := "CLEAN"
	if !rep.Clean {
		verdict = "NOT CLEAN"
	}
	fmt.Printf("dns verify: %s — %d applied, %d unchanged, %d pending, %d drift, %d manual review, %d not checked, %d untracked; %d unavailable zone(s), %d manual zone(s)\n",
		verdict, rep.Summary.Applied, rep.Summary.Unchanged, rep.Summary.Pending, rep.Summary.Drift,
		rep.Summary.ManualReview, rep.Summary.NotChecked, rep.Summary.Untracked,
		rep.Summary.UnavailableZones, rep.Summary.ManualZones)
	fmt.Fprintf(os.Stderr, "wrote %s\n", *outJSON)
	fmt.Fprintf(os.Stderr, "wrote %s\n", *outMD)

	if *failOnDrift && !rep.Clean {
		fmt.Fprintln(os.Stderr, "verdict not clean and --fail-on-drift is set — exiting 3")
		return exitDriftGate
	}
	return 0
}
