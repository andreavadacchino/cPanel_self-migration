package webfiles

import (
	"strings"
	"testing"
)

// parseFileList parses listScript output records ("<type>\t<size>\t<relpath>",
// NUL-terminated) into FileEntry values — the buffered counterpart of the
// production streaming listSrcFiles, kept here as a test helper for the
// record-parsing tests (production parses per NUL record as it streams).
func parseFileList(out string) []FileEntry {
	var files []FileEntry
	for _, line := range strings.Split(out, "\x00") {
		if f, ok, _ := parseFileLine(line); ok {
			files = append(files, f)
		}
	}
	return files
}

func TestExcludePruneExprAnchorsSystemEntriesToTopLevel(t *testing.T) {
	ex := excludePruneExpr(`"$d"`)
	for _, name := range []string{"cgi-bin", ".ftpquota"} {
		// Anchored to the docroot top level via -path, so a deeper user dir with the
		// same name is NOT pruned.
		if !strings.Contains(ex, `-path "$d"/`+name) {
			t.Errorf("excludePruneExpr missing top-level -path for %q: %s", name, ex)
		}
	}
	if strings.Contains(ex, ".well-known") {
		t.Errorf("excludePruneExpr must not exclude .well-known user content: %s", ex)
	}
	// -name would match the entry at EVERY depth (the data-loss bug); it must be gone.
	if strings.Contains(ex, "-name '") {
		t.Errorf("excludePruneExpr must not use -name (matches at every depth): %s", ex)
	}
	if !strings.Contains(ex, "-prune") {
		t.Errorf("excludePruneExpr must -prune: %s", ex)
	}
}

func TestEmptyDestScriptHasBothGuards(t *testing.T) {
	s := emptyDestScript()
	// Guard 1: resolve the destination on the remote host before mutation.
	if !strings.Contains(s, "guard_dest_docroot") || !strings.Contains(s, "realpath -m") {
		t.Errorf("emptyDestScript missing canonical destination guard:\n%s", s)
	}
	// Guard 2: reject traversal before canonicalization.
	if !strings.Contains(s, "containing '..'") {
		t.Errorf("emptyDestScript missing raw traversal rejection:\n%s", s)
	}
	// It must compute ph from $HOME on the destination, not from the passed path.
	if !strings.Contains(s, `ph="$HOME/public_html"`) {
		t.Errorf("emptyDestScript must derive public_html from $HOME:\n%s", s)
	}
	// It must cd before deleting (relative find), and delete only top-level
	// entries via rm -rf (NOT find -delete, which conflicts with -prune/-depth).
	if !strings.Contains(s, `cd -P -- "$DEST_DOCROOT_CANON"`) || !strings.Contains(s, "-maxdepth 1") || !strings.Contains(s, "rm -rf") {
		t.Errorf("emptyDestScript must cd then top-level rm -rf:\n%s", s)
	}
	if strings.Contains(s, "-delete") {
		t.Errorf("emptyDestScript must NOT use find -delete (breaks prune/depth):\n%s", s)
	}
	// System entries must be excluded from deletion (preserved).
	if !strings.Contains(s, `! -name 'cgi-bin'`) {
		t.Errorf("emptyDestScript must preserve system dirs via ! -name:\n%s", s)
	}
	if strings.Contains(s, `! -name '.well-known'`) {
		t.Errorf("emptyDestScript must treat .well-known as user content, not a protected system dir:\n%s", s)
	}
}

func TestSrcTarCmdUsesFilesFromStdin(t *testing.T) {
	if !strings.Contains(srcTarCmd, "--no-recursion") || !strings.Contains(srcTarCmd, "--files-from=-") {
		t.Errorf("srcTarCmd must use --no-recursion --files-from=-: %s", srcTarCmd)
	}
	if !strings.Contains(srcTarCmd, "--null") {
		t.Errorf("srcTarCmd must use --null (NUL-delimited list, names with spaces/dashes): %s", srcTarCmd)
	}
	if strings.Contains(extractCmd, "--keep-newer-files") {
		t.Errorf("extractCmd must NOT keep-newer-files (migration overwrites): %s", extractCmd)
	}
}

