package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
)

// --- stateful uapi stub for DNS -----------------------------------------------

// dnsStubScript is a STATEFUL `uapi` stub for DNS ops: zone records
// live in a flat JSON state file per zone under $CPSM_DNS_STATE. The
// stub handles DNS parse_zone and DNS mass_edit_zone (add and remove).
// bash 3.2 compatible (macOS).
const dnsStubScript = `#!/bin/bash
shift # drop --output=json
mod="$1"; fn="$2"; shift 2
zone=""; serial=""
for kv in "$@"; do
  case "$kv" in
    zone=*) zone="${kv#zone=}";;
    serial=*) serial="${kv#serial=}";;
  esac
done
S="$CPSM_DNS_STATE"
ZFILE="$S/${zone}.json"
case "$mod $fn" in
  "DNS parse_zone")
    if [ -f "$ZFILE" ]; then
      cat "$ZFILE"
    else
      echo '{"result":{"status":1,"data":[]}}'
    fi
    ;;
  "DNS mass_edit_zone")
    if [ ! -f "$ZFILE" ]; then
      echo '{"result":{"status":0,"errors":["zone not found"]}}'
      exit 0
    fi
    # Read current serial from state
    cur_serial=$(python3 -c "
import json,sys,base64
with open('$ZFILE') as f: data=json.load(f)
for r in data['result']['data']:
  if r.get('record_type')=='SOA' and len(r.get('data_b64',[]))>=3:
    print(base64.b64decode(r['data_b64'][2]).decode().strip())
    break
" 2>/dev/null)
    if [ -n "$serial" ] && [ -n "$cur_serial" ] && [ "$serial" != "$cur_serial" ]; then
      echo "{\"result\":{\"status\":0,\"errors\":[\"The serial number $serial does not match the DNS zone serial $cur_serial\"]}}"
      exit 0
    fi
    # Collect add-N and remove-N args
    adds=""
    removes=""
    for kv in "$@"; do
      case "$kv" in
        add-*=*) adds="$adds ${kv#add-*=}";;
        remove-*=*) removes="$removes ${kv#remove-*=}";;
      esac
    done
    # Process via python3 (manipulate JSON state)
    new_serial=$((cur_serial + 1))
    python3 -c "
import json, sys, base64, os
zfile = '$ZFILE'
with open(zfile) as f:
    state = json.load(f)
records = state['result']['data']
# Process removes first (by line_index)
remove_lines = set()
for kv in '''$@'''.split():
    if kv.startswith('remove-') and '=' in kv:
        idx = kv.split('=',1)[1]
        try: remove_lines.add(int(idx))
        except: pass
if remove_lines:
    records = [r for r in records if r.get('line_index') not in remove_lines]
# Process adds
max_line = max((r.get('line_index',0) for r in records), default=0)
for kv in '''$@'''.split():
    if kv.startswith('add-') and '=' in kv:
        rec_json = kv.split('=',1)[1]
        try:
            rec = json.loads(rec_json)
            max_line += 1
            data_b64 = [base64.b64encode(d.encode()).decode() for d in rec.get('data',[])]
            records.append({
                'type': 'record',
                'record_type': rec['record_type'],
                'dname_b64': base64.b64encode(rec['dname'].encode()).decode(),
                'data_b64': data_b64,
                'ttl': rec.get('ttl', 300),
                'line_index': max_line,
            })
        except Exception as e:
            print(f'stub add parse error: {e}', file=sys.stderr)
# Update SOA serial
new_ser = $new_serial
for r in records:
    if r.get('record_type') == 'SOA' and len(r.get('data_b64',[])) >= 3:
        r['data_b64'][2] = base64.b64encode(str(new_ser).encode()).decode()
state['result']['data'] = records
with open(zfile, 'w') as f:
    json.dump(state, f)
" 2>/dev/null
    echo "{\"result\":{\"status\":1,\"data\":{\"new_serial\":\"$new_serial\"}}}"
    ;;
  *) echo '{"result":{"status":0,"errors":["stub: unknown uapi call"]}}';;
esac
`

