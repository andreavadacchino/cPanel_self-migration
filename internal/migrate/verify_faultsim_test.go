package migrate

import (
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/dbmig"
)

// S2 fault-injection for the verify fail-closed logic (mail + DB). The campaign
// invariant: a check that COULD NOT run must surface as a hard/unverified result,
// NEVER as OK/clean. verify_test.go spot-checks the classifiers; this adds the
// EXHAUSTIVE property (every kind x content combination) and the diffDeepTables
// fail-closed cases (empty/NULL checksum must never pass via ""=="" equality).

// TestFaultSimClassifyMailVerifyImpactOnlySoftWhenClean is the exhaustive property:
// across EVERY (folder verdict x deep-content) combination, the result is non-hard in
// EXACTLY two cases — (consistent, clean)=OK and (destAhead, clean)=soft DEST AHEAD.
// Every other combination (any folder-hard verdict, or any content problem on an
// otherwise-soft mailbox) MUST be hard. This locks the "DEST AHEAD + corrupt body is
// not benign" rule against regression.
func TestFaultSimClassifyMailVerifyImpactOnlySoftWhenClean(t *testing.T) {
	kinds := []verifyKind{vConsistent, vIncomplete, vUIDMismatch, vDestAhead, vUnreadable}
	contents := []deepContent{contentClean, contentDiverged, contentUnverified}
	for _, k := range kinds {
		for _, c := range contents {
			_, hard := classifyMailVerifyImpact(k, c)
			wantSoft := (k == vConsistent && c == contentClean) || (k == vDestAhead && c == contentClean)
			if hard == wantSoft {
				t.Errorf("classifyMailVerifyImpact(kind=%d, content=%d) hard=%v, want hard=%v", k, c, hard, !wantSoft)
			}
		}
	}
}

// TestFaultSimClassifyDeepContentNeverCleanWhenUnprovable: for a requested deep check,
// ANY signal of trouble (digest read error, a missing or changed body, or an
// unreadable body) must yield a non-clean result. Only a fully clean deep read (or
// deep off) is contentClean.
func TestFaultSimClassifyDeepContentNeverCleanWhenUnprovable(t *testing.T) {
	mk := func(b bool) []string {
		if b {
			return []string{"x"}
		}
		return nil
	}
	for _, digestErr := range []bool{false, true} {
		for _, miss := range []bool{false, true} {
			for _, chg := range []bool{false, true} {
				for _, unv := range []bool{false, true} {
					got := classifyDeepContent(true, digestErr, mk(miss), mk(chg), mk(unv))
					trouble := digestErr || miss || chg || unv
					if trouble && got == contentClean {
						t.Errorf("deep classifyDeepContent(digestErr=%v miss=%v chg=%v unv=%v) = clean, want non-clean (fail closed)", digestErr, miss, chg, unv)
					}
					if !trouble && got != contentClean {
						t.Errorf("deep all-clean = %v, want contentClean", got)
					}
				}
			}
		}
	}
	// deep OFF is always clean (no check requested).
	if classifyDeepContent(false, true, []string{"a"}, []string{"b"}, []string{"c"}) != contentClean {
		t.Error("deep off must be contentClean regardless of inputs")
	}
}

// TestFaultSimDiffDeepTablesFailClosed: with checksums ATTEMPTED, a common equal-row
// table whose checksum is empty/NULL on either side must mark ContentUnchecked (never
// pass on ""=="" equality); a row or checksum difference is a HardDiff; and when
// checksums were NOT attempted (cross-version, nil maps) equal rows alone do not
// invent a content proof.
func TestFaultSimDiffDeepTablesFailClosed(t *testing.T) {
	info := func(rows map[string]int64) dbmig.DeepDBInfo {
		d := dbmig.DeepDBInfo{Version: "8.0.0", Tables: map[string]dbmig.DeepTable{}}
		for n, r := range rows {
			d.Tables[n] = dbmig.DeepTable{Rows: r}
		}
		return d
	}
	src := info(map[string]int64{"t1": 10})
	dest := info(map[string]int64{"t1": 10})

	// Empty checksum on the destination side -> ContentUnchecked (fail closed).
	r := diffDeepTables(src, dest, map[string]string{"t1": "abc"}, map[string]string{"t1": ""})
	if !r.ContentUnchecked {
		t.Errorf("equal rows + empty dest checksum: ContentUnchecked=false, want true (fail closed)")
	}
	if r.HardDiff() {
		t.Errorf("an unproven (not differing) checksum must not be a HardDiff: %+v", r)
	}

	// Both empty must NOT pass via ""=="" equality.
	r = diffDeepTables(src, dest, map[string]string{"t1": ""}, map[string]string{"t1": ""})
	if !r.ContentUnchecked {
		t.Errorf(`both checksums empty: ContentUnchecked=false, want true ("" must not equal "")`)
	}

	// Present but differing checksums -> HardDiff (real content difference).
	r = diffDeepTables(src, dest, map[string]string{"t1": "aaa"}, map[string]string{"t1": "bbb"})
	if len(r.ChecksumDiffs) == 0 || !r.HardDiff() {
		t.Errorf("differing checksums must be a HardDiff: %+v", r)
	}

	// Row count difference -> HardDiff regardless of checksums.
	r = diffDeepTables(info(map[string]int64{"t1": 10}), info(map[string]int64{"t1": 9}), nil, nil)
	if len(r.RowDiffs) == 0 || !r.HardDiff() {
		t.Errorf("row diff must be a HardDiff: %+v", r)
	}

	// Missing table on destination -> HardDiff.
	r = diffDeepTables(info(map[string]int64{"t1": 10, "t2": 5}), info(map[string]int64{"t1": 10}), nil, nil)
	if len(r.MissingTables) == 0 || !r.HardDiff() {
		t.Errorf("missing table must be a HardDiff: %+v", r)
	}

	// Checksums NOT attempted (nil maps), equal rows: no invented content proof and no
	// false ContentUnchecked from this path (the cross-version case is handled upstream).
	r = diffDeepTables(src, dest, nil, nil)
	if r.HardDiff() || r.ContentUnchecked {
		t.Errorf("equal rows, no checksums attempted: want clean (no invented proof), got %+v", r)
	}
}
