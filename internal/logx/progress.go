package logx

import (
	"fmt"
	"strings"
	"time"
)

// Progress renders a single, in-place updating progress line for one unit of
// work (e.g. copying one mailbox):
//
//	[#####.....] 52%  7.2/13.8 GB  batch 3/8
//
// It only animates on a terminal; when colors/TTY are off (output redirected to
// a file or pipe) it stays silent so logs are not polluted with \r spam.
// Updates are throttled so a fast stream doesn't flood the terminal.
type Progress struct {
	l         *Logger
	prefix    string // shown before the bar, e.g. "[12/15] info@main.example"
	total     int64  // total units (0 = unknown -> percentage hidden)
	width     int    // bar width in characters
	live      bool   // animate in place (TTY) vs stay silent
	unit      string // "" = bytes (human-formatted); non-empty = plain count + this label (e.g. "docroots")
	inline    bool   // true = "<prefix>  [bar] data" (prefix is a pre-padded item block); false = "[bar] %  <prefix> data"
	liveBytes bool   // inline + unknown total: show a live HumanBytes(cur) counter (e.g. a DB dump of unknown size)
	// All state below — and every write to l.w — is guarded by the PARENT Logger's
	// mutex (p.l.mu), so concurrent progress updates and ordinary log lines on the
	// same Logger never interleave or race. (No per-Progress mutex: a separate lock
	// would not coordinate with the Logger's own writes to the shared writer.)
	cur     int64
	batch   string
	last    time.Time
	started bool
	frame   int64 // repaint counter; drives the moving block of an unknown-total bar
}

// minRedraw bounds how often the bar repaints, to avoid terminal flooding.
const minRedraw = 100 * time.Millisecond

// liveProgress reports whether a progress bar should animate in place. It needs a
// color/TTY writer AND debug logging OFF: under --log-level debug an operator
// typically merges the streams (... 2>&1 | tee run.log) to capture everything in
// order, and there the bar's "\r ... \033[K" repaints on stdout interleave with
// the [debug]/[warn] lines on stderr and mangle both. Going non-live in debug
// mode (final result line only, no repaint) keeps the merged capture readable.
func liveProgress(l *Logger) bool {
	return l.color && !debugEnabled.Load()
}

// NewProgress starts a progress line for `total` bytes, prefixed with `prefix`.
// If total <= 0 the percentage is omitted (only bytes are shown).
func (l *Logger) NewProgress(prefix string, total int64) *Progress {
	return &Progress{l: l, prefix: prefix, total: total, width: 24, live: liveProgress(l)}
}

// NewCountProgress starts a progress line that counts discrete units (e.g.
// docroots) rather than bytes: it renders "[####....] 33%  2/6 docroots". Used
// for the web-file analysis, where each step is one whole du/find (no byte-level
// progress). unit is the plural label shown after the count.
func (l *Logger) NewCountProgress(prefix string, total int, unit string) *Progress {
	return &Progress{l: l, prefix: prefix, total: int64(total), width: 24, live: liveProgress(l), unit: unit}
}

// NewInlineCountProgress is the inline (item-block-left, bar-right) counterpart
// of NewCountProgress: the bar sits AFTER an already-padded "<marker> <name>"
// block and the right side counts discrete units. With total > 0 it shows
// "N/M unit" + a percentage; with total <= 0 it shows a live "N unit" counter and
// an indeterminate bar (no percentage, no byte text). Pair with Replace to turn
// the row into its result line.
func (l *Logger) NewInlineCountProgress(prefix string, total int, unit string) *Progress {
	return &Progress{l: l, prefix: prefix, total: int64(total), width: 16, live: liveProgress(l), inline: true, unit: unit}
}

// NewInlineBytesProgress is an inline progress whose total is unknown but whose
// throughput IS in bytes — it shows a live HumanBytes(cur) counter with an
// indeterminate bar (used for a streamed DB dump, whose final size we cannot know
// up front). Pair with Replace for the result line.
func (l *Logger) NewInlineBytesProgress(prefix string) *Progress {
	return &Progress{l: l, prefix: prefix, total: 0, width: 16, live: liveProgress(l), inline: true, liveBytes: true}
}

// NewInlineProgress starts a progress line whose bar sits AFTER an already-
// formatted item block (e.g. "     • → info@x.it" padded to a fixed column),
// occupying the spot where the final result text will go:
//
//   - → info@domain4.example          [########........] 52%  12/40 files
//
// `prefix` must be the full, already-indented and padded left block; render adds
// no extra indent. Pair with Replace to overwrite the bar with the final result
// line once the work completes (the SAME line becomes the result). The bar is a
// narrower 16 chars so the line is not too wide next to the item block.
func (l *Logger) NewInlineProgress(prefix string, total int64) *Progress {
	return &Progress{l: l, prefix: prefix, total: total, width: 16, live: liveProgress(l), inline: true}
}

