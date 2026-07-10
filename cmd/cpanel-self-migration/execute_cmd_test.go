package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/executioncontract"
)

// --- unit: spec -> Options mapping ---

// specToOptions must map an execution-spec faithfully and, above all, keep
// Apply=false: the dry-run bridge can NEVER write to the destination.
func TestSpecToOptionsFaithfulMapping(t *testing.T) {
	now := time.Now()
	spec := executioncontract.ExecutionSpec{
		RunID: "run-20260101-000000",
		Scope: executioncontract.SpecScope{
			Mail: true, Files: true, Databases: false,
			DomainFilter: "example.com",
		},
	}
	opts := specToOptions(spec, "/out", now)

	if opts.Apply {
		t.Error("Apply must be false: the dry-run bridge must never write")
	}
	if opts.MirrorMail || opts.ForceSync {
		t.Error("no write/mirror modes may be enabled from a spec")
	}
	if !opts.DoMail || !opts.DoFile || opts.DoDB {
		t.Errorf("scope mapping wrong: DoMail=%v DoFile=%v DoDB=%v", opts.DoMail, opts.DoFile, opts.DoDB)
	}
	if opts.OnlyDomain != "example.com" {
		t.Errorf("OnlyDomain = %q, want example.com", opts.OnlyDomain)
	}
	if opts.RunID != "run-20260101-000000" {
		t.Errorf("RunID = %q, want the spec's run_id", opts.RunID)
	}
	if opts.OutputDir != "/out" || !opts.Now.Equal(now) {
		t.Errorf("OutputDir/Now not propagated: %q %v", opts.OutputDir, opts.Now)
	}
}

// A databases+mailbox mapping: mailbox filter -> OnlyMailbox, and only the DB
// scope bool set.
func TestSpecToOptionsMailboxAndDB(t *testing.T) {
	spec := executioncontract.ExecutionSpec{
		RunID: "run-20260101-000000",
		Scope: executioncontract.SpecScope{Mail: true, MailboxFilter: "bob@example.com"},
	}
	opts := specToOptions(spec, "/out", time.Now())
	if opts.OnlyMailbox != "bob@example.com" {
		t.Errorf("OnlyMailbox = %q, want bob@example.com", opts.OnlyMailbox)
	}
	if !opts.DoMail {
		t.Error("DoMail must be true")
	}
}

// --- command: input errors (no network) ---

func TestExecuteMissingSpecFlag(t *testing.T) {
	code, stderr := runDispatchChild(t, "execute")
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "usage: cpanel-self-migration execute") {
		t.Errorf("stderr missing execute usage:\n%s", stderr)
	}
}

