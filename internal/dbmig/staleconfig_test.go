package dbmig

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
)

// TestSourceCredsStillReachable exercises the V35 source-cred containment scan: it
// must DETECT a source DB name/user still reachable in the destination docroot (the
// split/include/cache evidence) while NOT false-firing on a clean cutover, a no-remap
// case, or a destination name that merely CONTAINS the source name as a substring.
func TestSourceCredsStillReachable(t *testing.T) {
	sshtest.RequireTools(t, "bash", "grep", "head")
	home := t.TempDir()
	dest := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer dest.Close()
	ctx := context.Background()

	write := func(rel, content string) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A docroot whose MAIN config was rewritten to the destination DB, but a SPLIT
	// included file still carries the SOURCE name (the exact V35 residual).
	leak := filepath.Join(home, "leak")
	write("leak/wp-config.php", "<?php\ndefine('DB_NAME', 'destacct_wp');\nrequire __DIR__.'/db.inc.php';\n")
	write("leak/db.inc.php", "<?php\n// real creds live here\n$db = 'srcacct_wp';\n")

	// A clean docroot: only the rewritten config, destination name, no source trace.
	clean := filepath.Join(home, "clean")
	write("clean/wp-config.php", "<?php\ndefine('DB_NAME', 'destacct_wp');\n")

	// A docroot where the destination name CONTAINS the source name as a substring
	// (srcacct_wp inside srcacct_wp2): -w must NOT match it.
	substr := filepath.Join(home, "substr")
	write("substr/wp-config.php", "<?php\ndefine('DB_NAME', 'srcacct_wp2');\n")

	// A docroot leaking only the source USER (not the name).
	userleak := filepath.Join(home, "userleak")
	write("userleak/config.php", "<?php\ndefine('DB_DATABASE', 'destacct_wp');\n$legacyUser = 'srcacct_u1';\n")

	// -F literal: a needle with a regex meta '.' must NOT match where the '.' would
	// stand in for any char under a regex engine.
	regex := filepath.Join(home, "regex")
	write("regex/wp-config.php", "<?php\ndefine('DB_NAME', 'srcacctXwp');\n")

	// -w with a hyphenated name (validate.DBName allows '-'): a suffix-extended name
	// must NOT match.
	hyphen := filepath.Join(home, "hyphen")
	write("hyphen/wp-config.php", "<?php\ndefine('DB_NAME', 'acct-wp2');\n")

	// A docroot that is actually a regular FILE, not a directory.
	notdir := filepath.Join(home, "afile")
	write("afile", "irrelevant")

	// The source name survives ONLY in a backup DIR (a WP-security plugin store): the
	// live site never loads it, so the scan must not flag it (--exclude-dir='*backup*').
	bkdir := filepath.Join(home, "bkdir")
	write("bkdir/wp-config.php", "<?php\ndefine('DB_NAME', 'destacct_wp');\n")
	write("bkdir/wp-content/aiowps_backups/backup.wp-config.php", "<?php\ndefine('DB_NAME', 'srcacct_wp');\n")

	// The source name survives ONLY in a backup-NAMED file outside a backup dir: grep
	// matches it, but isNonLiveConfigPath filters it Go-side.
	bkname := filepath.Join(home, "bkname")
	write("bkname/wp-config.php", "<?php\ndefine('DB_NAME', 'destacct_wp');\n")
	write("bkname/wp-config-backup.php", "<?php\ndefine('DB_NAME', 'srcacct_wp');\n")

	// Two sibling configs on the SAME DB (a docroot's wp-config.php + a test/ copy): the
	// test/ copy is in THIS DB's rewrite plan (passed via ignore), so a stale name found
	// there mid-process must NOT flag — it is rewritten/certified on its own iteration.
	planned := filepath.Join(home, "planned")
	write("planned/wp-config.php", "<?php\ndefine('DB_NAME', 'destacct_wp');\n")
	write("planned/test/wp-config.php", "<?php\ndefine('DB_NAME', 'srcacct_wp');\n")

	// ignore must be path-EXACT, never over-broad: an ignored sibling does not hide a
	// REAL residual elsewhere in the docroot.
	plannedReal := filepath.Join(home, "plannedreal")
	write("plannedreal/test/wp-config.php", "<?php\ndefine('DB_NAME', 'srcacct_wp');\n") // ignored
	write("plannedreal/inc/db.inc.php", "<?php\n$db = 'srcacct_wp';\n")                  // real residual

	cases := []struct {
		name                                      string
		docroot, srcDB, destDB, srcUser, destUser string
		ignore                                    []string
		wantFound                                 bool
		wantInReason                              string
	}{
		{"split include still has source name", leak, "srcacct_wp", "destacct_wp", "srcacct_u1", "destacct_u1", nil, true, "db.inc.php"},
		{"clean cutover", clean, "srcacct_wp", "destacct_wp", "srcacct_u1", "destacct_u1", nil, false, ""},
		{"no remap (src==dest) is skipped", clean, "destacct_wp", "destacct_wp", "destacct_u1", "destacct_u1", nil, false, ""},
		{"dest name contains source as substring", substr, "srcacct_wp", "srcacct_wp2", "srcacct_u1", "destacct_u1", nil, false, ""},
		{"source user still reachable", userleak, "srcacct_wp", "destacct_wp", "srcacct_u1", "destacct_u1", nil, true, "DB user"},
		{"-F literal: regex meta does not match", regex, "srcacct.wp", "destacct_wp", "srcacct.u1", "destacct_u1", nil, false, ""},
		{"-w hyphen suffix does not match", hyphen, "acct-wp", "acct-wp9", "acct-u1", "destacct_u1", nil, false, ""},
		{"docroot is a regular file", notdir, "srcacct_wp", "destacct_wp", "srcacct_u1", "destacct_u1", nil, false, ""},
		{"absent docroot", filepath.Join(home, "nope"), "srcacct_wp", "destacct_wp", "srcacct_u1", "destacct_u1", nil, false, ""},
		{"empty docroot", "", "srcacct_wp", "destacct_wp", "srcacct_u1", "destacct_u1", nil, false, ""},
		{"stale name only in a backup dir is ignored", bkdir, "srcacct_wp", "destacct_wp", "srcacct_u1", "destacct_u1", nil, false, ""},
		{"stale name only in a backup-named file is ignored", bkname, "srcacct_wp", "destacct_wp", "srcacct_u1", "destacct_u1", nil, false, ""},
		{"stale name only in a planned sibling config is ignored", planned, "srcacct_wp", "destacct_wp", "srcacct_u1", "destacct_u1", []string{filepath.Join(planned, "test", "wp-config.php")}, false, ""},
		{"ignored sibling does not hide a real residual", plannedReal, "srcacct_wp", "destacct_wp", "srcacct_u1", "destacct_u1", []string{filepath.Join(plannedReal, "test", "wp-config.php")}, true, "db.inc.php"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			found, reason, err := SourceCredsStillReachable(ctx, dest, c.docroot, c.srcDB, c.destDB, c.srcUser, c.destUser, c.ignore)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if found != c.wantFound {
				t.Fatalf("found = %v, want %v (reason=%q)", found, c.wantFound, reason)
			}
			if c.wantInReason != "" && !strings.Contains(reason, c.wantInReason) {
				t.Fatalf("reason %q does not mention %q", reason, c.wantInReason)
			}
			if !found && reason != "" {
				t.Fatalf("not found but reason is non-empty: %q", reason)
			}
		})
	}
}

