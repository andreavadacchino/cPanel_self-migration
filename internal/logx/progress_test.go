package logx

import (
	"bytes"
	"strings"
	"testing"
)

func TestProgressRenderNegativeCurNoPanic(t *testing.T) {
	var buf bytes.Buffer
	l := NewTo(&buf, 0)
	p := l.NewProgress("copying x", 1000)
	p.Add(-50) // a failed-attempt rollback can over-shoot and drive cur negative
	// render() must not panic: a negative fill count would crash strings.Repeat.
	out := p.render()
	if !strings.Contains(out, ".") {
		t.Errorf("negative cur should render an empty (all-dots) bar, got %q", out)
	}
}

func TestProgressSilentWhenNotTTY(t *testing.T) {
	var buf bytes.Buffer
	l := NewTo(&buf, 0) // buffer -> color/live disabled
	p := l.NewProgress("copying info@x.it", 1000)
	p.Add(500)
	p.Finish()
	if buf.Len() != 0 {
		t.Errorf("progress must be silent on non-TTY, got %q", buf.String())
	}
}

func TestProgressRenderWhenLive(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{w: &buf, color: true}
	p := l.NewProgress("copying info@x.it", 1000)
	p.SetBatch(2, 5)
	p.cur = 500 // half
	out := p.render()
	if !strings.Contains(out, "copying info@x.it") {
		t.Errorf("render missing prefix: %q", out)
	}
	if !strings.Contains(out, "50%") {
		t.Errorf("render missing percentage: %q", out)
	}
	if !strings.Contains(out, "batch 2/5") {
		t.Errorf("render missing batch: %q", out)
	}
	// Half the 24-wide bar is filled.
	if !strings.Contains(out, strings.Repeat("#", 12)+strings.Repeat(".", 12)) {
		t.Errorf("bar not half-filled: %q", out)
	}
}

func TestProgressUnknownTotal(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{w: &buf, color: true}
	p := l.NewProgress("copying", 0) // unknown total -> no percentage
	p.cur = 2048
	out := p.render()
	if strings.Contains(out, "%") {
		t.Errorf("unknown total should not show percentage: %q", out)
	}
	if !strings.Contains(out, "2.0 KB copied") {
		t.Errorf("expected byte counter: %q", out)
	}
}

// TestProgressBarAlignment verifies the bar starts at the SAME column regardless
// of the prefix length (the whole point of "bar first, text after"), across the
// byte mode, count mode, and unknown-total mode.
func TestProgressBarAlignment(t *testing.T) {
	l := &Logger{w: &bytes.Buffer{}, color: true}

	short := l.NewProgress("copying x", 1000)
	short.cur = 500
	long := l.NewProgress("copying a-very-long-mailbox-name@example.com", 1000)
	long.cur = 500
	cnt := l.NewCountProgress("analyzing addon1.example", 6, "docroots")
	cnt.cur = 2
	unk := l.NewProgress("copying y", 0)

	barCol := func(s string) int { return strings.IndexByte(s, '[') }
	cs, cl, cc, cu := barCol(short.render()), barCol(long.render()), barCol(cnt.render()), barCol(unk.render())
	if cs <= 0 {
		t.Fatalf("no bar found: %q", short.render())
	}
	if cs != cl || cs != cc || cs != cu {
		t.Errorf("bar column not constant: short=%d long=%d count=%d unknown=%d", cs, cl, cc, cu)
	}

	// The bar must come BEFORE the prefix text now.
	rendered := long.render()
	if i := strings.Index(rendered, "copying"); i > 0 && i < barCol(rendered) {
		t.Errorf("prefix must come AFTER the bar: %q", rendered)
	}
}

