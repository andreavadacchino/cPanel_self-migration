package migrate

import (
	"context"
	"fmt"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
	"github.com/tis24dev/cPanel_self-migration/internal/webfiles"
)

// applyWebFiles is the web-file copy step: build the plan, gather sizes
// (read-only on the source), then for each not-skipped domain empty the
// destination docroot (guarded) and bridge-copy the source docroot into it.
// Per-domain outcomes go to the shared reporter. Runs after applyDomains so the
// destination docroots exist. It returns the number of docroots that FAILED to
// copy, so runApply can turn lost data into a non-zero exit — mirroring the
// database flow. (Skips, e.g. a failed domain or an empty source, are reported
// but are not counted as failures here.)
func applyWebFiles(ctx context.Context, pool *sshx.Pool, pd migrationData, log *logx.Logger, rep *report.Reporter) (int, error) {
	// Build the plan from the docroots gathered up front (paths only). NO separate
	// preliminary size/find pass here: it would block with a blinking cursor before
	// the copy starts. Instead CopyDocroot does its own read-only listing on each
	// docroot's OWN inline row (shown live), and decides empty/absent inline — it
	// returns without touching the destination when the source has no files.
	items := webPlan(pd)

	log.Step("Copying website files (%d docroot(s)) ...", len(probePairs(items, true)))
	rep.FileOnlyf("")
	rep.FileOnlyf("%s", report.WebHeaderLine())

	actionable := actionableWebCopyItems(pd, items)
	if _, issues := webfiles.ValidateDestTargets(ctx, pool.Dest, actionable); len(issues) > 0 {
		if err := ctx.Err(); err != nil {
			rep.FileOnlyf("INTERRUPTED during web-file destination preflight.")
			return len(issues), err
		}
		issueDomains := make(map[string]bool, len(issues))
		for _, issue := range issues {
			issueDomains[issue.Domain] = true
			reason := issue.Reason
			rep.LogScreenFile(itemStr(log, "✗", issue.Domain, "%s", log.Red("FAIL — "+reason)), report.WebFailLine(issue.Domain, reason))
		}
		typeFailures := reportWebTypeIssueFailures(pd, items, log, rep, issueDomains)
		var blocked int
		for _, it := range actionable {
			if issueDomains[it.Domain] {
				continue
			}
			reason := "not attempted — web-file destination preflight failed"
			rep.LogScreenFile(itemStr(log, "→", it.Domain, "%s", log.Yellow("skip — "+reason)), report.WebSkipLine(it.Domain, reason))
			blocked++
		}
		// Items the normal copy loop would FAIL outright but that actionable filtering
		// dropped (it.Skip / no dest docroot): a destination canonical-domain collision
		// or a destination domain present with no docroot. Without this the early return
		// here omits their FAIL line and under-counts `failed`. Mirror the normal loop's
		// instant-FAIL classification exactly. (domainUnavailable items are reported by
		// their owning domain-creation step; type-blocked ones were handled above.)
		instantFails := reportWebInstantFailures(pd, items, log, rep, issueDomains)
		failed := len(issues) + typeFailures + instantFails
		rep.FileOnlyf("")
		rep.FileOnlyf("Web-file migration summary: 0 copied, %d skipped, %d failed.", blocked, failed)
		log.Warn("web-file destination preflight failed: %d issue(s)", failed)
		return failed, nil
	}

	// A per-batch stall timeout aborts (and retries) an attempt that wedges with no
	// progress.
	transfer := webfiles.Transfer{Src: pool.Src, Dest: pool.Dest, Timeout: sshx.DefaultStallTimeout}

	total := len(items)
	var done, skipped, failed int
	for i, it := range items {
		if ctx.Err() != nil {
			log.Warn("interrupted — %d of %d docroots processed; stopping", i, total)
			rep.FileOnlyf("INTERRUPTED after %d/%d docroots.", i, total)
			return failed, ctx.Err()
		}

		// Skip a docroot whose domain failed creation earlier (instant row).
		if domainFailed(pd, it.Domain) {
			reason := "domain '" + it.Domain + "' creation failed"
			rep.LogScreenFile(itemStr(log, "→", it.Domain, "%s", log.Yellow("skip — "+reason)), report.WebSkipLine(it.Domain, reason))
			skipped++
			continue
		}
		if reason, blocked := domainBlocked(pd, it.Domain); blocked {
			rep.LogScreenFile(itemStr(log, "→", it.Domain, "%s", log.Yellow("skip — "+reason)), report.WebSkipLine(it.Domain, reason))
			skipped++
			continue
		}
		if issue, blocked := domainTypeIssue(pd, it.Domain); blocked && issue.BlockWeb {
			reason := issue.Reason()
			rep.LogScreenFile(itemStr(log, "✗", it.Domain, "%s", log.Red("FAIL — "+reason)), report.WebFailLine(it.Domain, reason))
			failed++
			continue
		}

		// Skip a domain with no destination docroot yet (BuildPlan-skip): nothing
		// to copy into (instant row).
		if it.Skip || it.DestDocroot == "" {
			if hasNote(it.Notes, "canonical domain collision") {
				reason := skipReason(it.Notes)
				rep.LogScreenFile(itemStr(log, "✗", it.Domain, "%s", log.Red("FAIL — "+reason)), report.WebFailLine(it.Domain, reason))
				failed++
				continue
			}
			if destinationDomainMissingDocroot(pd, it.Domain) {
				reason := "destination domain exists but has no destination docroot in DomainInfo::domains_data"
				rep.LogScreenFile(itemStr(log, "✗", it.Domain, "%s", log.Red("FAIL — "+reason)), report.WebFailLine(it.Domain, reason))
				failed++
				continue
			}
			reason := skipReason(it.Notes)
			if reason == "" {
				reason = "no destination docroot"
			}
			rep.LogScreenFile(itemStr(log, "→", it.Domain, "%s", log.Yellow("skip — "+reason)), report.WebSkipLine(it.Domain, reason))
			skipped++
			continue
		}

		// One inline row: action left; on the right a live "N files" counter while
		// CopyDocroot LISTS the source (no muted pre-pass), then a byte % bar while
		// it copies; then Replace turns the row into the result.
		logx.Debug("applyWebFiles %s: starting docroot copy (src=%q dest=%q)", it.Domain, it.SrcDocroot, it.DestDocroot)
		prog := log.NewInlineProgress(itemPrefix(log, "→", it.Domain), 0)
		prog.Draw()
		res, err := transfer.CopyDocroot(ctx, it, prog, func(files int) {
			prog.SetSuffix(fmt.Sprintf("%d files", files))
		})
		if err != nil {
			if stopOnInterruptDuring(ctx, log, rep, it.Domain, "docroots", i, total) {
				prog.Replace(itemStr(log, "→", it.Domain, "%s", log.Yellow("interrupted")))
				return failed, ctx.Err()
			}
			prog.Replace(itemStr(log, "✗", it.Domain, "%s", log.Red("FAIL — "+err.Error())))
			rep.FileOnlyf("%s", report.WebFailLine(it.Domain, err.Error()))
			failed++
			continue
		}
		// A source with no regular files does NOT wipe the destination: existing
		// destination content is backed up aside, OR there was nothing to back up.
		// Turn THIS row into a skip (Replace, so the animated row is consumed) and
		// report what actually happened to the destination.
		logx.Debug("applyWebFiles %s: copy returned %d/%d files (%d bytes); backedUp=%q", it.Domain, res.FilesSent, res.FilesTotal, res.BytesSent, res.BackedUpDir)
		if res.FilesTotal == 0 {
			switch {
			case res.BackedUpDir != "":
				prog.Replace(itemStr(log, "→", it.Domain, "%s", log.Yellow("source empty — backed up existing destination to "+res.BackedUpDir)))
				rep.FileOnlyf("%s", report.WebSkipLine(it.Domain, "source empty — existing destination backed up to "+res.BackedUpDir))
			default:
				prog.Replace(itemStr(log, "→", it.Domain, "%s", log.Yellow("skip — source docroot empty — destination already empty")))
				rep.FileOnlyf("%s", report.WebSkipLine(it.Domain, "source docroot empty — destination already empty"))
			}
			skipped++
			continue
		}
		prog.Replace(itemStr(log, "✓", it.Domain, "%s — %d/%d files copied (%s)", log.Green("copied"), res.FilesSent, res.FilesTotal, report.HumanBytes(res.BytesSent)))
		rep.FileOnlyf("%s", report.WebOKLine(it.Domain, res.FilesSent, res.FilesTotal, res.BytesSent))
		done++
	}

	rep.FileOnlyf("")
	rep.FileOnlyf("Web-file migration summary: %d copied, %d skipped, %d failed.", done, skipped, failed)
	log.OK("web-file step done: %d copied, %d skipped, %d failed", done, skipped, failed)
	return failed, nil
}

