package maildir

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
)

// S3 fault-injection for the mailbox-root containment guard (maildir/guard.go). The
// invariant: every hostile mailbox path is refused with the DOCUMENTED exit code
// (11 empty, 12 illegal shape/segments, 13 unresolvable, 14 escape/dangling-ancestor,
// 15 mailbox root is a symlink) — so the destructive cd/extract/rename downstream can
// never act on a path that escapes ~/mail. guard_mailbox_path has no permission-bit
// checks, so the codes are correct run as any user (root does NOT bypass `-L`/realpath).
//
// guard_test.go already covers allow/refuse outcomes; this adds the exact exit-code
// matrix and the symlink-CLASS distinctions (leaf-symlink=15 vs escaping/dangling
// ancestor=14).

// guardCode runs the REAL guard_mailbox_path with a CLEAN env (HOME+PATH only, so an
// inherited HOME cannot shadow the value) and returns combined output + exit code.
func guardCode(t *testing.T, home, path string) (string, int) {
	t.Helper()
	sshtest.RequireTools(t, "bash", "realpath")
	script := mailboxGuardScript() + `guard_mailbox_path "$1"` + "\n"
	cmd := exec.Command("bash", "-c", script, "_", path)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("guard exec error (not an exit): %v\n%s", err, out)
	}
	return string(out), code
}

func TestFaultSimGuardExitCodeMatrix(t *testing.T) {
	home := t.TempDir()
	// A real domain dir so leaf-level cases resolve under ~/mail.
	if err := os.MkdirAll(filepath.Join(home, "mail", "example.com", "jdoe", "cur"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Symlinked-leaf (mailbox root IS a symlink) -> 15.
	evil := t.TempDir()
	if err := os.Symlink(evil, filepath.Join(home, "mail", "example.com", "eviluser")); err != nil {
		t.Fatal(err)
	}
	// Escaping DOMAIN-dir symlink (target exists, outside ~/mail) -> 14.
	if err := os.Symlink(evil, filepath.Join(home, "mail", "evildom")); err != nil {
		t.Fatal(err)
	}
	// Dangling DOMAIN-dir symlink (target absent) -> 14.
	if err := os.Symlink(filepath.Join(evil, "nope"), filepath.Join(home, "mail", "danglingdom")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		home string
		path string
		want int
	}{
		{"empty path", home, "", 11},
		{"not under ~/mail (absolute)", home, "/etc/passwd", 12},
		{"not under ~/mail (in-home)", home, filepath.Join(home, "notmail", "d", "u"), 12},
		{"only one segment", home, filepath.Join(home, "mail", "onlydom"), 12},
		{"dotdot domain segment", home, home + "/mail/../etc/u", 12},
		{"dot user segment", home, home + "/mail/example.com/.", 12},
		{"dotdot user segment", home, home + "/mail/example.com/..", 12},
		{"unresolvable HOME", "/nonexistent-home-xyz", "/nonexistent-home-xyz/mail/d/u", 13},
		{"escaping domain symlink", home, filepath.Join(home, "mail", "evildom", "u"), 14},
		{"dangling domain symlink", home, filepath.Join(home, "mail", "danglingdom", "u"), 14},
		{"mailbox root is a symlink", home, filepath.Join(home, "mail", "example.com", "eviluser"), 15},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, code := guardCode(t, c.home, c.path)
			if code != c.want {
				t.Errorf("guard_mailbox_path(%q) exit %d, want %d\n%s", c.path, code, c.want, out)
			}
		})
	}

	// Positive controls: a normal existing mailbox and a fresh (absent) one must PASS.
	for _, p := range []string{
		filepath.Join(home, "mail", "example.com", "jdoe"),  // exists
		filepath.Join(home, "mail", "example.com", "fresh"), // absent leaf, valid
		filepath.Join(home, "mail", "newdom.com", "u"),      // absent domain, valid
	} {
		if out, code := guardCode(t, home, p); code != 0 {
			t.Errorf("guard_mailbox_path(%q) exit %d, want 0 (PASS)\n%s", p, code, out)
		}
	}
}
