package logx

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

// withDebug redirects the package-global Debug to a buffer with debug enabled,
// restoring the previous sink + state on cleanup. It is the testability seam the
// facility previously lacked (Debug used to write to os.Stderr unconditionally).
func withDebug(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	debugMu.Lock()
	prev := debugOut
	debugOut = buf
	debugMu.Unlock()
	SetDebug(true)
	t.Cleanup(func() {
		SetDebug(false)
		debugMu.Lock()
		debugOut = prev
		debugMu.Unlock()
	})
	return buf
}

func TestDebugDisabledEmitsNothing(t *testing.T) {
	buf := withDebug(t)
	SetDebug(false)
	Debug("must not appear %d", 1)
	if buf.Len() != 0 {
		t.Fatalf("Debug wrote while disabled: %q", buf.String())
	}
}

func TestWarnEmitsEvenWhenDebugDisabled(t *testing.T) {
	buf := withDebug(t)
	SetDebug(false) // the whole point: Warn must survive at the default info level
	Warn("host key MISMATCH for %s", "example.com")
	got := buf.String()
	if !strings.HasPrefix(got, "  [warn] ") {
		t.Errorf("missing [warn] marker in %q", got)
	}
	if !strings.Contains(got, "host key MISMATCH for example.com") {
		t.Errorf("missing formatted message in %q", got)
	}
}

func TestDebugEnabledEmitsMessageWithElapsedMarker(t *testing.T) {
	buf := withDebug(t)
	Debug("hello %s", "world")
	got := buf.String()
	if !strings.Contains(got, "hello world") {
		t.Errorf("missing formatted message in %q", got)
	}
	// The doc-comment promises a "+<elapsed>" timing marker; prove it is really
	// emitted (it was entirely absent before this fix).
	if !strings.HasPrefix(got, "  [debug +") {
		t.Errorf("missing [debug +<elapsed>] marker in %q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("missing trailing newline in %q", got)
	}
}

func TestDiagnosticsHandleSinkWriteFailure(t *testing.T) {
	debugMu.Lock()
	prev := debugOut
	debugOut = errWriter{}
	debugMu.Unlock()
	SetDebug(true)
	t.Cleanup(func() {
		SetDebug(false)
		debugMu.Lock()
		debugOut = prev
		debugMu.Unlock()
	})

	Debug("debug message")
	Warn("warn message")
}

func TestStepCounter(t *testing.T) {
	var buf bytes.Buffer
	l := NewTo(&buf, 3)
	l.Step("first")
	l.Step("second")
	want := "[1/3] first\n[2/3] second\n"
	if buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
}

func TestSkipAdvancesCounter(t *testing.T) {
	var buf bytes.Buffer
	l := NewTo(&buf, 7)
	l.Step("first")             // [1/7]
	l.Skip("dry-run", "second") // [2/7] ... (skipped: dry-run)
	l.Step("third")             // [3/7]
	want := "[1/7] first\n" +
		"[2/7] second (skipped: dry-run)\n" +
		"[3/7] third\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

func TestStepNoCounter(t *testing.T) {
	var buf bytes.Buffer
	l := NewTo(&buf, 0)
	l.Step("only")
	if got, want := buf.String(), "==> only\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPrefixes(t *testing.T) {
	var buf bytes.Buffer
	l := NewTo(&buf, 0)
	l.Detail("d %d", 1)
	l.Item("i")
	l.Warn("w")
	l.OK("o")
	l.Plain("p")
	// All under-step lines share a 5-space indent so the ->/•/!/✓ markers align
	// in the same column.
	want := "     -> d 1\n     • i\n     ! w\n     ✓ o\np\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}
