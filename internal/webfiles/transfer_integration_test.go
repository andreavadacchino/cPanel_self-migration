package webfiles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
)

// CopyDocroot (and its listSrcFiles / emptyDest / syncBatch / streamOnce) needs
// real *sshx.Client transfers, so it is covered against an in-process SSH server
// that runs the real tar/find in temp HOMEs. The destination docroot lives under
// $HOME/public_html/ so the empty-dest guard accepts it. Needs tar/bash.

func mkfile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// recSink records ProgressSink callbacks for assertions.
type recSink struct {
	total, added int64
	batches      int
}

func (r *recSink) SetTotal(b int64)  { r.total = b }
func (r *recSink) SetBatch(i, n int) { r.batches++ }
func (r *recSink) Add(n int64)       { r.added += n }

// TestCopyDocrootIntegration copies a docroot SRC -> DEST: the destination is
// emptied of stale content, system entries (cgi-bin) are excluded, empty dirs
// survive, file content lands intact, and progress/list callbacks fire.
func TestCopyDocrootIntegration(t *testing.T) {
	sshtest.RequireTools(t, "tar", "bash")
	requireDestGuardTools(t)
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	srcDoc := filepath.Join(srcHome, "public_html", "site")
	dstDoc := filepath.Join(dstHome, "public_html", "site")

	mkfile(t, filepath.Join(srcDoc, "index.php"), "<?php echo 1;")
	mkfile(t, filepath.Join(srcDoc, "css", "style.css"), "body{}")
	if err := os.MkdirAll(filepath.Join(srcDoc, "emptydir"), 0o755); err != nil { // must survive
		t.Fatal(err)
	}
	mkfile(t, filepath.Join(srcDoc, "cgi-bin", "old.cgi"), "x") // system entry -> excluded
	mkfile(t, filepath.Join(dstDoc, "stale.html"), "old")       // must be emptied

	src := sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))
	defer dst.Close()

	sink := &recSink{}
	var listCalls int
	tr := Transfer{Src: src, Dest: dst}
	res, err := tr.CopyDocroot(bg, WebPlanItem{Domain: "site.it", SrcDocroot: srcDoc, DestDocroot: dstDoc},
		sink, func(int) { listCalls++ })
	if err != nil {
		t.Fatalf("CopyDocroot: %v", err)
	}
	if res.FilesSent != 3 { // index.php, css/style.css, emptydir (cgi-bin excluded)
		t.Errorf("FilesSent = %d, want 3", res.FilesSent)
	}
	if sink.total <= 0 || sink.added <= 0 || sink.batches < 1 {
		t.Errorf("progress not reported: total=%d added=%d batches=%d", sink.total, sink.added, sink.batches)
	}
	if listCalls == 0 {
		t.Error("onList callback was never invoked")
	}

	if b, _ := os.ReadFile(filepath.Join(dstDoc, "index.php")); string(b) != "<?php echo 1;" {
		t.Errorf("index.php content = %q", b)
	}
	if !exists(filepath.Join(dstDoc, "css", "style.css")) {
		t.Error("css/style.css must be copied")
	}
	if !exists(filepath.Join(dstDoc, "emptydir")) {
		t.Error("empty dir must survive the transfer")
	}
	if exists(filepath.Join(dstDoc, "cgi-bin")) {
		t.Error("cgi-bin (system entry) must be excluded")
	}
	if exists(filepath.Join(dstDoc, "stale.html")) {
		t.Error("stale destination content must be emptied before extract")
	}
}

