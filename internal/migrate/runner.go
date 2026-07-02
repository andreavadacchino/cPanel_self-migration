// Package migrate orchestrates the migration phases: connect, analyze, compare,
// and on --apply: create domains, then migrate mailboxes, website files, and
// MySQL databases, each followed by a verify pass. Only the flows selected by
// --mail/--file/--db run (all if none). The SOURCE is only ever read from.
package migrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/dbmig"
	"github.com/tis24dev/cPanel_self-migration/internal/domainname"
	"github.com/tis24dev/cPanel_self-migration/internal/events"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// Options controls a run.
type Options struct {
	Apply     bool // false = dry-run (default)
	ForceSync bool // --full: re-sync even already-consistent boxes
	// VerifyChecksums makes the fast-skip stricter: when SRC and DEST agree on
	// message count + UIDVALIDITY, also compare the exact set of message IDs
	// before skipping the copy. Catches same-count-but-different-content cases.
	VerifyChecksums bool
	// DeepVerify upgrades the post-apply integrity checks from metadata-only to
	// CONTENT hashing (--deep-verify): the web-file verify compares a per-file
	// sha256 instead of size, catching same-size corruption a truncated transfer
	// could leave. It reads every byte on both sides, so it is opt-in and bounded
	// by a per-item size ceiling (above which it falls back to the metadata check
	// with a DEEP-SKIPPED note).
	DeepVerify bool
	// MirrorMail makes the mail flow MIRROR the source instead of merging into the
	// destination (--apply-mirror): each destination mailbox is renamed aside to
	// <user>-bak[.N] (recoverable) and fully re-copied, so mail that exists ONLY on
	// the destination (e.g. Trash, or mail delivered after an MX switch) is removed
	// from the live mailbox. It implies a full re-copy and skips the fast-skip.
	// Files/databases are unaffected (they already replace the destination).
	MirrorMail bool
	// DoMail / DoFile / DoDB select WHAT to migrate. They are the RESOLVED
	// booleans (main.go applies the "no flag => all" rule), so the runner never
	// re-derives the semantics. Mail covers the mailbox flow (analyze/compare/
	// migrate/verify); File covers the web-file flow (analyze/compare/copy/
	// verify); DB covers the database flow (analyze/compare/migrate/verify).
	// Domain creation runs when any is set.
	DoMail bool
	DoFile bool
	DoDB   bool
	// OnlyDomain, when non-empty, narrows the run to a single source domain: its
	// docroot and mailboxes (composed with DoMail/DoFile). Databases are NEVER in
	// --domain scope. Validated against the source inventory at run time.
	OnlyDomain string
	// OnlyMailbox, when non-empty (local@domain), narrows the run to exactly one
	// ACTIVE mailbox; it is inherently mail-only.
	OnlyMailbox string
	OutputDir string // where artifacts (logs) are written
	Now       time.Time
	RunID     string         // optional; generated if empty
	Events    events.Emitter // optional; zero value = no events emitted
}

// timeFormat is the timestamp layout used in the log artifacts.
const timeFormat = "2006-01-02 15:04:05 -0700"

// logsDir is the subdirectory (under OutputDir) where the .log artifacts are
// written, keeping the project root clean.
const logsDir = "logs"

// atomicLogFile writes a log artifact through a temp file in the logs directory.
// Close commits it with an atomic rename only after the file itself closed cleanly.
// Abort closes and removes the temp file without replacing any existing artifact.
type atomicLogFile struct {
	*os.File
	root     *os.Root
	tmp      string
	name     string
	dir      string
	writeErr error
	closed   bool
}

func (f *atomicLogFile) Write(p []byte) (int, error) {
	n, err := f.File.Write(p)
	if err != nil && f.writeErr == nil {
		f.writeErr = err
	}
	return n, err
}

func (f *atomicLogFile) WriteString(s string) (int, error) {
	n, err := f.File.WriteString(s)
	if err != nil && f.writeErr == nil {
		f.writeErr = err
	}
	return n, err
}

