package migrate

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// These tests cover the two most critical SSH orchestration paths that depend
// only on GetBoxStats (mailbox count + UIDVALIDITY) — verify() (which decides the
// non-zero exit) and compareDryRun() — against an in-process SSH server that runs
// the real find/head/awk/wc in a temp HOME. Needs bash & friends; skipped otherwise.

// mkBox writes a mailbox INBOX with nMsgs message files (under cur/) and, if
// uid != "", a dovecot-uidlist whose UIDVALIDITY is uid.
func mkBox(t *testing.T, home, dom, user string, nMsgs int, uid string) {
	t.Helper()
	mkFolderBox(t, filepath.Join(home, "mail", dom, user), nMsgs, uid)
}

// mkFolder writes a Maildir++ SUBFOLDER (e.g. ".Archive") under a mailbox: a sibling
// ".Name" dir of the INBOX root with its own cur/ + dovecot-uidlist.
func mkFolder(t *testing.T, home, dom, user, folder string, nMsgs int, uid string) {
	t.Helper()
	mkFolderBox(t, filepath.Join(home, "mail", dom, user, folder), nMsgs, uid)
}

// mkFolderBox writes nMsgs message files under box/cur and, if uid != "", a
// dovecot-uidlist with that UIDVALIDITY — the shared body of mkBox / mkFolder.
func mkFolderBox(t *testing.T, box string, nMsgs int, uid string) {
	t.Helper()
	for i := 1; i <= nMsgs; i++ {
		p := filepath.Join(box, "cur", fmt.Sprintf("%d.M%d.host:2,S", i, i))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if uid != "" {
		if err := os.MkdirAll(box, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(box, "dovecot-uidlist"), []byte("1 "+uid+" N1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func discardReporter(t *testing.T) *report.Reporter {
	t.Helper()
	rep, err := report.NewReporter(io.Discard, io.Discard, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// bufReporter returns a Reporter whose FILE side is captured in the returned buffer
// (the screen side is discarded), so a test can assert the report-file lines.
func bufReporter(t *testing.T) (*report.Reporter, *strings.Builder) {
	t.Helper()
	var buf strings.Builder
	rep, err := report.NewReporter(io.Discard, &buf, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	return rep, &buf
}

// corruptMsg overwrites a destination message body in place, keeping its filename,
// so only the bytes differ (the deep body hash diverges while the count/name match).
func corruptMsg(t *testing.T, home, dom, user, name string) {
	t.Helper()
	p := filepath.Join(home, "mail", dom, user, "cur", name)
	if err := os.WriteFile(p, []byte("CORRUPT"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestVerifyIntegration exercises verify() across every classification: it must
// count only the REAL-loss divergences (INCOMPLETE + UIDVALIDITY mismatch) and
// EXCLUDE a benign DEST AHEAD, while a consistent box passes.
func TestVerifyIntegration(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "head", "awk", "wc", "mkdir")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	// ok: identical; incomplete: dest missing msgs; ahead: dest has extra (benign);
	// uidmismatch: same count, different UIDVALIDITY.
	mkBox(t, srcHome, "d.it", "ok", 2, "V1")
	mkBox(t, dstHome, "d.it", "ok", 2, "V1")
	mkBox(t, srcHome, "d.it", "incomplete", 3, "V1")
	mkBox(t, dstHome, "d.it", "incomplete", 1, "V1")
	mkBox(t, srcHome, "d.it", "ahead", 1, "V1")
	mkBox(t, dstHome, "d.it", "ahead", 2, "V1")
	mkBox(t, srcHome, "d.it", "uidmismatch", 2, "V1")
	mkBox(t, dstHome, "d.it", "uidmismatch", 2, "V2")

	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))}
	defer pool.Src.Close()
	defer pool.Dest.Close()

	pd := migrationData{
		DestDomainSet: map[string]bool{"d.it": true},
		Mailboxes: []model.Mailbox{
			{Domain: "d.it", User: "ok", Hash: "x"},
			{Domain: "d.it", User: "incomplete", Hash: "x"},
			{Domain: "d.it", User: "ahead", Hash: "x"},
			{Domain: "d.it", User: "uidmismatch", Hash: "x"},
		},
	}
	log := logx.NewTo(io.Discard, 0)

	realDiff, err := verify(context.Background(), pool, pd, log, discardReporter(t), false)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if realDiff != 2 {
		t.Errorf("verify realDiff = %d, want 2 (INCOMPLETE + UIDVALIDITY; DEST AHEAD excluded)", realDiff)
	}
}

func TestVerifyUsesRawDestinationDomainSpelling(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "head", "awk", "wc", "mkdir")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	mkBox(t, srcHome, "Example.COM", "info", 1, "V1")
	mkBox(t, dstHome, "example.com.", "info", 1, "V1")

	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))}
	defer pool.Src.Close()
	defer pool.Dest.Close()

	pd := migrationData{
		DestDomains:   []model.Domain{{Name: "example.com."}},
		DestDomainSet: cpanel.DomainNameSet([]model.Domain{{Name: "example.com."}}),
		Mailboxes:     []model.Mailbox{{Domain: "Example.COM", User: "info", Hash: "x"}},
	}
	realDiff, err := verify(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), discardReporter(t), false)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if realDiff != 0 {
		t.Fatalf("verify realDiff = %d, want 0 when destination raw domain spelling differs", realDiff)
	}
}

// TestVerifyPerFolderCatchesNetZero is the end-to-end regression for the aggregate
// blind spot: INBOX gains 5 on the destination while .Archive loses 5, so the
// whole-mailbox count is 20==20 and the OLD count-only verify passed. The
// per-folder verify must catch the .Archive shortfall as a real (INCOMPLETE)
// divergence and fail the run.
func TestVerifyPerFolderCatchesNetZero(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "head", "awk", "wc", "mkdir", "basename")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	// Source: INBOX 10 + .Archive 10 = 20. Destination: INBOX 15 + .Archive 5 = 20.
	mkBox(t, srcHome, "d.it", "u", 10, "V1")
	mkFolder(t, srcHome, "d.it", "u", ".Archive", 10, "V1")
	mkBox(t, dstHome, "d.it", "u", 15, "V1")
	mkFolder(t, dstHome, "d.it", "u", ".Archive", 5, "V1")

	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))}
	defer pool.Src.Close()
	defer pool.Dest.Close()

	pd := migrationData{
		DestDomainSet: map[string]bool{"d.it": true},
		Mailboxes:     []model.Mailbox{{Domain: "d.it", User: "u", Hash: "x"}},
	}
	realDiff, err := verify(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), discardReporter(t), false)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if realDiff != 1 {
		t.Errorf("verify realDiff = %d, want 1 (.Archive is INCOMPLETE despite the equal aggregate count)", realDiff)
	}
}

