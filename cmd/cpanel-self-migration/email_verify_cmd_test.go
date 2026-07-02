package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

func readEmailVerifyReport(t *testing.T, path string) accountinventory.EmailVerifyReport {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var r accountinventory.EmailVerifyReport
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("parse report: %v", err)
	}
	return r
}

func TestEmailVerifyCmdFlagAndInputErrors(t *testing.T) {
	dir := t.TempDir()
	if code := runEmailVerifyCmd([]string{"--bogus"}); code != 2 {
		t.Errorf("unknown flag: code = %d, want 2", code)
	}
	if code := runEmailVerifyCmd([]string{}); code != 1 {
		t.Errorf("missing --plan: code = %d, want 1", code)
	}
	badMode := filepath.Join(dir, "badmode.json")
	if err := os.WriteFile(badMode, []byte(`{"mode":"dns-import-plan"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runEmailVerifyCmd([]string{"--plan", badMode}); code != 1 {
		t.Errorf("wrong mode: code = %d, want 1", code)
	}
}

// The stale-plan gate refuses before any SSH and writes no report.
func TestEmailVerifyCmdStalePlanGate(t *testing.T) {
	dir := t.TempDir()
	src := writeEmailPlanInventory(t, dir, "src.json", "source", "acct", nil, nil)
	outJSON := filepath.Join(dir, "report.json")

	plan := accountinventory.EmailApplyPlan{
		Mode: "email-apply-plan", FormatVersion: 1,
		SourceSHA256: "deadbeef", Ops: []accountinventory.EmailPlanOp{},
	}
	planPath := filepath.Join(dir, "plan.json")
	if err := accountinventory.WriteEmailPlanJSON(planPath, plan); err != nil {
		t.Fatal(err)
	}
	code := runEmailVerifyCmd([]string{"--plan", planPath, "--source", src,
		"--output-json", outJSON, "--output-md", filepath.Join(dir, "report.md")})
	if code != 3 {
		t.Fatalf("code = %d, want 3 (stale-plan refusal)", code)
	}
	if _, err := os.Stat(outJSON); !os.IsNotExist(err) {
		t.Error("a refused verify must not write a report")
	}

	// A plan with no embedded hash cannot be validated — fail-safe refusal.
	plan.SourceSHA256 = ""
	if err := accountinventory.WriteEmailPlanJSON(planPath, plan); err != nil {
		t.Fatal(err)
	}
	if code := runEmailVerifyCmd([]string{"--plan", planPath, "--source", src,
		"--output-json", outJSON, "--output-md", filepath.Join(dir, "report.md")}); code != 3 {
		t.Errorf("plan without embedded hash: code = %d, want 3", code)
	}
}

// End-to-end: pending before the apply (gates with --fail-on-drift),
// clean after it.
func TestEmailVerifyCmdEndToEnd(t *testing.T) {
	cfgPath, stateDir := setupEmailServer(t)
	dir := t.TempDir()
	planPath := buildEmailTestPlan(t, dir)
	setEmailStubState(t, stateDir, nil, []string{"example.com|acct"})

	outJSON := filepath.Join(dir, "email_verify_report.json")
	outMD := filepath.Join(dir, "email_verify_report.md")

	code := runEmailVerifyCmd([]string{"--plan", planPath, "--config", cfgPath,
		"--output-json", outJSON, "--output-md", outMD})
	if code != 0 {
		t.Fatalf("verify (pre-apply): code = %d, want 0 (report written, gate only with --fail-on-drift)", code)
	}
	rep := readEmailVerifyReport(t, outJSON)
	if rep.Clean || rep.Summary.Pending != 2 {
		t.Fatalf("pre-apply: clean = %v, pending = %d, want NOT clean with 2 pending", rep.Clean, rep.Summary.Pending)
	}
	if code := runEmailVerifyCmd([]string{"--plan", planPath, "--config", cfgPath,
		"--output-json", outJSON, "--output-md", outMD, "--fail-on-drift"}); code != 3 {
		t.Errorf("--fail-on-drift pre-apply: code = %d, want 3", code)
	}

	// Apply, then verify again: clean.
	if code := runEmailApplyCmd([]string{
		"--plan", planPath, "--config", cfgPath, "--yes-apply-writes",
		"--backup", filepath.Join(dir, "b.json"), "--output-json", filepath.Join(dir, "apply.json"),
	}); code != 0 {
		t.Fatalf("apply: code = %d", code)
	}
	code = runEmailVerifyCmd([]string{"--plan", planPath, "--config", cfgPath,
		"--output-json", outJSON, "--output-md", outMD, "--fail-on-drift"})
	if code != 0 {
		t.Fatalf("verify (post-apply): code = %d, want 0 (clean)", code)
	}
	rep = readEmailVerifyReport(t, outJSON)
	if !rep.Clean || rep.Summary.Applied != 2 {
		t.Fatalf("post-apply: clean = %v, applied = %d", rep.Clean, rep.Summary.Applied)
	}
	if _, err := os.Stat(outMD); err != nil {
		t.Errorf("markdown report missing: %v", err)
	}
}
