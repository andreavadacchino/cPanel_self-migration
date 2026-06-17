package maildir

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
)

func TestParseMirrorResult(t *testing.T) {
	if r := parseMirrorResult("BAKDIR jdoe-bak.2\n"); r.BackedUpDir != "jdoe-bak.2" {
		t.Errorf("BAKDIR -> %+v, want BackedUpDir=jdoe-bak.2", r)
	}
	if r := parseMirrorResult("NOBAK\n"); r.BackedUpDir != "" {
		t.Errorf("NOBAK -> %+v, want zero", r)
	}
	if r := parseMirrorResult(""); r.BackedUpDir != "" {
		t.Errorf("empty -> %+v, want zero", r)
	}
	if r := parseMirrorResult("garbage\n"); r.BackedUpDir != "" {
		t.Errorf("unknown -> %+v, want zero", r)
	}
}

// mirrorRequireBash skips when bash is unavailable.
func mirrorRequireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
}

// execMirrorScript runs mirrorBoxScript() locally with a clean environment so a
// duplicate USER/HOME in the inherited env cannot shadow the values we set
// (glibc getenv returns the FIRST match). stderr (GUARD messages) is discarded so
// the returned string is the single status line on stdout.
func execMirrorScript(home, dom, user string) (string, error) {
	cmd := exec.Command("bash", "-c", mirrorBoxScript())
	cmd.Env = []string{"HOME=" + home, "DOM=" + dom, "USER=" + user, "PATH=" + os.Getenv("PATH")}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	return stdout.String(), err
}

func runMirrorScript(t *testing.T, home, dom, user string) string {
	t.Helper()
	out, err := execMirrorScript(home, dom, user)
	if err != nil {
		t.Fatalf("mirrorBoxScript(%q/%q) failed: %v\n%s", dom, user, err, out)
	}
	return strings.TrimSpace(out)
}

// TestMirrorBoxScriptRenamesPopulatedMailbox: a populated dest mailbox is renamed
// aside to <user>-bak (all messages and control files move with it) and a fresh
// empty live mailbox is left in its place.
func TestMirrorBoxScriptRenamesPopulatedMailbox(t *testing.T) {
	mirrorRequireBash(t)
	home := t.TempDir()
	box := filepath.Join(home, "mail", "example.com", "jdoe")
	mirrorMustWrite(t, filepath.Join(box, "cur", "1700000000.M1.host:2,S"), "read msg")
	mirrorMustWrite(t, filepath.Join(box, "new", "1700000001.M2.host"), "unread msg")
	mirrorMustWrite(t, filepath.Join(box, "dovecot-uidlist"), "3 V1700000000 N5")

	if got := runMirrorScript(t, home, "example.com", "jdoe"); got != "BAKDIR jdoe-bak" {
		t.Fatalf("status = %q, want BAKDIR jdoe-bak", got)
	}
	bak := filepath.Join(home, "mail", "example.com", "jdoe-bak")

	// Old content moved into the backup.
	mirrorAssertExists(t, filepath.Join(bak, "cur", "1700000000.M1.host:2,S"))
	mirrorAssertExists(t, filepath.Join(bak, "new", "1700000001.M2.host"))
	mirrorAssertExists(t, filepath.Join(bak, "dovecot-uidlist"))
	// The live mailbox exists but is fresh (the old content is gone, ready for a full re-copy).
	mirrorAssertExists(t, box)
	mirrorAssertMissing(t, filepath.Join(box, "cur"))
	mirrorAssertMissing(t, filepath.Join(box, "dovecot-uidlist"))
}

// TestMirrorBoxScriptCollisionUsesNumberedBak: when <user>-bak already exists, the
// next free <user>-bak.N is used so a previous backup is never overwritten.
func TestMirrorBoxScriptCollisionUsesNumberedBak(t *testing.T) {
	mirrorRequireBash(t)
	home := t.TempDir()
	box := filepath.Join(home, "mail", "example.com", "jdoe")
	mirrorMustWrite(t, filepath.Join(box, "cur", "msg"), "current")
	mirrorMustWrite(t, filepath.Join(home, "mail", "example.com", "jdoe-bak", "old"), "prior backup") // -bak taken

	if got := runMirrorScript(t, home, "example.com", "jdoe"); got != "BAKDIR jdoe-bak.2" {
		t.Fatalf("status = %q, want BAKDIR jdoe-bak.2", got)
	}
	mirrorAssertExists(t, filepath.Join(home, "mail", "example.com", "jdoe-bak", "old")) // prior backup intact
	mirrorAssertExists(t, filepath.Join(home, "mail", "example.com", "jdoe-bak.2", "cur", "msg"))
}