// TestVerifyDeepCatchesBodyCorruption: with deep=true, a message with the SAME
// filename but a CORRUPTED body on the destination must be flagged. Counts and
// UIDVALIDITY are identical (so the per-folder check passes), proving the deep
// content hash catches what metadata cannot.
func TestVerifyDeepCatchesBodyCorruption(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "sha256sum", "cut", "basename", "head", "awk", "wc")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	mkBox(t, srcHome, "d.it", "u", 3, "V1")
	mkBox(t, dstHome, "d.it", "u", 3, "V1")
	// Corrupt one destination message body, keeping its filename (so count + name
	// match, only the bytes differ). mkFolderBox wrote "x"; make it "CORRUPT".
	corrupt := filepath.Join(dstHome, "mail", "d.it", "u", "cur", "2.M2.host:2,S")
	if err := os.WriteFile(corrupt, []byte("CORRUPT"), 0o644); err != nil {
		t.Fatal(err)
	}

	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))}
	defer pool.Src.Close()
	defer pool.Dest.Close()

	pd := migrationData{
		DestDomainSet: map[string]bool{"d.it": true},
		Mailboxes:     []model.Mailbox{{Domain: "d.it", User: "u", Hash: "x"}},
	}

	// DEFAULT now ALSO catches it via the per-mailbox body fingerprint (V19/V27): the
	// per-folder counts + UIDVALIDITY match, but the body bytes differ.
	if rd, err := verify(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), discardReporter(t), false); err != nil || rd != 1 {
		t.Fatalf("non-deep verify: realDiff=%d err=%v, want 1 (default body fingerprint catches same-count corruption)", rd, err)
	}
	// With deep, the same-size/same-name body corruption is a real divergence.
	rd, err := verify(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), discardReporter(t), true)
	if err != nil {
		t.Fatalf("deep verify: %v", err)
	}
	if rd != 1 {
		t.Errorf("deep verify realDiff = %d, want 1 (one corrupted message body)", rd)
	}
}