func TestParseSize(t *testing.T) {
	cases := []struct {
		in     string
		bytes  int64
		count  int
		status sizeStatus
	}{
		{"12345|67\n", 12345, 67, sizePresent},
		{"  0|0  ", 0, 0, sizePresent}, // empty readable docroot is PRESENT (count 0), NOT unreadable
		{"ABSENT", 0, 0, sizeAbsent},
		{"UNREADABLE", 0, 0, sizeUnreadable},       // distinct from absent and from empty
		{"UNREADABLE\n", 0, 0, sizeUnreadable},     // trailing newline tolerated
		{"UNREADABLE foo\n", 0, 0, sizeUnreadable}, // prefix match (the script emits a bare marker)
		{"", 0, 0, sizeAbsent},
		{"garbage", 0, 0, sizeAbsent},
		{"12|x", 0, 0, sizeAbsent},
	}
	for _, c := range cases {
		b, n, status := parseSize(c.in)
		if b != c.bytes || n != c.count || status != c.status {
			t.Errorf("parseSize(%q) = (%d,%d,%d), want (%d,%d,%d)", c.in, b, n, status, c.bytes, c.count, c.status)
		}
	}
}

// TestGatherScriptIsSinglePass guards the optimization: the per-docroot size/count
// probe must walk the tree ONCE (sum bytes AND count files in a single find piped
// to awk), not twice. It still emits ABSENT for a missing docroot and the
// "<bytes>|<count>" form parseSize expects.
func TestGatherScriptIsSinglePass(t *testing.T) {
	s := gatherScript()
	if n := strings.Count(s, "find "); n != 1 {
		t.Errorf("gatherScript should run a single find, found %d:\n%s", n, s)
	}
	for _, want := range []string{`-printf '%s\n'`, "awk", `print s+0 "|" n+0`, "echo ABSENT", "echo UNREADABLE"} {
		if !strings.Contains(s, want) {
			t.Errorf("gatherScript missing %q:\n%s", want, s)
		}
	}
	// wc -l was the second-pass tool; it must be gone.
	if strings.Contains(s, "wc -l") {
		t.Errorf("gatherScript still uses a second-pass wc -l:\n%s", s)
	}
}

func TestParseFileList(t *testing.T) {
	// NUL-terminated records. "uploads/my photo.jpg" proves a path with a space is KEPT.
	out := "f\t100\tindex.html\x00" +
		"f\t2048\tsub/page.php\x00" +
		"f\t512\tuploads/my photo.jpg\x00" + // spaced name -> KEPT
		"d\t0\temptydir\x00" +
		"\x00" +
		"bad record without tabs\x00"
	files := parseFileList(out)
	if len(files) != 4 {
		t.Fatalf("got %d entries, want 4: %+v", len(files), files)
	}
	if files[0] != (FileEntry{RelPath: "index.html", Size: 100, IsDir: false}) {
		t.Errorf("files[0] = %+v", files[0])
	}
	if files[2] != (FileEntry{RelPath: "uploads/my photo.jpg", Size: 512, IsDir: false}) {
		t.Errorf("files[2] = %+v", files[2])
	}
	if files[3] != (FileEntry{RelPath: "emptydir", Size: 0, IsDir: true}) {
		t.Errorf("files[3] = %+v", files[3])
	}
}

