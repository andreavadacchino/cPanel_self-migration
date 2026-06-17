package cpanel

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests run the REAL ensureAccountScript via bash to validate the atomic
// shadow rewrite: the live shadow is updated correctly with its permissions
// preserved, and — crucially — a failed awk leaves the live file UNTOUCHED and
// reports ACCTFAIL (the bug: the old `awk backup > $SH` truncated the live shadow
// before awk ran and printed "UPDATED" unconditionally, so a mid-write failure
// corrupted mail auth AND was reported as success).

func requireBashAwk(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "awk", "grep", "mv", "chmod", "rm"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
}

// runEnsureScript runs ensureAccountScript with a minimal env (HOME + the given
// vars). prependPath, if set, is put first on PATH so a stub binary there shadows
// the real one (used to inject a failing awk).
func runEnsureScript(t *testing.T, home string, env map[string]string, prependPath string) string {
	t.Helper()
	path := os.Getenv("PATH")
	if prependPath != "" {
		path = prependPath + string(os.PathListSeparator) + path
	}
	full := []string{"HOME=" + home, "PATH=" + path}
	for k, v := range env {
		full = append(full, k+"="+v)
	}
	cmd := exec.Command("bash", "-s")
	cmd.Stdin = strings.NewReader(ensureAccountScript)
	cmd.Env = full
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("script run error: %v (stderr: %s)", err, errb.String())
	}
	return out.String()
}

func writeShadow(t *testing.T, home, dom, content string) string {
	t.Helper()
	dir := filepath.Join(home, "etc", dom)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "shadow")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// assertOnlyShadow fails if any temp/backup file was left next to the shadow.
func assertOnlyShadow(t *testing.T, shPath string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(shPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "shadow" {
			t.Errorf("leftover file in shadow dir: %s", e.Name())
		}
	}
}

func TestEnsureAccountScriptAtomicUpdate(t *testing.T) {
	requireBashAwk(t)
	home := t.TempDir()
	dom := "domain4.example"
	orig := "user1:$6$s1$h1:19000:0:99999:7:::\n" +
		"homelab:$6$OLDSALT$oldhashval:19000:0:99999:7:::\n" +
		"user2:$6$s2$h2:19000:0:99999:7:::\n"
	shPath := writeShadow(t, home, dom, orig)

	out := runEnsureScript(t, home, map[string]string{
		"DOM": dom, "USER": "homelab", "HASH": "$6$NEWSALT$newhashval",
	}, "")
	if !strings.Contains(out, "UPDATED") {
		t.Errorf("want UPDATED, got %q", out)
	}

	// Only the target user's hash field changed; every other field and line intact.
	got, _ := os.ReadFile(shPath)
	want := "user1:$6$s1$h1:19000:0:99999:7:::\n" +
		"homelab:$6$NEWSALT$newhashval:19000:0:99999:7:::\n" +
		"user2:$6$s2$h2:19000:0:99999:7:::\n"
	if string(got) != want {
		t.Errorf("shadow after update:\n got %q\nwant %q", got, want)
	}
	// Permissions must stay owner-only (the file carries password hashes).
	fi, _ := os.Stat(shPath)
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("shadow perms = %v, want 0600", perm)
	}
	assertOnlyShadow(t, shPath)
}

