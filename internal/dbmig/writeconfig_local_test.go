package dbmig

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
)

// execWriteConfig runs the REAL writeConfigScript() locally with a CLEAN env (so a
// duplicate HOME in the inherited env cannot shadow the value we set — glibc getenv
// returns the FIRST match). stdout+stderr are combined so GUARD messages are visible.
func execWriteConfig(t *testing.T, home, wpconfig, newcontent string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "-s")
	cmd.Env = []string{"HOME=" + home, "WPCONFIG=" + wpconfig, "NEWCONTENT=" + newcontent, "PATH=" + os.Getenv("PATH")}
	cmd.Stdin = strings.NewReader(writeConfigScript())
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// TestWriteConfigRejectsDotDotEscape: a config path with a literal ".." that lexically
// begins under HOME but resolves OUTSIDE it must be refused (the old lexical
// "$HOME"/?* glob accepted these). The escape target must be left untouched.
func TestWriteConfigRejectsDotDotEscape(t *testing.T) {
	sshtest.RequireTools(t, "bash", "realpath", "mktemp")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(home), "escape.txt")
	if err := os.WriteFile(outside, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	// Literal ".." kept by string concat (filepath.Join would clean it away).
	wpconfig := home + "/sub/../../escape.txt"
	out, err := execWriteConfig(t, home, wpconfig, "PWNED")
	if err == nil {
		t.Fatalf("must reject a ../ escape with a non-zero exit:\n%s", out)
	}
	if b, _ := os.ReadFile(outside); string(b) != "ORIGINAL" {
		t.Errorf("the escape target was written: %q", b)
	}
}

// TestWriteConfigRejectsSymlinkEscape: a config that is a SYMLINK resolving outside
// HOME must be refused (no literal "..", so only canonicalization catches it). The
// symlink target must not be overwritten.
func TestWriteConfigRejectsSymlinkEscape(t *testing.T) {
	sshtest.RequireTools(t, "bash", "realpath", "mktemp")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "site"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(home), "real-config.php")
	if err := os.WriteFile(outside, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	link := filepath.Join(home, "site", "wp-config.php")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	out, err := execWriteConfig(t, home, link, "PWNED")
	if err == nil {
		t.Fatalf("must reject a config symlink escaping HOME:\n%s", out)
	}
	if b, _ := os.ReadFile(outside); string(b) != "ORIGINAL" {
		t.Errorf("the symlink target was written: %q", b)
	}
}

