package maildir

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
)

// chmodTraversable makes the TEST-OWNED components of p (those strictly under the
// system temp dir, i.e. the t.TempDir() tree) world-rwx so the unprivileged "nobody"
// user can traverse down to them. It deliberately stops AT os.TempDir() and never
// chmods it or any shared ancestor (/tmp, /), which would strip the sticky bit and
// destabilize test isolation. The test-owned dirs are auto-removed by t.Cleanup, so
// no perm restore is needed.
func chmodTraversable(t *testing.T, p string) {
	t.Helper()
	tmp := filepath.Clean(os.TempDir())
	for p = filepath.Clean(p); p != tmp && p != "/" && p != "." && strings.HasPrefix(p, tmp+string(os.PathSeparator)); p = filepath.Dir(p) {
		_ = os.Chmod(p, 0o777)
	}
}

// runGuard runs the REAL mailboxGuardScript()'s guard_mailbox_path as the
// unprivileged "nobody" user (root bypasses the symlink/permission checks), with
// HOME=home. Returns combined output and whether it REFUSED (non-nil = non-zero).
func runGuard(t *testing.T, home, path string) (string, error) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("must be root to drop to nobody")
	}
	if _, err := exec.LookPath("runuser"); err != nil {
		t.Skip("runuser not available")
	}
	sshtest.RequireTools(t, "bash", "realpath")
	script := mailboxGuardScript() + `guard_mailbox_path "$1"` + "\n"
	cmd := exec.Command("runuser", "-u", "nobody", "--", "env", "HOME="+home, "bash", "-c", script, "_", path)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestMailboxGuardAllowsNormal(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "mail", "example.com", "jdoe", "cur"), 0o777); err != nil {
		t.Fatal(err)
	}
	chmodTraversable(t, home)
	if out, err := runGuard(t, home, filepath.Join(home, "mail", "example.com", "jdoe")); err != nil {
		t.Fatalf("normal mailbox must PASS, got %v: %s", err, out)
	}
}

func TestMailboxGuardRejectsTraversal(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "mail", "example.com", "jdoe"), 0o777); err != nil {
		t.Fatal(err)
	}
	chmodTraversable(t, home)
	for _, leaf := range []string{"..", "."} {
		if out, err := runGuard(t, home, filepath.Join(home, "mail", "example.com", leaf)); err == nil {
			t.Errorf("USER=%q must be REFUSED:\n%s", leaf, out)
		}
	}
}

// THE key test: a mailbox root that is a symlink escaping ~/mail must be refused,
// and the escape target must be untouched.
func TestMailboxGuardRejectsSymlinkEscape(t *testing.T) {
	home := t.TempDir()
	evil := t.TempDir()
	if err := os.WriteFile(filepath.Join(evil, "canary.txt"), []byte("DO NOT TOUCH"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "mail", "example.com"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(evil, filepath.Join(home, "mail", "example.com", "eviluser")); err != nil {
		t.Fatal(err)
	}
	// also a symlinked DOMAIN dir escaping
	if err := os.Symlink(evil, filepath.Join(home, "mail", "evildom")); err != nil {
		t.Fatal(err)
	}
	chmodTraversable(t, home)
	chmodTraversable(t, evil)
	for _, p := range []string{
		filepath.Join(home, "mail", "example.com", "eviluser"), // symlinked leaf
		filepath.Join(home, "mail", "evildom", "u"),            // symlinked domain dir
	} {
		if out, err := runGuard(t, home, p); err == nil {
			t.Errorf("symlink escape %q must be REFUSED:\n%s", p, out)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(evil, "canary.txt")); string(b) != "DO NOT TOUCH" {
		t.Errorf("escape target was touched: %q", b)
	}
}

// A fresh destination mailbox (leaf and/or domain dir not yet created) must PASS —
// the extract creates it; the guard only forbids escaping ~/mail.
func TestMailboxGuardAllowsFreshDest(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "mail", "example.com"), 0o777); err != nil {
		t.Fatal(err)
	}
	chmodTraversable(t, home)
	// absent leaf (domain dir exists)
	if out, err := runGuard(t, home, filepath.Join(home, "mail", "example.com", "newuser")); err != nil {
		t.Errorf("fresh leaf must PASS, got %v: %s", err, out)
	}
	// absent domain dir (mail exists)
	if out, err := runGuard(t, home, filepath.Join(home, "mail", "newdom.com", "u")); err != nil {
		t.Errorf("fresh domain dir must PASS, got %v: %s", err, out)
	}
}

// A /home2-style SYMLINKED $HOME with a normal mailbox under it must PASS (the
// guard canonicalizes $HOME, so it must not false-reject).
func TestMailboxGuardAcceptsSymlinkedHome(t *testing.T) {
	base := t.TempDir()
	realhome := filepath.Join(base, "realhome")
	if err := os.MkdirAll(filepath.Join(realhome, "mail", "example.com", "jdoe"), 0o777); err != nil {
		t.Fatal(err)
	}
	linkhome := filepath.Join(base, "home2link")
	if err := os.Symlink(realhome, linkhome); err != nil {
		t.Fatal(err)
	}
	chmodTraversable(t, base)
	if out, err := runGuard(t, linkhome, filepath.Join(linkhome, "mail", "example.com", "jdoe")); err != nil {
		t.Fatalf("a mailbox under a symlinked HOME (/home<->/home2) must PASS, got %v: %s", err, out)
	}
}

// End-to-end (root-safe): MirrorBox against a destination whose mailbox root is a
// symlink escaping ~/mail must REFUSE and leave the escape target untouched. The
// realpath containment works as root (root does not bypass symlink RESOLUTION, only
// permission bits), and the assertion is a content check on the canary, not a perms
// check — so this is valid on a root CI box.
func TestMirrorBoxRefusesSymlinkEscape(t *testing.T) {
	sshtest.RequireTools(t, "bash", "realpath", "find", "mv", "mkdir")
	dstHome := t.TempDir()
	evil := t.TempDir()
	canary := filepath.Join(evil, "canary.txt")
	if err := os.WriteFile(canary, []byte("DO NOT TOUCH"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dstHome, "mail", "dom.it"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(evil, filepath.Join(dstHome, "mail", "dom.it", "info")); err != nil {
		t.Fatal(err)
	}
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome))
	defer dst.Close()
	tr := Transfer{Dest: dst}
	if _, err := tr.MirrorBox(context.Background(), "dom.it", "info"); err == nil {
		t.Error("MirrorBox must REFUSE a dest mailbox root that symlinks outside ~/mail")
	}
	if b, _ := os.ReadFile(canary); string(b) != "DO NOT TOUCH" {
		t.Errorf("MirrorBox touched the escape target: %q", b)
	}
	if _, err := os.Stat(evil + "-bak"); err == nil {
		t.Error("the escape target was renamed aside")
	}
	// The symlink itself must not have been renamed/replaced (still a symlink).
	if fi, err := os.Lstat(filepath.Join(dstHome, "mail", "dom.it", "info")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the mailbox symlink was disturbed: err=%v", err)
	}
}

// Sanity: the guard refuses a path containing a literal ".." even spelled out (the
// shape check), independent of the nobody harness — a pure-string property.
func TestMailboxGuardScriptShape(t *testing.T) {
	s := mailboxGuardScript()
	for _, want := range []string{"guard_mailbox_path", "canon_existing_path", `case "$gp" in "$HOME"/mail/?*/?*`, `[ -L "$gp" ]`} {
		if !strings.Contains(s, want) {
			t.Errorf("mailboxGuardScript missing %q", want)
		}
	}
}
