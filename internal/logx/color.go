package logx

import (
	"os"
)

// ANSI color codes used by the logger. Green (40) and red (196) use 256-color
// codes for more vivid/intense shades than the basic colors (32/31).
const (
	ansiReset  = "\x1b[0m"
	ansiBlue   = "\x1b[34m"
	ansiGreen  = "\x1b[38;5;40m"
	ansiRed    = "\x1b[38;5;196m"
	ansiYellow = "\x1b[33m"
	ansiDim    = "\x1b[2m" // dimmed/faint, for skipped steps
)

// colorEnabled reports whether ANSI colors should be emitted to w. Colors are
// on only when w is the real stdout AND it is a terminal AND NO_COLOR is unset
// (the de-facto standard, https://no-color.org). When output is redirected to a
// file or a pipe, colors are disabled so logs stay clean.
func colorEnabled(w any) bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	if w != os.Stdout {
		return false // tests use a buffer; keep their output plain
	}
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// colorEnabledStderr mirrors colorEnabled for the diagnostic (Debug/Warn) sink,
// which writes to os.Stderr rather than os.Stdout. Colors are on only when
// NO_COLOR is unset, the sink is the real os.Stderr (tests swap in a buffer, which
// must stay plain), AND stderr is a terminal.
func colorEnabledStderr(w any) bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	if w != os.Stderr {
		return false // redirected to a file/pipe or a test buffer: keep it plain
	}
	info, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// wrap applies an ANSI color around s when enabled, otherwise returns s as-is.
func (l *Logger) wrap(code, s string) string {
	if !l.color {
		return s
	}
	return code + s + ansiReset
}

// Blue/Green/Red/Yellow color a string for inline use in messages (e.g. to
// color the IDENTICAL/DIFFERS token), honoring the logger's color setting.
func (l *Logger) Blue(s string) string   { return l.wrap(ansiBlue, s) }
func (l *Logger) Green(s string) string  { return l.wrap(ansiGreen, s) }
func (l *Logger) Red(s string) string    { return l.wrap(ansiRed, s) }
func (l *Logger) Yellow(s string) string { return l.wrap(ansiYellow, s) }
