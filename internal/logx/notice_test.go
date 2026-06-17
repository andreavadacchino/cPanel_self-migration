package logx

import (
	"bytes"
	"testing"
)

// Non-live (buffer / --log-level debug / non-TTY): Notice commits the message now and
// the replacement later as TWO ordinary lines, so a redirected log keeps both.
func TestNoticeNonLiveCommitsBothLines(t *testing.T) {
	var buf bytes.Buffer
	l := NewTo(&buf, 0) // buffer -> not live
	replace := l.Notice("CAVEAT")
	if got := buf.String(); got != "CAVEAT\n" {
		t.Fatalf("non-live Notice = %q, want \"CAVEAT\\n\"", got)
	}
	replace("DONE")
	if got := buf.String(); got != "CAVEAT\nDONE\n" {
		t.Fatalf("non-live after replace = %q, want both lines committed", got)
	}
}

// Live (TTY): Notice prints the message WITHOUT a newline (a transient line) and replace
// overwrites it in place (\r + erase-to-EOL + replacement) — so a clean run leaves only
// the replacement, while an interruption before replace leaves the caveat on screen.
func TestNoticeLiveOverwritesInPlace(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{w: &buf, color: true} // live
	replace := l.Notice("CAVEAT")
	if got := buf.String(); got != "CAVEAT" {
		t.Fatalf("live Notice must print the transient line with NO newline, got %q", got)
	}
	replace("DONE")
	if want := "CAVEAT\r" + clearEOL + "DONE\n"; buf.String() != want {
		t.Fatalf("live replace = %q, want %q (carriage-return + erase + replacement)", buf.String(), want)
	}
}
