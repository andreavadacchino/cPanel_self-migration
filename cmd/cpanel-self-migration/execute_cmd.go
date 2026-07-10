package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/executioncontract"
	"github.com/tis24dev/cPanel_self-migration/internal/migrate"
)

// runExecuteCmd implements `cpanel-self-migration execute --spec spec.json`: the
// platform → executor bridge (ADR-001 §D5). It consumes an execution-spec-v1
// document, runs a GOVERNED DRY-RUN — it never writes to the destination — and
// emits the versioned executor → platform output: execution-event-v1 lines to
// events.jsonl and a final execution-result-v1 report.json.
//
// The spec carries only non-secret references (plan/snapshot ids, mode, scope);
// the connection details and credentials come from host.yaml, resolved at run
// time and never taken from the spec. The spec's mode is dry_run only and its
// scope must select at least one of mail/files/databases — both are enforced by
// executioncontract.ParseSpec, so this command never has to re-derive them.
//
// Exit codes mirror the migration flow: 0 ok; 1 input/runtime failure (the report
// is still written when the run reached it); 2 flag/usage error; 130 interrupted.
func runExecuteCmd(args []string) int {
	fs := flag.NewFlagSet("execute", flag.ContinueOnError)
	specPath := fs.String("spec", "", "path to an execution-spec-v1 JSON document (required)")
	cfgFlag := fs.String("config", "", "path to host.yaml (default: configs/host.yaml or host.yaml)")
	outputDir := fs.String("output-dir", "", "directory for events.jsonl + report.json (default: current directory)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration execute --spec execution-spec.json [--config host.yaml] [--output-dir DIR]")
		fmt.Fprintln(os.Stderr, "  Runs a governed DRY-RUN from an execution-spec-v1 document (never writes to the destination).")
		fmt.Fprintln(os.Stderr, "  Emits execution-event-v1 lines to events.jsonl and a final execution-result-v1 report.json.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *specPath == "" {
		fs.Usage()
		return 2
	}

	raw, err := os.ReadFile(*specPath) // #nosec G304 -- orchestrator-provided spec path, not untrusted input
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: read execution spec:", err)
		return 1
	}
	spec, err := executioncontract.ParseSpec(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: invalid execution spec:", err)
		return 1
	}

	path, alternates, err := resolveConfigPath(*cfgFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "Config: %s\n", path)
	if len(alternates) > 0 {
		fmt.Fprintf(os.Stderr, "warning: multiple host.yaml found; using %s (ignoring %s). Pass --config to choose explicitly.\n",
			path, strings.Join(alternates, ", "))
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	outDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: cannot determine the current working directory (needed for the artifacts):", err)
		return 1
	}
	if *outputDir != "" {
		outDir = *outputDir
	}

	// A dry-run can be long; a Ctrl-C cancels it cleanly (the SOURCE is read-only,
	// so there is nothing to roll back), matching the migration flow's handler.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go handleSignals(cancel)

	// The bridge ALWAYS emits both channels: events.jsonl (the live stream) and
	// report.json (the final result) are the executor → platform contract.
	em, collector, closeEv, err := buildEmitter(outDir, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: cannot create events file:", err)
		return 1
	}
	defer func() { _ = closeEv() }()

	startedAt := time.Now()
	opts := specToOptions(spec, outDir, startedAt)
	opts.Events = em
	return runMigrationAndReport(ctx, cfg, opts, collector, startedAt, outDir, true, true)
}

// specToOptions maps a validated execution-spec into migrate.Options for a
// DRY-RUN. Apply is ALWAYS false — the hard invariant that guarantees the bridge
// executor never writes to the destination, independent of the spec (whose mode
// is dry_run only). The scope booleans and filters map 1:1; ParseSpec has already
// rejected an all-false scope and any filter/mode inconsistency, so no re-check is
// needed here. The spec's plan/snapshot/comparison ids are provenance the platform
// records; the executor resolves the actual data itself from host.yaml + its own
// analysis, so they are intentionally not consumed here.
func specToOptions(spec executioncontract.ExecutionSpec, outDir string, now time.Time) migrate.Options {
	return migrate.Options{
		Apply:       false,
		DoMail:      spec.Scope.Mail,
		DoFile:      spec.Scope.Files,
		DoDB:        spec.Scope.Databases,
		OnlyDomain:  spec.Scope.DomainFilter,
		OnlyMailbox: spec.Scope.MailboxFilter,
		OutputDir:   outDir,
		RunID:       spec.RunID,
		Now:         now,
	}
}
