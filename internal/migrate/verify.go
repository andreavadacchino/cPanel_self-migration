package migrate

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/domainname"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/maildir"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// verifyKind classifies the outcome of comparing one mailbox SRC vs DEST.
type verifyKind int

const (
	vConsistent  verifyKind = iota // same count + same UIDVALIDITY
	vIncomplete                    // DEST has FEWER messages than SRC (re-run helps)
	vDestAhead                     // DEST has MORE messages than SRC (re-run does NOT help)
	vUIDMismatch                   // same count but different UIDVALIDITY
	vUnreadable                    // could not read one of the sides
)

// verdict carries the classified result plus presentation fields.
type verdict struct {
	kind  verifyKind
	label string // short tag shown in the report line
	note  string // human explanation appended to the line
}

// classifyVerify decides the verify outcome from the two stats and any read
// errors. Pure; unit-tested. It is the heart of the "tell the truth about
// whether re-running --apply helps" fix.
func classifyVerify(s, d maildir.BoxStats, srcErr, destErr error) verdict {
	switch {
	case srcErr != nil || destErr != nil:
		return verdict{vUnreadable, "UNREADABLE", "could not read one side"}
	case s.MsgCount == d.MsgCount && (s.MsgCount == 0 || s.UIDValidity == d.UIDValidity):
		// Equal counts are consistent when the UIDVALIDITY matches OR the mailbox is
		// empty on both sides — with zero messages there is nothing to keep numbered,
		// so a UIDVALIDITY difference is irrelevant (a freshly-provisioned empty
		// destination otherwise looked "DEST AHEAD" by 0, a false divergence).
		return verdict{vConsistent, "OK", ""}
	case s.UIDValidity != d.UIDValidity && s.MsgCount == d.MsgCount:
		return verdict{vUIDMismatch, "UIDVALIDITY",
			"same count but different UIDVALIDITY — re-sync to align (IMAP clients will re-download)"}
	case d.MsgCount < s.MsgCount:
		return verdict{vIncomplete, "INCOMPLETE",
			fmt.Sprintf("dest is missing %d message(s) — re-run --apply to copy them", s.MsgCount-d.MsgCount)}
	default: // d.MsgCount > s.MsgCount
		return verdict{vDestAhead, "DEST AHEAD",
			fmt.Sprintf("dest has %d message(s) NOT on source — --apply will NOT change this (extra mail lives only on dest, e.g. Trash/INBOX)", d.MsgCount-s.MsgCount)}
	}
}

// deepContent is the per-mailbox result of the --deep-verify message-body check,
// for a mailbox whose folder counts already match.
type deepContent int

const (
	contentClean      deepContent = iota // bodies verified equal (or deep verify off)
	contentDiverged                      // a body is missing or corrupted (real loss)
	contentUnverified                    // deep verify requested but digests unreadable
)

// classifyDeepContent decides the deep-content result for one mailbox. The load-
// bearing rule: when the user asked for a body check (deep) and the digests could
// not be read (digestErr), the result is contentUnverified, never contentClean — a
// check we could not run must not be reported as a check that passed. Pure.
func classifyDeepContent(deep, digestErr bool, missing, changed, unverified []string) deepContent {
	if !deep {
		return contentClean
	}
	if digestErr {
		return contentUnverified
	}
	if len(missing) > 0 || len(changed) > 0 {
		// Real loss (missing or genuinely-different bytes) outranks unverified: if some
		// bodies are corrupt AND others merely unreadable, the mailbox is CONTENT-bad.
		return contentDiverged
	}
	if len(unverified) > 0 {
		// No corruption found, but a body could not be read on a side: the user asked
		// for a content check we could not complete for it — UNVERIFIED, not clean.
		return contentUnverified
	}
	return contentClean
}

// mailVerifyBucket is the single summary category a verified mailbox rolls up to.
// Each mailbox counts under exactly ONE bucket, so the "X divergent" total and the
// hard-difference exit count never double-count a mailbox that is, say, both DEST
// AHEAD and body-corrupted.
type mailVerifyBucket int

const (
	bOK          mailVerifyBucket = iota // consistent folders + clean (or no) deep check
	bIncomplete                          // a folder is missing messages
	bUIDMismatch                         // same count, different UIDVALIDITY
	bUnreadable                          // could not read one side
	bContentBad                          // deep check found corrupted/missing bodies
	bUnverified                          // deep check requested but bodies unreadable
	bDestAhead                           // extra mail only on dest (soft, NOT data loss)
)

