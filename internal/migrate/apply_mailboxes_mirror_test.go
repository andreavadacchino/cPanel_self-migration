package migrate

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// mirrorWriteFile writes content at path, creating parent dirs. Test helper.
func mirrorWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mirrorPathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected %s to exist: %v", path, err)
	}
}

func mirrorPathMissing(t *testing.T, path string) {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		t.Errorf("expected %s to be gone", path)
		return
	}
	if !os.IsNotExist(err) { // a permission/I-O error is NOT "gone" — fail, don't pass silently
		t.Errorf("stat %s: want not-exist, got %v", path, err)
	}
}

// TestApplyMailboxesMirrorAbsentSourceFailsClosed is the regression for Step 9 #4:
// under --apply-mirror, an ABSENT source mailbox must FAIL the mailbox BEFORE
// MirrorBox renames the live destination aside, so the live destination is never
// silently emptied with nothing to copy back. The dest account is pre-configured
// (shadow lists the user) so EnsureAccount returns UPDATED without calling uapi.
func TestApplyMailboxesMirrorAbsentSourceFailsClosed(t *testing.T) {
	sshtest.RequireTools(t, "bash", "awk", "mv", "chmod", "find")
	srcHome := t.TempDir()
	dstHome := t.TempDir()

	// Dest: configured account + a POPULATED live mailbox.
	mirrorWriteFile(t, filepath.Join(dstHome, "etc", "example.com", "shadow"), "info:OLDHASH\n")
	mirrorWriteFile(t, filepath.Join(dstHome, "mail", "example.com", "info", "cur", "1.msg"), "live dest mail")
	// Source: the mailbox root is ABSENT — mirroring would empty the live dest.

	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "s", "d", "now")
	if err != nil {
		t.Fatal(err)
	}
	pd := migrationData{
		DestDomainSet: cpanel.DomainNameSet([]model.Domain{{Name: "example.com"}}),
		Mailboxes:     []model.Mailbox{{Domain: "example.com", User: "info", Hash: "NEWHASH"}},
	}

	res, err := applyMailboxes(context.Background(), pool, config.Config{}, pd, Options{MirrorMail: true}, logx.NewTo(io.Discard, 0), rep)
	if err != nil {
		t.Fatalf("applyMailboxes: %v", err)
	}
	if res.failed != 1 {
		t.Fatalf("failed = %d, want 1 (an absent source must fail-close the destructive mirror)", res.failed)
	}
	out := file.String()
	if !strings.Contains(out, "absent on source") || !strings.Contains(out, "left intact") {
		t.Fatalf("report must say the source was absent and the live dest was left intact:\n%s", out)
	}
	// The live dest mailbox must be UNTOUCHED: the original message is still there
	// and MirrorBox never ran (no -bak). EnsureAccount took the UPDATED path, so any
	// -bak here would unambiguously be MirrorBox's destructive rename (the bug).
	mirrorPathExists(t, filepath.Join(dstHome, "mail", "example.com", "info", "cur", "1.msg"))
	mirrorPathMissing(t, filepath.Join(dstHome, "mail", "example.com", "info-bak"))
}

