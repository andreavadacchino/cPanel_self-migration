package maildir

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
)

// These tests drive the REAL SSH transfer pipeline (SyncBox / listFilesOn /
// GetBoxStats / GetMessageSet) against an in-process SSH server that actually
// executes the remote bash/tar/find commands in a temp HOME. Two servers (one
// per HOME) stand in for source and destination, so a maildir is really copied
// SRC -> DEST over SSH+tar. Needs bash/tar/find; skipped otherwise.

// mkMailbox materializes a maildir at <home>/mail/<dom>/<user>/ from rel->content.
func mkMailbox(t *testing.T, home, dom, user string, files map[string]string) {
	t.Helper()
	box := filepath.Join(home, "mail", dom, user)
	for rel, content := range files {
		p := filepath.Join(box, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSyncBoxIntegration copies a whole mailbox SRC -> DEST over the in-process
// SSH bridge and verifies: the index file is excluded, every other file lands,
// and a second run re-sends ONLY the control file (delta-skip for messages,
// always-resend for dovecot-uidlist via the delete-carrying final step).
func TestSyncBoxIntegration(t *testing.T) {
	sshtest.RequireTools(t, "bash", "tar", "find", "mkdir")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	mkMailbox(t, srcHome, "dom.it", "info", map[string]string{
		"cur/1.M1.host:2,S":             "msg one",
		"cur/2.M2.host:2,S":             "msg two",
		"new/3.M3.host":                 "msg three",
		".Sent Items/cur/4.M4.host:2,S": "spaced-folder msg", // space in folder must survive
		"dovecot-uidlist":               "3 V1687370761 N4 G1\n1 info1\n",
		"dovecot.index":                 "BINARY-INDEX-EXCLUDED",
	})
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))
	defer dst.Close()

	tr := Transfer{Src: src, Dest: dst, MaxBytes: DefaultBatchMaxBytes}
	res, err := tr.SyncBox(context.Background(), "dom.it", "info")
	if err != nil {
		t.Fatalf("SyncBox: %v", err)
	}
	if res.FilesTotal != 5 { // 6 files minus the excluded index
		t.Errorf("FilesTotal = %d, want 5", res.FilesTotal)
	}
	if res.MsgTotal != 4 || res.ControlTotal != 1 { // 4 messages (cur/new) + 1 dovecot-uidlist
		t.Errorf("MsgTotal/ControlTotal = %d/%d, want 4/1", res.MsgTotal, res.ControlTotal)
	}
	if res.MsgTotal+res.ControlTotal != res.FilesTotal {
		t.Errorf("messages+control (%d+%d) must equal FilesTotal (%d)", res.MsgTotal, res.ControlTotal, res.FilesTotal)
	}
	if res.FilesSent != 5 {
		t.Errorf("FilesSent = %d, want 5 (dest was empty)", res.FilesSent)
	}

	got := relFiles(t, filepath.Join(dstHome, "mail", "dom.it", "info"))
	want := []string{".Sent Items/cur/4.M4.host:2,S", "cur/1.M1.host:2,S", "cur/2.M2.host:2,S", "dovecot-uidlist", "new/3.M3.host"}
	if !equalStrs(got, want) {
		t.Errorf("dest files = %v, want %v", got, want)
	}
	if _, err := os.Stat(filepath.Join(dstHome, "mail", "dom.it", "info", "dovecot.index")); err == nil {
		t.Error("dovecot.index must have been excluded from the transfer")
	}

	// Second run: messages already present (delta-skipped); the control file is
	// always re-sent via the final delete-carrying step.
	res2, err := tr.SyncBox(context.Background(), "dom.it", "info")
	if err != nil {
		t.Fatalf("SyncBox (2nd run): %v", err)
	}
	if res2.FilesSent != 1 {
		t.Errorf("2nd run FilesSent = %d, want 1 (only the control file re-sent)", res2.FilesSent)
	}
	uid, _ := os.ReadFile(filepath.Join(dstHome, "mail", "dom.it", "info", "dovecot-uidlist"))
	if parseUIDValidity(string(uid)) != "V1687370761" {
		t.Errorf("UIDVALIDITY not preserved after re-sync: %q", uid)
	}
}

// TestSyncBoxFullResendsEverything verifies Full mode skips the delta and streams
// every file even when the destination already has them.
func TestSyncBoxFullResendsEverything(t *testing.T) {
	sshtest.RequireTools(t, "bash", "tar", "find", "mkdir")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	mkMailbox(t, srcHome, "d.it", "u", map[string]string{
		"cur/1.M1.host:2,S": "a",
		"dovecot-uidlist":   "1 V1 N1\n",
	})
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))
	defer dst.Close()

	tr := Transfer{Src: src, Dest: dst, Full: true}
	res, err := tr.SyncBox(context.Background(), "d.it", "u")
	if err != nil {
		t.Fatalf("SyncBox(Full): %v", err)
	}
	if res.FilesSent != res.FilesTotal || res.FilesTotal != 2 {
		t.Errorf("Full sync sent %d/%d, want 2/2", res.FilesSent, res.FilesTotal)
	}
}