// TestCopyDocrootEmptySourceLeavesDestUntouched: an empty source must NOT empty
// the destination (protects against an anomalous source read wiping content).
func TestCopyDocrootEmptySourceBacksUpDest(t *testing.T) {
	sshtest.RequireTools(t, "tar", "bash")
	requireDestGuardTools(t)
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	srcDoc := filepath.Join(srcHome, "public_html", "empty")
	dstDoc := filepath.Join(dstHome, "public_html", "empty")
	if err := os.MkdirAll(srcDoc, 0o755); err != nil { // empty source docroot
		t.Fatal(err)
	}
	mkfile(t, filepath.Join(dstDoc, "keep.html"), "precious") // must be PRESERVED in -bak

	src := sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))
	defer dst.Close()

	tr := Transfer{Src: src, Dest: dst}
	res, err := tr.CopyDocroot(bg, WebPlanItem{Domain: "e.it", SrcDocroot: srcDoc, DestDocroot: dstDoc}, nil, nil)
	if err != nil {
		t.Fatalf("CopyDocroot(empty source): %v", err)
	}
	if res.FilesSent != 0 {
		t.Errorf("FilesSent = %d, want 0 for an empty source", res.FilesSent)
	}
	if res.BackedUpDir != "empty-bak" {
		t.Errorf("BackedUpDir = %q, want empty-bak", res.BackedUpDir)
	}
	// The precious content is preserved in the backup (NOT wiped)...
	if b, err := os.ReadFile(filepath.Join(dstHome, "public_html", "empty-bak", "keep.html")); err != nil || string(b) != "precious" {
		t.Errorf("destination content must be preserved in the -bak dir (err=%v, content=%q)", err, b)
	}
	// ...and the live docroot is fresh (mirrors the empty source).
	if exists(filepath.Join(dstDoc, "keep.html")) {
		t.Error("live docroot must be fresh after backup (keep.html should be in -bak, not here)")
	}
}

// TestCopyDocrootAbsentSourceFailsClosed: an ABSENT/unreadable source docroot (the
// dir does not exist on the source, so listScript prints NODIR) must FAIL the docroot,
// NOT be treated as an empty source — otherwise CopyDocroot would back up and wipe the
// LIVE destination over a source it never read. The destination must be untouched.
func TestCopyDocrootAbsentSourceFailsClosed(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	requireDestGuardTools(t)
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	srcDoc := filepath.Join(srcHome, "public_html", "gone") // deliberately NOT created
	dstDoc := filepath.Join(dstHome, "public_html", "gone")
	mkfile(t, filepath.Join(dstDoc, "keep.html"), "precious") // live destination content

	src := sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))
	defer dst.Close()

	tr := Transfer{Src: src, Dest: dst}
	_, err := tr.CopyDocroot(bg, WebPlanItem{Domain: "g.it", SrcDocroot: srcDoc, DestDocroot: dstDoc}, nil, nil)
	if err == nil {
		t.Fatal("CopyDocroot must FAIL on an absent source docroot, not silently mutate the destination")
	}
	if !strings.Contains(err.Error(), "absent or unreadable") {
		t.Errorf("error should name the absent source: %v", err)
	}
	// The live destination must be UNTOUCHED — no wipe, no -bak.
	if b, rerr := os.ReadFile(filepath.Join(dstDoc, "keep.html")); rerr != nil || string(b) != "precious" {
		t.Errorf("destination must be untouched on an absent source (err=%v content=%q)", rerr, b)
	}
	if exists(filepath.Join(dstHome, "public_html", "gone-bak")) {
		t.Error("no -bak directory may be created when the source is absent/unreadable")
	}

	// Present-but-unusable source (a regular FILE where the docroot should be): cd
	// fails the same way -> NODIR -> fail closed, dest still untouched. (Covers the
	// "or unreadable" half without needing an unprivileged user to chmod a dir.)
	notdirSrc := filepath.Join(srcHome, "public_html", "notdir")
	mkfile(t, notdirSrc, "i am a file, not a docroot")
	notdirDst := filepath.Join(dstHome, "public_html", "notdir")
	mkfile(t, filepath.Join(notdirDst, "keep.html"), "precious2")
	if _, err := tr.CopyDocroot(bg, WebPlanItem{Domain: "n.it", SrcDocroot: notdirSrc, DestDocroot: notdirDst}, nil, nil); err == nil {
		t.Error("CopyDocroot must fail when the source docroot is not a usable directory")
	}
	if b, rerr := os.ReadFile(filepath.Join(notdirDst, "keep.html")); rerr != nil || string(b) != "precious2" {
		t.Errorf("destination must be untouched for an unusable source (err=%v content=%q)", rerr, b)
	}
}