// classifyMailVerifyImpact rolls a mailbox's per-folder verdict and deep-content
// result into the single summary bucket it counts under, and whether that bucket is
// a HARD difference (a non-zero exit: data is missing or wrong on the destination).
//
// The load-bearing rule, and the bug it fixes: a soft DEST AHEAD mailbox whose
// bodies ALSO fail the deep check is not benign — the corruption, not the extra mail,
// is what makes it hard-fail, so it rolls up to CONTENT/UNVERIFIED, never to the soft
// DEST AHEAD bucket (which is excluded from the exit count). A folder-hard verdict
// (INCOMPLETE/UIDVALIDITY/UNREADABLE) outranks the deep result, which is then shown
// only as detail, so a mailbox that is both INCOMPLETE and corrupted counts as ONE
// hard difference, never two. Pure; unit-tested.
func classifyMailVerifyImpact(kind verifyKind, content deepContent) (bucket mailVerifyBucket, hard bool) {
	switch kind {
	case vIncomplete:
		return bIncomplete, true
	case vUIDMismatch:
		return bUIDMismatch, true
	case vUnreadable:
		return bUnreadable, true
	}
	// kind is vConsistent or vDestAhead: a deep-content failure is now the headline.
	switch content {
	case contentDiverged:
		return bContentBad, true
	case contentUnverified:
		return bUnverified, true
	}
	// Bodies are clean (or deep verify is off).
	if kind == vDestAhead {
		return bDestAhead, false
	}
	return bOK, false
}

