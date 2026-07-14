package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

// A rejected spec must leave the workspace EMPTY. Exit 1 alone cannot tell the
// orchestrator "the platform built a bad spec" from "the dry-run failed on the
// server"; given a fresh workspace, the absence of report.json can. Pin it: if a
// rejection ever started emitting artifacts, that signal would go quiet.
func TestExecuteRejectedSpecWritesNoArtifacts(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "spec.json")
	if err := os.WriteFile(sp, []byte(validSpec(t, `"mode":"apply"`)), 0o600); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")

	code, stderr := runDispatchChild(t, "execute",
		"--spec", sp, "--config", writeRefusedHostYAML(t), "--output-dir", outDir)
	if code != 1 {
		t.Fatalf("exit = %d, want 1; stderr:\n%s", code, stderr)
	}
	for _, name := range []string{"report.json", "events.jsonl"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); !os.IsNotExist(err) {
			t.Errorf("a rejected spec wrote %s (stat err = %v); the workspace must stay empty", name, err)
		}
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

// --- workspace invariant: one execution == one artifact set ---

// The bridge must REFUSE an --output-dir that already holds the artifacts of a
// previous execution.
//
// events.jsonl is opened O_APPEND — right for the flag-driven flow, where an
// operator re-running in the same directory is making that choice themselves.
// For the bridge it is a trap: a re-used workspace interleaves two runs into one
// stream, under the SAME run_id, with every single line still a valid
// execution-event-v1 — so no contract validator downstream would ever catch it.
// An orchestrator retrying a transient dial failure into the same workspace is
// exactly that case. Refuse, loudly, and touch nothing.
func TestExecuteRefusesDirtyOutputDir(t *testing.T) {
	for _, name := range []string{"events.jsonl", "report.json"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			outDir := filepath.Join(dir, "out")
			if err := os.MkdirAll(outDir, 0o755); err != nil {
				t.Fatal(err)
			}
			stale := []byte("STALE-FROM-A-PREVIOUS-RUN\n")
			victim := filepath.Join(outDir, name)
			if err := os.WriteFile(victim, stale, 0o600); err != nil {
				t.Fatal(err)
			}
			sp := filepath.Join(dir, "spec.json")
			if err := os.WriteFile(sp, []byte(validSpec(t, "")), 0o600); err != nil {
				t.Fatal(err)
			}

			code, stderr := runDispatchChild(t, "execute",
				"--spec", sp, "--config", writeRefusedHostYAML(t), "--output-dir", outDir)
			if code != 1 {
				t.Fatalf("exit = %d, want 1 (a used workspace must be refused); stderr:\n%s", code, stderr)
			}
			if !strings.Contains(stderr, name) {
				t.Errorf("stderr should name the pre-existing artifact %q:\n%s", name, stderr)
			}
			// The refusal must not have written anything: the previous run's
			// artifacts are evidence, and evidence is not overwritten.
			got, err := os.ReadFile(victim)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, stale) {
				t.Errorf("the refused run modified %s:\n%s", name, got)
			}
		})
	}
}

// Regression for the interleaving bug itself: two executions into the same
// --output-dir. Before the workspace guard the second run APPENDED its events to
// the first run's events.jsonl — two run_started lines under one run_id, both
// contract-valid.
func TestExecuteRerunSameOutputDirDoesNotInterleaveEvents(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "spec.json")
	if err := os.WriteFile(sp, []byte(validSpec(t, "")), 0o600); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	cfgPath := writeRefusedHostYAML(t)

	if code, stderr := runDispatchChild(t, "execute",
		"--spec", sp, "--config", cfgPath, "--output-dir", outDir); code != 1 {
		t.Fatalf("first run exit = %d, want 1 (connect refused); stderr:\n%s", code, stderr)
	}
	evPath := filepath.Join(outDir, "events.jsonl")
	first, err := os.ReadFile(evPath)
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(first, []byte(`"event":"run_started"`)); n != 1 {
		t.Fatalf("the first run wrote %d run_started events, want exactly 1:\n%s", n, first)
	}

	code, stderr := runDispatchChild(t, "execute",
		"--spec", sp, "--config", cfgPath, "--output-dir", outDir)
	if code != 1 {
		t.Fatalf("second run exit = %d, want 1 (workspace already used); stderr:\n%s", code, stderr)
	}
	after, err := os.ReadFile(evPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, after) {
		t.Fatalf("the second run mutated the first run's events.jsonl\nbefore:\n%s\nafter:\n%s", first, after)
	}
}

