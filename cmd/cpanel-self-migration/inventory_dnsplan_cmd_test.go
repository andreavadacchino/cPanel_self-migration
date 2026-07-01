package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

// writeDNSInventory writes a real inventory JSON with one DNS zone, in
// the real name format (apex absolute, non-apex relative).
func writeDNSInventory(t *testing.T, dir, name, side, zone string, records []accountinventory.DNSRecordEntry) string {
	t.Helper()
	inv := accountinventory.NewEmptyInventory("u", "1.2.3.4", side)
	inv.DNS.Available = true
	inv.DNS.Zones = []accountinventory.DNSZoneResult{
		{Available: true, Zone: zone, Method: "uapi", Records: records},
	}
	b, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInventoryDNSPlanCmdHappyPath(t *testing.T) {
	dir := t.TempDir()
	src := writeDNSInventory(t, dir, "src.json", "source", "example.com",
		[]accountinventory.DNSRecordEntry{
			{Type: "A", Name: "example.com.", TTL: 14400, Address: "194.76.118.193", Value: "194.76.118.193"},
			{Type: "A", Name: "ftp", TTL: 14400, Address: "194.76.118.193", Value: "194.76.118.193"},
		})
	dest := writeDNSInventory(t, dir, "dest.json", "destination", "example.com",
		[]accountinventory.DNSRecordEntry{
			{Type: "A", Name: "example.com.", TTL: 14400, Address: "38.224.109.78", Value: "38.224.109.78"},
		})
	outJSON := filepath.Join(dir, "plan.json")
	outMD := filepath.Join(dir, "plan.md")

	code := runInventoryDNSPlanCmd([]string{
		"--source", src, "--destination", dest,
		"--ip-map", "194.76.118.193=38.224.109.78",
		"--output-json", outJSON, "--output-md", outMD,
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}

	b, err := os.ReadFile(outJSON)
	if err != nil {
		t.Fatal(err)
	}
	var p accountinventory.DNSPlan
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if p.Mode != "dns-import-plan" {
		t.Errorf("mode = %q", p.Mode)
	}
	if p.Summary.Add != 1 || p.Summary.Skip != 1 {
		t.Errorf("summary = %+v, want 1 add (ftp) + 1 skip (apex)", p.Summary)
	}
	// SHA-256 of the inputs must be pinned into the plan.
	if len(p.SourceSHA256) != 64 || len(p.DestinationSHA256) != 64 {
		t.Errorf("input hashes missing: %q %q", p.SourceSHA256, p.DestinationSHA256)
	}
	if p.IPMap["194.76.118.193"] != "38.224.109.78" {
		t.Error("ip-map not embedded in the plan")
	}
	if _, err := os.Stat(outMD); err != nil {
		t.Errorf("markdown not written: %v", err)
	}
}

func TestInventoryDNSPlanCmdWithPolicyContext(t *testing.T) {
	dir := t.TempDir()
	src := writeDNSInventory(t, dir, "src.json", "source", "example.com",
		[]accountinventory.DNSRecordEntry{
			{Type: "MX", Name: "example.com.", TTL: 3600, Exchange: "example.com.", Priority: 0, Value: "example.com."},
		})
	dest := writeDNSInventory(t, dir, "dest.json", "destination", "example.com", nil)
	pol := accountinventory.PolicyReport{
		Mode:          "inventory-policy",
		OverallStatus: accountinventory.StatusBlocked,
		Findings: []accountinventory.PolicyFinding{
			{ID: "POL-DNS-MX-REMOVED", Section: "dns", Severity: accountinventory.SeverityBlocker,
				SourceRef: "zone example.com MX example.com."},
		},
	}
	polPath := filepath.Join(dir, "policy.json")
	pb, _ := json.Marshal(pol)
	if err := os.WriteFile(polPath, pb, 0o600); err != nil {
		t.Fatal(err)
	}
	outJSON := filepath.Join(dir, "plan.json")

	// A blocked policy must NOT prevent plan generation (context, not gate).
	code := runInventoryDNSPlanCmd([]string{
		"--source", src, "--destination", dest, "--policy", polPath,
		"--output-json", outJSON, "--output-md", filepath.Join(dir, "plan.md"),
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (policy is context, never a gate)", code)
	}
	b, _ := os.ReadFile(outJSON)
	var p accountinventory.DNSPlan
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Zones) != 1 || len(p.Zones[0].PolicyFindings) != 1 {
		t.Errorf("policy findings not attached: %+v", p.Zones)
	}
}

func TestInventoryDNSPlanCmdBadIPMap(t *testing.T) {
	dir := t.TempDir()
	src := writeDNSInventory(t, dir, "src.json", "source", "example.com", nil)
	dest := writeDNSInventory(t, dir, "dest.json", "destination", "example.com", nil)

	for _, bad := range []string{"no-equals", "1.2.3.4=", "=5.6.7.8", "notanip=5.6.7.8", "1.2.3.4=notanip"} {
		code := runInventoryDNSPlanCmd([]string{
			"--source", src, "--destination", dest, "--ip-map", bad,
			"--output-json", filepath.Join(dir, "p.json"), "--output-md", filepath.Join(dir, "p.md"),
		})
		if code != 1 {
			t.Errorf("--ip-map %q: exit = %d, want 1", bad, code)
		}
	}
}

func TestInventoryDNSPlanCmdWrongModePolicy(t *testing.T) {
	dir := t.TempDir()
	src := writeDNSInventory(t, dir, "src.json", "source", "example.com", nil)
	dest := writeDNSInventory(t, dir, "dest.json", "destination", "example.com", nil)
	notPolicy := filepath.Join(dir, "not-policy.json")
	if err := os.WriteFile(notPolicy, []byte(`{"mode":"inventory-diff"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code := runInventoryDNSPlanCmd([]string{
		"--source", src, "--destination", dest, "--policy", notPolicy,
	})
	if code != 1 {
		t.Errorf("wrong-mode policy exit = %d, want 1", code)
	}
}

func TestInventoryDNSPlanCmdMissingFlags(t *testing.T) {
	if code := runInventoryDNSPlanCmd([]string{}); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if code := runInventoryDNSPlanCmd([]string{"--no-such"}); code != 2 {
		t.Errorf("bad flag exit = %d, want 2", code)
	}
}

func TestInventoryDNSPlanCmdNotAnInventory(t *testing.T) {
	dir := t.TempDir()
	bogus := filepath.Join(dir, "x.json")
	if err := os.WriteFile(bogus, []byte(`{"mode":"other"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code := runInventoryDNSPlanCmd([]string{"--source", bogus, "--destination", bogus})
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}

func TestInventoryDNSPlanCmdMarkdownNamesManualFirst(t *testing.T) {
	dir := t.TempDir()
	src := writeDNSInventory(t, dir, "src.json", "source", "example.com",
		[]accountinventory.DNSRecordEntry{
			{Type: "A", Name: "ext", TTL: 300, Address: "203.0.113.9", Value: "203.0.113.9"},
		})
	dest := writeDNSInventory(t, dir, "dest.json", "destination", "example.com", nil)
	outMD := filepath.Join(dir, "plan.md")

	code := runInventoryDNSPlanCmd([]string{
		"--source", src, "--destination", dest,
		"--output-json", filepath.Join(dir, "plan.json"), "--output-md", outMD,
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	b, _ := os.ReadFile(outMD)
	if !strings.Contains(string(b), "MANUAL") || !strings.Contains(string(b), "203.0.113.9") {
		t.Error("markdown should surface the manual unmapped-address op")
	}
}