// TestCopyDocrootDirOnlySourceCopiesEmptyDirsAndBacksUpDest: a source with only
// DIRECTORIES (no regular files) is NOT empty — listScript emits `d` records, no
// NODIR — so it must preserve old destination content aside and still stream the
// empty directory entries into the fresh live docroot.
func TestCopyDocrootDirOnlySourceCopiesEmptyDirsAndBacksUpDest(t *testing.T) {
	sshtest.RequireTools(t, "tar", "bash")
	requireDestGuardTools(t)
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	srcDoc := filepath.Join(srcHome, "public_html", "dirs")
	dstDoc := filepath.Join(dstHome, "public_html", "dirs")
	if err := os.MkdirAll(filepath.Join(srcDoc, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcDoc, "uploads", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkfile(t, filepath.Join(dstDoc, "keep.html"), "precious")
	mkfile(t, filepath.Join(dstDoc, "tmp", "stale.txt"), "old")

	src := sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))
	defer dst.Close()

	tr := Transfer{Src: src, Dest: dst}
	res, err := tr.CopyDocroot(bg, WebPlanItem{Domain: "d.it", SrcDocroot: srcDoc, DestDocroot: dstDoc}, nil, nil)
	if err != nil {
		t.Fatalf("CopyDocroot(dir-only source) must NOT fail (it is not absent): %v", err)
	}
	if res.FilesSent != 2 || res.FilesTotal != 2 || res.BytesSent != 0 {
		t.Fatalf("result = sent=%d total=%d bytes=%d, want 2/2 entries and 0 bytes", res.FilesSent, res.FilesTotal, res.BytesSent)
	}
	if res.BackedUpDir != "dirs-bak" {
		t.Errorf("BackedUpDir = %q, want dirs-bak", res.BackedUpDir)
	}
	if !exists(filepath.Join(dstDoc, "cache")) || !exists(filepath.Join(dstDoc, "uploads", "empty")) {
		t.Fatalf("directory-only source entries must be copied into the live destination")
	}
	if exists(filepath.Join(dstDoc, "keep.html")) || exists(filepath.Join(dstDoc, "tmp", "stale.txt")) {
		t.Fatalf("stale destination files must be moved out of the live docroot")
	}
	if b, err := os.ReadFile(filepath.Join(dstHome, "public_html", "dirs-bak", "keep.html")); err != nil || string(b) != "precious" {
		t.Fatalf("stale destination content must be preserved in backup (err=%v content=%q)", err, b)
	}
	if b, err := os.ReadFile(filepath.Join(dstHome, "public_html", "dirs-bak", "tmp", "stale.txt")); err != nil || string(b) != "old" {
		t.Fatalf("nested stale destination content must be preserved in backup (err=%v content=%q)", err, b)
	}
}

// TestCopyDocrootBatchTimeoutRetries: a tiny per-batch timeout makes every
// streamOnce attempt expire, exercising syncBatch's full retry loop and final
// error (listing and empty-dest, which use the parent ctx, still succeed).
func TestCopyDocrootBatchTimeoutRetries(t *testing.T) {
	sshtest.RequireTools(t, "tar", "bash")
	requireDestGuardTools(t)
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	srcDoc := filepath.Join(srcHome, "public_html", "site")
	dstDoc := filepath.Join(dstHome, "public_html", "site")
	mkfile(t, filepath.Join(srcDoc, "index.php"), "x")

	src := sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))
	defer dst.Close()

	tr := Transfer{Src: src, Dest: dst, Timeout: time.Nanosecond} // every batch times out
	if _, err := tr.CopyDocroot(bg, WebPlanItem{Domain: "s.it", SrcDocroot: srcDoc, DestDocroot: dstDoc}, nil, nil); err == nil {
		t.Error("CopyDocroot must fail when every batch times out")
	}
}

// CopyDocroot rejects an item missing a docroot (pure guard, no SSH).
func TestCopyDocrootMissingDocroot(t *testing.T) {
	if _, err := (Transfer{}).CopyDocroot(bg, WebPlanItem{Domain: "x", SrcDocroot: "", DestDocroot: "/d"}, nil, nil); err == nil {
		t.Error("CopyDocroot must reject a missing source docroot")
	}
}
