// Package logx is a tiny progress logger for the CLI: numbered step headers
// with indented detail/warn lines, written to stdout. It has no dependencies
// and produces clean, human-readable terminal output.
//
//	[1/2] Analyzing the SOURCE (~/mail) ...
//	  -> connected to user@host:port
//	  -> 7 domains, 22 mailboxes (15 active, 7 orphan)
//	  ! SPF on destination differs
//	  ✓ wrote mail_analysis.log
package logx

import (
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Logger renders numbered steps and indented details to a writer.
type Logger struct {
	// mu serializes every write to w AND the cur counter, across the Logger's own
	// methods AND every Progress it creates (a Progress locks this same mutex). It
	// keeps concurrent log/progress output from interleaving or racing if logging
	// ever happens from more than one goroutine. The zero value is ready to use, so
	// a Logger built as a struct literal (e.g. in tests) is safe too.
	mu    sync.Mutex
	w     io.Writer
	total int  // total number of steps (for the [n/N] counter); 0 = no counter
	cur   int  // current step number
	color bool // emit ANSI colors
}

// New returns a Logger writing to stdout with the given total step count.
// Pass total=0 to omit the [n/N] counter. Colors are auto-enabled when stdout
// is a terminal and NO_COLOR is unset.
func New(total int) *Logger {
	return &Logger{w: os.Stdout, total: total, color: colorEnabled(os.Stdout)}
}

// NewTo returns a Logger writing to w (used in tests; colors disabled).
func NewTo(w io.Writer, total int) *Logger {
	return &Logger{w: w, total: total, color: colorEnabled(w)}
}

// debugEnabled gates the package-level Debug function. It is an atomic so the
// hot "is debug on?" check is lock-free yet race-free even though the flag is
// read from many goroutines (SSH dial / keepalive / transfer all call Debug)
// while it could be toggled. In practice SetDebug runs once at startup, before
// those goroutines spawn, but the atomic makes the facility safe regardless —
// the safety no longer rests on an unenforced "set it once" convention.
var debugEnabled atomic.Bool

// debugMu serializes Debug's writes to debugOut — so concurrent diagnostic lines
// from different goroutines never interleave mid-line — and guards debugOut and
// debugStart. debugStart is the origin for the elapsed marker; debugOut is the
// sink (os.Stderr in production, swapped for a buffer in tests).
var (
	debugMu    sync.Mutex
	debugOut   io.Writer = os.Stderr
	debugStart time.Time
)

// SetDebug turns the global debug logging on or off. Enabling (re)sets the
// elapsed-time origin so Debug's "+<elapsed>" marker counts from this call.
func SetDebug(on bool) {
	debugMu.Lock()
	if on {
		debugStart = time.Now()
	}
	debugMu.Unlock()
	debugEnabled.Store(on)
}

// SwapDebugOutput redirects the Debug/Warn sink to w and returns a function that
// restores the previous sink. It exists for tests that need to assert on the
// diagnostic output (e.g. to prove a secret is redacted before it is logged); it
// generalizes the swap dance the logx tests already perform. It takes debugMu so
// it is safe to call concurrently with Debug/Warn, and the returned restore is
// idempotent-safe to defer.
func SwapDebugOutput(w io.Writer) (restore func()) {
	debugMu.Lock()
	prev := debugOut
	debugOut = w
	debugMu.Unlock()
	return func() {
		debugMu.Lock()
		debugOut = prev
		debugMu.Unlock()
	}
}

// Debug prints a "[debug +<elapsed>]" diagnostic line to stderr when debug
// logging is enabled. The "+<elapsed>" is the time since debug was enabled — a
// monotonic marker for correlating the timing and reconstructing the order of
// concurrent SSH sessions and transfers, which otherwise interleave on stderr
// with no clue to their real sequence. It writes to stderr (never stdout) so it
// cannot corrupt the human-facing stdout artifacts, and serializes its writes so
// concurrent lines stay intact.
func Debug(format string, args ...any) {
	if !debugEnabled.Load() {
		return
	}
	debugMu.Lock()
	defer debugMu.Unlock()
	elapsed := time.Since(debugStart).Round(time.Millisecond)
	if _, err := fmt.Fprintf(debugOut, "  [debug +%s] %s\n", elapsed, fmt.Sprintf(format, args...)); err != nil {
		diagnosticWriteFailed(err)
	}
}

// Warn prints a "[warn]" diagnostic line to stderr UNCONDITIONALLY — unlike
// Debug, it shows even at the default --log-level info. It is the package-level
// WARN tier for the low-level packages (sshx, maildir, dbmig) that hold no
// *Logger but must surface a condition the operator should see without having to
// already suspect a problem and re-run with debug: a changed host key, a
// keepalive that closed the connection, a high-risk credential guess. It shares
// Debug's sink and lock, so the two never interleave mid-line. (Distinct from
// Logger.Warn, which is part of the structured stdout step output; this is an
// out-of-band diagnostic, kept off stdout so it cannot corrupt the artifacts.)
func Warn(format string, args ...any) {
	debugMu.Lock()
	defer debugMu.Unlock()
	tag := "[warn]"
	if colorEnabledStderr(debugOut) {
		tag = ansiYellow + tag + ansiReset
	}
	if _, err := fmt.Fprintf(debugOut, "  %s %s\n", tag, fmt.Sprintf(format, args...)); err != nil {
		diagnosticWriteFailed(err)
	}
}

func diagnosticWriteFailed(err error) {
	_, _ = fmt.Fprintf(os.Stderr, "  [warn] diagnostic output write failed: %v\n", err)
}

// Step prints a numbered step header (in blue), e.g. "[2/5] Creating domains".
func (l *Logger) Step(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cur++
	var line string
	if l.total > 0 {
		line = fmt.Sprintf("[%d/%d] %s", l.cur, l.total, msg)
	} else {
		line = fmt.Sprintf("==> %s", msg)
	}
	fmt.Fprintln(l.w, l.wrap(ansiBlue, line))
}

// Skip prints a numbered step header marked as skipped, advancing the counter
// like a normal step so the [n/N] sequence stays linear. reason explains why.
// The header is dimmed; the "(skipped: reason)" suffix is not colored.
func (l *Logger) Skip(reason, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cur++
	var head string
	if l.total > 0 {
		head = fmt.Sprintf("[%d/%d] %s", l.cur, l.total, msg)
	} else {
		head = fmt.Sprintf("==> %s", msg)
	}
	fmt.Fprintf(l.w, "%s (skipped: %s)\n", l.wrap(ansiDim, head), reason)
}

// Detail prints an indented progress line under the current step ("     -> ...").
// The 5-space indent makes the "->" marker start at the SAME column as the "•"
// of per-item lines (Item/ItemLine), so every line under a step lines up
// vertically regardless of which marker it uses.
func (l *Logger) Detail(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "     -> %s\n", msg)
}