func reportWebTypeIssueFailures(pd migrationData, items []webfiles.WebPlanItem, log *logx.Logger, rep *report.Reporter, already map[string]bool) int {
	var failed int
	for _, it := range items {
		if already[it.Domain] {
			continue
		}
		issue, blocked := domainTypeIssue(pd, it.Domain)
		if !blocked || !issue.BlockWeb {
			continue
		}
		reason := issue.Reason()
		rep.LogScreenFile(itemStr(log, "✗", it.Domain, "%s", log.Red("FAIL — "+reason)), report.WebFailLine(it.Domain, reason))
		already[it.Domain] = true
		failed++
	}
	return failed
}

// reportWebInstantFailures FAILs the items the preflight-failed early return would
// otherwise drop silently: a non-actionable docroot (it.Skip / no dest docroot) that
// the normal copy loop would have FAILed for a destination canonical-domain collision
// or a present-but-docroot-less destination domain. `already` holds the domains already
// reported (preflight issues + type failures), which are skipped. domainUnavailable
// items are reported by their owning step, not here. Returns the count it FAILed.
func reportWebInstantFailures(pd migrationData, items []webfiles.WebPlanItem, log *logx.Logger, rep *report.Reporter, already map[string]bool) int {
	var failed int
	for _, it := range items {
		if already[it.Domain] || domainUnavailable(pd, it.Domain) {
			continue
		}
		if !it.Skip && it.DestDocroot != "" {
			continue // actionable item — handled by the blocked/not-attempted loop
		}
		var reason string
		switch {
		case hasNote(it.Notes, "canonical domain collision"):
			reason = skipReason(it.Notes)
		case destinationDomainMissingDocroot(pd, it.Domain):
			reason = "destination domain exists but has no destination docroot in DomainInfo::domains_data"
		default:
			continue // a benign no-destination skip, not a failure
		}
		rep.LogScreenFile(itemStr(log, "✗", it.Domain, "%s", log.Red("FAIL — "+reason)), report.WebFailLine(it.Domain, reason))
		failed++
	}
	return failed
}

