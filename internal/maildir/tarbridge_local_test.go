package maildir

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// This test validates the tar-streaming invariants of the bridge (§1) using
// REAL local tar processes wired by io.Copy — the same shape the SSH bridge
// uses (src `tar -c` | pipe | dest `tar -x`), without needing SSH. It proves:
//   - the whole maildir round-trips,
//   - dovecot.index* files are excluded but dovecot-uidlist is kept,
//   - a second run is idempotent (--keep-newer-files doesn't clobber).
func TestTarBridgeLocalRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not available")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	srcRoot := t.TempDir()
	dstRoot := t.TempDir()

	// Build a fake maildir under <srcRoot>/mail/dom.it/info
	box := filepath.Join("mail", "dom.it", "info")
	mk := func(rel, content string) {
		p := filepath.Join(srcRoot, box, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("cur/1.M1.host:2,S", "message one")
	mk("cur/2.M2.host:2,S", "message two")
	mk("new/3.M3.host", "message three")
	mk(".Sent Items/cur/4.M4.host:2,S", "message four in a folder with a space") // regression: spaced IMAP folder must round-trip
	mk("dovecot-uidlist", "3 V1687370761 N4 G1\n1 info1\n")
	mk("dovecot.index", "BINARY-INDEX-SHOULD-BE-EXCLUDED")
	mk("dovecot.index.log", "INDEX-LOG-EXCLUDED")
	mk("dovecot.list.index", "LIST-INDEX-EXCLUDED")

	// Gather the file list as the transfer would (relative, with sizes),
	// excluding index files — using the same exclude globs.
	files := listLocalFiles(t, filepath.Join(srcRoot, box))
	if len(files) == 0 {
		t.Fatal("no files listed")
	}
	var names []string
	for _, f := range files {
		names = append(names, f.RelPath)
	}
	// NUL-delimited list (matches the production --null --files-from path), fed to
	// the source tar via stdin — so a name with a space round-trips.
	fileList := strings.Join(names, "\x00") + "\x00"

	runBridge := func() {
		// SRC side: tar -c --null reading the NUL list from stdin, archive to stdout.
		srcCmd := exec.Command("bash", "-c",
			fmt.Sprintf(`cd %q && tar -c --null --no-recursion --files-from=- -f -`,
				filepath.Join(srcRoot, box)))
		srcCmd.Stdin = strings.NewReader(fileList)
		// DST side: tar -x --keep-newer-files into the destination box.
		dstBox := filepath.Join(dstRoot, box)
		dstCmd := exec.Command("bash", "-c",
			fmt.Sprintf(`mkdir -p %q && tar -x --keep-newer-files -C %q -f -`, dstBox, dstBox))

		srcOut, err := srcCmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		dstIn, err := dstCmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		var srcErr, dstErr bytes.Buffer
		srcCmd.Stderr = &srcErr
		dstCmd.Stderr = &dstErr

		if err := dstCmd.Start(); err != nil {
			t.Fatal(err)
		}
		if err := srcCmd.Start(); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(dstIn, srcOut); err != nil {
			t.Fatalf("bridge copy: %v", err)
		}
		dstIn.Close()
		if err := srcCmd.Wait(); err != nil {
			t.Fatalf("src tar: %v (stderr: %s)", err, srcErr.String())
		}
		if err := dstCmd.Wait(); err != nil {
			// --keep-newer-files can exit non-zero with warnings on re-run;
			// tolerate when stderr only mentions "newer".
			if !strings.Contains(dstErr.String(), "newer") {
				t.Fatalf("dst tar: %v (stderr: %s)", err, dstErr.String())
			}
		}
	}

	runBridge()

	// Verify the destination contents.
	gotFiles := relFiles(t, filepath.Join(dstRoot, box))
	want := []string{".Sent Items/cur/4.M4.host:2,S", "cur/1.M1.host:2,S", "cur/2.M2.host:2,S", "dovecot-uidlist", "new/3.M3.host"}
	if !equalStrs(gotFiles, want) {
		t.Errorf("destination files = %v, want %v", gotFiles, want)
	}
	// Index files must NOT be present.
	for _, idx := range []string{"dovecot.index", "dovecot.index.log", "dovecot.list.index"} {
		if _, err := os.Stat(filepath.Join(dstRoot, box, idx)); err == nil {
			t.Errorf("index file %q should have been excluded", idx)
		}
	}
	// uidlist content preserved (UIDVALIDITY survives).
	uid, _ := os.ReadFile(filepath.Join(dstRoot, box, "dovecot-uidlist"))
	if parseUIDValidity(strings.SplitN(string(uid), "\n", 2)[0]) != "V1687370761" {
		t.Errorf("dovecot-uidlist UIDVALIDITY not preserved: %q", uid)
	}

	// Idempotency: a second run must not error or change content.
	runBridge()
	gotFiles2 := relFiles(t, filepath.Join(dstRoot, box))
	if !equalStrs(gotFiles2, want) {
		t.Errorf("after re-run files = %v, want %v", gotFiles2, want)
	}
}

// TestControlFileStepReplacesStaleNewerUIDList validates the control-file step's
// --overwrite extract using REAL tar: a freshly-provisioned destination has a
// dovecot-uidlist with a DIFFERENT UIDVALIDITY and a NEWER mtime, so a plain
// `tar -x --keep-newer-files` (what message steps run) keeps the stale copy —
// while the final control step (--overwrite) installs the source's UIDVALIDITY.
// This is the per-step half of the multi-batch fix; planSyncSteps proves control
// files only ever go through the final --overwrite step.
func TestControlFileStepReplacesStaleNewerUIDList(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not available")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	box := filepath.Join("mail", "dom.it", "info")
	srcBox := filepath.Join(srcRoot, box)
	dstBox := filepath.Join(dstRoot, box)

	write := func(root, rel, content string) string {
		p := filepath.Join(root, box, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// Source uidlist carries the UIDVALIDITY we must preserve; give it an OLDER mtime.
	srcUID := write(srcRoot, "dovecot-uidlist", "3 V1687370761 N4 G1\n")
	// Destination has a freshly-provisioned uidlist: different UIDVALIDITY, NEWER mtime.
	dstUID := write(dstRoot, "dovecot-uidlist", "9 V9999999999 N9 G9\n")
	old := time.Now().Add(-2 * time.Hour)
	newer := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(srcUID, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dstUID, newer, newer); err != nil {
		t.Fatal(err)
	}

	fileList := "dovecot-uidlist\x00"
	runBridge := func(destBody string) {
		t.Helper()
		src := exec.Command("bash", "-c", fmt.Sprintf(`cd %q && tar -c --null --no-recursion --files-from=- -f -`, srcBox))
		src.Stdin = strings.NewReader(fileList)
		dst := exec.Command("bash", "-c", destBody)
		srcOut, err := src.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		dstIn, err := dst.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		var se, de bytes.Buffer
		src.Stderr, dst.Stderr = &se, &de
		if err := dst.Start(); err != nil {
			t.Fatal(err)
		}
		if err := src.Start(); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(dstIn, srcOut); err != nil {
			t.Fatalf("bridge copy: %v", err)
		}
		dstIn.Close()
		if err := src.Wait(); err != nil {
			t.Fatalf("src tar: %v (stderr: %s)", err, se.String())
		}
		if err := dst.Wait(); err != nil {
			// --keep-newer-files exits nonzero with "newer" warnings; tolerate those.
			if !strings.Contains(de.String(), "newer") {
				t.Fatalf("dst tar: %v (stderr: %s)", err, de.String())
			}
		}
	}
	readUV := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return parseUIDValidity(strings.SplitN(string(b), "\n", 2)[0])
	}

	// Message-step behavior (plain extract): --keep-newer-files keeps the newer dest
	// copy, so the stale UIDVALIDITY survives. Control files must NOT rely on this.
	runBridge(fmt.Sprintf(`cd %q && tar -x --keep-newer-files -f -`, dstBox))
	if uv := readUV(dstUID); uv != "V9999999999" {
		t.Fatalf("plain extract unexpectedly changed a newer dest uidlist to %s (test premise broken)", uv)
	}

	// Control-step behavior (--overwrite): the source copy replaces the newer dest
	// copy unconditionally, so the source UIDVALIDITY lands.
	runBridge(fmt.Sprintf(`cd %q && tar -x --overwrite -f -`, dstBox))
	if uv := readUV(dstUID); uv != "V1687370761" {
		t.Errorf("control step should install the source UIDVALIDITY V1687370761, got %s", uv)
	}
}

// TestMultiBatchPreservesControlFile is the end-to-end proof of the fix: a mailbox
// whose messages span MORE THAN ONE batch is transferred through the REAL
// planSyncSteps ordering with real tar. The dovecot-uidlist must survive and carry
// the SOURCE UIDVALIDITY. planSyncSteps puts control files in the single final
// --overwrite step, so a message batch never disturbs them.
func TestMultiBatchPreservesControlFile(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not available")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	box := filepath.Join("mail", "dom.it", "info")
	srcBox := filepath.Join(srcRoot, box)
	dstBox := filepath.Join(dstRoot, box)

	write := func(root, rel, content string) string {
		p := filepath.Join(root, box, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// Two 400-byte messages (so they split across batches at maxBytes=500) plus the
	// source dovecot-uidlist (older mtime).
	write(srcRoot, "cur/1.M1.host:2,S", strings.Repeat("a", 400))
	write(srcRoot, "cur/2.M2.host:2,S", strings.Repeat("b", 400))
	srcUID := write(srcRoot, "dovecot-uidlist", "3 V1687370761 N4 G1\n")
	// Destination starts with a freshly-provisioned, stale, NEWER uidlist.
	dstUID := write(dstRoot, "dovecot-uidlist", "9 V9999999999 N9 G9\n")
	old := time.Now().Add(-2 * time.Hour)
	newer := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(srcUID, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dstUID, newer, newer); err != nil {
		t.Fatal(err)
	}

	steps := planSyncSteps(listLocalFiles(t, srcBox), 500)
	if len(steps) < 2 {
		t.Fatalf("test premise: expected multiple steps (message batches + control step), got %d", len(steps))
	}

	runStep := func(st batchStep) {
		t.Helper()
		var names []string
		for _, f := range st.files {
			names = append(names, f.RelPath)
		}
		fileList := strings.Join(names, "\x00") + "\x00"
		destBody := fmt.Sprintf(`mkdir -p %q && cd %q && tar -x --keep-newer-files -f -`, dstBox, dstBox)
		if st.controlStep {
			destBody = fmt.Sprintf(`mkdir -p %q && cd %q && tar -x --overwrite -f -`, dstBox, dstBox)
		}
		src := exec.Command("bash", "-c", fmt.Sprintf(`cd %q && tar -c --null --no-recursion --files-from=- -f -`, srcBox))
		src.Stdin = strings.NewReader(fileList)
		dst := exec.Command("bash", "-c", destBody)
		srcOut, err := src.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		dstIn, err := dst.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		var se, de bytes.Buffer
		src.Stderr, dst.Stderr = &se, &de
		if err := dst.Start(); err != nil {
			t.Fatal(err)
		}
		if err := src.Start(); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(dstIn, srcOut); err != nil {
			t.Fatalf("bridge copy: %v", err)
		}
		dstIn.Close()
		if err := src.Wait(); err != nil {
			t.Fatalf("src tar: %v (stderr: %s)", err, se.String())
		}
		if err := dst.Wait(); err != nil {
			if !strings.Contains(de.String(), "newer") {
				t.Fatalf("dst tar: %v (stderr: %s)", err, de.String())
			}
		}
	}
	for _, st := range steps {
		runStep(st)
	}

	// Both messages AND the control file are present on the destination.
	got := relFiles(t, dstBox)
	for _, w := range []string{"cur/1.M1.host:2,S", "cur/2.M2.host:2,S", "dovecot-uidlist"} {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("destination missing %q after multi-batch sync: %v", w, got)
		}
	}
	// The control file carries the SOURCE UIDVALIDITY (not regenerated/stale).
	uid, _ := os.ReadFile(dstUID)
	if uv := parseUIDValidity(strings.SplitN(string(uid), "\n", 2)[0]); uv != "V1687370761" {
		t.Errorf("dovecot-uidlist UIDVALIDITY = %q, want source's V1687370761 (lost across batches?)", uv)
	}
}

func listLocalFiles(t *testing.T, root string) []FileEntry {
	t.Helper()
	var files []FileEntry
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		name := info.Name()
		for _, g := range []string{"dovecot.index", "dovecot.index.log", "dovecot.list.index"} {
			if name == g || strings.HasPrefix(name, "dovecot.index") || strings.HasPrefix(name, "dovecot.list.index") {
				return nil
			}
		}
		rel, _ := filepath.Rel(root, p)
		files = append(files, FileEntry{RelPath: rel, Size: info.Size()})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func relFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out
}

func equalStrs(a, b []string) bool {
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

// runTarBridge wires a real local `tar -c` (reading the NUL file list from stdin)
// to a destination shell command via io.Copy — the same shape streamOnce drives
// over SSH. destDir, when non-empty, becomes the dest shell's cwd (a stand-in for
// the connecting shell's $HOME). The copy error is intentionally ignored: when the
// dest command exits early (e.g. a failed mkdir aborts the `&&` chain before tar
// reads stdin) the write side sees EPIPE, which is expected — the meaningful
// signal is the dest command's exit status, returned alongside its stderr.
func runTarBridge(t *testing.T, srcBox, fileList, destBody, destDir string) (string, error) {
	t.Helper()
	src := exec.Command("bash", "-c", fmt.Sprintf(`cd %q && tar -c --null --no-recursion --files-from=- -f -`, srcBox))
	src.Stdin = strings.NewReader(fileList)
	dst := exec.Command("bash", "-c", destBody)
	if destDir != "" {
		dst.Dir = destDir
	}
	srcOut, err := src.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	dstIn, err := dst.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var se, de bytes.Buffer
	src.Stderr, dst.Stderr = &se, &de
	if err := dst.Start(); err != nil {
		t.Fatal(err)
	}
	if err := src.Start(); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(dstIn, srcOut) // EPIPE expected when the dest aborts early; see doc
	dstIn.Close()
	_ = src.Wait()
	// Wait() must complete before reading de: it joins the goroutine copying the
	// dest's stderr into the buffer (a plain return would read de concurrently).
	waitErr := dst.Wait()
	return de.String(), waitErr
}

// retarget rewrites the production extract command (which references "$HOME/$REL")
// to a concrete test box path, so the tests exercise the REAL destExtractCmd string
// rather than a hand-copied lookalike.
func retarget(cmd, box string) string {
	// box is "<root>/mail/<dom>/<user>". The production command now resolves the
	// mailbox tree from $HOME (the canonical containment guard), so export HOME=<root>
	// to match the retargeted absolute box; otherwise the guard's $HOME-anchored shape
	// check would reject the path for the wrong reason.
	root := box
	if i := strings.Index(box, "/mail/"); i >= 0 {
		root = box[:i]
	}
	return fmt.Sprintf("export HOME=%q\n", root) + strings.ReplaceAll(cmd, `"$HOME/$REL"`, fmt.Sprintf("%q", box))
}

// TestControlStepPreservesDestOnlyControlFile proves the control-file step (now an
// --overwrite extract, not a tree-wide delete) leaves a control file that exists
// ONLY on the destination — in a folder the source does not have — in place. The
// previous `find ... -delete` wiped every dest control file while the delta only
// re-sent source-present ones, so a dest-only .Junk/dovecot-uidlist was lost and
// Dovecot regenerated a fresh UIDVALIDITY (forcing every IMAP client to
// re-download). --overwrite replaces only the archive members, so it survives.
func TestControlStepPreservesDestOnlyControlFile(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not available")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	box := filepath.Join("mail", "dom.it", "info")
	srcBox := filepath.Join(srcRoot, box)
	dstBox := filepath.Join(dstRoot, box)
	write := func(root, rel, content string) {
		p := filepath.Join(root, box, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Source's control delta is just the top-level dovecot-uidlist.
	write(srcRoot, "dovecot-uidlist", "3 V1687370761 N4 G1\n")
	// Destination has that file (stale) AND a control file in a folder the source
	// does NOT have (.Junk), freshly provisioned with its own UIDVALIDITY.
	write(dstRoot, "dovecot-uidlist", "9 V9999999999 N9 G9\n")
	write(dstRoot, ".Junk/dovecot-uidlist", "5 V5555555555 N5 G5\n")

	// Run the REAL production control-step command, retargeted to the test box.
	if se, err := runTarBridge(t, srcBox, "dovecot-uidlist\x00", retarget(destExtractCmd(true), dstBox), ""); err != nil {
		t.Fatalf("control step failed: %v (stderr: %s)", err, se)
	}

	// The dest-only .Junk control file MUST survive untouched.
	junk, err := os.ReadFile(filepath.Join(dstBox, ".Junk", "dovecot-uidlist"))
	if err != nil {
		t.Fatalf("dest-only control file was wiped: %v", err)
	}
	if uv := parseUIDValidity(strings.SplitN(string(junk), "\n", 2)[0]); uv != "V5555555555" {
		t.Errorf(".Junk/dovecot-uidlist = %q, want its original V5555555555 (must be left untouched)", uv)
	}
	// And the top-level uidlist took the SOURCE UIDVALIDITY (overwritten).
	top, _ := os.ReadFile(filepath.Join(dstBox, "dovecot-uidlist"))
	if uv := parseUIDValidity(strings.SplitN(string(top), "\n", 2)[0]); uv != "V1687370761" {
		t.Errorf("top dovecot-uidlist = %q, want source's V1687370761", uv)
	}
}

// TestControlStepDoesNotScatterWhenMkdirFails proves the control-step command
// aborts — and never extracts into the connecting shell's $HOME — when the mailbox
// directory cannot be created. The pre-fix command ended the delete in
// `... -delete 2>/dev/null || true && tar -x`; because shell `&&`/`||` share
// precedence and associate left, the `|| true` rescued a failed mkdir/cd too, so
// `tar -x` still ran in $HOME, scattering cur/new/dovecot-uidlist across the home
// directory. The fixed command is a plain `&&` chain with no `|| true`.
func TestControlStepDoesNotScatterWhenMkdirFails(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not available")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	srcRoot := t.TempDir()
	box := filepath.Join("mail", "dom.it", "info")
	srcBox := filepath.Join(srcRoot, box)
	if err := os.MkdirAll(srcBox, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcBox, "dovecot-uidlist"), []byte("3 V1 N4 G1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// run builds a dest box whose creation FAILS (a regular file sits where the
	// box's parent directory must be), runs destBody with cwd set to a fresh empty
	// "home", and reports whether the maildir leaked into that home.
	run := func(destBodyFor func(box string) string) (scattered bool, exitErr error) {
		root := t.TempDir()
		// "mail" is a FILE, so `mkdir -p <root>/mail/dom.it/info` cannot succeed.
		if err := os.WriteFile(filepath.Join(root, "mail"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		home := t.TempDir() // simulated $HOME = cwd of the connecting shell
		_, exitErr = runTarBridge(t, srcBox, "dovecot-uidlist\x00", destBodyFor(filepath.Join(root, box)), home)
		_, statErr := os.Stat(filepath.Join(home, "dovecot-uidlist"))
		return statErr == nil, exitErr
	}

	// 1) Pre-fix form (kept here ONLY to demonstrate the regression): the trailing
	//    `|| true` masks the failed mkdir/cd, so tar -x runs in $HOME.
	oldBuggy := func(box string) string {
		return fmt.Sprintf(`mkdir -p %q && cd %q && find . -type f \( -name 'dovecot-uidlist' \) -delete 2>/dev/null || true && tar -x --keep-newer-files -f -`, box, box)
	}
	if scattered, _ := run(oldBuggy); !scattered {
		t.Fatal("test premise broken: the pre-fix command should have scattered the maildir into $HOME")
	}

	// 2) Fixed form (the REAL production command): a failed mkdir short-circuits the
	//    chain, so tar never runs and nothing lands in $HOME.
	scattered, exitErr := run(func(box string) string { return retarget(destExtractCmd(true), box) })
	if scattered {
		t.Error("fixed control step scattered the maildir into $HOME after a failed mkdir")
	}
	if exitErr == nil {
		t.Error("fixed control step should exit non-zero when mkdir fails (the batch must fail/retry, not report success)")
	}
}