func (f *atomicLogFile) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	closeErr := f.File.Close()
	if f.writeErr != nil {
		_ = f.root.Remove(f.tmp)
		_ = f.root.Close()
		return f.writeErr
	}
	if closeErr != nil {
		_ = f.root.Remove(f.tmp)
		_ = f.root.Close()
		return fmt.Errorf("close temp %s: %w", filepath.Join(f.dir, f.tmp), closeErr)
	}
	if err := f.root.Rename(f.tmp, f.name); err != nil {
		_ = f.root.Remove(f.tmp)
		_ = f.root.Close()
		return fmt.Errorf("replace %s from temp %s: %w", filepath.Join(f.dir, f.name), filepath.Join(f.dir, f.tmp), err)
	}
	_ = f.root.Close()
	return nil
}

func (f *atomicLogFile) Abort() error {
	if f.closed {
		return nil
	}
	f.closed = true
	err := f.File.Close()
	_ = f.root.Remove(f.tmp)
	_ = f.root.Close()
	if err != nil {
		return fmt.Errorf("close temp %s: %w", filepath.Join(f.dir, f.tmp), err)
	}
	return nil
}

// createLogFile creates <OutputDir>/logs/<name> for writing and returns the open
// file plus its display path. The file is created THROUGH an os.Root scoped to the
// logs dir, so name can never resolve outside it. The final path is replaced only
// when the returned file is successfully closed, so a write/flush failure cannot
// clobber the previous artifact.
func createLogFile(outputDir, name string) (*atomicLogFile, string, error) {
	if name == "" || name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return nil, "", fmt.Errorf("invalid log file name %q", name)
	}
	dir := filepath.Join(outputDir, logsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", fmt.Errorf("create logs dir %s: %w", dir, err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, "", fmt.Errorf("stat logs dir %s: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, "", fmt.Errorf("logs path %s is not a real directory", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, "", fmt.Errorf("chmod logs dir %s: %w", dir, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "", fmt.Errorf("open logs dir %s: %w", dir, err)
	}
	tmp := fmt.Sprintf(".%s.%d.%d.tmp", name, os.Getpid(), time.Now().UnixNano())
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = root.Close()
		return nil, "", fmt.Errorf("create temp %s: %w", filepath.Join(dir, tmp), err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = root.Remove(tmp)
		_ = root.Close()
		return nil, "", fmt.Errorf("chmod temp %s: %w", filepath.Join(dir, tmp), err)
	}
	return &atomicLogFile{File: f, root: root, tmp: tmp, name: name, dir: dir}, filepath.Join(dir, name), nil
}

// emitEvent is a convenience wrapper for sending events with host context.
func emitEvent(em events.Emitter, runID string, srcRef, destRef hostRef, phase events.Phase, etype events.EventType, level events.Level, msg string, data any) {
	em.Send(events.Event{
		RunID:   runID,
		TS:      time.Now(),
		Level:   level,
		Phase:   phase,
		Type:    etype,
		Message: msg,
		Source:  events.HostRef{IP: srcRef.IP, User: srcRef.User},
		Dest:    events.HostRef{IP: destRef.IP, User: destRef.User},
		Data:    data,
	})
}

// Run executes the migration according to opts. cfg must have a valid source;
// if the destination is not configured, Run stops after the source analysis.
func Run(ctx context.Context, cfg config.Config, opts Options) error {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	date := opts.Now.Format(timeFormat)

	srcRef := hostRef{User: cfg.Src.SSHUser, IP: cfg.Src.IP, Port: cfg.Src.Port}
	destConfigured := cfg.DestConfigured()
	destRef := hostRef{User: cfg.Dest.SSHUser, IP: cfg.Dest.IP, Port: cfg.Dest.Port}

	// Reject illegal scope combinations FAIL-FAST before any resolution. Options is
	// exported, so a programmatic caller could otherwise have an invalid mix silently
	// normalized by resolveScopeFlags (e.g. OnlyDomain+DoDB -> mail+file), quietly
	// changing what gets migrated. The CLI never trips this (main.go already passes a
	// resolved, valid Options), so it only protects non-CLI callers.
	if err := validateScopeCombos(opts); err != nil {
		return err
	}
	// Resolve WHAT to migrate (defensive: mirrors main.go's CLI resolution so a
	// programmatic caller and buildPipeline's step count stay consistent). Kept in a
	// pure helper so the coercion is unit-testable.
	doMail, doFile, doDB := resolveScopeFlags(opts)
	opts.DoMail, opts.DoFile, opts.DoDB = doMail, doFile, doDB
	if opts.Apply && !destConfigured {
		return fmt.Errorf("apply mode requires a configured destination; fill dest in host.yaml or run without --apply for source-only analysis")
	}
	// The --domain/--mailbox filters narrow a MIGRATION (compare against, and write
	// to, a destination). Source-only analysis (no dest configured) covers the whole
	// account; honoring the filter there is not implemented and the source-only path
	// would otherwise skip target validation and emit a banner that disagrees with
	// the account-wide artifacts. Reject up front instead of analyzing the wrong scope.
	if (opts.OnlyDomain != "" || opts.OnlyMailbox != "") && !destConfigured {
		return fmt.Errorf("--domain/--mailbox requires a configured destination (these filters scope a migration; source-only analysis covers the whole account)")
	}

	// The pipeline is built dynamically from the selection: only the active
	// steps are numbered, so a mail-only run shows [1..6] and a file-only run
	// shows its own steps, with no misleading "skipped" noise for the other
	// flow. Domain creation is shared (needed by both mail and files).
	runID := opts.RunID
	if runID == "" {
		runID = events.NewRunID(opts.Now)
	}
	// Propagate the RESOLVED run ID to runApply, which emits the apply phase
	// events from opts (PR 7C) — otherwise those events would carry the raw,
	// possibly-empty flag value.
	opts.RunID = runID

	plan := buildPipeline(opts, destConfigured)
	log := logx.New(plan.total)
	mode := "DRY-RUN (no changes)"
	if opts.Apply {
		mode = "APPLY (writes to destination)"
	}
	scope := scopeLabel(doMail, doFile, doDB, opts.OnlyDomain, opts.OnlyMailbox)
	log.Plain("cpanel-self-migration — mode: %s — scope: %s", mode, scope)
	log.Plain("  SOURCE: %s   (read-only)", srcRef)
	if destConfigured {
		log.Plain("  DEST  : %s", destRef)
	} else {
		log.Plain("  DEST  : (not configured — source-only analysis)")
	}
	log.Plain("")

	em := opts.Events
	emitEvent(em, runID, srcRef, destRef, "", events.EventRunStarted, events.LevelInfo,
		fmt.Sprintf("migration started — mode: %s — scope: %s", mode, scope), nil)

	// ---- STEP: connect to both servers ----
	emitEvent(em, runID, srcRef, destRef, events.PhaseConnect, events.EventPhaseStarted, events.LevelInfo,
		"Connecting to servers", nil)
	if destConfigured {
		log.Step("Connecting to source and destination ...")
		log.Detail("opening SSH connections ...")
	} else {
		log.Step("Connecting to source ...")
		log.Detail("opening SSH connection ...")
	}
	pool, err := sshx.DialBoth(ctx, cfg, "")
	if err != nil {
		emitEvent(em, runID, srcRef, destRef, events.PhaseConnect, events.EventPhaseFailed, events.LevelError,
			fmt.Sprintf("connect failed: %v", err), nil)
		return err
	}
	defer pool.Close()
	emitEvent(em, runID, srcRef, destRef, events.PhaseConnect, events.EventPhaseCompleted, events.LevelInfo,
		"connected", nil)
	if destConfigured {
		log.OK("connected to source and destination")
	} else {
		log.OK("connected to source (destination not configured)")
	}

	// ---- STEP: analyze source ~/mail (read-only) — only when mail is in scope.
	if doMail {
		emitEvent(em, runID, srcRef, destRef, events.PhaseAnalyzeMail, events.EventPhaseStarted, events.LevelInfo,
			"Analyzing source mailboxes", nil)
		log.Step("Analyzing the SOURCE mailboxes (~/mail) ...")
		// Live scan as ONE inline row: action "~/mail scan" on the left, a live
		// "N mailboxes" counter on the right while the (single) ~/mail walk runs,
		// then Replace turns the row into the totals — same layout as every other
		// step; no separate static "found ..." line needed.
		prog := inlineRow(log, "→", "~/mail scan", 0, "mailboxes")
		rep, err := analyze(ctx, pool.Src, srcRef.String(), date, func() { prog.Add(1) })
		if err != nil {
			prog.Finish()
			emitEvent(em, runID, srcRef, destRef, events.PhaseAnalyzeMail, events.EventPhaseFailed, events.LevelError,
				fmt.Sprintf("analyze mail failed: %v", err), nil)
			return err
		}
		af, analysisPath, err := createLogFile(opts.OutputDir, "mail_analysis.log")
		if err != nil {
			prog.Finish()
			return err
		}
		if err := report.WriteAnalysis(af, rep); err != nil {
			_ = af.Abort()
			prog.Finish()
			return fmt.Errorf("write %s: %w", analysisPath, err)
		}
		cerr := af.Close()
		if cerr != nil {
			// A Close error means a buffered write/flush to mail_analysis.log failed
			// (e.g. disk full/quota); surface it instead of reporting "wrote ..." for a
			// possibly-truncated analysis file.
			prog.Finish()
			return fmt.Errorf("closing %s: %w", analysisPath, cerr)
		}
		nd, nm, na, no := rep.Totals()
		prog.Replace(itemStr(log, "✓", "~/mail scan",
			"%s — %d mailbox(es) in %d domain(s) (%d active, %d orphan)", log.Green("done"), nm, nd, na, no))
		log.OK("wrote %s", analysisPath)
		emitEvent(em, runID, srcRef, destRef, events.PhaseAnalyzeMail, events.EventPhaseCompleted, events.LevelInfo,
			fmt.Sprintf("analyzed %d mailbox(es) in %d domain(s)", nm, nd), map[string]any{"domains": nd, "mailboxes": nm, "active": na, "orphan": no})
	}

	if !destConfigured {
		if doFile || doDB {
			pd, err := gatherSourceOnlyData(ctx, pool, log, doFile, doDB)
			if err != nil {
				return err
			}
			if doFile {
				log.Step("Analyzing the SOURCE web files (docroots) ...")
				if err := analyzeWebFiles(ctx, pool, pd, log, opts.OutputDir, srcRef.String(), date, true); err != nil {
					return err
				}
			}
			if doDB {
				overrides := dbOverrides(cfg)
				log.Step("Analyzing the SOURCE databases ...")
				if err := analyzeDBs(ctx, pd, log, opts.OutputDir, srcRef.String(), date, overrides, true); err != nil {
					return err
				}
			}
		}
		log.Plain("")
		log.Plain("Destination not configured — stopped after source analysis.")
		emitEvent(em, runID, srcRef, destRef, "", events.EventRunCompleted, events.LevelInfo,
			"source-only analysis complete", nil)
		return nil
	}

	// ---- Gather the data the selected phases need (read-only). ----
	// gatherData renders its own live progress (a per-operation bar that ends in a
	// persistent "✓ inventory  read — ..." line), so no static header is printed
	// here — that would just blink with a frozen cursor during the reads.
	emitEvent(em, runID, srcRef, destRef, events.PhaseGatherData, events.EventPhaseStarted, events.LevelInfo,
		"Gathering inventory from source and destination", nil)
	pd, err := gatherData(ctx, pool, log, doMail, doFile, doDB)
	if err != nil {
		emitEvent(em, runID, srcRef, destRef, events.PhaseGatherData, events.EventPhaseFailed, events.LevelError,
			fmt.Sprintf("gather data failed: %v", err), nil)
		return err
	}
	emitEvent(em, runID, srcRef, destRef, events.PhaseGatherData, events.EventPhaseCompleted, events.LevelInfo,
		"inventory gathered", nil)
	// Narrow the working-set to --domain / --mailbox (if set) BEFORE coverage,
	// domain-create planning, summaries, and compare/apply all read it. This one
	// call cascades to every phase; it validates the target exists on the source
	// and never touches databases. No-op when neither filter is set.
	if err := applyScopeFilter(&pd, opts, doMail, doFile, log); err != nil {
		return err
	}
	scopeOpts := Options{DoMail: doMail, DoFile: doFile, DoDB: doDB}
	overrides := dbOverrides(cfg)
	uses := updateSelectedDomainCoverage(&pd, scopeOpts, overrides)
	addons, subs := plannedDomainCreates(pd, uses)
	// Capture the survivors. preflightAddonLabelCollisions moves addon-label
	// collisions into pd.BlockedDomains; without reassigning, the len(addons)-based
	// summary below would count such an addon BOTH as "to create" (it is still in
	// addons) AND as "blocked" (now in BlockedDomains). Reassigning keeps them
	// consistent — a collision-blocked addon is counted once, as blocked.
	addons = preflightAddonLabelCollisions(&pd, addons, subs)
	updateDomainTypeIssuesForUses(&pd, uses)
	logDataSummary(log, pd, doMail, doFile, doDB, len(addons), len(subs))

	// ---- STEP: compare domains + mailboxes SRC vs DEST (read-only) — mail scope
	// only. compareDryRun reports BOTH the domain plan (present / to create) and the
	// per-mailbox comparison, so the step title names both. ----
	if doMail {
		emitEvent(em, runID, srcRef, destRef, events.PhaseCompareMail, events.EventPhaseStarted, events.LevelInfo,
			"Comparing mailboxes", nil)
		log.Step("Comparing SOURCE and DESTINATION domains and mailboxes (read-only) ...")
		compareDryRun(ctx, &comparator{src: pool.Src, dest: pool.Dest}, pd, log, opts.MirrorMail)
		emitEvent(em, runID, srcRef, destRef, events.PhaseCompareMail, events.EventPhaseCompleted, events.LevelInfo,
			"mailbox comparison done", nil)
	}

	// ---- STEPS: analyze + compare web files (read-only) — file scope only. ----
	if doFile {
		emitEvent(em, runID, srcRef, destRef, events.PhaseAnalyzeFiles, events.EventPhaseStarted, events.LevelInfo,
			"Analyzing web files", nil)
		log.Step("Analyzing the SOURCE web files (docroots) ...")
		if err := analyzeWebFiles(ctx, pool, pd, log, opts.OutputDir, srcRef.String(), date, false); err != nil {
			emitEvent(em, runID, srcRef, destRef, events.PhaseAnalyzeFiles, events.EventPhaseFailed, events.LevelError,
				fmt.Sprintf("analyze web files failed: %v", err), nil)
			return err
		}
		emitEvent(em, runID, srcRef, destRef, events.PhaseAnalyzeFiles, events.EventPhaseCompleted, events.LevelInfo,
			"web file analysis done", nil)

		emitEvent(em, runID, srcRef, destRef, events.PhaseCompareFiles, events.EventPhaseStarted, events.LevelInfo,
			"Comparing web files", nil)
		log.Step("Comparing SOURCE and DESTINATION web files (read-only) ...")
		compareWebFiles(ctx, pool, pd, log)
		emitEvent(em, runID, srcRef, destRef, events.PhaseCompareFiles, events.EventPhaseCompleted, events.LevelInfo,
			"web file comparison done", nil)
	}

	// ---- STEPS: analyze + compare databases (read-only) — db scope only. ----
	if doDB {
		emitEvent(em, runID, srcRef, destRef, events.PhaseAnalyzeDB, events.EventPhaseStarted, events.LevelInfo,
			"Analyzing databases", nil)
		overrides := dbOverrides(cfg)
		log.Step("Analyzing the SOURCE databases ...")
		if err := analyzeDBs(ctx, pd, log, opts.OutputDir, srcRef.String(), date, overrides, false); err != nil {
			emitEvent(em, runID, srcRef, destRef, events.PhaseAnalyzeDB, events.EventPhaseFailed, events.LevelError,
				fmt.Sprintf("analyze databases failed: %v", err), nil)
			return err
		}
		emitEvent(em, runID, srcRef, destRef, events.PhaseAnalyzeDB, events.EventPhaseCompleted, events.LevelInfo,
			"database analysis done", nil)

		emitEvent(em, runID, srcRef, destRef, events.PhaseCompareDB, events.EventPhaseStarted, events.LevelInfo,
			"Comparing databases", nil)
		log.Step("Comparing SOURCE and DESTINATION databases (read-only) ...")
		compareDBs(pd, log, overrides)
		emitEvent(em, runID, srcRef, destRef, events.PhaseCompareDB, events.EventPhaseCompleted, events.LevelInfo,
			"database comparison done", nil)
	}

	// ---- Apply (or stop here in dry-run). ----
	if !opts.Apply {
		log.Plain("")
		log.Plain("DRY-RUN complete: no changes were made to either server.")
		log.Plain("Re-run with %s to perform the migration (%s).", applyCommand(doMail, doFile, doDB, opts.OnlyDomain, opts.OnlyMailbox), scope)
		emitEvent(em, runID, srcRef, destRef, "", events.EventRunCompleted, events.LevelInfo,
			"dry-run complete", nil)
		return nil
	}

	err = runApply(ctx, pool, cfg, pd, opts, log, srcRef.String(), destRef.String(), date)
	if err != nil {
		emitEvent(em, runID, srcRef, destRef, "", events.EventRunFailed, events.LevelError,
			fmt.Sprintf("migration failed: %v", err), nil)
	} else {
		emitEvent(em, runID, srcRef, destRef, "", events.EventRunCompleted, events.LevelInfo,
			"migration completed", nil)
	}
	return err
}

// pipeline describes the active steps for a run (only used for the [n/N] count).
type pipeline struct {
	total int
}

// buildPipeline computes how many numbered steps a run will emit, given the
// selection and mode. Connect is always present. When no destination is
// configured, only source-side analysis steps run (no compare/apply steps).
// With a destination, Mail/File/DB each contribute analyze + compare (always)
// and migrate + verify (apply). Domain creation (apply) is shared and counted
// once when any flow is active.
func buildPipeline(opts Options, destConfigured bool) pipeline {
	n := 1 // connect
	if !destConfigured {
		if opts.DoMail {
			n++
		}
		if opts.DoFile {
			n++
		}
		if opts.DoDB {
			n++
		}
		return pipeline{total: n}
	}
	if opts.DoMail {
		n += 2 // analyze + compare mailboxes
	}
	if opts.DoFile {
		n += 2 // analyze + compare web files
	}
	if opts.DoDB {
		n += 2 // analyze + compare databases
	}
	if opts.Apply {
		if opts.DoMail || opts.DoFile || opts.DoDB {
			n++ // create domains (shared)
		}
		if opts.DoMail {
			n += 2 // migrate + verify mailboxes
		}
		if opts.DoFile {
			n += 2 // copy + verify web files
		}
		if opts.DoDB {
			n += 2 // migrate + verify databases
		}
	}
	return pipeline{total: n}
}

// validateScopeCombos rejects illegal --domain/--mailbox combinations on Options,
// mirroring the CLI's validateScopeFilters but at the (exported) Run boundary so a
// programmatic caller fails fast instead of having an invalid mix silently coerced.
// It checks ONLY the combination legality; syntax (mailbox local@domain, domain
// chars) and target existence are validated by main.go and applyScopeFilter.
func validateScopeCombos(opts Options) error {
	if opts.OnlyMailbox != "" {
		if opts.OnlyDomain != "" {
			return fmt.Errorf("--mailbox and --domain are mutually exclusive (a mailbox already names its domain)")
		}
		if opts.DoFile || opts.DoDB {
			return fmt.Errorf("--mailbox is mail-only; do not combine it with --file/--db")
		}
	}
	if opts.OnlyDomain != "" && opts.DoDB {
		return fmt.Errorf("--domain does not support databases (cPanel databases are account-wide); drop --db")
	}
	return nil
}

// resolveScopeFlags resolves WHAT to migrate from opts, applying the same rules as
// the CLI: "no kind flag set => all three", --mailbox is mail-only, and --domain
// never includes databases (defaulting to docroot+mail when no kind flag is set).
// Pure and unit-testable; Run uses it so a programmatic caller is coerced the same
// way the CLI is.
func resolveScopeFlags(opts Options) (doMail, doFile, doDB bool) {
	doMail, doFile, doDB = opts.DoMail, opts.DoFile, opts.DoDB
	if !doMail && !doFile && !doDB {
		doMail, doFile, doDB = true, true, true
	}
	switch {
	case opts.OnlyMailbox != "":
		return true, false, false
	case opts.OnlyDomain != "":
		doDB = false
		if !doMail && !doFile {
			doMail, doFile = true, true
		}
	}
	return doMail, doFile, doDB
}

// scopeLabel renders the selection for the banner, plus the active --domain /
// --mailbox filter (if any), as a parenthetical suffix.
func scopeLabel(doMail, doFile, doDB bool, onlyDomain, onlyMailbox string) string {
	var parts []string
	if doMail {
		parts = append(parts, "mail")
	}
	if doFile {
		parts = append(parts, "web files")
	}
	if doDB {
		parts = append(parts, "databases")
	}
	var base string
	switch len(parts) {
	case 0:
		base = "nothing"
	case 1:
		base = parts[0] + " only"
	default:
		base = strings.Join(parts, " + ")
	}
	switch {
	case onlyMailbox != "":
		return base + " (mailbox: " + onlyMailbox + ")"
	case onlyDomain != "":
		return base + " (domain: " + onlyDomain + ")"
	}
	return base
}

// applyCommand renders the flags to re-run the CURRENT selection under --apply.
// The kind flags (--mail/--file/--db) are independent of --apply, so a bare
// "--apply" re-run would reset the selection to ALL (the default when none is
// set); the dry-run hint must echo the active flags to preserve a narrowed run
// (e.g. "--apply --mail"). It also echoes the --domain / --mailbox filter:
//   - --mailbox is mail-only and self-describing, so "--apply --mailbox a@d".
//   - --domain defaults to docroot+mail, so a kind flag is emitted only when the
//     run is narrower than that default (exactly one of mail/file); --db is never
//     emitted under --domain. e.g. "--apply --domain X", "--apply --mail --domain X".
func applyCommand(doMail, doFile, doDB bool, onlyDomain, onlyMailbox string) string {
	if onlyMailbox != "" {
		return "--apply --mailbox " + onlyMailbox
	}
	if onlyDomain != "" {
		cmd := "--apply"
		if doMail != doFile { // narrower than the docroot+mail default
			if doMail {
				cmd += " --mail"
			} else {
				cmd += " --file"
			}
		}
		return cmd + " --domain " + onlyDomain
	}
	if doMail && doFile && doDB {
		return "--apply"
	}
	cmd := "--apply"
	if doMail {
		cmd += " --mail"
	}
	if doFile {
		cmd += " --file"
	}
	if doDB {
		cmd += " --db"
	}
	return cmd
}

// gatherWhat tailors the "reading domains…" detail line to the scope.
func gatherWhat(doMail, doFile, doDB bool) string {
	var extras []string
	if doMail {
		extras = append(extras, "active mailboxes")
	}
	if doFile || doDB {
		extras = append(extras, "document roots")
	}
	if doDB {
		extras = append(extras, "databases")
	}
	if len(extras) == 0 {
		return ""
	}
	return " and " + strings.Join(extras, ", ")
}

// logDataSummary prints the domain breakdown gathered up front plus, per scope,
// the mailbox / docroot / database counts. plannedAddons/plannedSubs are the
// ACTUAL creation plan (from plannedDomainCreates) so the "to create" counts
// reflect what the selected scope / --domain / --mailbox filter will really
// create, not every source domain absent from the destination.
func logDataSummary(log *logx.Logger, pd migrationData, doMail, doFile, doDB bool, plannedAddons, plannedSubs int) {
	var present, blocked int
	for _, d := range pd.SrcDomains {
		if _, ok := domainBlocked(pd, d.Name); ok {
			blocked++
			continue
		}
		if model.ActionFor(d.Type, domainname.Has(pd.DestDomainSet, d.Name)) == model.AlreadyPresent {
			present++
		}
	}
	if blocked > 0 {
		log.Info("source domains: %d (%d already on destination, %d addon + %d sub to create, %d blocked)",
			len(pd.SrcDomains), present, plannedAddons, plannedSubs, blocked)
	} else {
		log.Info("source domains: %d (%d already on destination, %d addon + %d sub to create)",
			len(pd.SrcDomains), present, plannedAddons, plannedSubs)
	}
	if doMail {
		log.Info("active mailboxes to migrate: %d", len(pd.Mailboxes))
	}
	if doFile || doDB {
		log.Info("source docroots: %d", len(pd.SrcDocroots))
	}
	if doDB {
		log.Info("source databases: %d (%d wp-config cred(s) found)", len(pd.Databases), len(pd.SiteCreds))
	}
}

// dbOverrides adapts the optional config.databases section into the planner's
// override map.
func dbOverrides(cfg config.Config) map[string]dbmig.Override {
	src := cfg.DBOverrides()
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]dbmig.Override, len(src))
	for name, d := range src {
		out[name] = dbmig.Override{User: d.User, Password: d.Password}
	}
	return out
}