func actionableWebCopyItems(pd migrationData, items []webfiles.WebPlanItem) []webfiles.WebPlanItem {
	out := make([]webfiles.WebPlanItem, 0, len(items))
	for _, it := range items {
		if domainUnavailable(pd, it.Domain) || it.Skip || it.DestDocroot == "" {
			continue
		}
		if issue, blocked := domainTypeIssue(pd, it.Domain); blocked && issue.BlockWeb {
			continue
		}
		out = append(out, it)
	}
	return out
}

// webVerifyManifestCap bounds the per-path manifest the web verify builds on each side;
// 0 means webfiles.DefaultManifestCap (the production value). It exists as a package var
// only so a test can lower it to force the >cap fallback branch (verifyWebFallback plus the
// over-cap content fingerprint) on a small fixture instead of a 400k-entry one.
var webVerifyManifestCap = 0

// verifyWebFiles re-reads each copied docroot on BOTH sides (read-only) and
// compares them by a per-path MANIFEST — every relpath with its type, size,
// symlink target, and mode — instead of only the aggregate file count + bytes.
// The aggregate check passed whenever the two totals happened to match, so a
// dropped file masked by an extra one, a wrong permission, a rewritten symlink, or
// a type change all slipped through; the manifest names the exact diverging paths.
//
// It returns the number of docroots with a HARD divergence (missing/extra/size/
// type/symlink-target/content — the destination is not a faithful mirror) so
// runApply can fail the run. A mode-only drift is reported but not counted (a
// permission bit is not data loss). A docroot too large for a full manifest falls
// back to the aggregate count+bytes check, with a note. When deep is set
// (--deep-verify), files are compared by sha256 content hash (bounded by a per-
// docroot size cap, above which it falls back to metadata). The source is only read.
func verifyWebFiles(ctx context.Context, pool *sshx.Pool, pd migrationData, log *logx.Logger, rep *report.Reporter, deep bool) (int, error) {
	items := webPlan(pd)

	mode := "manifest: paths + size + type + mode + tree content fingerprint"
	if deep {
		mode = "deep: per-file sha256 content hash"
	}
	log.Step("Verifying website files (%s) ...", mode)
	rep.FileOnlyf("")
	rep.FileOnlyf("=== --apply: verifying web files (%s) ===", mode)

	var ok, diff int
	for _, it := range items {
		if ctx.Err() != nil {
			log.Warn("interrupted — web verification stopped")
			return diff, ctx.Err()
		}
		// Verify only docroots the copy step actually targeted. Mirror the copy gate
		// (actionableWebCopyItems): a no-dest-match/BuildPlan-skipped item, and a domain
		// whose creation FAILED or was BLOCKED, were never copied — verifying them would
		// re-read an uncopied (often absent) destination and report a bogus web diff.
		if it.Skip || it.DestDocroot == "" || domainUnavailable(pd, it.Domain) {
			continue
		}
		if issue, blocked := domainTypeIssue(pd, it.Domain); blocked && issue.BlockWeb {
			continue
		}

		// Deep mode reads every byte on both hosts; for an enormous docroot fall back
		// to the metadata manifest (still per-path) and say so, rather than hash for
		// an unbounded time.
		useDeep := deep
		if deep {
			if b, _, okb, _, berr := webfiles.CountBytes(ctx, pool.Src, it.SrcDocroot); berr == nil && okb && b > webfiles.DeepByteCap {
				useDeep = false
				rep.FileOnlyf("      DEEP-SKIPPED %s: %s exceeds the deep-verify cap (%s) — verified by metadata",
					it.Domain, report.HumanBytes(b), report.HumanBytes(webfiles.DeepByteCap))
			}
		}

		// Unit "" (not "entries") so the row renders as bar-only with the count in the
		// suffix ("src/dest N entries", then "N hashed" in deep mode): the suffix already
		// labels the phase AND the count, so a unit here would add a phantom "0 entries"
		// from the empty counter.
		prog := inlineRow(log, "→", it.Domain, 0, "")
		srcMan, srcAbsent, srcUnreadable, srcTrunc, srcDropped, serr := webfiles.GetManifest(ctx, pool.Src, it.SrcDocroot, webVerifyManifestCap, useDeep,
			func(hashing bool, n int) {
				if hashing {
					prog.SetSuffix(fmt.Sprintf("src %d hashed", n))
				} else {
					prog.SetSuffix(fmt.Sprintf("src %d entries", n))
				}
			})
		if serr != nil {
			prog.Replace(itemStr(log, "~", it.Domain, "%s — source manifest: %v", log.Red("UNREADABLE"), serr))
			rep.FileOnlyf("%s", report.WebManifestDiffLine(it.Domain, "UNREADABLE — source manifest error"))
			diff++
			continue
		}
		// A source docroot that is PRESENT but unreadable is NOT empty/absent: its
		// content could not be read, so it must not verify clean (the empty/absent skip
		// below would otherwise swallow it). Surface UNREADABLE and fail the run.
		if srcUnreadable {
			prog.Replace(itemStr(log, "~", it.Domain, "%s — source docroot present but unreadable", log.Red("UNREADABLE")))
			rep.FileOnlyf("%s", report.WebManifestDiffLine(it.Domain, "UNREADABLE — source docroot present but not readable; mirror could not be verified"))
			diff++
			continue
		}
		// A SOURCE path the parser DROPPED as unsafe (a tab, a traversal "..", or a
		// control byte in the filename) is silently absent from this manifest — and the
		// copy step skipped the same unrepresentable entry, so DiffManifests would see no
		// Missing/Extra and pass the docroot as a clean OK. That is a FALSE-OK: a real
		// source path went unverified. Mark the docroot UNVERIFIED and fail the run so the
		// operator inspects it. Checked BEFORE the empty/absent skip (so a source that is
		// empty-OF-SAFE-entries but had unsafe ones is not swallowed as a benign skip —
		// srcAbsent means NODIR, which parses no records, so dropped is always 0 there)
		// and BEFORE the truncation fallback (a known-incomplete evidence set outranks the
		// coarser count+bytes check, which does not apply the parser's safety guard).
		if srcDropped > 0 {
			prog.Replace(itemStr(log, "~", it.Domain, "%s — %d source path(s) have unsupported names (tab/control-byte/traversal); cannot verify", log.Red("UNVERIFIED"), srcDropped))
			rep.FileOnlyf("%s", report.WebManifestDiffLine(it.Domain, fmt.Sprintf("UNVERIFIED — %d source path(s) dropped as unsupported (tab/control-byte/traversal in the filename); the mirror could not be verified for them. Re-running will not help — rename the source path(s). See --log-level debug for each.", srcDropped)))
			diff++
			continue
		}
		// A source docroot that is absent or empty was, by the copy's rule, NOT
		// mirrored onto the destination (the destination is preserved), so there is
		// nothing to verify — skip it exactly as the copy did, rather than flag the
		// preserved destination content as spurious "extra".
		if srcAbsent || (len(srcMan) == 0 && !srcTrunc) {
			prog.Replace(itemStr(log, "→", it.Domain, "%s", log.Yellow("skip — source empty/absent (destination left untouched)")))
			continue
		}

		// Dest-side dropped paths are pre-existing junk on the destination, not source
		// data at risk, so they are not failed here (only the source is authoritative).
		destMan, _, destUnreadable, destTrunc, destDropped, derr := webfiles.GetManifest(ctx, pool.Dest, it.DestDocroot, webVerifyManifestCap, useDeep,
			func(hashing bool, n int) {
				if hashing {
					prog.SetSuffix(fmt.Sprintf("dest %d hashed", n))
				} else {
					prog.SetSuffix(fmt.Sprintf("dest %d entries", n))
				}
			})
		if derr != nil {
			prog.Replace(itemStr(log, "~", it.Domain, "%s — destination manifest: %v", log.Red("UNREADABLE"), derr))
			rep.FileOnlyf("%s", report.WebManifestDiffLine(it.Domain, "UNREADABLE — destination manifest error"))
			diff++
			continue
		}
		// A present-but-unreadable DESTINATION docroot would otherwise read as an empty
		// manifest, making every source file look "missing" (a misleading DIFF). Report
		// it honestly as UNREADABLE instead.
		if destUnreadable {
			prog.Replace(itemStr(log, "~", it.Domain, "%s — destination docroot present but unreadable", log.Red("UNREADABLE")))
			rep.FileOnlyf("%s", report.WebManifestDiffLine(it.Domain, "UNREADABLE — destination docroot present but not readable; mirror could not be verified"))
			diff++
			continue
		}

		// Pathologically large docroot: fall back to the aggregate count+bytes+namelist
		// digest check (still verified, just coarser than the per-path manifest) with a
		// note instead of holding a huge map. The namelist fallback runs FIRST as the cheap
		// structural gate: a count/bytes/name/size/type/symlink-target divergence (including
		// dest-only junk, which the source can never carry here — srcDropped>0 already failed
		// the run above) short-circuits with its specific explanation and skips the expensive
		// body hash.
		if srcTrunc || destTrunc {
			divergent, unverified, srcBytes, ferr := verifyWebFallback(ctx, pool, it, rep)
			switch {
			case ferr != nil:
				prog.Replace(itemStr(log, "~", it.Domain, "%s — %v", log.Red("UNREADABLE"), ferr))
				diff++
				continue
			case unverified:
				prog.Replace(itemStr(log, "~", it.Domain, "%s — namelist digest unavailable on host (manifest > cap; see report)", log.Red("UNVERIFIED")))
				diff++
				continue
			case divergent:
				prog.Replace(itemStr(log, "~", it.Domain, "%s — count/bytes/namelist differ (manifest > cap; see report)", log.Red("DIFF")))
				diff++
				continue
			}
			// Structure (count+bytes+name/size/type/symlink-target) verified clean above the
			// manifest cap — but, exactly like the per-path metadata manifest under the cap,
			// the namelist digest never read a file BODY, so a same-name/same-size content
			// corruption is still invisible (finding V02). Fold in the streaming tree CONTENT
			// fingerprint (the same DocrootContentDigest the under-cap default path uses,
			// O(1) Go memory, works over the cap) and FAIL on a body mismatch. Over the cap
			// there is no per-path map to hash, so this aggregate fingerprint is the strongest
			// content check available in BOTH tiers — hence tier-independent (deep does not
			// change it). Tier policy mirrors the under-cap gate and the default DB verify: a
			// provable mismatch is a HARD DIFF; content that cannot be hashed (docroot over the
			// byte cap, or a side unreadable/absent at hash time) is a SOFT "content NOT
			// byte-verified" note — metadata still verified — never a green content-OK and
			// never a hard fail.
			differ, contentNote, cerr := verifyWebContentDigest(ctx, pool, it, srcBytes)
			switch {
			case cerr != nil:
				prog.Replace(itemStr(log, "~", it.Domain, "%s — content fingerprint: %v", log.Red("UNREADABLE"), cerr))
				rep.FileOnlyf("%s", report.WebManifestDiffLine(it.Domain, fmt.Sprintf("UNREADABLE — over-cap content fingerprint error: %v", cerr)))
				diff++
			case differ:
				prog.Replace(itemStr(log, "~", it.Domain, "%s — file content differs at matching count/bytes/names (manifest > cap; run --deep-verify on an under-cap docroot to localize)", log.Red("DIFF")))
				rep.FileOnlyf("%s", report.WebManifestDiffLine(it.Domain, "DIFF — file CONTENT differs above the manifest cap: count+bytes+name/size/type/symlink-target all matched but the tree content fingerprint diverges. The destination is NOT a faithful mirror. Re-run --apply --file to re-copy."))
				diff++
			case contentNote != "":
				prog.Replace(itemStr(log, "~", it.Domain, "%s (count+bytes+namelist; manifest > cap; content NOT byte-verified — %s)", log.Yellow("OK"), contentNote))
				rep.FileOnlyf("      NOTE: file CONTENT was not byte-verified above the manifest cap — %s. Count+bytes+name/size/type/symlink-target matched. Use --deep-verify on an under-cap docroot for content hashes.", contentNote)
				ok++
			default:
				prog.Replace(itemStr(log, "✓", it.Domain, "%s (count+bytes+namelist + content fingerprint; manifest > cap)", log.Green("OK")))
				rep.FileOnlyf("      NOTE: file CONTENT byte-verified above the manifest cap by a streaming tree fingerprint (every file body + symlink target hashed).")
				ok++
			}
			continue
		}

		md := webfiles.DiffManifests(srcMan, destMan)
		logx.Debug("verify web %s: manifest src=%d dest=%d -> missing=%d(sym=%d) extra=%d size=%d type=%d link=%d content=%d cunv=%d mode=%d",
			it.Domain, len(srcMan), len(destMan), md.Missing, md.MissingSymlinks, md.Extra, md.SizeDiff, md.TypeDiff, md.LinkDiff, md.ContentDiff, md.ContentUnverified, md.ModeDiff)

		// DEFAULT-tier content gate: the metadata manifest matched the structure
		// (paths/size/type/symlink-target — no HARD diff) but it never read a single file
		// BODY, so a same-name/same-size content corruption is still invisible. Compare a
		// streaming sha256 fingerprint of the whole tree on both hosts and FAIL on a
		// mismatch. Only at default and only when no hard metadata diff already condemns
		// the docroot: --deep already hashes every file per-path via the H records
		// (classifyContent), and a hard diff is reported regardless of content. When the
		// content cannot be hashed (tools unavailable, or the docroot exceeds the content
		// cap) it is a SOFT note — metadata still verified — not a green "content verified"
		// and not a hard fail (the same tier policy the default DB verify uses).
		var contentNote string
		if !deep && md.Hard() == 0 && destDropped > 0 {
			// The metadata manifest dropped one or more DESTINATION paths as unsafe-named
			// (tab/control-byte) — pre-existing junk the verify intentionally ignores
			// (only the source is authoritative). The streaming content fingerprint, by
			// contrast, hashes the raw `find` output and WOULD include that junk, diverging
			// from the source for a benign reason. The source is guaranteed clean here
			// (srcDropped>0 already continued above), so skip the content hash and note it
			// rather than raise a false content DIFF.
			contentNote = fmt.Sprintf("%d destination path(s) have unsupported names (tab/control-byte) — content fingerprint skipped to avoid a false mismatch", destDropped)
		} else if !deep && md.Hard() == 0 {
			var srcBytes int64
			for _, e := range srcMan {
				srcBytes += e.Size
			}
			differ, note, cerr := verifyWebContentDigest(ctx, pool, it, srcBytes)
			switch {
			case cerr != nil:
				prog.Replace(itemStr(log, "~", it.Domain, "%s — content fingerprint: %v", log.Red("UNREADABLE"), cerr))
				rep.FileOnlyf("%s", report.WebManifestDiffLine(it.Domain, fmt.Sprintf("UNREADABLE — content fingerprint error: %v", cerr)))
				diff++
				continue
			case differ:
				prog.Replace(itemStr(log, "~", it.Domain, "%s — file content differs at matching path/size/type (run --deep-verify to localize the file)", log.Red("DIFF")))
				rep.FileOnlyf("%s", report.WebManifestDiffLine(it.Domain, "DIFF — file CONTENT differs at equal path/size/type/symlink-target (tree content fingerprint mismatch). Run --deep-verify to localize the diverging file(s)."))
				diff++
				continue
			default:
				contentNote = note // "" when content was verified equal; a reason when it could not be hashed
			}
		}

		if md.OK() {
			switch {
			case contentNote != "":
				prog.Replace(itemStr(log, "~", it.Domain, "%s (manifest=%d entries; content NOT byte-verified — %s)", log.Yellow("OK"), len(srcMan), contentNote))
				rep.FileOnlyf("%s", report.WebManifestOKLine(it.Domain, len(srcMan)))
				rep.FileOnlyf("      NOTE: file CONTENT was not byte-verified — %s. Path/size/type/mode/symlink-target matched. Use --deep-verify for per-file content hashes.", contentNote)
			case !deep:
				prog.Replace(itemStr(log, "✓", it.Domain, "%s (manifest=%d entries, content verified)", log.Green("OK"), len(srcMan)))
				rep.FileOnlyf("%s", report.WebManifestOKLine(it.Domain, len(srcMan)))
			default:
				prog.Replace(itemStr(log, "✓", it.Domain, "%s (manifest=%d entries)", log.Green("OK"), len(srcMan)))
				rep.FileOnlyf("%s", report.WebManifestOKLine(it.Domain, len(srcMan)))
			}
			ok++
			continue
		}
		summary := webManifestSummary(md)
		// A hard divergence (data missing/wrong) is red and fails the run; a mode-only
		// drift is yellow and reported but not counted (a permission bit is not loss).
		label := log.Yellow("MODE-DIFF")
		if md.Hard() > 0 {
			label = log.Red("DIFF")
			diff++
		}
		prog.Replace(itemStr(log, "~", it.Domain, "%s — %s", label, summary))
		rep.FileOnlyf("%s", report.WebManifestDiffLine(it.Domain, summary))
		for _, ex := range md.Examples {
			rep.FileOnlyf("      %s", ex)
		}
	}

	rep.FileOnlyf("")
	rep.FileOnlyf("Web-file integrity check: %d consistent, %d divergent.", ok, diff)
	if diff == 0 {
		log.OK("web-file integrity check passed: %d docroot(s) consistent", ok)
	} else {
		log.Warn("%d web docroot(s) differ — re-run --apply --file to re-copy", diff)
		rep.FileOnlyf("  re-run --apply --file to re-copy the divergent docroots.")
	}
	return diff, nil
}