func TestCountProgressRender(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{w: &buf, color: true}
	p := l.NewCountProgress("analyzing docroots", 6, "docroots")
	p.SetSuffix("site2.example")
	p.cur = 2 // 2 of 6
	out := p.render()
	if !strings.Contains(out, "2/6 docroots") {
		t.Errorf("count render missing '2/6 docroots': %q", out)
	}
	if !strings.Contains(out, "33%") {
		t.Errorf("count render missing percentage: %q", out)
	}
	if !strings.Contains(out, "site2.example") {
		t.Errorf("count render missing suffix: %q", out)
	}
	// Must NOT format the count as bytes.
	if strings.Contains(out, "B  ") || strings.Contains(out, "2.0 B") {
		t.Errorf("count render must not show bytes: %q", out)
	}
}

func TestInlineProgressRender(t *testing.T) {
	l := &Logger{w: &bytes.Buffer{}, color: true}
	prefix := "     • → info@domain4.example          " // already indented + padded
	p := l.NewInlineProgress(prefix, 40)
	p.cur = 20 // half
	out := p.render()
	// The item block comes FIRST, then the bar, then the data.
	if !strings.HasPrefix(out, prefix) {
		t.Errorf("inline render must start with the item block: %q", out)
	}
	bi := strings.IndexByte(out, '[')
	pi := strings.Index(out, "info@domain4.example")
	if bi < pi {
		t.Errorf("in inline mode the bar must come AFTER the item block: %q", out)
	}
	if !strings.Contains(out, "20 B/40 B") {
		t.Errorf("inline render missing byte data... got %q", out)
	}
	if !strings.Contains(out, "50%") {
		t.Errorf("inline render missing percentage: %q", out)
	}
}

// TestInlineRenderSubModes covers the four inline data sub-modes — the core
// invariant of the uniform "action-left, bar-right → result" layout. In every
// mode the item block comes first and the bar after it.
func TestInlineRenderSubModes(t *testing.T) {
	l := &Logger{w: &bytes.Buffer{}, color: true}
	prefix := "     • → x          "

	// (i) byte + total -> "X/Y GB" + %  (existing NewInlineProgress path)
	p := l.NewInlineProgress(prefix, 1000)
	p.cur = 500
	if out := p.render(); !strings.Contains(out, "500 B/1000 B") || !strings.Contains(out, "50%") {
		t.Errorf("byte+total: %q", out)
	}

	// (ii) count + total -> "N/M unit" + %, NO bytes
	p = l.NewInlineCountProgress(prefix, 6, "docroots")
	p.cur = 2
	out := p.render()
	if !strings.Contains(out, "2/6 docroots") || !strings.Contains(out, "33%") {
		t.Errorf("count+total missing 'N/M unit' + %%: %q", out)
	}
	if strings.Contains(out, " B") {
		t.Errorf("count+total must not show bytes: %q", out)
	}

	// (iii) live counter, unknown total -> "N unit", NO % , NO bytes
	p = l.NewInlineCountProgress(prefix, 0, "files")
	p.cur = 12
	out = p.render()
	if !strings.Contains(out, "12 files") {
		t.Errorf("live counter missing 'N unit': %q", out)
	}
	if strings.Contains(out, "%") || strings.Contains(out, " B") {
		t.Errorf("live counter must have no %% and no bytes: %q", out)
	}

	// (iv) indeterminate (unknown byte total, no unit) -> JUST the bar, NEVER
	// "0 B copied" (the bug this refactor fixes for the DB/mail-0% frame).
	p = l.NewInlineProgress(prefix, 0)
	p.cur = 2048
	out = p.render()
	if strings.Contains(out, "copied") || strings.Contains(out, "%") {
		t.Errorf("indeterminate inline must be bar-only (no 'copied'/%%): %q", out)
	}
	if !strings.Contains(out, "[") {
		t.Errorf("indeterminate inline must still show a bar: %q", out)
	}

	// live bytes (unknown total, byte throughput) -> "X copied"? no — HumanBytes(cur)
	p = l.NewInlineBytesProgress(prefix)
	p.cur = 2048
	out = p.render()
	if !strings.Contains(out, "2.0 KB") {
		t.Errorf("live-bytes must show HumanBytes(cur): %q", out)
	}
	if strings.Contains(out, "%") {
		t.Errorf("live-bytes must have no percentage: %q", out)
	}
}