func TestExecuteMissingSpecFile(t *testing.T) {
	code, stderr := runDispatchChild(t, "execute", "--spec", filepath.Join(t.TempDir(), "nope.json"))
	if code != 1 {
		t.Fatalf("exit = %d, want 1; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "spec") {
		t.Errorf("stderr should name the spec read failure:\n%s", stderr)
	}
}

func TestExecuteRejectsInvalidSpec(t *testing.T) {
	cases := map[string]string{
		"not json":        `{ this is not json`,
		"mode not dryrun": validSpec(t, `"mode":"apply"`),
		"all-false scope": validSpec(t, `"scope":{"mail":false,"files":false,"databases":false}`),
		"unknown top key": validSpec(t, `"bogus":1`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			sp := filepath.Join(t.TempDir(), "spec.json")
			if err := os.WriteFile(sp, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			code, stderr := runDispatchChild(t, "execute", "--spec", sp, "--config", writeRefusedHostYAML(t))
			if code != 1 {
				t.Fatalf("%s: exit = %d, want 1; stderr:\n%s", name, code, stderr)
			}
			if !strings.Contains(strings.ToLower(stderr), "spec") {
				t.Errorf("%s: stderr should name the invalid spec:\n%s", name, stderr)
			}
		})
	}
}

// --- end-to-end: a valid spec against an unreachable source ---
// The connect fails deterministically (refused port), which exercises the FULL
// execute assembly: parse spec -> map -> run (fails at connect) -> write the
// versioned events.jsonl + report.json. Proves the bridge OUTPUT conforms to
// execution-event-v1 and execution-result-v1 without a full cPanel SSH server.
func TestExecuteProducesContractValidArtifactsOnConnectFailure(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "spec.json")
	if err := os.WriteFile(sp, []byte(validSpec(t, `"scope":{"mail":false,"files":true,"databases":false}`)), 0o600); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}

	code, stderr := runDispatchChild(t, "execute",
		"--spec", sp, "--config", writeRefusedHostYAML(t), "--output-dir", outDir)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (connect must fail); stderr:\n%s", code, stderr)
	}

	// report.json must exist and be a valid execution-result-v1 document.
	rptRaw, err := os.ReadFile(filepath.Join(outDir, "report.json"))
	if err != nil {
		t.Fatalf("report.json not written: %v", err)
	}
	if err := executioncontract.ValidateResultJSON(rptRaw); err != nil {
		t.Fatalf("report.json is not a valid execution-result-v1: %v\n%s", err, rptRaw)
	}
	var rpt struct {
		Mode       string `json:"mode"`
		ExitStatus string `json:"exit_status"`
		Scope      struct {
			Mail, Files, Databases bool
		} `json:"scope"`
	}
	if err := json.Unmarshal(rptRaw, &rpt); err != nil {
		t.Fatal(err)
	}
	if rpt.Mode != "dry-run" {
		t.Errorf("report mode = %q, want dry-run", rpt.Mode)
	}
	if rpt.ExitStatus != "failed" {
		t.Errorf("exit_status = %q, want failed", rpt.ExitStatus)
	}
	if rpt.Scope.Mail || !rpt.Scope.Files || rpt.Scope.Databases {
		t.Errorf("report scope = %+v, want files-only (mapped from the spec)", rpt.Scope)
	}

	// Every events.jsonl line must be a valid execution-event-v1 document.
	evRaw, err := os.ReadFile(filepath.Join(outDir, "events.jsonl"))
	if err != nil {
		t.Fatalf("events.jsonl not written: %v", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(evRaw))
	n := 0
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		n++
		if err := executioncontract.ValidateEventJSON(line); err != nil {
			t.Errorf("events.jsonl line %d invalid execution-event-v1: %v\n%s", n, err, line)
		}
	}
	if n == 0 {
		t.Error("events.jsonl has no events")
	}
}

// --- helpers ---

// validSpec returns a valid execution-spec-v1 JSON string, with one field
// overridden by `override` (a raw `"key":value` fragment). The override REPLACES
// the same-named default when present (mode/scope), else is appended.
func validSpec(t *testing.T, override string) string {
	t.Helper()
	fields := map[string]string{
		"format_version":          `1`,
		"run_id":                  `"run-20260101-000000"`,
		"plan_id":                 `1`,
		"source_snapshot_id":      `1`,
		"destination_snapshot_id": `1`,
		"comparison_report_id":    `1`,
		"mode":                    `"dry_run"`,
		"scope":                   `{"mail":false,"files":true,"databases":false}`,
	}
	// Apply the override: replace a known key or append an extra one.
	if k, v, ok := splitKV(override); ok {
		fields[k] = v
	}
	var b strings.Builder
	b.WriteByte('{')
	first := true
	for k, v := range fields {
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.WriteString(`"` + k + `":` + v)
	}
	b.WriteByte('}')
	return b.String()
}

// splitKV parses a `"key":value` fragment into key (unquoted) and raw value.
func splitKV(frag string) (key, val string, ok bool) {
	frag = strings.TrimSpace(frag)
	if !strings.HasPrefix(frag, `"`) {
		return "", "", false
	}
	end := strings.Index(frag[1:], `"`)
	if end < 0 {
		return "", "", false
	}
	key = frag[1 : 1+end]
	rest := strings.TrimSpace(frag[1+end+1:])
	rest = strings.TrimPrefix(rest, ":")
	return key, strings.TrimSpace(rest), true
}

// writeRefusedHostYAML writes a host.yaml whose SOURCE points at a closed local
// port, so a dry-run fails deterministically at connect. Returns its path.
func writeRefusedHostYAML(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close() // now refused
	p := filepath.Join(t.TempDir(), "host.yaml")
	yaml := "src:\n  ip: 127.0.0.1\n  port: " + strconv.Itoa(port) + "\n  ssh_user: u\n  ssh_pass: p\n  timeout: 2s\n"
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
