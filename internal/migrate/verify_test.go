package migrate

import (
	"errors"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/maildir"
)

func bs(n int, uid string) maildir.BoxStats {
	return maildir.BoxStats{MsgCount: n, UIDValidity: uid}
}

func TestClassifyVerify(t *testing.T) {
	cases := []struct {
		name      string
		s, d      maildir.BoxStats
		se, de    error
		wantKind  verifyKind
		wantLabel string
	}{
		{"consistent", bs(505, "V1"), bs(505, "V1"), nil, nil, vConsistent, "OK"},
		{"dest missing (incomplete)", bs(505, "V1"), bs(500, "V1"), nil, nil, vIncomplete, "INCOMPLETE"},
		{"dest ahead", bs(3781, "V1"), bs(3833, "V1"), nil, nil, vDestAhead, "DEST AHEAD"},
		{"uidvalidity mismatch", bs(505, "V1"), bs(505, "V2"), nil, nil, vUIDMismatch, "UIDVALIDITY"},
		{"src unreadable", bs(0, ""), bs(505, "V1"), errors.New("x"), nil, vUnreadable, "UNREADABLE"},
		{"dest unreadable", bs(505, "V1"), bs(0, ""), nil, errors.New("x"), vUnreadable, "UNREADABLE"},
		// Empty on both sides is consistent (nothing to migrate); a UIDVALIDITY
		// difference is irrelevant with zero messages, so it must NOT be flagged
		// (regression: an empty box previously read as a false "DEST AHEAD" by 0).
		{"both empty, same uid", bs(0, "V1"), bs(0, "V1"), nil, nil, vConsistent, "OK"},
		{"both empty, no uid", bs(0, ""), bs(0, ""), nil, nil, vConsistent, "OK"},
		{"both empty, diff uid", bs(0, ""), bs(0, "V9"), nil, nil, vConsistent, "OK"},
		// A non-empty box whose UIDVALIDITY is genuinely ABSENT on both sides (no
		// dovecot-uidlist — a legitimate lazy-Dovecot state) but whose counts match is
		// NOT flagged here: the count (and, under --deep-verify, the bodies) certify the
		// mail is present. The dangerous "UID could not be READ" case is caught upstream
		// in the helper (unreadable uidlist) / strict parser (malformed uid), not here.
		{"equal non-empty, both no uid (absent uidlist) is OK", bs(5, ""), bs(5, ""), nil, nil, vConsistent, "OK"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := classifyVerify(c.s, c.d, c.se, c.de)
			if v.kind != c.wantKind {
				t.Errorf("kind = %v, want %v", v.kind, c.wantKind)
			}
			if v.label != c.wantLabel {
				t.Errorf("label = %q, want %q", v.label, c.wantLabel)
			}
		})
	}
}

// fs is a tiny FolderStats constructor for the classifyMailbox tests.
func fs(n int, uid string) maildir.FolderStats {
	return maildir.FolderStats{Count: n, UIDValidity: uid}
}

// TestClassifyMailboxNetZero is the headline per-folder regression: an aggregate
// whole-mailbox count would read 20==20 and pass, but the messages are wrong PER
// FOLDER (INBOX +5, .Archive -5). The mailbox must roll up to INCOMPLETE (the
// worst folder), naming both divergent folders.
func TestClassifyMailboxNetZero(t *testing.T) {
	src := map[string]maildir.FolderStats{"INBOX": fs(10, "V1"), ".Archive": fs(10, "V1")}
	dest := map[string]maildir.FolderStats{"INBOX": fs(15, "V1"), ".Archive": fs(5, "V1")}
	mv := classifyMailbox(src, dest)
	if mv.kind != vIncomplete {
		t.Errorf("net-zero mailbox must roll up to INCOMPLETE, got %s", mv.label)
	}
	if mv.totalCount != 20 {
		t.Errorf("totalCount = %d, want 20 (sum of source folders)", mv.totalCount)
	}
	if len(mv.folderDiffs) != 2 {
		t.Errorf("both folders must be flagged, got %v", mv.diffNames)
	}
}

func TestClassifyMailboxCases(t *testing.T) {
	cases := []struct {
		name     string
		src, dst map[string]maildir.FolderStats
		want     verifyKind
	}{
		{"all consistent",
			map[string]maildir.FolderStats{"INBOX": fs(5, "V1"), ".Sent": fs(2, "V2")},
			map[string]maildir.FolderStats{"INBOX": fs(5, "V1"), ".Sent": fs(2, "V2")},
			vConsistent},
		{"dest-only Trash is benign DEST AHEAD",
			map[string]maildir.FolderStats{"INBOX": fs(5, "V1")},
			map[string]maildir.FolderStats{"INBOX": fs(5, "V1"), ".Trash": fs(9, "V3")},
			vDestAhead},
		{"source-only folder missing on dest is INCOMPLETE",
			map[string]maildir.FolderStats{"INBOX": fs(5, "V1"), ".Important": fs(7, "V4")},
			map[string]maildir.FolderStats{"INBOX": fs(5, "V1")},
			vIncomplete},
		{"subfolder UIDVALIDITY mismatch is caught",
			map[string]maildir.FolderStats{"INBOX": fs(5, "V1"), ".Sent": fs(3, "V2")},
			map[string]maildir.FolderStats{"INBOX": fs(5, "V1"), ".Sent": fs(3, "V9")},
			vUIDMismatch},
		{"empty subfolder with differing uid is still consistent",
			map[string]maildir.FolderStats{"INBOX": fs(5, "V1"), ".Drafts": fs(0, "V2")},
			map[string]maildir.FolderStats{"INBOX": fs(5, "V1"), ".Drafts": fs(0, "V9")},
			vConsistent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if mv := classifyMailbox(c.src, c.dst); mv.kind != c.want {
				t.Errorf("classifyMailbox kind = %s, want %s", kindLabel(mv.kind), kindLabel(c.want))
			}
		})
	}
}

