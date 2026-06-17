package webfiles

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
)

func TestParseDigestOutput(t *testing.T) {
	cases := []struct {
		in         string
		wantB      int64
		wantC      int
		wantD      string
		wantStatus sizeStatus
	}{
		{"ABSENT\n", 0, 0, "", sizeAbsent},
		{"UNREADABLE\n", 0, 0, "", sizeUnreadable},
		{"10|2\nDIGEST abc123\n", 10, 2, "abc123", sizePresent},
		{"DIGEST def456\n0|0\n", 0, 0, "def456", sizePresent}, // line order independent; empty-but-present tree
		{"", 0, 0, "", sizeAbsent},
	}
	for _, c := range cases {
		b, n, d, s := parseDigestOutput(c.in)
		if b != c.wantB || n != c.wantC || d != c.wantD || s != c.wantStatus {
			t.Errorf("parseDigestOutput(%q) = (%d,%d,%q,%d), want (%d,%d,%q,%d)",
				c.in, b, n, d, s, c.wantB, c.wantC, c.wantD, c.wantStatus)
		}
	}
}

// digestOf builds a docroot tree via build() under a fresh temp HOME, runs the REAL
// DocrootDigest over the in-process bash SSH server, and returns the digest.
func digestOf(t *testing.T, build func(t *testing.T, root string)) string {
	t.Helper()
	home := t.TempDir()
	doc := filepath.Join(home, "public_html", "site")
	if err := os.MkdirAll(doc, 0o755); err != nil {
		t.Fatal(err)
	}
	build(t, doc)
	c := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer c.Close()
	_, _, d, ok, unread, err := DocrootDigest(context.Background(), c, doc)
	if err != nil || !ok || unread {
		t.Fatalf("DocrootDigest: ok=%v unreadable=%v err=%v", ok, unread, err)
	}
	if d == "" {
		t.Fatal("empty digest for a present readable docroot")
	}
	return d
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// base is a representative docroot: a file, a nested file, an empty dir, a symlink,
// and an EXCLUDED cgi-bin with content.
func base(t *testing.T, r string) {
	t.Helper()
	write(t, filepath.Join(r, "index.html"), "hello") // 5 bytes
	write(t, filepath.Join(r, "sub", "a.php"), "AAA")
	if err := os.MkdirAll(filepath.Join(r, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub", filepath.Join(r, "link")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(r, "cgi-bin", "x"), "junk") // excluded
}

// TestGetManifestProgressPhases: a deep GetManifest streams a listing phase
// (hashing=false) and THEN a per-file sha256 phase (hashing=true); BOTH must drive
// the progress callback, so the verify row keeps advancing through the slow hashing
// pass instead of freezing at the final entry count. A non-deep manifest must never
// report a hashing phase.
func TestGetManifestProgressPhases(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "sha256sum", "cut")
	home := t.TempDir()
	doc := filepath.Join(home, "public_html", "site")
	if err := os.MkdirAll(doc, 0o755); err != nil {
		t.Fatal(err)
	}
	base(t, doc)
	c := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer c.Close()

	type tick struct {
		hashing bool
		n       int
	}
	run := func(deep bool) ([]tick, Manifest) {
		var ticks []tick
		m, absent, unreadable, truncated, dropped, err := GetManifest(context.Background(), c, doc, 0, deep,
			func(hashing bool, n int) { ticks = append(ticks, tick{hashing, n}) })
		if err != nil || absent || unreadable || truncated {
			t.Fatalf("GetManifest(deep=%v): err=%v absent=%v unreadable=%v truncated=%v dropped=%d", deep, err, absent, unreadable, truncated, dropped)
		}
		return ticks, m
	}

	// Deep: both phases observed; every regular file carries a digest.
	ticks, m := run(true)
	var sawList, sawHash bool
	for _, tk := range ticks {
		if tk.n < 1 {
			t.Errorf("progress tick count must be >= 1, got %d", tk.n)
		}
		if tk.hashing {
			sawHash = true
		} else {
			sawList = true
		}
	}
	if !sawList {
		t.Error("deep GetManifest: expected a listing-phase tick (hashing=false)")
	}
	if !sawHash {
		t.Error("deep GetManifest: expected a hashing-phase tick (hashing=true) — the slow pass must drive the row")
	}
	files := 0
	for rel, e := range m {
		if e.Type == 'f' {
			files++
			if e.Digest == "" {
				t.Errorf("deep manifest: file %q has no content digest", rel)
			}
		}
	}
	if files == 0 {
		t.Fatal("deep manifest produced no regular files to hash")
	}

	// Non-deep: only the listing phase exists; no hashing ticks at all.
	ticks2, _ := run(false)
	for _, tk := range ticks2 {
		if tk.hashing {
			t.Errorf("non-deep GetManifest must not report a hashing phase, got %+v", tk)
		}
	}
}

func TestDocrootDigestBehavior(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "sort", "sha256sum", "awk")
	d0 := digestOf(t, base)

	// Identical tree -> identical digest (determinism).
	if digestOf(t, base) != d0 {
		t.Error("identical trees must digest equally")
	}
	// A rename (same size) must change the digest.
	if digestOf(t, func(t *testing.T, r string) {
		base(t, r)
		if err := os.Rename(filepath.Join(r, "index.html"), filepath.Join(r, "renamed.html")); err != nil {
			t.Fatal(err)
		}
	}) == d0 {
		t.Error("a rename must change the digest")
	}
	// Equal-size add+remove (hello->world, both 5 bytes; index.html->other.html): count
	// AND bytes are unchanged, so count+bytes alone would miss it — the digest must not.
	if digestOf(t, func(t *testing.T, r string) {
		base(t, r)
		if err := os.Remove(filepath.Join(r, "index.html")); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(r, "other.html"), "world")
	}) == d0 {
		t.Error("an equal-size add+remove must change the digest (the #4 count+bytes false-OK)")
	}
	// A retargeted symlink must change the digest (via %l).
	if digestOf(t, func(t *testing.T, r string) {
		base(t, r)
		if err := os.Remove(filepath.Join(r, "link")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("empty", filepath.Join(r, "link")); err != nil {
			t.Fatal(err)
		}
	}) == d0 {
		t.Error("a symlink retarget must change the digest")
	}
	// A change under an EXCLUDED dir must NOT change the digest (no false DIFF).
	if digestOf(t, func(t *testing.T, r string) {
		base(t, r)
		write(t, filepath.Join(r, "cgi-bin", "more"), "JUNKJUNKJUNK")
	}) != d0 {
		t.Error("a change under an excluded dir must NOT change the digest")
	}
	// Same-name/same-size/DIFFERENT content is NOT caught (by design — that's --deep's
	// job; documented). The digest must be UNCHANGED here.
	if digestOf(t, func(t *testing.T, r string) {
		base(t, r)
		write(t, filepath.Join(r, "index.html"), "world") // same name, same 5 bytes, different content
	}) != d0 {
		t.Error("name+size digest must NOT change on same-name/same-size/different-content (deep's job)")
	}
}

// TestDocrootDigestFailClosedStatuses covers the two fail-closed diagnostics: an
// UNREADABLE subtree (present but not certifiable) and a PRESENT tree whose host
// could not produce a digest (sha256sum / sort -z missing) — both warn and return
// an empty digest without certifying the over-cap mirror.
func TestDocrootDigestFailClosedStatuses(t *testing.T) {
	unread := fnRunner(func(string, map[string]string) ([]byte, error) { return []byte("UNREADABLE\n"), nil })
	if _, _, dg, ok, unreadable, err := DocrootDigest(bg, unread, "/d"); err != nil || ok || !unreadable || dg != "" {
		t.Errorf("unreadable: dg=%q ok=%v unreadable=%v err=%v", dg, ok, unreadable, err)
	}
	// Present (bytes|count read) but no DIGEST line -> tools missing on the host.
	noTools := fnRunner(func(string, map[string]string) ([]byte, error) { return []byte("10|2\n"), nil })
	b, n, dg, ok, unreadable, err := DocrootDigest(bg, noTools, "/d")
	if err != nil || !ok || unreadable || dg != "" || b != 10 || n != 2 {
		t.Errorf("no-tools: b=%d n=%d dg=%q ok=%v unreadable=%v err=%v", b, n, dg, ok, unreadable, err)
	}
}
