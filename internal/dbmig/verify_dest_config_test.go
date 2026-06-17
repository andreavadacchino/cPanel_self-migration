package dbmig

import (
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
	"github.com/tis24dev/cPanel_self-migration/internal/wpconfig"
)

func TestIsLocalDBHost(t *testing.T) {
	local := []string{"", "localhost", "127.0.0.1", "::1", "LOCALHOST", "localhost:3306",
		"127.0.0.1:3307", "[::1]", "[::1]:3306", "[127.0.0.1]:3306",
		"localhost:/var/run/mysqld/mysqld.sock", "  localhost  ",
		"p:localhost", "p:127.0.0.1", "p:localhost:3306"} // mysqli persistent prefix
	for _, h := range local {
		if !IsLocalDBHost(h) {
			t.Errorf("IsLocalDBHost(%q) = false, want true (local)", h)
		}
	}
	// Remote hosts AND suffix-attack lookalikes that must NOT read as local.
	remote := []string{"10.0.0.5", "db.example.com", "10.0.0.5:3306", "192.168.1.1",
		"mysql.internal", "rds.amazonaws.com:3306", "localhost.evil.com",
		"127.0.0.1.example.com", "localhostX", "0.0.0.0", "::", "p:db.example.com"}
	for _, h := range remote {
		if IsLocalDBHost(h) {
			t.Errorf("IsLocalDBHost(%q) = true, want false (remote)", h)
		}
	}
}

func TestCheckDestCreds(t *testing.T) {
	good := wpconfig.Creds{DBName: "d", DBUser: "u", DBPassword: "p", DBHost: "localhost"}
	if ok, r := checkDestCreds(good, "d", "u", "p"); !ok {
		t.Errorf("correct dest creds rejected: %s", r)
	}
	bad := []struct {
		name string
		c    wpconfig.Creds
	}{
		{"name mismatch", wpconfig.Creds{DBName: "X", DBUser: "u", DBPassword: "p", DBHost: "localhost"}},
		{"user mismatch", wpconfig.Creds{DBName: "d", DBUser: "X", DBPassword: "p", DBHost: "localhost"}},
		{"pass mismatch", wpconfig.Creds{DBName: "d", DBUser: "u", DBPassword: "X", DBHost: "localhost"}},
		{"remote host (name/user/pass right)", wpconfig.Creds{DBName: "d", DBUser: "u", DBPassword: "p", DBHost: "10.0.0.5"}},
	}
	for _, b := range bad {
		if ok, _ := checkDestCreds(b.c, "d", "u", "p"); ok {
			t.Errorf("%s: expected verification to fail", b.name)
		}
	}
	// An empty wanted password (an orphan whose password we generated and wrote, but
	// the caller passes "" to mean "do not compare") must skip the password check.
	if ok, _ := checkDestCreds(wpconfig.Creds{DBName: "d", DBUser: "u", DBPassword: "z", DBHost: ""}, "d", "u", ""); !ok {
		t.Error("empty wanted password must skip the password compare")
	}
}

// TestVerifyDestConfigIntegration drives the real read+parse over the in-process SSH
// server: a config pointing at the dest DB on a local host verifies; one whose host
// is a remote DB server, or whose name did not land, does NOT — the residual the
// rewrite step's own pre-write check cannot see (DB_HOST is never rewritten).
func TestVerifyDestConfigIntegration(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	home := t.TempDir()
	dest := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer dest.Close()
	p := writeDestConfig(t, home, wpConfig("vh_db", "vh_user", "vh_pass")) // no DB_HOST => local

	if ok, reason, _, err := VerifyDestConfig(bg, dest, p, KindWordPress, "vh_db", "vh_user", "vh_pass"); err != nil || !ok {
		t.Errorf("correct local config: ok=%v reason=%q err=%v, want ok=true", ok, reason, err)
	}

	// Name/user/password are correct but DB_HOST is a remote server — the site cannot
	// reach the destination MySQL; must fail with a host reason.
	remote := "<?php\ndefine('DB_NAME','vh_db');\ndefine('DB_USER','vh_user');\n" +
		"define('DB_PASSWORD','vh_pass');\ndefine('DB_HOST','10.0.0.5');\n$table_prefix='wp_';\n"
	if err := os.WriteFile(p, []byte(remote), 0o644); err != nil {
		t.Fatal(err)
	}
	ok, reason, _, err := VerifyDestConfig(bg, dest, p, KindWordPress, "vh_db", "vh_user", "vh_pass")
	if err != nil || ok {
		t.Errorf("remote-host config must fail: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(reason, "host") {
		t.Errorf("reason should name the host problem: %q", reason)
	}

	// The rewrite did not land (still the old DB name) — must fail.
	if err := os.WriteFile(p, []byte(wpConfig("old_db", "vh_user", "vh_pass")), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, _, _, _ := VerifyDestConfig(bg, dest, p, KindWordPress, "vh_db", "vh_user", "vh_pass"); ok {
		t.Error("a config still pointing at the old DB name must fail verification")
	}
}

// TestVerifyDestConfigStructurallyAmbiguous: a destination config whose value/host checks
// PASS (the shared parser reads the planned creds) but whose DB_NAME is structurally
// ambiguous — a heredoc-embedded decoy define the rewrite edited, while the live define
// still points at the source DB — must return ok=false with unverified=true (a soft
// "not independently verified" signal), NOT a clean ok=true. This is the V35 false-OK the
// shared-parser re-read cannot see.
func TestVerifyDestConfigStructurallyAmbiguous(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	home := t.TempDir()
	dest := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer dest.Close()
	// The heredoc decoy reads as the planned name to the blind parser; the live define
	// still says the source DB. DB_USER/DB_PASSWORD are clean planned values.
	ambiguous := "<?php\n$h = <<<EOT\ndefine('DB_NAME','vh_db');\nEOT;\n" +
		"define('DB_NAME','old_src_db');\ndefine('DB_USER','vh_user');\n" +
		"define('DB_PASSWORD','vh_pass');\n$table_prefix='wp_';\n"
	p := writeDestConfig(t, home, ambiguous)

	ok, reason, unverified, err := VerifyDestConfig(bg, dest, p, KindWordPress, "vh_db", "vh_user", "vh_pass")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok || !unverified {
		t.Fatalf("ambiguous cutover: ok=%v unverified=%v, want ok=false unverified=true (reason=%q)", ok, unverified, reason)
	}
	if reason == "" {
		t.Error("an unverified verdict must carry a reason")
	}
}
