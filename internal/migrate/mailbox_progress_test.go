package migrate

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// TestMailboxProgressRescanRendersInterruption checks that a mid-copy re-scan
// produces the visible sequence the operator expects: the failed batch's row is
// finalized with the batch numbers, a note describes what changed, and a NEW row
// then carries the final result (proving a fresh progress line was opened).
func TestMailboxProgressRescanRendersInterruption(t *testing.T) {
	var buf bytes.Buffer
	log := logx.NewTo(&buf, 0)

	mp := newMailboxProgress(log, nil, "info@rsgarage.eu")
	mp.SetTotal(1000)
	mp.Add(500)
	mp.Rescan(3, 9, 2, 1) // batch 3/9 failed; 2 vanished, 1 appeared
	mp.replace("FINAL-RESULT-LINE")

	out := buf.String()
	for _, want := range []string{
		"info@rsgarage.eu",
		"re-scanning",
		"batch 3/9 changed on source",
		"2 message(s) vanished, 1 appeared",
		"continuing with the missing blocks",
		"FINAL-RESULT-LINE",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("re-scan output missing %q\n--- got ---\n%s", want, out)
		}
	}

	// The interruption line must come BEFORE the final result line (the failed row
	// is frozen first, then a fresh row becomes the result).
	if i, j := strings.Index(out, "re-scanning"), strings.Index(out, "FINAL-RESULT-LINE"); i < 0 || j < 0 || i > j {
		t.Errorf("expected the re-scan line before the final result line, got order i=%d j=%d", i, j)
	}
}
