package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
)

// --- fixtures -------------------------------------------------------------

func writeDNSVerifyInventory(t *testing.T, dir, name, side string, zones ...accountinventory.DNSZoneResult) string {
	t.Helper()
	inv := accountinventory.NewEmptyInventory("u", "h", side)
	inv.DNS.Available = true
	inv.DNS.Zones = zones
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

func dnsVerifyZone(zone string, records ...accountinventory.DNSRecordEntry) accountinventory.DNSZoneResult {
	return accountinventory.DNSZoneResult{Available: true, Zone: zone, Method: "uapi", Records: records}
}

func dnsVerifyARec(name, addr string) accountinventory.DNSRecordEntry {
	return accountinventory.DNSRecordEntry{Type: "A", Name: name, TTL: 300, Address: addr, Value: addr}
}

func writeDNSVerifyPlanFile(t *testing.T, dir string, plan accountinventory.DNSPlan) string {
	t.Helper()
	path := filepath.Join(dir, "plan.json")
	if err := accountinventory.WriteDNSPlanJSON(path, plan); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeUAPIZoneFixture renders the raw UAPI DNS::parse_zone response the
// stub `uapi` serves: real-server shape (base64 dname/data segments).
func writeUAPIZoneFixture(t *testing.T, dir, name string, records ...accountinventory.DNSRecordEntry) string {
	t.Helper()
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	type rawRec struct {
		Type       string   `json:"type"`
		RecordType string   `json:"record_type"`
		DNameB64   string   `json:"dname_b64"`
		DataB64    []string `json:"data_b64"`
		TTL        int      `json:"ttl"`
		LineIndex  int      `json:"line_index"`
	}
	var raws []rawRec
	for i, r := range records {
		raws = append(raws, rawRec{
			Type: "record", RecordType: r.Type, DNameB64: b64(r.Name),
			DataB64: []string{b64(r.Address)}, TTL: r.TTL, LineIndex: i + 1,
		})
	}
	payload := map[string]any{"result": map[string]any{"status": 1, "data": raws}}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// setupDNSVerifyServer starts the in-process SSH server with a `uapi` stub
// that serves $CPSM_DNSVERIFY_FIX for DNS parse_zone, and writes a
// host.yaml whose DESTINATION points at it. The source block is filled
// with the same endpoint only to satisfy config.Load — verify must never
// dial it.
func setupDNSVerifyServer(t *testing.T, fixturePath string) (cfgPath string) {
	t.Helper()
	tmp := t.TempDir()
	stubDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := `#!/bin/bash
shift # drop --output=json
case "$1 $2" in
  "DNS parse_zone") cat "$CPSM_DNSVERIFY_FIX" ;;
  *) echo '{"result":{"status":0,"errors":["stub: unknown uapi call"]}}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(stubDir, "uapi"), []byte(stub), 0o755); err != nil { // #nosec G306 -- stub must be executable
		t.Fatal(err)
	}
	t.Setenv("CPSM_DNSVERIFY_FIX", fixturePath)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", tmp) // keep the TOFU known_hosts out of the real ~/.ssh

	addr := sshtest.NewExecServer(t, tmp)
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	cfgPath = filepath.Join(tmp, "host.yaml")
	yaml := fmt.Sprintf(`src:
  ip: %[1]s
  port: %[2]s
  ssh_user: u
  ssh_pass: p
  timeout: 10s
dest:
  ip: %[1]s
  port: %[2]s
  ssh_user: u
  ssh_pass: p
  timeout: 10s
`, host, port)
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

func readDNSVerifyReport(t *testing.T, path string) accountinventory.DNSVerifyReport {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var rep accountinventory.DNSVerifyReport
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatalf("parse report: %v", err)
	}
	return rep
}

// --- tests ----------------------------------------------------------------

func TestDNSVerifyCmdFlagAndInputErrors(t *testing.T) {
	dir := t.TempDir()
	if code := runDNSVerifyCmd([]string{"--definitely-not-a-flag"}); code != 2 {
		t.Errorf("unknown flag: code = %d, want 2", code)
	}
	if code := runDNSVerifyCmd([]string{}); code != 1 {
		t.Errorf("missing --plan: code = %d, want 1", code)
	}
	if code := runDNSVerifyCmd([]string{"--plan", filepath.Join(dir, "nope.json")}); code != 1 {
		t.Errorf("nonexistent plan: code = %d, want 1", code)
	}
	badMode := filepath.Join(dir, "badmode.json")
	if err := os.WriteFile(badMode, []byte(`{"mode":"inventory-policy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runDNSVerifyCmd([]string{"--plan", badMode}); code != 1 {
		t.Errorf("wrong mode: code = %d, want 1", code)
	}
	badVer := filepath.Join(dir, "badver.json")
	if err := os.WriteFile(badVer, []byte(`{"mode":"dns-import-plan","format_version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runDNSVerifyCmd([]string{"--plan", badVer}); code != 1 {
		t.Errorf("unknown format_version: code = %d, want 1", code)
	}
}

// The stale-plan gate refuses to verify a plan whose embedded input hashes
// no longer match the inventories the operator points at — BEFORE any SSH,
// and without writing a report.
func TestDNSVerifyCmdStalePlanGate(t *testing.T) {
	dir := t.TempDir()
	srcPath := writeDNSVerifyInventory(t, dir, "src.json", "source",
		dnsVerifyZone("example.com", dnsVerifyARec("example.com.", "194.76.118.193")))
	outJSON := filepath.Join(dir, "report.json")
	outMD := filepath.Join(dir, "report.md")

	t.Run("hash mismatch", func(t *testing.T) {
		planPath := writeDNSVerifyPlanFile(t, t.TempDir(), accountinventory.DNSPlan{
			Mode: "dns-import-plan", FormatVersion: 1,
			SourceSHA256: "deadbeef", Zones: []accountinventory.PlanZone{},
		})
		code := runDNSVerifyCmd([]string{"--plan", planPath, "--source", srcPath,
			"--output-json", outJSON, "--output-md", outMD})
		if code != 3 {
			t.Fatalf("code = %d, want 3 (stale-plan refusal)", code)
		}
		if _, err := os.Stat(outJSON); !os.IsNotExist(err) {
			t.Error("a refused verify must not write a report")
		}
	})

	t.Run("plan without embedded hash", func(t *testing.T) {
		planPath := writeDNSVerifyPlanFile(t, t.TempDir(), accountinventory.DNSPlan{
			Mode: "dns-import-plan", FormatVersion: 1, Zones: []accountinventory.PlanZone{},
		})
		code := runDNSVerifyCmd([]string{"--plan", planPath, "--source", srcPath,
			"--output-json", outJSON, "--output-md", outMD})
		if code != 3 {
			t.Fatalf("code = %d, want 3 (nothing to compare against is a refusal, fail-safe)", code)
		}
	})
}

// A plan whose zones all landed in manual_zones has nothing to fetch: the
// command must not need a config or open SSH, but the verdict still gates.
func TestDNSVerifyCmdManualZonesOnlyNeedsNoConfig(t *testing.T) {
	dir := t.TempDir()
	planPath := writeDNSVerifyPlanFile(t, dir, accountinventory.DNSPlan{
		Mode: "dns-import-plan", FormatVersion: 1,
		Zones: []accountinventory.PlanZone{},
		ManualZones: []accountinventory.ManualZone{
			{Zone: "example.com", Reason: "zone missing on destination — create it via WHM/park first, then re-run"}},
	})
	outJSON := filepath.Join(dir, "report.json")
	outMD := filepath.Join(dir, "report.md")

	code := runDNSVerifyCmd([]string{"--plan", planPath, "--output-json", outJSON, "--output-md", outMD})
	if code != 0 {
		t.Fatalf("code = %d, want 0 (report written, gate only with --fail-on-drift)", code)
	}
	rep := readDNSVerifyReport(t, outJSON)
	if rep.Clean || rep.Summary.ManualZones != 1 {
		t.Errorf("clean = %v, manual_zones = %d", rep.Clean, rep.Summary.ManualZones)
	}
	if _, err := os.Stat(outMD); err != nil {
		t.Errorf("markdown report missing: %v", err)
	}

	if code := runDNSVerifyCmd([]string{"--plan", planPath, "--output-json", outJSON,
		"--output-md", outMD, "--fail-on-drift"}); code != 3 {
		t.Errorf("--fail-on-drift with manual zones: code = %d, want 3", code)
	}
}

// End-to-end over the real SSH transport: dns-plan builds the plan from
// crafted inventories, the stub server serves the live zone, dns verify
// re-fetches and reports — clean and drifted variants, sha256 gate green.
func TestDNSVerifyCmdEndToEnd(t *testing.T) {
	work := t.TempDir()
	srcPath := writeDNSVerifyInventory(t, work, "src.json", "source",
		dnsVerifyZone("example.com", dnsVerifyARec("example.com.", "194.76.118.193")))
	destPath := writeDNSVerifyInventory(t, work, "dest.json", "destination",
		dnsVerifyZone("example.com"))
	planPath := filepath.Join(work, "dns_import_plan.json")
	planMD := filepath.Join(work, "dns_import_plan.md")
	if code := runInventoryDNSPlanCmd([]string{
		"--source", srcPath, "--destination", destPath,
		"--ip-map", "194.76.118.193=38.224.109.78",
		"--output-json", planPath, "--output-md", planMD,
	}); code != 0 {
		t.Fatalf("dns-plan: code = %d, want 0", code)
	}

	fixApplied := writeUAPIZoneFixture(t, work, "live_applied.json",
		dnsVerifyARec("example.com.", "38.224.109.78"))
	fixDrift := writeUAPIZoneFixture(t, work, "live_drift.json",
		dnsVerifyARec("example.com.", "9.9.9.9"))
	cfgPath := setupDNSVerifyServer(t, fixApplied)

	outJSON := filepath.Join(work, "dns_verify_report.json")
	outMD := filepath.Join(work, "dns_verify_report.md")
	args := []string{"--plan", planPath, "--config", cfgPath,
		"--source", srcPath, "--destination", destPath,
		"--output-json", outJSON, "--output-md", outMD}

	if code := runDNSVerifyCmd(append(args, "--fail-on-drift")); code != 0 {
		t.Fatalf("clean verify: code = %d, want 0", code)
	}
	rep := readDNSVerifyReport(t, outJSON)
	if !rep.Clean || rep.Summary.Applied != 1 {
		t.Fatalf("clean = %v, applied = %d (report: %+v)", rep.Clean, rep.Summary.Applied, rep.Summary)
	}
	if len(rep.Zones) != 1 || rep.Zones[0].Method != "uapi" {
		t.Errorf("zones = %+v, want one fetched via uapi", rep.Zones)
	}
	if rep.PlanFile != planPath || rep.PlanSHA256 == "" {
		t.Errorf("plan provenance missing: file=%q sha=%q", rep.PlanFile, rep.PlanSHA256)
	}
	if rep.GeneratedAt == "" {
		t.Error("generated_at missing")
	}

	// Same plan, drifted live zone: report written (exit 0), gate exits 3.
	t.Setenv("CPSM_DNSVERIFY_FIX", fixDrift)
	if code := runDNSVerifyCmd(args); code != 0 {
		t.Fatalf("drifted verify without gate: code = %d, want 0", code)
	}
	rep = readDNSVerifyReport(t, outJSON)
	if rep.Clean || rep.Summary.Drift != 1 {
		t.Fatalf("clean = %v, drift = %d", rep.Clean, rep.Summary.Drift)
	}
	if code := runDNSVerifyCmd(append(args, "--fail-on-drift")); code != 3 {
		t.Errorf("drifted verify with gate: code = %d, want 3", code)
	}
}
