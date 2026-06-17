package webfiles

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestScriptListsTypesWithMode(t *testing.T) {
	s := manifestScript(false)
	for _, want := range []string{
		`-type f -printf 'f\t%m\t%s\t%P\t\0'`,
		`-type l -printf 'l\t%m\t0\t%P\t%l\0'`, // symlink with its target (%l)
		`-type d -empty -printf 'd\t%m\t0\t%P\t\0'`,
		"echo \"NODIR\"",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("manifestScript missing %q:\n%s", want, s)
		}
	}
	// It must never dereference symlinks (no -L), so a symlink loop cannot hang it.
	if strings.Contains(s, "find -L") || strings.Contains(s, "find . -L") {
		t.Errorf("manifestScript must NOT follow symlinks (-L):\n%s", s)
	}
	// Non-deep mode must NOT hash (no per-byte read).
	if strings.Contains(s, "sha256sum") {
		t.Errorf("non-deep manifestScript must not hash files:\n%s", s)
	}
}

// The script must distinguish a present-but-unreadable docroot (UNREADABLE) from a
// genuinely absent/non-directory one (NODIR) with a deterministic -r/-x root check.
func TestManifestScriptDistinguishesUnreadableFromAbsent(t *testing.T) {
	s := manifestScript(false)
	for _, want := range []string{`[ -d "$DOCROOT" ]`, `[ -r "$DOCROOT" ]`, `[ -x "$DOCROOT" ]`, `echo "UNREADABLE"`, `echo "NODIR"`} {
		if !strings.Contains(s, want) {
			t.Errorf("manifestScript missing %q:\n%s", want, s)
		}
	}
}