// TestVerifyDeepCatchesCorruptedMessageWhenDestAhead is the regression for the
// DEST-AHEAD accounting bug: a mailbox whose destination has EXTRA mail (so the
// per-folder verdict is the soft DEST AHEAD) but whose one source message is ALSO
// corrupted on the destination must hard-fail. Source has message A; destination has
// A with a corrupted body PLUS an extra destination-only message B; UIDVALIDITY is
// equal. BOTH tiers must now catch it (M5): the DEFAULT body fingerprint compares only
// the source-present messages (ignoring the benign surplus) and promotes the corrupted
// source body to CONTENT; deep additionally reports DEST AHEAD + the one corrupted
// message. Neither tier reports the destination-only B as missing.
func TestVerifyDeepCatchesCorruptedMessageWhenDestAhead(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "sha256sum", "cut", "basename", "head", "awk", "wc")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	// Source: INBOX has 1 message (1.M1.host:2,S). Destination: INBOX has 2 messages
	// (1.M1.host + 2.M2.host) — so dest is AHEAD by one — then message 1 is corrupted.
	mkBox(t, srcHome, "d.it", "u", 1, "V1")
	mkBox(t, dstHome, "d.it", "u", 2, "V1")
	corruptMsg(t, dstHome, "d.it", "u", "1.M1.host:2,S")

	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))}
	defer pool.Src.Close()
	defer pool.Dest.Close()

	pd := migrationData{
		DestDomainSet: map[string]bool{"d.it": true},
		Mailboxes:     []model.Mailbox{{Domain: "d.it", User: "u", Hash: "x"}},
	}

	// Non-deep (M5): the default body fingerprint compares only the SOURCE-PRESENT
	// messages — it ignores the benign surplus but catches the corrupted source body and
	// promotes it to CONTENT (a hard diff). The destination-only B is NOT flagged.
	repND, bufND := bufReporter(t)
	if rd, err := verify(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), repND, false); err != nil || rd != 1 {
		t.Fatalf("non-deep verify: realDiff=%d err=%v, want 1 (default fingerprint catches the corrupted source body under DEST AHEAD)", rd, err)
	}
	if ndOut := bufND.String(); !strings.Contains(ndOut, "CONTENT") {
		t.Errorf("non-deep report should promote the corrupted DEST AHEAD mailbox to CONTENT:\n%s", ndOut)
	} else if strings.Contains(ndOut, "2.M2.host") {
		t.Errorf("the destination-only message B must NOT be reported at the default tier:\n%s", ndOut)
	}

	// Deep: the corrupted source-present body is real loss even though the mailbox is
	// DEST AHEAD overall — it must count as one hard difference.
	rep, buf := bufReporter(t)
	rd, err := verify(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, true)
	if err != nil {
		t.Fatalf("deep verify: %v", err)
	}
	if rd != 1 {
		t.Errorf("deep verify realDiff = %d, want 1 (corrupted body under a DEST AHEAD mailbox)", rd)
	}
	out := buf.String()
	if !strings.Contains(out, "DEST AHEAD") {
		t.Errorf("report should still show the DEST AHEAD folder verdict:\n%s", out)
	}
	if !strings.Contains(out, "1 message(s) corrupted, 0 missing") {
		t.Errorf("report should detail the one corrupted, zero missing body:\n%s", out)
	}
	if strings.Contains(out, "2.M2.host") {
		t.Errorf("the destination-only message B must NOT be reported as missing:\n%s", out)
	}
}

// TestVerifyDefaultBenignDestAheadStaysClean guards the M5 change against false
// positives: a DEST AHEAD mailbox whose SOURCE-PRESENT bodies all match (only extra
// dest-only mail) must stay a benign DEST AHEAD at the default tier (realDiff 0, no
// CONTENT), even though the body fingerprint now runs for DEST AHEAD mailboxes too.
func TestVerifyDefaultBenignDestAheadStaysClean(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "sha256sum", "cut", "basename", "head", "awk", "wc")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	// Source: 1 message. Destination: same message 1 (identical body) + extra message 2.
	mkBox(t, srcHome, "d.it", "u", 1, "V1")
	mkBox(t, dstHome, "d.it", "u", 2, "V1")

	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))}
	defer pool.Src.Close()
	defer pool.Dest.Close()

	pd := migrationData{
		DestDomainSet: map[string]bool{"d.it": true},
		Mailboxes:     []model.Mailbox{{Domain: "d.it", User: "u", Hash: "x"}},
	}
	rep, buf := bufReporter(t)
	rd, err := verify(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, false)
	if err != nil {
		t.Fatalf("non-deep verify: %v", err)
	}
	if rd != 0 {
		t.Errorf("benign DEST AHEAD realDiff = %d, want 0 (extra mail only, source bodies match)", rd)
	}
	out := buf.String()
	if strings.Contains(out, "CONTENT") {
		t.Errorf("benign DEST AHEAD must not be promoted to CONTENT:\n%s", out)
	}
	if !strings.Contains(out, "DEST AHEAD") {
		t.Errorf("benign surplus should still report DEST AHEAD:\n%s", out)
	}
}