// The crucial behavior: the note must tell the truth about whether re-running
// --apply helps.
func TestClassifyVerifyNotesAreHonest(t *testing.T) {
	// DEST AHEAD must say --apply will NOT change it.
	ahead := classifyVerify(bs(3781, "V1"), bs(3833, "V1"), nil, nil)
	if !strings.Contains(ahead.note, "NOT") {
		t.Errorf("DEST AHEAD note should say re-run will NOT help: %q", ahead.note)
	}
	if !strings.Contains(ahead.note, "52") {
		t.Errorf("DEST AHEAD note should mention the 52-message difference: %q", ahead.note)
	}

	// INCOMPLETE must invite re-running --apply.
	inc := classifyVerify(bs(505, "V1"), bs(500, "V1"), nil, nil)
	if !strings.Contains(inc.note, "re-run --apply") {
		t.Errorf("INCOMPLETE note should invite re-run: %q", inc.note)
	}
	if !strings.Contains(inc.note, "5") {
		t.Errorf("INCOMPLETE note should mention the 5 missing: %q", inc.note)
	}
}

func TestClassifyDeepContent(t *testing.T) {
	if got := classifyDeepContent(false, false, nil, nil, nil); got != contentClean {
		t.Errorf("deep off => %v, want contentClean", got)
	}
	if got := classifyDeepContent(true, false, nil, nil, nil); got != contentClean {
		t.Errorf("clean bodies => %v, want contentClean", got)
	}
	if got := classifyDeepContent(true, false, []string{"a"}, nil, nil); got != contentDiverged {
		t.Errorf("missing body => %v, want contentDiverged", got)
	}
	if got := classifyDeepContent(true, false, nil, []string{"b"}, nil); got != contentDiverged {
		t.Errorf("changed body => %v, want contentDiverged", got)
	}
	// A per-message body that could not be read (unverified) with no corruption is
	// UNVERIFIED, never clean — the requested check could not run for it.
	if got := classifyDeepContent(true, false, nil, nil, []string{"u"}); got != contentUnverified {
		t.Errorf("unverified-only => %v, want contentUnverified", got)
	}
	// Real loss outranks unverified: corruption present alongside an unreadable body is
	// still CONTENT-bad, not merely unverified.
	if got := classifyDeepContent(true, false, nil, []string{"b"}, []string{"u"}); got != contentDiverged {
		t.Errorf("changed + unverified => %v, want contentDiverged (real loss wins)", got)
	}
	// The fix: a deep check the user requested whose digests cannot be read must be
	// UNVERIFIED, never clean — it must not be reported as a passing OK.
	if got := classifyDeepContent(true, true, nil, nil, nil); got != contentUnverified {
		t.Errorf("deep + digest read error => %v, want contentUnverified (must not pass as OK)", got)
	}
}

// TestClassifyMailVerifyImpact is the matrix for the per-mailbox rollup that decides
// the summary bucket and the hard-difference (non-zero exit) count. The load-bearing
// cases: a DEST AHEAD mailbox is soft alone but is PROMOTED to a hard CONTENT/
// UNVERIFIED diff when the deep body check also fails (the bug being fixed), while a
// folder-hard verdict (INCOMPLETE/UIDVALIDITY/UNREADABLE) outranks the deep result so
// a mailbox that is both is counted as ONE hard difference, never two.
func TestClassifyMailVerifyImpact(t *testing.T) {
	cases := []struct {
		name       string
		kind       verifyKind
		content    deepContent
		wantBucket mailVerifyBucket
		wantHard   bool
	}{
		// Consistent folders: clean is OK; a deep failure is a hard diff.
		{"consistent + clean is OK (soft)", vConsistent, contentClean, bOK, false},
		{"consistent + corrupt is CONTENT (hard)", vConsistent, contentDiverged, bContentBad, true},
		{"consistent + unverified is UNVERIFIED (hard)", vConsistent, contentUnverified, bUnverified, true},
		// DEST AHEAD: soft alone, PROMOTED by a deep failure — the bug under test.
		{"dest ahead + clean stays soft", vDestAhead, contentClean, bDestAhead, false},
		{"dest ahead + corrupt promotes to CONTENT (hard)", vDestAhead, contentDiverged, bContentBad, true},
		{"dest ahead + unverified promotes to UNVERIFIED (hard)", vDestAhead, contentUnverified, bUnverified, true},
		// Folder-hard verdicts outrank the deep result: one hard mailbox, no double count.
		{"incomplete + clean is hard", vIncomplete, contentClean, bIncomplete, true},
		{"incomplete + corrupt stays one hard INCOMPLETE", vIncomplete, contentDiverged, bIncomplete, true},
		{"uidmismatch + clean is hard", vUIDMismatch, contentClean, bUIDMismatch, true},
		{"uidmismatch + unverified stays one hard UIDVALIDITY", vUIDMismatch, contentUnverified, bUIDMismatch, true},
		{"unreadable is hard", vUnreadable, contentClean, bUnreadable, true},
		{"unreadable + corrupt stays one hard UNREADABLE", vUnreadable, contentDiverged, bUnreadable, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, hard := classifyMailVerifyImpact(c.kind, c.content)
			if b != c.wantBucket || hard != c.wantHard {
				t.Errorf("classifyMailVerifyImpact(%v, %v) = (%v, %v), want (%v, %v)",
					c.kind, c.content, b, hard, c.wantBucket, c.wantHard)
			}
		})
	}
}