// verify is STEP: for every active mailbox whose destination domain exists,
// compare source vs destination PER FOLDER (the INBOX root and each .Subfolder)
// on message count + UIDVALIDITY, and report a precise verdict. The per-folder
// breakdown is the fix for the aggregate check's blind spot: a shortfall in one
// folder offset by a surplus in another netted to zero and passed as OK. Both
// sides are read-only. It returns the number of mailboxes with REAL loss (a folder
// that is INCOMPLETE or UIDVALIDITY-mismatched, or an unreadable mailbox) so
// runApply can fail the run; DEST AHEAD (extra mail only on the destination, e.g.
// Trash) is reported but NOT counted — it is not data loss.
func verify(ctx context.Context, pool *sshx.Pool, pd migrationData, log *logx.Logger, rep *report.Reporter, deep bool) (int, error) {
	mode := "per-folder count + UIDVALIDITY + message body hashes (mailbox verdict)"
	if deep {
		mode = "deep: per-folder + per-message sha256 body hash"
	}
	log.Step("Verifying migration integrity (%s) ...", mode)
	rep.FileOnlyf("")
	rep.FileOnlyf("=== --apply: verifying migration integrity (%s) ===", mode)

	// Per-category presentation counters drive the summary lines; realDiff is the
	// hard-difference exit count. Both are fed ONLY through bump() from the pure
	// classifyMailVerifyImpact helper, so each mailbox is accounted exactly once.
	var ok, incomplete, ahead, uidmismatch, unreadable, contentBad, unverified, realDiff int
	// Absent-destination-domain accounting (see the domain branch in the loop):
	// domainFailedSkip/domainBlockedSkip/noHashSkip are visible-but-not-counted
	// skips (their root causes are counted by applyOutcome via FailedDomains/
	// BlockedDomains, and via mailUnverified for the no-hash case); absentUnverified
	// is a HARD mail issue (a selected mailbox whose destination domain is absent
	// with no accounted domain marker, so nothing else would make the run non-zero).
	var domainFailedSkip, domainBlockedSkip, noHashSkip, absentUnverified int
	bump := func(bucket mailVerifyBucket, hard bool) {
		switch bucket {
		case bOK:
			ok++
		case bIncomplete:
			incomplete++
		case bUIDMismatch:
			uidmismatch++
		case bUnreadable:
			unreadable++
		case bContentBad:
			contentBad++
		case bUnverified:
			unverified++
		case bDestAhead:
			ahead++
		}
		if hard {
			realDiff++
		}
	}
	for _, m := range pd.Mailboxes {
		if ctx.Err() != nil {
			log.Warn("interrupted — integrity check stopped")
			return 0, ctx.Err()
		}
		email := m.Email()
		// A mailbox whose destination domain is absent cannot be verified. Silently
		// continuing is only safe when the absence is ALREADY accounted for. Every
		// mailbox in pd.Mailboxes IS in scope — the apply/compare paths process them by
		// pd.Mailboxes, never by pd.SrcDomains — so the only benign absence is a
		// domain whose creation FAILED (already counted by applyOutcome via
		// len(FailedDomains)). Any other absent-domain mailbox (e.g. a create that
		// reported success but the domain is missing, or a domain the create step never
		// saw) had its mail neither migrated nor verified and nothing else would make
		// the run non-zero, so it is a HARD issue, not a clean exit.
		if !domainname.Has(pd.DestDomainSet, m.Domain) {
			if domainFailed(pd, m.Domain) {
				domainFailedSkip++
				rep.LogScreenFile(
					itemStr(log, "→", email, "%s — destination domain creation failed earlier; mailbox not migrated/verified", log.Yellow("SKIP")),
					report.VerifySkipLine(email, "domain '"+m.Domain+"' creation failed earlier; counted under failed domains"))
			} else if reason, blocked := domainBlocked(pd, m.Domain); blocked {
				domainBlockedSkip++
				rep.LogScreenFile(
					itemStr(log, "→", email, "%s — %s", log.Yellow("SKIP"), reason),
					report.VerifySkipLine(email, reason+"; counted under blocked domains"))
			} else {
				absentUnverified++
				rep.LogScreenFile(
					itemStr(log, "~", email, "%s — destination domain absent after the domain step (not migrated, not verified)", log.Red("UNVERIFIED")),
					report.VerifyDiffLine(email, "UNVERIFIED", "", "", "", "", "destination domain '"+m.Domain+"' absent after the domain step"))
			}
			continue
		}

		// Mirror the apply-side hash gate (apply_mailboxes.go): a mailbox with no
		// source password hash had no destination account created, so there is nothing
		// to verify. applyMailboxes already counted it ONCE as mailUnverified (the run
		// ends non-zero via applyOutcome's "missing source password hash" line);
		// verifying it here would read source mail against an absent destination and
		// re-count the SAME mailbox as a hard divergence (mailDiff). Reached only when
		// the dest domain is PRESENT (an absent/failed/blocked domain was already
		// handled above), exactly as apply reaches its hash gate only after its domain
		// gates. This defers the no-hash mailbox's non-zero exit to apply's
		// mailUnverified, which is sound because verify always runs immediately after
		// applyMailboxes over the same pd.Mailboxes (runApply).
		if m.Hash == "" {
			noHashSkip++
			logx.Debug("verify %s: SKIP — no source password hash; not applied (counted under unverified at apply)", email)
			rep.LogScreenFile(
				itemStr(log, "→", email, "%s — no source password hash; account/password not applied (counted under unverified)", log.Yellow("SKIP")),
				report.VerifySkipLine(email, "no source password hash; account/password not applied; counted under unverified at apply"))
			continue
		}
		destDomain, ok := destDomainNameFor(pd, m.Domain)
		if !ok {
			absentUnverified++
			reason := destDomainResolutionIssue(pd, m.Domain)
			rep.LogScreenFile(
				itemStr(log, "~", email, "%s — %s (not migrated, not verified)", log.Red("UNVERIFIED"), reason),
				report.VerifyDiffLine(email, "UNVERIFIED", "", "", "", "", reason))
			continue
		}
		srcF, err1 := maildir.GetFolderStats(ctx, pool.Src, m.Domain, m.User)
		destF, err2 := maildir.GetFolderStats(ctx, pool.Dest, destDomain, m.User, maildir.GuardRoot())
		if err1 != nil || err2 != nil {
			logx.Debug("verify %s: UNREADABLE (srcErr=%v destErr=%v)", email, err1, err2)
			rep.LogScreenFile(
				itemStr(log, "~", email, "%s — could not read one side", log.Red("UNREADABLE")),
				report.VerifyDiffLine(email, "UNREADABLE", "", "", "", "", "could not read one side"))
			bump(classifyMailVerifyImpact(vUnreadable, contentClean))
			continue
		}

		mv := classifyMailbox(srcF, destF)

		// --deep-verify / --verify-checksums: hash every message BODY and compare by
		// stable base ID. This catches what the per-folder counts cannot — a message
		// replaced by a different one (same count) or a corrupted/truncated body (same
		// name). When the user asked for this check and the digests cannot be read, the
		// mailbox is UNVERIFIED, never clean: a check we could not run must not be
		// reported as a check that passed (see classifyDeepContent).
		var cMissing, cChanged, cUnverified []string
		var digestErr bool
		if deep {
			// Per-message sha256 of every body, on both hosts, is the slow part of the
			// deep verify; stream a live progress row so a big mailbox shows activity
			// instead of an idle wait. The row is cleared (Finish) before the verdict
			// line prints, so the existing result branches are untouched. On a non-TTY /
			// --log-level debug run the row is inert (liveProgress off), exactly like the
			// copy step's bars.
			prog := inlineRow(log, "→", email, 0, "")
			sd, e1 := maildir.GetMessageDigests(ctx, pool.Src, m.Domain, m.User,
				maildir.WithProgress(func(n int) { prog.SetSuffix(fmt.Sprintf("src %d hashed", n)) }))
			dd, e2 := maildir.GetMessageDigests(ctx, pool.Dest, destDomain, m.User, maildir.GuardRoot(),
				maildir.WithProgress(func(n int) { prog.SetSuffix(fmt.Sprintf("dest %d hashed", n)) }))
			prog.Finish()
			if e1 == nil && e2 == nil {
				cMissing, cChanged, cUnverified = maildir.DiffMessageDigests(sd, dd, 5)
			} else {
				logx.Debug("verify %s: deep digest read failed (src=%v dest=%v)", email, e1, e2)
				digestErr = true
			}
		}
		content := classifyDeepContent(deep, digestErr, cMissing, cChanged, cUnverified)
		contentBadThis := content == contentDiverged

		// DEFAULT-tier body check (the mail sibling of the web content fingerprint): the
		// per-folder counts + UIDVALIDITY matched but no message BODY was read, so a
		// same-count body corruption or a cross-folder swap is invisible. Hash every message
		// body on both hosts and roll the PER-MESSAGE comparison up to a mailbox-level
		// verdict (keyed by stable folder-aware identity, so a flag change or a new/->cur/
		// move is NOT a false diff). --deep runs the same per-message hashing but reports
		// each diverging message; the default rolls them to one verdict.
		//
		// Runs for vConsistent AND vDestAhead. The roll-up (CountDigestDivergence) keys by
		// SOURCE-PRESENT identity and IGNORES dest-only extra messages, so a DEST AHEAD
		// mailbox's benign surplus never trips it — only a corrupted/replaced/lost SOURCE
		// body does, which then outranks the soft DEST AHEAD verdict (see
		// classifyMailVerifyImpact). Without this, a DEST AHEAD mailbox whose one source
		// message was corrupted on the destination passed clean at the default tier.
		// Unhashable content (sha256sum missing, an unreadable/ambiguous body, or a mailbox
		// above the content-check cap) is a SOFT note (metadata still verified), never a
		// green "content verified" nor a hard fail — the tier policy the default DB/web
		// verify use.
		var mailContentNote string
		fingerprintDiff := false
		if !deep && (mv.kind == vConsistent || mv.kind == vDestAhead) {
			if differ, note := verifyMailContentDigest(ctx, pool, m.Domain, m.User, destDomain, mv.totalCount, mailboxMsgTotal(destF), log, email); differ {
				content = contentDiverged
				contentBadThis = true
				fingerprintDiff = true
			} else {
				mailContentNote = note
			}
		}

		logx.Debug("verify %s: %s (%d folder(s), %d divergent; total msg=%d inbox uid=%s; deep missing=%d changed=%d unverified=%d; fpDiff=%v fpNote=%q)",
			email, mv.label, len(srcF), len(mv.folderDiffs), mv.totalCount, orQ(mv.inboxUV), len(cMissing), len(cChanged), len(cUnverified), fingerprintDiff, mailContentNote)

		// Account this mailbox ONCE, by its rolled-up bucket: a DEST AHEAD mailbox
		// whose bodies are also corrupt counts as CONTENT (a hard diff), not as a
		// benign DEST AHEAD. The presentation branches below only print; they no
		// longer touch the counters.
		bump(classifyMailVerifyImpact(mv.kind, content))

		if mv.kind == vConsistent && content == contentClean {
			switch {
			case mailContentNote != "":
				// Default tier, content could not be byte-verified: honest yellow OK with a
				// note (metadata matched), never a green "content verified".
				rep.LogScreenFile(
					itemStr(log, "~", email, "%s (msg=%d uid=%s; bodies NOT byte-verified — %s)", log.Yellow("OK"), mv.totalCount, orQ(mv.inboxUV), mailContentNote),
					report.VerifyOKLine(email, strconv.Itoa(mv.totalCount), mv.inboxUV))
				rep.FileOnlyf("      NOTE: message bodies were not byte-verified — %s. Per-folder count + UIDVALIDITY matched. Use --deep-verify for per-message content hashes.", mailContentNote)
			case !deep:
				// Default tier, content fingerprint matched: bodies verified at tree level.
				rep.LogScreenFile(
					itemStr(log, "✓", email, "%s (msg=%d uid=%s, content verified)", log.Green("OK"), mv.totalCount, orQ(mv.inboxUV)),
					report.VerifyOKLine(email, strconv.Itoa(mv.totalCount), mv.inboxUV))
			default:
				rep.LogScreenFile(
					itemStr(log, "✓", email, "%s (msg=%d uid=%s)", log.Green("OK"), mv.totalCount, orQ(mv.inboxUV)),
					report.VerifyOKLine(email, strconv.Itoa(mv.totalCount), mv.inboxUV))
			}
			continue
		}

		// Folder counts are consistent but the deep check has something to say. Either
		// the bodies diverge (CONTENT: corruption/replacement the counts cannot see) or
		// they could not be read (UNVERIFIED: the user asked for a body check we could
		// not perform — fail closed, never a clean OK). A mailbox already divergent by
		// folder is handled below, with the content finding added as detail.
		if mv.kind == vConsistent {
			if content == contentUnverified {
				rep.LogScreenFile(
					itemStr(log, "~", email, "%s — could not read message bodies for the deep check (re-run --apply --deep-verify)", log.Red("UNVERIFIED")),
					report.VerifyDiffLine(email, "UNVERIFIED", strconv.Itoa(mv.totalCount), mv.inboxUV, "", "", "deep content check could not read one side"))
				continue
			}
			if fingerprintDiff {
				// DEFAULT tier: the body check found a divergence. It proves the bodies
				// differ (same folder counts) but the default rolls it to one verdict
				// without naming the message(s) — that detail is --deep-verify's job.
				rep.LogScreenFile(
					itemStr(log, "~", email, "%s — message bodies differ at matching folder counts (run --deep-verify to localize the message)", log.Red("CONTENT")),
					report.VerifyDiffLine(email, "CONTENT", strconv.Itoa(mv.totalCount), mv.inboxUV, "", "", "message bodies differ (run --deep-verify to localize the message(s))"))
				continue
			}
			rep.LogScreenFile(
				itemStr(log, "~", email, "%s — %d message(s) corrupted, %d missing (same folder counts)",
					log.Red("CONTENT"), len(cChanged), len(cMissing)),
				report.VerifyDiffLine(email, "CONTENT", strconv.Itoa(mv.totalCount), mv.inboxUV, "", "", "message bodies differ"))
			reportContentDiffs(rep, cMissing, cChanged)
			continue
		}

		// DEST AHEAD at the DEFAULT tier: the dest-only surplus is benign, but the body
		// check still compared the SOURCE-PRESENT messages (CountDigestDivergence ignores
		// the surplus) and they diverge — real loss. Promote to CONTENT rather than the soft
		// DEST AHEAD (already counted hard by classifyMailVerifyImpact). --deep reaches the
		// same finding through its per-message path in the folder-diff block below.
		if mv.kind == vDestAhead && fingerprintDiff {
			rep.LogScreenFile(
				itemStr(log, "~", email, "%s — source-present message bodies differ; the destination also holds extra mail (run --deep-verify to localize)", log.Red("CONTENT")),
				report.VerifyDiffLine(email, "CONTENT", strconv.Itoa(mv.totalCount), mv.inboxUV, "", "", "source-present message bodies differ; destination also holds extra dest-only mail (run --deep-verify to localize)"))
			for _, fd := range mv.folderDiffs {
				rep.FileOnlyf("      %s", fd)
			}
			continue
		}

		// Colour by whether re-running --apply can fix it: yellow = fixable
		// (INCOMPLETE/UIDVALIDITY), red = won't change (DEST AHEAD).
		label := log.Red(mv.label)
		if mv.kind == vIncomplete || mv.kind == vUIDMismatch {
			label = log.Yellow(mv.label)
		}
		brief := strings.Join(firstN(mv.diffNames, 4), ", ")
		note := fmt.Sprintf("%d folder(s) differ: %s", len(mv.folderDiffs), brief)
		rep.LogScreenFile(
			itemStr(log, "~", email, "%s — %s", label, note),
			report.VerifyDiffLine(email, mv.label, strconv.Itoa(mv.totalCount), mv.inboxUV, "", "", note))
		for _, fd := range mv.folderDiffs {
			rep.FileOnlyf("      %s", fd)
		}
		if contentBadThis {
			rep.FileOnlyf("      content: %d message(s) corrupted, %d missing", len(cChanged), len(cMissing))
			reportContentDiffs(rep, cMissing, cChanged)
		} else if content == contentUnverified {
			rep.FileOnlyf("      content: deep check could not read one side (unverified)")
		} else if mailContentNote != "" {
			rep.FileOnlyf("      content: source-present bodies not byte-verified — %s", mailContentNote)
		}
	}

	// realDiff (accumulated in bump) = divergences that mean data is missing/wrong on
	// the destination (a non-zero exit). DEST AHEAD is excluded: extra mail living
	// only on the destination is not data loss and --apply cannot remove it. CONTENT
	// and UNVERIFIED count even under a DEST AHEAD folder verdict (the corrupt/unread
	// bodies are the real loss); a folder-hard mailbox with corrupt bodies counts once.
	// Fold the absent-domain HARD issues into the exit count; the failed-domain and
	// out-of-scope skips are visible but counted elsewhere (or benign).
	realDiff += absentUnverified
	total := realDiff + ahead
	domainSkipped := domainFailedSkip + domainBlockedSkip
	skipped := domainSkipped + noHashSkip
	rep.FileOnlyf("")
	if skipped > 0 {
		rep.FileOnlyf("Integrity check: %d consistent, %d divergent, %d skipped.", ok, total, skipped)
	} else {
		rep.FileOnlyf("Integrity check: %d consistent, %d divergent.", ok, total)
	}
	if total == 0 {
		if skipped == 0 {
			log.OK("integrity check passed: %d mailbox(es) consistent", ok)
		} else {
			// No hard difference, but some mailboxes were skipped — do NOT claim a
			// clean pass; their root cause (a failed/absent domain, or a missing source
			// password hash) is counted elsewhere.
			log.OK("integrity check: %d consistent, %d skipped (counted elsewhere)", ok, skipped)
			reportSkipBreakdown(rep, domainSkipped, noHashSkip)
		}
		return realDiff, nil
	}

	// Report each category with an HONEST recommendation (screen via the logger,
	// file via the reporter — kept in sync, no double-print on screen).
	log.Warn("%d mailbox(es) differ:", total)
	if incomplete > 0 {
		log.Detail("%d INCOMPLETE (a folder is missing messages) — re-run --apply to copy them", incomplete)
		rep.FileOnlyf("  %d INCOMPLETE: a folder is missing messages — re-run --apply to copy them.", incomplete)
	}
	if uidmismatch > 0 {
		log.Detail("%d UIDVALIDITY mismatch — re-run --apply to re-sync", uidmismatch)
		rep.FileOnlyf("  %d UIDVALIDITY mismatch — re-run --apply to re-sync.", uidmismatch)
	}
	if ahead > 0 {
		log.Detail("%d DEST AHEAD (extra mail only on dest) — re-run --apply will NOT change this", ahead)
		rep.FileOnlyf("  %d DEST AHEAD: extra mail exists only on dest (e.g. Trash/INBOX); --apply will NOT remove it.", ahead)
	}
	if contentBad > 0 {
		log.Detail("%d CONTENT (message bodies corrupted/replaced) — re-run --apply --full to re-copy", contentBad)
		rep.FileOnlyf("  %d CONTENT: message bodies differ (corruption/replacement) — re-run --apply --full to re-copy.", contentBad)
	}
	if unverified > 0 {
		log.Detail("%d UNVERIFIED (deep content check could not read message bodies) — re-run --apply --deep-verify", unverified)
		rep.FileOnlyf("  %d UNVERIFIED: the deep content check could not read one side; not certified — re-run --apply --deep-verify.", unverified)
	}
	if unreadable > 0 {
		log.Detail("%d UNREADABLE — could not compare", unreadable)
		rep.FileOnlyf("  %d UNREADABLE: could not read one side.", unreadable)
	}
	if absentUnverified > 0 {
		log.Detail("%d UNVERIFIED (selected destination domain absent after the domain step) — fix the domain and re-run --apply", absentUnverified)
		rep.FileOnlyf("  %d UNVERIFIED: selected destination domain absent after the domain step; not migrated, not verified.", absentUnverified)
	}
	if domainSkipped > 0 {
		log.Detail("%d mailbox(es) SKIPPED (domain issue counted elsewhere)", domainSkipped)
	}
	if noHashSkip > 0 {
		log.Detail("%d mailbox(es) SKIPPED (no source password hash; counted under unverified at apply)", noHashSkip)
	}
	reportSkipBreakdown(rep, domainSkipped, noHashSkip)
	return realDiff, nil
}