// TestVerifyDeepNoDoubleCountIncompleteAndCorrupt guards the no-double-count rule: a
// mailbox that is BOTH folder-INCOMPLETE and body-corrupted is one hard difference,
// not two. Source has two messages; destination has one corrupted matching message
// and is missing the second. Deep verify must return 1 (not 2), with an INCOMPLETE
// headline and the content shown only as detail (no extra CONTENT summary bucket).
func TestVerifyDeepNoDoubleCountIncompleteAndCorrupt(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "sha256sum", "cut", "basename", "head", "awk", "wc")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	// Source: INBOX 2 messages. Destination: INBOX 1 message (missing the second),
	// and that one message is corrupted.
	mkBox(t, srcHome, "d.it", "u", 2, "V1")
	mkBox(t, dstHome, "d.it", "u", 1, "V1")
	corruptMsg(t, dstHome, "d.it", "u", "1.M1.host:2,S")

	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))}
	defer pool.Src.Close()
	defer pool.Dest.Close()

	pd := migrationData{
		DestDomainSet: map[string]bool{"d.it": true},
		Mailboxes:     []model.Mailbox{{Domain: "d.it", User: "u", Hash: "x"}},
	}
	rep, buf := bufReporter(t)
	rd, err := verify(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, true)
	if err != nil {
		t.Fatalf("deep verify: %v", err)
	}
	if rd != 1 {
		t.Errorf("deep verify realDiff = %d, want 1 (one hard mailbox: INCOMPLETE + corrupt is NOT two)", rd)
	}
	out := buf.String()
	if !strings.Contains(out, "1 INCOMPLETE") {
		t.Errorf("report summary should headline the mailbox as INCOMPLETE:\n%s", out)
	}
	if strings.Contains(out, "CONTENT:") {
		t.Errorf("a folder-hard mailbox must NOT also be counted under the CONTENT summary bucket:\n%s", out)
	}
}

// TestVerifyAbsentUIDListIsToleratedNotFalseFailed pins the deliberate decision NOT
// to hard-fail a healthy mailbox whose UIDVALIDITY is genuinely ABSENT. Both sides
// have the SAME non-zero count but NEITHER has a dovecot-uidlist — a legitimate state
// (Dovecot writes per-folder uidlists lazily; a Maildir++ subfolder delivered by
// Sieve/LMTP and never opened by a client has none, and the migration faithfully
// copies that absence). The matching count certifies the mail is present, so verify
// must return 0 here. (The genuinely-dangerous "UIDVALIDITY could not be READ" case —
// an unreadable or malformed uidlist — is caught upstream in the helper/parser, not
// by a blanket missing-UID rule that would permanently and unfixably fail this box.)
func TestVerifyAbsentUIDListIsToleratedNotFalseFailed(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "awk", "head", "wc")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	mkBox(t, srcHome, "d.it", "u", 3, "") // 3 messages, NO dovecot-uidlist
	mkBox(t, dstHome, "d.it", "u", 3, "") // same count, also NO dovecot-uidlist
	// A subfolder with mail but no uidlist on both sides — the exact lazy-uidlist case.
	mkFolder(t, srcHome, "d.it", "u", ".Drafts", 2, "")
	mkFolder(t, dstHome, "d.it", "u", ".Drafts", 2, "")

	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))}
	defer pool.Src.Close()
	defer pool.Dest.Close()

	pd := migrationData{
		DestDomainSet: map[string]bool{"d.it": true},
		Mailboxes:     []model.Mailbox{{Domain: "d.it", User: "u", Hash: "x"}},
	}
	rd, err := verify(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), discardReporter(t), false)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rd != 0 {
		t.Errorf("verify realDiff = %d, want 0 (matching counts certify the mail; absent uidlist is benign, not a hard fail)", rd)
	}
}

// TestVerifyFolderStatReadFailureIsUnreadable: when the destination mailbox path
// EXISTS but is not a readable maildir directory (here, a regular file), GetFolderStats
// fails closed; verify must surface that as one hard UNREADABLE difference, not a
// silent empty/clean read.
func TestVerifyFolderStatReadFailureIsUnreadable(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "awk", "head", "wc")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	mkBox(t, srcHome, "d.it", "u", 2, "V1")
	// Destination mailbox path is a regular FILE, so require_listable fails the script.
	if err := os.MkdirAll(filepath.Join(dstHome, "mail", "d.it"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstHome, "mail", "d.it", "u"), []byte("not a maildir"), 0o644); err != nil {
		t.Fatal(err)
	}

	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))}
	defer pool.Src.Close()
	defer pool.Dest.Close()

	pd := migrationData{
		DestDomainSet: map[string]bool{"d.it": true},
		Mailboxes:     []model.Mailbox{{Domain: "d.it", User: "u", Hash: "x"}},
	}
	rep, buf := bufReporter(t)
	rd, err := verify(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, false)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rd != 1 {
		t.Errorf("verify realDiff = %d, want 1 (unreadable destination mailbox)", rd)
	}
	if !strings.Contains(buf.String(), "UNREADABLE") {
		t.Errorf("report should flag the unreadable mailbox:\n%s", buf.String())
	}
}