// TestApplyMailboxesMirrorEmptyReadableSourceProceeds proves the gate does NOT
// over-block: a present-but-EMPTY source is a valid mirror target, so MirrorBox
// still renames the live dest aside and the run does not FAIL.
func TestApplyMailboxesMirrorEmptyReadableSourceProceeds(t *testing.T) {
	sshtest.RequireTools(t, "bash", "awk", "mv", "chmod", "find", "tar", "mkdir", "basename", "head", "wc")
	srcHome := t.TempDir()
	dstHome := t.TempDir()

	// Source mailbox exists but is EMPTY (present + readable, no messages).
	if err := os.MkdirAll(filepath.Join(srcHome, "mail", "example.com", "info"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Dest: configured account + a populated live mailbox to be mirrored away.
	mirrorWriteFile(t, filepath.Join(dstHome, "etc", "example.com", "shadow"), "info:OLDHASH\n")
	mirrorWriteFile(t, filepath.Join(dstHome, "mail", "example.com", "info", "cur", "1.msg"), "dest-only mail")

	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "s", "d", "now")
	if err != nil {
		t.Fatal(err)
	}
	pd := migrationData{
		DestDomainSet: cpanel.DomainNameSet([]model.Domain{{Name: "example.com"}}),
		Mailboxes:     []model.Mailbox{{Domain: "example.com", User: "info", Hash: "NEWHASH"}},
	}

	res, err := applyMailboxes(context.Background(), pool, config.Config{}, pd, Options{MirrorMail: true}, logx.NewTo(io.Discard, 0), rep)
	if err != nil {
		t.Fatalf("applyMailboxes: %v", err)
	}
	if res.failed != 0 {
		t.Fatalf("failed = %d, want 0 (an empty-but-present source is a valid mirror target):\n%s", res.failed, file.String())
	}
	// MirrorBox ran: the previous dest-only mail was moved aside to <user>-bak.
	mirrorPathExists(t, filepath.Join(dstHome, "mail", "example.com", "info-bak", "cur", "1.msg"))
}

// TestApplyMailboxesMirrorUnreadableSourceFailsClosed covers the UNREADABLE arm of
// the gate at the migrate level WITHOUT permission bits (so it runs as root): a
// regular FILE where the source mailbox root must be a directory makes the probe
// fail closed. The destructive MirrorBox must not run — the live dest is intact.
func TestApplyMailboxesMirrorUnreadableSourceFailsClosed(t *testing.T) {
	sshtest.RequireTools(t, "bash", "awk", "mv", "chmod", "find")
	srcHome := t.TempDir()
	dstHome := t.TempDir()

	// Source: a regular file where mail/example.com/info must be a directory.
	mirrorWriteFile(t, filepath.Join(srcHome, "mail", "example.com", "info"), "not a maildir")
	// Dest: configured account + a POPULATED live mailbox.
	mirrorWriteFile(t, filepath.Join(dstHome, "etc", "example.com", "shadow"), "info:OLDHASH\n")
	mirrorWriteFile(t, filepath.Join(dstHome, "mail", "example.com", "info", "cur", "1.msg"), "live dest mail")

	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "s", "d", "now")
	if err != nil {
		t.Fatal(err)
	}
	pd := migrationData{
		DestDomainSet: cpanel.DomainNameSet([]model.Domain{{Name: "example.com"}}),
		Mailboxes:     []model.Mailbox{{Domain: "example.com", User: "info", Hash: "NEWHASH"}},
	}

	res, err := applyMailboxes(context.Background(), pool, config.Config{}, pd, Options{MirrorMail: true}, logx.NewTo(io.Discard, 0), rep)
	if err != nil {
		t.Fatalf("applyMailboxes: %v", err)
	}
	if res.failed != 1 {
		t.Fatalf("failed = %d, want 1 (an unreadable source must fail-close the destructive mirror)", res.failed)
	}
	out := file.String()
	if !strings.Contains(out, "unreadable") || !strings.Contains(out, "left intact") {
		t.Fatalf("report must say the source was unreadable and the live dest was left intact:\n%s", out)
	}
	mirrorPathExists(t, filepath.Join(dstHome, "mail", "example.com", "info", "cur", "1.msg"))
	mirrorPathMissing(t, filepath.Join(dstHome, "mail", "example.com", "info-bak"))
}

// TestApplyMailboxesMirrorPopulatedSourceMirrorsLiveDest is the end-to-end happy
// path through applyMailboxes: a populated source under --apply-mirror succeeds,
// the dest-only mail is moved aside to <user>-bak, and the live dest ends up an
// exact mirror of the source.
func TestApplyMailboxesMirrorPopulatedSourceMirrorsLiveDest(t *testing.T) {
	sshtest.RequireTools(t, "bash", "awk", "mv", "chmod", "find", "tar", "mkdir", "basename", "head", "wc")
	srcHome := t.TempDir()
	dstHome := t.TempDir()

	// Source: the authoritative mailbox.
	mirrorWriteFile(t, filepath.Join(srcHome, "mail", "example.com", "info", "cur", "1.msg"), "source mail")
	// Dest: configured account + a populated live mailbox carrying dest-only mail.
	mirrorWriteFile(t, filepath.Join(dstHome, "etc", "example.com", "shadow"), "info:OLDHASH\n")
	mirrorWriteFile(t, filepath.Join(dstHome, "mail", "example.com", "info", "cur", "9.msg"), "dest-only mail")

	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "s", "d", "now")
	if err != nil {
		t.Fatal(err)
	}
	pd := migrationData{
		DestDomainSet: cpanel.DomainNameSet([]model.Domain{{Name: "example.com"}}),
		Mailboxes:     []model.Mailbox{{Domain: "example.com", User: "info", Hash: "NEWHASH"}},
	}

	res, err := applyMailboxes(context.Background(), pool, config.Config{}, pd, Options{MirrorMail: true}, logx.NewTo(io.Discard, 0), rep)
	if err != nil {
		t.Fatalf("applyMailboxes: %v", err)
	}
	if res.failed != 0 {
		t.Fatalf("failed = %d, want 0 (a populated source must mirror cleanly):\n%s", res.failed, file.String())
	}
	// The dest-only mail is preserved in -bak, out of the live mailbox...
	mirrorPathExists(t, filepath.Join(dstHome, "mail", "example.com", "info-bak", "cur", "9.msg"))
	// ...and the live dest now mirrors the source exactly.
	mirrorPathExists(t, filepath.Join(dstHome, "mail", "example.com", "info", "cur", "1.msg"))
	mirrorPathMissing(t, filepath.Join(dstHome, "mail", "example.com", "info", "cur", "9.msg"))
}