// webManifestSummary renders a ManifestDiff as a compact one-line summary naming
// each category (e.g. "3 missing (2 symlink), 1 size, 2 mode"). Pure.
func webManifestSummary(d webfiles.ManifestDiff) string {
	var parts []string
	if d.Missing > 0 {
		if d.MissingSymlinks > 0 {
			parts = append(parts, fmt.Sprintf("%d missing (%d symlink)", d.Missing, d.MissingSymlinks))
		} else {
			parts = append(parts, fmt.Sprintf("%d missing", d.Missing))
		}
	}
	if d.Extra > 0 {
		parts = append(parts, fmt.Sprintf("%d extra-on-dest", d.Extra))
	}
	if d.SizeDiff > 0 {
		parts = append(parts, fmt.Sprintf("%d size", d.SizeDiff))
	}
	if d.ContentDiff > 0 {
		parts = append(parts, fmt.Sprintf("%d content", d.ContentDiff))
	}
	if d.ContentUnverified > 0 {
		parts = append(parts, fmt.Sprintf("%d content-unverified", d.ContentUnverified))
	}
	if d.TypeDiff > 0 {
		parts = append(parts, fmt.Sprintf("%d type", d.TypeDiff))
	}
	if d.LinkDiff > 0 {
		parts = append(parts, fmt.Sprintf("%d symlink-target", d.LinkDiff))
	}
	if d.ModeDiff > 0 {
		parts = append(parts, fmt.Sprintf("%d mode", d.ModeDiff))
	}
	return strings.Join(parts, ", ")
}