// TestVerifyAbsentVsIncomplete pins the two boundary semantics the fail-closed work
// must preserve: both mailbox roots absent is clean (0), but a source mailbox with
// messages whose destination root is absent is hard INCOMPLETE (1).
func TestVerifyAbsentVsIncomplete(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "awk", "head", "wc")

	// Both absent -> clean.
	emptyPool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir())), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))}
	defer emptyPool.Src.Close()
	defer emptyPool.Dest.Close()
	pd := migrationData{
		DestDomainSet: map[string]bool{"d.it": true},
		Mailboxes:     []model.Mailbox{{Domain: "d.it", User: "u", Hash: "x"}},
	}
	if rd, err := verify(context.Background(), emptyPool, pd, logx.NewTo(io.Discard, 0), discardReporter(t), false); err != nil || rd != 0 {
		t.Fatalf("both-absent verify: realDiff=%d err=%v, want 0", rd, err)
	}

	// Source has mail, destination root absent -> INCOMPLETE.
	srcHome := t.TempDir()
	mkBox(t, srcHome, "d.it", "u", 4, "V1")
	incPool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))}
	defer incPool.Src.Close()
	defer incPool.Dest.Close()
	rep, buf := bufReporter(t)
	rd, err := verify(context.Background(), incPool, pd, logx.NewTo(io.Discard, 0), rep, false)
	if err != nil {
		t.Fatalf("incomplete verify: %v", err)
	}
	if rd != 1 {
		t.Errorf("source-only verify realDiff = %d, want 1 (INCOMPLETE)", rd)
	}
	if !strings.Contains(buf.String(), "INCOMPLETE") {
		t.Errorf("report should flag INCOMPLETE:\n%s", buf.String())
	}
}

// TestVerifyDeepCatchesWrongFolderSameCounts is the folder-aware deep-verify
// regression: source and destination have IDENTICAL per-folder counts and
// UIDVALIDITY, but two messages are in the WRONG folders on the destination (INBOX's
// message landed in .Sent and vice-versa). Non-deep verify is blind (counts match ->
// 0); deep verify, now keying message identity by folder + base ID, must catch it
// (realDiff 1) and name the wrong-folder messages folder-qualified.
func TestVerifyDeepCatchesWrongFolderSameCounts(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "awk", "head", "wc", "sha256sum", "cut", "basename")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	put := func(home, folder, name, body string) {
		dir := filepath.Join(home, "mail", "d.it", "u")
		if folder != "" {
			dir = filepath.Join(dir, folder)
		}
		if err := os.MkdirAll(filepath.Join(dir, "cur"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cur", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "dovecot-uidlist"), []byte("1 V1 N1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Source: INBOX has A, .Sent has B.
	put(srcHome, "", "A.host:2,S", "body-A")
	put(srcHome, ".Sent", "B.host:2,S", "body-B")
	// Destination: same per-folder counts + UIDVALIDITY, but A and B are swapped.
	put(dstHome, "", "B.host:2,S", "body-B")
	put(dstHome, ".Sent", "A.host:2,S", "body-A")

	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))}
	defer pool.Src.Close()
	defer pool.Dest.Close()

	pd := migrationData{
		DestDomainSet: map[string]bool{"d.it": true},
		Mailboxes:     []model.Mailbox{{Domain: "d.it", User: "u", Hash: "x"}},
	}
	// DEFAULT now catches the cross-folder swap too (V19/V27): per-folder counts and
	// UIDVALIDITY match, but the folder-aware body fingerprint differs (INBOX/A vs INBOX/B).
	if rd, err := verify(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), discardReporter(t), false); err != nil || rd != 1 {
		t.Fatalf("non-deep verify: realDiff=%d err=%v, want 1 (default folder-aware fingerprint catches the swap)", rd, err)
	}
	// Deep: folder-aware identity catches the wrong-folder messages.
	rep, buf := bufReporter(t)
	rd, err := verify(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, true)
	if err != nil {
		t.Fatalf("deep verify: %v", err)
	}
	if rd != 1 {
		t.Errorf("deep verify realDiff = %d, want 1 (messages in the wrong folders)", rd)
	}
	out := buf.String()
	if !strings.Contains(out, "INBOX/A.host") || !strings.Contains(out, ".Sent/B.host") {
		t.Errorf("report should name the wrong-folder messages folder-qualified (INBOX/A.host, .Sent/B.host):\n%s", out)
	}
}

