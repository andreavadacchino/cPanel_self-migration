// Command cpanel-self-migration migrates email mailboxes, website files, and
// MySQL databases (plus the domains they need) between two cPanel accounts using
// only user-level SSH. The SOURCE host is always read-only; all writes target
// the DESTINATION. Default mode is dry-run.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/events"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/migrate"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
	"github.com/tis24dev/cPanel_self-migration/internal/validate"
	"github.com/tis24dev/cPanel_self-migration/internal/version"
)

func main() {
	// Subcommand dispatch happens before the global flag parsing: the
	// `inventory …` commands and the local `ui` dashboard are fully
	// offline and share none of the migration flags.
	if len(os.Args) >= 2 && os.Args[1] == "ui" {
		os.Exit(runUICmd(os.Args[2:]))
	}
	// Like the `dns` namespace below, `inventory` never falls through to
	// the migration flow: a missing or unknown subcommand used to be
	// silently ignored by flag.Parse and, with a resolvable config,
	// started a full migration dry-run.
	if len(os.Args) >= 2 && os.Args[1] == "inventory" {
		if len(os.Args) >= 3 {
			switch os.Args[2] {
			case "diff":
				os.Exit(runInventoryDiffCmd(os.Args[3:]))
			case "policy":
				os.Exit(runInventoryPolicyCmd(os.Args[3:]))
			case "dns-plan":
				os.Exit(runInventoryDNSPlanCmd(os.Args[3:]))
			case "email-plan":
				os.Exit(runInventoryEmailPlanCmd(os.Args[3:]))
			case "checklist":
				os.Exit(runInventoryChecklistCmd(os.Args[3:]))
			case "cron-plan":
				os.Exit(runInventoryCronPlanCmd(os.Args[3:]))
			}
		}
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration inventory <diff|policy|dns-plan|email-plan|cron-plan|checklist> … (each has its own --help)")
		os.Exit(2)
	}
	// The `dns` namespace never falls through to the migration flow: before
	// PR 6C these tokens were silently ignored by flag.Parse and, with a
	// resolvable config, started a full migration dry-run — an unknown dns
	// subcommand is an error instead.
	if len(os.Args) >= 2 && os.Args[1] == "dns" {
		if len(os.Args) >= 3 {
			switch os.Args[2] {
			case "verify":
				os.Exit(runDNSVerifyCmd(os.Args[3:]))
			case "apply":
				os.Exit(runDNSApplyCmd(os.Args[3:]))
			}
		}
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration dns <verify|apply> --plan dns_import_plan.json …")
		os.Exit(2)
	}
	// The `cron` namespace mirrors `dns` and `email`: apply is the cron
	// writer (destination only), verify the read-only re-certification.
	if len(os.Args) >= 2 && os.Args[1] == "cron" {
		if len(os.Args) >= 3 {
			switch os.Args[2] {
			case "apply":
				os.Exit(runCronApplyCmd(os.Args[3:]))
			case "verify":
				os.Exit(runCronVerifyCmd(os.Args[3:]))
			}
		}
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration cron <apply|verify> … (each has its own --help)")
		os.Exit(2)
	}
	// The `migration` namespace is the session governance layer: offline
	// management of migration sessions (create, list, show, set-status,
	// attach-artifact, archive). Never falls through to the migration flow.
	if len(os.Args) >= 2 && os.Args[1] == "migration" {
		os.Exit(runMigrationCmd(os.Args[2:]))
	}
	// The `email` namespace (PR 2B-1) mirrors `dns`: apply is the email
	// config writer (destination only), verify the read-only
	// re-certification; an unknown subcommand is an error, never a
	// fall-through to the migration flow.
	if len(os.Args) >= 2 && os.Args[1] == "email" {
		if len(os.Args) >= 3 {
			switch os.Args[2] {
			case "apply":
				os.Exit(runEmailApplyCmd(os.Args[3:]))
			case "verify":
				os.Exit(runEmailVerifyCmd(os.Args[3:]))
			}
		}
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration email <apply|verify> … (each has its own --help)")
		os.Exit(2)
	}

	var (
		apply            = flag.Bool("apply", false, "create missing domains + migrate the selected data (default: dry-run)")
		dryRun           = flag.Bool("dry-run", false, "explicit dry-run: analyze + compare SRC/DEST, no changes")
		mailFlag         = flag.Bool("mail", false, "migrate mail (mailboxes) only; default (no --mail/--file/--db) does ALL")
		fileFlag         = flag.Bool("file", false, "migrate website files (docroots) only; default does ALL")
		dbFlag           = flag.Bool("db", false, "migrate databases (MySQL) only; default does ALL")
		onlyDomain       = flag.String("domain", "", "narrow to a single domain (docroot + mail); composes with --mail/--file; never databases")
		onlyMailbox      = flag.String("mailbox", "", "narrow to a single mailbox local@domain (implies mail only)")
		full             = flag.Bool("full", false, "with --apply, force re-sync of every mailbox even if consistent")
		forceSync        = flag.Bool("force-sync", false, "alias of --full")
		applyMirror      = flag.Bool("apply-mirror", false, "like --apply, but MIRROR each mailbox: rename the destination mailbox aside (<user>-bak) and re-copy ALL messages from the source, so mail that exists only on the dest is removed from the live mailbox. Files/databases behave as under --apply.")
		verifyCsum       = flag.Bool("verify-checksums", false, "stricter fast-skip: when count+UIDVALIDITY match, also compare the exact message-ID set before skipping a mailbox")
		deepVerify       = flag.Bool("deep-verify", false, "with --apply, verify by CONTENT hash (sha256 per web file; slower, reads every byte on both sides) instead of metadata only — catches same-size corruption")
		cfgPath          = flag.String("config", "", "path to host.yaml (default: configs/host.yaml next to the binary or CWD)")
		logLevel         = flag.String("log-level", "info", "log verbosity: info | debug (debug traces SSH sessions, transfers, and network errors to stderr)")
		showVersion      = flag.Bool("version", false, "print the version and exit")
		runID            = flag.String("run-id", "", "optional run identifier for structured output (default: auto-generated)")
		outputDir        = flag.String("output-dir", "", "output directory for artifacts (default: current working directory)")
		jsonEvents       = flag.Bool("json-events", false, "write JSONL events to <output-dir>/events.jsonl")
		reportJSON       = flag.Bool("report-json", false, "write JSON report to <output-dir>/report.json")
		accountInventory = flag.Bool("account-inventory", false, "collect a read-only account inventory and exit (no migration)")
	)
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println(version.String())
		return
	}

	switch *logLevel {
	case "debug":
		logx.SetDebug(true)
	case "info", "":
		// default
	default:
		fmt.Fprintf(os.Stderr, "error: unknown --log-level %q (use info or debug)\n", *logLevel)
		os.Exit(2)
	}

	if *apply && *dryRun {
		fmt.Fprintln(os.Stderr, "error: --apply and --dry-run are mutually exclusive")
		os.Exit(2)
	}
	if *applyMirror && *dryRun {
		fmt.Fprintln(os.Stderr, "error: --apply-mirror and --dry-run are mutually exclusive")
		os.Exit(2)
	}
	if *apply && *applyMirror {
		fmt.Fprintln(os.Stderr, "error: --apply and --apply-mirror are mutually exclusive")
		os.Exit(2)
	}
	if *accountInventory {
		if *apply || *applyMirror {
			fmt.Fprintln(os.Stderr, "error: --account-inventory is mutually exclusive with --apply/--apply-mirror")
			os.Exit(2)
		}
		if *mailFlag || *fileFlag || *dbFlag {
			fmt.Fprintln(os.Stderr, "error: --account-inventory collects the full account; do not combine with --mail/--file/--db")
			os.Exit(2)
		}
		if *onlyDomain != "" || *onlyMailbox != "" {
			fmt.Fprintln(os.Stderr, "error: --account-inventory collects the full account; do not combine with --domain/--mailbox")
			os.Exit(2)
		}
	}
	if err := validateScopeFilters(*onlyDomain, *onlyMailbox, *mailFlag, *fileFlag, *dbFlag); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

	path, altConfigs, err := resolveConfigPath(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	// Always announce WHICH config is in effect — a stale installed host.yaml silently
	// shadowing the intended one would otherwise migrate the wrong accounts unnoticed.
	fmt.Fprintf(os.Stderr, "Config: %s\n", path)
	if len(altConfigs) > 0 {
		fmt.Fprintf(os.Stderr, "warning: multiple host.yaml found; using %s (ignoring %s). Pass --config to choose explicitly.\n",
			path, strings.Join(altConfigs, ", "))
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	// Signal handling — a SINGLE handler so the two interrupts behave
	// deterministically:
	//   1st Ctrl-C  -> cancel ctx (in-flight work stops; deferred cleanups like
	//                  token revoke and connection close run to completion);
	//   2nd Ctrl-C  -> force-exit, for the rare case the cleanup itself wedges.
	// Using ONE handler (not NotifyContext + a second signal.Notify on the same
	// signals) avoids a race where the second Ctrl-C could os.Exit before the
	// first interrupt's cleanup — e.g. token revocation — has finished.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go handleSignals(cancel)

	// Selection: --mail / --file / --db choose WHAT to migrate; --domain / --mailbox
	// narrow WHICH domain or mailbox. With no kind flag set the default is "all",
	// with two exceptions: --mailbox is mail-only, and --domain defaults to
	// docroot+mail and NEVER databases (cPanel databases are account-wide and only
	// loosely tied to a domain). validateScopeFilters has already rejected the
	// illegal combinations (e.g. --domain --db, --mailbox --file).
	doMail, doFile, doDB := *mailFlag, *fileFlag, *dbFlag
	switch {
	case *onlyMailbox != "":
		doMail, doFile, doDB = true, false, false
	case *onlyDomain != "":
		if !doMail && !doFile {
			doMail, doFile = true, true
		}
		doDB = false
	default:
		if !doMail && !doFile && !doDB {
			doMail, doFile, doDB = true, true, true
		}
	}

	// --apply-mirror only changes the mail flow; warn (but proceed) if mail is not
	// selected so it does not silently behave like a plain --apply.
	if *applyMirror && !doMail {
		fmt.Fprintln(os.Stderr, "warning: --apply-mirror changes only the mail flow; with --file/--db only it behaves like --apply")
	}

	outDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: cannot determine the current working directory (needed for the logs/ artifacts):", err)
		os.Exit(1)
	}
	if *outputDir != "" {
		outDir = *outputDir
	}

	if *runID != "" {
		if err := events.ValidateRunID(*runID); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(2)
		}
	}

	// The emitter always feeds the phase collector (report.json's
	// phases_completed works with --report-json alone) and tees to the JSONL
	// writer only when --json-events is set.
	collector := newPhaseCollector()
	em := events.Emitter{Emit: collector.observe}
	if *jsonEvents {
		evPath := filepath.Join(outDir, "events.jsonl")
		ew, err := events.NewWriter(evPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: cannot create events file:", err)
			os.Exit(1)
		}
		defer ew.Close()
		var evWriteErr sync.Once
		em = events.Emitter{Emit: func(e events.Event) {
			collector.observe(e)
			if err := ew.Write(e); err != nil {
				evWriteErr.Do(func() {
					fmt.Fprintln(os.Stderr, "warning: events.jsonl write error:", err)
				})
			}
		}}
	}

	if *accountInventory {
		rid := *runID
		if rid == "" {
			rid = events.NewRunID(time.Now())
		}
		if err := runAccountInventory(ctx, cfg, outDir, rid, em, *reportJSON); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}

	startedAt := time.Now()
	opts := migrate.Options{
		Apply:           *apply || *applyMirror,
		ForceSync:       *full || *forceSync,
		VerifyChecksums: *verifyCsum,
		DeepVerify:      *deepVerify,
		MirrorMail:      *applyMirror,
		DoMail:          doMail,
		DoFile:          doFile,
		DoDB:            doDB,
		OnlyDomain:      *onlyDomain,
		OnlyMailbox:     *onlyMailbox,
		OutputDir:       outDir,
		RunID:           *runID,
		Events:          em,
		Now:             startedAt,
	}
	runErr := migrate.Run(ctx, cfg, opts)
	finishedAt := time.Now()

	if *reportJSON {
		rpt := buildRunReport(opts, cfg, startedAt, finishedAt, runErr, ctx.Err(),
			collector.completed(), runArtifacts(outDir, collector.applyPhaseSeen(), *jsonEvents))
		rptPath := filepath.Join(outDir, "report.json")
		if werr := events.WriteReport(rptPath, rpt); werr != nil {
			fmt.Fprintln(os.Stderr, "warning: could not write report.json:", werr)
		}
	}

	if err := runErr; err != nil {
		if ctx.Err() != nil {
			// Interrupted: report cleanly with a distinct exit code (130 =
			// terminated by Ctrl-C, the shell convention).
			fmt.Fprintln(os.Stderr, "\ninterrupted — stopped; no further changes will be made.")
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runAccountInventory(ctx context.Context, cfg config.Config, outDir, runID string, em events.Emitter, writeReportJSON bool) error {
	startedAt := time.Now()
	srcRef := events.HostRef{IP: cfg.Src.IP, User: cfg.Src.SSHUser}
	em.Send(events.Event{
		RunID: runID, TS: startedAt,
		Level: events.LevelInfo, Type: events.EventRunStarted,
		Message: "account inventory started",
		Source:  srcRef,
	})

	fail := func(err error) error {
		em.Send(events.Event{
			RunID: runID, TS: time.Now(),
			Level: events.LevelError, Type: events.EventRunFailed,
			Message: err.Error(), Source: srcRef,
		})
		return err
	}

	pool, err := sshx.DialBoth(ctx, cfg, "")
	if err != nil {
		return fail(err)
	}
	defer pool.Close()

	srcInfo := accountinventory.HostInfo{User: cfg.Src.SSHUser, Host: cfg.Src.IP}
	var destInfo accountinventory.HostInfo
	var destRunner cpanel.Runner
	if cfg.DestConfigured() {
		destInfo = accountinventory.HostInfo{User: cfg.Dest.SSHUser, Host: cfg.Dest.IP}
		destRunner = pool.Dest
	}

	result, err := accountinventory.Collect(ctx, pool.Src, destRunner, srcInfo, destInfo)
	if err != nil {
		return fail(fmt.Errorf("inventory collection: %w", err))
	}

	srcPath := filepath.Join(outDir, "inventory_source.json")
	if err := accountinventory.WriteInventoryJSON(srcPath, result.Source); err != nil {
		return fail(fmt.Errorf("write source inventory: %w", err))
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", srcPath)

	if result.Dest != nil {
		destPath := filepath.Join(outDir, "inventory_destination.json")
		if err := accountinventory.WriteInventoryJSON(destPath, *result.Dest); err != nil {
			return fail(fmt.Errorf("write dest inventory: %w", err))
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", destPath)
	}

	reportPath := filepath.Join(outDir, "inventory_report.md")
	if err := accountinventory.WriteReport(reportPath, result); err != nil {
		return fail(fmt.Errorf("write report: %w", err))
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", reportPath)

	if writeReportJSON {
		var destRef events.HostRef
		if cfg.DestConfigured() {
			destRef = events.HostRef{IP: cfg.Dest.IP, User: cfg.Dest.SSHUser}
		}
		rpt := events.RunReport{
			RunID:           runID,
			Version:         version.String(),
			Mode:            "account-inventory",
			Source:          srcRef,
			Dest:            destRef,
			StartedAt:       startedAt,
			FinishedAt:      time.Now(),
			ExitStatus:      events.ExitSuccess,
			PhasesCompleted: []events.Phase{},
			Warnings:        accountinventory.AggregateWarnings(result),
			Errors:          []string{},
		}
		rptPath := filepath.Join(outDir, "report.json")
		if werr := events.WriteReport(rptPath, rpt); werr != nil {
			fmt.Fprintln(os.Stderr, "warning: could not write report.json:", werr)
		}
	}

	em.Send(events.Event{
		RunID: runID, TS: time.Now(),
		Level: events.LevelInfo, Type: events.EventRunCompleted,
		Message: "account inventory completed",
	})
	return nil
}

func buildRunReport(opts migrate.Options, cfg config.Config, startedAt, finishedAt time.Time, runErr, ctxErr error, phases []events.Phase, artifacts map[string]string) events.RunReport {
	mode := "dry-run"
	if opts.Apply {
		mode = "apply"
	}
	status := events.ExitSuccess
	errs := []string{}
	if runErr != nil {
		if ctxErr != nil {
			status = events.ExitInterrupted
		} else {
			status = events.ExitFailed
		}
		errs = append(errs, runErr.Error())
	}
	runID := opts.RunID
	if runID == "" {
		runID = events.NewRunID(opts.Now)
	}
	if phases == nil {
		phases = []events.Phase{}
	}
	return events.RunReport{
		RunID:   runID,
		Version: version.String(),
		Mode:    mode,
		Scope: events.ReportScope{
			Mail:          opts.DoMail,
			Files:         opts.DoFile,
			Databases:     opts.DoDB,
			DomainFilter:  opts.OnlyDomain,
			MailboxFilter: opts.OnlyMailbox,
		},
		Source:          events.HostRef{IP: cfg.Src.IP, User: cfg.Src.SSHUser},
		Dest:            events.HostRef{IP: cfg.Dest.IP, User: cfg.Dest.SSHUser},
		StartedAt:       startedAt,
		FinishedAt:      finishedAt,
		ExitStatus:      status,
		PhasesCompleted: phases,
		Warnings:        []string{},
		Errors:          errs,
		Artifacts:       artifacts,
	}
}

// phaseCollector records completed phases from the event stream, in
// first-completion order and deduplicated, for report.json's
// phases_completed. It also tracks whether ANY apply-phase event was
// observed (started/completed/failed) — proof this run actually entered
// runApply. Mutex-guarded so a future concurrent emitter cannot corrupt it
// (the race CI job is authoritative).
type phaseCollector struct {
	mu        sync.Mutex
	seen      map[events.Phase]bool
	phases    []events.Phase
	applySeen bool
}

func newPhaseCollector() *phaseCollector {
	return &phaseCollector{seen: map[events.Phase]bool{}}
}

// applyPhases is the set of phases only runApply emits.
var applyPhases = map[events.Phase]bool{
	events.PhaseCreateDomains: true,
	events.PhaseMigrateMail:   true, events.PhaseVerifyMail: true,
	events.PhaseCopyFiles: true, events.PhaseVerifyFiles: true,
	events.PhaseMigrateDB: true, events.PhaseVerifyDB: true,
}

// observe records e when it is a phase_completed event for a named phase;
// run-level events (no phase) and every other event type are ignored for
// phases_completed, but any apply-phase event marks the run as having
// entered the apply flow.
func (c *phaseCollector) observe(e events.Event) {
	if e.Phase == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if applyPhases[e.Phase] {
		c.applySeen = true
	}
	if e.Type != events.EventPhaseCompleted || c.seen[e.Phase] {
		return
	}
	c.seen[e.Phase] = true
	c.phases = append(c.phases, e.Phase)
}

// completed returns a copy of the recorded phases.
func (c *phaseCollector) completed() []events.Phase {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]events.Phase{}, c.phases...)
}

// applyPhaseSeen reports whether this run emitted any apply-phase event.
func (c *phaseCollector) applyPhaseSeen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.applySeen
}

// runArtifacts records the run's artifact files in report.json, by
// EXISTENCE CHECK only — never on trust. The migration report log is
// claimed only when THIS run provably entered the apply flow
// (applyPhaseSeen): migration_report.log has a fixed name in outDir, so an
// apply run that failed before runApply (connect/analyze error) must not
// claim a stale log left by a previous run. events.jsonl is recorded only
// when --json-events was set (this run created it).
func runArtifacts(outDir string, applyPhaseSeen, jsonEvents bool) map[string]string {
	arts := map[string]string{}
	if applyPhaseSeen {
		if p := filepath.Join(outDir, "logs", "migration_report.log"); fileIsRegular(p) {
			arts["migration_report_log"] = p
		}
	}
	if jsonEvents {
		if p := filepath.Join(outDir, "events.jsonl"); fileIsRegular(p) {
			arts["events_jsonl"] = p
		}
	}
	return arts
}

// fileIsRegular reports whether path exists and is a regular file.
func fileIsRegular(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// handleSignals is the SINGLE interrupt handler. The first SIGINT/SIGTERM cancels
// the run (via cancel), letting in-flight work stop and deferred cleanups — token
// revoke, connection close — run to completion; it prints a notice telling the
// user a second Ctrl-C will force-quit. The second signal force-exits, for the
// rare case the cleanup itself wedges.
//
// One handler on one channel means the two presses are ordered deterministically:
// there is no separate consumer that could os.Exit before the first interrupt's
// cleanup has finished. (A buffer of 2 ensures a quick double Ctrl-C is not lost.)
func handleSignals(cancel context.CancelFunc) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)

	<-ch // first interrupt: ask for a clean shutdown
	fmt.Fprintln(os.Stderr, "\ninterrupting — finishing the current step and shutting down cleanly (press Ctrl-C again to force quit) ...")
	cancel()

	<-ch // second interrupt: give up and exit now
	fmt.Fprintln(os.Stderr, "\nforced quit.")
	os.Exit(130)
}

// resolveConfigPath finds host.yaml when --config is not given. It looks, in
// order, under configs/ and then the bare directory, relative to both the binary's
// directory and the current working directory, and returns the FIRST match plus any
// OTHER distinct files that also matched. The caller announces the chosen path and
// warns when alternates exist: a stale installed config silently shadowing the
// intended one is a real foot-gun (it would migrate the wrong accounts). Matches
// that resolve to the SAME file via different bases (e.g. the binary dir IS the cwd)
// are de-duplicated by inode (os.SameFile), so they never look ambiguous.
func resolveConfigPath(explicit string) (path string, alternates []string, err error) {
	if explicit != "" {
		return explicit, nil, nil
	}

	var bases []string
	if exe, err := os.Executable(); err == nil {
		bases = append(bases, filepath.Dir(exe))
	}
	bases = append(bases, ".") // current working directory

	var found []string
	var infos []os.FileInfo
	for _, base := range bases {
		for _, rel := range []string{
			filepath.Join("configs", "host.yaml"),
			"host.yaml",
		} {
			cand := filepath.Join(base, rel)
			fi, statErr := os.Stat(cand)
			if statErr != nil {
				continue
			}
			dup := false
			for _, prev := range infos {
				if os.SameFile(prev, fi) {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			infos = append(infos, fi)
			found = append(found, cand)
		}
	}
	if len(found) == 0 {
		return "", nil, fmt.Errorf("host.yaml not found (use --config); copy configs/host_template.yaml to configs/host.yaml")
	}
	return found[0], found[1:], nil
}

// validateScopeFilters checks the --domain/--mailbox narrowing flags and their
// interaction with the kind flags (--mail/--file/--db). It returns a user-facing
// error (nil when the combination is valid). --mailbox is mail-only and already
// names its own domain; --domain composes with --mail/--file but NEVER with
// databases (cPanel databases are account-wide and only loosely tied to a domain
// by the wp-config path).
func validateScopeFilters(onlyDomain, onlyMailbox string, mailFlag, fileFlag, dbFlag bool) error {
	if onlyMailbox != "" {
		if onlyDomain != "" {
			return fmt.Errorf("--mailbox and --domain are mutually exclusive (a mailbox already names its domain)")
		}
		if fileFlag || dbFlag {
			return fmt.Errorf("--mailbox is mail-only; do not combine it with --file/--db")
		}
		local, domain, ok := migrate.SplitMailbox(onlyMailbox)
		if !ok {
			return fmt.Errorf("invalid --mailbox %q: must be local@domain", onlyMailbox)
		}
		if err := validate.MailboxUser(local); err != nil {
			return fmt.Errorf("invalid --mailbox %q: %v", onlyMailbox, err)
		}
		if err := validate.Domain(domain); err != nil {
			return fmt.Errorf("invalid --mailbox %q: %v", onlyMailbox, err)
		}
	}
	if onlyDomain != "" {
		if dbFlag {
			return fmt.Errorf("--domain does not support databases (cPanel databases are account-wide); drop --db, or migrate databases without --domain")
		}
		if err := validate.Domain(onlyDomain); err != nil {
			return fmt.Errorf("invalid --domain %q: %v", onlyDomain, err)
		}
	}
	return nil
}

func usage() {
	fmt.Fprintf(os.Stderr, `cpanel-self-migration — cPanel email + website-file + database migration tool

Usage: %s [--apply|--apply-mirror|--dry-run|--account-inventory] [--mail] [--file] [--db] [--domain DOMAIN] [--mailbox ADDR] [--full] [--verify-checksums] [--deep-verify] [--config PATH] [--log-level LEVEL] [--run-id ID] [--output-dir DIR] [--json-events] [--report-json]

  (default) dry-run  : analyze + compare SOURCE/DEST, no changes
  --apply            : create missing domains + migrate the selected data
  --apply-mirror     : like --apply, but MIRROR each mailbox — rename the dest
                       mailbox aside (<user>-bak) and re-copy ALL messages, so
                       mail existing only on the dest is removed from the live
                       mailbox; files/databases behave as under --apply
  --mail             : migrate MAIL only (mailboxes: accounts + messages)
  --file             : migrate WEBSITE FILES only (docroots / public_html)
  --db               : migrate DATABASES only (MySQL: data + users + grants)
                       (with none of --mail/--file/--db, ALL are migrated)
  --domain DOMAIN    : narrow to a single domain — its docroot + mailboxes;
                       composes with --mail/--file (e.g. --domain X --mail).
                       Databases are NOT in --domain scope (--domain --db is
                       rejected: cPanel databases are account-wide)
  --mailbox ADDR     : narrow to a single mailbox (local@domain); copies +
                       verifies only that mailbox (implies mail only)
  --full             : with --apply, force re-sync of every mailbox
  --verify-checksums : with --apply, compare the exact message-ID set when
                       count+UIDVALIDITY match, before skipping a mailbox
  --deep-verify      : with --apply, verify by CONTENT hash (sha256 per web
                       file) instead of metadata only — catches same-size
                       corruption; slower (reads every byte on both sides)
  --config PATH      : path to host.yaml (default: configs/host.yaml)
  --log-level LEVEL  : info (default) or debug (verbose diagnostics to stderr)
  --run-id ID        : optional run identifier for structured output (default:
                       auto-generated as run-YYYYMMDD-HHMMSS)
  --output-dir DIR   : output directory for all artifacts — logs/, events.jsonl,
                       report.json (default: current working directory)
  --json-events      : write JSONL events to <output-dir>/events.jsonl (one JSON
                       object per line, append-only; does not suppress stdout)
  --report-json      : write JSON summary to <output-dir>/report.json at the end
                       of the run (does not suppress stdout)
  --account-inventory: collect a read-only account inventory (domains, mailboxes,
                       databases) and exit — no migration is performed

The SOURCE host is ALWAYS read-only; all writes target the DESTINATION.
Mail is normally MERGED into the destination (existing messages are kept, only
missing ones are copied); --apply-mirror instead makes each mailbox an EXACT
copy of the source, moving any dest-only mail aside to <user>-bak first. Do NOT
use --apply-mirror after switching the MX to the new server: it would remove
mail delivered there. Website files are copied as a MIGRATION (the destination
docroot is emptied first, within a safety guard, then mirrored from the source).
Databases are dumped read-only (mysqldump --single-transaction), recreated on
the destination with the destination account prefix, and each site's
wp-config.php is rewritten to point at the new database. Logs are written under
logs/.

Offline subcommands (each has its own --help): inventory diff | policy |
dns-plan | email-plan | checklist, and ui (local read-only dashboard over the
artifacts). Writer subcommands: email apply (destination-only email-config
writer, dry-run by default) and email verify (read-only re-certification).
`, os.Args[0])
}