// TestSyncBoxEmptySourceNoOp: an empty (or absent) source mailbox is a no-op, not a
// failure — there is simply nothing to copy. (Returning an error here would mark a
// legitimately-empty ACTIVE account FAILED under --full / on a stats hiccup.)
func TestSyncBoxEmptySourceNoOp(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find")
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir())) // empty HOME, no mailbox
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))
	defer dst.Close()

	tr := Transfer{Src: src, Dest: dst}
	res, err := tr.SyncBox(context.Background(), "nope.it", "ghost")
	if err != nil {
		t.Fatalf("empty source must be a no-op success, got error: %v", err)
	}
	if res.FilesSent != 0 || res.FilesTotal != 0 || res.BytesSent != 0 {
		t.Errorf("empty source must yield a zero SyncResult, got %+v", res)
	}
}

// TestGetBoxStatsAndMessageSetIntegration drives the read-only stat/verify paths
// against a real mailbox over SSH.
func TestGetBoxStatsAndMessageSetIntegration(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "awk", "head", "wc")
	home := t.TempDir()
	mkMailbox(t, home, "dom.it", "info", map[string]string{
		"cur/1.M1.host:2,S":  "a",
		"cur/2.M2.host:2,FS": "b", // same base ID family; distinct message
		"new/3.M3.host":      "c",
		"dovecot-uidlist":    "5 V1687370761 N4 G1\n",
	})
	c := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer c.Close()

	bs, err := GetBoxStats(context.Background(), c, "dom.it", "info")
	if err != nil {
		t.Fatalf("GetBoxStats: %v", err)
	}
	if bs.MsgCount != 3 {
		t.Errorf("MsgCount = %d, want 3", bs.MsgCount)
	}
	if bs.UIDValidity != "V1687370761" {
		t.Errorf("UIDVALIDITY = %q, want V1687370761", bs.UIDValidity)
	}

	set, err := GetMessageSet(context.Background(), c, "dom.it", "info")
	if err != nil {
		t.Fatalf("GetMessageSet: %v", err)
	}
	if len(set) != 3 {
		t.Errorf("message set size = %d, want 3", len(set))
	}
}

// TestGetMessageSetFolderAwareIntegration drives the real GetMessageSet over SSH and
// asserts the SAME base ID in two folders yields TWO distinct folder-aware identities
// (the script must emit mailbox-relative paths, not basenames).
func TestGetMessageSetFolderAwareIntegration(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find")
	home := t.TempDir()
	mkMailbox(t, home, "dom.it", "info", map[string]string{
		"cur/A.host:2,S":       "x",
		".Sent/cur/A.host:2,S": "y", // same base ID A, different folder
		"new/B.host":           "z",
	})
	c := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer c.Close()

	set, err := GetMessageSet(context.Background(), c, "dom.it", "info")
	if err != nil {
		t.Fatalf("GetMessageSet: %v", err)
	}
	want := map[string]struct{}{
		messageIdentity("cur/A.host:2,S"):       {}, // INBOX/A.host
		messageIdentity(".Sent/cur/A.host:2,S"): {}, // .Sent/A.host
		messageIdentity("new/B.host"):           {}, // INBOX/B.host
	}
	if len(set) != 3 {
		t.Fatalf("message set size = %d, want 3 (same base ID in two folders must NOT merge): %v", len(set), set)
	}
	for k := range want {
		if _, ok := set[k]; !ok {
			t.Errorf("missing identity %q in %v", displayIdentity(k), set)
		}
	}
}

