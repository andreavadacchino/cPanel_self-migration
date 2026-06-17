package sshx

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestSingleQuoteEscape(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"plain", "plain"},
		{"$6$abc$def", "$6$abc$def"}, // dollars are literal inside single quotes
		{"it's", `it'\''s`},
		{"a'b'c", `a'\''b'\''c`},
	}
	for _, c := range cases {
		if got := SingleQuoteEscape(c.in); got != c.want {
			t.Errorf("SingleQuoteEscape(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestWithEnvFormAndEscaping(t *testing.T) {
	// Single key -> one export prefix, command body untouched.
	got := WithEnv(`cd "$HOME/$REL" && tar -c`, map[string]string{"REL": "mail/dom.it/info"})
	want := `export REL='mail/dom.it/info'; cd "$HOME/$REL" && tar -c`
	if got != want {
		t.Errorf("WithEnv =\n  %q\nwant\n  %q", got, want)
	}

	// A value containing a single quote must be escaped as '\'' so it stays one
	// shell token — the whole point of not interpolating untrusted names.
	got = WithEnv(`echo "$X"`, map[string]string{"X": "a'b"})
	if !strings.Contains(got, `export X='a'\''b';`) {
		t.Errorf("single quote not escaped: %q", got)
	}

	// Multiple keys are emitted in sorted order (deterministic).
	got = WithEnv(`:`, map[string]string{"B": "2", "A": "1"})
	if got != `export A='1'; export B='2'; :` {
		t.Errorf("keys not sorted/deterministic: %q", got)
	}

	// A DB-style env set, sorted and escaped (the former dbmig canonical case).
	got = WithEnv(`mysql "$DB_NAME"`, map[string]string{"DB_USER": "u", "DB_NAME": "d", "MYSQL_PWD": "a'b"})
	if want := `export DB_NAME='d'; export DB_USER='u'; export MYSQL_PWD='a'\''b'; mysql "$DB_NAME"`; got != want {
		t.Errorf("WithEnv =\n  %q\nwant\n  %q", got, want)
	}

	// Empty env returns the command unchanged.
	if got := WithEnv(`x`, nil); got != "x" {
		t.Errorf("empty env should pass through, got %q", got)
	}
}

// TestWithEnvExpandsSafelyInBash proves end-to-end that a value with shell-special
// characters, passed via WithEnv, expands back to the EXACT original string in a
// real bash — i.e. it cannot break the command or inject anything.
func TestWithEnvExpandsSafelyInBash(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	// A pathological value with quotes, spaces, $, ; and backticks — the kind of
	// thing that, if interpolated, would break or inject. Built with a double-quoted
	// Go string so the backtick and quote are literal characters.
	nasty := "d'o m;$(touch x)/u`ser`"
	script := WithEnv(`printf '%s' "$REL"`, map[string]string{"REL": nasty})
	out, err := exec.Command("bash", "-c", script).Output()
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if string(out) != nasty {
		t.Errorf("expanded value =\n  %q\nwant\n  %q (the value must survive verbatim)", string(out), nasty)
	}
}

// TestValidEnvName (M13): the POSIX env-name shape used to guard shell splicing.
func TestValidEnvName(t *testing.T) {
	for _, n := range []string{"MYSQL_PWD", "DOM", "ARG_0", "_x", "a1_B2"} {
		if !validEnvName(n) {
			t.Errorf("validEnvName(%q) = false, want true", n)
		}
	}
	for _, n := range []string{"", "1abc", "a b", "x;rm", "a-b", "a=b", "a$b", "DOM\n", "../x"} {
		if validEnvName(n) {
			t.Errorf("validEnvName(%q) = true, want false", n)
		}
	}
}

// TestWithEnvValidName: a well-formed key is spliced as an export assignment.
func TestWithEnvValidName(t *testing.T) {
	got := WithEnv("cmd", map[string]string{"DOM": "d.it"})
	if !strings.Contains(got, "export DOM='d.it'; ") {
		t.Errorf("WithEnv = %q", got)
	}
}

// TestWithEnvPanicsOnInvalidName (M13): a malformed key would be command injection if
// spliced into `export <key>=...`, so it must panic (a call-site bug, never data).
func TestWithEnvPanicsOnInvalidName(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("WithEnv did not panic on an invalid env name")
		}
	}()
	_ = WithEnv("bash -s", map[string]string{"x; rm -rf ~": "v"})
}

// TestSecretStdinProloguePanicsOnInvalidName (M13): same guard on the stdin-prologue
// builder (`read -r <key>; export <key>`).
func TestSecretStdinProloguePanicsOnInvalidName(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("secretStdinPrologue did not panic on an invalid env name")
		}
	}()
	_ = secretStdinPrologue("cmd", []string{"BAD NAME"})
}

// TestRunWithInlineEnvPanicsOnInvalidName (M13): the buffered RunScript fallback
// (used when the server rejects Setenv) also splices env keys into `export <k>=...`,
// so it must apply the same name guard. The panic fires before any session work.
func TestRunWithInlineEnvPanicsOnInvalidName(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("runWithInlineEnv did not panic on an invalid env name")
		}
	}()
	c := &Client{}
	_, _ = c.runWithInlineEnv(context.Background(), "bash -s",
		map[string]string{"x; rm -rf ~": "v"}, strings.NewReader("echo hi\n"))
}