// TestMirrorEmptiedLiveDest is the pure truth table for the V36 mirror-vanish
// predicate: FAIL only when live mail was set aside (-bak), the source held mail at
// the gate, AND the destination came back empty. Every other combination must NOT
// fire — a genuinely-empty source, a healthy refill, a partial shortfall (left to
// verify), or a NOBAK (no live data was at risk).
func TestMirrorEmptiedLiveDest(t *testing.T) {
	cases := []struct {
		name      string
		bak       string
		gateCount int
		destCount int
		want      bool
	}{
		{"vanish: live mail set aside, source had mail, dest empty", "info-bak", 5, 0, true},
		{"healthy: dest refilled to the source count", "info-bak", 5, 5, false},
		{"healthy: dest refilled (count differs but non-empty)", "info-bak", 5, 4, false},
		{"partial: dest below the gate count but non-empty — left to verify", "info-bak", 5, 1, false},
		{"empty source: occupancy 0 mirrors to empty legitimately", "info-bak", 0, 0, false},
		{"nobak: dest was already empty, no live data lost", "", 5, 0, false},
		{"nobak + empty source", "", 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mirrorEmptiedLiveDest(c.bak, c.gateCount, c.destCount); got != c.want {
				t.Errorf("mirrorEmptiedLiveDest(%q, %d, %d) = %v, want %v", c.bak, c.gateCount, c.destCount, got, c.want)
			}
		})
	}
}

// TestApplyMailboxesMirrorSourceVanishesAfterGateFailsClosed is the V36 regression:
// under --apply-mirror, a source proven occupied at the gate that vanishes BEFORE
// the copy reads it (the TOCTOU window) sends 0 files and leaves the just-emptied
// live destination empty. Pre-fix this was reported "synced" (clean exit); now the
// post-copy occupancy re-assertion FAILs the mailbox and points at <user>-bak. The
// mirrorVanishHook seam removes the source between the gate read and the copy scan.
func TestApplyMailboxesMirrorSourceVanishesAfterGateFailsClosed(t *testing.T) {
	sshtest.RequireTools(t, "bash", "awk", "mv", "chmod", "find", "tar", "mkdir", "basename", "head", "wc")
	srcHome := t.TempDir()
	dstHome := t.TempDir()

	// Source: a populated, present, readable mailbox at the gate.
	mirrorWriteFile(t, filepath.Join(srcHome, "mail", "example.com", "info", "cur", "1.msg"), "source mail")
	// Dest: configured account + a POPULATED live mailbox (so MirrorBox sets a -bak).
	mirrorWriteFile(t, filepath.Join(dstHome, "etc", "example.com", "shadow"), "info:OLDHASH\n")
	mirrorWriteFile(t, filepath.Join(dstHome, "mail", "example.com", "info", "cur", "9.msg"), "live dest mail")

	// TOCTOU: remove the source mailbox AFTER the occupancy gate recorded it but
	// BEFORE SyncBoxProgressDomains scans it, so the copy sees an empty source.
	mirrorVanishHook = func(string) {
		_ = os.RemoveAll(filepath.Join(srcHome, "mail", "example.com", "info"))
	}
	t.Cleanup(func() { mirrorVanishHook = nil })

	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "s", "d", "now")
	if err != nil {
		t.Fatal(err)
	}
	pd := migrationData{
		DestDomainSet: cpanel.DomainNameSet([]model.Domain{{Name: "example.com"}}),
		Mailboxes:     []model.Mailbox{{Domain: "example.com", User: "info", Hash: "NEWHASH"}},
	}

	res, err := applyMailboxes(context.Background(), pool, config.Config{}, pd, Options{MirrorMail: true}, logx.NewTo(io.Discard, 0), rep)
	if err != nil {
		t.Fatalf("applyMailboxes: %v", err)
	}
	if res.failed != 1 {
		t.Fatalf("failed = %d, want 1 (a source that vanished after the gate must FAIL, not report synced):\n%s", res.failed, file.String())
	}
	out := file.String()
	if !strings.Contains(out, "mirror left the destination EMPTY") || !strings.Contains(out, "info-bak") {
		t.Fatalf("report must name the empty-destination mirror failure and the -bak recovery dir:\n%s", out)
	}
	// The recovery path is intact: the original live mail is preserved in -bak...
	mirrorPathExists(t, filepath.Join(dstHome, "mail", "example.com", "info-bak", "cur", "9.msg"))
	// ...and the live destination is genuinely empty (the copy brought nothing back).
	mirrorPathMissing(t, filepath.Join(dstHome, "mail", "example.com", "info", "cur", "9.msg"))
	mirrorPathMissing(t, filepath.Join(dstHome, "mail", "example.com", "info", "cur", "1.msg"))
}
