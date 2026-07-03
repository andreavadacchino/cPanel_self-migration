package main

import (
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

// --- stateful uapi stub -----------------------------------------------------

// emailStubScript is a STATEFUL `uapi` stub: forwarders and default
// addresses live in flat files under $CPSM_EMAIL_STATE, so the E2E tests
// exercise the real write→re-list→verify-after loop, including cPanel's
// dedupe-on-identical-add behavior (2B-pre finding 2). bash 3.2
// compatible (macOS): no associative arrays.
const emailStubScript = `#!/bin/bash
shift # drop --output=json
mod="$1"; fn="$2"; shift 2
domain=""; email=""; fwdemail=""; fwdopt=""; address=""; forwarder=""; failmsgs=""
from=""; subject=""; body=""; is_html=""; interval=""; start=""; stop=""; charset=""
for kv in "$@"; do
  case "$kv" in
    domain=*) domain="${kv#domain=}";;
    email=*) email="${kv#email=}";;
    fwdemail=*) fwdemail="${kv#fwdemail=}";;
    fwdopt=*) fwdopt="${kv#fwdopt=}";;
    address=*) address="${kv#address=}";;
    forwarder=*) forwarder="${kv#forwarder=}";;
    failmsgs=*) failmsgs="${kv#failmsgs=}";;
    from=*) from="${kv#from=}";;
    subject=*) subject="${kv#subject=}";;
    body=*) body="${kv#body=}";;
    is_html=*) is_html="${kv#is_html=}";;
    interval=*) interval="${kv#interval=}";;
    start=*) start="${kv#start=}";;
    stop=*) stop="${kv#stop=}";;
    charset=*) charset="${kv#charset=}";;
  esac
done
S="$CPSM_EMAIL_STATE"
FW="$S/forwarders.txt"; DF="$S/defaults.txt"; AR="$S/autoresponders.txt"
touch "$FW" "$DF" "$AR"
case "$mod $fn" in
  "Email list_forwarders")
    out=""; first=1
    while IFS='|' read -r a t; do
      [ -z "$a" ] && continue
      case "$a" in *"@$domain") ;; *) continue;; esac
      if [ $first = 1 ]; then first=0; else out="$out,"; fi
      out="$out{\"dest\":\"$a\",\"forward\":\"$t\"}"
    done < "$FW"
    echo "{\"result\":{\"status\":1,\"data\":[$out]}}"
    ;;
  "Email add_forwarder")
    a="$email@$domain"
    if ! grep -qF "$a|$fwdemail" "$FW"; then
      echo "$a|$fwdemail" >> "$FW"
    fi
    echo "{\"result\":{\"status\":1,\"data\":{\"forward\":\"$fwdemail\",\"domain\":\"$domain\",\"email\":\"$a\"}}}"
    ;;
  "Email delete_forwarder")
    grep -vF "$address|$forwarder" "$FW" > "$FW.tmp" || true
    mv "$FW.tmp" "$FW"
    echo '{"result":{"status":1,"data":null}}'
    ;;
  "Email list_default_address")
    out=""; first=1
    while IFS='|' read -r d v; do
      [ -z "$d" ] && continue
      if [ $first = 1 ]; then first=0; else out="$out,"; fi
      out="$out{\"domain\":\"$d\",\"defaultaddress\":\"$v\"}"
    done < "$DF"
    echo "{\"result\":{\"status\":1,\"data\":[$out]}}"
    ;;
  "Email set_default_address")
    v="$fwdemail"
    case "$fwdopt" in
      fail) v=":fail: ${failmsgs:-no such address}";;
      blackhole) v=":blackhole:";;
    esac
    grep -v "^$domain|" "$DF" > "$DF.tmp" || true
    mv "$DF.tmp" "$DF"
    echo "$domain|$v" >> "$DF"
    echo "{\"result\":{\"status\":1,\"data\":[{\"dest\":\"$v\",\"domain\":\"$domain\"}]}}"
    ;;
  "Email list_auto_responders")
    out=""; first=1
    while IFS='|' read -r a subj; do
      [ -z "$a" ] && continue
      case "$a" in *"@$domain") ;; *) continue;; esac
      if [ $first = 1 ]; then first=0; else out="$out,"; fi
      out="$out{\"email\":\"$a\",\"subject\":\"$subj\"}"
    done < "$AR"
    echo "{\"result\":{\"status\":1,\"data\":[$out]}}"
    ;;
  "Email add_auto_responder")
    a="$email@$domain"
    # race simulation hook: creating aaa-trigger also creates a FOREIGN
    # autoresponder on zzz-victim (a human racing the tool mid-run).
    if [ "$email" = "aaa-trigger" ]; then
      v="zzz-victim@$domain"
      printf '%s' "{\"body\":\"Foreign content.\\n\",\"charset\":\"utf-8\",\"from\":\"Human\",\"interval\":1,\"is_html\":0,\"start\":null,\"stop\":null,\"subject\":\"Foreign\"}" > "$S/ar_$v.json"
      grep -v "^$v|" "$AR" > "$AR.tmp" || true
      mv "$AR.tmp" "$AR"
      echo "$v|Foreign" >> "$AR"
    fi
    body_json=$(printf '%s' "$body" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' | awk '{printf "%s\\n", $0}')
    [ -z "$body_json" ] && body_json='\n'
    printf '%s' "{\"body\":\"$body_json\",\"charset\":\"${charset:-utf-8}\",\"from\":\"$from\",\"interval\":${interval:-0},\"is_html\":${is_html:-0},\"start\":${start:-null},\"stop\":${stop:-null},\"subject\":\"$subject\"}" > "$S/ar_$a.json"
    grep -v "^$a|" "$AR" > "$AR.tmp" || true
    mv "$AR.tmp" "$AR"
    echo "$a|$subject" >> "$AR"
    echo '{"result":{"status":1,"data":null}}'
    ;;
  "Email get_auto_responder")
    if [ -f "$S/ar_$email.json" ]; then
      echo "{\"result\":{\"status\":1,\"data\":$(cat "$S/ar_$email.json")}}"
    else
      echo '{"result":{"status":1,"data":{"charset":"utf-8"}}}'
    fi
    ;;
  "Email delete_auto_responder")
    grep -v "^$email|" "$AR" > "$AR.tmp" || true
    mv "$AR.tmp" "$AR"
    rm -f "$S/ar_$email.json"
    echo '{"result":{"status":1,"data":null}}'
    ;;
  *) echo '{"result":{"status":0,"errors":["stub: unknown uapi call"]}}';;
esac
`

// setupEmailServer starts the in-process SSH server with the stateful
// uapi stub and writes a host.yaml whose DESTINATION points at it.
func setupEmailServer(t *testing.T) (cfgPath, stateDir string) {
	t.Helper()
	tmp := t.TempDir()
	stubDir := filepath.Join(tmp, "bin")
	stateDir = filepath.Join(tmp, "state")
	for _, d := range []string{stubDir, stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stubDir, "uapi"), []byte(emailStubScript), 0o755); err != nil { // #nosec G306 -- stub must be executable
		t.Fatal(err)
	}
	t.Setenv("CPSM_EMAIL_STATE", stateDir)
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
	return cfgPath, stateDir
}

