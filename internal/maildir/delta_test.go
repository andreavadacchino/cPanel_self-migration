package maildir

import (
	"reflect"
	"testing"
)

func relPaths(fs []FileEntry) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.RelPath
	}
	return out
}

func TestDeltaFilesOnlyMissing(t *testing.T) {
	src := []FileEntry{
		{"cur/100.M1.h:2,S", 10},
		{"cur/200.M2.h:2,S", 20},
		{"new/300.M3.h", 30},
		{"dovecot-uidlist", 5},
	}
	dest := []FileEntry{
		{"cur/100.M1.h:2,S", 10},
		{"dovecot-uidlist", 4}, // present, but control files are ALWAYS re-sent
	}
	got := deltaFiles(src, dest)
	// 100 already present -> skipped. 200, 300 missing. The control file
	// dovecot-uidlist is re-sent even though a same-named file is on the
	// destination, because its content (UIDVALIDITY) may differ.
	want := []string{"cur/200.M2.h:2,S", "new/300.M3.h", "dovecot-uidlist"}
	if !reflect.DeepEqual(relPaths(got), want) {
		t.Errorf("delta = %v, want %v", relPaths(got), want)
	}
}

func TestDeltaFilesAlwaysReSendsControlFiles(t *testing.T) {
	// Messages are all present on the destination; only the Dovecot control
	// files must still be re-sent so UIDVALIDITY is realigned. This is the
	// fresh-recreate UIDVALIDITY-mismatch fix.
	src := []FileEntry{
		{"cur/1.M.h:2,S", 1},
		{"dovecot-uidlist", 9},
		{".Sent/dovecot-uidlist", 7},
		{"dovecot-keywords", 3},
	}
	dest := []FileEntry{
		{"cur/1.M.h:2,S", 1},
		{"dovecot-uidlist", 99},       // different size -> different UIDVALIDITY
		{".Sent/dovecot-uidlist", 77}, // per-folder control file too
		{"dovecot-keywords", 33},
	}
	got := relPaths(deltaFiles(src, dest))
	want := []string{"dovecot-uidlist", ".Sent/dovecot-uidlist", "dovecot-keywords"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("delta = %v, want only the control files %v", got, want)
	}
}

func TestIsControlFile(t *testing.T) {
	cases := map[string]bool{
		"dovecot-uidlist":             true,
		".Sent/dovecot-uidlist":       true,
		"dovecot-keywords":            true,
		".Drafts/dovecot.mailbox.log": true,
		"cur/1700.M1.host:2,S":        false,
		"new/300.M3.h":                false,
		"dovecot.index":               false, // an index, excluded from the stream anyway
	}
	for in, want := range cases {
		if got := isControlFile(in); got != want {
			t.Errorf("isControlFile(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDeltaFilesFlagChangeNotReSent(t *testing.T) {
	// Same INBOX message in new/ and cur/ with different flags; the dest already has
	// it (cur/, other flags). Within one folder it must NOT be re-sent.
	src := []FileEntry{{"new/100.M1.h", 10}, {"cur/100.M1.h:2,S", 10}}
	dest := []FileEntry{{"cur/100.M1.h:2,FS", 10}}
	if got := deltaFiles(src, dest); len(got) != 0 {
		t.Errorf("same INBOX message must not be re-sent on a flag/new<->cur change, got %v", relPaths(got))
	}
}

// TestDeltaFilesCrossFolderNotDropped: a message present in TWO folders on the
// source but only ONE on the destination must still copy the missing folder's copy.
// Dedup is per-folder, not mailbox-wide — otherwise a Sent/Archive copy of a message
// that also lives in INBOX is silently dropped on a delta sync (the D5 fix).
func TestDeltaFilesCrossFolderNotDropped(t *testing.T) {
	src := []FileEntry{{"cur/100.M1.h:2,S", 10}, {".Sent/cur/100.M1.h:2,S", 10}}
	dest := []FileEntry{{"cur/100.M1.h:2,S", 10}} // only the INBOX copy exists on dest
	got := deltaFiles(src, dest)
	if len(got) != 1 || got[0].RelPath != ".Sent/cur/100.M1.h:2,S" {
		t.Errorf("the .Sent copy must be in the delta (per-folder dedup), got %v", relPaths(got))
	}
}

func TestDeltaFilesEmptyDestCopiesAll(t *testing.T) {
	src := []FileEntry{{"cur/1.M.h:2,S", 1}, {"new/2.M.h", 2}}
	got := deltaFiles(src, nil)
	if len(got) != 2 {
		t.Errorf("empty dest should copy all %d files, got %d", len(src), len(got))
	}
}

func TestDeltaFilesNothingMissing(t *testing.T) {
	src := []FileEntry{{"cur/1.M.h:2,S", 1}}
	dest := []FileEntry{{"cur/1.M.h:2,RS", 1}} // same msg, different flags
	if got := deltaFiles(src, dest); len(got) != 0 {
		t.Errorf("nothing should be missing, got %v", relPaths(got))
	}
}

func TestFileIdentity(t *testing.T) {
	// Same INBOX message across new/cur and flags shares one identity (no re-copy).
	if a, b := fileIdentity("cur/1700.M1.host:2,S"), fileIdentity("new/1700.M1.host:2,FS"); a != b {
		t.Errorf("same INBOX message must share identity: %q vs %q", a, b)
	}
	// The SAME base ID in a DIFFERENT folder is a DISTINCT message: it must NOT
	// collapse onto the INBOX copy, or a Sent/Archive copy would be dropped (D5).
	if a, b := fileIdentity("cur/9.M.h:2,S"), fileIdentity(".Sent/cur/9.M.h:2,S"); a == b {
		t.Errorf("same base ID in INBOX vs .Sent must be distinct, both = %q", a)
	}
	// A subfolder's new/ and cur/ still share identity (same folder).
	if a, b := fileIdentity(".Sent/new/9.M.h"), fileIdentity(".Sent/cur/9.M.h:2,S"); a != b {
		t.Errorf(".Sent new/ vs cur/ must share identity: %q vs %q", a, b)
	}
	// Control files stay keyed by their full path.
	for _, cf := range []string{"dovecot-uidlist", "dovecot-keywords", ".Sent/dovecot-uidlist"} {
		if got := fileIdentity(cf); got != cf {
			t.Errorf("control file %q should be path-keyed, got %q", cf, got)
		}
	}
}