// setupDNSApplyServer starts the in-process SSH server with the
// stateful DNS uapi stub and writes a host.yaml pointing at it.
func setupDNSApplyServer(t *testing.T) (cfgPath, stateDir string) {
	t.Helper()
	tmp := t.TempDir()
	stubDir := filepath.Join(tmp, "bin")
	stateDir = filepath.Join(tmp, "state")
	for _, d := range []string{stubDir, stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stubDir, "uapi"), []byte(dnsStubScript), 0o755); err != nil { // #nosec G306 -- stub must be executable
		t.Fatal(err)
	}
	t.Setenv("CPSM_DNS_STATE", stateDir)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", tmp)

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
	return cfgPath, stateDir
}

// writeDNSZoneState writes a UAPI parse_zone response for a zone into
// the stub state directory.
func writeDNSZoneState(t *testing.T, stateDir, zone string, records []accountinventory.DNSRecordEntry, serial int) {
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
	// Always include a SOA record for serial tracking.
	raws = append(raws, rawRec{
		Type: "record", RecordType: "SOA", DNameB64: b64(zone + "."),
		DataB64: []string{b64("ns1." + zone + "."), b64("admin." + zone + "."), b64(fmt.Sprintf("%d", serial))},
		TTL:     86400, LineIndex: 1,
	})
	for i, r := range records {
		rec := rawRec{
			Type: "record", RecordType: r.Type, DNameB64: b64(r.Name),
			TTL: r.TTL, LineIndex: i + 2,
		}
		switch r.Type {
		case "A", "AAAA":
			rec.DataB64 = []string{b64(r.Address)}
		case "CNAME":
			rec.DataB64 = []string{b64(r.Target)}
		case "MX":
			rec.DataB64 = []string{b64(fmt.Sprintf("%d", r.Priority)), b64(r.Exchange)}
		case "TXT":
			rec.DataB64 = []string{b64(r.TxtData)}
		default:
			rec.DataB64 = []string{b64(r.Value)}
		}
		raws = append(raws, rec)
	}
	payload := map[string]any{"result": map[string]any{"status": 1, "data": raws}}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, zone+".json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeDNSTestPlan writes a minimal dns_import_plan.json with the
// given zones.
func writeDNSTestPlan(t *testing.T, dir string, zones []accountinventory.PlanZone) string {
	t.Helper()
	plan := accountinventory.DNSPlan{
		Mode:          "dns-import-plan",
		FormatVersion: 1,
		IPMap:         map[string]string{},
		Zones:         zones,
	}
	for _, z := range zones {
		for _, op := range z.Ops {
			switch op.Action {
			case accountinventory.ActionAdd:
				plan.Summary.Add++
			case accountinventory.ActionReplace:
				plan.Summary.Replace++
			case accountinventory.ActionManual:
				plan.Summary.Manual++
			case accountinventory.ActionSkip:
				plan.Summary.Skip++
			}
		}
	}
	planPath := filepath.Join(dir, "dns_import_plan.json")
	b, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return planPath
}

func readDNSApplyReport(t *testing.T, path string) accountinventory.DNSApplyReport {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var r accountinventory.DNSApplyReport
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("parse report: %v", err)
	}
	return r
}

// --- flag/input errors -------------------------------------------------------