// TestMirrorBoxScriptAbsentOrEmptyIsNoBak: an absent or already-empty mailbox is a
// no-op (NOBAK) — nothing is renamed and no useless -bak is created.
func TestMirrorBoxScriptAbsentOrEmptyIsNoBak(t *testing.T) {
	mirrorRequireBash(t)
	home := t.TempDir()

	// Absent mailbox -> NOBAK, and (unlike the docroot backup) the dir is NOT
	// pre-created: the subsequent tar copy / account creation makes it.
	if got := runMirrorScript(t, home, "example.com", "absent"); got != "NOBAK" {
		t.Fatalf("absent mailbox status = %q, want NOBAK", got)
	}
	mirrorAssertMissing(t, filepath.Join(home, "mail", "example.com", "absent"))

	// Empty mailbox dir -> NOBAK, left as-is, no -bak created.
	empty := filepath.Join(home, "mail", "example.com", "fresh")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := runMirrorScript(t, home, "example.com", "fresh"); got != "NOBAK" {
		t.Fatalf("empty mailbox status = %q, want NOBAK", got)
	}
	mirrorAssertExists(t, empty)
	mirrorAssertMissing(t, filepath.Join(home, "mail", "example.com", "fresh-bak"))
}

// TestMirrorBoxScriptRefusesEmptyDomOrUser: an empty DOM or USER would collapse the
// target path; the guard refuses with a non-zero exit and renames nothing.
func TestMirrorBoxScriptRefusesEmptyDomOrUser(t *testing.T) {
	mirrorRequireBash(t)
	home := t.TempDir()
	// Populate ~/mail so a buggy script that ignored the guard would have something
	// to (wrongly) rename.
	mirrorMustWrite(t, filepath.Join(home, "mail", "example.com", "jdoe", "cur", "msg"), "keep")

	for _, tc := range []struct{ dom, user string }{
		{"", "jdoe"},
		{"example.com", ""},
	} {
		if _, err := execMirrorScript(home, tc.dom, tc.user); err == nil {
			t.Errorf("mirrorBoxScript must refuse empty dom/user (dom=%q user=%q) with a non-zero exit", tc.dom, tc.user)
		}
	}
	// ~/mail and the real mailbox are untouched.
	mirrorAssertExists(t, filepath.Join(home, "mail", "example.com", "jdoe", "cur", "msg"))
	mirrorAssertMissing(t, filepath.Join(home, "mail-bak"))
}

// execSourceReadableScript runs sourceBoxReadableScript locally with a clean
// environment (same discipline as execMirrorScript) so a duplicate USER/HOME in
// the inherited env cannot shadow the values we set. stderr (GUARD messages) is
// discarded; the returned string is the single status line on stdout.
func execSourceReadableScript(home, dom, user string) (string, error) {
	cmd := exec.Command("bash", "-c", sourceBoxReadableScript)
	cmd.Env = []string{"HOME=" + home, "DOM=" + dom, "USER=" + user, "PATH=" + os.Getenv("PATH")}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	return stdout.String(), err
}

