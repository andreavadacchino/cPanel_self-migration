package migrate

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// runApply performs the write phases, gated by the selection:
//   - create missing destination domains (shared; runs if mail OR files are
//     selected, because both need the destination docroot/account to exist);
//   - migrate active mailboxes + verify mailbox integrity (mail);
//   - copy website files + verify web files (files).
//
// All write phases share a single migration_report.log. Implemented across
// apply_domains.go / apply_mailboxes.go / verify.go / apply_webfiles.go.
func runApply(ctx context.Context, pool *sshx.Pool, cfg config.Config, pd migrationData, opts Options, log *logx.Logger, srcRef, destRef, date string) error {
	// --apply-mirror is destructive on the destination mailboxes: warn loudly and
	// once, up front, before any write. The source always stays read-only.
	if opts.MirrorMail && opts.DoMail {
		log.Warn("--apply-mirror: destination mailboxes are reset to mirror the source; dest-only mail is moved aside to <user>-bak (recoverable). Source read-only.")
	}

	// One report file for the shared domain step and every selected data flow.
	// The domain step can fail before mail/web/db start, and applyOutcome points
	// operators at migration_report.log, so open it before applyDomains.
	rep, closeReport, err := openReport(opts, log, srcRef, destRef, date)
	if err != nil {
		return err
	}
	defer closeReport()

	logx.Debug("runApply: flows mail=%v file=%v db=%v; mirror=%v deepVerify=%v verifyChecksums=%v forceSync=%v",
		opts.DoMail, opts.DoFile, opts.DoDB, opts.MirrorMail, opts.DeepVerify, opts.VerifyChecksums, opts.ForceSync)

	// Shared domain creation: a web docroot can only be filled once the addon/sub
	// exists on the destination, exactly like a mailbox needs its domain — and a
	// database's destination user is prefixed with the account, so the database
	// flow needs the destination account/domains to exist too. Runs when ANY flow
	// is selected (matches buildPipeline, which counts this step for mail||file||db).
	if opts.DoMail || opts.DoFile || opts.DoDB {
		if err := applyDomains(ctx, pool, cfg, &pd, opts, log, rep); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	// Per-flow tallies: items that FAILED during apply, and items still DIVERGENT
	// after the read-only verify. Both are aggregated into a single non-zero
	// outcome AFTER every selected flow has run (so a mailbox failure does not skip
	// the web/db flows), mirroring how the database flow already turns dbFailed
	// into a process-level error.
	var tally applyTally

	if opts.DoMail {
		res, err := applyMailboxes(ctx, pool, cfg, pd, opts, log, rep)
		if err != nil {
			return err
		}
		tally.mailFailed = res.failed
		tally.mailUnverified = res.unverified
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// --verify-checksums implies the deep mail content check too (it already
		// promises message-identity precision), alongside --deep-verify.
		d, err := verify(ctx, pool, pd, log, rep, opts.DeepVerify || opts.VerifyChecksums)
		if err != nil {
			return err
		}
		tally.mailDiff = d
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	if opts.DoFile {
		n, err := applyWebFiles(ctx, pool, pd, log, rep)
		if err != nil {
			return err
		}
		tally.webFailed = n
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d, err := verifyWebFiles(ctx, pool, pd, log, rep, opts.DeepVerify)
		if err != nil {
			return err
		}
		tally.webDiff = d
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	if opts.DoDB {
		// Databases come AFTER web files: the files carry the wp-config.php that
		// the DB step rewrites, and a site needs its files before its database is
		// useful. The cPanel ACCOUNT credentials (the SSH password) double as the
		// MySQL credentials that can dump/import every database on each side.
		overrides := dbOverrides(cfg)
		destCreds, n, cfgUnrewritten, cfgUnmigrated, err := applyDBs(ctx, pool, pd, log, rep,
			cfg.Src.SSHUser, cfg.Src.SSHPass, overrides, opts.DeepVerify)
		if err != nil {
			return err
		}
		tally.dbFailed = n
		tally.dbConfigNotRewritten = cfgUnrewritten
		tally.dbConfigUnmigrated = cfgUnmigrated
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d, err := verifyDBs(ctx, pool, pd, log, rep,
			cfg.Src.SSHUser, cfg.Src.SSHPass, overrides, destCreds, opts.DeepVerify)
		if err != nil {
			return err
		}
		tally.dbDiff = d
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	// A mailbox/docroot/database that failed to migrate or is still divergent after
	// verify — or a domain that failed to create (whose mail/files/databases were
	// then skipped) — must make the run end non-zero, so a migration that lost data
	// is NEVER reported as a clean success. The per-item FAIL/skip/verify lines are
	// already on screen and in the report; this aggregates them into one
	// process-level error.
	tally.failedDomains = len(pd.FailedDomains)
	tally.blockedDomains = len(pd.BlockedDomains)
	logx.Debug("applyOutcome tally: domainsFailed=%d domainsBlocked=%d mailFailed=%d mailUnverified=%d mailDiff=%d webFailed=%d webDiff=%d dbFailed=%d dbConfigNotRewritten=%d dbConfigUnmigrated=%d dbDiff=%d",
		tally.failedDomains, tally.blockedDomains, tally.mailFailed, tally.mailUnverified, tally.mailDiff,
		tally.webFailed, tally.webDiff, tally.dbFailed, tally.dbConfigNotRewritten, tally.dbConfigUnmigrated, tally.dbDiff)
	return applyOutcome(tally)
}

// applyTally collects, per flow, the items that FAILED during apply and the items
// still DIVERGENT after the read-only verify (benign divergences — mail DEST
// AHEAD, db UNVERIFIED-already-counted — are excluded by the verify functions),
// plus domains that failed to create or were blocked by selected-domain
// inventory coverage.
type applyTally struct {
	mailFailed, mailUnverified    int
	webFailed, dbFailed           int
	mailDiff, webDiff, dbDiff     int
	failedDomains, blockedDomains int
	// dbConfigNotRewritten: databases whose DATA migrated but whose referencing site
	// config could NOT be rewritten — the site still points at the OLD database, so the
	// cutover is incomplete and the run must end non-zero (not a clean success).
	dbConfigNotRewritten int
	// dbConfigUnmigrated: sites whose DB-config FORMAT this tool does not discover/rewrite
	// at all (Magento 1 local.xml, PrestaShop 1.7 parameters.php, Symfony DATABASE_URL,
	// SilverStripe) — the config was never even handled, so it is a coverage gap reported
	// separately from dbConfigNotRewritten (whose message asserts the DB migrated).
	dbConfigUnmigrated int
}

// applyOutcome turns the tally into a single process-level error (nil when
// everything that ran succeeded and verified). Pure; unit-tested. An item that
// both failed during apply AND is divergent after verify is listed under both —
// the two lines describe the same item from the copy side and the integrity side.
func applyOutcome(t applyTally) error {
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, what))
		}
	}
	add(t.failedDomains, "domain(s) failed to create (dependent mail/files/databases skipped)")
	add(t.blockedDomains, "domain(s) blocked by domain creation preflight (dependent mail/files/databases skipped)")
	add(t.mailFailed, "mailbox(es) failed to migrate")
	add(t.mailUnverified, "mailbox(es) missing source password hash; account/password not applied")
	add(t.webFailed, "docroot(s) failed to copy")
	add(t.dbFailed, "database(s) failed to migrate")
	add(t.dbConfigNotRewritten, "database(s) migrated but their site config was NOT rewritten (the site still points at the OLD database)")
	add(t.dbConfigUnmigrated, "site(s) use a DB-config format this tool does not migrate/verify (Magento 1 / PrestaShop 1.7 / Symfony / SilverStripe) — set their destination DB by hand")
	add(t.mailDiff, "mailbox(es) still divergent after verify")
	add(t.webDiff, "docroot(s) still divergent after verify")
	add(t.dbDiff, "database(s) still divergent after verify")
	if len(parts) == 0 {
		return nil
	}
	return fmt.Errorf("migration completed with issues: %s — see the FAIL/UNVERIFIED/skip/verify lines above and logs/migration_report.log",
		strings.Join(parts, "; "))
}