// --- interruption: the signal contract the orchestrator depends on ---

// A SIGTERM — what a worker sends to stop a run — must end the bridge with exit
// 130 AND still leave a report.json saying `interrupted`. Without both, the
// platform cannot tell "the operator cancelled" from "the run failed", and the
// ADR's `interrupted` status has nothing to be derived from.
//
// The source here ACCEPTS the TCP connection and then says nothing, so the run
// is genuinely wedged inside the SSH handshake when the signal lands — the case
// dialOnce's watchdog exists for.
func TestExecuteSIGTERMExits130AndReportsInterrupted(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "spec.json")
	if err := os.WriteFile(sp, []byte(validSpec(t, "")), 0o600); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	cfgPath, accepted := writeHangingHostYAML(t)

	cmd := exec.Command(os.Args[0], "execute",
		"--spec", sp, "--config", cfgPath, "--output-dir", outDir)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "CPSM_DISPATCH_TEST_CHILD=1")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	select {
	case <-accepted: // the executor is now inside the handshake
	case <-time.After(30 * time.Second):
		t.Fatal("the executor never connected to the source")
	}
	// The signal handler is installed before the dial, but "before" is a happens-
	// before on the goroutine, not on signal.Notify's syscall: give it a moment so
	// a SIGTERM cannot land on the default disposition and kill the child outright.
	time.Sleep(300 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	err := cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("the run did not exit with a status: %v; stderr:\n%s", err, stderr.String())
	}
	if exitErr.ExitCode() != 130 {
		t.Fatalf("exit = %d, want 130 (interrupted); stderr:\n%s", exitErr.ExitCode(), stderr.String())
	}

	rptRaw, err := os.ReadFile(filepath.Join(outDir, "report.json"))
	if err != nil {
		t.Fatalf("an interrupted run must still write report.json: %v", err)
	}
	if err := executioncontract.ValidateResultJSON(rptRaw); err != nil {
		t.Fatalf("report.json is not a valid execution-result-v1: %v\n%s", err, rptRaw)
	}
	var rpt struct {
		ExitStatus string `json:"exit_status"`
	}
	if err := json.Unmarshal(rptRaw, &rpt); err != nil {
		t.Fatal(err)
	}
	if rpt.ExitStatus != "interrupted" {
		t.Errorf("exit_status = %q, want interrupted", rpt.ExitStatus)
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

// writeHangingHostYAML writes a host.yaml whose SOURCE is a listener that accepts
// the TCP connection and then never speaks SSH, so the dry-run wedges inside the
// handshake instead of failing. Returns the config path and a channel closed on
// the first accept — the point from which the run is genuinely in flight and a
// signal is meaningful. The timeout is long so the handshake deadline can never
// preempt the signal under test.
func writeHangingHostYAML(t *testing.T) (string, <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	accepted := make(chan struct{})
	go func() {
		var once bool
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed at cleanup
			}
			if !once {
				once = true
				close(accepted)
			}
			// Hold the connection open and say nothing: the SSH version exchange
			// blocks forever, which is the state the watchdog must abort.
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()

	p := filepath.Join(t.TempDir(), "host.yaml")
	port := ln.Addr().(*net.TCPAddr).Port
	yaml := "src:\n  ip: 127.0.0.1\n  port: " + strconv.Itoa(port) + "\n  ssh_user: u\n  ssh_pass: p\n  timeout: 120s\n"
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return p, accepted
}
