package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

func hashFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func TestInventoryDiffCmdEmbedsInputHashes(t *testing.T) {
	dir := t.TempDir()
	src, dest, _, _ := checklistFixtureFiles(t, dir, false, nil)
	outJSON := filepath.Join(dir, "d.json")

	if code := runInventoryDiffCmd([]string{"--source", src, "--destination", dest,
		"--output-json", outJSON, "--output-md", filepath.Join(dir, "d.md")}); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	var d accountinventory.InventoryDiff
	b, err := os.ReadFile(outJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	if d.SourceSHA256 != hashFile(t, src) {
		t.Errorf("source_sha256 = %q, want the hash of the raw source file", d.SourceSHA256)
	}
	if d.DestinationSHA256 != hashFile(t, dest) {
		t.Errorf("destination_sha256 = %q, want the hash of the raw destination file", d.DestinationSHA256)
	}
}

func TestInventoryPolicyCmdEmbedsDiffHash(t *testing.T) {
	dir := t.TempDir()
	diff := writeDiffFileFor(t, dir, nil)
	outJSON := filepath.Join(dir, "p.json")

	if code := runInventoryPolicyCmd([]string{"--diff", diff,
		"--output-json", outJSON, "--output-md", filepath.Join(dir, "p.md")}); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	var p accountinventory.PolicyReport
	b, err := os.ReadFile(outJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if p.InputDiffSHA256 != hashFile(t, diff) {
		t.Errorf("input_diff_sha256 = %q, want the hash of the raw diff file", p.InputDiffSHA256)
	}
}

// TestProvenanceChainEndToEnd runs the REAL CLI pipeline: inventories →
// diff → policy → dns-plan → checklist, and expects a fully verified
// chain. Tampering with the source inventory afterwards must break it.
func TestProvenanceChainEndToEnd(t *testing.T) {
	dir := t.TempDir()
	src, dest, _, _ := checklistFixtureFiles(t, dir, false, nil)
	rep := writeApplyReport(t, dir)
	diff := filepath.Join(dir, "chain_diff.json")
	policy := filepath.Join(dir, "chain_policy.json")
	plan := filepath.Join(dir, "chain_plan.json")
	outJSON := filepath.Join(dir, "chain_checklist.json")
	outMD := filepath.Join(dir, "chain_checklist.md")

	if code := runInventoryDiffCmd([]string{"--source", src, "--destination", dest,
		"--output-json", diff, "--output-md", filepath.Join(dir, "x.md")}); code != 0 {
		t.Fatalf("diff exit = %d", code)
	}
	if code := runInventoryPolicyCmd([]string{"--diff", diff,
		"--output-json", policy, "--output-md", filepath.Join(dir, "y.md")}); code != 0 {
		t.Fatalf("policy exit = %d", code)
	}
	if code := runInventoryDNSPlanCmd([]string{"--source", src, "--destination", dest,
		"--output-json", plan, "--output-md", filepath.Join(dir, "z.md")}); code != 0 {
		t.Fatalf("dns-plan exit = %d", code)
	}
	runChecklist := func() accountinventory.MigrationChecklist {
		t.Helper()
		if code := runInventoryChecklistCmd([]string{
			"--source", src, "--destination", dest, "--diff", diff, "--policy", policy,
			"--dns-plan", plan, "--migration-report", rep,
			"--output-json", outJSON, "--output-md", outMD}); code != 0 {
			t.Fatalf("checklist exit = %d", code)
		}
		return readChecklistJSON(t, outJSON)
	}

	c := runChecklist()
	if !c.ChainVerified {
		t.Fatalf("chain_verified = false on a freshly generated pipeline (warnings: %v)", c.Warnings)
	}
	md, err := os.ReadFile(outMD)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "Chain verified**: yes") {
		t.Error("markdown must state the chain is verified")
	}

	// Tamper: regenerate the source inventory with different content but
	// do NOT regenerate diff/policy/plan → the chain must break and the
	// overall must be capped (fixture is READY_WITH_MANUAL_NOTES when
	// intact).
	if c.OverallStatus != accountinventory.OverallReadyWithManualNotes {
		t.Fatalf("fixture contract: intact overall = %q, want READY_WITH_MANUAL_NOTES", c.OverallStatus)
	}
	var inv accountinventory.NormalizedInventory
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &inv); err != nil {
		t.Fatal(err)
	}
	inv.Warnings = append(inv.Warnings, "tampered after the diff was generated")
	if err := accountinventory.WriteInventoryJSON(src, inv); err != nil {
		t.Fatal(err)
	}

	c = runChecklist()
	if c.ChainVerified {
		t.Fatal("chain_verified = true after tampering with the source inventory")
	}
	if c.OverallStatus != accountinventory.OverallNotReady {
		t.Errorf("overall = %q, want NOT_READY (proven mismatch must cap READY_*)", c.OverallStatus)
	}
}