// TestSourceBoxReadableScriptStates locks the load-bearing distinction the
// --apply-mirror fail-closed gate depends on: an ABSENT source root must be
// distinguishable from a present-but-EMPTY one (GetBoxStats collapses both into
// "0|"/exit 0, which is exactly why this probe exists). Empty-but-present must
// stay PRESENT so mirroring to an empty source remains valid.
func TestSourceBoxReadableScriptStates(t *testing.T) {
	mirrorRequireBash(t)
	home := t.TempDir()

	// Absent root -> ABSENT (exit 0), NOT confused with empty.
	if out, err := execSourceReadableScript(home, "example.com", "absent"); err != nil || strings.TrimSpace(out) != "ABSENT" {
		t.Fatalf("absent source -> (%q, %v), want (ABSENT, nil)", strings.TrimSpace(out), err)
	}

	// Present-but-empty readable dir -> PRESENT (mirroring to empty is valid).
	if err := os.MkdirAll(filepath.Join(home, "mail", "example.com", "fresh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := execSourceReadableScript(home, "example.com", "fresh"); err != nil || strings.TrimSpace(out) != "PRESENT" {
		t.Fatalf("empty-but-present source -> (%q, %v), want (PRESENT, nil)", strings.TrimSpace(out), err)
	}

	// Populated -> PRESENT.
	mirrorMustWrite(t, filepath.Join(home, "mail", "example.com", "jdoe", "cur", "msg"), "x")
	if out, err := execSourceReadableScript(home, "example.com", "jdoe"); err != nil || strings.TrimSpace(out) != "PRESENT" {
		t.Fatalf("populated source -> (%q, %v), want (PRESENT, nil)", strings.TrimSpace(out), err)
	}
}

// TestSourceBoxReadableScriptRefusesEmptyDomOrUser: an empty DOM or USER would
// collapse the path to ~/mail; the guard must refuse with a non-zero exit so the
// probe never reports a phantom PRESENT for a missing identity.
func TestSourceBoxReadableScriptRefusesEmptyDomOrUser(t *testing.T) {
	mirrorRequireBash(t)
	home := t.TempDir()
	mirrorMustWrite(t, filepath.Join(home, "mail", "example.com", "jdoe", "cur", "msg"), "keep")
	for _, tc := range []struct{ dom, user string }{{"", "jdoe"}, {"example.com", ""}} {
		if _, err := execSourceReadableScript(home, tc.dom, tc.user); err == nil {
			t.Errorf("must refuse empty dom/user (dom=%q user=%q) with a non-zero exit", tc.dom, tc.user)
		}
	}
}

// TestSourceBoxReadableScriptFailsOnUnreadableRoot: a present-but-unreadable root
// must fail closed (non-zero) so the caller FAILs rather than wiping the live dest.
// Skipped under root, which bypasses permission bits.
func TestSourceBoxReadableScriptFailsOnUnreadableRoot(t *testing.T) {
	mirrorRequireBash(t)
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission bits; cannot simulate an unreadable root")
	}
	home := t.TempDir()
	box := filepath.Join(home, "mail", "example.com", "locked")
	if err := os.MkdirAll(box, 0o700); err != nil { // parents traversable...
		t.Fatal(err)
	}
	if err := os.Chmod(box, 0o000); err != nil { // ...root itself unreadable.
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(box, 0o700) })
	if _, err := execSourceReadableScript(home, "example.com", "locked"); err == nil {
		t.Error("must fail closed (non-zero) on an unreadable mailbox root")
	}
}

// TestSourceBoxReadableScriptNonDirectoryFailsClosed exercises the fail-closed
// error arm WITHOUT relying on permission bits (so it runs even as root, unlike
// TestSourceBoxReadableScriptFailsOnUnreadableRoot which skips): a regular FILE
// where the mailbox root must be a directory makes require_listable's `[ ! -d ]`
// branch fire a non-zero exit, which the caller maps to a FAIL.
func TestSourceBoxReadableScriptNonDirectoryFailsClosed(t *testing.T) {
	mirrorRequireBash(t)
	home := t.TempDir()
	mirrorMustWrite(t, filepath.Join(home, "mail", "example.com", "afile"), "not a maildir")
	if _, err := execSourceReadableScript(home, "example.com", "afile"); err == nil {
		t.Error("a non-directory mailbox root must fail closed (non-zero exit)")
	}
}

// TestSourceBoxReadableIntegration drives SourceBoxReadable over the in-process
// SSH bridge (exercising the DOM/USER env-passing end-to-end) and asserts the
// present/absent verdicts plus the read-only-source invariant.
func TestSourceBoxReadableIntegration(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "mkdir")
	srcHome := t.TempDir()
	mkMailbox(t, srcHome, "dom.it", "present", map[string]string{"cur/1.M1.host:2,S": "x"})
	srcBefore := snapshotTree(t, srcHome)

	src := sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome))
	defer src.Close()
	tr := Transfer{Src: src}

	if present, err := tr.SourceBoxReadable(context.Background(), "dom.it", "present"); err != nil || !present {
		t.Errorf("populated source -> (present=%v, err=%v), want (true, nil)", present, err)
	}
	if present, err := tr.SourceBoxReadable(context.Background(), "dom.it", "absent"); err != nil || present {
		t.Errorf("absent source -> (present=%v, err=%v), want (false, nil)", present, err)
	}
	if !sameTree(srcBefore, snapshotTree(t, srcHome)) {
		t.Error("SourceBoxReadable must not modify the source (read-only invariant)")
	}
}

