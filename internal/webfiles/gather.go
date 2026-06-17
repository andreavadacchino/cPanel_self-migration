package webfiles

import (
	"context"
	"fmt"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// Gather fills SrcBytes + SrcFileCount for every not-yet-skipped plan item by
// reading the SOURCE docroot READ-ONLY (a single-pass find that sums file sizes
// and counts files, applying the same system exclusions the transfer uses) and
// decides skips:
//
//   - docroot absent      -> Note + Skip
//   - docroot unreadable  -> Note + Skip (a permission problem, surfaced distinctly
//     from absent/empty so it is not mistaken for "nothing to migrate")
//   - docroot empty       -> Note + Skip (per project decision: a missing/empty
//     source must NOT cause the destination to be emptied — protects against an
//     anomalous source read wiping destination content)
//
// One RunScript per probed docroot (no progress reporting). For the user-facing
// analyze/verify steps, which want a live per-docroot + per-file bar, the migrate
// layer uses the ONE-SHOT streaming gather instead (gatherStream +
// ParseGatherStream); this per-docroot form remains for the dry-run compare and
// the copy step's plan sizing. Items already skipped by BuildPlan (no destination
// match) are left untouched. The returned slice is a copy; the input is not
// mutated.
func Gather(ctx context.Context, src Runner, items []WebPlanItem) ([]WebPlanItem, error) {
	out := make([]WebPlanItem, len(items))
	copy(out, items)

	for i := range out {
		it := &out[i]
		if it.Skip {
			continue
		}
		bytes, count, ok, unreadable, err := gatherOne(ctx, src, it.SrcDocroot)
		if err != nil {
			return nil, fmt.Errorf("gather %s (%s): %w", it.Domain, it.SrcDocroot, err)
		}
		switch {
		case unreadable:
			// The docroot exists but could not be read (permissions). Fail closed in the
			// report: do NOT fold it into "empty" (which would silently abandon it). Skip
			// here only means the analysis/compare won't size it; the apply path re-reads
			// the source independently.
			it.Skip = true
			it.Notes = append(it.Notes, "source docroot unreadable: "+it.SrcDocroot)
		case !ok:
			it.Skip = true
			it.Notes = append(it.Notes, "source docroot absent: "+it.SrcDocroot)
		case count == 0:
			it.Skip = true
			it.Notes = append(it.Notes, "source docroot empty — destination left untouched")
		default:
			it.SrcBytes = bytes
			it.SrcFileCount = count
		}
	}
	return out, nil
}

// CountBytes runs the read-only aggregate size/count probe for one docroot. It is
// the fallback the manifest verify uses when a docroot exceeds the manifest cap
// (too many entries to hold a full per-path map): the run is still verified, just
// at count+bytes granularity, with a note. ok is true only when the docroot is
// present (a real bytes/count was read); unreadable is true when it exists but is
// not readable/traversable (a permission problem, distinct from absent — the caller
// must NOT treat it as a clean empty/match).
func CountBytes(ctx context.Context, c Runner, docroot string) (bytes int64, count int, ok, unreadable bool, err error) {
	return gatherOne(ctx, c, docroot)
}

// DocrootDigest runs the read-only aggregate probe used by the manifest verify's
// >cap FALLBACK: in one round-trip it returns the docroot's total bytes + file count
// AND a name/size/type(/symlink-target) sha256 digest of exactly the entry set the
// copy mirrors. The fallback compares the two sides' digests (in addition to
// count+bytes) so renames, compensating add+remove, and type/symlink-target changes
// — invisible to count+bytes — are caught above the manifest cap. ok/unreadable have
// the same meaning as CountBytes (present / present-but-unreadable). digest is "" only
// when the root is absent/unreadable or find produced nothing.
func DocrootDigest(ctx context.Context, c Runner, docroot string) (bytes int64, count int, digest string, ok, unreadable bool, err error) {
	out, err := c.RunScript(ctx, digestScript(), map[string]string{"DOCROOT": docroot})
	if err != nil {
		return 0, 0, "", false, false, err
	}
	b, n, dg, status := parseDigestOutput(string(out))
	logx.Debug("webfiles digest %s: bytes=%d files=%d digest=%q status=%d", docroot, b, n, dg, status)
	// Surface the two fail-closed cases that the report also records but that an
	// operator scanning live output would otherwise miss — both block name-verifying
	// an over-cap docroot and neither self-resolves.
	switch {
	case status == sizeUnreadable:
		logx.Warn("webfiles digest %s: docroot present but a subtree is unreadable — the over-cap mirror cannot be name-verified", docroot)
	case status == sizePresent && dg == "":
		logx.Warn("webfiles digest %s: sha256sum / 'sort -z' unavailable on the host — the over-cap docroot cannot be name-verified", docroot)
	}
	return b, n, dg, status == sizePresent, status == sizeUnreadable, nil
}

// DocrootContentDigest runs the read-only aggregate-CONTENT probe used by the DEFAULT
// web verify to prove file-content fidelity in one round-trip and O(1) Go memory: it
// returns the docroot's total bytes + file count AND a single sha256 over every file's
// BODY (plus symlink targets and empty-dir presence) for exactly the copied entry set.
// Two faithful mirrors return the same digest; a same-name/same-size/different-content
// divergence flips it. ok/unreadable mirror CountBytes. digest is "" when the host lacks
// the digest tools (sha256sum / sort -z) — the caller then treats the docroot as
// content-unverified (never a silent OK). See contentDigestScript for the record format
// and the fail-closed semantics.
func DocrootContentDigest(ctx context.Context, c Runner, docroot string) (bytes int64, count int, digest string, ok, unreadable bool, err error) {
	out, err := c.RunScript(ctx, contentDigestScript(), map[string]string{"DOCROOT": docroot})
	if err != nil {
		return 0, 0, "", false, false, err
	}
	b, n, dg, status := parseDigestOutput(string(out))
	logx.Debug("webfiles content-digest %s: bytes=%d files=%d digest=%q status=%d", docroot, b, n, dg, status)
	switch {
	case status == sizeUnreadable:
		logx.Warn("webfiles content-digest %s: docroot present but a subtree is unreadable — the content mirror cannot be verified", docroot)
	case status == sizePresent && dg == "":
		logx.Warn("webfiles content-digest %s: sha256sum / 'sort -z' unavailable on the host — the docroot content cannot be byte-verified", docroot)
	}
	return b, n, dg, status == sizePresent, status == sizeUnreadable, nil
}

// gatherOne runs the read-only single-pass size/count probe for one docroot. ok is
// true only when the docroot is present; unreadable is true when it exists but
// could not be read (permissions). Both false == absent.
func gatherOne(ctx context.Context, c Runner, docroot string) (bytes int64, count int, ok, unreadable bool, err error) {
	out, err := c.RunScript(ctx, gatherScript(), map[string]string{"DOCROOT": docroot})
	if err != nil {
		return 0, 0, false, false, err
	}
	b, n, status := parseSize(string(out))
	logx.Debug("webfiles gather %s: bytes=%d files=%d status=%d", docroot, b, n, status)
	return b, n, status == sizePresent, status == sizeUnreadable, nil
}
