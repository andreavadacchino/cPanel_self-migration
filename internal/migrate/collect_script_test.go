package migrate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
)

// Two SIBLING accounts whose names differ only where a regex metacharacter would
// let one match the other: a regex `^john.doe:` (the OLD grep idiom) matches BOTH
// "johnxdoe:" and "john.doe:" — and with -m1 the FIRST line (the sibling) wins.
// The sibling is listed FIRST and carries a different (weak) hash, so a regex
// match is observably wrong.
const (
	siblingHash = "$1$abcdefgh$0123456789abcdefABCDEF01"          // johnxdoe (WRONG for john.doe)
	ownHash     = "$6$saltsaltsalt$Xq.n0Ro7PzzzZZ/longhashvalue0" // john.doe (the correct one)
)

func writeSrcAccounts(t *testing.T, home, dom string) {
	t.Helper()
	dir := filepath.Join(home, "etc", dom)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// passwd: the authoritative active list, one account per line (sibling FIRST).
	passwd := "johnxdoe:x:1000:1000::/home:/bin/false\n" +
		"john.doe:x:1001:1001::/home:/bin/false\n"
	if err := os.WriteFile(filepath.Join(dir, "passwd"), []byte(passwd), 0o644); err != nil {
		t.Fatal(err)
	}
	// shadow: sibling FIRST so a regex `^john.doe:` + -m1 would grab its hash.
	shadow := "johnxdoe:" + siblingHash + ":19000:0:99999:7:::\n" +
		"john.doe:" + ownHash + ":19000:0:99999:7:::\n"
	if err := os.WriteFile(filepath.Join(dir, "shadow"), []byte(shadow), 0o600); err != nil {
		t.Fatal(err)
	}
}

// runScriptHome runs script via `bash -s` with HOME pointed at home (read-only).
func runScriptHome(t *testing.T, home, script string) string {
	t.Helper()
	return runScriptHomeEnv(t, home, script)
}

func runScriptHomeEnv(t *testing.T, home, script string, extraEnv ...string) string {
	t.Helper()
	out, err := runScriptHomeEnvErr(t, home, script, extraEnv...)
	if err != nil {
		t.Fatalf("run script: %v\n%s", err, out)
	}
	return out
}

func runScriptHomeEnvErr(t *testing.T, home, script string, extraEnv ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "-s")
	cmd.Env = appendEnv(os.Environ(), "HOME="+home)
	for _, kv := range extraEnv {
		cmd.Env = appendEnv(cmd.Env, kv)
	}
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func runScriptHomeAsNobody(t *testing.T, home, script string) (string, error) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("requires root to run the script as nobody")
	}
	sshtest.RequireTools(t, "runuser", "env", "bash", "id")
	if err := exec.Command("id", "-u", "nobody").Run(); err != nil {
		t.Skip("nobody user not available")
	}
	probe, err := exec.Command("runuser", "-m", "-u", "nobody", "--", "id", "-u").CombinedOutput()
	if err != nil {
		t.Skipf("runuser cannot execute as nobody: %v\n%s", err, probe)
	}
	if strings.TrimSpace(string(probe)) == "0" {
		t.Skip("runuser did not drop privileges")
	}
	if err := os.Chmod(home, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("runuser", "-m", "-u", "nobody", "--", "bash", "-c", script)
	cmd.Env = appendEnv(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func skipIfReadableAsNobody(t *testing.T, path string) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("requires root to check readability as nobody")
	}
	sshtest.RequireTools(t, "runuser", "test")
	out, err := exec.Command("runuser", "-m", "-u", "nobody", "--", "test", "-r", path).CombinedOutput()
	if err == nil {
		t.Skipf("%s is readable as nobody in this environment", path)
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return
	}
	t.Skipf("cannot check readability as nobody: %v\n%s", err, out)
}

func chmodDirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func appendEnv(env []string, kv string) []string {
	key, _, ok := strings.Cut(kv, "=")
	if !ok {
		return append(env, kv)
	}
	prefix := key + "="
	for i, cur := range env {
		if strings.HasPrefix(cur, prefix) {
			out := append([]string(nil), env...)
			out[i] = kv
			return out
		}
	}
	return append(env, kv)
}