// TestGetMessageDigestsFolderAwareIntegration: the same base ID in two folders with
// DIFFERENT bodies must yield two distinct digest keys (no cross-folder overwrite).
func TestGetMessageDigestsFolderAwareIntegration(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "sha256sum", "cut", "basename")
	home := t.TempDir()
	mkMailbox(t, home, "dom.it", "info", map[string]string{
		"cur/A.host:2,S":       "inbox-body",
		".Sent/cur/A.host:2,S": "sent-body", // same base ID, different folder AND body
	})
	c := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer c.Close()

	md, err := GetMessageDigests(context.Background(), c, "dom.it", "info")
	if err != nil {
		t.Fatalf("GetMessageDigests: %v", err)
	}
	if len(md) != 2 {
		t.Fatalf("digest map size = %d, want 2 (same base ID across folders must be distinct): %v", len(md), md)
	}
	inbox := md[messageIdentity("cur/A.host:2,S")]
	sent := md[messageIdentity(".Sent/cur/A.host:2,S")]
	if inbox == "" || sent == "" {
		t.Fatalf("both folder keys must be present: INBOX=%q .Sent=%q", inbox, sent)
	}
	if inbox == sent {
		t.Errorf("distinct bodies must hash differently: %q == %q", inbox, sent)
	}
}

// TestGetMessageDigestsReportsProgress: the streamed digest read fires the
// WithProgress callback as it hashes (so the deep verify can animate a row) while
// still returning the correct, complete digest map.
func TestGetMessageDigestsReportsProgress(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "sha256sum", "cut")
	home := t.TempDir()
	mkMailbox(t, home, "dom.it", "info", map[string]string{
		"cur/1.host:2,S":       "a",
		"cur/2.host:2,S":       "b",
		"new/3.host":           "c",
		".Sent/cur/4.host:2,S": "d",
		"dovecot-uidlist":      "1 V1 N5\n", // control file, NOT a message → not hashed
	})
	c := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer c.Close()

	var counts []int
	md, err := GetMessageDigests(context.Background(), c, "dom.it", "info",
		WithProgress(func(n int) { counts = append(counts, n) }))
	if err != nil {
		t.Fatalf("GetMessageDigests: %v", err)
	}
	if len(md) != 4 { // 4 messages; the control file is not hashed
		t.Fatalf("digest map size = %d, want 4: %v", len(md), md)
	}
	if len(counts) == 0 {
		t.Fatal("WithProgress callback never fired — the row would stay frozen during hashing")
	}
	if counts[0] != 1 {
		t.Errorf("first progress tick = %d, want 1", counts[0])
	}
	for i := 1; i < len(counts); i++ {
		if counts[i] < counts[i-1] {
			t.Errorf("progress counts must be monotonic non-decreasing: %v", counts)
		}
	}
	if last := counts[len(counts)-1]; last > 4 {
		t.Errorf("last progress tick %d exceeds the 4 hashed messages", last)
	}
}

// TestGetMessageDigestsHandlesBackslashName: GNU sha256sum prefixes its output line
// with a literal backslash when the filename contains a backslash (a legal, non-control
// byte that transfers fine). The digest helper must strip that escape marker so the
// strict isSHA256 check sees a real 64-hex digest, not mislabel a healthy message as
// ?unreadable (which would make --deep-verify report a clean mailbox UNVERIFIED).
func TestGetMessageDigestsHandlesBackslashName(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "sha256sum", "cut", "basename")
	home := t.TempDir()
	mkMailbox(t, home, "dom.it", "info", map[string]string{
		`.Arch\ive/cur/Z.host:2,S`: "body",
	})
	c := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer c.Close()

	md, err := GetMessageDigests(context.Background(), c, "dom.it", "info")
	if err != nil {
		t.Fatalf("GetMessageDigests: %v", err)
	}
	got := md[messageIdentity(`.Arch\ive/cur/Z.host:2,S`)]
	if got == digestUnreadable || !isSHA256(got) {
		t.Errorf("backslash-named message digest = %q, want a real 64-hex sha256 (escape marker must be stripped)", got)
	}
}