// Behavioral: run the REAL manifestScript as the unprivileged "nobody" user (root
// would bypass the permission check) and assert the marker for each docroot state.
func TestManifestScriptUnreadableBehavior(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("must be root to chmod 000 and drop to nobody")
	}
	if _, err := exec.LookPath("runuser"); err != nil {
		t.Skip("runuser not available")
	}
	base := t.TempDir()
	// nobody must be able to traverse the temp path down to the cases. Only chmod the
	// TEST-OWNED dirs (strictly under os.TempDir()); never chmod /tmp or a shared
	// ancestor (that would strip the sticky bit and destabilize other tests).
	tmp := filepath.Clean(os.TempDir())
	for p := filepath.Clean(base); p != tmp && p != "/" && p != "." && strings.HasPrefix(p, tmp+string(os.PathSeparator)); p = filepath.Dir(p) {
		_ = os.Chmod(p, 0o755)
	}
	good := filepath.Join(base, "good")
	noread := filepath.Join(base, "noread")
	afile := filepath.Join(base, "afile")
	if err := os.MkdirAll(filepath.Join(good, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(good, "f.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(noread, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(afile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(noread, 0o000); err != nil { // unreadable to nobody (root bypasses, hence runuser)
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(noread, 0o755) })

	script := manifestScript(false)
	run := func(docroot string) string {
		t.Helper()
		cmd := exec.Command("runuser", "-u", "nobody", "--", "bash", "-c", script)
		cmd.Env = append(os.Environ(), "DOCROOT="+docroot)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("run %s: %v (%s)", docroot, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if got := run(filepath.Join(base, "absent")); got != "NODIR" {
		t.Errorf("absent docroot -> %q, want NODIR", got)
	}
	if got := run(afile); got != "NODIR" {
		t.Errorf("file (not a dir) -> %q, want NODIR", got)
	}
	if got := run(noread); got != "UNREADABLE" {
		t.Errorf("present-but-unreadable docroot -> %q, want UNREADABLE (a bare `cd||NODIR` would mis-report it as absent)", got)
	}
	if got := run(good); !strings.Contains(got, "f.txt") || got == "NODIR" || got == "UNREADABLE" {
		t.Errorf("readable docroot -> %q, want file records", got)
	}
}

func TestManifestScriptDeepHashesFiles(t *testing.T) {
	s := manifestScript(true)
	for _, want := range []string{"sha256sum", `printf 'H\t%s\t%s\0'`, "${p#./}"} {
		if !strings.Contains(s, want) {
			t.Errorf("deep manifestScript missing %q:\n%s", want, s)
		}
	}
}

func TestParseDigestRecord(t *testing.T) {
	rel, hex, ok := parseDigestRecord("H\tabc123\twp-content/file.php")
	if !ok || rel != "wp-content/file.php" || hex != "abc123" {
		t.Errorf("parseDigestRecord = (%q,%q,%v), want (wp-content/file.php, abc123, true)", rel, hex, ok)
	}
	for _, bad := range []string{"f\t644\t1\tx\t", "H\t", "H\tonlyhash", "H\t\trel", "notH"} {
		if _, _, ok := parseDigestRecord(bad); ok {
			t.Errorf("parseDigestRecord(%q) should be !ok", bad)
		}
	}
}

// TestDiffManifestsContent: same size, different sha256 = a HARD content divergence
// (the silent-corruption case deep verify exists to catch). No digests = no content
// check (Tier 1 falls back to size, which matches here).
func TestDiffManifestsContent(t *testing.T) {
	src := Manifest{"a.php": {Type: 'f', Mode: "644", Size: 100, Digest: "AAA"}}
	dest := Manifest{"a.php": {Type: 'f', Mode: "644", Size: 100, Digest: "BBB"}}
	d := DiffManifests(src, dest)
	if d.ContentDiff != 1 || d.SizeDiff != 0 || d.Hard() != 1 || d.OK() {
		t.Errorf("same-size content mismatch must be a hard content divergence: %+v", d)
	}
	// Without digests the same inputs look identical (size matches) — Tier 1 cannot
	// see content, by design.
	noDig := DiffManifests(
		Manifest{"a.php": {Type: 'f', Mode: "644", Size: 100}},
		Manifest{"a.php": {Type: 'f', Mode: "644", Size: 100}},
	)
	if !noDig.OK() {
		t.Errorf("without digests, equal-size files must be OK: %+v", noDig)
	}
}

// TestDiffManifestsContentUnverified: in deep mode, a file whose body could not be
// hashed (sentinel) or whose digest is missing on one side must be UNVERIFIED — a
// hard divergence, never a silent clean pass under the deep banner.
func TestDiffManifestsContentUnverified(t *testing.T) {
	cases := map[string]Manifest{
		"sentinel on dest":  {"a.php": {Type: 'f', Mode: "644", Size: 100, Digest: digestUnreadable}},
		"empty digest dest": {"a.php": {Type: 'f', Mode: "644", Size: 100}}, // no H record on dest
	}
	for name, dest := range cases {
		src := Manifest{"a.php": {Type: 'f', Mode: "644", Size: 100, Digest: "AAA"}}
		d := DiffManifests(src, dest)
		if d.ContentUnverified != 1 || d.ContentDiff != 0 || d.Hard() != 1 || d.OK() {
			t.Errorf("%s: must be one content-unverified hard divergence: %+v", name, d)
		}
	}
	// Sentinel on BOTH sides is still unverified (we cannot certify either body).
	both := DiffManifests(
		Manifest{"a.php": {Type: 'f', Mode: "644", Size: 100, Digest: digestUnreadable}},
		Manifest{"a.php": {Type: 'f', Mode: "644", Size: 100, Digest: digestUnreadable}},
	)
	if both.ContentUnverified != 1 || both.OK() {
		t.Errorf("both-sentinel must be unverified, not a clean match: %+v", both)
	}
	// A clean deep run (equal real digests) stays OK with nothing unverified.
	clean := DiffManifests(
		Manifest{"a.php": {Type: 'f', Mode: "644", Size: 100, Digest: "AAA"}},
		Manifest{"a.php": {Type: 'f', Mode: "644", Size: 100, Digest: "AAA"}},
	)
	if !clean.OK() || clean.ContentUnverified != 0 {
		t.Errorf("equal real digests must be a clean OK: %+v", clean)
	}
}

func TestExtractCmdPreservesPermissions(t *testing.T) {
	// -p makes the destination mirror the source's mode bits (ignoring umask), which
	// the manifest verify's per-file mode comparison depends on.
	if !strings.Contains(extractCmd, "tar -xp") {
		t.Errorf("extractCmd must extract with -p (preserve permissions): %s", extractCmd)
	}
}

func TestParseManifestRecord(t *testing.T) {
	cases := []struct {
		name   string
		rec    string
		rel    string
		entry  ManifestEntry
		ok     bool
		unsafe bool
	}{
		{"file", "f\t644\t1234\tindex.html\t", "index.html", ManifestEntry{Type: 'f', Mode: "644", Size: 1234}, true, false},
		{"symlink", "l\t777\t0\twp-content/uploads\t../shared/uploads", "wp-content/uploads", ManifestEntry{Type: 'l', Mode: "777", Size: 0, Link: "../shared/uploads"}, true, false},
		{"emptydir", "d\t755\t0\tcache\t", "cache", ManifestEntry{Type: 'd', Mode: "755", Size: 0}, true, false},
		{"spaced path", "f\t10\t5\tmy photo.jpg\t", "my photo.jpg", ManifestEntry{Type: 'f', Mode: "10", Size: 5}, true, false},
		{"NODIR", "NODIR", "", ManifestEntry{}, false, false},
		{"blank", "", "", ManifestEntry{}, false, false},
		{"too few fields", "f\t644\tindex.html", "", ManifestEntry{}, false, false},
		{"bad size", "f\t644\tNaN\tx\t", "", ManifestEntry{}, false, false},
		{"bad type", "x\t644\t1\tx\t", "", ManifestEntry{}, false, false},
		{"traversal unsafe", "f\t644\t1\t../../etc/passwd\t", "", ManifestEntry{}, false, true},
		// A literal TAB inside an f/d filename truncates the path at SplitN; the tail
		// lands in the (normally empty) link field, so it must be rejected as unsafe
		// rather than keyed under the truncated prefix ("dir") — the false-OK bug.
		{"file tab in path", "f\t644\t10\tdir\tfile\t", "", ManifestEntry{}, false, true},
		{"emptydir tab in path", "d\t755\t0\tparent\tchild\t", "", ManifestEntry{}, false, true},
		{"file two tabs in path", "f\t644\t10\ta\tb\tc\t", "", ManifestEntry{}, false, true},
		// A trailing SPACE (not a tab) is a legitimate filename and must still parse.
		{"trailing space ok", "f\t644\t10\tname \t", "name ", ManifestEntry{Type: 'f', Mode: "644", Size: 10}, true, false},
	}
	for _, c := range cases {
		rel, e, ok, unsafe := parseManifestRecord(c.rec)
		if rel != c.rel || ok != c.ok || unsafe != c.unsafe || e != c.entry {
			t.Errorf("%s: parseManifestRecord(%q) = (%q,%+v,%v,%v), want (%q,%+v,%v,%v)",
				c.name, c.rec, rel, e, ok, unsafe, c.rel, c.entry, c.ok, c.unsafe)
		}
	}
}

func TestParseManifestRecordKeepsTabInSymlinkTarget(t *testing.T) {
	// The target is the LAST field, so a tab inside it must survive the split. The
	// f/d tab-in-filename guard is type-gated to f/d (a symlink legitimately carries
	// a tab-bearing target here), so this symlink still parses with its target intact.
	rel, e, ok, _ := parseManifestRecord("l\t777\t0\tlink\ta\tb")
	if !ok || rel != "link" || e.Link != "a\tb" {
		t.Errorf("symlink target with tab not preserved: rel=%q link=%q ok=%v", rel, e.Link, ok)
	}
}

// TestParseManifestRecordSymlinkNameTabResidual documents the KNOWN residual of the
// f/d-only tab guard: a symlink whose NAME (not its target) contains a tab is still
// mis-keyed under the truncated prefix, because the guard cannot fire for 'l' (the
// link field legitimately carries a tab-bearing target, so emptiness is not a valid
// signal there). This captures the boundary as a conscious decision — closing it
// would require reframing the record. If a later fix changes this, update the note.
func TestParseManifestRecordSymlinkNameTabResidual(t *testing.T) {
	rel, _, ok, _ := parseManifestRecord("l\t777\t0\tna\tme\t../target")
	if !ok || rel != "na" {
		t.Fatalf("symlink-name-tab residual changed (rel=%q ok=%v) — re-evaluate and update the note", rel, ok)
	}
}

func TestDiffManifestsIdentical(t *testing.T) {
	m := Manifest{
		"index.html":         {Type: 'f', Mode: "644", Size: 100},
		"wp-content/uploads": {Type: 'l', Mode: "777", Link: "../shared"},
		"cache":              {Type: 'd', Mode: "755"},
	}
	// A distinct but equal copy.
	cp := Manifest{}
	for k, v := range m {
		cp[k] = v
	}
	d := DiffManifests(m, cp)
	if !d.OK() || d.Hard() != 0 {
		t.Errorf("identical manifests must be OK: %+v", d)
	}
}

// TestDiffManifestsMissingSymlink is the regression for the silent-loss bug: a
// source symlink absent on the destination must be a HARD divergence, counted as a
// missing symlink (not netted away).
func TestDiffManifestsMissingSymlink(t *testing.T) {
	src := Manifest{
		"index.html": {Type: 'f', Mode: "644", Size: 100},
		"latest.jpg": {Type: 'l', Mode: "777", Link: "uploads/photo.jpg"},
	}
	dest := Manifest{
		"index.html": {Type: 'f', Mode: "644", Size: 100},
	}
	d := DiffManifests(src, dest)
	if d.Missing != 1 || d.MissingSymlinks != 1 || d.Hard() != 1 || d.OK() {
		t.Errorf("missing symlink must be a hard divergence: %+v", d)
	}
	if len(d.Examples) == 0 || !strings.Contains(d.Examples[0], "missing symlink latest.jpg") {
		t.Errorf("example should name the missing symlink: %+v", d.Examples)
	}
}

func TestDiffManifestsMissingEmptyDirIsHardDivergence(t *testing.T) {
	src := Manifest{"cache": {Type: 'd', Mode: "755"}}
	d := DiffManifests(src, Manifest{})
	if d.Missing != 1 || d.Hard() != 1 || d.OK() {
		t.Fatalf("missing empty directory must be a hard divergence: %+v", d)
	}
	if len(d.Examples) == 0 || !strings.Contains(d.Examples[0], "missing cache") {
		t.Fatalf("example should name the missing directory: %+v", d.Examples)
	}
}

func TestDiffManifestsCategories(t *testing.T) {
	src := Manifest{
		"keep":     {Type: 'f', Mode: "644", Size: 10},
		"shrunk":   {Type: 'f', Mode: "644", Size: 100}, // size diff
		"perm":     {Type: 'f', Mode: "644", Size: 5},   // mode diff (soft)
		"waslink":  {Type: 'l', Mode: "777", Link: "a"}, // type diff (became file)
		"link":     {Type: 'l', Mode: "777", Link: "a"}, // link target diff
		"goneflat": {Type: 'f', Mode: "644", Size: 7},   // missing (file)
	}
	dest := Manifest{
		"keep":      {Type: 'f', Mode: "644", Size: 10},
		"shrunk":    {Type: 'f', Mode: "644", Size: 40},
		"perm":      {Type: 'f', Mode: "600", Size: 5},
		"waslink":   {Type: 'f', Mode: "644", Size: 3},
		"link":      {Type: 'l', Mode: "777", Link: "b"},
		"extrafile": {Type: 'f', Mode: "644", Size: 1}, // extra on dest
	}
	d := DiffManifests(src, dest)
	if d.SizeDiff != 1 || d.ModeDiff != 1 || d.TypeDiff != 1 || d.LinkDiff != 1 || d.Missing != 1 || d.Extra != 1 {
		t.Errorf("category counts wrong: %+v", d)
	}
	// Hard excludes the soft mode drift.
	if d.Hard() != 5 { // size+type+link+missing+extra
		t.Errorf("Hard()=%d, want 5: %+v", d.Hard(), d)
	}
	if d.OK() {
		t.Errorf("OK() must be false when anything differs: %+v", d)
	}
}

func TestDiffManifestsExamplesBounded(t *testing.T) {
	src := Manifest{}
	for i := 0; i < manifestExamples*3; i++ {
		src[string(rune('a'+i))+"-file"] = ManifestEntry{Type: 'f', Mode: "644", Size: 1}
	}
	d := DiffManifests(src, Manifest{}) // everything missing
	if d.Missing != len(src) {
		t.Errorf("Missing=%d, want %d", d.Missing, len(src))
	}
	if len(d.Examples) > manifestExamples {
		t.Errorf("Examples must be bounded to %d, got %d", manifestExamples, len(d.Examples))
	}
}
