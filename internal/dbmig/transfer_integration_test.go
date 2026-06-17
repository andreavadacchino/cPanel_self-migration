package dbmig

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
	"github.com/tis24dev/cPanel_self-migration/internal/wpconfig"
)

// RewriteWPConfig needs a *sshx.Client (it reads, rewrites, then writes the
// dest wp-config via a guarded bash script — no MySQL involved), so it is covered
// against an in-process SSH server that runs the real bash in a temp HOME.

func writeDestConfig(t *testing.T, home, content string) string {
	t.Helper()
	p := filepath.Join(home, "site", "wp-config.php")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// CopyDatabase must reject a missing database name before touching the network.
func TestCopyDatabaseRejectsMissingNames(t *testing.T) {
	if _, err := (Transfer{}).CopyDatabase(bg, DBPlanItem{SrcDB: "", DestDB: "x"}, "u", "p", nil); err == nil {
		t.Error("CopyDatabase must reject a missing source DB name")
	}
}

// CopyDatabase must surface a failed dump bridge (here the mysqldump/mysql
// commands cannot succeed against the throwaway servers) rather than report
// success.
func TestCopyDatabaseBridgeFailureSurfaces(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	// CopyDatabase now retries the dump bridge (sshx.RetryBatch); drop the backoff so
	// the exhausted-retries failure path stays fast.
	origBackoff := sshx.RetryBackoffBase
	sshx.RetryBackoffBase = 0
	t.Cleanup(func() { sshx.RetryBackoffBase = origBackoff })
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))
	defer dst.Close()
	tr := Transfer{Src: src, Dest: dst, Timeout: 10 * time.Second}
	if _, err := tr.CopyDatabase(bg, DBPlanItem{SrcDB: "db", DestDB: "vh_db"}, "u", "p", nil); err == nil {
		t.Error("CopyDatabase must surface a failed dump bridge")
	}
}

// TestRewriteWPConfigIntegration: a dest wp-config with the OLD credentials is
// rewritten in place to the destination-prefixed name/user/password.
func TestRewriteWPConfigIntegration(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	home := t.TempDir()
	cfgPath := writeDestConfig(t, home, wpConfig("old_db", "old_user", "old_pass"))
	dest := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer dest.Close()

	if err := RewriteWPConfig(bg, dest, cfgPath, "vh_db", "vh_user", "vh_pass"); err != nil {
		t.Fatalf("RewriteWPConfig: %v", err)
	}
	got, _ := os.ReadFile(cfgPath)
	c := wpconfig.Parse(string(got))
	if c.DBName != "vh_db" || c.DBUser != "vh_user" || c.DBPassword != "vh_pass" {
		t.Errorf("rewritten creds = %+v, want vh_db/vh_user/vh_pass", c)
	}
}

// TestRewriteWPConfigNoChange: a config already pointing at the destination DB is
// a no-op (returns nil) and is left intact.
func TestRewriteWPConfigNoChange(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	home := t.TempDir()
	original := wpConfig("vh_db", "vh_user", "vh_pass")
	cfgPath := writeDestConfig(t, home, original)
	dest := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer dest.Close()

	if err := RewriteWPConfig(bg, dest, cfgPath, "vh_db", "vh_user", "vh_pass"); err != nil {
		t.Fatalf("RewriteWPConfig (no-op): %v", err)
	}
	got, _ := os.ReadFile(cfgPath)
	if string(got) != original {
		t.Errorf("no-op rewrite must leave the file intact")
	}
}

// TestRewriteWPConfigMissingDefineErrors: a config lacking a DB_PASSWORD define
// cannot be made to carry it, so RewriteWPConfig must fail loudly rather than
// report a false success.
func TestRewriteWPConfigMissingDefineErrors(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	home := t.TempDir()
	cfgPath := writeDestConfig(t, home, "<?php\ndefine('DB_NAME','vh_db');\ndefine('DB_USER','vh_user');\n")
	dest := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer dest.Close()

	if err := RewriteWPConfig(bg, dest, cfgPath, "vh_db", "vh_user", "vh_pass"); err == nil {
		t.Error("RewriteWPConfig must error when the DB_PASSWORD define is absent")
	}
}
