package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

func writeEmailPlanInventory(t *testing.T, dir, name, side, user string, fwds []accountinventory.ForwarderEntry, defaults []accountinventory.DefaultAddressEntry) string {
	t.Helper()
	inv := accountinventory.NewEmptyInventory(user, "192.0.2.1", side)
	inv.Domains = []accountinventory.DomainEntry{{Name: "example.com", Type: "main"}}
	inv.Forwarders = fwds
	inv.DefaultAddresses.Available = true
	inv.DefaultAddresses.Items = defaults
	inv.EmailRouting.Available = true
	inv.EmailFilters.Available = true
	b, err := json.MarshalIndent(inv, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInventoryEmailPlanCmdFlagAndInputErrors(t *testing.T) {
	if code := runInventoryEmailPlanCmd([]string{"--bogus"}); code != 2 {
		t.Errorf("unknown flag: code = %d, want 2", code)
	}
	if code := runInventoryEmailPlanCmd([]string{}); code != 1 {
		t.Errorf("missing required flags: code = %d, want 1", code)
	}
	dir := t.TempDir()
	src := writeEmailPlanInventory(t, dir, "src.json", "source", "acct", nil, nil)
	if code := runInventoryEmailPlanCmd([]string{"--source", src, "--destination", filepath.Join(dir, "nope.json")}); code != 1 {
		t.Errorf("nonexistent destination: code = %d, want 1", code)
	}
}

func TestInventoryEmailPlanCmdEndToEnd(t *testing.T) {
	dir := t.TempDir()
	src := writeEmailPlanInventory(t, dir, "src.json", "source", "acct",
		[]accountinventory.ForwarderEntry{
			{Source: "info@example.com", Destination: "someone@gmail.com", Domain: "example.com"},
		},
		[]accountinventory.DefaultAddressEntry{
			{Domain: "example.com", DefaultAddress: "someone@gmail.com"},
		})
	dest := writeEmailPlanInventory(t, dir, "dest.json", "destination", "acct", nil,
		[]accountinventory.DefaultAddressEntry{
			{Domain: "example.com", DefaultAddress: "acct"},
		})
	outJSON := filepath.Join(dir, "email_apply_plan.json")
	outMD := filepath.Join(dir, "email_apply_plan.md")

	code := runInventoryEmailPlanCmd([]string{
		"--source", src, "--destination", dest,
		"--output-json", outJSON, "--output-md", outMD,
	})
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}

	b, err := os.ReadFile(outJSON)
	if err != nil {
		t.Fatal(err)
	}
	var plan accountinventory.EmailApplyPlan
	if err := json.Unmarshal(b, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Mode != "email-apply-plan" || plan.FormatVersion != 1 {
		t.Errorf("mode/version = %q/%d", plan.Mode, plan.FormatVersion)
	}
	if plan.Summary.Create != 1 || plan.Summary.Set != 1 {
		t.Errorf("summary = %+v, want 1 create + 1 set", plan.Summary)
	}
	// The embedded hashes must match the raw input files (stale-plan defense).
	srcSHA, err := fileSHA256(src)
	if err != nil {
		t.Fatal(err)
	}
	if plan.SourceSHA256 != srcSHA {
		t.Errorf("source_sha256 = %q, want %q", plan.SourceSHA256, srcSHA)
	}
	if plan.SourceUser != "acct" || plan.DestinationUser != "acct" {
		t.Errorf("users = %q/%q", plan.SourceUser, plan.DestinationUser)
	}
	if _, err := os.Stat(outMD); err != nil {
		t.Errorf("markdown plan missing: %v", err)
	}
}
