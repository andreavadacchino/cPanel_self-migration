package maildir

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

func TestDiffScans(t *testing.T) {
	before := []FileEntry{{RelPath: "cur/a"}, {RelPath: "cur/b"}, {RelPath: "cur/c"}}
	after := []FileEntry{{RelPath: "cur/b"}, {RelPath: "cur/c"}, {RelPath: "cur/d"}}

	vanished, appeared := diffScans(before, after)
	if !reflect.DeepEqual(vanished, []string{"cur/a"}) {
		t.Errorf("vanished = %v, want [cur/a]", vanished)
	}
	if !reflect.DeepEqual(appeared, []string{"cur/d"}) {
		t.Errorf("appeared = %v, want [cur/d]", appeared)
	}

	if v, a := diffScans(before, before); len(v) != 0 || len(a) != 0 {
		t.Errorf("identical scans must diff empty, got vanished=%v appeared=%v", v, a)
	}
}

func TestIsSourceVanishedFileErr(t *testing.T) {
	src := func(s string) error { return &sshx.BridgeSideError{Side: sshx.SideSource, Err: errors.New(s)} }
	dst := func(s string) error { return &sshx.BridgeSideError{Side: sshx.SideDest, Err: errors.New(s)} }

	match := []error{
		src(`source: "tar -c ..." failed: status 2 (stderr: tar: cur/1.M1.host:2,S: Cannot stat: No such file or directory)`),
		src("tar: foo: No such file or directory"),
		// Both sides failed (source aborted on the vanished member, so the dest tar
		// hit a truncated archive): the source-vanished signal must still be found.
		errors.Join(dst("tar: Unexpected EOF in archive"), src("tar: x: Cannot stat: No such file or directory")),
	}
	for _, e := range match {
		if !isSourceVanishedFileErr(e) {
			t.Errorf("expected a vanished-file match for %v", e)
		}
	}

	noMatch := []error{
		// The SAME ENOENT text but DEST-side must NOT match (the fix): a real dest
		// path error would otherwise be misread as a source mutation and loop on rescans.
		dst("tar: /home/u/mail/d.it/u/cur: No such file or directory"),
		src("tar: cur/x: Cannot open: Disk quota exceeded"), // source side but not ENOENT
		dst("use of closed connection"),
		errors.New("untagged: No such file or directory"), // not a bridge error at all
	}
	for _, e := range noMatch {
		if isSourceVanishedFileErr(e) {
			t.Errorf("did not expect a match for %v", e)
		}
	}
	if isSourceVanishedFileErr(nil) {
		t.Error("nil error must not match")
	}
}

func TestFirstN(t *testing.T) {
	if got := firstN([]string{"a", "b", "c"}, 2); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("firstN(.., 2) = %v, want [a b]", got)
	}
	if got := firstN([]string{"a"}, 5); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("firstN of a shorter slice must return it whole, got %v", got)
	}
}

// TestSyncBoxRescansWhenSourceFileVanishes drives the real SSH+tar bridge and uses
// the afterScan seam to mutate the LIVE source between the up-front scan and the
// copy: message #1 vanishes (expunged/renamed away) and #4 appears. The first batch
// references the now-missing #1, so the source tar aborts with "Cannot stat"; the
// re-scan path must drop #1, pick up #4, and finish — the destination ends an exact
// mirror of the CURRENT source, in a single SyncBox call.
func TestSyncBoxRescansWhenSourceFileVanishes(t *testing.T) {
	sshtest.RequireTools(t, "bash", "tar", "find", "mkdir")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	mkMailbox(t, srcHome, "d.it", "u", map[string]string{
		"cur/1.M1.host:2,S": "alpha",
		"cur/2.M2.host:2,S": "bravo",
		"cur/3.M3.host:2,S": "charlie",
		"dovecot-uidlist":   "1 V1 N4\n",
	})
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))
	defer dst.Close()

	box := filepath.Join(srcHome, "mail", "d.it", "u")
	tr := Transfer{Src: src, Dest: dst, MaxBytes: DefaultBatchMaxBytes}
	tr.afterScan = func() {
		if err := os.Remove(filepath.Join(box, "cur", "1.M1.host:2,S")); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(filepath.Join(box, "cur", "4.M4.host:2,S"), []byte("delta"), 0o644); err != nil {
			t.Error(err)
		}
	}

	if _, err := tr.SyncBox(context.Background(), "d.it", "u"); err != nil {
		t.Fatalf("SyncBox must recover from a vanished source file, got: %v", err)
	}

	got := relFiles(t, filepath.Join(dstHome, "mail", "d.it", "u"))
	want := []string{"cur/2.M2.host:2,S", "cur/3.M3.host:2,S", "cur/4.M4.host:2,S", "dovecot-uidlist"}
	if !equalStrs(got, want) {
		t.Errorf("dest = %v, want %v (vanished #1 dropped, appeared #4 copied)", got, want)
	}
}