// TestVerifyAbsentDomainClassification: a mailbox whose destination domain is absent
// must be classified, not silently skipped. The dangerous case (in scope, absent, NOT
// marked failed) must be a HARD UNVERIFIED so the run cannot exit 0 over unverified
// mail; a failed-domain or out-of-scope mailbox is a visible-but-not-counted skip.
func TestVerifyAbsentDomainClassification(t *testing.T) {
	cases := []struct {
		name         string
		pd           migrationData
		wantRealDiff int
		wantReport   string
	}{
		{
			"in-scope absent, not failed -> hard UNVERIFIED",
			migrationData{
				SrcDomains:    []model.Domain{{Name: "ghost.it"}}, // selected scope
				DestDomainSet: map[string]bool{},                  // but absent on dest
				Mailboxes:     []model.Mailbox{{Domain: "ghost.it", User: "info"}},
			},
			1, "UNVERIFIED",
		},
		{
			"failed-domain -> skip, not counted (FailedDomains already counts it)",
			migrationData{
				SrcDomains:    []model.Domain{{Name: "ghost.it"}},
				DestDomainSet: map[string]bool{},
				FailedDomains: map[string]bool{"ghost.it": true},
				Mailboxes:     []model.Mailbox{{Domain: "ghost.it", User: "info"}},
			},
			0, "SKIP",
		},
		{
			"blocked-domain -> skip, not counted (BlockedDomains already counts it)",
			migrationData{
				DestDomainSet:  map[string]bool{},
				BlockedDomains: map[string]string{"ghost.it": "domain absent from source domain inventory and destination; Step 8 cannot create it"},
				Mailboxes:      []model.Mailbox{{Domain: "ghost.it", User: "info"}},
			},
			0, "SKIP",
		},
		{
			// A selected mailbox is authoritative by pd.Mailboxes (the apply/compare
			// paths migrate by Mailboxes, never by SrcDomains). So an absent, not-failed
			// mailbox is a HARD issue even if its domain is not in SrcDomains — that
			// must NOT be a benign skip (it would silently lose selected mail).
			"absent + not failed + domain not in SrcDomains -> still hard UNVERIFIED",
			migrationData{
				SrcDomains:    []model.Domain{{Name: "other.it"}}, // ghost.it NOT listed
				DestDomainSet: map[string]bool{},
				Mailboxes:     []model.Mailbox{{Domain: "ghost.it", User: "info"}},
			},
			1, "UNVERIFIED",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir())), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))}
			defer pool.Src.Close()
			defer pool.Dest.Close()
			rep, buf := bufReporter(t)
			rd, err := verify(context.Background(), pool, c.pd, logx.NewTo(io.Discard, 0), rep, false)
			if err != nil {
				t.Fatalf("verify: %v", err)
			}
			if rd != c.wantRealDiff {
				t.Errorf("realDiff = %d, want %d", rd, c.wantRealDiff)
			}
			if !strings.Contains(buf.String(), c.wantReport) {
				t.Errorf("report should contain %q:\n%s", c.wantReport, buf.String())
			}
		})
	}
}

// TestVerifyFailedDomainSkipNotInflatedByHealthy: with one HEALTHY mailbox and one
// dependent on a FAILED domain, verify returns 0 — the healthy box passes and the
// failed-domain box is skipped (its root cause is counted by applyOutcome via
// len(FailedDomains)), so mailDiff is not double-inflated.
func TestVerifyFailedDomainSkipNotInflatedByHealthy(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "awk", "head", "wc")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	mkBox(t, srcHome, "ok.it", "u", 3, "V1")
	mkBox(t, dstHome, "ok.it", "u", 3, "V1")

	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))}
	defer pool.Src.Close()
	defer pool.Dest.Close()

	pd := migrationData{
		SrcDomains:    []model.Domain{{Name: "ok.it"}, {Name: "bad.it"}},
		DestDomainSet: map[string]bool{"ok.it": true}, // bad.it absent
		FailedDomains: map[string]bool{"bad.it": true},
		Mailboxes: []model.Mailbox{
			{Domain: "ok.it", User: "u", Hash: "x"},
			{Domain: "bad.it", User: "v"},
		},
	}
	rep, buf := bufReporter(t)
	rd, err := verify(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, false)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rd != 0 {
		t.Errorf("realDiff = %d, want 0 (healthy OK; failed-domain box skipped, counted under failed domains)", rd)
	}
	if !strings.Contains(buf.String(), "SKIP") {
		t.Errorf("report should show the failed-domain mailbox SKIPPED:\n%s", buf.String())
	}
}

