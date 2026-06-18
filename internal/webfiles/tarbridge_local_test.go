package webfiles

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// This test validates the web-file copy end-to-end using REAL local bash + tar
// processes wired by io.Copy — the SAME scripts the SSH path runs (listScript,
// emptyDestScript, srcTarCmd, extractCmd), without needing SSH. It proves:
//   - the whole docroot round-trips,
//   - cgi-bin/.ftpquota are excluded from the copy,
//   - .well-known is copied as real user-served site content,
//   - an empty directory is recreated on the destination,
//   - the destination is EMPTIED first (a pre-existing junk file is removed)
//     while a pre-existing cgi-bin/ is PRESERVED,
//   - the safety guard refuses public_html itself and paths outside it,
//   - a second run is idempotent.
func TestWebBridgeLocalRoundTrip(t *testing.T) {
	sshtest.RequireTools(t, "tar", "bash")
	requireDestGuardTools(t)

	home := t.TempDir()    // fake destination $HOME
	srcRoot := t.TempDir() // source docroot lives here

	// --- Build a fake SOURCE docroot ---
	srcDoc := filepath.Join(srcRoot, "sitefiles")
	mk := func(root, rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk(srcDoc, "index.html", "<h1>home</h1>")
	mk(srcDoc, "sub/page.php", "<?php echo 1; ?>")
	mk(srcDoc, "assets/style.css", "body{}")
	mk(srcDoc, "uploads/my photo.jpg", "JPEG-DATA")    // regression: a name with a space must copy
	mk(srcDoc, "Screen Shot 2024/a b.png", "PNG-DATA") // regression: spaces in a dir AND file name
	mk(srcDoc, "-leadingdash.txt", "dash file")        // regression: a leading dash must not be read as a tar option
	mk(srcDoc, "cgi-bin/script.cgi", "SYSTEM-SHOULD-BE-EXCLUDED")
	mk(srcDoc, ".well-known/acme", "USER-CONTENT-MUST-SURVIVE")
	mk(srcDoc, ".ftpquota", "SYSTEM-SHOULD-BE-EXCLUDED")
	// Regression: a dir named like a cPanel system entry but NESTED under the docroot
	// is REAL user content and must be copied (the exclusion is top-level-only).
	mk(srcDoc, "uploads/.well-known/pgp.txt", "NESTED-MUST-SURVIVE")
	mk(srcDoc, "app/cgi-bin/handler.php", "NESTED-MUST-SURVIVE")
	if err := os.MkdirAll(filepath.Join(srcDoc, "emptydir"), 0o755); err != nil {
		t.Fatal(err) // an empty dir that must be recreated
	}
	// Symlinks must be MIRRORED as symlinks (the silent-data-loss fix): a link to a
	// file, and a link to a directory which must NOT be dereferenced/recursed.
	if err := os.Symlink("uploads/my photo.jpg", filepath.Join(srcDoc, "latest.jpg")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("assets", filepath.Join(srcDoc, "assets-link")); err != nil {
		t.Fatal(err)
	}

	// --- Build the DESTINATION docroot under <home>/public_html/<dom> ---
	destDoc := filepath.Join(home, "public_html", "example.com")
	mk(destDoc, "OLD_JUNK.html", "should be removed by the empty step")
	mk(destDoc, "cgi-bin/keepme.cgi", "system dir, must be preserved")
	mk(destDoc, ".well-known/dest-only.txt", "stale destination user content")

	// 1) List source entries via the REAL listScript.
	listOut := runScriptLocal(t, home, listScript(), map[string]string{"DOCROOT": srcDoc}, "")
	files := parseFileList(listOut)
	if len(files) == 0 {
		t.Fatalf("listScript returned no entries:\n%s", listOut)
	}
	var names []string
	for _, f := range files {
		names = append(names, f.RelPath)
	}
	// NUL-delimited list (matches the production --null --files-from path).
	fileList := strings.Join(names, "\x00") + "\x00"

	runCopy := func() {
		// 2) Empty the destination via the REAL emptyDestScript (guarded).
		runScriptLocal(t, home, emptyDestScript(), map[string]string{"DEST_DOCROOT": destDoc}, "")
		// 3) Bridge: src tar -c | io.Copy | dest tar -x, using the REAL commands.
		bridgeLocal(t, home,
			sshx.WithEnv(srcTarCmd, map[string]string{"SRC_DOCROOT": srcDoc}), fileList,
			sshx.WithEnv(extractCmd, map[string]string{"DEST_DOCROOT": destDoc}))
	}

	runCopy()

	got := relEntries(t, destDoc)

	// Normal files copied — including names with spaces and a leading dash.
	for _, rel := range []string{
		"index.html", "sub/page.php", "assets/style.css",
		"uploads/my photo.jpg", "Screen Shot 2024/a b.png", "-leadingdash.txt",
		".well-known/acme",
		"uploads/.well-known/pgp.txt", "app/cgi-bin/handler.php", // nested system-named dirs are user content
	} {
		if !contains(got, rel) {
			t.Errorf("expected %q to be copied, dest has %v", rel, got)
		}
	}
	// Empty directory recreated.
	if !contains(got, "emptydir") {
		t.Errorf("expected emptydir to be recreated, dest has %v", got)
	}
	// Symlinks mirrored AS symlinks, not dropped and not dereferenced.
	for _, rel := range []string{"latest.jpg", "assets-link"} {
		if !contains(got, rel) {
			t.Errorf("expected symlink %q to be copied, dest has %v", rel, got)
			continue
		}
		fi, err := os.Lstat(filepath.Join(destDoc, rel))
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("dest %q must be a symlink (err=%v mode=%v)", rel, err, fi.Mode())
		}
	}
	// The symlinked directory must NOT have been recursed into (only the link is
	// copied, never its target tree).
	if contains(got, "assets-link/style.css") {
		t.Errorf("symlinked dir must not be recursed, dest has %v", got)
	}

	// Manifest verify: the source and destination manifests must match 1:1 (the
	// basis of verifyWebFiles). This proves the manifestScript -printf format
	// round-trips through real `find`, the -p extract preserves mode bits, and
	// symlinks survive as symlinks — so a clean copy produces a clean verdict.
	srcMan := parseManifestLocal(t, home, srcDoc, false)
	if e, ok := srcMan["latest.jpg"]; !ok || e.Type != 'l' || e.Link != "uploads/my photo.jpg" {
		t.Errorf("source manifest must record latest.jpg as a symlink to its target: %+v (ok=%v)", e, ok)
	}
	destMan := parseManifestLocal(t, home, destDoc, false)
	if md := DiffManifests(srcMan, destMan); !md.OK() {
		t.Errorf("post-copy manifest diff should be clean, got %+v\nexamples: %v", md, md.Examples)
	}

	// Deep manifest: per-file sha256 must match on both sides after a faithful copy,
	// and the digests must actually be populated (proving the hashing pass ran).
	srcDeep := parseManifestLocal(t, home, srcDoc, true)
	destDeep := parseManifestLocal(t, home, destDoc, true)
	if e := srcDeep["index.html"]; e.Digest == "" {
		t.Errorf("deep manifest must populate a file digest: %+v", e)
	}
	if md := DiffManifests(srcDeep, destDeep); !md.OK() {
		t.Errorf("post-copy DEEP manifest diff should be clean, got %+v\nexamples: %v", md, md.Examples)
	}
	// Corrupt one destination file's content (same size) and confirm deep verify
	// catches it while metadata (size) does not.
	if err := os.WriteFile(filepath.Join(destDoc, "index.html"), []byte("<h1>XXXX</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	destCorrupt := parseManifestLocal(t, home, destDoc, true)
	if md := DiffManifests(srcDeep, destCorrupt); md.ContentDiff != 1 || md.Hard() != 1 {
		t.Errorf("deep verify must catch same-size content corruption: %+v", md)
	}
	// System entries from the SOURCE must NOT have been copied.
	for _, rel := range []string{"cgi-bin/script.cgi", ".ftpquota"} {
		if contains(got, rel) {
			t.Errorf("system entry %q should have been excluded from the copy, dest has %v", rel, got)
		}
	}
	if contains(got, ".well-known/dest-only.txt") {
		t.Errorf("stale destination .well-known content should have been removed before copy, dest has %v", got)
	}
	// The destination's OWN cgi-bin (pre-existing) must be PRESERVED by the empty.
	if !contains(got, "cgi-bin/keepme.cgi") {
		t.Errorf("pre-existing dest cgi-bin should be preserved, dest has %v", got)
	}
	// The junk file must have been REMOVED by the empty step (migration mirror).
	if contains(got, "OLD_JUNK.html") {
		t.Errorf("OLD_JUNK.html should have been removed by the empty step, dest has %v", got)
	}

	// Idempotency: a second full run yields the same result.
	runCopy()
	got2 := relEntries(t, destDoc)
	if !equalSet(got, got2) {
		t.Errorf("re-run changed dest: before=%v after=%v", got, got2)
	}
}

// TestEmptyDestGuardRefusesDangerousPaths drives the REAL emptyDestScript with
// dangerous DEST_DOCROOT values and asserts it exits non-zero AND deletes
// nothing. This is the single most important safety test (no HOME destruction).
func TestEmptyDestGuardRefusesDangerousPaths(t *testing.T) {
	sshtest.RequireTools(t, "tar", "bash")
	requireDestGuardTools(t)
	home := t.TempDir()

	// Seed public_html with a sentinel file and a sentinel outside it.
	ph := filepath.Join(home, "public_html")
	if err := os.MkdirAll(ph, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(ph, "SENTINEL")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(home, "OUTSIDE")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideDir := filepath.Join(home, "outside-dir")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "OUTSIDE_SENTINEL"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(ph, "link-out")); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{
		ph,                                 // exactly public_html root
		home,                               // not under public_html
		"/tmp",                             // absolute, not under it
		ph + "extra",                       // prefix-but-not-under trick
		ph + "/site/../other",              // raw traversal must fail closed
		ph + "/site/../..",                 // raw traversal above public_html
		filepath.Join(ph, "link-out/site"), // symlink-resolved escape
	} {
		err := runScriptExpectFail(t, home, emptyDestScript(), map[string]string{"DEST_DOCROOT": bad})
		if err == nil {
			t.Errorf("guard should have REFUSED %q (non-zero exit)", bad)
		}
	}

	// Nothing must have been deleted.
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("public_html sentinel was deleted by a guarded run: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("outside sentinel was deleted by a guarded run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "OUTSIDE_SENTINEL")); err != nil {
		t.Errorf("outside-dir sentinel was deleted by a guarded run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "site")); err == nil {
		t.Errorf("guarded run created a symlink-escaped target under %s", outsideDir)
	}
}

func TestExtractCmdGuardRefusesSymlinkEscapedDestination(t *testing.T) {
	sshtest.RequireTools(t, "tar", "bash")
	requireDestGuardTools(t)
	home := t.TempDir()
	srcRoot := t.TempDir()
	srcDoc := filepath.Join(srcRoot, "site")
	mk := func(root, rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk(srcDoc, "index.html", "should not escape")

	ph := filepath.Join(home, "public_html")
	outsideDir := filepath.Join(home, "outside-dir")
	if err := os.MkdirAll(ph, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(ph, "link-out")); err != nil {
		t.Fatal(err)
	}
	destDoc := filepath.Join(ph, "link-out", "site")
	srcCmd := sshx.WithEnv(srcTarCmd, map[string]string{"SRC_DOCROOT": srcDoc})
	destCmd := sshx.WithEnv(extractCmd, map[string]string{"DEST_DOCROOT": destDoc})

	if err := bridgeLocalExpectDestFail(t, home, srcCmd, "index.html\x00", destCmd); err == nil {
		t.Fatal("extractCmd must refuse a symlink-escaped destination")
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "site", "index.html")); err == nil {
		t.Fatal("extractCmd wrote through a symlink-escaped destination")
	}
}

// --- local helpers ---

// runScriptLocal runs a bash -s script with HOME overridden and env passed via
// the process environment, returning stdout; fails the test on a non-zero exit.
// TestGatherScriptUnreadableDocrootEmitsMarker drives BOTH gather scripts against a
// real chmod-000 docroot: it must emit the UNREADABLE marker (not "0|0"/END or
// ABSENT), so a permission problem is not mistaken for an empty docroot. Skipped
// under root, which bypasses permission bits.
func TestGatherScriptUnreadableDocrootEmitsMarker(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find")
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission bits; cannot simulate an unreadable docroot")
	}
	home := t.TempDir()
	doc := filepath.Join(home, "locked")
	if err := os.MkdirAll(doc, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(doc, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(doc, 0o700) })

	if out := strings.TrimSpace(runScriptLocal(t, home, gatherScript(), map[string]string{"DOCROOT": doc}, "")); out != "UNREADABLE" {
		t.Errorf("gatherScript on a chmod-000 docroot = %q, want UNREADABLE", out)
	}
	body := runScriptLocal(t, home, GatherAllScriptBody(), map[string]string{"DOCROOTS": "locked.it\t" + doc}, "")
	if !strings.Contains(body, "\nUNREADABLE\n") {
		t.Errorf("GatherAllScriptBody on a chmod-000 docroot must emit UNREADABLE:\n%s", body)
	}
	if strings.Contains(body, "\nEND\n") || strings.Contains(body, "\nABSENT\n") {
		t.Errorf("an unreadable docroot must not emit END/ABSENT:\n%s", body)
	}
}

// TestGatherScriptEmptyReadableStaysEmpty is the root-SAFE over-fire guard: a
// genuinely empty BUT readable docroot must still report 0|0 / END (empty), never
// UNREADABLE — the new readability gate must not false-positive on a legit empty dir.
func TestGatherScriptEmptyReadableStaysEmpty(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find")
	home := t.TempDir()
	doc := filepath.Join(home, "empty")
	if err := os.MkdirAll(doc, 0o755); err != nil {
		t.Fatal(err)
	}
	if out := strings.TrimSpace(runScriptLocal(t, home, gatherScript(), map[string]string{"DOCROOT": doc}, "")); out != "0|0" {
		t.Errorf("empty readable docroot = %q, want 0|0 (empty, not UNREADABLE)", out)
	}
	body := runScriptLocal(t, home, GatherAllScriptBody(), map[string]string{"DOCROOTS": "empty.it\t" + doc}, "")
	if !strings.Contains(body, "\nEND\n") || strings.Contains(body, "UNREADABLE") {
		t.Errorf("empty readable docroot must emit END, not UNREADABLE:\n%s", body)
	}
}

// TestEmptyDestFailsWhenCleanupLeavesContent: the cleanup `rm` runs with its
// errors suppressed (2>/dev/null || true). If removal cannot complete — an
// immutable flag, a read-only mount, denied permission, a transient error — the
// docroot stays non-empty. emptyDestScript MUST fail closed in that case instead of
// reporting success, or CopyDocroot would extract OVER the leftover content and
// leave destination-only files live in production. A failing `rm` stub on PATH
// simulates the un-removable entry deterministically (root-safe: no chmod needed).
func TestEmptyDestFailsWhenCleanupLeavesContent(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	requireDestGuardTools(t)
	home := t.TempDir()
	destDoc := filepath.Join(home, "public_html", "example.com")
	mk := func(rel, content string) {
		p := filepath.Join(destDoc, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("OLD_JUNK.html", "stale destination content that the cleanup cannot remove")
	mk("cgi-bin/keep.cgi", "preserved system entry")

	// A `rm` that always fails: the cleanup removes nothing, so the docroot stays
	// populated — exactly the masked-error condition the gate must catch.
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "rm"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := execScriptPath(home, emptyDestScript(), map[string]string{"DEST_DOCROOT": destDoc}, binDir)
	if err == nil {
		t.Fatal("emptyDest must FAIL when the cleanup leaves user content behind, not report success")
	}
	// Precondition sanity: the stubbed rm really left the content in place.
	if _, statErr := os.Stat(filepath.Join(destDoc, "OLD_JUNK.html")); statErr != nil {
		t.Errorf("precondition: stubbed rm should have left OLD_JUNK.html behind: %v", statErr)
	}
}

// execScriptPath runs a script with binDir prepended to PATH so a stub binary
// there shadows the real one, returning a non-nil error on a non-zero exit.
func execScriptPath(home, script string, env map[string]string, binDir string) error {
	cmd := exec.Command("bash", "-s")
	cmd.Env = append(os.Environ(), "HOME="+home,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = strings.NewReader(script + "\n")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	return cmd.Run()
}

func runScriptLocal(t *testing.T, home, script string, env map[string]string, _ string) string {
	t.Helper()
	out, err := execScript(home, script, env)
	if err != nil {
		t.Fatalf("script failed: %v\n--- script ---\n%s", err, script)
	}
	return out
}

// runScriptExpectFail runs a script expecting a NON-zero exit; returns the error.
func runScriptExpectFail(t *testing.T, home, script string, env map[string]string) error {
	t.Helper()
	_, err := execScript(home, script, env)
	return err
}

func execScript(home, script string, env map[string]string) (string, error) {
	cmd := exec.Command("bash", "-s")
	cmd.Env = append(os.Environ(), "HOME="+home)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = strings.NewReader(script + "\n")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	return out.String(), err
}

// bridgeLocal wires srcCmd | dstCmd via io.Copy, like the SSH bridge.
func bridgeLocal(t *testing.T, home, srcCmd, fileList, dstCmd string) {
	t.Helper()
	sc := exec.Command("bash", "-c", srcCmd)
	sc.Env = append(os.Environ(), "HOME="+home)
	sc.Stdin = strings.NewReader(fileList)
	dc := exec.Command("bash", "-c", dstCmd)
	dc.Env = append(os.Environ(), "HOME="+home)

	srcOut, err := sc.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	dstIn, err := dc.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var srcErr, dstErr bytes.Buffer
	sc.Stderr = &srcErr
	dc.Stderr = &dstErr
	if err := dc.Start(); err != nil {
		t.Fatal(err)
	}
	if err := sc.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dstIn, srcOut); err != nil {
		t.Fatalf("bridge copy: %v", err)
	}
	dstIn.Close()
	if err := sc.Wait(); err != nil {
		t.Fatalf("src tar: %v (stderr: %s)", err, srcErr.String())
	}
	if err := dc.Wait(); err != nil {
		t.Fatalf("dst tar: %v (stderr: %s)", err, dstErr.String())
	}
}

func bridgeLocalExpectDestFail(t *testing.T, home, srcCmd, fileList, dstCmd string) error {
	t.Helper()
	sc := exec.Command("bash", "-c", srcCmd)
	sc.Env = append(os.Environ(), "HOME="+home)
	sc.Stdin = strings.NewReader(fileList)
	dc := exec.Command("bash", "-c", dstCmd)
	dc.Env = append(os.Environ(), "HOME="+home)

	srcOut, err := sc.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	dstIn, err := dc.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var srcErr, dstErr bytes.Buffer
	sc.Stderr = &srcErr
	dc.Stderr = &dstErr
	if err := dc.Start(); err != nil {
		t.Fatal(err)
	}
	if err := sc.Start(); err != nil {
		t.Fatal(err)
	}
	// The destination is EXPECTED to refuse and close its stdin early; the io.Copy
	// feeding that stdin (and the src tar upstream) then legitimately see a broken
	// pipe (EPIPE) — that IS the refusal under test, not a helper failure, and which
	// side observes it first is a race. Tolerate the broken pipe, drain any unread
	// source output so sc.Wait can never block on a full pipe, and assert ONLY that
	// the DESTINATION command itself exited non-zero (refused). The caller separately
	// checks nothing was written through the symlink.
	if _, cerr := io.Copy(dstIn, srcOut); cerr != nil && !isBrokenPipe(cerr) {
		t.Fatalf("io.Copy src->dst: %v (src stderr: %s)", cerr, srcErr.String())
	}
	_ = dstIn.Close()
	_, _ = io.Copy(io.Discard, srcOut) // let the src tar finish even if io.Copy stopped early
	_ = sc.Wait()                      // src tar may exit via SIGPIPE when the dest refuses early
	err = dc.Wait()
	if err == nil {
		t.Fatalf("destination unexpectedly succeeded (stderr: %s)", dstErr.String())
	}
	return err
}

// isBrokenPipe reports whether err is a broken-pipe / closed-pipe write error —
// the expected outcome when a downstream consumer (here a guard that refuses)
// closes its stdin before the producer finishes writing.
func isBrokenPipe(err error) bool {
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, io.ErrClosedPipe)
}

// relEntries lists files AND empty directories under root (so emptydir shows up),
// relative + sorted.
func relEntries(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if info.IsDir() {
			// Only record a directory if it is empty (mirrors what we assert on).
			entries, _ := os.ReadDir(p)
			if len(entries) == 0 {
				out = append(out, rel)
			}
			return nil
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out
}

// TestManifestLocalDropsTabInFilename is the integration regression for the Step-12
// tab-in-filename false-OK: it creates a REAL file whose name contains a literal TAB
// and runs the production manifestScript + parser. The tab file must be DROPPED as
// unsafe (absent from the manifest), never keyed under the truncated prefix "weird"
// (which could shadow a real entry and verify the wrong object). This pins the
// script↔parser byte-layout contract against the actual `find -printf`, not just
// unit string literals.
func TestManifestLocalDropsTabInFilename(t *testing.T) {
	sshtest.RequireTools(t, "find", "bash")
	home := t.TempDir()
	doc := filepath.Join(t.TempDir(), "sitefiles")
	write := func(rel, content string) {
		p := filepath.Join(doc, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.html", "ok")
	write("weird\tname.txt", "tabbed") // a literal TAB in the filename (valid on Linux)

	m := parseManifestLocal(t, home, doc, false)
	if _, ok := m["index.html"]; !ok {
		t.Errorf("the normal file must be in the manifest, got keys %v", m)
	}
	for k := range m {
		if strings.Contains(k, "\t") {
			t.Errorf("a tab-bearing path must never be keyed in the manifest: %q", k)
		}
		if k == "weird" {
			t.Errorf("the tab file must be dropped, not keyed under the truncated prefix %q", k)
		}
	}
}

// parseManifestLocal runs the REAL manifestScript over a docroot locally and
// parses its NUL-delimited records into a Manifest, the same way GetManifest does
// over SSH — so the round-trip test exercises the production script + parser. With
// deep=true it also parses the H digest records and attaches them.
func parseManifestLocal(t *testing.T, home, docroot string, deep bool) Manifest {
	t.Helper()
	out := runScriptLocal(t, home, manifestScript(deep), map[string]string{"DOCROOT": docroot}, "")
	m := make(Manifest)
	for _, rec := range strings.Split(out, "\x00") {
		if rel, hex, ok := parseDigestRecord(rec); ok {
			if e, exists := m[rel]; exists {
				e.Digest = hex
				m[rel] = e
			}
			continue
		}
		if rel, e, ok, _ := parseManifestRecord(rec); ok {
			m[rel] = e
		}
	}
	return m
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func equalSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}