func TestReplaceWritesFinalLineWhenLive(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{w: &buf, color: true}
	p := l.NewInlineProgress("     • → x", 10)
	p.cur = 5
	p.Replace("     • ✓ x  done")
	out := buf.String()
	if !strings.Contains(out, "     • ✓ x  done\n") {
		t.Errorf("Replace must emit the final line with newline: %q", out)
	}
}

func TestReplaceWritesFinalLineWhenNotLive(t *testing.T) {
	var buf bytes.Buffer
	l := NewTo(&buf, 0) // non-TTY -> bar silent, but result must still appear once
	p := l.NewInlineProgress("     • → x", 10)
	p.Add(5)
	p.Replace("     • ✓ x  done")
	out := buf.String()
	if out != "     • ✓ x  done\n" {
		t.Errorf("non-live Replace must emit ONLY the final line: %q", out)
	}
}

// TestProgressClearsToEndOfLine is the regression for the "leftover ...ts on the
// addon1.example line" bug: a long frame ("analyzing addon1.example ...
// 0/5 docroots") followed by a SHORTER one must not leave the long frame's tail
// behind. We assert every repaint AND Finish emits the clear-to-EOL sequence, so
// clearing never depends on matching the previous frame's length.
func TestProgressClearsToEndOfLine(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{w: &buf, color: true}
	p := l.NewCountProgress("analyzing addon1.example ...", 5, "docroots")

	p.Draw() // long frame at 0/5
	p.cur = 5
	p.SetPrefix("analyzing x ...") // shorter frame
	p.Draw()
	p.Finish()

	out := buf.String()
	// Each \r-prefixed frame must be followed (eventually) by the EOL clear.
	if strings.Count(out, clearEOL) < 3 {
		t.Errorf("expected clear-to-EOL after each draw and on Finish, got %d in %q",
			strings.Count(out, clearEOL), out)
	}
	// The final visible state (after the last \r) must be empty/cleared: nothing
	// from the long frame may survive past the Finish clear.
	if last := out[strings.LastIndex(out, "\r"):]; strings.Contains(last, "docroots") {
		t.Errorf("Finish left bar text on screen: %q", last)
	}
}

// TestReplaceClearsLongAnimatedFrame is the Replace-path counterpart of the
// clear-to-EOL regression: the apply steps (mailboxes/webfiles/dbs) animate a
// possibly long inline frame ("→ info@x  [####] 90% 11.9/12.9 GB batch 8/8")
// and then Replace it with a result line that may be SHORTER. The long frame's
// tail must not survive next to the result.
func TestReplaceClearsLongAnimatedFrame(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{w: &buf, color: true}
	p := l.NewInlineProgress("     • → info@main.example          ", 12_000_000_000)
	p.SetBatch(8, 8)
	p.cur = 11_900_000_000
	p.Draw() // long animated frame painted

	p.Replace("     • ✓ info@main.example          synced") // shorter result

	out := buf.String()
	// Replace must emit a clear-to-EOL before the result so the long frame's tail
	// (the byte counter / "batch 8/8") cannot remain on the line.
	if !strings.Contains(out, clearEOL) {
		t.Errorf("Replace must clear to end of line, got %q", out)
	}
	// Nothing from the animated frame may appear AFTER the final result text.
	final := out[strings.LastIndex(out, "synced"):]
	if strings.Contains(final, "batch") || strings.Contains(final, "GB") {
		t.Errorf("animated frame tail leaked past the result: %q", final)
	}
}

