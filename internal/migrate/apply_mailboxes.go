package migrate

import (
	"context"
	"fmt"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/domainname"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/maildir"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

type mailboxApplyResult struct {
	failed     int
	unverified int
}

// mirrorVanishHook, if non-nil, is invoked under --apply-mirror right before the
// copy reads the source — after the source-occupancy gate and after MirrorBox has
// renamed the live destination aside. TEST-ONLY seam (nil in production) to
// deterministically reproduce the TOCTOU where a source proven occupied at the gate
// vanishes before SyncBoxProgressDomains reads it.
var mirrorVanishHook func(email string)

// mirrorEmptiedLiveDest reports whether a --apply-mirror copy emptied a LIVE
// destination mailbox without bringing the source's mail back — the silent-empty
// TOCTOU the mirror gate alone cannot close (the source is proven present at the
// gate but can vanish before the copy reads it, sending 0 files). It is true only
// when MirrorBox set the previous live mailbox aside (backedUpDir non-empty: there
// WAS live mail to lose), the source held messages at the gate (srcGateMsgCount > 0:
// a genuinely-empty source mirrors to an empty destination legitimately), and the
// destination is empty after the copy (destMsgCount == 0: the copy brought nothing
// back).
//
// Scope and a deliberate, documented over-fire: an empty destination after we
// emptied a populated live one is INDISTINGUISHABLE at the destination between a
// data-loss TOCTOU (the case this closes) and a source legitimately emptied around
// mirror time (a concurrent full expunge). Both leave the live mailbox emptied with
// nothing brought back, so we fail closed and preserve <user>-bak rather than report
// a clean 1:1 of an emptied mailbox — the conservative direction for a destructive,
// recoverable operation. We do NOT gate this on the copy's own file count (e.g.
// FilesTotal): a full expunge that leaves the dovecot-uidlist control file behind
// makes the copy observe a non-empty source while ZERO messages are actually copied,
// so a file-count gate would re-open the false-OK. A PARTIAL shortfall (destMsgCount
// between 1 and srcGateMsgCount) is intentionally NOT caught here — it is left to the
// verify step's live per-folder compare, which must not be second-guessed by a stale
// gate baseline (that would false-FAIL a source legitimately shrunk mid-copy, which
// the rescan path is designed to allow). Pure; unit-tested.
func mirrorEmptiedLiveDest(backedUpDir string, srcGateMsgCount, destMsgCount int) bool {
	return backedUpDir != "" && srcGateMsgCount > 0 && destMsgCount == 0
}

// sumFolderCounts totals the per-folder message counts from GetFolderStats.
func sumFolderCounts(folders map[string]maildir.FolderStats) int {
	n := 0
	for _, f := range folders {
		n += f.Count
	}
	return n
}

// applyMailboxes migrates the active mailboxes idempotently, writing per-mailbox
// outcome lines to the shared reporter. The integrity check (verify) is run
// separately by runApply, so mail and web files can share one report file. It
// returns the number of mailboxes that FAILED to migrate (account or copy error)
// plus selected mailboxes that could not be certified as migrated during this
// step, so runApply can turn lost or unapplied mailboxes into a non-zero exit.
// Domain-failed/blocked skips remain skips because their root cause is already
// counted at the domain level.
func applyMailboxes(ctx context.Context, pool *sshx.Pool, cfg config.Config, pd migrationData, opts Options, log *logx.Logger, rep *report.Reporter) (mailboxApplyResult, error) {
	log.Step("Migrating active mailboxes (%d) ...", len(pd.Mailboxes))

	// --full (and --apply-mirror, which empties the dest mailbox first) re-streams
	// every file, ignoring the per-file delta. A per-batch stall timeout aborts
	// (and retries) an attempt that wedges with no progress.
	transfer := maildir.Transfer{Src: pool.Src, Dest: pool.Dest, Full: opts.ForceSync || opts.MirrorMail, Timeout: sshx.DefaultStallTimeout}

	total := len(pd.Mailboxes)
	var result mailboxApplyResult
	var done, unchanged, skipped, failed int
	finish := func() mailboxApplyResult {
		result.failed = failed
		return result
	}
	reportedTypeWarnings := map[string]bool{}
	for i, m := range pd.Mailboxes {
		// Stop immediately if interrupted (Ctrl-C), instead of churning through
		// the remaining mailboxes with closed connections.
		if ctx.Err() != nil {
			log.Warn("interrupted — %d of %d mailboxes processed; stopping", i, total)
			rep.Logf("INTERRUPTED after %d/%d mailboxes.", i, total)
			return finish(), ctx.Err()
		}

		email := m.Email()
		if issue, ok := domainTypeIssue(pd, m.Domain); ok && issue.WarnMail && !reportedTypeWarnings[domainname.Key(m.Domain)] {
			rep.FileOnlyf("  [domain WARN] %s — %s; mail will still be attempted", m.Domain, issue.Reason())
			reportedTypeWarnings[domainname.Key(m.Domain)] = true
		}

		// Skip everything tied to a domain whose creation failed earlier.
		if domainFailed(pd, m.Domain) {
			reason := "domain '" + m.Domain + "' creation failed"
			rep.LogScreenFile(itemStr(log, "→", email, "%s", log.Yellow("skip — "+reason)), report.SkipLine(email, reason))
			skipped++
			continue
		}
		if reason, blocked := domainBlocked(pd, m.Domain); blocked {
			rep.LogScreenFile(itemStr(log, "→", email, "%s", log.Yellow("skip — "+reason)), report.SkipLine(email, reason))
			skipped++
			continue
		}

		// Safety: destination domain must exist.
		if !domainname.Has(pd.DestDomainSet, m.Domain) {
			reason := "destination domain '" + m.Domain + "' not configured"
			rep.LogScreenFile(itemStr(log, "→", email, "%s", log.Yellow("skip — "+reason)), report.SkipLine(email, reason))
			skipped++
			continue
		}
		if m.Hash == "" {
			reason := "no password hash found on source; account/password not applied"
			rep.LogScreenFile(itemStr(log, "~", email, "%s", log.Red("UNVERIFIED — "+reason)), report.UnverifiedLine(email, reason))
			result.unverified++
			continue
		}
		destDomain, ok := destDomainNameFor(pd, m.Domain)
		if !ok {
			reason := destDomainResolutionIssue(pd, m.Domain)
			rep.LogScreenFile(itemStr(log, "✗", email, "%s", log.Red("FAIL — "+reason)), report.FailLine(email, reason))
			failed++
			continue
		}

		// Create or update the account on the destination (idempotent). On a
		// fresh create, an orphan Maildir (dir present but account unconfigured)
		// is renamed to <user>-bak[.N] first; report it so it's visible.
		ens, err := cpanel.EnsureAccount(ctx, pool.Dest, destDomain, m.User, m.Hash)
		if err != nil {
			if stopOnInterrupt(ctx, log, rep, email, i, total) {
				return finish(), ctx.Err()
			}
			rep.LogScreenFile(itemStr(log, "✗", email, "%s", log.Red("FAIL — account step: "+err.Error())), report.FailLine(email, "account step: "+err.Error()))
			failed++
			continue
		}
		state := ens.State
		logx.Debug("applyMailboxes %s: account %s, backedup=%q, proceeding to stats check", email, state, ens.BackedUpDir)
		if ens.BackedUpDir != "" {
			// An orphan Maildir was found on dest (dir present, account not
			// configured) and moved aside before re-creating the account. This is
			// a non-routine event — surface it prominently on screen, not just in
			// the report.
			log.Warn("orphan maildir for %s renamed to %q on dest before re-creating the account", email, ens.BackedUpDir)
			rep.FileOnlyf("  [backup] %s — orphan maildir renamed to %s on dest", email, ens.BackedUpDir)
		}

		// --apply-mirror: make the destination an EXACT mirror of the source.
		// Rename the dest mailbox aside to <user>-bak[.N] (recoverable) so the copy
		// below re-creates it from scratch; mail that exists ONLY on the destination
		// (e.g. Trash) is thereby moved out of the live mailbox instead of being kept.
		// Runs AFTER EnsureAccount (the account must exist) and only on the dest.
		//
		// mirrorBackedUpDir / srcGateMsgCount are carried from the destructive gate to
		// the post-copy mirror-vanish check below (both zero/"" for non-mirror runs).
		var mirrorBackedUpDir string
		var srcGateMsgCount int
		if opts.MirrorMail {
			// FAIL-CLOSED PRECONDITION: MirrorBox below renames the LIVE destination
			// mailbox aside before re-copying from the source. If the source root is
			// absent or unreadable, the copy would bring nothing back and the live
			// destination would be silently emptied (the -bak is recoverable, but the
			// run would still report success). So prove the SOURCE root exists and is
			// readable FIRST; if not, FAIL this mailbox WITHOUT touching the dest. An
			// EMPTY-but-present source is allowed — mirroring to an empty source is valid.
			//
			// This closes the systematic case (a source absent/unreadable up front). The
			// narrow TOCTOU window — a source proven present here that vanishes before
			// SyncBoxProgressDomains reads it, so the copy sends 0 files and the
			// just-emptied live mailbox would be reported synced — is closed by recording
			// the source occupancy at this gate (srcGateMsgCount, below) and re-asserting
			// after the copy that the destination we emptied came back non-empty (the
			// post-copy mirror-vanish check). A genuinely-empty source (occupancy 0)
			// mirrors to an empty destination legitimately and is left alone by that check.
			srcPresent, err := transfer.SourceBoxReadable(ctx, m.Domain, m.User)
			if err != nil {
				if stopOnInterrupt(ctx, log, rep, email, i, total) {
					return finish(), ctx.Err()
				}
				rep.LogScreenFile(itemStr(log, "✗", email, "%s", log.Red("FAIL — mirror prep: source unreadable: "+err.Error())), report.FailLine(email, "mirror prep: source mailbox unreadable ("+err.Error()+"); live destination left intact (not mirrored)"))
				failed++
				continue
			}
			if !srcPresent {
				reason := "mirror prep: source mailbox mail/" + m.Domain + "/" + m.User + " absent on source; live destination left intact (not mirrored)"
				rep.LogScreenFile(itemStr(log, "✗", email, "%s", log.Red("FAIL — "+reason)), report.FailLine(email, reason))
				failed++
				continue
			}

			// Record the source occupancy NOW, before MirrorBox empties the live dest:
			// the baseline the post-copy mirror-vanish check compares against. Use the
			// authoritative per-folder reader (GetFolderStats), the SAME one verify uses,
			// NOT the whole-tree GetBoxStats: GetBoxStats guards only the mailbox root and
			// swallows per-subfolder errors (best-effort undercount), so a source whose
			// mail sits in an UNREADABLE subfolder would undercount to 0 here and silently
			// disarm the mirror-vanish check below (srcGateMsgCount > 0) — re-opening the
			// very TOCTOU we are closing. GetFolderStats require_listable's every subfolder,
			// so an unreadable one is a hard error that fails closed WITHOUT touching the
			// dest, exactly like the readability gate above.
			gateFolders, err := maildir.GetFolderStats(ctx, pool.Src, m.Domain, m.User)
			if err != nil {
				if stopOnInterrupt(ctx, log, rep, email, i, total) {
					return finish(), ctx.Err()
				}
				reason := "mirror prep: could not read source occupancy (" + err.Error() + "); live destination left intact (not mirrored)"
				rep.LogScreenFile(itemStr(log, "✗", email, "%s", log.Red("FAIL — "+reason)), report.FailLine(email, reason))
				failed++
				continue
			}
			srcGateMsgCount = sumFolderCounts(gateFolders)

			mr, err := transfer.MirrorBox(ctx, destDomain, m.User)
			if err != nil {
				if stopOnInterrupt(ctx, log, rep, email, i, total) {
					return finish(), ctx.Err()
				}
				rep.LogScreenFile(itemStr(log, "✗", email, "%s", log.Red("FAIL — mirror prep: "+err.Error())), report.FailLine(email, "mirror prep: "+err.Error()))
				failed++
				continue
			}
			mirrorBackedUpDir = mr.BackedUpDir
			if mr.BackedUpDir != "" {
				log.Warn("--apply-mirror: dest mailbox for %s renamed to %q before re-copy", email, mr.BackedUpDir)
				rep.FileOnlyf("  [mirror] %s — dest mailbox renamed to %s before re-copy", email, mr.BackedUpDir)
			} else {
				logx.Debug("applyMailboxes %s: --apply-mirror, dest mailbox absent/empty, nothing to set aside", email)
			}
		}

		// Fast-skip: identical count + UIDVALIDITY on both sides. Skipped under
		// --apply-mirror (the dest was just emptied, so a full re-copy is required).
		if !opts.ForceSync && !opts.MirrorMail {
			ss, e1 := maildir.GetBoxStats(ctx, pool.Src, m.Domain, m.User)
			ds, e2 := maildir.GetBoxStats(ctx, pool.Dest, destDomain, m.User, maildir.GuardRoot())
			if e1 != nil || e2 != nil {
				logx.Debug("applyMailboxes %s: fast-skip stats read failed (srcErr=%v destErr=%v); falling through to a full copy", email, e1, e2)
			}
			if e1 == nil && e2 == nil && ss.Consistent(ds) {
				// With --verify-checksums, don't trust count+UIDVALIDITY alone:
				// compare the exact set of message IDs. If they differ (same
				// count but different content), fall through to the copy.
				skip := true
				if opts.VerifyChecksums {
					sset, es := maildir.GetMessageSet(ctx, pool.Src, m.Domain, m.User)
					dset, ed := maildir.GetMessageSet(ctx, pool.Dest, destDomain, m.User, maildir.GuardRoot())
					switch {
					case es != nil || ed != nil:
						// Could not read a message set — fall through to a full copy, but
						// trace WHY, distinct from a genuine content difference (the set read
						// is itself what might be misbehaving).
						logx.Debug("applyMailboxes %s: --verify-checksums set read failed (srcErr=%v destErr=%v); not fast-skipping", email, es, ed)
						skip = false
					case !maildir.SameMessageSet(sset, dset):
						// Same count + UIDVALIDITY but a different set of messages — the exact
						// case --verify-checksums exists to catch. Log a few example diverging
						// IDs so an operator can see WHICH messages differ (this is what the
						// previously-unused DiffMessageSets is for).
						onlySrc, onlyDest := maildir.DiffMessageSets(sset, dset, 5)
						logx.Debug("applyMailboxes %s: --verify-checksums message-set differs, re-copying (src-only e.g. %v; dest-only e.g. %v)", email, onlySrc, onlyDest)
						skip = false
					}
				}
				if skip {
					logx.Debug("applyMailboxes %s: fast-skip succeeded (msg+UIDVALIDITY consistent, VerifyChecksums=%v)", email, opts.VerifyChecksums)
					rep.LogScreenFile(itemStr(log, "=", email, "%s", log.Green("unchanged")+" (msg+UIDVALIDITY match)"), report.UnchangedLine(email))
					unchanged++
					continue
				}
			}
		}

		// Bridge-copy the Maildir (tar stream SRC -> Go -> DEST), transferring
		// only the files missing on the destination. The progress bar lives in the
		// item's OWN row (neutral "→" marker), where the result text will go; once
		// the copy finishes, replace turns that same row into the final "✓/✗"
		// result line, then we move to the next item. The bar's total is set from
		// inside the transfer once the delta is known. If the live source changes
		// mid-copy, mailboxProgress freezes the failed batch's row, prints a re-scan
		// note, and opens a fresh bar for the continuation (see its Rescan).
		prog := newMailboxProgress(log, rep, email)
		if opts.MirrorMail && mirrorVanishHook != nil {
			mirrorVanishHook(email) // TEST-ONLY: reproduce the source vanishing before the copy reads it
		}
		res, err := transfer.SyncBoxProgressDomains(ctx, m.Domain, destDomain, m.User, prog)
		if err != nil {
			if stopOnInterrupt(ctx, log, rep, email, i, total) {
				prog.replace(itemStr(log, "→", email, "%s", log.Yellow("interrupted")))
				return finish(), ctx.Err()
			}
			prog.replace(itemStr(log, "✗", email, "%s", log.Red("FAIL — mailbox copy failed: "+err.Error())))
			rep.FileOnlyf("%s", report.FailLine(email, "mailbox copy failed: "+err.Error()))
			failed++
			continue
		}

		logx.Debug("applyMailboxes %s: mailbox synced, %d of %d files sent", email, res.FilesSent, res.FilesTotal)

		// --apply-mirror: MirrorBox emptied the live destination before this copy, so a
		// source that vanished in the TOCTOU window after the occupancy gate would have
		// sent nothing and left the destination empty — which classifyVerify later reads
		// as a clean 0==0 match. Confirm here, where we still hold the gate baseline and
		// the -bak name, that the destination we emptied came back non-empty. Only pay
		// the read when a loss is even possible (live mail was set aside AND the source
		// held messages at the gate); mirrorEmptiedLiveDest is the authoritative predicate.
		//
		// The destination occupancy uses the lenient whole-tree GetBoxStats (NOT the
		// per-folder reader the gate uses): here under-counting an unreadable subfolder
		// to 0 would FALSE-FAIL a destination that actually refilled, so leniency is the
		// safe direction — while a wholly-unreadable root or a transport error still
		// errors out and fails closed (we cannot confirm the live mailbox we emptied).
		if opts.MirrorMail && mirrorBackedUpDir != "" && srcGateMsgCount > 0 {
			destStats, err := maildir.GetBoxStats(ctx, pool.Dest, destDomain, m.User, maildir.GuardRoot())
			if err != nil {
				if stopOnInterrupt(ctx, log, rep, email, i, total) {
					return finish(), ctx.Err()
				}
				reason := "mirror verify: could not confirm destination occupancy after the copy (" + err.Error() + "); the previous live mailbox is preserved in " + mirrorBackedUpDir + " — investigate before trusting this mailbox as migrated"
				prog.replace(itemStr(log, "✗", email, "%s", log.Red("FAIL — "+reason)))
				rep.FileOnlyf("%s", report.FailLine(email, reason))
				failed++
				continue
			}
			if mirrorEmptiedLiveDest(mirrorBackedUpDir, srcGateMsgCount, destStats.MsgCount) {
				reason := fmt.Sprintf("mirror left the destination EMPTY: the source held %d message(s) at the occupancy gate but none reached the destination — the source was emptied or removed around mirror time (a TOCTOU race and a concurrent full expunge are indistinguishable here). The previous live mailbox is preserved in %s; investigate and recover from there before trusting this mailbox as migrated", srcGateMsgCount, mirrorBackedUpDir)
				prog.replace(itemStr(log, "✗", email, "%s", log.Red("FAIL — "+reason)))
				rep.FileOnlyf("%s", report.FailLine(email, reason))
				failed++
				continue
			}
		}
		if res.FilesSent == 0 {
			// Nothing was missing — fast-skip didn't catch it (e.g. UIDVALIDITY
			// changed) but every message is already present.
			prog.replace(itemStr(log, "✓", email, "%s — account %s, already in sync", log.Green("synced"), state))
			rep.FileOnlyf("%s", report.OKLine(email, string(state))+" (already in sync)")
		} else {
			prog.replace(itemStr(log, "✓", email, "%s — account %s, %d messages + %d control = %d files", log.Green("synced"), state, res.MsgTotal, res.ControlTotal, res.FilesTotal))
			rep.FileOnlyf("%s", report.OKLine(email, string(state))+fmt.Sprintf(" — %d messages + %d control = %d files", res.MsgTotal, res.ControlTotal, res.FilesTotal))
		}
		done++
	}

	rep.FileOnlyf("")
	rep.FileOnlyf("Mailbox migration summary: %d migrated, %d unchanged, %d skipped, %d unverified, %d failed.",
		done, unchanged, skipped, result.unverified, failed)
	log.OK("mailbox step done: %d migrated, %d unchanged, %d skipped, %d unverified, %d failed",
		done, unchanged, skipped, result.unverified, failed)

	// The integrity check is run by runApply (so mail + web share one report).
	return finish(), nil
}

// stopOnInterrupt reports and returns true when ctx was cancelled (Ctrl-C /
// timeout) while the current mailbox was being processed, so an in-flight step's
// error (EnsureAccount / MirrorBox / SyncBoxProgress) is treated as an interruption
// rather than a per-mailbox FAIL — which would otherwise inflate the failed count
// and the run's exit code over a deliberate cancellation. Mirrors the top-of-loop
// interrupt guard, for an error that surfaces mid-step instead of between mailboxes.
func stopOnInterrupt(ctx context.Context, log *logx.Logger, rep *report.Reporter, email string, processed, total int) bool {
	return stopOnInterruptDuring(ctx, log, rep, email, "mailboxes", processed, total)
}

func stopOnInterruptDuring(ctx context.Context, log *logx.Logger, rep *report.Reporter, item, unit string, processed, total int) bool {
	if ctx.Err() == nil {
		return false
	}
	log.Warn("interrupted during %s — %d of %d %s processed; stopping", item, processed, total, unit)
	rep.Logf("INTERRUPTED during %s after %d/%d %s.", item, processed, total, unit)
	return true
}
