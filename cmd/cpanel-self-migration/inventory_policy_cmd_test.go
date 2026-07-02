package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

// writeDiffFileFor produces a real inventory_diff.json by running the real
// pipeline: two inventories → DiffInventories → WriteDiffJSON.
func writeDiffFileFor(t *testing.T, dir string, mutate func(dest *accountinventory.NormalizedInventory)) string {
	t.Helper()
	src := accountinventory.NewEmptyInventory("u", "1.2.3.4", "source")
	src.Domains = []accountinventory.DomainEntry{{Name: "main.example", Type: "main"}}
	src.Mailboxes = []accountinventory.MailboxEntry{{Email: "info@main.example", Domain: "main.example", User: "info"}}
	src.Forwarders = []accountinventory.ForwarderEntry{{Source: "fwd@main.example", Destination: "info@main.example", Domain: "main.example"}}
	// Mark every config section available on both sides: an unavailable
	// section emits POL-SECTION-UNAVAILABLE (review), so without this the
	// no-mutation fixture could never reach "ready".
	src.FTP.Available = true
	src.SSL.Available = true
	src.PHP.Available = true
	src.DNS.Available = true
	src.Cron.Available = true
	src.EmailRouting.Available = true
	src.DefaultAddresses.Available = true
	src.EmailFilters.Available = true
	src.Redirects.Available = true
	dest := src
	dest.Mailboxes = append([]accountinventory.MailboxEntry{}, src.Mailboxes...)
	dest.Forwarders = append([]accountinventory.ForwarderEntry{}, src.Forwarders...)
	if mutate != nil {
		mutate(&dest)
	}
	d := accountinventory.DiffInventories(src, dest)
	d.SourceFile, d.DestinationFile, d.GeneratedAt = "s.json", "d.json", "t"
	path := filepath.Join(dir, "inventory_diff.json")
	if err := accountinventory.WriteDiffJSON(path, d); err != nil {
		t.Fatal(err)
	}
	return path
}

// readOverallStatus parses the JSON policy report and returns its
// overall_status, failing the test if the file is missing or malformed.
func readOverallStatus(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("policy JSON report not readable: %v", err)
	}
	var r struct {
		OverallStatus string `json:"overall_status"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("policy JSON report not parsable: %v", err)
	}
	return r.OverallStatus
}

func TestInventoryPolicyCmdWithBlockerExitsZero(t *testing.T) {
	dir := t.TempDir()
	diff := writeDiffFileFor(t, dir, func(dest *accountinventory.NormalizedInventory) {
		dest.Mailboxes = nil // mailbox removed → blocker
	})
	outJSON := filepath.Join(dir, "policy.json")
	outMD := filepath.Join(dir, "policy.md")

	code := runInventoryPolicyCmd([]string{"--diff", diff, "--output-json", outJSON, "--output-md", outMD})
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (blockers are findings, not process errors)", code)
	}
	if got := readOverallStatus(t, outJSON); got != "blocked" {
		t.Errorf("overall_status = %v, want blocked", got)
	}
	md, err := os.ReadFile(outMD)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "POL-MAILBOX-REMOVED") {
		t.Error("markdown missing the blocker finding")
	}
}

func TestInventoryPolicyCmdFailOnBlockersBlockedExitsThree(t *testing.T) {
	dir := t.TempDir()
	diff := writeDiffFileFor(t, dir, func(dest *accountinventory.NormalizedInventory) {
		dest.Mailboxes = nil // mailbox removed → blocker
	})
	outJSON := filepath.Join(dir, "policy.json")
	outMD := filepath.Join(dir, "policy.md")

	code := runInventoryPolicyCmd([]string{"--diff", diff, "--fail-on-blockers", "--output-json", outJSON, "--output-md", outMD})
	// The literal 3 is asserted on purpose: the exit code is CLI contract
	// (docs/COMMAND.md), so a change to exitBlockedGate must fail here.
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (blocked status must gate with --fail-on-blockers)", code)
	}
	// The gate must NOT suppress report generation: both artifacts exist
	// and the JSON still records the blocked status.
	if got := readOverallStatus(t, outJSON); got != "blocked" {
		t.Errorf("overall_status = %v, want blocked", got)
	}
	if _, err := os.Stat(outMD); err != nil {
		t.Errorf("markdown report not written before gating exit: %v", err)
	}
}

func TestInventoryPolicyCmdFailOnBlockersReadyExitsZero(t *testing.T) {
	dir := t.TempDir()
	diff := writeDiffFileFor(t, dir, nil) // identical inventories → ready
	outJSON := filepath.Join(dir, "policy.json")

	code := runInventoryPolicyCmd([]string{"--diff", diff, "--fail-on-blockers",
		"--output-json", outJSON, "--output-md", filepath.Join(dir, "policy.md")})
	if code != 0 {
		t.Errorf("exit = %d, want 0 (ready must not gate)", code)
	}
	if got := readOverallStatus(t, outJSON); got != "ready" {
		t.Fatalf("fixture produced overall_status = %v, want ready (test would be vacuous)", got)
	}
}

func TestInventoryPolicyCmdFailOnBlockersReviewRequiredExitsZero(t *testing.T) {
	dir := t.TempDir()
	diff := writeDiffFileFor(t, dir, func(dest *accountinventory.NormalizedInventory) {
		dest.Forwarders = nil // forwarder removed → review, not blocker
	})
	outJSON := filepath.Join(dir, "policy.json")

	code := runInventoryPolicyCmd([]string{"--diff", diff, "--fail-on-blockers",
		"--output-json", outJSON, "--output-md", filepath.Join(dir, "policy.md")})
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (--fail-on-blockers gates only blocked, not review_required)", code)
	}
	if got := readOverallStatus(t, outJSON); got != "review_required" {
		t.Fatalf("fixture produced overall_status = %v, want review_required (test would be vacuous)", got)
	}
}

func TestInventoryPolicyCmdMissingFile(t *testing.T) {
	if code := runInventoryPolicyCmd([]string{"--diff", "/nonexistent/diff.json"}); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}

func TestInventoryPolicyCmdInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runInventoryPolicyCmd([]string{"--diff", bad}); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}

func TestInventoryPolicyCmdNotADiff(t *testing.T) {
	dir := t.TempDir()
	notDiff := filepath.Join(dir, "other.json")
	if err := os.WriteFile(notDiff, []byte(`{"mode":"something-else"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runInventoryPolicyCmd([]string{"--diff", notDiff}); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}

func TestInventoryPolicyCmdBadFlags(t *testing.T) {
	if code := runInventoryPolicyCmd([]string{"--no-such-flag"}); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}

func TestInventoryPolicyCmdMissingDiffFlag(t *testing.T) {
	if code := runInventoryPolicyCmd([]string{}); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}
