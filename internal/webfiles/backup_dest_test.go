package webfiles

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseBackupResult(t *testing.T) {
	if r := parseBackupResult("BAKDIR addon1.example-bak.2\n"); r.BackedUpDir != "addon1.example-bak.2" {
		t.Errorf("BAKDIR -> %+v", r)
	}
	if r := parseBackupResult("NOBAK\n"); r.BackedUpDir != "" {
		t.Errorf("NOBAK -> %+v, want zero", r)
	}
	if r := parseBackupResult("garbage\n"); r.BackedUpDir != "" {
		t.Errorf("unknown -> %+v, want zero", r)
	}
}

// requireBash skips when bash is unavailable.
func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	requireDestGuardTools(t)
}

func requireDestGuardTools(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if err := exec.Command("realpath", "-m", "/").Run(); err == nil {
		return
	}
	if err := exec.Command("readlink", "-m", "/").Run(); err == nil {
		return
	}
	t.Skip("destination docroot guard requires realpath -m or readlink -m")
}

// TestBackupDestScriptBacksUpPopulatedAddon: a populated addon docroot is renamed
// aside to <docroot>-bak and a fresh empty docroot is left. .well-known is user
// content, so it is carried into the backup instead of being preserved as system
// state.
func TestBackupDestScriptBacksUpPopulatedAddon(t *testing.T) {
	requireBash(t)
	home := t.TempDir()
	doc := filepath.Join(home, "public_html", "addon1.example")
	mustWrite(t, filepath.Join(doc, "index.html"), "old site")
	mustWrite(t, filepath.Join(doc, ".well-known", "acme"), "challenge")
	mustWrite(t, filepath.Join(doc, "wp-content", "x.php"), "<?php") // more content -> backed up

	out := runScriptLocal(t, home, backupDestScript(), map[string]string{"DEST_DOCROOT": doc}, "")
	if got := strings.TrimSpace(out); got != "BAKDIR addon1.example-bak" {
		t.Fatalf("status = %q, want BAKDIR addon1.example-bak", got)
	}
	bak := filepath.Join(home, "public_html", "addon1.example-bak")

	// Old content moved into the backup.
	assertExists(t, filepath.Join(bak, "index.html"))
	assertExists(t, filepath.Join(bak, "wp-content", "x.php"))
	assertExists(t, filepath.Join(bak, ".well-known", "acme"))
	// The live docroot exists and is fresh (no old content).
	assertExists(t, doc)
	assertMissing(t, filepath.Join(doc, "index.html"))
	assertMissing(t, filepath.Join(doc, "wp-content"))
	assertMissing(t, filepath.Join(doc, ".well-known"))
}

// TestBackupDestScriptCollisionUsesNumberedBak: when <docroot>-bak already exists,
// the next free <docroot>-bak.N is used (no overwrite of a previous backup).
func TestBackupDestScriptCollisionUsesNumberedBak(t *testing.T) {
	requireBash(t)
	home := t.TempDir()
	doc := filepath.Join(home, "public_html", "site.it")
	mustWrite(t, filepath.Join(doc, "index.html"), "current")
	mustWrite(t, filepath.Join(home, "public_html", "site.it-bak", "old"), "prior backup") // -bak taken

	out := runScriptLocal(t, home, backupDestScript(), map[string]string{"DEST_DOCROOT": doc}, "")
	if got := strings.TrimSpace(out); got != "BAKDIR site.it-bak.2" {
		t.Fatalf("status = %q, want BAKDIR site.it-bak.2", got)
	}
	assertExists(t, filepath.Join(home, "public_html", "site.it-bak", "old")) // prior backup intact
	assertExists(t, filepath.Join(home, "public_html", "site.it-bak.2", "index.html"))
}

// TestBackupDestScriptWebRootIsRefused: the account web root (public_html
// itself) is a hard guard failure and is not touched.
func TestBackupDestScriptWebRootIsRefused(t *testing.T) {
	requireBash(t)
	home := t.TempDir()
	ph := filepath.Join(home, "public_html")
	mustWrite(t, filepath.Join(ph, "index.html"), "main site")

	if _, err := execScript(home, backupDestScript(), map[string]string{"DEST_DOCROOT": ph}); err == nil {
		t.Fatal("backupDestScript must refuse public_html itself")
	}
	assertExists(t, filepath.Join(ph, "index.html")) // untouched
	assertMissing(t, filepath.Join(home, "public_html-bak"))
}

// TestBackupDestScriptEmptyOrSystemOnlyIsNoBak: an absent docroot, or one holding
// only protected system entries, is a no-op (NOBAK) — nothing to back up.
func TestBackupDestScriptEmptyOrSystemOnlyIsNoBak(t *testing.T) {
	requireBash(t)
	home := t.TempDir()
	ph := filepath.Join(home, "public_html")
	if err := os.MkdirAll(ph, 0o755); err != nil {
		t.Fatal(err)
	}

	// Absent docroot -> NOBAK, created empty.
	absent := filepath.Join(ph, "fresh.it")
	if got := strings.TrimSpace(runScriptLocal(t, home, backupDestScript(), map[string]string{"DEST_DOCROOT": absent}, "")); got != "NOBAK" {
		t.Fatalf("absent docroot status = %q, want NOBAK", got)
	}
	assertExists(t, absent)

	// System-entries-only docroot -> NOBAK, left as-is (no -bak created).
	sysOnly := filepath.Join(ph, "parked.it")
	mustWrite(t, filepath.Join(sysOnly, "cgi-bin", "probe.cgi"), "c")
	if got := strings.TrimSpace(runScriptLocal(t, home, backupDestScript(), map[string]string{"DEST_DOCROOT": sysOnly}, "")); got != "NOBAK" {
		t.Fatalf("system-only docroot status = %q, want NOBAK", got)
	}
	assertExists(t, filepath.Join(sysOnly, "cgi-bin", "probe.cgi"))
	assertMissing(t, filepath.Join(home, "public_html", "parked.it-bak"))

	// .well-known-only docroot -> BAKDIR, because it is real user-served content.
	wellKnownOnly := filepath.Join(ph, "wellknown.it")
	mustWrite(t, filepath.Join(wellKnownOnly, ".well-known", "security.txt"), "Contact: security@example.test")
	if got := strings.TrimSpace(runScriptLocal(t, home, backupDestScript(), map[string]string{"DEST_DOCROOT": wellKnownOnly}, "")); got != "BAKDIR wellknown.it-bak" {
		t.Fatalf(".well-known-only docroot status = %q, want BAKDIR wellknown.it-bak", got)
	}
	assertExists(t, filepath.Join(home, "public_html", "wellknown.it-bak", ".well-known", "security.txt"))
	assertMissing(t, filepath.Join(wellKnownOnly, ".well-known"))
}