// TestVerifySkipsNoHashMailbox is the F01 regression: a mailbox with NO source
// password hash (Hash=="") was never applied — applyMailboxes already counted it as
// UNVERIFIED (apply_mailboxes.go:80-85). verify() must not re-classify it as a
// divergence (which double-counts the same mailbox). Here the source has mail and
// the destination root is absent: under the OLD code this is INCOMPLETE (realDiff=1);
// under the fix it is an accounted SKIP (realDiff=0) whose reason names the missing
// hash. The destination DOMAIN is present, so the absent-domain branch does NOT fire
// first — this isolates the hash guard.
func TestVerifySkipsNoHashMailbox(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "awk", "head", "wc")
	srcHome := t.TempDir()
	mkBox(t, srcHome, "d.it", "u", 4, "V1") // source HAS mail; dest root absent

	pool := &sshx.Pool{
		Src:  sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)),
		Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir())), // empty dest
	}
	defer pool.Src.Close()
	defer pool.Dest.Close()

	pd := migrationData{
		DestDomainSet: map[string]bool{"d.it": true},                // domain present -> reach the hash guard
		Mailboxes:     []model.Mailbox{{Domain: "d.it", User: "u"}}, // Hash == "" (no source hash)
	}
	rep, buf := bufReporter(t)
	rd, err := verify(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, false)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rd != 0 {
		t.Errorf("verify realDiff = %d, want 0 (no-hash mailbox is an accounted SKIP, not a divergence)", rd)
	}
	out := buf.String()
	if !strings.Contains(out, "SKIP") || !strings.Contains(out, "no source password hash") {
		t.Errorf("report should SKIP the no-hash mailbox naming the missing hash:\n%s", out)
	}
	if strings.Contains(out, "INCOMPLETE") {
		t.Errorf("a no-hash mailbox must not also be re-classified INCOMPLETE (the double-count):\n%s", out)
	}
}

// TestVerifyNoHashConsistentDestNotMislabeledDomainIssue pins the reporting half of
// F01: even when the destination is fully consistent, a no-hash mailbox is SKIPPED
// (it could not have been applied), and its summary must be attributed to the missing
// hash — NOT folded into the "domain issue counted elsewhere" wording.
func TestVerifyNoHashConsistentDestNotMislabeledDomainIssue(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "awk", "head", "wc", "mkdir")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	mkBox(t, srcHome, "d.it", "u", 2, "V1")
	mkBox(t, dstHome, "d.it", "u", 2, "V1") // identical, would be OK if it had a hash

	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))}
	defer pool.Src.Close()
	defer pool.Dest.Close()

	pd := migrationData{
		DestDomainSet: map[string]bool{"d.it": true},
		Mailboxes:     []model.Mailbox{{Domain: "d.it", User: "u"}}, // Hash == ""
	}
	rep, buf := bufReporter(t)
	rd, err := verify(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, false)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rd != 0 {
		t.Errorf("verify realDiff = %d, want 0", rd)
	}
	out := buf.String()
	if !strings.Contains(out, "SKIP") || !strings.Contains(out, "no source password hash") {
		t.Errorf("a no-hash mailbox must be SKIPPED naming the missing hash, even with a consistent dest:\n%s", out)
	}
	if strings.Contains(out, "domain issue") {
		t.Errorf("a missing-hash skip must NOT be attributed to a domain issue:\n%s", out)
	}
}

// TestCompareDryRunIntegration drives the dry-run comparison renderer end-to-end:
// a present domain, a to-create domain, and mailboxes that are identical / differ
// / to-migrate. It asserts the human summary reflects each.
func TestCompareDryRunIntegration(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "head", "awk", "wc", "mkdir")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	mkBox(t, srcHome, "d.it", "same", 2, "V1")
	mkBox(t, dstHome, "d.it", "same", 2, "V1") // identical
	mkBox(t, srcHome, "d.it", "diff", 5, "V1")
	mkBox(t, dstHome, "d.it", "diff", 2, "V1") // differs

	src := sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))
	defer dst.Close()

	var buf strings.Builder
	log := logx.NewTo(&buf, 0)
	pd := migrationData{
		SrcDomains: []model.Domain{
			{Name: "d.it", Type: model.Addon},   // present on dest
			{Name: "new.it", Type: model.Addon}, // to create
		},
		DestDomainSet: map[string]bool{"d.it": true},
		Mailboxes: []model.Mailbox{
			{Domain: "d.it", User: "same"},
			{Domain: "d.it", User: "diff"},
			{Domain: "new.it", User: "x"}, // domain missing on dest -> TO MIGRATE
		},
	}
	c := &comparator{src: src, dest: dst}
	compareDryRun(context.Background(), c, pd, log, false)

	out := buf.String()
	if !strings.Contains(out, "domain summary") || !strings.Contains(out, "mailbox summary") {
		t.Errorf("compareDryRun output missing summaries:\n%s", out)
	}
	if !strings.Contains(out, "IDENTICAL") || !strings.Contains(out, "DIFFERS") || !strings.Contains(out, "TO MIGRATE") {
		t.Errorf("compareDryRun output missing a verdict:\n%s", out)
	}
}