// Draw forces an immediate repaint of the current bar state (bypassing the
// redraw throttle). Use it to show the bar right away — e.g. at 0% before the
// first slow step begins — so the user never stares at a frozen cursor.
func (p *Progress) Draw() { p.maybeDraw(true) }

// SetTotal updates the total byte count (used when the real amount to transfer
// — the delta — is only known after listing the destination).
func (p *Progress) SetTotal(bytes int64) {
	p.l.mu.Lock()
	p.total = bytes
	p.l.mu.Unlock()
}

// SetBatch sets the "batch i/n" suffix shown after the byte counter.
func (p *Progress) SetBatch(i, n int) {
	p.l.mu.Lock()
	p.batch = fmt.Sprintf("batch %d/%d", i, n)
	p.l.mu.Unlock()
}

// SetSuffix sets a free-text suffix shown after the counter (e.g. the name of
// the item currently being processed). It shares the slot used by SetBatch.
func (p *Progress) SetSuffix(s string) {
	p.l.mu.Lock()
	p.batch = s
	p.l.mu.Unlock()
	p.maybeDraw(false)
}

// SetPrefix updates the prefix (verb + item name) shown after the bar, e.g.
// "analyzing addon1.example". Used when one progress line walks several
// items (the bar advances by count while the name changes).
func (p *Progress) SetPrefix(s string) {
	p.l.mu.Lock()
	p.prefix = s
	p.l.mu.Unlock()
	p.maybeDraw(false)
}

// Add advances the byte counter by n and repaints if enough time has passed.
func (p *Progress) Add(n int64) {
	p.l.mu.Lock()
	p.cur += n
	p.l.mu.Unlock()
	p.maybeDraw(false)
}

// Set replaces the counter with an ABSOLUTE value, for streams that report a
// cumulative total on each tick (e.g. a running file count) rather than a delta.
// Pairs with the unknown-total live-counter mode so data() renders "N unit"
// directly instead of stuffing the count into the suffix slot.
func (p *Progress) Set(n int64) {
	p.l.mu.Lock()
	p.cur = n
	p.l.mu.Unlock()
	p.maybeDraw(false)
}

func (p *Progress) maybeDraw(force bool) {
	if !p.live {
		return
	}
	p.l.mu.Lock()
	defer p.l.mu.Unlock()
	now := time.Now()
	if !force && p.started && now.Sub(p.last) < minRedraw {
		return
	}
	p.last = now
	p.started = true
	// \r returns to column 0; clearEOL erases from the cursor to the end of the
	// line so a SHORTER new frame cannot leave the tail of a longer previous frame
	// on screen (e.g. a long "analyzing addon1.example ..." line followed by a
	// shorter one would otherwise leave trailing "...ts" garbage behind).
	fmt.Fprintf(p.l.w, "\r%s%s", p.render(), clearEOL)
	// Advance the animation clock AFTER painting, so the first frame is 0 and an
	// unknown-total bar's moving block steps once per repaint (~10/s given the
	// 100ms redraw throttle).
	p.frame++
}

// clearEOL is the ANSI "erase to end of line" sequence. Emitted right after the
// bar text on every repaint (and on Finish/Replace) so clearing never depends on
// matching the previous frame's exact length.
const clearEOL = "\033[K"

