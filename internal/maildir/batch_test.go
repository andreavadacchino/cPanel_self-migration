package maildir

import "testing"

func sum(b []FileEntry) int64 {
	var t int64
	for _, f := range b {
		t += f.Size
	}
	return t
}

func TestSplitBatchesEmpty(t *testing.T) {
	if got := SplitBatches(nil, 100); got != nil {
		t.Errorf("empty input = %v, want nil", got)
	}
}

func TestSplitBatchesSingleFits(t *testing.T) {
	files := []FileEntry{{"a", 10}, {"b", 20}}
	b := SplitBatches(files, 100)
	if len(b) != 1 {
		t.Fatalf("got %d batches, want 1", len(b))
	}
	if len(b[0]) != 2 {
		t.Errorf("batch 0 has %d files, want 2", len(b[0]))
	}
}

func TestSplitBatchesExactBoundary(t *testing.T) {
	// 60 + 40 == 100 exactly fits one batch; the third file starts a new one.
	files := []FileEntry{{"a", 60}, {"b", 40}, {"c", 10}}
	b := SplitBatches(files, 100)
	if len(b) != 2 {
		t.Fatalf("got %d batches, want 2: %+v", len(b), b)
	}
	if sum(b[0]) != 100 {
		t.Errorf("batch 0 sum = %d, want 100", sum(b[0]))
	}
	if len(b[1]) != 1 || b[1][0].RelPath != "c" {
		t.Errorf("batch 1 = %+v, want [c]", b[1])
	}
}

func TestSplitBatchesOversizeFile(t *testing.T) {
	// A file larger than max gets its own batch.
	files := []FileEntry{{"small", 10}, {"huge", 500}, {"small2", 10}}
	b := SplitBatches(files, 100)
	if len(b) != 3 {
		t.Fatalf("got %d batches, want 3: %+v", len(b), b)
	}
	if len(b[1]) != 1 || b[1][0].RelPath != "huge" {
		t.Errorf("oversize file should be alone in its batch, got %+v", b[1])
	}
}

func TestSplitBatchesManyFiles(t *testing.T) {
	var files []FileEntry
	for i := 0; i < 10; i++ {
		files = append(files, FileEntry{RelPath: string(rune('a' + i)), Size: 30})
	}
	// max 100 -> 3 files per batch (90), 4th overflows -> 4 batches of 3,3,3,1
	b := SplitBatches(files, 100)
	wantLens := []int{3, 3, 3, 1}
	if len(b) != len(wantLens) {
		t.Fatalf("got %d batches, want %d: lens=%v", len(b), len(wantLens), batchLens(b))
	}
	for i, wl := range wantLens {
		if len(b[i]) != wl {
			t.Errorf("batch %d len = %d, want %d", i, len(b[i]), wl)
		}
		if sum(b[i]) > 100 {
			t.Errorf("batch %d sum = %d exceeds max 100", i, sum(b[i]))
		}
	}
}

func TestSplitBatchesPreservesOrder(t *testing.T) {
	files := []FileEntry{{"1", 50}, {"2", 50}, {"3", 50}, {"4", 50}}
	b := SplitBatches(files, 100)
	var flat []string
	for _, batch := range b {
		for _, f := range batch {
			flat = append(flat, f.RelPath)
		}
	}
	want := []string{"1", "2", "3", "4"}
	for i := range want {
		if flat[i] != want[i] {
			t.Errorf("flattened order[%d] = %q, want %q", i, flat[i], want[i])
		}
	}
}

func batchLens(b [][]FileEntry) []int {
	out := make([]int, len(b))
	for i := range b {
		out[i] = len(b[i])
	}
	return out
}