// verifyWebContentDigest is the DEFAULT-tier content gate for an under-cap docroot. It
// streams a sha256 fingerprint of every file BODY (plus symlink targets and empty-dir
// presence) on both hosts and compares them, catching the same-name/same-size content
// corruption a metadata-only manifest cannot see. differ is true on a content mismatch
// (a HARD divergence that fails the run). When the content could NOT be hashed — the
// docroot exceeds the content-hash cap, a side is absent/unreadable at hash time, or the
// host lacks sha256sum / 'sort -z' — it returns differ=false with a non-empty note: a
// SOFT "content not byte-verified" signal (the metadata is still verified), mirroring the
// default DB verify's content-unchecked tier rather than failing the run or printing a
// false "content verified". err is only a genuine SSH round-trip failure.
func verifyWebContentDigest(ctx context.Context, pool *sshx.Pool, it webfiles.WebPlanItem, srcBytes int64) (differ bool, note string, err error) {
	// Bound the default-tier work: hashing every byte of an enormous docroot would run
	// for a very long time, so above deep-verify's byte cap we do not hash at all and
	// report the content as not byte-verified (soft) instead of holding the run.
	if srcBytes > webfiles.DeepByteCap {
		return false, fmt.Sprintf("docroot (%s) exceeds the content-hash cap (%s)", report.HumanBytes(srcBytes), report.HumanBytes(webfiles.DeepByteCap)), nil
	}
	_, _, sdg, sok, sunread, serr := webfiles.DocrootContentDigest(ctx, pool.Src, it.SrcDocroot)
	if serr != nil {
		return false, "", serr
	}
	_, _, ddg, dok, dunread, derr := webfiles.DocrootContentDigest(ctx, pool.Dest, it.DestDocroot)
	if derr != nil {
		return false, "", derr
	}
	// Any side unreadable or absent at hash time (a race vs the manifest pass, or a
	// permission change) cannot certify content. Report content-unverified (soft), never
	// a spurious DIFF nor a green "content verified" — the metadata manifest verified
	// clean and the structure is unchanged.
	if sunread || dunread {
		return false, "a docroot side became unreadable during content hashing", nil
	}
	if !sok || !dok {
		return false, "a docroot side was not present during content hashing", nil
	}
	// A present side with an empty digest means the host lacks sha256sum / 'sort -z' —
	// content cannot be hashed. A present EMPTY tree hashes the non-empty empty-input
	// sha256, so digest=="" on a present docroot uniquely means tools-unavailable.
	if sdg == "" || ddg == "" {
		return false, "sha256sum / sort -z unavailable on the host", nil
	}
	return sdg != ddg, "", nil
}

