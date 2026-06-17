package dbmig

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
)

// S3 fault-injection for the atomic config writer guard (dbmig.go::writeConfigScript).
// Invariant: a hostile destination path is refused with the DOCUMENTED exit code
// (21 containment: non-absolute / `..` / outside HOME, 22 not-found / not-a-regular-
// file) and the escape target / original is left intact with no temp file leaked; a
// valid in-HOME path is written ATOMICALLY (mktemp+mv), preserving the original mode.
//
// writeconfig_local_test.go already covers the ../-escape and symlink-escape outcomes;
// this adds the exit-code matrix, the no-temp-leak property, and the success-path
// atomicity/mode-preservation.

// writeConfigCode runs the REAL writeConfigScript() with a CLEAN env and returns the
// combined output + exit code.
func writeConfigCode(t *testing.T, home, wpconfig, newcontent string) (string, int) {
	t.Helper()
	sshtest.RequireTools(t, "bash", "realpath", "mktemp")
	cmd := exec.Command("bash", "-s")
	cmd.Env = []string{"HOME=" + home, "WPCONFIG=" + wpconfig, "NEWCONTENT=" + newcontent, "PATH=" + os.Getenv("PATH")}
	cmd.Stdin = strings.NewReader(writeConfigScript())
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("writeConfig exec error (not an exit): %v\n%s", err, out)
	}
	return string(out), code
}

// noTempLeak fails if any .dbmig.* temp file is left behind in dir.
func noTempLeak(t *testing.T, dir string) {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(dir, ".dbmig.*"))
	if len(matches) != 0 {
		t.Errorf("temp file(s) leaked in %s: %v", dir, matches)
	}
}

func TestFaultSimWriteConfigExitCodeMatrix(t *testing.T) {
	home := t.TempDir()
	// A real in-HOME config (the legitimate target for positive controls).
	good := filepath.Join(home, "wp-config.php")
	if err := os.WriteFile(good, []byte("ORIGINAL"), 0o640); err != nil {
		t.Fatal(err)
	}
	// A directory (resolved path is not a regular file) -> 22.
	if err := os.MkdirAll(filepath.Join(home, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	// An EXISTING file outside HOME (so the containment check, not the not-found check,
	// is what rejects it) -> 21; it must stay intact.
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "escape.php")
	if err := os.WriteFile(outside, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink inside HOME pointing OUTSIDE HOME -> 21 (only canonicalization catches it).
	symEscape := filepath.Join(home, "link.php")
	if err := os.Symlink(outside, symEscape); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
		want int
	}{
		{"relative path", "relative.php", 21},
		{"dotdot escape", home + "/sub/../../escape.php", 21},
		{"absolute outside HOME", outside, 21},
		{"symlink escapes HOME", symEscape, 21},
		{"not found", filepath.Join(home, "missing.php"), 22},
		{"resolved is a directory", filepath.Join(home, "adir"), 22},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, code := writeConfigCode(t, home, c.path, "PWNED")
			if code != c.want {
				t.Errorf("writeConfig(%q) exit %d, want %d\n%s", c.path, code, c.want, out)
			}
			noTempLeak(t, home)
		})
	}

	// Containment violations must NOT touch the escape target or the original.
	if b, _ := os.ReadFile(outside); string(b) != "ORIGINAL" {
		t.Errorf("the outside-HOME target was modified: %q", b)
	}
	if b, _ := os.ReadFile(good); string(b) != "ORIGINAL" {
		t.Errorf("the untargeted in-HOME config was modified: %q", b)
	}
	noTempLeak(t, outsideDir)
}

// TestFaultSimWriteConfigSuccessIsAtomicAndPreservesMode: a valid in-HOME write must
// succeed (exit 0, "OK"), replace the content, preserve the original file mode (the
// chmod --reference), and leave no temp file behind.
func TestFaultSimWriteConfigSuccessIsAtomicAndPreservesMode(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, "wp-config.php")
	if err := os.WriteFile(cfg, []byte("OLD"), 0o640); err != nil {
		t.Fatal(err)
	}
	out, code := writeConfigCode(t, home, cfg, "NEWBYTES")
	if code != 0 || !strings.Contains(out, "OK") {
		t.Fatalf("valid write must succeed (exit 0, OK), got exit %d: %s", code, out)
	}
	if b, _ := os.ReadFile(cfg); string(b) != "NEWBYTES" {
		t.Errorf("content not replaced: %q", b)
	}
	if fi, err := os.Stat(cfg); err != nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("mode not preserved: mode=%v err=%v (want 0640)", fi.Mode().Perm(), err)
	}
	noTempLeak(t, home)
}
