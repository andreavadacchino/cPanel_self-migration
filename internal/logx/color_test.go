package logx

import (
	"bytes"
	"strings"
	"testing"
)

func TestColorDisabledOnBuffer(t *testing.T) {
	var buf bytes.Buffer
	l := NewTo(&buf, 2)
	if l.color {
		t.Fatal("color must be disabled when writing to a buffer")
	}
	l.Step("hello")
	if strings.Contains(buf.String(), "\x1b[") {
		t.Errorf("buffer output must not contain ANSI codes: %q", buf.String())
	}
}

func TestColorWrappingWhenEnabled(t *testing.T) {
	// Force-enable color to test the wrapping helpers independently of TTY.
	l := &Logger{color: true}
	cases := []struct {
		got, code string
	}{
		{l.Green("X"), ansiGreen},
		{l.Red("X"), ansiRed},
		{l.Blue("X"), ansiBlue},
		{l.Yellow("X"), ansiYellow},
	}
	for _, c := range cases {
		want := c.code + "X" + ansiReset
		if c.got != want {
			t.Errorf("got %q, want %q", c.got, want)
		}
	}
}

func TestColorNoopWhenDisabled(t *testing.T) {
	l := &Logger{color: false}
	if l.Green("X") != "X" || l.Red("X") != "X" {
		t.Error("color funcs must be no-ops when color is disabled")
	}
}

func TestStepColoredWhenEnabled(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{w: &buf, total: 3, color: true}
	l.Step("Analyzing")
	out := buf.String()
	if !strings.HasPrefix(out, ansiBlue) || !strings.Contains(out, "[1/3] Analyzing") {
		t.Errorf("step header should be blue and contain the text: %q", out)
	}
}