// render builds the bar line (without the leading \r). Caller holds the lock.
//
// Layout: the bar comes FIRST, at a fixed column, so it stays aligned across
// every phase and item regardless of how long the prefix/name is; the variable
// text (prefix, counters, suffix) follows to the right where its width doesn't
// disturb alignment:
//
//	[#####...........]  52%  copying info@main.example  6.7 GB/12.9 GB  batch 3/8
//	[########........]  33%  analyzing site2.example  2/6 docroots
//	[................]   0%  copying x  0 B copied            (unknown total)
func (p *Progress) render() string {
	var b strings.Builder

	bar := func() string {
		if p.total <= 0 {
			// Unknown total: a moving block (bounces left<->right, one step per
			// repaint) so the bar shows live activity instead of sitting empty while
			// a streamed step — e.g. the source web-file scan — counts up.
			return indeterminateBar(p.width, p.frame)
		}
		frac := float64(p.cur) / float64(p.total)
		if frac > 1 {
			frac = 1
		}
		if frac < 0 {
			frac = 0 // a rollback over-shoot can make cur negative; a negative fill
			// count would panic strings.Repeat below.
		}
		filled := int(frac * float64(p.width))
		return strings.Repeat("#", filled) + strings.Repeat(".", p.width-filled)
	}
	data := func() string {
		switch {
		case p.total > 0 && p.unit != "":
			return fmt.Sprintf("%d/%d %s", p.cur, p.total, p.unit)
		case p.total > 0:
			return fmt.Sprintf("%s/%s", HumanBytes(p.cur), HumanBytes(p.total))
		case p.unit != "":
			return fmt.Sprintf("%d %s", p.cur, p.unit)
		default:
			return fmt.Sprintf("%s copied", HumanBytes(p.cur))
		}
	}
	pct := func() string {
		if p.total <= 0 {
			return ""
		}
		frac := float64(p.cur) / float64(p.total)
		if frac > 1 {
			frac = 1
		}
		return fmt.Sprintf("%3.0f%%", frac*100)
	}

	if p.inline {
		// Item block FIRST (already indented + padded), then the bar where the
		// result text will later appear, in one of four sub-modes:
		switch {
		case p.total > 0:
			// Known total: "[bar] %  X/Y GB" (bytes) or "[bar] %  N/M unit" (count);
			// data() already picks the right form.
			fmt.Fprintf(&b, "%s  [%s] %s  %s", p.prefix, bar(), pct(), data())
		case p.unit != "":
			// Live counter, unknown total: "[bar]  N unit", no percentage.
			fmt.Fprintf(&b, "%s  [%s]  %s", p.prefix, bar(), data())
		case p.liveBytes:
			// Live byte counter, unknown total (e.g. a streamed DB dump).
			fmt.Fprintf(&b, "%s  [%s]  %s", p.prefix, bar(), HumanBytes(p.cur))
		default:
			// Unknown total, no unit (e.g. a byte transfer's brief pre-SetTotal
			// frame): JUST the bar — never a misleading "0 B copied".
			fmt.Fprintf(&b, "%s  [%s]", p.prefix, bar())
		}
		if p.batch != "" {
			fmt.Fprintf(&b, "  %s", p.batch)
		}
		return b.String()
	}

	// Default layout: bar + percentage FIRST (fixed columns), then prefix + data.
	if p.total > 0 {
		fmt.Fprintf(&b, "  [%s] %s  ", bar(), pct())
	} else {
		// Unknown total: animated (moving-block) bar, no percentage column, but keep
		// the SAME width so the following text still lines up with the known-total case.
		fmt.Fprintf(&b, "  [%s]       ", bar())
	}
	fmt.Fprintf(&b, "%s  %s", p.prefix, data())

	// Optional trailing suffix (batch counter or current sub-item).
	if p.batch != "" {
		fmt.Fprintf(&b, "  %s", p.batch)
	}
	return b.String()
}

// Finish clears the progress line (TTY) and prints nothing else; the caller
// then logs the final outcome via the normal report line.
func (p *Progress) Finish() {
	if !p.live {
		return
	}
	p.l.mu.Lock()
	defer p.l.mu.Unlock()
	// Return to column 0 and erase to end of line — clears the whole bar regardless
	// of how long the last (or any earlier, longer) frame was.
	fmt.Fprintf(p.l.w, "\r%s", clearEOL)
}

// Replace finishes the progress line by turning IT into the final result line:
// on a TTY it clears the animated bar and prints finalLine in its place (same
// line), then a newline so the next item starts fresh. When not live (output
// redirected), the bar never animated, so it simply prints finalLine — the
// result still appears exactly once. This gives the "bar in the item row that
// becomes the result, then move to the next row" behavior.
func (p *Progress) Replace(finalLine string) {
	p.l.mu.Lock()
	defer p.l.mu.Unlock()
	if p.live {
		// Return to col 0 and erase to end of line, then write the result in place
		// (clearEOL avoids leaving the tail of a longer animated frame behind it).
		fmt.Fprintf(p.l.w, "\r%s", clearEOL)
	}
	fmt.Fprintln(p.l.w, finalLine)
}

// indeterminateBar renders an unknown-total bar as a fixed-width track with a
// short block that bounces from edge to edge, advancing one column per `frame`.
// It conveys "work is ongoing" without claiming a (fake) percentage — used for
// streamed steps whose total is only known once they finish (the source web-file
// scan, a DB dump of unknown size, a manifest read). frame=0 paints the block at
// the left edge, so a direct render() in tests is deterministic.
func indeterminateBar(width int, frame int64) string {
	const seg = 3 // width of the moving block
	if width <= seg {
		return strings.Repeat("#", width)
	}
	span := width - seg // distinct left-edge positions: 0..span
	// Triangle wave over [0,span]: 0,1,..,span,span-1,..,0 (period 2*span), so the
	// block bounces instead of jumping back to the left.
	cycle := int64(2 * span)
	pos := int(((frame % cycle) + cycle) % cycle)
	if pos > span {
		pos = 2*span - pos
	}
	return strings.Repeat(".", pos) + strings.Repeat("#", seg) + strings.Repeat(".", width-seg-pos)
}

// HumanBytes formats a byte count as B/KB/MB/GB/TB with one decimal.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