func TestDNSApplyCmdFlagAndInputErrors(t *testing.T) {
	dir := t.TempDir()
	if code := runDNSApplyCmd([]string{"--bogus"}); code != 2 {
		t.Errorf("unknown flag: code = %d, want 2", code)
	}
	if code := runDNSApplyCmd([]string{}); code != 1 {
		t.Errorf("missing --plan: code = %d, want 1", code)
	}
	if code := runDNSApplyCmd([]string{"--plan", "x.json", "--rollback", "y.json"}); code != 2 {
		t.Errorf("--plan + --rollback: code = %d, want 2", code)
	}
	if code := runDNSApplyCmd([]string{"--plan", "x.json", "--report", "y.json"}); code != 2 {
		t.Errorf("--report without --rollback: code = %d, want 2", code)
	}
	badMode := filepath.Join(dir, "badmode.json")
	if err := os.WriteFile(badMode, []byte(`{"mode":"email-apply-plan"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runDNSApplyCmd([]string{"--plan", badMode}); code != 1 {
		t.Errorf("wrong mode: code = %d, want 1", code)
	}
	badVer := filepath.Join(dir, "badver.json")
	if err := os.WriteFile(badVer, []byte(`{"mode":"dns-import-plan","format_version":9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runDNSApplyCmd([]string{"--plan", badVer}); code != 1 {
		t.Errorf("unknown format_version: code = %d, want 1", code)
	}
}

// --- dry-run -----------------------------------------------------------------

func TestDNSApplyCmdDryRunIsOffline(t *testing.T) {
	dir := t.TempDir()
	planPath := writeDNSTestPlan(t, dir, []accountinventory.PlanZone{
		{
			Zone: "example.com",
			Ops: []accountinventory.PlanOp{
				{
					Action: accountinventory.ActionAdd,
					Type:   "A",
					Name:   "www.example.com.",
					Records: []accountinventory.PlanRecord{
						{Name: "www.example.com.", Type: "A", TTL: 300, Data: []string{"1.2.3.4"}},
					},
				},
			},
		},
	})

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	if err := os.Chdir(empty); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	if code := runDNSApplyCmd([]string{"--plan", planPath}); code != 0 {
		t.Fatalf("dry-run: code = %d, want 0 (offline, no config needed)", code)
	}
	entries, err := os.ReadDir(empty)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("dry-run wrote artifacts: %v", entries)
	}
}

// --- apply and verify --------------------------------------------------------

func TestDNSApplyCmdApplyAndVerify(t *testing.T) {
	cfgPath, stateDir := setupDNSApplyServer(t)
	dir := t.TempDir()

	// Set up the destination zone with just a SOA (empty zone).
	writeDNSZoneState(t, stateDir, "example.com", nil, 2024010100)

	planPath := writeDNSTestPlan(t, dir, []accountinventory.PlanZone{
		{
			Zone: "example.com",
			Ops: []accountinventory.PlanOp{
				{
					Action: accountinventory.ActionAdd,
					Type:   "A",
					Name:   "www.example.com.",
					Records: []accountinventory.PlanRecord{
						{Name: "www.example.com.", Type: "A", TTL: 300, Data: []string{"1.2.3.4"}},
					},
				},
				{
					Action: accountinventory.ActionSkip,
					Type:   "A",
					Name:   "example.com.",
					Reason: "identical",
				},
				{
					Action: accountinventory.ActionReplace,
					Type:   "MX",
					Name:   "example.com.",
					Records: []accountinventory.PlanRecord{
						{Name: "example.com.", Type: "MX", TTL: 300, Data: []string{"10", "mail.example.com."}},
					},
				},
			},
		},
	})

	outJSON := filepath.Join(dir, "dns_apply_report.json")
	backupPath := filepath.Join(dir, "dns_backup_test.json")

	code := runDNSApplyCmd([]string{
		"--plan", planPath, "--config", cfgPath, "--yes-apply-writes",
		"--backup", backupPath, "--output-json", outJSON,
	})
	if code != 0 {
		t.Fatalf("apply: code = %d, want 0", code)
	}

	rep := readDNSApplyReport(t, outJSON)

	// Check summary: 1 applied (add A), 1 skipped (skip + replace_v1 counted together).
	if rep.Summary.Applied != 1 {
		t.Errorf("summary.Applied = %d, want 1", rep.Summary.Applied)
	}
	if rep.Summary.Failed != 0 {
		t.Errorf("summary.Failed = %d, want 0", rep.Summary.Failed)
	}
	if rep.Summary.Refused != 0 {
		t.Errorf("summary.Refused = %d, want 0", rep.Summary.Refused)
	}
	// skipped = ActionSkip + skipped_replace_v1
	if rep.Summary.Skipped != 2 {
		t.Errorf("summary.Skipped = %d, want 2", rep.Summary.Skipped)
	}

	// Check per-op statuses.
	if len(rep.Zones) != 1 {
		t.Fatalf("zones = %d, want 1", len(rep.Zones))
	}
	zr := rep.Zones[0]
	if len(zr.Ops) != 3 {
		t.Fatalf("ops = %d, want 3", len(zr.Ops))
	}
	for _, op := range zr.Ops {
		switch {
		case op.Type == "A" && strings.Contains(op.Name, "www"):
			if op.Status != accountinventory.DNSOpApplied {
				t.Errorf("A www: status = %q, want applied", op.Status)
			}
		case op.Type == "A":
			if op.Status != accountinventory.DNSOpSkipped {
				t.Errorf("A apex skip: status = %q, want skipped", op.Status)
			}
		case op.Type == "MX":
			if op.Status != accountinventory.DNSOpSkippedReplaceV1 {
				t.Errorf("MX replace: status = %q, want skipped_replace_v1", op.Status)
			}
		}
	}

	// Bidirectional pairing: report <-> backup.
	if rep.BackupFile != backupPath || rep.BackupSHA256 == "" {
		t.Errorf("report backup pairing = %q/%q", rep.BackupFile, rep.BackupSHA256)
	}

	// Read the zone state to confirm the record was actually added.
	zoneState, err := os.ReadFile(filepath.Join(stateDir, "example.com.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(zoneState), base64.StdEncoding.EncodeToString([]byte("1.2.3.4"))) {
		t.Error("A record 1.2.3.4 not found in zone state after apply")
	}
}

// --- rollback ----------------------------------------------------------------

func TestDNSApplyCmdRollback(t *testing.T) {
	cfgPath, stateDir := setupDNSApplyServer(t)
	dir := t.TempDir()

	// Set up zone.
	writeDNSZoneState(t, stateDir, "example.com", nil, 2024010100)

	planPath := writeDNSTestPlan(t, dir, []accountinventory.PlanZone{
		{
			Zone: "example.com",
			Ops: []accountinventory.PlanOp{
				{
					Action: accountinventory.ActionAdd,
					Type:   "A",
					Name:   "test.example.com.",
					Records: []accountinventory.PlanRecord{
						{Name: "test.example.com.", Type: "A", TTL: 300, Data: []string{"5.6.7.8"}},
					},
				},
			},
		},
	})

	outJSON := filepath.Join(dir, "dns_apply_report.json")
	backupPath := filepath.Join(dir, "dns_backup_test.json")

	// Apply first.
	code := runDNSApplyCmd([]string{
		"--plan", planPath, "--config", cfgPath, "--yes-apply-writes",
		"--backup", backupPath, "--output-json", outJSON,
	})
	if code != 0 {
		t.Fatalf("apply: code = %d, want 0", code)
	}

	rep := readDNSApplyReport(t, outJSON)
	if rep.Summary.Applied != 1 {
		t.Fatalf("apply summary = %+v, want 1 applied", rep.Summary)
	}

	// Verify the record is present.
	zoneState, err := os.ReadFile(filepath.Join(stateDir, "example.com.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(zoneState), base64.StdEncoding.EncodeToString([]byte("5.6.7.8"))) {
		t.Fatal("A record 5.6.7.8 not found in zone state after apply")
	}

	// Rollback dry-run: offline, writes nothing.
	rbJSON := filepath.Join(dir, "dns_rollback_report.json")
	if code := runDNSApplyCmd([]string{"--rollback", backupPath, "--output-json", rbJSON}); code != 0 {
		t.Fatalf("rollback dry-run: code = %d, want 0", code)
	}
	if _, err := os.Stat(rbJSON); !os.IsNotExist(err) {
		t.Error("rollback dry-run must not write a report")
	}

	// Real rollback.
	if code := runDNSApplyCmd([]string{
		"--rollback", backupPath, "--config", cfgPath, "--yes-apply-writes",
		"--output-json", rbJSON,
	}); code != 0 {
		t.Fatalf("rollback: code = %d, want 0", code)
	}

	rb := readDNSApplyReport(t, rbJSON)
	if rb.RunMode != "rollback" {
		t.Errorf("rollback report run_mode = %q, want rollback", rb.RunMode)
	}
	if rb.Summary.Applied != 1 {
		t.Fatalf("rollback summary = %+v, want 1 applied (removed)", rb.Summary)
	}

	// Verify the record was removed.
	zoneState, err = os.ReadFile(filepath.Join(stateDir, "example.com.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(zoneState), base64.StdEncoding.EncodeToString([]byte("5.6.7.8"))) {
		t.Error("A record 5.6.7.8 still present after rollback")
	}
}
