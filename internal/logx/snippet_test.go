package logx

import (
	"strings"
	"testing"
)

func TestSnippet(t *testing.T) {
	if got := Snippet([]byte("short"), 200); got != "short" {
		t.Errorf("Snippet(short) = %q, want unchanged", got)
	}
	// Over the limit: truncated to exactly limit bytes plus a trailing ellipsis.
	long := Snippet([]byte(strings.Repeat("x", 250)), 200)
	if len(long) != 200+len("…") || !strings.HasSuffix(long, "…") {
		t.Errorf("Snippet(long) len=%d, want 200 bytes + ellipsis", len(long))
	}
	// limit <= 0 disables truncation.
	if got := Snippet([]byte(strings.Repeat("x", 50)), 0); len(got) != 50 {
		t.Errorf("Snippet with limit 0 must return the whole value, got len %d", len(got))
	}
}