// errRunner fails every RunScript, to exercise the best-effort error path.
type errRunner struct{ err error }

func (e errRunner) RunScript(context.Context, string, map[string]string) ([]byte, error) {
	return nil, e.err
}

// TestSourceCredsStillReachableBestEffortError: a scan error must surface as
// (found=false, err!=nil) so the caller keeps the existing verdict rather than
// wrongly demoting (or aborting) on a transient grep/transport failure.
func TestSourceCredsStillReachableBestEffortError(t *testing.T) {
	found, reason, err := SourceCredsStillReachable(context.Background(), errRunner{errors.New("boom")}, "/some/docroot", "srcacct_wp", "destacct_wp", "", "", nil)
	if err == nil {
		t.Fatal("want a non-nil error from a failing scan")
	}
	if found {
		t.Fatalf("found = true on a scan error; must be false (reason=%q)", reason)
	}
}

// TestSourceCredsStillReachablePartialResultUsedDespiteUnreadableDir locks the
// regression fix: GNU grep exits 2 when it cannot descend ONE unreadable subdir, but it
// still prints the matches it DID find. A non-empty result must be USED (the leak flagged),
// never discarded as a best-effort error — discarding it would re-suppress a real residual
// in a readable file. (As root the unreadable dir is still readable, so grep returns rc=0
// and finds both; the assertion — leak found, no error — holds either way.)
func TestSourceCredsStillReachablePartialResultUsedDespiteUnreadableDir(t *testing.T) {
	sshtest.RequireTools(t, "bash", "grep", "head")
	home := t.TempDir()
	dest := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer dest.Close()

	doc := filepath.Join(home, "site")
	mk := func(rel, content string) {
		p := filepath.Join(doc, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("wp-config.php", "<?php\ndefine('DB_NAME','destacct_wp');\n")
	mk("inc/db.inc.php", "<?php\n$db='srcacct_wp';\n") // the REAL leak, in a readable file
	mk("noperm/secret.php", "<?php\n$db='srcacct_wp';\n")
	noperm := filepath.Join(doc, "noperm")
	if err := os.Chmod(noperm, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(noperm, 0o755) }) // let TempDir cleanup remove it

	found, reason, err := SourceCredsStillReachable(context.Background(), dest, doc,
		"srcacct_wp", "destacct_wp", "srcacct_u1", "destacct_u1", nil)
	if err != nil {
		t.Fatalf("a readable leak must be reported, not turned into a best-effort error: %v", err)
	}
	if !found {
		t.Fatalf("the leak in inc/db.inc.php must be found despite an unreadable sibling dir (reason=%q)", reason)
	}
}

// TestIsNonLiveConfigPath locks the non-live-file classifier: a stale source DB name
// surviving only in a backup/old copy, a numbered rotation, or a PHP error log is not a
// cutover gap, so these must be filtered out (true); a live config name must not (false).
func TestIsNonLiveConfigPath(t *testing.T) {
	cases := []struct {
		p    string
		want bool
	}{
		{"/home/u/public_html/site/wp-content/aiowps_backups/backup.wp-config.php", true}, // backup dir + backup name
		{"/home/u/public_html/site/wp-content/updraft_backups/wp-config.php", true},       // backup dir
		{"/home/u/public_html/site/wp-config-backup.php", true},                           // backup-named
		{"/home/u/public_html/site/backup.wp-config.php", true},                           // backup-named
		{"/home/u/public_html/site/wp-config.php.bak", true},                              // .bak suffix
		{"/home/u/public_html/site/wp-config.php.bak2", true},                             // .bak + rotation number
		{"/home/u/public_html/site/wp-config.php.bak.1", true},                            // .bak. infix + number
		{"/home/u/public_html/site/wp-config.old.3~", true},                               // .old + number + editor "~"
		{"/home/u/public_html/site/wp-config.bak.php", true},                              // .bak. infix
		{"/home/u/public_html/site/wp-config.php.old", true},                              // .old suffix
		{"/home/u/public_html/site/wp-config.php.swp", true},                              // vim swap
		{"/home/u/public_html/site/wp-config.php~", true},                                 // editor backup
		{"/home/u/public_html/site/wp-admin/error_log", true},                             // cPanel PHP error log
		{"/home/u/public_html/site/php_errorlog", true},                                   // alt PHP error log name
		{"/home/u/public_html/site/wp-content/debug.log", true},                           // WP_DEBUG_LOG
		{"/home/u/public_html/site/error_log.php", false},                                 // LIVE .php w/ "error_log" substring — must scrutinize
		{"/home/u/public_html/site/my_error_log_viewer.php", false},                       // LIVE .php w/ "error_log" substring
		{"/home/u/public_html/site/wp-config.php", false},                                 // the live config
		{"/home/u/public_html/site/config/database.php", false},                           // a live split config
		{"/home/u/public_html/site/wp-content/db.inc.php", false},                         // a live include
		{"backup.wp-config.php", true},                                                    // basename only
		{"", false},
	}
	for _, c := range cases {
		if got := isNonLiveConfigPath(c.p); got != c.want {
			t.Errorf("isNonLiveConfigPath(%q) = %v, want %v", c.p, got, c.want)
		}
	}
}