// defaultMailContentMsgCap bounds how many messages a mailbox may hold before the
// DEFAULT body fingerprint is skipped (a soft "not byte-verified" note, never a silent
// OK): the fingerprint forks sha256sum per message on both hosts, so an enormous mailbox
// would hash for a very long time on every run. --deep-verify is the opt-in path above
// this. 200k messages is generous for a single account.
const defaultMailContentMsgCap = 200_000

// verifyMailContentDigest is the DEFAULT-tier body gate for one mailbox whose per-folder
// counts already match (vConsistent) or that is DEST AHEAD (the source-present subset is
// compared, dest-only extras ignored). It hashes every message body on both hosts (keyed
// by stable folder-aware identity, so a flag change or a new/->cur/ move is NOT a false
// diff) and rolls the per-message comparison up to a mailbox verdict. differ is true on a
// genuine body divergence (a HARD CONTENT diff). When the content cannot be hashed (the
// mailbox is above the message cap, sha256sum is missing on the host, or a body is
// unreadable/ambiguous) it returns differ=false with a non-empty note: a SOFT "bodies not
// byte-verified" signal (the per-folder metadata is still verified). GetFolderStats already
// ran and hard-failed on a real read/transport error before this point, so a digest fetch
// failure here is a content-layer hiccup (most likely sha256sum missing), reported as the
// soft note rather than failing the default run.
//
// The cap is checked against max(srcCount, destCount): GetMessageDigests hashes EVERY
// message on each host, so a DEST AHEAD mailbox with a small source but a huge destination
// (e.g. a live archive) must be bounded by the dest side too, not just the source count.
func verifyMailContentDigest(ctx context.Context, pool *sshx.Pool, srcDomain, user, destDomain string, srcCount, destCount int, log *logx.Logger, email string) (differ bool, note string) {
	if n := max(srcCount, destCount); n > defaultMailContentMsgCap {
		return false, fmt.Sprintf("mailbox has %d messages (src %d / dest %d), above the default content-check cap (%d)", n, srcCount, destCount, defaultMailContentMsgCap)
	}
	// Hashing every message body on both hosts is the slow part of the DEFAULT verify
	// (the deep path already streams this). Show a live progress row so a big mailbox
	// shows "src/dest N hashed" instead of an idle blinking cursor; the row is cleared
	// (Finish) before the caller prints the verdict. Inert on a non-TTY / --log-level
	// debug run (liveProgress off), exactly like the copy step's bars.
	prog := inlineRow(log, "→", email, 0, "")
	sd, e1 := maildir.GetMessageDigests(ctx, pool.Src, srcDomain, user,
		maildir.WithProgress(func(n int) { prog.SetSuffix(fmt.Sprintf("src %d hashed", n)) }))
	dd, e2 := maildir.GetMessageDigests(ctx, pool.Dest, destDomain, user, maildir.GuardRoot(),
		maildir.WithProgress(func(n int) { prog.SetSuffix(fmt.Sprintf("dest %d hashed", n)) }))
	prog.Finish()
	if e1 != nil || e2 != nil {
		logx.Debug("verify %s@%s: default body check unavailable (srcErr=%v destErr=%v)", user, srcDomain, e1, e2)
		return false, "message bodies could not be hashed (sha256sum unavailable on the host?)"
	}
	// Roll up PER MESSAGE (not a whole-mailbox aggregate): a real body change or lost
	// message among the readable ones is a HARD divergence; a body a side could not hash
	// is only UNVERIFIED. This is what stops one unreadable message from masking a
	// DIFFERENT message's corruption — a corrupted readable message still counts as hard
	// even when another message in the same mailbox is ?unreadable.
	hard, unverified := maildir.CountDigestDivergence(sd, dd)
	if hard > 0 {
		return true, "" // genuine body divergence (corruption or lost mail) -> fail the run
	}
	if unverified > 0 {
		return false, fmt.Sprintf("%d message body(ies) could not be read on a side", unverified)
	}
	return false, ""
}