// TestMirrorBoxIntegrationLeavesSourceReadOnly drives MirrorBox over the
// in-process SSH bridge — so the DOM/USER env-passing through sshx.WithEnv is
// exercised end-to-end, not just a local bash run — then re-copies from the
// source under Full mode (as applyMailboxes does under --apply-mirror), and
// asserts the three properties that matter:
//
//   - the populated dest mailbox is renamed aside to <user>-bak, and the
//     dest-only message (the DEST AHEAD case) is preserved THERE, out of the
//     live mailbox;
//   - after the copy the live dest mailbox mirrors the source EXACTLY (the
//     dest-only message is gone from it);
//   - the SOURCE tree is byte-for-byte unchanged — nothing in the mirror+copy
//     path ever writes to SRC.
func TestMirrorBoxIntegrationLeavesSourceReadOnly(t *testing.T) {
	sshtest.RequireTools(t, "bash", "tar", "find", "mkdir", "mv", "chmod", "basename")
	srcHome := t.TempDir()
	dstHome := t.TempDir()

	// Source: the authoritative set.
	mkMailbox(t, srcHome, "dom.it", "info", map[string]string{
		"cur/1.M1.host:2,S": "msg one",
		"new/2.M2.host":     "msg two",
		"dovecot-uidlist":   "3 V1687370761 N3\n",
	})
	// Destination: DEST AHEAD — an extra message and a whole Trash folder that are
	// NOT on the source (exactly what --apply-mirror removes), plus a stale uidlist.
	mkMailbox(t, dstHome, "dom.it", "info", map[string]string{
		"cur/1.M1.host:2,S":        "msg one",
		"cur/9.M9.host:2,S":        "ONLY ON DEST",
		".Trash/cur/8.M8.host:2,S": "dest-only trash",
		"dovecot-uidlist":          "99 V0000000000 N9\n",
	})

	srcBefore := snapshotTree(t, srcHome)

	src := sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))
	defer dst.Close()

	tr := Transfer{Src: src, Dest: dst, MaxBytes: DefaultBatchMaxBytes}

	// 1. Mirror prep: rename the populated dest mailbox aside.
	mr, err := tr.MirrorBox(context.Background(), "dom.it", "info")
	if err != nil {
		t.Fatalf("MirrorBox: %v", err)
	}
	if mr.BackedUpDir != "info-bak" {
		t.Fatalf("BackedUpDir = %q, want info-bak", mr.BackedUpDir)
	}
	// The dest-only mail is preserved in the backup, out of the live mailbox.
	mirrorAssertExists(t, filepath.Join(dstHome, "mail", "dom.it", "info-bak", "cur", "9.M9.host:2,S"))
	mirrorAssertExists(t, filepath.Join(dstHome, "mail", "dom.it", "info-bak", ".Trash", "cur", "8.M8.host:2,S"))

	// 2. Full re-copy from the source (Full=true, as applyMailboxes sets under --apply-mirror).
	tr.Full = true
	if _, err := tr.SyncBox(context.Background(), "dom.it", "info"); err != nil {
		t.Fatalf("SyncBox after mirror: %v", err)
	}

	// The live dest mailbox now mirrors the source EXACTLY: the dest-only message
	// and Trash folder are gone from the live mailbox.
	gotDest := relFiles(t, filepath.Join(dstHome, "mail", "dom.it", "info"))
	wantDest := []string{"cur/1.M1.host:2,S", "dovecot-uidlist", "new/2.M2.host"}
	if !equalStrs(gotDest, wantDest) {
		t.Errorf("live dest mailbox = %v, want %v (exact source mirror)", gotDest, wantDest)
	}

	// 3. The SOURCE is byte-for-byte unchanged — the read-only-source invariant.
	srcAfter := snapshotTree(t, srcHome)
	if !sameTree(srcBefore, srcAfter) {
		t.Errorf("SOURCE tree changed during mirror+copy: before=%v after=%v (src must stay read-only)", srcBefore, srcAfter)
	}
}

// snapshotTree records every file under root as rel-path -> content. Used to prove
// the source is left untouched.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(root, p)
		m[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func sameTree(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func mirrorMustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mirrorAssertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected %s to exist: %v", path, err)
	}
}

func mirrorAssertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("expected %s to be gone", path)
	}
}
