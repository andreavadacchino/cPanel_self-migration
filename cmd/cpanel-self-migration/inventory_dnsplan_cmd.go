package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

// ipMapFlag collects repeatable --ip-map OLD=NEW pairs. Both sides must
// be IP literals; identity mappings (X=X) are the explicit way to
// authorize copying an address verbatim. Set never returns an error:
// malformed values accumulate in errs and are reported AFTER flag
// parsing as input errors (exit 1) — a Set error would surface as
// flag.Parse's generic failure and misclassify as usage (exit 2), and
// distinguishing the two by matching the stdlib error text would be
// fragile.
type ipMapFlag struct {
	m    map[string]string
	errs []string
}

func (f *ipMapFlag) String() string {
	keys := make([]string, 0, len(f.m))
	for k := range f.m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+f.m[k])
	}
	return strings.Join(parts, ",")
}

func (f *ipMapFlag) Set(s string) error {
	from, to, ok := strings.Cut(s, "=")
	switch {
	case !ok || from == "" || to == "":
		f.errs = append(f.errs, fmt.Sprintf("--ip-map: want OLD_IP=NEW_IP, got %q", s))
	case net.ParseIP(from) == nil:
		f.errs = append(f.errs, fmt.Sprintf("--ip-map: %q is not an IP address", from))
	case net.ParseIP(to) == nil:
		f.errs = append(f.errs, fmt.Sprintf("--ip-map: %q is not an IP address", to))
	default:
		f.m[strings.ToLower(from)] = to
	}
	return nil
}

// runInventoryDNSPlanCmd implements `cpanel-self-migration inventory
// dns-plan`: a fully offline builder of the DNS import plan (PR 6B). It
// never connects to any server and never writes anything but the two
// report files. The policy report is context, never a gate. Exit codes:
// 0 = plan generated, 1 = invalid input or write failure, 2 = flags.
func runInventoryDNSPlanCmd(args []string) int {
	fs := flag.NewFlagSet("inventory dns-plan", flag.ContinueOnError)
	source := fs.String("source", "", "path to the source inventory JSON (required)")
	destination := fs.String("destination", "", "path to the destination inventory JSON (required)")
	policyPath := fs.String("policy", "", "optional policy_report.json for context cross-references")
	outJSON := fs.String("output-json", "dns_import_plan.json", "path for the machine-readable plan")
	outMD := fs.String("output-md", "dns_import_plan.md", "path for the human-readable plan")
	ipMap := &ipMapFlag{m: map[string]string{}}
	fs.Var(ipMap, "ip-map", "OLD_IP=NEW_IP translation (repeatable; identity X=X authorizes a verbatim copy)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration inventory dns-plan --source SRC.json --destination DEST.json [--policy POLICY.json] [--ip-map OLD=NEW ...] [--output-json PATH] [--output-md PATH]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(ipMap.errs) > 0 {
		for _, e := range ipMap.errs {
			fmt.Fprintln(os.Stderr, "error:", e)
		}
		return 1
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

	plan, err := accountinventory.BuildDNSPlan(srcInv, destInv, policy, ipMap.m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
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

	if err := accountinventory.WriteDNSPlanJSON(*outJSON, plan); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := accountinventory.WriteDNSPlanMarkdown(*outMD, plan); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	fmt.Printf("dns plan: %d zone(s) — %d add, %d replace, %d manual, %d skip, %d informational\n",
		len(plan.Zones), plan.Summary.Add, plan.Summary.Replace, plan.Summary.Manual,
		plan.Summary.Skip, plan.Summary.Informational)
	fmt.Fprintf(os.Stderr, "wrote %s\nwrote %s\n", *outJSON, *outMD)
	return 0
}

// loadPolicyFile reads and minimally validates a policy report JSON.
func loadPolicyFile(path string) (accountinventory.PolicyReport, error) {
	var p accountinventory.PolicyReport
	b, err := os.ReadFile(path) // #nosec G304 -- user-supplied CLI input path, read-only
	if err != nil {
		return p, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("parse %s: %w", path, err)
	}
	if p.Mode != "inventory-policy" {
		return p, fmt.Errorf("%s: not a policy report (mode %q)", path, p.Mode)
	}
	return p, nil
}

// fileSHA256 hashes the raw input file so the plan can be refused by a
// future apply when its inputs changed (stale-plan defense).
func fileSHA256(path string) (string, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- user-supplied CLI input path, read-only
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b)), nil
}