// reportSkipBreakdown writes the per-root-cause SKIPPED summary lines (file side):
// domain skips (counted under failed/blocked domains) and no-hash skips (counted
// under unverified at apply) are attributed separately so the summary never
// mislabels a missing-source-hash skip as a domain issue.
func reportSkipBreakdown(rep *report.Reporter, domainSkipped, noHashSkip int) {
	if domainSkipped > 0 {
		rep.FileOnlyf("  %d SKIPPED: domain issue counted elsewhere.", domainSkipped)
	}
	if noHashSkip > 0 {
		rep.FileOnlyf("  %d SKIPPED: no source password hash; counted under unverified at apply.", noHashSkip)
	}
}

// reportContentDiffs writes a few example diverging message IDs (missing on dest /
// body changed) to the report, for the --deep-verify mail check.
func reportContentDiffs(rep *report.Reporter, missing, changed []string) {
	for _, id := range changed {
		rep.FileOnlyf("        corrupted: %s", id)
	}
	for _, id := range missing {
		rep.FileOnlyf("        missing:   %s", id)
	}
}

// mailboxVerdict is the rolled-up per-folder verify result for one mailbox: the
// worst folder verdict, the source total message count and INBOX UIDVALIDITY (for
// the headline OK/DIFF line), and the per-folder detail + just-the-names for the
// divergent folders.
type mailboxVerdict struct {
	kind        verifyKind
	label       string
	totalCount  int
	inboxUV     string
	folderDiffs []string // "<folder>: <label> — SRC(...) DEST(...)" per divergent folder
	diffNames   []string // the divergent folder labels, for the brief screen line
}