// TestBackupDestScriptMovesSystemEntriesBackIntoFreshDocroot pins the move-back
// happy path: a docroot with BOTH user content and protected system entries is
// renamed aside (BAKDIR), and the system entries (cgi-bin/.ftpquota) are restored
// into the fresh live docroot while the user content stays in the backup.
func TestBackupDestScriptMovesSystemEntriesBackIntoFreshDocroot(t *testing.T) {
	requireBash(t)
	home := t.TempDir()
	doc := filepath.Join(home, "public_html", "mixed.it")
	mustWrite(t, filepath.Join(doc, "index.html"), "user content") // user content -> BAKDIR
	mustWrite(t, filepath.Join(doc, "cgi-bin", "script.cgi"), "system")
	mustWrite(t, filepath.Join(doc, ".ftpquota"), "0")

	out := runScriptLocal(t, home, backupDestScript(), map[string]string{"DEST_DOCROOT": doc}, "")
	if got := strings.TrimSpace(out); got != "BAKDIR mixed.it-bak" {
		t.Fatalf("status = %q, want BAKDIR mixed.it-bak", got)
	}
	// Protected system entries restored into the fresh live docroot.
	assertExists(t, filepath.Join(doc, "cgi-bin", "script.cgi"))
	assertExists(t, filepath.Join(doc, ".ftpquota"))
	// User content went to the backup, not the live docroot.
	assertExists(t, filepath.Join(home, "public_html", "mixed.it-bak", "index.html"))
	assertMissing(t, filepath.Join(doc, "index.html"))
}

// TestBackupDestScriptFailsWhenSystemEntryRestoreFails: if a protected system entry
// exists but cannot be moved back into the fresh docroot (immutable flag, denied
// permission, read-only mount), the script must FAIL CLOSED — not print BAKDIR and
// exit 0 leaving the live docroot silently missing cgi-bin/.ftpquota. The old
// content stays safe in -bak (recoverable).
func TestBackupDestScriptFailsWhenSystemEntryRestoreFails(t *testing.T) {
	requireBash(t)
	home := t.TempDir()
	doc := filepath.Join(home, "public_html", "mixed.it")
	mustWrite(t, filepath.Join(doc, "index.html"), "user content")
	mustWrite(t, filepath.Join(doc, "cgi-bin", "script.cgi"), "system")

	// A mv that refuses to move a cgi-bin entry but delegates every other mv
	// (including the main docroot rename) to the real mv, so only the system-entry
	// restore fails. Root-safe: the failure is forced by the stub, not by perms.
	realMv, err := exec.LookPath("mv")
	if err != nil {
		t.Skip("mv not available")
	}
	binDir := t.TempDir()
	stub := "#!/bin/sh\ncase \"$*\" in\n  *cgi-bin*) echo 'stub mv: refusing cgi-bin restore' >&2; exit 1 ;;\nesac\nexec " + realMv + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "mv"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := execScriptPath(home, backupDestScript(), map[string]string{"DEST_DOCROOT": doc}, binDir); err == nil {
		t.Fatal("backupDestScript must FAIL when a protected system entry cannot be restored, not report BAKDIR")
	}
	// The original content is preserved in -bak (recoverable).
	assertExists(t, filepath.Join(home, "public_html", "mixed.it-bak", "cgi-bin", "script.cgi"))
}

// TestBackupDestScriptRefusesOutsidePublicHtml: a target not strictly under
// ~/public_html is refused with a non-zero exit (the same guard emptyDestScript
// carries), so a malformed path can never rename arbitrary files.
func TestBackupDestScriptRefusesOutsidePublicHtml(t *testing.T) {
	requireBash(t)
	home := t.TempDir()
	ph := filepath.Join(home, "public_html")
	if err := os.MkdirAll(ph, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(home, "secret")
	mustWrite(t, filepath.Join(outside, "data"), "keep")
	if err := os.Symlink(outside, filepath.Join(ph, "link-out")); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{
		outside,
		ph + "/site/../other",
		filepath.Join(ph, "link-out", "site"),
	} {
		if _, err := execScript(home, backupDestScript(), map[string]string{"DEST_DOCROOT": bad}); err == nil {
			t.Errorf("backupDestScript must refuse dangerous target %q with a non-zero exit", bad)
		}
	}
	assertExists(t, filepath.Join(outside, "data")) // untouched
	assertMissing(t, filepath.Join(outside, "site"))
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected %s to exist: %v", path, err)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("expected %s to be gone", path)
	}
}