// openReport creates logs/migration_report.log, writes its header, and returns a
// Reporter teeing to screen + file plus a close func for the underlying file.
func openReport(opts Options, log *logx.Logger, srcRef, destRef, date string) (*report.Reporter, func(), error) {
	rf, _, err := createLogFile(opts.OutputDir, "migration_report.log")
	if err != nil {
		return nil, nil, err
	}
	rep, err := report.NewReporter(os.Stdout, rf, srcRef, destRef, date)
	if err != nil {
		_ = rf.Abort() // error-path cleanup: the NewReporter error is what matters
		return nil, nil, err
	}
	// migration_report.log is a LOCAL convenience log tee'd to os.Stdout, so the
	// screen always has the full record. But the file is still the artifact the run
	// points the operator to ("see logs/migration_report.log"), so a failure to
	// produce it must be surfaced, not silently swallowed. Two distinct failures:
	//   - a WRITE error during the run -> the report would be truncated;
	//   - a CLOSE/RENAME error at the end -> rf.Close performs the atomic close+rename
	//     that COMMITS the temp file as the final report; if it fails (read-only
	//     mount, ENOSPC, an immutable/occupied target, ...) the report was never
	//     committed and the pointer above is misleading.
	// Warn on whichever applies. When writes already failed, Close just surfaces that
	// same write error, so the truncation warning covers it — skip the commit warning.
	return rep, func() {
		werr := rep.Err()
		if werr != nil {
			log.Warn("migration_report.log writes failed (%v) — the on-screen record is complete, but the report file may be truncated", werr)
		}
		if cerr := rf.Close(); cerr != nil && werr == nil {
			log.Warn("migration_report.log could not be committed (%v) — the on-screen record is complete, but logs/migration_report.log may be missing or stale", cerr)
		}
	}, nil
}