// mailboxMsgTotal sums the message counts across all of a mailbox's folders. Used to
// bound the default body fingerprint by the LARGER of the two sides (a DEST AHEAD mailbox
// can have far more messages on the destination than the source).
func mailboxMsgTotal(folders map[string]maildir.FolderStats) int {
	n := 0
	for _, f := range folders {
		n += f.Count
	}
	return n
}

// classifyMailbox compares src vs dest folder maps and rolls the per-folder
// verdicts up to one mailbox verdict. It REUSES classifyVerify per folder — each
// folder's (count, UIDVALIDITY) is a BoxStats, and a folder present on only one
// side is the zero value on the other, which classifyVerify already maps correctly
// (a source-only folder with mail -> INCOMPLETE, a dest-only folder with mail ->
// DEST AHEAD, an empty folder either way -> consistent). Pure; unit-tested.
func classifyMailbox(src, dest map[string]maildir.FolderStats) mailboxVerdict {
	mv := mailboxVerdict{kind: vConsistent, label: kindLabel(vConsistent)}
	for _, f := range unionFolders(src, dest) {
		s := maildir.BoxStats{MsgCount: src[f].Count, UIDValidity: src[f].UIDValidity}
		d := maildir.BoxStats{MsgCount: dest[f].Count, UIDValidity: dest[f].UIDValidity}
		mv.totalCount += s.MsgCount
		if f == "INBOX" {
			mv.inboxUV = s.UIDValidity
		}
		v := classifyVerify(s, d, nil, nil)
		if v.kind == vConsistent {
			continue
		}
		mv.diffNames = append(mv.diffNames, f)
		mv.folderDiffs = append(mv.folderDiffs, fmt.Sprintf("%s: %s — SRC(msg=%d uv=%s) DEST(msg=%d uv=%s)",
			f, v.label, s.MsgCount, orQ(s.UIDValidity), d.MsgCount, orQ(d.UIDValidity)))
		if kindSeverity(v.kind) > kindSeverity(mv.kind) {
			mv.kind = v.kind
		}
	}
	mv.label = kindLabel(mv.kind)
	return mv
}