// TestIndeterminateBarMoves locks in the unknown-total bar fix: instead of a
// permanently empty (all-dots) track, the bar shows a short block that bounces
// across a fixed width, advancing one column per frame. This is what makes the
// step-4 source web-file row visibly alive while it counts files.
func TestIndeterminateBarMoves(t *testing.T) {
	const w = 16
	prev := indeterminateBar(w, 0)
	if len(prev) != w {
		t.Fatalf("bar width = %d, want %d (%q)", len(prev), w, prev)
	}
	if !strings.Contains(prev, "#") {
		t.Errorf("unknown-total bar must show a moving block, got all dots: %q", prev)
	}
	if strings.IndexByte(prev, '#') != 0 {
		t.Errorf("frame 0 must paint the block at the left edge: %q", prev)
	}
	// Across a full bounce period the block must occupy at least two distinct
	// positions (it actually moves) and never overflow the width.
	positions := map[int]bool{}
	for f := int64(0); f < 64; f++ {
		s := indeterminateBar(w, f)
		if len(s) != w {
			t.Fatalf("frame %d width = %d, want %d (%q)", f, len(s), w, s)
		}
		positions[strings.IndexByte(s, '#')] = true
	}
	if len(positions) < 2 {
		t.Errorf("block never moved across frames: positions=%v", positions)
	}
	// A negative frame (shouldn't happen, but the counter is int64) must not panic
	// or index out of range.
	if got := indeterminateBar(w, -3); len(got) != w {
		t.Errorf("negative frame width = %d, want %d (%q)", len(got), w, got)
	}
}

// TestLiveCounterRendersCountFromCur is the regression for the step-4 "phantom
// 0 files" duplication: the live file count must come from the bar's counter
// (cur) and render as a single "N files", not a "0 files" from cur PLUS the real
// count parked in the suffix slot.
func TestLiveCounterRendersCountFromCur(t *testing.T) {
	l := &Logger{w: &bytes.Buffer{}, color: true}
	p := l.NewInlineCountProgress("     • → site.example          ", 0, "files")
	p.cur = 142 // as Set(142) would leave it
	out := p.render()
	if !strings.Contains(out, "142 files") {
		t.Errorf("live counter must render the count from cur: %q", out)
	}
	if strings.Contains(out, "0 files") {
		t.Errorf("phantom '0 files' must be gone: %q", out)
	}
}

// TestBarOnlyRowSuffixNoPhantom locks in the apply/verify row fix: with unit ""
// the unknown-total row is bar-only, so a suffix carrying both the phase label and
// the count ("src N entries") renders alone — no phantom "0 entries" from cur.
func TestBarOnlyRowSuffixNoPhantom(t *testing.T) {
	l := &Logger{w: &bytes.Buffer{}, color: true}
	p := l.NewInlineCountProgress("     • → site.example          ", 0, "") // unit ""
	p.SetSuffix("src 142 entries")
	out := p.render()
	if !strings.Contains(out, "src 142 entries") {
		t.Errorf("bar-only row must show the suffix: %q", out)
	}
	if strings.Contains(out, "0 entries") {
		t.Errorf("phantom '0 entries' must not appear: %q", out)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		512:           "512 B",
		1024:          "1.0 KB",
		1536:          "1.5 KB",
		1048576:       "1.0 MB",
		1610612736:    "1.5 GB",
		1099511627776: "1.0 TB",
	}
	for in, want := range cases {
		if got := HumanBytes(in); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestProgressNonLiveUnderDebug(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{w: &buf, color: true} // color on -> would normally animate

	SetDebug(false)
	if p := l.NewProgress("x", 100); !p.live {
		t.Error("color on + debug off: progress must animate (live)")
	}
	SetDebug(true)
	defer SetDebug(false)
	// Under --log-level debug the bar's \r repaints corrupt a merged 2>&1 capture,
	// so it must fall back to non-live (final result line only).
	if p := l.NewProgress("x", 100); p.live {
		t.Error("under debug, progress must be non-live to avoid corrupting merged stdout+stderr")
	}
}