// TestCompareDryRunMirrorIntegration drives the dry-run comparison in --apply-mirror
// mode: every mailbox reads "will mirror" (no IDENTICAL/DIFFERS), and a mailbox whose
// destination has MORE messages than the source is flagged as a destructive preview
// (dest-only mail that the mirror moves aside to -bak).
func TestCompareDryRunMirrorIntegration(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "head", "awk", "wc", "mkdir")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	mkBox(t, srcHome, "d.it", "norm", 3, "V1")
	mkBox(t, dstHome, "d.it", "norm", 3, "V1") // would be "identical" — under mirror: will mirror
	mkBox(t, srcHome, "d.it", "ahead", 2, "V1")
	mkBox(t, dstHome, "d.it", "ahead", 5, "V1") // dest AHEAD: 5 > 2 -> dest-only flagged

	src := sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))
	defer dst.Close()

	var buf strings.Builder
	log := logx.NewTo(&buf, 0)
	pd := migrationData{
		SrcDomains:    []model.Domain{{Name: "d.it", Type: model.Addon}},
		DestDomainSet: map[string]bool{"d.it": true},
		Mailboxes: []model.Mailbox{
			{Domain: "d.it", User: "norm"},
			{Domain: "d.it", User: "ahead"},
		},
	}
	c := &comparator{src: src, dest: dst}
	compareDryRun(context.Background(), c, pd, log, true)

	out := buf.String()
	if strings.Contains(out, "IDENTICAL") || strings.Contains(out, "DIFFERS") {
		t.Errorf("mirror comparison must NOT show IDENTICAL/DIFFERS verdicts:\n%s", out)
	}
	for _, want := range []string{
		"will mirror",
		"moved aside to -bak",   // the dest-ahead preview
		"2 to mirror",           // summary
		"1 with dest-only mail", // summary flag
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mirror comparison output missing %q:\n%s", want, out)
		}
	}
}

// TestVerifyDefaultBodyFingerprintFaithfulMirror is the V19/V27 positive control: a
// byte-for-byte identical mailbox passes the DEFAULT verify clean (rd=0) and is NOT
// downgraded to a soft "not byte-verified" note (sha256sum is present), proving the
// content fingerprint ran and matched rather than being skipped.
func TestVerifyDefaultBodyFingerprintFaithfulMirror(t *testing.T) {
	sshtest.RequireTools(t, "bash", "find", "sha256sum", "cut", "awk", "head", "wc", "basename")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	mkBox(t, srcHome, "d.it", "u", 3, "V1")
	mkBox(t, dstHome, "d.it", "u", 3, "V1")
	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	pd := migrationData{
		DestDomainSet: map[string]bool{"d.it": true},
		Mailboxes:     []model.Mailbox{{Domain: "d.it", User: "u", Hash: "x"}},
	}
	rep, buf := bufReporter(t)
	rd, err := verify(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, false)
	if err != nil || rd != 0 {
		t.Fatalf("faithful mirror default verify: realDiff=%d err=%v, want 0", rd, err)
	}
	if out := buf.String(); strings.Contains(out, "NOT byte-verified") {
		t.Fatalf("a faithful mirror with sha256sum present must verify content, not soft-note it:\n%s", out)
	}
}

// TestVerifyMailContentDigestOverCapIsSoftNote: a mailbox above the message cap is not
// hashed (no SSH round-trip) and returns a soft content-unverified note, never a hard
// fail nor a false "content verified". The cap is checked against max(src, dest), so a
// small-source / huge-destination DEST AHEAD mailbox is bounded by the dest side too.
func TestVerifyMailContentDigestOverCapIsSoftNote(t *testing.T) {
	// Source over the cap.
	differ, note := verifyMailContentDigest(context.Background(), &sshx.Pool{}, "d.it", "u", "d.it", defaultMailContentMsgCap+1, 0)
	if differ || note == "" {
		t.Fatalf("src over-cap: differ=%v note=%q, want differ=false and a non-empty note", differ, note)
	}
	if !strings.Contains(note, "cap") {
		t.Fatalf("src over-cap note should mention the cap: %q", note)
	}
	// Destination over the cap with a tiny source (the small-source/huge-dest case): the
	// dest-side bound must still skip the hash with a soft note (no unbounded dest hashing).
	differ, note = verifyMailContentDigest(context.Background(), &sshx.Pool{}, "d.it", "u", "d.it", 1, defaultMailContentMsgCap+1)
	if differ || note == "" {
		t.Fatalf("dest over-cap: differ=%v note=%q, want differ=false and a non-empty note", differ, note)
	}
	if !strings.Contains(note, "cap") {
		t.Fatalf("dest over-cap note should mention the cap: %q", note)
	}
}