// unionFolders returns the folder labels present in either map, INBOX first then
// the rest sorted, so the verify output is deterministic and INBOX-led.
func unionFolders(src, dest map[string]maildir.FolderStats) []string {
	seen := map[string]bool{}
	hasInbox := false
	var rest []string
	add := func(k string) {
		if seen[k] {
			return
		}
		seen[k] = true
		if k == "INBOX" {
			hasInbox = true
			return
		}
		rest = append(rest, k)
	}
	for k := range src {
		add(k)
	}
	for k := range dest {
		add(k)
	}
	sort.Strings(rest)
	if hasInbox {
		return append([]string{"INBOX"}, rest...)
	}
	return rest
}

// kindSeverity ranks verify kinds so a mailbox rolls up to its worst folder:
// consistent < DEST AHEAD < UIDVALIDITY < INCOMPLETE < UNREADABLE.
func kindSeverity(k verifyKind) int {
	switch k {
	case vDestAhead:
		return 1
	case vUIDMismatch:
		return 2
	case vIncomplete:
		return 3
	case vUnreadable:
		return 4
	default: // vConsistent
		return 0
	}
}

// kindLabel maps a verify kind to its short report tag.
func kindLabel(k verifyKind) string {
	switch k {
	case vIncomplete:
		return "INCOMPLETE"
	case vUIDMismatch:
		return "UIDVALIDITY"
	case vDestAhead:
		return "DEST AHEAD"
	case vUnreadable:
		return "UNREADABLE"
	default:
		return "OK"
	}
}
