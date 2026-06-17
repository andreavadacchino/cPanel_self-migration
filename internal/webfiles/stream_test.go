package webfiles

import (
	"strings"
	"testing"
)

// recorder captures hook invocations for assertions.
type recorder struct {
	starts []string       // domains, in Start order
	dones  []doneRec      // Done results, in order
	ticks  map[string]int // domain -> last filesSoFar seen in Tick
}

type doneRec struct {
	domain string
	res    GatherResult
}

func newRecorder() (*recorder, GatherHooks) {
	r := &recorder{ticks: map[string]int{}}
	h := GatherHooks{
		Start: func(idx, total int, dom string) { r.starts = append(r.starts, dom) },
		Tick:  func(idx, total int, dom string, files int) { r.ticks[dom] = files },
		Done:  func(idx, total int, dom string, res GatherResult) { r.dones = append(r.dones, doneRec{dom, res}) },
	}
	return r, h
}

func TestParseGatherStreamHappyPath(t *testing.T) {
	// a.it: 2 files (100+200); b.it: present but empty (END with no sizes);
	// c.it: ABSENT.
	in := "DOC\ta.it\n100\n200\nEND\nDOC\tb.it\nEND\nDOC\tc.it\nABSENT\nALLDONE\n"
	r, hooks := newRecorder()
	res, err := ParseGatherStream(strings.NewReader(in), 3, hooks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := res["a.it"]; got.Bytes != 300 || got.Count != 2 || got.Absent {
		t.Errorf("a.it = %+v, want {300,2,false}", got)
	}
	if got := res["b.it"]; got.Bytes != 0 || got.Count != 0 || got.Absent {
		t.Errorf("b.it (empty) = %+v, want {0,0,false}", got)
	}
	if got := res["c.it"]; !got.Absent {
		t.Errorf("c.it = %+v, want Absent=true", got)
	}
	// Hooks: Start for each, Done for each, in order.
	if want := []string{"a.it", "b.it", "c.it"}; !eqStr(r.starts, want) {
		t.Errorf("starts = %v, want %v", r.starts, want)
	}
	if len(r.dones) != 3 {
		t.Errorf("expected 3 Done, got %d", len(r.dones))
	}
}

// TestParseGatherStreamUnreadable: an UNREADABLE marker finishes the docroot like
// ABSENT (it must NOT leave the frame open) and is reported distinctly from absent
// and from a present-but-empty docroot. The trailing c.it (after locked.it) proves
// UNREADABLE cleared `cur` so the next docroot parses cleanly.
func TestParseGatherStreamUnreadable(t *testing.T) {
	in := "DOC\ta.it\n100\nEND\nDOC\tlocked.it\nUNREADABLE\nDOC\tc.it\nABSENT\nALLDONE\n"
	r, hooks := newRecorder()
	res, err := ParseGatherStream(strings.NewReader(in), 3, hooks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := res["locked.it"]; !got.Unreadable || got.Absent || got.Bytes != 0 || got.Count != 0 {
		t.Errorf("locked.it = %+v, want {Unreadable:true}", got)
	}
	if got := res["c.it"]; !got.Absent || got.Unreadable {
		t.Errorf("c.it = %+v, want Absent only (no cross-contamination)", got)
	}
	if got := res["a.it"]; got.Unreadable || got.Absent || got.Bytes != 100 {
		t.Errorf("a.it = %+v, want a clean present docroot", got)
	}
	if len(r.dones) != 3 {
		t.Errorf("expected 3 Done (UNREADABLE must finish the frame), got %d", len(r.dones))
	}
}

func TestParseGatherStreamTickAndTotals(t *testing.T) {
	// 600 files of size 1 -> Done.Count=600, Bytes=600; Tick must have fired
	// (throttled) at least once with a value <= 600.
	var b strings.Builder
	b.WriteString("DOC\tbig.it\n")
	for i := 0; i < 600; i++ {
		b.WriteString("1\n")
	}
	b.WriteString("END\nALLDONE\n")
	r, hooks := newRecorder()
	res, err := ParseGatherStream(strings.NewReader(b.String()), 1, hooks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := res["big.it"]; got.Count != 600 || got.Bytes != 600 {
		t.Errorf("big.it = %+v, want {600,600}", got)
	}
	if r.ticks["big.it"] == 0 {
		t.Error("Tick never fired for a 600-file docroot")
	}
	if r.ticks["big.it"] > 600 {
		t.Errorf("Tick reported %d files, more than streamed", r.ticks["big.it"])
	}
}

func TestParseGatherStreamTruncatedNoAllDone(t *testing.T) {
	// a.it completes, b.it is left open at EOF (no END, no ALLDONE).
	in := "DOC\ta.it\n10\nEND\nDOC\tb.it\n20\n"
	_, hooks := newRecorder()
	res, err := ParseGatherStream(strings.NewReader(in), 2, hooks)
	if err == nil {
		t.Fatal("expected a truncation error")
	}
	// The error must quantify the loss so an operator knows how many docroots
	// were not measured, not just that "something truncated".
	if !strings.Contains(err.Error(), "1 of 2") {
		t.Errorf("truncation error should report progress (1 of 2 completed), got %q", err)
	}
	if got, ok := res["a.it"]; !ok || got.Count != 1 || got.Bytes != 10 {
		t.Errorf("completed a.it should be present: %+v ok=%v", got, ok)
	}
	if _, ok := res["b.it"]; ok {
		t.Error("open-at-EOF b.it must NOT be recorded")
	}
}

func TestParseGatherStreamReportsStrayLinesOnTruncation(t *testing.T) {
	// A stray non-numeric line inside a.it, then EOF with no ALLDONE: the
	// truncation error must note the stray line (a stream-corruption hint).
	in := "DOC\ta.it\ngarbage\n100\nEND\n"
	_, err := ParseGatherStream(strings.NewReader(in), 1, GatherHooks{})
	if err == nil {
		t.Fatal("expected a truncation error (no ALLDONE)")
	}
	if !strings.Contains(err.Error(), "stray line") {
		t.Errorf("expected the stray-line count in %q", err)
	}
}

func TestParseGatherStreamEmptyAndOnlyAllDone(t *testing.T) {
	// Empty input -> error (no ALLDONE), empty map.
	res, err := ParseGatherStream(strings.NewReader(""), 0, GatherHooks{})
	if err == nil {
		t.Error("empty input should error (no ALLDONE)")
	}
	if len(res) != 0 {
		t.Errorf("empty input map should be empty, got %v", res)
	}
	// Only ALLDONE (zero pairs) -> valid, empty map, nil error.
	res, err = ParseGatherStream(strings.NewReader("ALLDONE\n"), 0, GatherHooks{})
	if err != nil {
		t.Errorf("ALLDONE-only should be nil error, got %v", err)
	}
	if len(res) != 0 {
		t.Errorf("ALLDONE-only map should be empty, got %v", res)
	}
}

func TestParseGatherStreamIgnoresGarbage(t *testing.T) {
	// A non-numeric stray line inside a docroot is tolerated (skipped); numerics
	// still count. It undercounts x.it (only the 50 counts), which the parser warns
	// about on the clean path (out-of-band, not in the returned error).
	in := "DOC\tx.it\nfoo\n50\nEND\nALLDONE\n"
	_, hooks := newRecorder()
	res, err := ParseGatherStream(strings.NewReader(in), 1, hooks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := res["x.it"]; got.Count != 1 || got.Bytes != 50 {
		t.Errorf("x.it = %+v, want {50,1} (in-frame garbage line skipped)", got)
	}
}

// TestParseGatherStreamOutOfFrameNoiseIsHarmless: lines BEFORE the first DOC (e.g. a
// shell rc echo over `bash -s`) are out-of-frame noise — they must not corrupt any
// docroot's totals and the stream is still a clean success.
func TestParseGatherStreamOutOfFrameNoiseIsHarmless(t *testing.T) {
	in := "hello from .bashrc\nDOC\tx.it\n50\n70\nEND\nALLDONE\n"
	res, err := ParseGatherStream(strings.NewReader(in), 1, GatherHooks{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := res["x.it"]; got.Count != 2 || got.Bytes != 120 {
		t.Errorf("x.it = %+v, want {120,2} (out-of-frame noise must not affect totals)", got)
	}
}

func TestGatherAllScriptBodyUsesSharedExcludes(t *testing.T) {
	body := GatherAllScriptBody()
	for _, name := range systemExcludes {
		// Top-level-anchored via -path (not -name, which would prune at every depth).
		if !strings.Contains(body, `-path "$path"/`+name) {
			t.Errorf("script body missing top-level exclude %q:\n%s", name, body)
		}
	}
	for _, want := range []string{"-prune", "-type f", "-printf '%s\\n'", "DOC\\t", "printf 'END\\n'", "printf 'ABSENT\\n'", "printf 'UNREADABLE\\n'", "printf 'ALLDONE\\n'"} {
		if !strings.Contains(body, want) {
			t.Errorf("script body missing %q:\n%s", want, body)
		}
	}
	// Exactly one find (single traversal per docroot).
	if n := strings.Count(body, "find "); n != 1 {
		t.Errorf("script body should run a single find, found %d", n)
	}
}

func TestGatherAllCommandEnvOnly(t *testing.T) {
	cmd := GatherAllCommand([]GatherPair{
		{Domain: "a.it", Path: "/home/u/a"},
		{Domain: "b.it", Path: "/home/u/b x"}, // path with a space
	})
	// DOCROOTS carries tab-joined pairs, newline-separated, inside single quotes,
	// and the command ends with bash -s. Nothing is interpolated into the body.
	want := "export DOCROOTS='a.it\t/home/u/a\nb.it\t/home/u/b x'; bash -s"
	if cmd != want {
		t.Errorf("GatherAllCommand =\n  %q\nwant\n  %q", cmd, want)
	}

	// A single quote in a path must be escaped ('\'') so the export stays safe.
	cmd = GatherAllCommand([]GatherPair{{Domain: "q.it", Path: "/home/u/a'b"}})
	if !strings.Contains(cmd, `'\''`) {
		t.Errorf("single quote in path not escaped: %q", cmd)
	}
}

func eqStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