// withUAPIStub returns a bin dir containing a fake `uapi` that reports success,
// so the create path can be observed end-to-end ("CREATED") in a test.
func withUAPIStub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "uapi"), []byte("#!/bin/sh\necho '{\"status\":1}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// withUAPIStubRaw returns a bin dir with a fake `uapi` that prints the given raw
// stdout verbatim, so the create-path success check can be exercised against
// non-compact (whitespace / pretty-printed) UAPI JSON, not just the one exact
// compact byte sequence the happy-path stub emits.
func withUAPIStubRaw(t *testing.T, output string) string {
	t.Helper()
	dir := t.TempDir()
	// A heredoc preserves the output bytes (incl. newlines) without shell mangling.
	script := "#!/bin/sh\ncat <<'CPSMEOF'\n" + output + "\nCPSMEOF\n"
	if err := os.WriteFile(filepath.Join(dir, "uapi"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestEnsureAccountCreateAcceptsNonCompactUAPIJSON: UAPI success is "status":1, but
// a valid result may carry whitespace around the colon (`"status" : 1`) or be
// pretty-printed across lines. The create-path success check must key off the JSON
// status, not the exact compact byte sequence — otherwise a mailbox that cPanel
// DID create is reported ACCTFAIL, and the mail copy is skipped on the first apply,
// leaving a recoverable-only-by-rerun partial state.
func TestEnsureAccountCreateAcceptsNonCompactUAPIJSON(t *testing.T) {
	requireBashAwk(t)
	cases := []struct {
		name string
		json string
	}{
		{"compact", `{"result":{"status":1}}`},
		{"space around colon", `{"result":{ "status" : 1 }}`},
		{"space after colon", `{"result":{"status": 1}}`},
		{"tab around colon", "{\"result\":{\t\"status\"\t:\t1\t}}"},
		{"pretty printed", "{\n   \"result\" : {\n      \"errors\" : null,\n      \"status\" : 1\n   }\n}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir() // no shadow -> reaches the create path
			out := runEnsureScript(t, home, map[string]string{
				"DOM": "dom.it", "USER": "newuser", "HASH": "$6$x$y",
			}, withUAPIStubRaw(t, tc.json))
			if !strings.Contains(out, "CREATED") || strings.Contains(out, "ACCTFAIL") {
				t.Errorf("non-compact UAPI success must be CREATED, got %q", out)
			}
		})
	}
}

// TestEnsureAccountCreateFailureStaysACCTFAIL: the whitespace-tolerant success match
// must NOT widen to treat a UAPI failure (`"status":0`, or any non-1 status) as a
// created account. A false CREATED would skip the mail copy for a mailbox that does
// not actually exist.
func TestEnsureAccountCreateFailureStaysACCTFAIL(t *testing.T) {
	requireBashAwk(t)
	cases := []struct {
		name string
		json string
	}{
		{"status 0", `{"result":{"status":0,"errors":["add_pop failed"]}}`},
		{"status 0 spaced", `{"result":{ "status" : 0 }}`},
		{"no status field", `{"result":{"errors":["boom"]}}`},
		// A failure whose error PROSE contains an escaped "status":1 must stay
		// ACCTFAIL: JSON escapes the inner quotes (\"status\"), so the anchored
		// quoted-key pattern does not false-match the message text.
		{"escaped status:1 in error", `{"result":{"status":0,"errors":["prior \"status\":1 was stale"]}}`},
		// status:10 must not widen to a match for status==1.
		{"status 10", `{"result":{"status":10}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			out := runEnsureScript(t, home, map[string]string{
				"DOM": "dom.it", "USER": "newuser", "HASH": "$6$x$y",
			}, withUAPIStubRaw(t, tc.json))
			if !strings.Contains(out, "ACCTFAIL") || strings.Contains(out, "CREATED") {
				t.Errorf("UAPI non-success must be ACCTFAIL, got %q", out)
			}
		})
	}
}

// TestEnsureAccountExactMatchAvoidsSiblingFalsePositive: the existence check must
// match the mailbox EXACTLY, not as a regex. A '.' in the local part is a regex
// any-char, so the old `grep "^first.last:"` matched the sibling "first1last" and
// then reported "UPDATED" without ever creating the real account (silent skip).
func TestEnsureAccountExactMatchAvoidsSiblingFalsePositive(t *testing.T) {
	requireBashAwk(t)
	home := t.TempDir()
	dom := "domain4.example"
	orig := "first1last:$6$SIB$h:19000:0:99999:7:::\n" // sibling only; first.last absent
	shPath := writeShadow(t, home, dom, orig)

	out := runEnsureScript(t, home, map[string]string{
		"DOM": dom, "USER": "first.last", "HASH": "$6$NEW$y",
	}, withUAPIStub(t))

	if !strings.Contains(out, "CREATED") || strings.Contains(out, "UPDATED") {
		t.Errorf("absent 'first.last' must be CREATED, not falsely UPDATED via the sibling: got %q", out)
	}
	// The sibling line must be untouched.
	if got, _ := os.ReadFile(shPath); string(got) != orig {
		t.Errorf("sibling shadow changed:\n got %q\nwant %q", got, orig)
	}
}

// TestEnsureAccountExactMatchHandlesRegexCharsInLocalPart: '+' is a regex
// quantifier, so the old `grep "^user+tag:"` did NOT match the literal account and
// sent an EXISTING mailbox down the create path. Exact matching updates it.
func TestEnsureAccountExactMatchHandlesRegexCharsInLocalPart(t *testing.T) {
	requireBashAwk(t)
	home := t.TempDir()
	dom := "domain4.example"
	orig := "user+tag:$6$OLD$h:19000:0:99999:7:::\n"
	shPath := writeShadow(t, home, dom, orig)

	out := runEnsureScript(t, home, map[string]string{
		"DOM": dom, "USER": "user+tag", "HASH": "$6$NEW$y",
	}, withUAPIStub(t))

	if !strings.Contains(out, "UPDATED") {
		t.Errorf("existing 'user+tag' must be UPDATED despite the '+', got %q", out)
	}
	got, _ := os.ReadFile(shPath)
	want := "user+tag:$6$NEW$y:19000:0:99999:7:::\n"
	if string(got) != want {
		t.Errorf("shadow after update:\n got %q\nwant %q", got, want)
	}
}

// TestEnsureAccountRefusesSymlinkedMailboxComponent: the orphan-Maildir rename must
// fail closed if ANY component of ~/mail/<dom>/<user> is a symlink — here the <dom>
// dir symlinks OUTSIDE ~/mail. A bare leaf-only `[ -L "$MD" ]` check would miss it
// (the <user> leaf is a real dir inside the link target), so the rename would operate
// on a directory outside the mailbox tree. The escape target must be untouched.
func TestEnsureAccountRefusesSymlinkedMailboxComponent(t *testing.T) {
	requireBashAwk(t)
	home := t.TempDir()
	evil := t.TempDir()
	orphan := filepath.Join(evil, "info") // the would-be orphan dir, reached via the symlinked <dom>
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "keep.txt"), []byte("DO NOT TOUCH"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "mail"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(evil, filepath.Join(home, "mail", "dom.it")); err != nil { // <dom> dir escapes ~/mail
		t.Fatal(err)
	}
	// No shadow -> account not configured -> reaches the orphan-mailbox block.
	out := runEnsureScript(t, home, map[string]string{
		"DOM": "dom.it", "USER": "info", "HASH": "$6$x$y",
	}, withUAPIStub(t))
	if !strings.Contains(out, "ACCTFAIL") || !strings.Contains(out, "symlink") {
		t.Errorf("a symlinked mailbox path component must be refused (ACCTFAIL symlink), got %q", out)
	}
	if _, err := os.Stat(orphan + "-bak"); err == nil {
		t.Error("the symlinked-out orphan dir was renamed aside (escaped ~/mail)")
	}
	if b, _ := os.ReadFile(filepath.Join(orphan, "keep.txt")); string(b) != "DO NOT TOUCH" {
		t.Errorf("escape target content changed: %q", b)
	}
}

func TestEnsureAccountScriptDoesNotTruncateOnAwkFailure(t *testing.T) {
	requireBashAwk(t)
	home := t.TempDir()
	dom := "domain4.example"
	orig := "homelab:$6$OLDSALT$oldhashval:19000:0:99999:7:::\n"
	shPath := writeShadow(t, home, dom, orig)

	// A failing `awk` earlier on PATH simulates an awk error / a mid-write kill.
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "awk"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	out := runEnsureScript(t, home, map[string]string{
		"DOM": dom, "USER": "homelab", "HASH": "$6$NEWSALT$newhashval",
	}, binDir)

	if !strings.Contains(out, "ACCTFAIL") {
		t.Errorf("awk failure must report ACCTFAIL, got %q", out)
	}
	if strings.Contains(out, "UPDATED") {
		t.Errorf("awk failure must NOT report a false UPDATED, got %q", out)
	}
	// The live shadow must be byte-for-byte intact (never truncated) — the fix.
	got, _ := os.ReadFile(shPath)
	if string(got) != orig {
		t.Errorf("shadow changed despite awk failure:\n got %q\nwant %q", got, orig)
	}
	assertOnlyShadow(t, shPath)
}

// TestEnsureAccountScriptReportsFailureWhenShadowMvFails: the atomic rename of the
// rewritten shadow can fail (read-only mount, immutable flag, ENOSPC). When it does
// the live shadow is unchanged and the user's hash was NOT written, so the script
// must report ACCTFAIL — never a false UPDATED, which would silently lock the user
// out (the bug: the old script printed UPDATED unconditionally after an unchecked
// `mv`). A stub `mv` earlier on PATH simulates the failing rename.
func TestEnsureAccountScriptReportsFailureWhenShadowMvFails(t *testing.T) {
	requireBashAwk(t)
	home := t.TempDir()
	dom := "domain4.example"
	orig := "homelab:$6$OLDSALT$oldhashval:19000:0:99999:7:::\n"
	shPath := writeShadow(t, home, dom, orig)

	// A failing `mv` earlier on PATH makes the rename that replaces the live shadow
	// fail, without disturbing awk/chmod/rm (which the update path also runs).
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "mv"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	out := runEnsureScript(t, home, map[string]string{
		"DOM": dom, "USER": "homelab", "HASH": "$6$NEWSALT$newhashval",
	}, binDir)

	if !strings.Contains(out, "ACCTFAIL") {
		t.Errorf("a failed shadow rename must report ACCTFAIL, got %q", out)
	}
	if strings.Contains(out, "UPDATED") {
		t.Errorf("a failed shadow rename must NOT report a false UPDATED, got %q", out)
	}
	// The live shadow is unchanged: the new hash was never written.
	got, _ := os.ReadFile(shPath)
	if string(got) != orig {
		t.Errorf("shadow changed despite a failed rename:\n got %q\nwant %q", got, orig)
	}
	// And the orphan temp file is cleaned up.
	assertOnlyShadow(t, shPath)
}

// TestEnsureAccountScriptKeepsHashOutOfAwkArgv (A2): the shadow-rewrite awk must
// read the password hash from ENVIRON, never from a `-v h=` argument, so the hash
// is not visible in awk's /proc/<pid>/cmdline. A stub awk records its argv and then
// execs the real awk: the captured argv must NOT contain the hash, yet the rewrite
// must still succeed (proving ENVIRON delivered the value).
func TestEnsureAccountScriptKeepsHashOutOfAwkArgv(t *testing.T) {
	requireBashAwk(t)
	realAwk, err := exec.LookPath("awk")
	if err != nil {
		t.Skip("awk not found")
	}
	home := t.TempDir()
	dom := "domain4.example"
	shPath := writeShadow(t, home, dom, "homelab:$6$OLD$old:19000:0:99999:7:::\n")

	capture := filepath.Join(t.TempDir(), "awk_argv")
	stubDir := t.TempDir()
	stub := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done >> \"" + capture + "\"\nexec " + realAwk + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(stubDir, "awk"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	const hash = "$6$SECRETSALT$SECRETHASHvalue1234567890"
	out := runEnsureScript(t, home, map[string]string{
		"DOM": dom, "USER": "homelab", "HASH": hash,
	}, stubDir)
	if !strings.Contains(out, "UPDATED") {
		t.Fatalf("rewrite must still succeed via ENVIRON, got %q", out)
	}
	if got, _ := os.ReadFile(shPath); !strings.Contains(string(got), hash) {
		t.Errorf("ENVIRON hash was not written into the shadow:\n%s", got)
	}
	argv, _ := os.ReadFile(capture)
	if len(argv) == 0 {
		t.Fatal("stub awk captured no argv (was it invoked?)")
	}
	if strings.Contains(string(argv), hash) {
		t.Errorf("password hash leaked into awk argv:\n%s", argv)
	}
}

// TestEnsureAccountScriptAwkUsesEnvironNotArgv (A2): static guard that the script
// reads the hash via ENVIRON and never via a `-v h=` awk argument.
func TestEnsureAccountScriptAwkUsesEnvironNotArgv(t *testing.T) {
	if !strings.Contains(ensureAccountScript, `ENVIRON["HASH"]`) {
		t.Error(`shadow-rewrite awk must read the hash via ENVIRON["HASH"]`)
	}
	if strings.Contains(ensureAccountScript, "-v h=") {
		t.Error(`awk must not receive the hash as a "-v h=" argument (argv leak)`)
	}
}
