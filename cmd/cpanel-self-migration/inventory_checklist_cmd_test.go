package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

// checklistFixtureFiles runs the REAL pipeline into a temp dir: two
// inventories → diff → policy, all written with the real writers. mutate
// tweaks the destination before diffing. Returns the four required paths.
func checklistFixtureFiles(t *testing.T, dir string, withMail bool, mutate func(dest *accountinventory.NormalizedInventory)) (src, dest, diff, policy string) {
	t.Helper()
	s := accountinventory.NewEmptyInventory("srcacct", "1.2.3.4", "source")
	s.Domains = []accountinventory.DomainEntry{{Name: "main.example", Type: "main", DocumentRoot: "/home/srcacct/public_html"}}
	s.Databases = []accountinventory.DatabaseEntry{{Name: "srcacct_db", Users: []string{"srcacct_u"}}}
	if withMail {
		s.Mailboxes = []accountinventory.MailboxEntry{{Email: "info@main.example", Domain: "main.example", User: "info"}}
	}
	s.FTP.Available = true
	s.SSL.Available = true
	s.PHP.Available = true
	s.DNS.Available = true
	s.Cron.Available = true

	// Every slice is re-allocated so a mutate callback can never corrupt
	// the source fixture through a shared backing array.
	d := s
	d.Account = accountinventory.AccountInfo{User: "destacct", Host: "5.6.7.8", CollectedAt: s.Account.CollectedAt, Side: "destination"}
	d.Domains = append([]accountinventory.DomainEntry{}, s.Domains...)
	d.Mailboxes = append([]accountinventory.MailboxEntry{}, s.Mailboxes...)
	d.Databases = append([]accountinventory.DatabaseEntry{}, s.Databases...)
	d.Forwarders = append([]accountinventory.ForwarderEntry{}, s.Forwarders...)
	d.Autoresponders = append([]accountinventory.AutoresponderEntry{}, s.Autoresponders...)
	d.FTP.Items = append([]accountinventory.FTPEntry{}, s.FTP.Items...)
	d.SSL.Items = append([]accountinventory.SSLEntry{}, s.SSL.Items...)
	d.PHP.Items = append([]accountinventory.PHPEntry{}, s.PHP.Items...)
	d.DNS.Zones = append([]accountinventory.DNSZoneResult{}, s.DNS.Zones...)
	d.Cron.Jobs = append([]accountinventory.CronJobEntry{}, s.Cron.Jobs...)
	if mutate != nil {
		mutate(&d)
	}

	src = filepath.Join(dir, "inventory_source.json")
	dest = filepath.Join(dir, "inventory_destination.json")
	if err := accountinventory.WriteInventoryJSON(src, s); err != nil {
		t.Fatal(err)
	}
	if err := accountinventory.WriteInventoryJSON(dest, d); err != nil {
		t.Fatal(err)
	}

	dd := accountinventory.DiffInventories(s, d)
	dd.SourceFile, dd.DestinationFile, dd.GeneratedAt = src, dest, "t"
	diff = filepath.Join(dir, "inventory_diff.json")
	if err := accountinventory.WriteDiffJSON(diff, dd); err != nil {
		t.Fatal(err)
	}

	pr := accountinventory.EvaluatePolicy(dd)
	pr.InputDiff, pr.GeneratedAt = diff, "t"
	policy = filepath.Join(dir, "policy_report.json")
	if err := accountinventory.WritePolicyJSON(policy, pr); err != nil {
		t.Fatal(err)
	}
	return src, dest, diff, policy
}