// TestSyncBoxAlreadyInSync covers the "nothing to send" early return: a mailbox
// with only message files (no control files) is fully present on the second run,
// so the delta is empty.
func TestSyncBoxAlreadyInSync(t *testing.T) {
	sshtest.RequireTools(t, "bash", "tar", "find", "mkdir")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	mkMailbox(t, srcHome, "d.it", "u", map[string]string{
		"cur/1.M1.host:2,S": "a",
		"cur/2.M2.host:2,S": "b",
	}) // no control file -> nothing is "always re-sent"
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))
	defer dst.Close()

	tr := Transfer{Src: src, Dest: dst}
	if _, err := tr.SyncBox(context.Background(), "d.it", "u"); err != nil {
		t.Fatalf("SyncBox (1st run): %v", err)
	}
	res, err := tr.SyncBox(context.Background(), "d.it", "u")
	if err != nil {
		t.Fatalf("SyncBox (2nd run): %v", err)
	}
	if res.FilesSent != 0 {
		t.Errorf("2nd run FilesSent = %d, want 0 (already in sync)", res.FilesSent)
	}
}

// TestGetBoxStatsGuardRootRejectsSymlinkedDest drives the Go GuardRoot() option over
// real SSH: a DESTINATION read (GuardRoot) of a symlinked mailbox root must ERROR — so a
// fast-skip/verify can never read THROUGH a link the guarded copy refuses to write to —
// while the same read WITHOUT the guard (source semantics) follows the link and succeeds.
func TestGetBoxStatsGuardRootRejectsSymlinkedDest(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "awk", "head", "wc", "realpath")
	home := t.TempDir()
	mkMailbox(t, home, "dom.it", "real", map[string]string{
		"cur/1.M1.host:2,S": "a",
		"dovecot-uidlist":   "5 V1 N4 G1\n",
	})
	// The <user> root is a SYMLINK to the real mailbox, both under ~/mail.
	if err := os.Symlink(filepath.Join(home, "mail", "dom.it", "real"),
		filepath.Join(home, "mail", "dom.it", "info")); err != nil {
		t.Fatal(err)
	}
	c := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer c.Close()

	// Unguarded (source semantics): follows the symlink, reads the real mailbox.
	if bs, err := GetBoxStats(context.Background(), c, "dom.it", "info"); err != nil || bs.MsgCount != 1 {
		t.Fatalf("unguarded read must follow the symlinked root: bs=%+v err=%v", bs, err)
	}
	// Guarded (destination): the symlinked root is rejected.
	if _, err := GetBoxStats(context.Background(), c, "dom.it", "info", GuardRoot()); err == nil {
		t.Error("GuardRoot() must reject a symlinked destination mailbox root")
	}
	// A guarded read of an ABSENT dest (fresh, never created) must still succeed (count 0),
	// so the FIRST sync is not broken by the guard.
	if bs, err := GetBoxStats(context.Background(), c, "dom.it", "ghost", GuardRoot()); err != nil || bs.MsgCount != 0 {
		t.Errorf("guarded read of an absent dest must succeed with 0: bs=%+v err=%v", bs, err)
	}
}

// --- Error paths (robust: a CLOSED client fails at the SSH layer, so no remote
// command runs — these cover the error branches without needing bash/tar/find).

func TestGetBoxStatsErrorOnClosedClient(t *testing.T) {
	c := sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))
	_ = c.Close()
	if _, err := GetBoxStats(context.Background(), c, "d.it", "u"); err == nil {
		t.Error("GetBoxStats on a closed client must error")
	}
}

func TestGetMessageSetErrorOnClosedClient(t *testing.T) {
	c := sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))
	_ = c.Close()
	if _, err := GetMessageSet(context.Background(), c, "d.it", "u"); err == nil {
		t.Error("GetMessageSet on a closed client must error")
	}
}

// SyncBox must surface a failure to LIST the source mailbox.
func TestSyncBoxSourceListError(t *testing.T) {
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))
	_ = src.Close() // source listing fails
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))
	defer dst.Close()
	tr := Transfer{Src: src, Dest: dst}
	if _, err := tr.SyncBox(context.Background(), "d.it", "u"); err == nil {
		t.Error("SyncBox must error when listing the source fails")
	}
}

// SyncBox must surface a failure to LIST the destination (the delta step).
func TestSyncBoxDestListError(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find") // the source listing must succeed first
	srcHome := t.TempDir()
	mkMailbox(t, srcHome, "d.it", "u", map[string]string{"cur/1.M1.host:2,S": "x"})
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))
	_ = dst.Close()                     // dest listing fails
	tr := Transfer{Src: src, Dest: dst} // Full=false -> lists the dest for the delta
	if _, err := tr.SyncBox(context.Background(), "d.it", "u"); err == nil {
		t.Error("SyncBox must error when listing the dest fails")
	}
}