// parseFileList must DROP path-traversal / absolute / control-byte entries so
// they never reach `tar --files-from` (which could read outside the docroot on
// the source or write outside it on extract). With the NUL-delimited list, NUL
// can no longer appear inside a record; the dangerous in-path bytes left to
// reject are TAB (field delimiter) and newline/CR.
func TestParseFileListDropsUnsafePaths(t *testing.T) {
	out := "f\t10\tindex.html\x00" +
		"f\t20\t../../.ssh/authorized_keys\x00" + // traversal -> dropped
		"f\t30\t/etc/passwd\x00" + // absolute -> dropped
		"d\t0\ta/../../b\x00" + // traversal in the middle -> dropped
		"f\t40\twp-content/ok file.php\x00" + // a space is fine -> KEPT
		"x\t45\tunknown-type.php\x00" + // unknown type -> dropped
		"f\t50\tbad\tname\x00" + // embedded TAB -> dropped
		"f\t60\tbad\nname\x00" // embedded newline -> dropped
	files := parseFileList(out)
	var got []string
	for _, f := range files {
		got = append(got, f.RelPath)
	}
	want := []string{"index.html", "wp-content/ok file.php"}
	if len(got) != len(want) {
		t.Fatalf("kept %v, want only the safe paths %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("kept[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestListScriptListsSymlinks guards the silent-data-loss fix: the source listing
// must emit symlinks (-type l) so the files-from tar mirrors them. Before this a
// docroot's symlinks were neither listed nor copied nor counted, vanishing with no
// verify signal.
func TestListScriptListsSymlinks(t *testing.T) {
	s := listScript()
	if !strings.Contains(s, `-type l -printf 'l\t0\t%P\0'`) {
		t.Errorf("listScript must list symlinks (-type l):\n%s", s)
	}
	// The symlink-to-a-directory case must NOT recurse the target (only the link is
	// archived) — guaranteed by the copy's --no-recursion, asserted in srcTarCmd.
	if !strings.Contains(srcTarCmd, "--no-recursion") {
		t.Errorf("srcTarCmd must keep --no-recursion so a symlinked dir is not walked: %s", srcTarCmd)
	}
}

// TestGatherCountsSymlinks guards that the size/count probes include symlinks, so
// the gather count stays consistent with what the copy now sends and the verify
// re-measures (a dropped symlink shows as a shortfall instead of netting to zero).
func TestGatherCountsSymlinks(t *testing.T) {
	for name, s := range map[string]string{"gatherScript": gatherScript(), "GatherAllScriptBody": GatherAllScriptBody()} {
		if !strings.Contains(s, `\( -type f -o -type l \)`) {
			t.Errorf("%s must count files AND symlinks via \\( -type f -o -type l \\):\n%s", name, s)
		}
	}
}

// TestParseFileLineSymlink: a symlink record parses as a sendable leaf (IsDir
// false), so it reaches the files-from tar and is archived as the link itself.
func TestParseFileLineSymlink(t *testing.T) {
	f, ok, unsafe := parseFileLine("l\t0\twp-content/uploads")
	if !ok || unsafe {
		t.Fatalf("symlink record should parse (ok=%v unsafe=%v)", ok, unsafe)
	}
	if f != (FileEntry{RelPath: "wp-content/uploads", Size: 0, IsDir: false}) {
		t.Errorf("symlink FileEntry = %+v, want a non-dir leaf", f)
	}
}

func TestSplitBatches(t *testing.T) {
	files := []FileEntry{
		{RelPath: "a", Size: 300},
		{RelPath: "b", Size: 300},
		{RelPath: "big", Size: 900}, // larger than max -> own batch
		{RelPath: "c", Size: 100},
	}
	batches := SplitBatches(files, 500)
	// a+b (600>500 so split after a) -> [a],[b,?]. Trace: a(300); b would make 600>500 -> new batch [a]; cur=[b]300; big 300+900>500 -> new [b]; cur=[big]900; c 900+100>500 -> new [big]; cur=[c]. => [a][b][big][c]
	if len(batches) != 4 {
		t.Fatalf("got %d batches, want 4: %+v", len(batches), batches)
	}
	if SplitBatches(nil, 500) != nil {
		t.Errorf("empty input must yield nil")
	}
}
