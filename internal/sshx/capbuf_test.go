package sshx

import (
	"strings"
	"testing"
)

func TestCapBufKeepsSmallInputVerbatim(t *testing.T) {
	b := newCapBuf(4096)
	_, _ = b.Write([]byte("ERROR 1045: access denied\n"))
	if got, want := b.String(), "ERROR 1045: access denied\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCapBufKeepsHeadAndTailWithTruncationMarker(t *testing.T) {
	b := newCapBuf(20)                               // headMax=10, tailMax=10
	_, _ = b.Write([]byte(strings.Repeat("A", 10)))  // head
	_, _ = b.Write([]byte(strings.Repeat("B", 100))) // middle — must be dropped
	_, _ = b.Write([]byte("ERR_AT_END"))             // the actionable trailing line

	got := b.String()
	if !strings.HasPrefix(got, "AAAAAAAAAA") {
		t.Errorf("head was lost: %q", got)
	}
	// The whole point: a head-only buffer dropped this; it must survive now.
	if !strings.HasSuffix(got, "ERR_AT_END") {
		t.Errorf("tail (the trailing error line) was lost: %q", got)
	}
	if !strings.Contains(got, "omitted") {
		t.Errorf("truncation must be marked, got %q", got)
	}
}

func TestCapBufExactFitHasNoMarker(t *testing.T) {
	b := newCapBuf(20) // headMax=10, tailMax=10 → 20 bytes fit with no gap
	_, _ = b.Write([]byte(strings.Repeat("x", 20)))
	if got := b.String(); strings.Contains(got, "omitted") {
		t.Errorf("no bytes were dropped, marker must be absent: %q", got)
	} else if got != strings.Repeat("x", 20) {
		t.Errorf("got %q, want the 20 bytes contiguous", got)
	}
}