func setEmailStubState(t *testing.T, stateDir string, forwarders, defaults []string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(stateDir, "forwarders.txt"),
		[]byte(strings.Join(forwarders, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "defaults.txt"),
		[]byte(strings.Join(defaults, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readEmailStubState(t *testing.T, stateDir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stateDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// buildEmailTestPlan writes crafted inventories and runs the real
// email-plan command, returning the plan path.
func buildEmailTestPlan(t *testing.T, dir string) string {
	t.Helper()
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
	planPath := filepath.Join(dir, "email_apply_plan.json")
	if code := runInventoryEmailPlanCmd([]string{
		"--source", src, "--destination", dest,
		"--output-json", planPath, "--output-md", filepath.Join(dir, "email_apply_plan.md"),
	}); code != 0 {
		t.Fatalf("email-plan: code = %d, want 0", code)
	}
	return planPath
}

func readEmailApplyReport(t *testing.T, path string) accountinventory.EmailApplyReport {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	var r accountinventory.EmailApplyReport
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("parse report: %v", err)
	}
	return r
}

// --- flag/input errors ------------------------------------------------------

func TestEmailApplyCmdFlagAndInputErrors(t *testing.T) {
	dir := t.TempDir()
	if code := runEmailApplyCmd([]string{"--bogus"}); code != 2 {
		t.Errorf("unknown flag: code = %d, want 2", code)
	}
	if code := runEmailApplyCmd([]string{}); code != 1 {
		t.Errorf("missing --plan: code = %d, want 1", code)
	}
	if code := runEmailApplyCmd([]string{"--plan", "x.json", "--rollback", "y.json"}); code != 2 {
		t.Errorf("--plan + --rollback: code = %d, want 2", code)
	}
	if code := runEmailApplyCmd([]string{"--plan", "x.json", "--report", "y.json"}); code != 2 {
		t.Errorf("--report without --rollback: code = %d, want 2", code)
	}
	badMode := filepath.Join(dir, "badmode.json")
	if err := os.WriteFile(badMode, []byte(`{"mode":"dns-import-plan"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runEmailApplyCmd([]string{"--plan", badMode}); code != 1 {
		t.Errorf("wrong mode: code = %d, want 1", code)
	}
	badVer := filepath.Join(dir, "badver.json")
	if err := os.WriteFile(badVer, []byte(`{"mode":"email-apply-plan","format_version":9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runEmailApplyCmd([]string{"--plan", badVer}); code != 1 {
		t.Errorf("unknown format_version: code = %d, want 1", code)
	}
}

// The default dry-run is fully offline: it must succeed with NO host.yaml
// anywhere (chdir into an empty dir) and write NO artifact.
func TestEmailApplyCmdDryRunIsOffline(t *testing.T) {
	dir := t.TempDir()
	planPath := buildEmailTestPlan(t, dir)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	if err := os.Chdir(empty); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()

	if code := runEmailApplyCmd([]string{"--plan", planPath}); code != 0 {
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

// --- apply end-to-end -------------------------------------------------------

func TestEmailApplyCmdEndToEnd(t *testing.T) {
	cfgPath, stateDir := setupEmailServer(t)
	dir := t.TempDir()
	planPath := buildEmailTestPlan(t, dir)
	setEmailStubState(t, stateDir, nil, []string{"example.com|acct"})

	outJSON := filepath.Join(dir, "email_apply_report.json")
	backupPath := filepath.Join(dir, "email_backup_test.json")

	code := runEmailApplyCmd([]string{
		"--plan", planPath, "--config", cfgPath, "--yes-apply-writes",
		"--backup", backupPath, "--output-json", outJSON,
	})
	if code != 0 {
		t.Fatalf("apply: code = %d, want 0", code)
	}

	rep := readEmailApplyReport(t, outJSON)
	if rep.Summary.Applied != 2 || rep.Summary.Failed != 0 || rep.Summary.Refused != 0 {
		t.Fatalf("summary = %+v, want 2 applied", rep.Summary)
	}
	// The destination state actually changed.
	if fw := readEmailStubState(t, stateDir, "forwarders.txt"); !strings.Contains(fw, "info@example.com|someone@gmail.com") {
		t.Errorf("forwarder not written: %q", fw)
	}
	if df := readEmailStubState(t, stateDir, "defaults.txt"); !strings.Contains(df, "example.com|someone@gmail.com") {
		t.Errorf("default not written: %q", df)
	}
	// Bidirectional pairing: report ↔ backup.
	if rep.BackupFile != backupPath || rep.BackupSHA256 == "" {
		t.Errorf("report backup pairing = %q/%q", rep.BackupFile, rep.BackupSHA256)
	}
	b, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	var backup accountinventory.EmailBackup
	if err := json.Unmarshal(b, &backup); err != nil {
		t.Fatal(err)
	}
	if backup.Mode != "email-apply-backup" || backup.ReportFile != outJSON {
		t.Errorf("backup = mode %q report %q", backup.Mode, backup.ReportFile)
	}
	// The backup archived the PRE-write state (default still "acct",
	// no forwarders) with the raw responses.
	if backup.DefaultAddresses == nil || len(backup.DefaultAddresses.Defaults) != 1 ||
		backup.DefaultAddresses.Defaults[0].DefaultAddress != "acct" {
		t.Errorf("backup defaults = %+v, want the pre-write value acct", backup.DefaultAddresses)
	}
	if backup.DefaultAddresses != nil && len(backup.DefaultAddresses.RawUAPIResponse) == 0 {
		t.Error("backup missing the raw UAPI response")
	}

	// Re-run: idempotent convergence — everything already_present, no new
	// backup (nothing to write), exit 0.
	outJSON2 := filepath.Join(dir, "email_apply_report2.json")
	code = runEmailApplyCmd([]string{
		"--plan", planPath, "--config", cfgPath, "--yes-apply-writes",
		"--output-json", outJSON2,
	})
	if code != 0 {
		t.Fatalf("re-apply: code = %d, want 0", code)
	}
	rep2 := readEmailApplyReport(t, outJSON2)
	if rep2.Summary.AlreadyPresent != 2 || rep2.Summary.Applied != 0 {
		t.Fatalf("re-apply summary = %+v, want 2 already_present", rep2.Summary)
	}
	if rep2.BackupFile != "" || rep2.BackupNote == "" {
		t.Errorf("re-apply must not create a backup: file=%q note=%q", rep2.BackupFile, rep2.BackupNote)
	}
}

// The freshness guard: a destination that moved to a THIRD state since
// the plan refuses the op (fail-closed, exit 3) and keeps going.
func TestEmailApplyCmdRefusedPrecondition(t *testing.T) {
	cfgPath, stateDir := setupEmailServer(t)
	dir := t.TempDir()
	planPath := buildEmailTestPlan(t, dir)
	// The default moved to a third value; the forwarder address gained a
	// different forward.
	setEmailStubState(t, stateDir,
		[]string{"info@example.com|third@party.com"},
		[]string{"example.com|third@party.com"})

	outJSON := filepath.Join(dir, "email_apply_report.json")
	code := runEmailApplyCmd([]string{
		"--plan", planPath, "--config", cfgPath, "--yes-apply-writes",
		"--backup", filepath.Join(dir, "b.json"), "--output-json", outJSON,
	})
	if code != 3 {
		t.Fatalf("code = %d, want 3 (gated refusal)", code)
	}
	rep := readEmailApplyReport(t, outJSON)
	if rep.Summary.Refused != 2 || rep.Summary.Applied != 0 {
		t.Fatalf("summary = %+v, want 2 refused", rep.Summary)
	}
	// Nothing was written.
	if fw := readEmailStubState(t, stateDir, "forwarders.txt"); strings.Contains(fw, "someone@gmail.com") {
		t.Errorf("refused op still wrote: %q", fw)
	}
}

// --- rollback end-to-end ----------------------------------------------------

func TestEmailApplyCmdRollbackEndToEnd(t *testing.T) {
	cfgPath, stateDir := setupEmailServer(t)
	dir := t.TempDir()
	planPath := buildEmailTestPlan(t, dir)
	setEmailStubState(t, stateDir, nil, []string{"example.com|acct"})

	outJSON := filepath.Join(dir, "email_apply_report.json")
	backupPath := filepath.Join(dir, "email_backup_test.json")
	if code := runEmailApplyCmd([]string{
		"--plan", planPath, "--config", cfgPath, "--yes-apply-writes",
		"--backup", backupPath, "--output-json", outJSON,
	}); code != 0 {
		t.Fatalf("apply: code = %d", code)
	}

	// Rollback dry-run: offline, prints the inverse ops, writes nothing.
	rbJSON := filepath.Join(dir, "email_rollback_report.json")
	if code := runEmailApplyCmd([]string{"--rollback", backupPath, "--output-json", rbJSON}); code != 0 {
		t.Fatalf("rollback dry-run: code = %d, want 0", code)
	}
	if _, err := os.Stat(rbJSON); !os.IsNotExist(err) {
		t.Error("rollback dry-run must not write a report")
	}

	// Real rollback: the pair is deleted, the default restored.
	if code := runEmailApplyCmd([]string{
		"--rollback", backupPath, "--config", cfgPath, "--yes-apply-writes",
		"--output-json", rbJSON,
	}); code != 0 {
		t.Fatalf("rollback: code = %d, want 0", code)
	}
	rb := readEmailApplyReport(t, rbJSON)
	if rb.RunMode != "rollback" || rb.Summary.Applied != 2 {
		t.Fatalf("rollback report = run_mode %q summary %+v", rb.RunMode, rb.Summary)
	}
	if fw := readEmailStubState(t, stateDir, "forwarders.txt"); strings.Contains(fw, "someone@gmail.com") {
		t.Errorf("forwarder not removed: %q", fw)
	}
	if df := readEmailStubState(t, stateDir, "defaults.txt"); !strings.Contains(df, "example.com|acct") {
		t.Errorf("default not restored: %q", df)
	}

	// Rollback again: everything already back — already_present, no writes.
	rbJSON2 := filepath.Join(dir, "email_rollback_report2.json")
	if code := runEmailApplyCmd([]string{
		"--rollback", backupPath, "--config", cfgPath, "--yes-apply-writes",
		"--output-json", rbJSON2,
	}); code != 0 {
		t.Fatalf("re-rollback: code = %d, want 0", code)
	}
	rb2 := readEmailApplyReport(t, rbJSON2)
	if rb2.Summary.AlreadyPresent != 2 || rb2.Summary.Applied != 0 {
		t.Fatalf("re-rollback summary = %+v, want 2 already_present", rb2.Summary)
	}
}

// Rollback refuses an item a human changed since the apply (post-apply
// state diverged) — exit 3, the other items still proceed.
func TestEmailApplyCmdRollbackRefusesDivergedState(t *testing.T) {
	cfgPath, stateDir := setupEmailServer(t)
	dir := t.TempDir()
	planPath := buildEmailTestPlan(t, dir)
	setEmailStubState(t, stateDir, nil, []string{"example.com|acct"})

	outJSON := filepath.Join(dir, "email_apply_report.json")
	backupPath := filepath.Join(dir, "email_backup_test.json")
	if code := runEmailApplyCmd([]string{
		"--plan", planPath, "--config", cfgPath, "--yes-apply-writes",
		"--backup", backupPath, "--output-json", outJSON,
	}); code != 0 {
		t.Fatalf("apply: code = %d", code)
	}
	// A human re-pointed the default after the apply.
	setEmailStubState(t, stateDir,
		[]string{"info@example.com|someone@gmail.com"},
		[]string{"example.com|human@choice.com"})

	rbJSON := filepath.Join(dir, "email_rollback_report.json")
	code := runEmailApplyCmd([]string{
		"--rollback", backupPath, "--config", cfgPath, "--yes-apply-writes",
		"--output-json", rbJSON,
	})
	if code != 3 {
		t.Fatalf("rollback: code = %d, want 3 (refused item)", code)
	}
	rb := readEmailApplyReport(t, rbJSON)
	if rb.Summary.Refused != 1 || rb.Summary.Applied != 1 {
		t.Fatalf("summary = %+v, want 1 refused (default) + 1 applied (forwarder)", rb.Summary)
	}
	// The human's default was NOT clobbered.
	if df := readEmailStubState(t, stateDir, "defaults.txt"); !strings.Contains(df, "human@choice.com") {
		t.Errorf("refused rollback clobbered the human's default: %q", df)
	}
}

// Report-loss degradation: without the paired report, rollback is refused
// unless --accept-report-loss opts into the documented degradation
// (default restores only, forwarders manual).
func TestEmailApplyCmdRollbackReportLoss(t *testing.T) {
	cfgPath, stateDir := setupEmailServer(t)
	dir := t.TempDir()
	planPath := buildEmailTestPlan(t, dir)
	setEmailStubState(t, stateDir, nil, []string{"example.com|acct"})

	outJSON := filepath.Join(dir, "email_apply_report.json")
	backupPath := filepath.Join(dir, "email_backup_test.json")
	if code := runEmailApplyCmd([]string{
		"--plan", planPath, "--config", cfgPath, "--yes-apply-writes",
		"--backup", backupPath, "--output-json", outJSON,
	}); code != 0 {
		t.Fatalf("apply: code = %d", code)
	}
	if err := os.Remove(outJSON); err != nil {
		t.Fatal(err)
	}

	rbJSON := filepath.Join(dir, "email_rollback_report.json")
	if code := runEmailApplyCmd([]string{
		"--rollback", backupPath, "--config", cfgPath, "--yes-apply-writes",
		"--output-json", rbJSON,
	}); code != 1 {
		t.Fatalf("rollback without report: code = %d, want 1 (required input)", code)
	}

	if code := runEmailApplyCmd([]string{
		"--rollback", backupPath, "--config", cfgPath, "--yes-apply-writes",
		"--accept-report-loss", "--output-json", rbJSON,
	}); code != 0 {
		t.Fatalf("degraded rollback: code = %d, want 0", code)
	}
	rb := readEmailApplyReport(t, rbJSON)
	if rb.Summary.Applied != 1 || rb.Summary.Manual != 1 {
		t.Fatalf("degraded summary = %+v, want 1 applied (default) + 1 manual (forwarders note)", rb.Summary)
	}
	// The default is back to the backup value; the forwarder was NOT
	// touched (never-delete wins without the report).
	if df := readEmailStubState(t, stateDir, "defaults.txt"); !strings.Contains(df, "example.com|acct") {
		t.Errorf("default not restored: %q", df)
	}
	if fw := readEmailStubState(t, stateDir, "forwarders.txt"); !strings.Contains(fw, "info@example.com|someone@gmail.com") {
		t.Errorf("degraded rollback must NOT delete forwarders: %q", fw)
	}
}

// --- autoresponders (PR 2B-2) -------------------------------------------------

// writeEmailPlanInventoryWithAR is writeEmailPlanInventory plus autoresponders.
func writeEmailPlanInventoryWithAR(t *testing.T, dir, name, side, user string, ars []accountinventory.AutoresponderEntry) string {
	t.Helper()
	inv := accountinventory.NewEmptyInventory(user, "192.0.2.1", side)
	inv.Domains = []accountinventory.DomainEntry{{Name: "example.com", Type: "main"}}
	inv.Autoresponders = ars
	inv.DefaultAddresses.Available = true
	inv.DefaultAddresses.Items = []accountinventory.DefaultAddressEntry{{Domain: "example.com", DefaultAddress: user}}
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

func testAutoresponderEntry() accountinventory.AutoresponderEntry {
	return accountinventory.AutoresponderEntry{
		Email: "info@example.com", Domain: "example.com",
		Subject: "Out of office", From: "Info Desk",
		Body: "Sono in ferie.\nRientro lunedì.\n", IsHTML: 0, Interval: 8,
		Charset: "utf-8", BodyCollected: true,
	}
}

// buildEmailAutoresponderPlan builds a plan whose only actionable op is one
// autoresponder create.
func buildEmailAutoresponderPlan(t *testing.T, dir string) string {
	t.Helper()
	src := writeEmailPlanInventoryWithAR(t, dir, "src_ar.json", "source", "acct",
		[]accountinventory.AutoresponderEntry{testAutoresponderEntry()})
	dest := writeEmailPlanInventoryWithAR(t, dir, "dest_ar.json", "destination", "acct", nil)
	planPath := filepath.Join(dir, "email_apply_plan_ar.json")
	if code := runInventoryEmailPlanCmd([]string{
		"--source", src, "--destination", dest,
		"--output-json", planPath, "--output-md", filepath.Join(dir, "email_apply_plan_ar.md"),
	}); code != 0 {
		t.Fatalf("email-plan: code = %d, want 0", code)
	}
	return planPath
}

func TestEmailApplyCmdAutoresponderEndToEnd(t *testing.T) {
	cfgPath, stateDir := setupEmailServer(t)
	setEmailStubState(t, stateDir, nil, []string{"example.com|acct"})
	dir := t.TempDir()
	planPath := buildEmailAutoresponderPlan(t, dir)
	reportPath := filepath.Join(dir, "email_apply_report.json")

	// 1. Apply: the create is written, verified-after, backed up first.
	code := runEmailApplyCmd([]string{
		"--plan", planPath, "--config", cfgPath, "--yes-apply-writes",
		"--output-json", reportPath, "--output-md", filepath.Join(dir, "email_apply_report.md"),
		"--backup", filepath.Join(dir, "email_backup.json"),
	})
	if code != 0 {
		t.Fatalf("apply: code = %d, want 0", code)
	}
	rep := readEmailApplyReport(t, reportPath)
	if rep.Summary.Applied != 1 {
		t.Fatalf("summary = %+v, want 1 applied", rep.Summary)
	}
	if rep.BackupFile == "" {
		t.Fatal("a real write must have produced a backup")
	}
	state := readEmailStubState(t, stateDir, "autoresponders.txt")
	if !strings.Contains(state, "info@example.com|Out of office") {
		t.Fatalf("stub state = %q, autoresponder not written", state)
	}
	// The backup archives the pre-write autoresponder section.
	bb, err := os.ReadFile(rep.BackupFile)
	if err != nil {
		t.Fatal(err)
	}
	var backup accountinventory.EmailBackup
	if err := json.Unmarshal(bb, &backup); err != nil {
		t.Fatal(err)
	}
	if _, ok := backup.AutorespondersByDomain["example.com"]; !ok {
		t.Errorf("backup lacks the autoresponders section: %+v", backup.AutorespondersByDomain)
	}

	// 2. Re-run: converges to already_present, no second backup.
	report2 := filepath.Join(dir, "email_apply_report2.json")
	if code := runEmailApplyCmd([]string{
		"--plan", planPath, "--config", cfgPath, "--yes-apply-writes",
		"--output-json", report2, "--output-md", filepath.Join(dir, "email_apply_report2.md"),
	}); code != 0 {
		t.Fatalf("re-apply: code = %d, want 0", code)
	}
	rep2 := readEmailApplyReport(t, report2)
	if rep2.Summary.AlreadyPresent != 1 || rep2.Summary.Applied != 0 {
		t.Fatalf("re-apply summary = %+v, want 1 already_present", rep2.Summary)
	}
	if rep2.BackupFile != "" {
		t.Errorf("no write decided, no backup expected (note %q)", rep2.BackupNote)
	}

	// 3. email verify: clean, the create verifies applied.
	verifyJSON := filepath.Join(dir, "email_verify.json")
	if code := runEmailVerifyCmd([]string{
		"--plan", planPath, "--config", cfgPath, "--fail-on-drift",
		"--output-json", verifyJSON, "--output-md", filepath.Join(dir, "email_verify.md"),
	}); code != 0 {
		t.Fatalf("verify: code = %d, want 0 (clean)", code)
	}

	// 4. Rollback dry-run: exactly one inverse (the own applied create).
	if code := runEmailApplyCmd([]string{"--rollback", rep.BackupFile}); code != 0 {
		t.Fatalf("rollback dry-run: code = %d, want 0", code)
	}

	// 4b. Apply dry-run renders the autoresponder create with its own
	// shape (offline, no config needed).
	if code := runEmailApplyCmd([]string{"--plan", planPath}); code != 0 {
		t.Fatalf("apply dry-run: code = %d, want 0", code)
	}

	// 5. Live rollback: the autoresponder is deleted and verified gone.
	rbReport := filepath.Join(dir, "email_rollback_report.json")
	if code := runEmailApplyCmd([]string{
		"--rollback", rep.BackupFile, "--config", cfgPath, "--yes-apply-writes",
		"--output-json", rbReport, "--output-md", filepath.Join(dir, "email_rollback_report.md"),
	}); code != 0 {
		t.Fatalf("rollback: code = %d, want 0", code)
	}
	rb := readEmailApplyReport(t, rbReport)
	if rb.Summary.Applied != 1 {
		t.Fatalf("rollback summary = %+v, want 1 applied", rb.Summary)
	}
	state = readEmailStubState(t, stateDir, "autoresponders.txt")
	if strings.Contains(state, "info@example.com") {
		t.Fatalf("stub state = %q, autoresponder still live after rollback", state)
	}
}

func TestEmailApplyCmdAutoresponderRefusesForeignContent(t *testing.T) {
	cfgPath, stateDir := setupEmailServer(t)
	setEmailStubState(t, stateDir, nil, []string{"example.com|acct"})
	dir := t.TempDir()
	planPath := buildEmailAutoresponderPlan(t, dir)

	// Between plan and apply somebody creates a DIFFERENT autoresponder on
	// the same address: the guard must refuse — an add would destroy it.
	if err := os.WriteFile(filepath.Join(stateDir, "autoresponders.txt"),
		[]byte("info@example.com|Somebody's subject\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "ar_info@example.com.json"),
		[]byte(`{"body":"Qualcun altro.\n","charset":"utf-8","from":"X","interval":1,"is_html":0,"start":null,"stop":null,"subject":"Somebody's subject"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	reportPath := filepath.Join(dir, "email_apply_report.json")
	code := runEmailApplyCmd([]string{
		"--plan", planPath, "--config", cfgPath, "--yes-apply-writes",
		"--output-json", reportPath, "--output-md", filepath.Join(dir, "email_apply_report.md"),
	})
	if code != exitDriftGate {
		t.Fatalf("apply onto foreign content: code = %d, want %d (refused)", code, exitDriftGate)
	}
	rep := readEmailApplyReport(t, reportPath)
	if rep.Summary.Refused != 1 || rep.Summary.Applied != 0 {
		t.Fatalf("summary = %+v, want 1 refused, 0 applied", rep.Summary)
	}
	// The foreign autoresponder must be UNTOUCHED.
	state := readEmailStubState(t, stateDir, "autoresponders.txt")
	if !strings.Contains(state, "Somebody's subject") {
		t.Fatalf("stub state = %q — the foreign autoresponder was destroyed", state)
	}
}

func TestEmailApplyCmdAutoresponderRollbackRefusesDiverged(t *testing.T) {
	cfgPath, stateDir := setupEmailServer(t)
	setEmailStubState(t, stateDir, nil, []string{"example.com|acct"})
	dir := t.TempDir()
	planPath := buildEmailAutoresponderPlan(t, dir)
	reportPath := filepath.Join(dir, "email_apply_report.json")
	backupPath := filepath.Join(dir, "email_backup.json")

	if code := runEmailApplyCmd([]string{
		"--plan", planPath, "--config", cfgPath, "--yes-apply-writes",
		"--output-json", reportPath, "--output-md", filepath.Join(dir, "email_apply_report.md"),
		"--backup", backupPath,
	}); code != 0 {
		t.Fatalf("apply: code = %d, want 0", code)
	}

	// A human customizes the applied autoresponder: rollback must refuse
	// to delete it (diverged from the post-apply state).
	if err := os.WriteFile(filepath.Join(stateDir, "ar_info@example.com.json"),
		[]byte(`{"body":"Modificato a mano.\n","charset":"utf-8","from":"Info Desk","interval":8,"is_html":0,"start":null,"stop":null,"subject":"Out of office"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	rbReport := filepath.Join(dir, "email_rollback_report.json")
	code := runEmailApplyCmd([]string{
		"--rollback", backupPath, "--config", cfgPath, "--yes-apply-writes",
		"--output-json", rbReport, "--output-md", filepath.Join(dir, "email_rollback_report.md"),
	})
	if code != exitDriftGate {
		t.Fatalf("rollback of customized autoresponder: code = %d, want %d (refused)", code, exitDriftGate)
	}
	state := readEmailStubState(t, stateDir, "autoresponders.txt")
	if !strings.Contains(state, "info@example.com") {
		t.Fatal("the customized autoresponder was deleted — never-delete violated")
	}
}


func TestEmailApplyCmdAutoresponderMidRunRaceIsRefused(t *testing.T) {
	// go-review 2B-2 round 1, finding 1 (HIGH): the batch live snapshot at
	// run start can be stale by the time an op reaches the sequential
	// write loop, and add_auto_responder UPSERTS — the guard-to-write
	// window must be collapsed by a fresh per-address re-check right
	// before the write. The stub simulates the race: writing the
	// aaa-trigger autoresponder also creates a FOREIGN one on zzz-victim.
	cfgPath, stateDir := setupEmailServer(t)
	setEmailStubState(t, stateDir, nil, []string{"example.com|acct"})
	dir := t.TempDir()

	trigger := testAutoresponderEntry()
	trigger.Email = "aaa-trigger@example.com"
	victim := testAutoresponderEntry()
	victim.Email = "zzz-victim@example.com"
	src := writeEmailPlanInventoryWithAR(t, dir, "src_race.json", "source", "acct",
		[]accountinventory.AutoresponderEntry{trigger, victim})
	dest := writeEmailPlanInventoryWithAR(t, dir, "dest_race.json", "destination", "acct", nil)
	planPath := filepath.Join(dir, "email_apply_plan_race.json")
	if code := runInventoryEmailPlanCmd([]string{
		"--source", src, "--destination", dest,
		"--output-json", planPath, "--output-md", filepath.Join(dir, "email_apply_plan_race.md"),
	}); code != 0 {
		t.Fatalf("email-plan: code = %d, want 0", code)
	}

	reportPath := filepath.Join(dir, "email_apply_report.json")
	code := runEmailApplyCmd([]string{
		"--plan", planPath, "--config", cfgPath, "--yes-apply-writes",
		"--output-json", reportPath, "--output-md", filepath.Join(dir, "email_apply_report.md"),
	})
	if code != exitDriftGate {
		t.Fatalf("apply with mid-run race: code = %d, want %d (refused)", code, exitDriftGate)
	}
	rep := readEmailApplyReport(t, reportPath)
	if rep.Summary.Applied != 1 || rep.Summary.Refused != 1 {
		t.Fatalf("summary = %+v, want 1 applied (trigger) + 1 refused (victim)", rep.Summary)
	}
	// The foreign autoresponder must be UNTOUCHED — an upsert would have
	// silently destroyed it and reported a clean applied.
	b, err := os.ReadFile(filepath.Join(stateDir, "ar_zzz-victim@example.com.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "Foreign content.") {
		t.Fatalf("foreign autoresponder content destroyed: %s", b)
	}
}
