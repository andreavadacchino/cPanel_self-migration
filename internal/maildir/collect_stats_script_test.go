package maildir

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
)

// These tests drive the read-only stat/digest SHELL scripts (boxStatsScript,
// folderStatsScript, messageDigestsScript) directly via local `bash -s`, so the
// fail-closed behavior can be pinned without an SSH round-trip — including the
// permission path as an unprivileged user (which a root CI box cannot exercise over
// the in-process SSH server, since that server runs as the test's own uid).

// envWith returns base with the given KEY=VALUE overrides applied, replacing any
// existing entry for that key (so a child bash sees exactly the intended value, with
// no duplicate-key ambiguity).
func envWith(base []string, overrides map[string]string) []string {
	out := make([]string, 0, len(base)+len(overrides))
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	for _, e := range base {
		if i := strings.IndexByte(e, '='); i >= 0 {
			if _, ok := overrides[e[:i]]; ok {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// runStatsScript runs a script const via local bash -s with the given env, returning
// combined output and the process error (non-nil on a non-zero exit).
func runStatsScript(t *testing.T, script string, env []string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "-s")
	cmd.Env = env
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// toolDir builds a PATH directory holding symlinks to exactly the named real tools
// (so a script run with PATH=<this> can see those tools and nothing else). Skips the
// test if any tool is absent.
func toolDir(t *testing.T, tools ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range tools {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not available", tool)
		}
		if err := os.Symlink(p, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func baseEnv(home string) []string {
	return envWith(os.Environ(), map[string]string{"HOME": home, "DOM": "d.it", "USER": "u"})
}

// readEnv wires GuardRoot() into the GUARD_ROOT env the read scripts test; without it
// the guard is off (source behavior).
func TestReadEnvGuardRoot(t *testing.T) {
	if e := readEnv("d", "u", nil); e["GUARD_ROOT"] != "" || e["DOM"] != "d" || e["USER"] != "u" {
		t.Errorf("default readEnv = %v, want DOM/USER set and no GUARD_ROOT", e)
	}
	if e := readEnv("d", "u", []ReadOption{GuardRoot()}); e["GUARD_ROOT"] != "1" {
		t.Errorf("GuardRoot() must set GUARD_ROOT=1, got %v", e)
	}
}

// With GuardRoot (GUARD_ROOT=1) a mailbox root that is a SYMLINK must be REJECTED by the
// read scripts — the same containment guard_mailbox_path enforces for the extract — so a
// DESTINATION read cannot follow a link the copy would refuse to write to. Without the
// guard (a SOURCE read) the symlink is followed as before. An ABSENT guarded root is
// tolerated (a fresh destination), so the guard rejects only the symlink, not absence.
func TestStatsScriptGuardRejectsSymlinkRoot(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "wc", "realpath", "sha256sum")
	home := t.TempDir()
	// A real mailbox elsewhere under ~/mail, with the <user> root a SYMLINK to it.
	realBox := filepath.Join(home, "mail", "d.it", "real")
	for _, q := range []string{"cur", "new"} {
		if err := os.MkdirAll(filepath.Join(realBox, q), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(realBox, filepath.Join(home, "mail", "d.it", "u")); err != nil {
		t.Fatal(err)
	}
	guardedEnv := envWith(baseEnv(home), map[string]string{"GUARD_ROOT": "1"})
	for _, sc := range []struct {
		name   string
		script string
	}{
		{"box", boxStatsScript}, {"folder", folderStatsScript}, {"set", messageSetScript},
		{"digests", messageDigestsScript},
	} {
		// Unguarded (source): the symlink root is followed, the read succeeds.
		if _, err := runStatsScript(t, sc.script, baseEnv(home)); err != nil {
			t.Errorf("%s unguarded must follow the symlinked root (source behavior): %v", sc.name, err)
		}
		// Guarded (dest): the symlink root is rejected (non-zero exit).
		if out, err := runStatsScript(t, sc.script, guardedEnv); err == nil {
			t.Errorf("%s guarded must REJECT a symlinked mailbox root, got success:\n%s", sc.name, out)
		}
	}
	// A guarded read of an ABSENT root (no symlink, never created) must NOT error.
	absent := envWith(baseEnv(home), map[string]string{"GUARD_ROOT": "1", "USER": "ghost"})
	if out, err := runStatsScript(t, boxStatsScript, absent); err != nil {
		t.Errorf("guarded read of an absent (fresh) dest root must succeed, got error: %v\n%s", err, out)
	}
}

// boxStatsScript must FAIL (not report empty) when the mailbox root exists but is not
// a directory — the "present but not a normal traversable directory" guard, which is
// uid-independent (it does not depend on permission bits root would bypass).
func TestBoxStatsScriptFailsOnNonDirRoot(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "wc")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "mail", "d.it"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The mailbox path is a regular FILE, not a maildir directory.
	if err := os.WriteFile(filepath.Join(home, "mail", "d.it", "u"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runStatsScript(t, boxStatsScript, baseEnv(home))
	if err == nil {
		t.Errorf("boxStatsScript must fail on a non-directory mailbox root, got success:\n%s", out)
	}
}

// folderStatsScript must FAIL when a folder's cur queue exists but is not a directory.
func TestFolderStatsScriptFailsOnNonDirCur(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "wc")
	home := t.TempDir()
	box := filepath.Join(home, "mail", "d.it", "u")
	if err := os.MkdirAll(box, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(box, "cur"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runStatsScript(t, folderStatsScript, baseEnv(home))
	if err == nil {
		t.Errorf("folderStatsScript must fail when cur is not a directory, got success:\n%s", out)
	}
}

// boxStatsScript must FAIL when the dovecot-uidlist EXISTS but is not a readable
// regular file (here a directory in its place), rather than silently emitting an
// empty UIDVALIDITY — a present-but-unreadable uidlist is corrupt evidence. (Using a
// non-regular-file makes this uid-independent: the `[ -f ]` guard fails for root too.)
func TestBoxStatsScriptFailsOnUnreadableUIDList(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "wc")
	home := t.TempDir()
	box := filepath.Join(home, "mail", "d.it", "u")
	if err := os.MkdirAll(filepath.Join(box, "cur"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(box, "cur", "1.M1.host:2,S"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// dovecot-uidlist is a DIRECTORY, not a regular file -> not readable as a uidlist.
	if err := os.MkdirAll(filepath.Join(box, "dovecot-uidlist"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := runStatsScript(t, boxStatsScript, baseEnv(home))
	if err == nil {
		t.Errorf("boxStatsScript must fail when dovecot-uidlist is not a readable file, got success:\n%s", out)
	}
}

// messageDigestsScript must FAIL (-> deep check UNVERIFIED) when sha256sum is not on
// PATH, rather than tagging every message ?unreadable and having that read as
// corruption.
func TestMessageDigestsScriptFailsWithoutSha256sum(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find")
	home := t.TempDir()
	box := filepath.Join(home, "mail", "d.it", "u", "cur")
	if err := os.MkdirAll(box, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(box, "1.M1.host:2,S"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	// PATH has find/cut/basename but deliberately NOT sha256sum.
	path := toolDir(t, "find", "cut", "basename")
	env := envWith(baseEnv(home), map[string]string{"PATH": path})
	out, err := runStatsScript(t, messageDigestsScript, env)
	if err == nil {
		t.Errorf("messageDigestsScript must fail when sha256sum is unavailable, got success:\n%s", out)
	}
	if !strings.Contains(out, "sha256sum not available") {
		t.Errorf("expected a clear 'sha256sum not available' message, got:\n%s", out)
	}
}

// A single message whose body cannot be hashed gets the ?unreadable sentinel (so the
// deep check surfaces it as UNVERIFIED per-message), while the helper as a whole still
// succeeds — one bad message must not abort the digest of the rest.
func TestMessageDigestsScriptMarksUnhashableMessage(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "cut", "basename")
	home := t.TempDir()
	box := filepath.Join(home, "mail", "d.it", "u", "cur")
	if err := os.MkdirAll(box, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(box, "1.M1.host:2,S"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	// PATH has real find/cut/basename plus a `sha256sum` shim that always fails — so
	// `command -v sha256sum` still succeeds (no whole-helper exit 16), but each per-file
	// hash comes back empty and must become the ?unreadable sentinel.
	path := toolDir(t, "find", "cut", "basename")
	if err := os.WriteFile(filepath.Join(path, "sha256sum"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := envWith(baseEnv(home), map[string]string{"PATH": path})
	out, err := runStatsScript(t, messageDigestsScript, env)
	if err != nil {
		t.Fatalf("messageDigestsScript must SUCCEED (one bad hash is per-message, not fatal): %v\n%s", err, out)
	}
	if !strings.Contains(out, "?unreadable") {
		t.Errorf("an unhashable message must be emitted with the ?unreadable sentinel, got:\n%s", out)
	}
}

// The stats scripts must TOLERATE a transient find error (the live-source folder-vanish
// race) rather than turning it into a hard failure: a `find` that exits non-zero must
// NOT fail the script — readability is already established by require_listable, so a
// non-zero find means a benign mid-walk change, counted as best effort.
func TestFolderStatsScriptToleratesFindError(t *testing.T) {
	sshtest.RequireTools(t, "bash", "wc", "head", "awk")
	home := t.TempDir()
	box := filepath.Join(home, "mail", "d.it", "u", "cur")
	if err := os.MkdirAll(box, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(box, "1.M1.host:2,S"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// PATH has real wc/head/awk plus a `find` shim that always fails.
	path := toolDir(t, "wc", "head", "awk")
	if err := os.WriteFile(filepath.Join(path, "find"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := envWith(baseEnv(home), map[string]string{"PATH": path})
	out, err := runStatsScript(t, folderStatsScript, env)
	if err != nil {
		t.Errorf("folderStatsScript must tolerate a transient find error, not hard-fail: %v\n%s", err, out)
	}
}