// writeApplyReport writes a minimal but real-shaped report.json from a
// successful full apply run.
func writeApplyReport(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "report.json")
	body := `{
  "run_id": "run-test",
  "version": "test",
  "mode": "apply",
  "scope": {"mail": true, "files": true, "databases": true},
  "exit_status": "success"
}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readChecklistJSON(t *testing.T, path string) accountinventory.MigrationChecklist {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("checklist JSON not readable: %v", err)
	}
	var c accountinventory.MigrationChecklist
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("checklist JSON not parsable: %v", err)
	}
	return c
}

func TestInventoryChecklistCmdHappyPath(t *testing.T) {
	dir := t.TempDir()
	src, dest, diff, policy := checklistFixtureFiles(t, dir, true, nil)
	rep := writeApplyReport(t, dir)
	outJSON := filepath.Join(dir, "checklist.json")
	outMD := filepath.Join(dir, "checklist.md")

	code := runInventoryChecklistCmd([]string{
		"--source", src, "--destination", dest, "--diff", diff, "--policy", policy,
		"--migration-report", rep, "--output-json", outJSON, "--output-md", outMD,
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	c := readChecklistJSON(t, outJSON)
	if c.Mode != "migration-checklist" || c.FormatVersion != 1 {
		t.Errorf("mode/version = %q/%d", c.Mode, c.FormatVersion)
	}
	if c.Account != "srcacct" {
		t.Errorf("account = %q, want srcacct", c.Account)
	}
	// A mail-bearing account in v0 can never be READY_*: email routing is
	// not inventoried and must be confirmed by hand.
	if c.OverallStatus != accountinventory.OverallManualActionRequired {
		t.Errorf("overall = %q, want %q", c.OverallStatus, accountinventory.OverallManualActionRequired)
	}
	if !c.Inputs.SourceInventory.Present || c.Inputs.SourceInventory.SHA256 == "" {
		t.Error("source inventory input ref missing sha256")
	}
	if c.Inputs.DNSPlan.Present {
		t.Error("dns plan marked present although it was not provided")
	}
	if c.ChainVerified {
		t.Error("chain_verified must be false in PR 7A")
	}
	md, err := os.ReadFile(outMD)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "# Migration Checklist — srcacct") {
		t.Error("markdown missing the title")
	}
}

func TestInventoryChecklistCmdFailOnNotReadyGates(t *testing.T) {
	dir := t.TempDir()
	// No migration report: core areas have no evidence → NOT_READY (mail
	// absent so the overall is not MANUAL_ACTION_REQUIRED).
	src, dest, diff, policy := checklistFixtureFiles(t, dir, false, nil)
	outJSON := filepath.Join(dir, "checklist.json")
	outMD := filepath.Join(dir, "checklist.md")

	code := runInventoryChecklistCmd([]string{
		"--source", src, "--destination", dest, "--diff", diff, "--policy", policy,
		"--fail-on-not-ready", "--output-json", outJSON, "--output-md", outMD,
	})
	// Literal 3 asserted on purpose: CLI contract (docs/COMMAND.md).
	if code != 3 {
		t.Fatalf("exit = %d, want 3", code)
	}
	// The gate must NOT suppress report generation.
	c := readChecklistJSON(t, outJSON)
	if c.OverallStatus != accountinventory.OverallNotReady {
		t.Errorf("overall = %q, want %q", c.OverallStatus, accountinventory.OverallNotReady)
	}
	if _, err := os.Stat(outMD); err != nil {
		t.Errorf("markdown not written before gating exit: %v", err)
	}
}

func TestInventoryChecklistCmdFailOnNotReadyPassesWhenReady(t *testing.T) {
	dir := t.TempDir()
	src, dest, diff, policy := checklistFixtureFiles(t, dir, false, nil)
	rep := writeApplyReport(t, dir)
	outJSON := filepath.Join(dir, "checklist.json")

	code := runInventoryChecklistCmd([]string{
		"--source", src, "--destination", dest, "--diff", diff, "--policy", policy,
		"--migration-report", rep, "--fail-on-not-ready",
		"--output-json", outJSON, "--output-md", filepath.Join(dir, "checklist.md"),
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	c := readChecklistJSON(t, outJSON)
	if c.OverallStatus != accountinventory.OverallReadyWithManualNotes {
		t.Fatalf("fixture produced overall %q, want %q (test would be vacuous)",
			c.OverallStatus, accountinventory.OverallReadyWithManualNotes)
	}
}

func TestInventoryChecklistCmdNotReadyWithoutGateExitsZero(t *testing.T) {
	dir := t.TempDir()
	src, dest, diff, policy := checklistFixtureFiles(t, dir, true, nil)

	code := runInventoryChecklistCmd([]string{
		"--source", src, "--destination", dest, "--diff", diff, "--policy", policy,
		"--output-json", filepath.Join(dir, "c.json"), "--output-md", filepath.Join(dir, "c.md"),
	})
	if code != 0 {
		t.Errorf("exit = %d, want 0 (manual actions are findings, not process errors)", code)
	}
}

func TestInventoryChecklistCmdAcceptsDNSPlan(t *testing.T) {
	dir := t.TempDir()
	src, dest, diff, policy := checklistFixtureFiles(t, dir, false, nil)

	// A real plan produced by the real builder.
	var s, d accountinventory.NormalizedInventory
	for path, dst := range map[string]*accountinventory.NormalizedInventory{src: &s, dest: &d} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, dst); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := accountinventory.BuildDNSPlan(s, d, nil, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	plan.GeneratedAt = "t"
	planPath := filepath.Join(dir, "dns_import_plan.json")
	if err := accountinventory.WriteDNSPlanJSON(planPath, plan); err != nil {
		t.Fatal(err)
	}

	outJSON := filepath.Join(dir, "c.json")
	code := runInventoryChecklistCmd([]string{
		"--source", src, "--destination", dest, "--diff", diff, "--policy", policy,
		"--dns-plan", planPath,
		"--output-json", outJSON, "--output-md", filepath.Join(dir, "c.md"),
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if c := readChecklistJSON(t, outJSON); !c.Inputs.DNSPlan.Present || c.Inputs.DNSPlan.SHA256 == "" {
		t.Error("dns plan input ref not recorded")
	}
}

func TestInventoryChecklistCmdInputErrors(t *testing.T) {
	dir := t.TempDir()
	src, dest, diff, policy := checklistFixtureFiles(t, dir, false, nil)
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	notAPlan := filepath.Join(dir, "notaplan.json")
	if err := os.WriteFile(notAPlan, []byte(`{"mode":"something-else"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		args []string
		want int
	}{
		{"missing required flags", []string{}, 1},
		{"missing source file", []string{"--source", "/nonexistent.json", "--destination", dest, "--diff", diff, "--policy", policy}, 1},
		{"invalid diff JSON", []string{"--source", src, "--destination", dest, "--diff", bad, "--policy", policy}, 1},
		{"wrong-mode policy", []string{"--source", src, "--destination", dest, "--diff", diff, "--policy", notAPlan}, 1},
		{"wrong-mode dns plan", []string{"--source", src, "--destination", dest, "--diff", diff, "--policy", policy, "--dns-plan", notAPlan}, 1},
		{"non-report migration report", []string{"--source", src, "--destination", dest, "--diff", diff, "--policy", policy, "--migration-report", notAPlan}, 1},
		{"bad flag", []string{"--no-such-flag"}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append(tt.args, "--output-json", filepath.Join(dir, "o.json"), "--output-md", filepath.Join(dir, "o.md"))
			if tt.want == 2 {
				args = tt.args
			}
			if code := runInventoryChecklistCmd(args); code != tt.want {
				t.Errorf("exit = %d, want %d", code, tt.want)
			}
		})
	}
}
