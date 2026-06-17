package maildir

import "testing"

// addFunc adapts a plain func into a ProgressSink: Add forwards, the others are
// no-ops (the outer SyncBoxProgress already set total/batch).
func TestAddFuncProgressSink(t *testing.T) {
	var sum int64
	var s ProgressSink = addFunc(func(n int64) { sum += n })
	s.SetTotal(999)  // no-op
	s.SetBatch(1, 2) // no-op
	s.Add(5)
	s.Add(7)
	if sum != 12 {
		t.Errorf("addFunc Add total = %d, want 12", sum)
	}
}

// parseFileList must skip malformed records: a valid size with an empty path,
// and a record with no TAB at all.
func TestParseFileListSkipsMalformed(t *testing.T) {
	files, _ := parseFileList("5\t\x00" + "no-tab-record\x00" + "10\tcur/ok\x00")
	if len(files) != 1 || files[0].RelPath != "cur/ok" {
		t.Errorf("parseFileList = %+v, want only cur/ok (empty-path and no-TAB records skipped)", files)
	}
}

// DiffMessageSets must cap BOTH sides' examples at max, sorted.
func TestDiffMessageSetsTruncatesToMax(t *testing.T) {
	src := map[string]struct{}{"a": {}, "b": {}, "c": {}, "d": {}}  // all only-src
	dest := map[string]struct{}{"p": {}, "q": {}, "r": {}, "s": {}} // all only-dest
	onlySrc, onlyDest := DiffMessageSets(src, dest, 2)
	if len(onlySrc) != 2 || onlySrc[0] != "a" || onlySrc[1] != "b" {
		t.Errorf("onlySrc = %v, want [a b] (capped, sorted)", onlySrc)
	}
	if len(onlyDest) != 2 || onlyDest[0] != "p" || onlyDest[1] != "q" {
		t.Errorf("onlyDest = %v, want [p q] (capped, sorted)", onlyDest)
	}
}