// Info prints an informational line — a fact discovered, not an action in
// progress — with a "•" bullet at the same indent as Detail's "->". Use Detail
// ("->") for "doing X ..." lines and Info ("•") for "here is the result" lines,
// so the reader can tell progress from data at a glance.
func (l *Logger) Info(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "     • %s\n", msg)
}

// Item prints a deeper-indented per-item line ("     • ..."), for per-domain /
// per-mailbox entries within a step.
func (l *Logger) Item(format string, args ...any) {
	line := l.ItemLine(format, args...) // pure (no shared state) — build before locking
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintln(l.w, line)
}

// ItemLine formats a per-item line ("     • ...") and RETURNS it (without a
// trailing newline) instead of printing it. Used when the same on-screen item
// rendering must also be teed elsewhere (e.g. the migration report writes a
// plain variant to its file while the screen gets this one).
func (l *Logger) ItemLine(format string, args ...any) string {
	return fmt.Sprintf("     • %s", fmt.Sprintf(format, args...))
}

// Notice prints msg as a standalone line and returns a func that REPLACES it. On a
// live TTY msg is printed WITHOUT a trailing newline (a transient line) and the
// returned func overwrites it in place (\r + erase-to-EOL + replacement); when output
// is NOT live (--log-level debug, a non-TTY, or a test buffer) msg is committed now and
// the replacement is committed later as ordinary lines, so a redirected log keeps both.
//
// It is the "show a caveat now, overwrite it with the outcome on success" primitive:
// the message stays on screen if the program is interrupted before the replace runs, and
// is cleanly overwritten (no stale warning) if it is. Caller passes fully-formatted lines
// (indent/markers included), as Step/Detail/Item do for their own.
func (l *Logger) Notice(msg string) (replace func(string)) {
	live := liveProgress(l)
	l.mu.Lock()
	if live {
		fmt.Fprint(l.w, msg) // no newline: keep the line "open" so replace can overwrite it
	} else {
		fmt.Fprintln(l.w, msg)
	}
	l.mu.Unlock()
	return func(s string) {
		l.mu.Lock()
		defer l.mu.Unlock()
		if live {
			fmt.Fprintf(l.w, "\r%s%s\n", clearEOL, s)
		} else {
			fmt.Fprintln(l.w, s)
		}
	}
}

// Warn prints an indented warning line ("     ! ...") with a red marker, aligned
// to the same column as Detail's "->" and Item's "•".
func (l *Logger) Warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "     %s %s\n", l.wrap(ansiRed, "!"), msg)
}

// OK prints an indented success line ("     ✓ ...") with a green marker, aligned
// to the same column as Detail's "->" and Item's "•".
func (l *Logger) OK(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "     %s %s\n", l.wrap(ansiGreen, "✓"), msg)
}

// Plain prints a line with no indentation (blank lines, final summaries).
func (l *Logger) Plain(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "%s\n", msg)
}
