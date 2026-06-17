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
	DoMail    bool
	DoFile    bool
	DoDB      bool
	OutputDir string // where artifacts (logs) are written
	Now       time.Time
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

	// Default selection (defensive): if no flag is set, do all. main.go normally
	// resolves this, but a programmatic caller might not.
	doMail, doFile, doDB := opts.DoMail, opts.DoFile, opts.DoDB
	if !doMail && !doFile && !doDB {
		doMail, doFile, doDB = true, true, true
		opts.DoMail, opts.DoFile, opts.DoDB = true, true, true
	}
	if opts.Apply && !destConfigured {
		return fmt.Errorf("apply mode requires a configured destination; fill dest in host.yaml or run without --apply for source-only analysis")
	}

	// The pipeline is built dynamically from the selection: only the active
	// steps are numbered, so a mail-only run shows [1..6] and a file-only run
	// shows its own steps, with no misleading "skipped" noise for the other
	// flow. Domain creation is shared (needed by both mail and files).
	plan := buildPipeline(opts, destConfigured)
	log := logx.New(plan.total)
	mode := "DRY-RUN (no changes)"
	if opts.Apply {
		mode = "APPLY (writes to destination)"
	}
	scope := scopeLabel(doMail, doFile, doDB)
	log.Plain("cpanel-self-migration — mode: %s — scope: %s", mode, scope)
	log.Plain("  SOURCE: %s   (read-only)", srcRef)
	if destConfigured {
		log.Plain("  DEST  : %s", destRef)
	} else {
		log.Plain("  DEST  : (not configured — source-only analysis)")
	}
	log.Plain("")

	// ---- STEP: connect to both servers ----
	if destConfigured {
		log.Step("Connecting to source and destination ...")
		log.Detail("opening SSH connections ...")
	} else {
		log.Step("Connecting to source ...")
		log.Detail("opening SSH connection ...")
	}
	pool, err := sshx.DialBoth(ctx, cfg, "")
	if err != nil {
		return err
	}
	defer pool.Close()
	if destConfigured {
		log.OK("connected to source and destination")
	} else {
		log.OK("connected to source (destination not configured)")
	}

	// ---- STEP: analyze source ~/mail (read-only) — only when mail is in scope.
	if doMail {
		log.Step("Analyzing the SOURCE mailboxes (~/mail) ...")
		// Live scan as ONE inline row: action "~/mail scan" on the left, a live
		// "N mailboxes" counter on the right while the (single) ~/mail walk runs,
		// then Replace turns the row into the totals — same layout as every other
		// step; no separate static "found ..." line needed.
		prog := inlineRow(log, "→", "~/mail scan", 0, "mailboxes")
		rep, err := analyze(ctx, pool.Src, srcRef.String(), date, func() { prog.Add(1) })
		if err != nil {
			prog.Finish()
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
		return nil
	}

	// ---- Gather the data the selected phases need (read-only). ----
	// gatherData renders its own live progress (a per-operation bar that ends in a
	// persistent "✓ inventory  read — ..." line), so no static header is printed
	// here — that would just blink with a frozen cursor during the reads.
	pd, err := gatherData(ctx, pool, log, doMail, doFile, doDB)
	if err != nil {
		return err
	}
	scopeOpts := Options{DoMail: doMail, DoFile: doFile, DoDB: doDB}
	overrides := dbOverrides(cfg)
	uses := updateSelectedDomainCoverage(&pd, scopeOpts, overrides)
	addons, subs := plannedDomainCreates(pd, uses)
	preflightAddonLabelCollisions(&pd, addons, subs)
	updateDomainTypeIssuesForUses(&pd, uses)
	logDataSummary(log, pd, doMail, doFile, doDB)

	// ---- STEP: compare domains + mailboxes SRC vs DEST (read-only) — mail scope
	// only. compareDryRun reports BOTH the domain plan (present / to create) and the
	// per-mailbox comparison, so the step title names both. ----
	if doMail {
		log.Step("Comparing SOURCE and DESTINATION domains and mailboxes (read-only) ...")
		compareDryRun(ctx, &comparator{src: pool.Src, dest: pool.Dest}, pd, log, opts.MirrorMail)
	}

	// ---- STEPS: analyze + compare web files (read-only) — file scope only. ----
	if doFile {
		log.Step("Analyzing the SOURCE web files (docroots) ...")
		if err := analyzeWebFiles(ctx, pool, pd, log, opts.OutputDir, srcRef.String(), date, false); err != nil {
			return err
		}

		log.Step("Comparing SOURCE and DESTINATION web files (read-only) ...")
		compareWebFiles(ctx, pool, pd, log)
	}

	// ---- STEPS: analyze + compare databases (read-only) — db scope only. ----
	if doDB {
		overrides := dbOverrides(cfg)
		log.Step("Analyzing the SOURCE databases ...")
		if err := analyzeDBs(ctx, pd, log, opts.OutputDir, srcRef.String(), date, overrides, false); err != nil {
			return err
		}

		log.Step("Comparing SOURCE and DESTINATION databases (read-only) ...")
		compareDBs(pd, log, overrides)
	}

	// ---- Apply (or stop here in dry-run). ----
	if !opts.Apply {
		log.Plain("")
		log.Plain("DRY-RUN complete: no changes were made to either server.")
		log.Plain("Re-run with --apply to perform the migration (%s).", scope)
		return nil
	}

	return runApply(ctx, pool, cfg, pd, opts, log, srcRef.String(), destRef.String(), date)
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

// scopeLabel renders the selection for the banner.
func scopeLabel(doMail, doFile, doDB bool) string {
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
	switch len(parts) {
	case 0:
		return "nothing"
	case 1:
		return parts[0] + " only"
	default:
		return strings.Join(parts, " + ")
	}
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
// the mailbox / docroot / database counts.
func logDataSummary(log *logx.Logger, pd migrationData, doMail, doFile, doDB bool) {
	var present, missingAddon, missingSub, blocked int
	for _, d := range pd.SrcDomains {
		if _, ok := domainBlocked(pd, d.Name); ok {
			blocked++
			continue
		}
		switch model.ActionFor(d.Type, domainname.Has(pd.DestDomainSet, d.Name)) {
		case model.AlreadyPresent:
			present++
		case model.CreateAddon:
			missingAddon++
		case model.CreateSub:
			missingSub++
		}
	}
	if blocked > 0 {
		log.Info("source domains: %d (%d already on destination, %d addon + %d sub to create, %d blocked)",
			len(pd.SrcDomains), present, missingAddon, missingSub, blocked)
	} else {
		log.Info("source domains: %d (%d already on destination, %d addon + %d sub to create)",
			len(pd.SrcDomains), present, missingAddon, missingSub)
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