// TestExactMatchHelpersVsRegexBug proves, in one place, both the vulnerability and
// the fix: the OLD regex grep grabs the SIBLING's hash for a dotted local part,
// while the shared awk helpers (used by both source scripts) match the exact field.
func TestExactMatchHelpersVsRegexBug(t *testing.T) {
	sshtest.RequireTools(t, "bash", "awk", "grep")
	home := t.TempDir()
	writeSrcAccounts(t, home, "dom.it")

	out := runScriptHome(t, home, `set -u
`+exactMatchHelpers+`
SHADOW="$HOME/etc/dom.it/shadow"
echo "field2:$(field2_exact "$SHADOW" john.doe)"
echo "line:$(line_exact "$SHADOW" john.doe)"
if has_user_exact "$SHADOW" john.doe; then echo "has:yes"; else echo "has:no"; fi
if has_user_exact "$SHADOW" ghost; then echo "ghost:yes"; else echo "ghost:no"; fi
echo "buggygrep:$(grep -m1 "^john.doe:" "$SHADOW" | cut -d: -f2)"
`)

	got := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if i := strings.IndexByte(l, ':'); i >= 0 {
			got[l[:i]] = l[i+1:]
		}
	}

	// The fix: exact field-1 match returns john.doe's OWN hash.
	if got["field2"] != ownHash {
		t.Errorf("field2_exact(john.doe) = %q, want its own %q (not the sibling)", got["field2"], ownHash)
	}
	if !strings.HasPrefix(got["line"], "john.doe:") {
		t.Errorf("line_exact(john.doe) = %q, want the john.doe line", got["line"])
	}
	if got["has"] != "yes" {
		t.Errorf("has_user_exact(john.doe) = %q, want yes", got["has"])
	}
	if got["ghost"] != "no" {
		t.Errorf("has_user_exact(ghost) = %q, want no (no false match)", got["ghost"])
	}
	// The bug being fixed: the OLD `grep -m1 "^john.doe:"` returns the SIBLING's
	// weak hash. This asserts the vulnerability really exists with a regex grep.
	if got["buggygrep"] != siblingHash {
		t.Fatalf("regex grep should have grabbed the sibling hash %q, got %q", siblingHash, got["buggygrep"])
	}
}

// TestMailboxesScriptExactMatch is the end-to-end proof on the migration-critical
// path: mailboxesScript must migrate each account's OWN crypt hash. Before the fix,
// john.doe would carry the sibling johnxdoe's hash and the migrated account could
// not log in (or would log in with the wrong password).
func TestMailboxesScriptExactMatch(t *testing.T) {
	sshtest.RequireTools(t, "bash", "awk")
	home := t.TempDir()
	writeSrcAccounts(t, home, "dom.it")

	out := runScriptHome(t, home, mailboxesScript)
	got := map[string]string{}
	for _, mb := range parseMailboxes(out) {
		got[mb.User] = mb.Hash
	}
	if got["john.doe"] != ownHash {
		t.Errorf("john.doe migrated hash = %q, want its own %q", got["john.doe"], ownHash)
	}
	if got["johnxdoe"] != siblingHash {
		t.Errorf("johnxdoe migrated hash = %q, want %q", got["johnxdoe"], siblingHash)
	}
}

func TestAnalyzeScriptIncludesActiveAccountWithoutMaildir(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	home := t.TempDir()
	writeSrcAccounts(t, home, "dom.it")
	if err := os.MkdirAll(filepath.Join(home, "mail", "dom.it", "john.doe"), 0o755); err != nil {
		t.Fatal(err)
	}

	out := runScriptHome(t, home, analyzeScript)
	domains := parseAnalysis(out)
	if len(domains) != 1 || domains[0].Name != "dom.it" {
		t.Fatalf("domains = %+v, want only dom.it", domains)
	}
	got := map[string]string{}
	for _, mb := range domains[0].Mailboxes {
		if !mb.Active {
			t.Errorf("%s should be ACTIVE", mb.User)
		}
		got[mb.User] = mb.Scheme
	}
	if got["john.doe"] != "SHA-512" {
		t.Errorf("john.doe scheme = %q, want SHA-512", got["john.doe"])
	}
	if got["johnxdoe"] != "MD5 (weak)" {
		t.Errorf("johnxdoe without Maildir scheme = %q, want MD5 (weak)", got["johnxdoe"])
	}
	if len(got) != 2 {
		t.Errorf("mailboxes = %+v, want exactly john.doe and johnxdoe without duplicates", got)
	}
}

