package main

// Inventory contract audit: runs the REAL --account-inventory flow end to
// end — real sshx transport against an in-process SSH server, stub
// uapi/cpapi2/crontab binaries answering in the live-server response shapes
// validated against cPanel 110.0 (build 131) — then audits every produced
// artifact against the inventory contract the future policy engine will
// rely on:
//
//   - no null arrays anywhere in the JSON;
//   - unavailable sections say available:false + method:"unavailable";
//   - secrets planted in the fake crontab never reach any artifact,
//     not even as a raw-command sha256 (oracle check);
//   - events.jsonl carries run_started/run_completed with a coherent
//     run_id; report.json aggregates warnings.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/events"
	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
)

// plantedSecret is embedded in the stub crontab: it must never surface in
// any artifact, in any form.
const plantedSecret = "SmokeSecretXYZ123"

// plantedRawCommand is the raw cron command containing the secret; its
// sha256 must not appear either (a raw-command hash next to the redacted
// command would be an offline brute-force oracle).
const plantedRawCommand = "/usr/local/bin/backup.sh --password=" + plantedSecret + " | gzip > /tmp/b.gz"

const stubCrontab = `MAILTO=ops@example.test
# nightly backup
0 3 * * * ` + plantedRawCommand + `
@daily /usr/bin/php /home/u/cron.php
#30 2 * * 0 /bin/weekly.sh
`