// A mailbox without a dovecot-uidlist yields an empty UIDVALIDITY (the
// missing-uidlist branch of GetBoxStats).
func TestGetBoxStatsNoUIDList(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "awk", "head", "wc")
	home := t.TempDir()
	mkMailbox(t, home, "d.it", "u", map[string]string{"cur/1.M1.host:2,S": "a"}) // no dovecot-uidlist
	c := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer c.Close()

	bs, err := GetBoxStats(context.Background(), c, "d.it", "u")
	if err != nil {
		t.Fatalf("GetBoxStats: %v", err)
	}
	if bs.MsgCount != 1 || bs.UIDValidity != "" {
		t.Errorf("got count=%d uid=%q, want count=1 and empty UIDVALIDITY", bs.MsgCount, bs.UIDValidity)
	}
}

// recSink records ProgressSink callbacks for assertions.
type recSink struct {
	total, added int64
	batches      int
}

func (r *recSink) SetTotal(b int64)  { r.total = b }
func (r *recSink) SetBatch(i, n int) { r.batches++ }
func (r *recSink) Add(n int64)       { r.added += n }

// TestSyncBoxProgressReportsToSink drives the progress-reporting branches of
// SyncBoxProgress and syncBatch: total bytes set up front, at least one batch,
// and the relayed byte count fed live.
func TestSyncBoxProgressReportsToSink(t *testing.T) {
	sshtest.RequireTools(t, "bash", "tar", "find", "mkdir")
	srcHome := t.TempDir()
	mkMailbox(t, srcHome, "d.it", "u", map[string]string{
		"cur/1.M1.host:2,S": "hello world",
		"dovecot-uidlist":   "1 V1 N1\n",
	})
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))
	defer dst.Close()

	sink := &recSink{}
	tr := Transfer{Src: src, Dest: dst}
	if _, err := tr.SyncBoxProgress(context.Background(), "d.it", "u", sink); err != nil {
		t.Fatalf("SyncBoxProgress: %v", err)
	}
	if sink.total <= 0 {
		t.Errorf("SetTotal = %d, want > 0", sink.total)
	}
	if sink.batches < 1 {
		t.Errorf("SetBatch called %d times, want >= 1", sink.batches)
	}
	if sink.added <= 0 {
		t.Errorf("Add total = %d, want > 0 (bytes relayed)", sink.added)
	}
}

func TestSyncBoxProgressDomainsUsesDestinationDomain(t *testing.T) {
	sshtest.RequireTools(t, "bash", "tar", "find", "mkdir")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	mkMailbox(t, srcHome, "Example.COM", "u", map[string]string{
		"cur/1.M1.host:2,S": "hello",
	})
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))
	defer dst.Close()

	tr := Transfer{Src: src, Dest: dst}
	if _, err := tr.SyncBoxProgressDomains(context.Background(), "Example.COM", "example.com.", "u", nil); err != nil {
		t.Fatalf("SyncBoxProgressDomains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dstHome, "mail", "example.com.", "u", "cur", "1.M1.host:2,S")); err != nil {
		t.Fatalf("message was not copied to destination raw domain path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dstHome, "mail", "Example.COM", "u", "cur", "1.M1.host:2,S")); err == nil {
		t.Fatalf("message was copied to source domain spelling on destination")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat source-spelled destination path: %v", err)
	}
}

// TestSyncBoxDestFailureRetries forces every destination extract to fail (mail/
// is a regular file, so mkdir -p can never create the mailbox), exercising
// syncBatch's full retry loop and final error.
func TestSyncBoxDestFailureRetries(t *testing.T) {
	sshtest.RequireTools(t, "bash", "tar", "find", "mkdir")
	srcHome := t.TempDir()
	mkMailbox(t, srcHome, "d.it", "u", map[string]string{"cur/1.M1.host:2,S": "x"})
	dstHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(dstHome, "mail"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))
	defer dst.Close()

	tr := Transfer{Src: src, Dest: dst}
	if _, err := tr.SyncBox(context.Background(), "d.it", "u"); err == nil {
		t.Error("SyncBox must fail (after retries) when the destination cannot extract")
	}
}