// verifyWebFallback verifies one docroot by an aggregate probe, used only when a
// docroot exceeds the manifest cap (too many entries for a per-path map). It compares
// count+bytes AND a name/size/type(/symlink-target) sha256 digest of exactly the
// copied entry set, so divergences invisible to count+bytes — renames, a compensating
// add+remove of equal size, type changes, and symlink retargets — are caught above the
// cap. (Same-name/same-size/different-content is not caught here; that needs
// --deep-verify on an under-cap docroot — which the caller now layers on top of a clean
// match via the over-cap content fingerprint.) divergent is true on any mismatch; it also
// tees the verify + note lines to the report. An unreadable side never certifies clean.
// srcBytes is the source docroot's full (un-truncated) byte total from DocrootDigest,
// returned so the caller can gate the over-cap CONTENT fingerprint against the content-hash
// byte cap without a redundant probe; it is meaningful only on a clean match (the sole path
// on which the caller hashes content), 0 otherwise.
func verifyWebFallback(ctx context.Context, pool *sshx.Pool, it webfiles.WebPlanItem, rep *report.Reporter) (divergent, unverified bool, srcBytes int64, err error) {
	sb, sc, sfp, sok, sunread, serr := webfiles.DocrootDigest(ctx, pool.Src, it.SrcDocroot)
	if serr != nil {
		return false, false, 0, serr
	}
	db, dc, dfp, dok, dunread, derr := webfiles.DocrootDigest(ctx, pool.Dest, it.DestDocroot)
	if derr != nil {
		return false, false, 0, derr
	}
	// A present docroot with an empty digest means the host lacks the digest tools
	// (sha256sum / sort -z) — we CANNOT certify the mirror at name granularity. Treat it
	// as UNVERIFIED (fail closed), never a silent count+bytes-only OK (the #4 hole) nor a
	// spurious DIFF. (A present EMPTY tree digests to the non-empty empty-input sha256, so
	// this only triggers on genuinely-unavailable tools.)
	if (sok && sfp == "") || (dok && dfp == "") {
		rep.FileOnlyf("%s", report.WebManifestDiffLine(it.Domain, fmt.Sprintf("UNVERIFIED — namelist digest unavailable (sha256sum / sort -z missing on the host); docroot exceeds the manifest cap (%d entries) so names/types could not be verified", webfiles.DefaultManifestCap)))
		return false, true, 0, nil
	}
	// An unreadable side (a subtree we could not read) cannot certify the mirror at name
	// granularity either. Folding sunread/dunread into the match below would render it as
	// a generic DIFF, hiding the "could not read" cause; surface it as UNVERIFIED (fail
	// closed), like the missing-tools case above, before the match calc.
	if sunread || dunread {
		rep.FileOnlyf("%s", report.WebManifestDiffLine(it.Domain, fmt.Sprintf("UNVERIFIED — could not read one side fully (docroot exceeds the manifest cap of %d entries); names/types not certified", webfiles.DefaultManifestCap)))
		return false, true, 0, nil
	}
	// A clean match requires both sides fully readable AND equal count+bytes AND equal
	// digests. The digest subsumes count+bytes for files/symlinks and adds
	// renames/type/symlink-target/empty-dir coverage; count+bytes stay for the
	// human-readable shortfall numbers in the report.
	match := sok && dok && !sunread && !dunread && sc == dc && sb == db && sfp == dfp
	rep.FileOnlyf("%s", report.WebVerifyLine(it.Domain, match, sc, dc, sb, db))
	rep.FileOnlyf("      NOTE: docroot exceeded the manifest cap (%d entries) — verified by count+bytes+namelist digest (names/sizes/types/symlink-targets).", webfiles.DefaultManifestCap)
	if !match && sok && dok && sc == dc && sb == db {
		// Counts and bytes are equal but the digests differ — surface the otherwise-
		// puzzling "DIFF at equal count+bytes" (a rename, an equal-size add/remove, or
		// a type/symlink-target change).
		rep.FileOnlyf("      namelist digest differs (src=%s dest=%s) — rename, equal-size add/remove, or type/symlink-target change", shortDigest(sfp), shortDigest(dfp))
	}
	return !match, false, sb, nil
}

// shortDigest renders a sha256 hex prefix for the report (or "none" if absent).
func shortDigest(d string) string {
	if d == "" {
		return "none"
	}
	if len(d) > 12 {
		return d[:12]
	}
	return d
}