func TestAnalyzeScriptAllowsMissingMailRoot(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	home := t.TempDir()
	writeSrcAccounts(t, home, "dom.it")

	out := runScriptHome(t, home, analyzeScript)
	domains := parseAnalysis(out)
	if len(domains) != 1 || domains[0].Name != "dom.it" {
		t.Fatalf("domains = %+v, want only dom.it", domains)
	}
	got := map[string]bool{}
	for _, mb := range domains[0].Mailboxes {
		got[mb.User] = mb.Active
	}
	for _, user := range []string{"john.doe", "johnxdoe"} {
		if !got[user] {
			t.Errorf("%s should be included as ACTIVE without ~/mail", user)
		}
	}
	if len(got) != 2 {
		t.Errorf("mailboxes = %+v, want exactly the passwd accounts", got)
	}
}

func TestAnalyzeScriptDeduplicatesPasswdOverlay(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	home := t.TempDir()
	writeSrcAccounts(t, home, "dom.it")
	passwd := filepath.Join(home, "etc", "dom.it", "passwd")
	f, err := os.OpenFile(passwd, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("john.doe:x:2001:2001::/home:/bin/false\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	out := runScriptHome(t, home, analyzeScript)
	domains := parseAnalysis(out)
	if len(domains) != 1 || domains[0].Name != "dom.it" {
		t.Fatalf("domains = %+v, want only dom.it", domains)
	}
	counts := map[string]int{}
	for _, mb := range domains[0].Mailboxes {
		counts[mb.User]++
	}
	if counts["john.doe"] != 1 {
		t.Fatalf("john.doe count = %d, want 1 despite duplicate passwd rows", counts["john.doe"])
	}
	if counts["johnxdoe"] != 1 {
		t.Fatalf("johnxdoe count = %d, want 1", counts["johnxdoe"])
	}
	if len(domains[0].Mailboxes) != 2 {
		t.Fatalf("mailboxes = %+v, want exactly 2 unique passwd users", domains[0].Mailboxes)
	}
}

func TestMailboxesScriptDeduplicatesPasswdRows(t *testing.T) {
	sshtest.RequireTools(t, "bash", "awk")
	home := t.TempDir()
	writeSrcAccounts(t, home, "dom.it")
	passwd := filepath.Join(home, "etc", "dom.it", "passwd")
	f, err := os.OpenFile(passwd, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("john.doe:x:2001:2001::/home:/bin/false\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	mbs := parseMailboxes(runScriptHome(t, home, mailboxesScript))
	counts := map[string]int{}
	for _, mb := range mbs {
		counts[mb.User]++
	}
	if counts["john.doe"] != 1 {
		t.Fatalf("john.doe count = %d, want 1 despite duplicate passwd rows", counts["john.doe"])
	}
	if counts["johnxdoe"] != 1 {
		t.Fatalf("johnxdoe count = %d, want 1", counts["johnxdoe"])
	}
	if len(mbs) != 2 {
		t.Fatalf("mailboxes = %+v, want exactly 2 unique passwd users", mbs)
	}
}

func TestAnalyzeScriptFailsUnreadableMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		want string
	}{
		{name: "passwd", file: "passwd", want: "cannot read mail passwd metadata"},
		{name: "shadow", file: "shadow", want: "cannot read mail shadow metadata"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeSrcAccounts(t, home, "dom.it")
			chmodDirs(t, filepath.Dir(home), home, filepath.Join(home, "etc"), filepath.Join(home, "etc", "dom.it"))
			if err := os.Chmod(filepath.Join(home, "etc", "dom.it", tc.file), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.file == "passwd" {
				if err := os.Chmod(filepath.Join(home, "etc", "dom.it", "shadow"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			skipIfReadableAsNobody(t, filepath.Join(home, "etc", "dom.it", tc.file))
			out, err := runScriptHomeAsNobody(t, home, analyzeScript)
			if err == nil {
				t.Fatalf("script succeeded with unreadable %s\n%s", tc.file, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("output = %q, want %q", out, tc.want)
			}
		})
	}
}

func TestMailboxesScriptFailsUnreadableMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		want string
	}{
		{name: "passwd", file: "passwd", want: "cannot read mail passwd metadata"},
		{name: "shadow", file: "shadow", want: "cannot read mail shadow metadata"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeSrcAccounts(t, home, "dom.it")
			chmodDirs(t, filepath.Dir(home), home, filepath.Join(home, "etc"), filepath.Join(home, "etc", "dom.it"))
			if err := os.Chmod(filepath.Join(home, "etc", "dom.it", tc.file), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.file == "passwd" {
				if err := os.Chmod(filepath.Join(home, "etc", "dom.it", "shadow"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			skipIfReadableAsNobody(t, filepath.Join(home, "etc", "dom.it", tc.file))
			out, err := runScriptHomeAsNobody(t, home, mailboxesScript)
			if err == nil {
				t.Fatalf("script succeeded with unreadable %s\n%s", tc.file, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("output = %q, want %q", out, tc.want)
			}
		})
	}
}

func TestAnalyzeScriptSkipsMailboxSymlink(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	home := t.TempDir()
	writeSrcAccounts(t, home, "dom.it")
	mailDom := filepath.Join(home, "mail", "dom.it")
	if err := os.MkdirAll(mailDom, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "elsewhere", "john.doe")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(mailDom, "john.doe")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(mailDom, "orphan")); err != nil {
		t.Fatal(err)
	}

	out := runScriptHome(t, home, analyzeScript)
	domains := parseAnalysis(out)
	if len(domains) != 1 || domains[0].Name != "dom.it" {
		t.Fatalf("domains = %+v, want only dom.it", domains)
	}
	counts := map[string]int{}
	for _, mb := range domains[0].Mailboxes {
		counts[mb.User]++
		if mb.User == "orphan" {
			t.Errorf("symlink orphan mailbox should not be reported: %+v", mb)
		}
	}
	if counts["john.doe"] != 1 {
		t.Errorf("john.doe count = %d, want 1 via passwd overlay only", counts["john.doe"])
	}
	if counts["johnxdoe"] != 1 {
		t.Errorf("johnxdoe count = %d, want 1", counts["johnxdoe"])
	}
	if len(counts) != 2 {
		t.Errorf("mailboxes = %+v, want exactly john.doe and johnxdoe", counts)
	}
}

func TestAnalyzeScriptDoesNotInvokeAwkForDomainScan(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	home := t.TempDir()
	writeSrcAccounts(t, home, "dom.it")
	if err := os.MkdirAll(filepath.Join(home, "mail", "dom.it", "john.doe"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	for _, name := range []string{"awk", "basename", "dirname"} {
		fake := filepath.Join(bin, name)
		body := "#!/bin/sh\necho " + name + "-called >&2\nexit 97\n"
		if err := os.WriteFile(fake, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	out := runScriptHomeEnv(t, home, analyzeScript, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	domains := parseAnalysis(out)
	if len(domains) != 1 || domains[0].Name != "dom.it" {
		t.Fatalf("domains = %+v, want only dom.it", domains)
	}
	if len(domains[0].Mailboxes) != 2 {
		t.Fatalf("mailboxes = %+v, want john.doe and johnxdoe", domains[0].Mailboxes)
	}
}

// chmodPath chmods a single path or fails the test. It registers a cleanup that
// restores the path to a removable mode so t.TempDir's RemoveAll can delete it even
// when the test runs (or skips) as a non-root user. Without the restore, an
// unreadable directory left behind defeats the temp-dir cleanup ("permission
// denied"), which marks the test FAILED instead of SKIPPED on a non-root CI box.
// Cleanups run LIFO, so this restore fires before the RemoveAll registered by the
// earlier t.TempDir call.
func chmodPath(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
}

// writeSrcMaildir creates ~/mail/<dom>/john.doe so the mail-root / mail-domain
// unreadable-directory cases have a maildir to make unreadable.
func writeSrcMaildir(t *testing.T, home, dom string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "mail", dom, "john.doe"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// skipIfListableAsNobody skips when path is BOTH readable (-r) and traversable (-x)
// as nobody, so require_listable would NOT fire — e.g. under uid 0 or an env where
// the permission bits do not apply. It is the directory analogue of
// skipIfReadableAsNobody, which checks only -r and would wrongly skip a mode-0644
// dir (readable but not traversable).
func skipIfListableAsNobody(t *testing.T, path string) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("requires root to check listability as nobody")
	}
	sshtest.RequireTools(t, "runuser", "sh")
	out, err := exec.Command("runuser", "-m", "-u", "nobody", "--",
		"sh", "-c", `test -r "$1" && test -x "$1"`, "sh", path).CombinedOutput()
	if err == nil {
		t.Skipf("%s is readable+traversable as nobody in this environment", path)
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return // not fully listable -> the guard will fire -> the test is meaningful
	}
	t.Skipf("cannot check listability as nobody: %v\n%s", err, out)
}

// A mail/etc DIRECTORY that EXISTS but is unreadable must FAIL LOUDLY, never be
// silently reported empty (Step 2 audit issue #1). Covers all four locations
// analyzeScript touches: the two roots and a per-domain dir under each.
func TestAnalyzeScriptFailsUnreadableDir(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target []string // path under home
	}{
		{"mail-root", []string{"mail"}},
		{"etc-root", []string{"etc"}},
		{"mail-domain", []string{"mail", "dom.it"}},
		{"etc-domain", []string{"etc", "dom.it"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeSrcAccounts(t, home, "dom.it")
			writeSrcMaildir(t, home, "dom.it")
			// shadow readable so the mail-domain case can pass emit_domain's
			// passwd/shadow read and reach the maildir guard (the other cases fail at
			// an earlier directory guard, before any file is read).
			chmodPath(t, filepath.Join(home, "etc", "dom.it", "shadow"), 0o644)
			chmodDirs(t, filepath.Dir(home), home,
				filepath.Join(home, "etc"), filepath.Join(home, "etc", "dom.it"),
				filepath.Join(home, "mail"), filepath.Join(home, "mail", "dom.it"))
			target := filepath.Join(append([]string{home}, tc.target...)...)
			chmodPath(t, target, 0o000)
			skipIfListableAsNobody(t, target)

			out, err := runScriptHomeAsNobody(t, home, analyzeScript)
			if err == nil {
				t.Fatalf("analyzeScript succeeded with unreadable %s\n%s", tc.name, out)
			}
			if !strings.Contains(out, "cannot read mail directory") {
				t.Fatalf("output = %q, want 'cannot read mail directory'", out)
			}
		})
	}
}

// mailboxesScript feeds the Step 9 apply inventory; an unreadable ~/etc or
// ~/etc/<dom> must fail it too, not silently shrink the inventory.
func TestMailboxesScriptFailsUnreadableDir(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target []string
	}{
		{"etc-root", []string{"etc"}},
		{"etc-domain", []string{"etc", "dom.it"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeSrcAccounts(t, home, "dom.it")
			chmodDirs(t, filepath.Dir(home), home,
				filepath.Join(home, "etc"), filepath.Join(home, "etc", "dom.it"))
			target := filepath.Join(append([]string{home}, tc.target...)...)
			chmodPath(t, target, 0o000)
			skipIfListableAsNobody(t, target)

			out, err := runScriptHomeAsNobody(t, home, mailboxesScript)
			if err == nil {
				t.Fatalf("mailboxesScript succeeded with unreadable %s\n%s", tc.name, out)
			}
			if !strings.Contains(out, "cannot read mail directory") {
				t.Fatalf("output = %q, want 'cannot read mail directory'", out)
			}
		})
	}
}

// mailboxesScript never walks ~/mail, so an unreadable ~/mail must NOT make it fail
// — proving the dir-guard coverage is correctly scoped to ~/etc.
func TestMailboxesScriptIgnoresUnreadableMailDir(t *testing.T) {
	home := t.TempDir()
	writeSrcAccounts(t, home, "dom.it")
	writeSrcMaildir(t, home, "dom.it")
	chmodPath(t, filepath.Join(home, "etc", "dom.it", "shadow"), 0o644) // nobody must read it
	chmodDirs(t, filepath.Dir(home), home,
		filepath.Join(home, "etc"), filepath.Join(home, "etc", "dom.it"),
		filepath.Join(home, "mail"), filepath.Join(home, "mail", "dom.it"))
	chmodPath(t, filepath.Join(home, "mail"), 0o000)
	skipIfListableAsNobody(t, filepath.Join(home, "mail"))

	out, err := runScriptHomeAsNobody(t, home, mailboxesScript)
	if err != nil {
		t.Fatalf("mailboxesScript must ignore an unreadable ~/mail (it never walks it): %v\n%s", err, out)
	}
	if mbs := parseMailboxes(out); len(mbs) != 2 {
		t.Fatalf("mailboxes = %+v, want 2 despite unreadable ~/mail", mbs)
	}
}

// Pin BOTH the -r and -x halves of the directory guard at a per-domain ~/etc dir, in
// BOTH scripts. A mode-0644 dir (-r, !-x) is silently dropped from the multi-level
// ~/etc/*/ glob without the -x check; a mode-0311 dir (!-r, -x) cannot be listed
// without the -r check. shadow is made readable (0644) so that a DROPPED -r is caught
// by the script wrongly SUCCEEDING (the domain is emitted), not by the unrelated
// "cannot read mail shadow metadata" error — making the -r pin robust rather than
// incidental to the shadow mode.
func TestScriptsFailUnreadableEtcDomainRX(t *testing.T) {
	for _, sc := range []struct{ name, script string }{
		{"analyze", analyzeScript},
		{"mailboxes", mailboxesScript},
	} {
		for _, mode := range []os.FileMode{0o644, 0o311} {
			t.Run(fmt.Sprintf("%s-%#o", sc.name, mode), func(t *testing.T) {
				home := t.TempDir()
				writeSrcAccounts(t, home, "dom.it")
				chmodPath(t, filepath.Join(home, "etc", "dom.it", "shadow"), 0o644)
				chmodDirs(t, filepath.Dir(home), home,
					filepath.Join(home, "etc"), filepath.Join(home, "etc", "dom.it"))
				target := filepath.Join(home, "etc", "dom.it")
				chmodPath(t, target, mode)
				skipIfListableAsNobody(t, target)

				out, err := runScriptHomeAsNobody(t, home, sc.script)
				if err == nil {
					t.Fatalf("%sScript succeeded with etc-domain mode %#o\n%s", sc.name, mode, out)
				}
				if !strings.Contains(out, "cannot read mail directory") {
					t.Fatalf("%sScript output = %q, want 'cannot read mail directory'", sc.name, out)
				}
			})
		}
	}
}

// The crucial NEGATIVE test: ABSENT roots (a legitimately mail-less account) are a
// CLEAN empty result, NOT a failure — absent must not be conflated with unreadable.
// Runs as the current user (no privilege drop needed; absent is absent for any uid).
func TestScriptsCleanWhenRootsAbsent(t *testing.T) {
	sshtest.RequireTools(t, "bash", "awk")
	home := t.TempDir() // no ~/mail, no ~/etc

	aOut, aErr := runScriptHomeEnvErr(t, home, analyzeScript)
	if aErr != nil {
		t.Fatalf("analyzeScript with absent roots must succeed: %v\n%s", aErr, aOut)
	}
	if strings.Contains(aOut, "cannot read") {
		t.Fatalf("absent roots must not be reported as unreadable: %q", aOut)
	}
	if d := parseAnalysis(aOut); len(d) != 0 {
		t.Fatalf("absent roots -> 0 domains, got %+v", d)
	}

	mOut, mErr := runScriptHomeEnvErr(t, home, mailboxesScript)
	if mErr != nil {
		t.Fatalf("mailboxesScript with absent roots must succeed: %v\n%s", mErr, mOut)
	}
	if strings.Contains(mOut, "cannot read") {
		t.Fatalf("absent roots must not be reported as unreadable: %q", mOut)
	}
	if mb := parseMailboxes(mOut); len(mb) != 0 {
		t.Fatalf("absent roots -> 0 mailboxes, got %+v", mb)
	}
}

// Anti-regression for the exact bug: an unreadable ~/etc/<dom> must NOT surface as a
// clean EMPTY result (the silent-under-migration failure mode), for BOTH scripts.
func TestUnreadableDirIsNotReportedEmpty(t *testing.T) {
	home := t.TempDir()
	writeSrcAccounts(t, home, "dom.it")
	chmodDirs(t, filepath.Dir(home), home,
		filepath.Join(home, "etc"), filepath.Join(home, "etc", "dom.it"))
	target := filepath.Join(home, "etc", "dom.it")
	chmodPath(t, target, 0o000)
	skipIfListableAsNobody(t, target)

	aOut, aErr := runScriptHomeAsNobody(t, home, analyzeScript)
	if aErr == nil && len(parseAnalysis(aOut)) == 0 {
		t.Fatalf("BUG: unreadable etc/dom.it reported as a CLEAN EMPTY analysis:\n%s", aOut)
	}
	mOut, mErr := runScriptHomeAsNobody(t, home, mailboxesScript)
	if mErr == nil && len(parseMailboxes(mOut)) == 0 {
		t.Fatalf("BUG: unreadable etc/dom.it reported as a CLEAN EMPTY inventory:\n%s", mOut)
	}
}

// Privilege-independent guard: both scripts must carry the directory-readability
// guard, so a removed guard is caught even on a box where the as-nobody tests skip
// (no root / no nobody / no runuser).
func TestDirGuardsPresentInBothScripts(t *testing.T) {
	for _, s := range []struct{ name, body string }{
		{"analyzeScript", analyzeScript},
		{"mailboxesScript", mailboxesScript},
	} {
		if !strings.Contains(s.body, "require_listable") {
			t.Errorf("%s must call require_listable (the unreadable-directory guard)", s.name)
		}
		if !strings.Contains(s.body, "cannot read mail directory") {
			t.Errorf("%s must carry the unreadable-directory error message", s.name)
		}
	}
}

// Two MORE clean (no-error) shapes that must NOT be mistaken for unreadable: an
// empty-but-readable root (0755, no children -> the */ glob discards the literal
// pattern), and a readable per-domain ~/etc dir with NO passwd (skipped by the
// [ -f passwd ] guard). Both scripts must succeed, emit the real domain, and not
// emit the no-passwd dir. Runs as the current user (these paths are uid-independent).
func TestScriptsCleanForEmptyRootAndNoPasswdDir(t *testing.T) {
	sshtest.RequireTools(t, "bash", "awk")
	home := t.TempDir()
	writeSrcAccounts(t, home, "dom.it") // a real domain, with passwd
	if err := os.MkdirAll(filepath.Join(home, "mail"), 0o755); err != nil {
		t.Fatal(err) // empty-but-readable ~/mail root (no children)
	}
	if err := os.MkdirAll(filepath.Join(home, "etc", "nopwd.it"), 0o755); err != nil {
		t.Fatal(err) // readable ~/etc/<dom> with NO passwd
	}

	for _, sc := range []struct{ name, script string }{
		{"analyze", analyzeScript},
		{"mailboxes", mailboxesScript},
	} {
		out, err := runScriptHomeEnvErr(t, home, sc.script)
		if err != nil {
			t.Fatalf("%sScript must succeed (empty root + no-passwd dir are clean): %v\n%s", sc.name, err, out)
		}
		if strings.Contains(out, "cannot read") {
			t.Fatalf("%sScript must not report a readable empty/no-passwd path as unreadable: %q", sc.name, out)
		}
		if strings.Contains(out, "nopwd.it") {
			t.Errorf("%sScript emitted nopwd.it (no passwd) which must be skipped: %q", sc.name, out)
		}
		if !strings.Contains(out, "dom.it") {
			t.Errorf("%sScript must still emit the real domain dom.it: %q", sc.name, out)
		}
	}
}
