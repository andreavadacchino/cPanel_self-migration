package webfiles

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
)

// contentDigestOf builds a docroot tree via build() under a fresh temp HOME, runs the
// REAL DocrootContentDigest over the in-process bash SSH server, and returns the digest.
func contentDigestOf(t *testing.T, build func(t *testing.T, root string)) string {
	t.Helper()
	home := t.TempDir()
	doc := filepath.Join(home, "public_html", "site")
	if err := os.MkdirAll(doc, 0o755); err != nil {
		t.Fatal(err)
	}
	build(t, doc)
	c := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer c.Close()
	_, _, d, ok, unread, err := DocrootContentDigest(context.Background(), c, doc)
	if err != nil || !ok || unread {
		t.Fatalf("DocrootContentDigest: ok=%v unreadable=%v err=%v", ok, unread, err)
	}
	if d == "" {
		t.Fatal("empty content digest for a present readable docroot")
	}
	return d
}

func TestDocrootContentDigestBehavior(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "sort", "sha256sum", "awk")
	d0 := contentDigestOf(t, base)

	// Identical tree -> identical digest (determinism, traversal-order independent).
	if contentDigestOf(t, base) != d0 {
		t.Error("identical trees must content-digest equally")
	}

	// THE POINT (V01/V28/V34): same-name/same-size/DIFFERENT content MUST flip the
	// content digest. This is exactly the case the namelist DocrootDigest CANNOT see
	// (TestDocrootDigestBehavior asserts that one stays unchanged here) — so the two
	// digests are genuinely different checks.
	if contentDigestOf(t, func(t *testing.T, r string) {
		base(t, r)
		write(t, filepath.Join(r, "index.html"), "world") // same name, same 5 bytes, different content
	}) == d0 {
		t.Error("content digest MUST change on same-name/same-size/different-content")
	}

	// A rename (same content, different path) still flips it.
	if contentDigestOf(t, func(t *testing.T, r string) {
		base(t, r)
		if err := os.Rename(filepath.Join(r, "index.html"), filepath.Join(r, "renamed.html")); err != nil {
			t.Fatal(err)
		}
	}) == d0 {
		t.Error("a rename must change the content digest")
	}

	// A retargeted symlink flips it (the target IS its content).
	if contentDigestOf(t, func(t *testing.T, r string) {
		base(t, r)
		if err := os.Remove(filepath.Join(r, "link")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("empty", filepath.Join(r, "link")); err != nil {
			t.Fatal(err)
		}
	}) == d0 {
		t.Error("a symlink retarget must change the content digest")
	}

	// A change under an EXCLUDED dir (cgi-bin) must NOT change the digest (no false DIFF).
	if contentDigestOf(t, func(t *testing.T, r string) {
		base(t, r)
		write(t, filepath.Join(r, "cgi-bin", "more"), "JUNKJUNKJUNK")
	}) != d0 {
		t.Error("a change under an excluded dir must NOT change the content digest")
	}
}

// TestContentDigestToolsMissingYieldsEmpty: a present tree whose host emits NODIGEST
// (sha256sum / sort -z unavailable) returns present + empty digest, which the caller
// maps to content-unverified (a soft note) rather than a silent OK.
func TestContentDigestToolsMissingYieldsEmpty(t *testing.T) {
	noTools := fnRunner(func(string, map[string]string) ([]byte, error) { return []byte("10|2\nNODIGEST\n"), nil })
	b, n, dg, ok, unreadable, err := DocrootContentDigest(bg, noTools, "/d")
	if err != nil || !ok || unreadable || dg != "" || b != 10 || n != 2 {
		t.Errorf("no-tools: b=%d n=%d dg=%q ok=%v unreadable=%v err=%v", b, n, dg, ok, unreadable, err)
	}
}

// TestContentDigestUnreadableFailsClosed: an UNREADABLE root never certifies content.
func TestContentDigestUnreadableFailsClosed(t *testing.T) {
	unread := fnRunner(func(string, map[string]string) ([]byte, error) { return []byte("UNREADABLE\n"), nil })
	if _, _, dg, ok, unreadable, err := DocrootContentDigest(bg, unread, "/d"); err != nil || ok || !unreadable || dg != "" {
		t.Errorf("unreadable: dg=%q ok=%v unreadable=%v err=%v", dg, ok, unreadable, err)
	}
}

// TestContentDigestUnreadableBodyFailsClosed proves the fail-closed fix for the
// both-sides-unreadable false-OK: a regular file whose BODY cannot be read makes the
// content digest emit UNREADABLE (so the caller treats the docroot as content-unverified)
// rather than hash an '?unreadable' sentinel that would collide with a different but
// also-unreadable body of the same size on the other host. Run as `nobody` (root bypasses
// permission bits), so it needs root + runuser + a GNU find with `! -readable`.
func TestContentDigestUnreadableBodyFailsClosed(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to drop to nobody")
	}
	for _, tool := range []string{"runuser", "find", "bash", "sha256sum", "sort"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("missing %s", tool)
		}
	}
	if _, err := user.Lookup("nobody"); err != nil {
		t.Skip("no nobody user on this host")
	}
	// A /tmp-rooted dir we control the perms of (t.TempDir ancestors are 0700 and would
	// block nobody's traversal, masking the body check behind a cd failure).
	base, err := os.MkdirTemp("", "webcontent")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	doc := filepath.Join(base, "site")
	if err := os.MkdirAll(doc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(doc, "ok.txt"), []byte("AAAA"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(doc, "secret.txt")
	if err := os.WriteFile(secret, []byte("BBBB"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Make the whole tree traversable/readable by nobody FIRST, then strip the body.
	if out, e := exec.Command("chmod", "-R", "o+rx", base).CombinedOutput(); e != nil {
		t.Fatalf("chmod: %v %s", e, out)
	}
	run := func() string {
		out, _ := exec.Command("runuser", "-u", "nobody", "--", "env", "DOCROOT="+doc, "bash", "-c", contentDigestScript()).CombinedOutput()
		return string(out)
	}

	if err := os.Chmod(secret, 0o000); err != nil {
		t.Fatal(err)
	}
	if got := run(); !strings.Contains(got, "UNREADABLE") {
		t.Fatalf("an unreadable file BODY must fail closed (UNREADABLE), got:\n%s", got)
	}
	// Once readable, the same tree must yield a real DIGEST (proves the check is specific
	// to the unreadable body, not a blanket failure).
	if err := os.Chmod(secret, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run(); strings.Contains(got, "UNREADABLE") || !strings.Contains(got, "DIGEST ") {
		t.Fatalf("a fully-readable tree must produce a DIGEST (not UNREADABLE), got:\n%s", got)
	}
}