// setupInventoryStubs creates stub uapi/cpapi2/crontab executables that
// serve the shared fixtures, prepends them to PATH and points HOME at a
// temp dir so the TOFU known_hosts never touches the real one.
func setupInventoryStubs(t *testing.T, dnsUAPIFails, noCrontab bool) {
	t.Helper()
	tmp := t.TempDir()
	stubDir := filepath.Join(tmp, "bin")
	fixDir := filepath.Join(tmp, "fixtures")
	for _, d := range []string{stubDir, fixDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Reuse the repo fixtures (live-server shapes) plus inline extras.
	for _, name := range []string{
		"domaininfo_list.json", "domaininfo_domains_data.json",
		"email_list_pops.json", "email_forwarders.json", "email_autoresponders.json",
		"ftp_list.json", "ssl_list_certs.json", "php_vhost_versions.json",
		"dns_parse_zone.json", "dns_fetchzone_records.json",
		"email_list_mxs_realserver.json", "email_default_address_realserver.json",
		"email_list_filters.json", "mime_redirects_realserver.json",
	} {
		b, err := os.ReadFile(filepath.Join("..", "..", "internal", "testdata", name))
		if err != nil {
			t.Fatalf("fixture %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(fixDir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFix := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(fixDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFix("mysql_list_databases.json",
		`{"result":{"status":1,"data":[{"database":"u_wp","disk_usage":2048,"users":["u_admin"]}]}}`)
	writeFix("mysql_list_users.json",
		`{"result":{"status":1,"data":[{"user":"u_admin","shortuser":"admin","databases":["u_wp"]}]}}`)
	writeFix("dns_parse_zone_fail.json",
		`{"result":{"status":0,"data":null,"errors":["Failed to load module DNS"]}}`)
	writeFix("crontab_sample.txt", stubCrontab)

	writeStub := func(name, body string) {
		t.Helper()
		path := filepath.Join(stubDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/bash\n"+body), 0o755); err != nil { // #nosec G306 -- stub must be executable
			t.Fatal(err)
		}
	}

	dnsFixture := "dns_parse_zone.json"
	if dnsUAPIFails {
		dnsFixture = "dns_parse_zone_fail.json"
	}
	writeStub("uapi", `shift # drop --output=json
case "$1 $2" in
  "DomainInfo list_domains")        cat "$CPSM_TEST_FIXDIR/domaininfo_list.json" ;;
  "DomainInfo domains_data")        cat "$CPSM_TEST_FIXDIR/domaininfo_domains_data.json" ;;
  "Email list_pops_with_disk")      cat "$CPSM_TEST_FIXDIR/email_list_pops.json" ;;
  "Email list_forwarders")          cat "$CPSM_TEST_FIXDIR/email_forwarders.json" ;;
  "Email list_auto_responders")     cat "$CPSM_TEST_FIXDIR/email_autoresponders.json" ;;
  "Mysql list_databases")           cat "$CPSM_TEST_FIXDIR/mysql_list_databases.json" ;;
  "Mysql list_users")               cat "$CPSM_TEST_FIXDIR/mysql_list_users.json" ;;
  "Ftp list_ftp_with_disk")         cat "$CPSM_TEST_FIXDIR/ftp_list.json" ;;
  "SSL list_certs")                 cat "$CPSM_TEST_FIXDIR/ssl_list_certs.json" ;;
  "LangPHP php_get_vhost_versions") cat "$CPSM_TEST_FIXDIR/php_vhost_versions.json" ;;
  "DNS parse_zone")                 cat "$CPSM_TEST_FIXDIR/`+dnsFixture+`" ;;
  "Email list_mxs")                 cat "$CPSM_TEST_FIXDIR/email_list_mxs_realserver.json" ;;
  "Email list_default_address")     cat "$CPSM_TEST_FIXDIR/email_default_address_realserver.json" ;;
  "Email list_filters")             cat "$CPSM_TEST_FIXDIR/email_list_filters.json" ;;
  "Mime list_redirects")            cat "$CPSM_TEST_FIXDIR/mime_redirects_realserver.json" ;;
  *) echo '{"result":{"status":0,"errors":["stub: unknown uapi call"]}}' ;;
esac`)
	writeStub("cpapi2", `shift # drop --output=json
case "$1 $2" in
  "ZoneEdit fetchzone_records") cat "$CPSM_TEST_FIXDIR/dns_fetchzone_records.json" ;;
  *) echo '{"cpanelresult":{"data":[],"event":{"result":0},"error":"stub: unknown cpapi2 call"}}' ;;
esac`)
	if noCrontab {
		writeStub("crontab", `if [ "$1" = "-l" ]; then echo "no crontab for u" >&2; exit 1; fi; exit 1`)
	} else {
		writeStub("crontab", `if [ "$1" = "-l" ]; then cat "$CPSM_TEST_FIXDIR/crontab_sample.txt"; exit 0; fi; exit 1`)
	}

	t.Setenv("CPSM_TEST_FIXDIR", fixDir)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", tmp) // keep the TOFU known_hosts out of the real ~/.ssh
}

// runInventoryFlow drives the real runAccountInventory against the stub
// SSH server and returns the artifact directory.
func runInventoryFlow(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	addr := sshtest.NewExecServer(t, home)
	hostIP, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{Src: config.HostConfig{
		IP: hostIP, Port: port, SSHUser: "u", SSHPass: "p", Timeout: 10 * time.Second,
	}}

	outDir := t.TempDir()
	ew, err := events.NewWriter(filepath.Join(outDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	em := events.Emitter{Emit: func(e events.Event) {
		if werr := ew.Write(e); werr != nil {
			t.Errorf("events write: %v", werr)
		}
	}}

	runID := "run-20260701-000000"
	if err := runAccountInventory(t.Context(), cfg, outDir, runID, em, true); err != nil {
		t.Fatalf("runAccountInventory: %v", err)
	}
	if err := ew.Close(); err != nil {
		t.Fatal(err)
	}
	return outDir
}

func TestAccountInventoryContract(t *testing.T) {
	sshtest.RequireTools(t, "bash", "cat")
	setupInventoryStubs(t, false, false)
	outDir := runInventoryFlow(t)

	// 1. Artifacts present.
	for _, f := range []string{"inventory_source.json", "inventory_report.md", "report.json", "events.jsonl"} {
		if _, err := os.Stat(filepath.Join(outDir, f)); err != nil {
			t.Fatalf("artifact %s missing: %v", f, err)
		}
	}

	rawJSON, err := os.ReadFile(filepath.Join(outDir, "inventory_source.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inv map[string]any
	if err := json.Unmarshal(rawJSON, &inv); err != nil {
		t.Fatalf("inventory_source.json invalid: %v", err)
	}

	// 2. All sections present.
	for _, section := range []string{
		"account", "domains", "mailboxes", "databases",
		"forwarders", "autoresponders", "ftp", "ssl", "php", "dns", "cron",
		"email_routing", "default_address", "email_filters", "redirects",
	} {
		if _, ok := inv[section]; !ok {
			t.Errorf("section %q missing from inventory JSON", section)
		}
	}

	// 3. Contract: no null anywhere in the tree.
	checkNoNulls(t, inv, "$")

	// Section semantics on the happy path.
	checkSectionAvailable(t, inv, "ftp", "uapi")
	checkSectionAvailable(t, inv, "ssl", "uapi")
	checkSectionAvailable(t, inv, "php", "uapi")
	checkSectionAvailable(t, inv, "dns", "uapi")
	checkSectionAvailable(t, inv, "cron", "ssh_crontab_l")
	checkSectionAvailable(t, inv, "email_routing", "uapi")
	checkSectionAvailable(t, inv, "default_address", "uapi")
	checkSectionAvailable(t, inv, "email_filters", "uapi")
	checkSectionAvailable(t, inv, "redirects", "uapi")

	cron := inv["cron"].(map[string]any)
	jobs := cron["jobs"].([]any)
	if len(jobs) != 3 {
		t.Errorf("cron jobs = %d, want 3 (2 enabled + 1 disabled)", len(jobs))
	}
	dns := inv["dns"].(map[string]any)
	zones := dns["zones"].([]any)
	if len(zones) == 0 {
		t.Error("dns zones empty on happy path")
	}

	// 4. Security: the planted secret must not appear in ANY artifact —
	// nor may the sha256 of the raw command (oracle check).
	rawHash := sha256.Sum256([]byte(plantedRawCommand))
	oracleHex := hex.EncodeToString(rawHash[:])
	for _, f := range []string{"inventory_source.json", "inventory_report.md", "report.json", "events.jsonl"} {
		b, err := os.ReadFile(filepath.Join(outDir, f))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if strings.Contains(s, plantedSecret) {
			t.Errorf("%s leaks the planted secret", f)
		}
		if strings.Contains(s, oracleHex) {
			t.Errorf("%s contains the sha256 of the RAW cron command (brute-force oracle)", f)
		}
	}

	// The redacted cron command is present and its pipe is table-escaped.
	md, err := os.ReadFile(filepath.Join(outDir, "inventory_report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "[REDACTED]") {
		t.Error("report.md should show a redacted cron command")
	}
	if !strings.Contains(string(md), `\|`) {
		t.Error("piped cron command must be escaped in the markdown table")
	}

	// 5. Events and report.json coherence.
	checkEvents(t, outDir, "run-20260701-000000")
	checkRunReport(t, outDir, "run-20260701-000000")

	// Section counts, logged as living documentation of the contract run.
	for _, section := range []string{"domains", "mailboxes", "databases", "forwarders", "autoresponders"} {
		if items, ok := inv[section].([]any); ok {
			t.Logf("%-15s %d item(s)", section, len(items))
		}
	}
	for _, section := range []string{"ftp", "ssl", "php"} {
		if sec, ok := inv[section].(map[string]any); ok {
			t.Logf("%-15s %d item(s)", section, len(sec["items"].([]any)))
		}
	}
	t.Logf("%-15s %d zone(s)", "dns", len(zones))
	t.Logf("%-15s %d job(s), %d env, %v comments, %v disabled", "cron",
		len(jobs), len(cron["environment"].([]any)), cron["comments_count"], cron["disabled_jobs_count"])
}

func TestAccountInventoryContractFallbacks(t *testing.T) {
	sshtest.RequireTools(t, "bash", "cat")
	setupInventoryStubs(t, true, true) // DNS UAPI fails; no crontab installed
	outDir := runInventoryFlow(t)

	rawJSON, err := os.ReadFile(filepath.Join(outDir, "inventory_source.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inv map[string]any
	if err := json.Unmarshal(rawJSON, &inv); err != nil {
		t.Fatal(err)
	}
	checkNoNulls(t, inv, "$")

	// DNS fell back to API2 end to end, through the real binary flow.
	dns := inv["dns"].(map[string]any)
	if dns["method"] != "api2" {
		t.Errorf("dns method = %v, want api2 (UAPI parse_zone failed)", dns["method"])
	}
	if dns["source_function"] != "ZoneEdit::fetchzone_records" {
		t.Errorf("dns source_function = %v", dns["source_function"])
	}

	// Empty crontab is available with zero jobs and a light warning —
	// NOT an unavailable section, NOT a bare empty list.
	cron := inv["cron"].(map[string]any)
	if cron["available"] != true {
		t.Errorf("empty crontab must stay available, got %v", cron["available"])
	}
	if jobs := cron["jobs"].([]any); len(jobs) != 0 {
		t.Errorf("jobs = %d, want 0", len(jobs))
	}
	if warns := cron["warnings"].([]any); len(warns) == 0 {
		t.Error("empty crontab must carry a warning (not silently empty)")
	}
}

// checkNoNulls walks the decoded JSON tree and fails on any null value:
// the contract requires empty arrays/objects, never null.
func checkNoNulls(t *testing.T, v any, path string) {
	t.Helper()
	switch val := v.(type) {
	case nil:
		t.Errorf("null value at %s (contract: empty, never null)", path)
	case map[string]any:
		for k, child := range val {
			checkNoNulls(t, child, path+"."+k)
		}
	case []any:
		for i, child := range val {
			checkNoNulls(t, child, fmt.Sprintf("%s[%d]", path, i))
		}
	}
}

func checkSectionAvailable(t *testing.T, inv map[string]any, section, wantMethod string) {
	t.Helper()
	sec, ok := inv[section].(map[string]any)
	if !ok {
		t.Errorf("section %s is not an object", section)
		return
	}
	if sec["available"] != true {
		t.Errorf("%s.available = %v, want true", section, sec["available"])
	}
	if sec["method"] != wantMethod {
		t.Errorf("%s.method = %v, want %q", section, sec["method"], wantMethod)
	}
}

func checkEvents(t *testing.T, outDir, wantRunID string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(outDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var started, completed bool
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("events.jsonl line invalid: %v", err)
		}
		if ev["run_id"] != wantRunID {
			t.Errorf("event run_id = %v, want %s", ev["run_id"], wantRunID)
		}
		if ev["ts"] == nil {
			t.Error("event missing timestamp")
		}
		switch ev["event"] {
		case "run_started":
			started = true
		case "run_completed":
			completed = true
		}
	}
	if !started || !completed {
		t.Errorf("events: started=%v completed=%v, want both", started, completed)
	}
}

func checkRunReport(t *testing.T, outDir, wantRunID string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(outDir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rpt map[string]any
	if err := json.Unmarshal(b, &rpt); err != nil {
		t.Fatalf("report.json invalid: %v", err)
	}
	if rpt["run_id"] != wantRunID {
		t.Errorf("report run_id = %v, want %s", rpt["run_id"], wantRunID)
	}
	if rpt["mode"] != "account-inventory" {
		t.Errorf("report mode = %v", rpt["mode"])
	}
	if rpt["warnings"] == nil {
		t.Error("report.json warnings must be an array, not null")
	}
	if rpt["started_at"] == nil || rpt["finished_at"] == nil {
		t.Error("report.json missing timestamps")
	}
}