// TestWriteConfigAcceptsLegitimateConfig: a normal config under HOME is rewritten.
func TestWriteConfigAcceptsLegitimateConfig(t *testing.T) {
	sshtest.RequireTools(t, "bash", "realpath", "mktemp")
	home := t.TempDir()
	dir := filepath.Join(home, "site")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "wp-config.php")
	if err := os.WriteFile(cfg, []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := execWriteConfig(t, home, cfg, "NEWCONTENT-XYZ")
	if err != nil {
		t.Fatalf("a legitimate config must be accepted: %v\n%s", err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Errorf("expected OK sentinel:\n%s", out)
	}
	if b, _ := os.ReadFile(cfg); string(b) != "NEWCONTENT-XYZ" {
		t.Errorf("config not updated: %q", b)
	}
}

// TestWriteConfigAcceptsSymlinkedHomeConfig is the false-positive guard for the
// canonical containment: cPanel routinely symlinks /home<->/home2, so $HOME itself is
// often a symlink. The guard must canonicalize $HOME too — a config reached via a
// symlinked HOME is LEGITIMATE and must be accepted (a guard that resolves only the
// target and compares against the raw $HOME would wrongly reject every /home2 host).
func TestWriteConfigAcceptsSymlinkedHomeConfig(t *testing.T) {
	sshtest.RequireTools(t, "bash", "realpath", "mktemp")
	realhome := t.TempDir()
	dir := filepath.Join(realhome, "site")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "wp-config.php")
	if err := os.WriteFile(cfg, []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkhome := filepath.Join(t.TempDir(), "home2link")
	if err := os.Symlink(realhome, linkhome); err != nil {
		t.Fatal(err)
	}
	out, err := execWriteConfig(t, linkhome, filepath.Join(linkhome, "site", "wp-config.php"), "NEW2")
	if err != nil {
		t.Fatalf("a config under a SYMLINKED HOME must be accepted (/home<->/home2): %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(cfg); string(b) != "NEW2" {
		t.Errorf("config not updated via symlinked HOME: %q", b)
	}
}

// TestWriteConfigDoesNotFollowFixedTempSymlink defeats the OLD fixed-temp attack: a
// symlink planted at the OLD predictable temp path ("$p.dbmig.tmp") pointing outside
// HOME must NOT be followed — mktemp uses an unpredictable name, so the victim is
// untouched and the real config is still rewritten.
func TestWriteConfigDoesNotFollowFixedTempSymlink(t *testing.T) {
	sshtest.RequireTools(t, "bash", "realpath", "mktemp")
	home := t.TempDir()
	dir := filepath.Join(home, "site")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "wp-config.php")
	if err := os.WriteFile(cfg, []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(filepath.Dir(home), "victim.txt")
	if err := os.WriteFile(victim, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(victim) })
	// Plant the OLD predictable temp name as a symlink to the victim.
	if err := os.Symlink(victim, cfg+".dbmig.tmp"); err != nil {
		t.Fatal(err)
	}

	out, err := execWriteConfig(t, home, cfg, "NEWCONTENT-XYZ")
	if err != nil {
		t.Fatalf("rewrite failed: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(victim); string(b) != "ORIGINAL" {
		t.Errorf("the planted fixed-temp symlink was followed (victim clobbered with DB creds): %q", b)
	}
	if b, _ := os.ReadFile(cfg); string(b) != "NEWCONTENT-XYZ" {
		t.Errorf("config not updated: %q", b)
	}
}

// TestWriteConfigPreservesPerms: the rewritten config inherits the original's
// permissions via chmod --reference.
func TestWriteConfigPreservesPerms(t *testing.T) {
	sshtest.RequireTools(t, "bash", "realpath", "mktemp")
	home := t.TempDir()
	dir := filepath.Join(home, "site")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "wp-config.php")
	if err := os.WriteFile(cfg, []byte("OLD"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg, 0o640); err != nil {
		t.Fatal(err)
	}
	if out, err := execWriteConfig(t, home, cfg, "NEW"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	fi, err := os.Stat(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("perms not preserved: got %o, want 640", fi.Mode().Perm())
	}
}

// TestWriteConfigPreservesContentByteExact locks the load-bearing `printf '%s'`
// contract: arbitrary config bytes (a literal %s, backslashes, a leading dash, single
// quotes, tabs, CR/LF) must be written byte-for-byte, not interpreted or mangled.
func TestWriteConfigPreservesContentByteExact(t *testing.T) {
	sshtest.RequireTools(t, "bash", "realpath", "mktemp")
	home := t.TempDir()
	dir := filepath.Join(home, "site")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "wp-config.php")
	if err := os.WriteFile(cfg, []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	content := "-leading\n100% \\back\\ 'quote' $NOT %s\ttab\r\nDONE"
	if out, err := execWriteConfig(t, home, cfg, content); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if b, _ := os.ReadFile(cfg); string(b) != content {
		t.Errorf("content not written byte-exact:\n got: %q\nwant: %q", b, content)
	}
}

// TestWriteConfigAcceptsPathWithSpaceAndGlob: a docroot path containing a space and a
// glob metacharacter must rewrite correctly (every expansion is quoted; no globbing).
func TestWriteConfigAcceptsPathWithSpaceAndGlob(t *testing.T) {
	sshtest.RequireTools(t, "bash", "realpath", "mktemp")
	home := t.TempDir()
	dir := filepath.Join(home, "my site [v2]")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "wp-config.php")
	if err := os.WriteFile(cfg, []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := execWriteConfig(t, home, cfg, "NEW-CONTENT"); err != nil {
		t.Fatalf("a path with a space/glob char must rewrite: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(cfg); string(b) != "NEW-CONTENT" {
		t.Errorf("config not updated: %q", b)
	}
}

// TestWriteConfigLiveConfigUnchangedOnWriteFailure: when the temp cannot be created
// (here: the config's directory is not writable), the script must fail closed and
// leave the LIVE config intact — never clobber it with an empty/partial file.
// Skipped under root, which bypasses directory write permissions.
func TestWriteConfigLiveConfigUnchangedOnWriteFailure(t *testing.T) {
	sshtest.RequireTools(t, "bash", "realpath", "mktemp")
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions; cannot force the temp create to fail")
	}
	home := t.TempDir()
	dir := filepath.Join(home, "site")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "wp-config.php")
	if err := os.WriteFile(cfg, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { // r-x, no write: mktemp in dir fails
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	out, err := execWriteConfig(t, home, cfg, "NEWCONTENT-XYZ")
	if err == nil {
		t.Fatalf("a failed temp create must fail closed (non-zero exit):\n%s", out)
	}
	if b, _ := os.ReadFile(cfg); string(b) != "ORIGINAL" {
		t.Errorf("the live config was clobbered on a write failure: %q", b)
	}
}
