package dbmig

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
)

func TestDetectUnmigratedConfigs(t *testing.T) {
	sshtest.RequireTools(t, "bash", "grep")
	home := t.TempDir()
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer src.Close()

	write := func(rel, content string) string {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	m1 := filepath.Join(home, "m1")
	write("m1/app/etc/local.xml", "<config><connection><host>localhost</host><dbname>shop</dbname></connection></config>")
	ps17 := filepath.Join(home, "ps17")
	write("ps17/app/config/parameters.php", "<?php\nreturn array(\n  'parameters' => array(\n    'database_name' => 'shop',\n    'database_user' => 'u',\n  ),\n);\n")
	sym := filepath.Join(home, "sym")
	write("sym/.env", "APP_ENV=prod\nDATABASE_URL=mysql://u:p@127.0.0.1:3306/db\n")
	ss := filepath.Join(home, "ss")
	write("ss/.env", "SS_DATABASE_NAME=\"ssdb\"\nSS_DATABASE_USERNAME=\"u\"\n")
	// Benign: a Laravel .env (only DB_DATABASE) and a local.xml WITHOUT a DB needle.
	laravel := filepath.Join(home, "laravel")
	write("laravel/.env", "APP_KEY=x\nDB_DATABASE=lara\nDB_USERNAME=u\n")
	noNeedle := filepath.Join(home, "noneedle")
	write("noneedle/app/etc/local.xml", "<config><general><locale>en_US</locale></general></config>")
	// A docroot that DID yield a handled config (suppressed even if it has a marker).
	handledDoc := filepath.Join(home, "handled")
	write("handled/.env", "DATABASE_URL=mysql://u:p@127.0.0.1/db\n")

	docroots := []string{m1, ps17, sym, ss, laravel, noNeedle, handledDoc}
	handled := []string{handledDoc}

	apps, err := DetectUnmigratedConfigs(bg, src, docroots, handled)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, a := range apps {
		got[a.App] = a.Docroot
	}
	wantApps := map[string]string{
		"Magento 1":       m1,
		"PrestaShop 1.7+": ps17,
		"Symfony":         sym,
		"SilverStripe":    ss,
	}
	if len(apps) != len(wantApps) {
		t.Fatalf("detected %d apps, want %d: %+v", len(apps), len(wantApps), apps)
	}
	for app, dr := range wantApps {
		if got[app] != dr {
			t.Errorf("%s: docroot %q, want %q", app, got[app], dr)
		}
	}
	// Benign and handled docroots must NOT appear.
	for _, a := range apps {
		if a.Docroot == laravel || a.Docroot == noNeedle || a.Docroot == handledDoc {
			t.Errorf("docroot %q must not be flagged (%s)", a.Docroot, a.App)
		}
	}
}

// Refuter R1: the handled-suppression must be containment-aware. A handled config recorded
// at a SUBDIRECTORY of the docroot (e.g. a Softaculous install path) must still suppress the
// whole docroot, so a stray .env DATABASE_URL does not spuriously fail a healthy run.
func TestDetectUnmigratedConfigsContainmentSuppression(t *testing.T) {
	sshtest.RequireTools(t, "bash", "grep")
	home := t.TempDir()
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer src.Close()
	docroot := filepath.Join(home, "site")
	if err := os.MkdirAll(docroot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docroot, ".env"), []byte("DATABASE_URL=mysql://u:p@127.0.0.1/db\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A handled config recorded at docroot/blog (a subdir install) must cover docroot.
	handled := []string{filepath.Join(docroot, "blog")}
	apps, err := DetectUnmigratedConfigs(bg, src, []string{docroot}, handled)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 0 {
		t.Fatalf("a handled config nested under the docroot must suppress it; got %+v", apps)
	}
}

// Refuter R1/F2: the content needles must not over-match. A local.xml with only
// <connectionType> (not <connection>) and no <dbname> must NOT be flagged.
func TestDetectUnmigratedConfigsNeedleDoesNotOverMatch(t *testing.T) {
	sshtest.RequireTools(t, "bash", "grep")
	home := t.TempDir()
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer src.Close()
	dr := filepath.Join(home, "x")
	if err := os.MkdirAll(filepath.Join(dr, "app", "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dr, "app", "etc", "local.xml"),
		[]byte("<config><connectionType>tcp</connectionType></config>"), 0o644); err != nil {
		t.Fatal(err)
	}
	apps, err := DetectUnmigratedConfigs(bg, src, []string{dr}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 0 {
		t.Fatalf("<connectionType> must not match the <connection> needle; got %+v", apps)
	}
}

// Refuter R2/F5: a Symfony app keeps its real DATABASE_URL in .env.local (the documented
// prod convention) — it must be detected there too.
func TestDetectUnmigratedConfigsEnvLocal(t *testing.T) {
	sshtest.RequireTools(t, "bash", "grep")
	home := t.TempDir()
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer src.Close()
	dr := filepath.Join(home, "sym")
	if err := os.MkdirAll(dr, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dr, ".env.local"), []byte("DATABASE_URL=mysql://u:p@127.0.0.1/db\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	apps, err := DetectUnmigratedConfigs(bg, src, []string{dr}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 || apps[0].App != "Symfony" {
		t.Fatalf("a DATABASE_URL in .env.local must be detected as Symfony; got %+v", apps)
	}
}

// Refuter pass 2: a site with the same DSN in BOTH .env and .env.local is ONE site — it
// must be flagged once, not double-counted.
func TestDetectUnmigratedConfigsNoDoubleEmit(t *testing.T) {
	sshtest.RequireTools(t, "bash", "grep")
	home := t.TempDir()
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer src.Close()
	dr := filepath.Join(home, "sym")
	if err := os.MkdirAll(dr, 0o755); err != nil {
		t.Fatal(err)
	}
	dsn := "DATABASE_URL=mysql://u:p@127.0.0.1/db\n"
	for _, f := range []string{".env", ".env.local"} {
		if err := os.WriteFile(filepath.Join(dr, f), []byte(dsn), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	apps, err := DetectUnmigratedConfigs(bg, src, []string{dr}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 {
		t.Fatalf("DATABASE_URL in both .env and .env.local must flag ONE Symfony site, got %d: %+v", len(apps), apps)
	}
}

// Refuter pass 2: a parameters.php whose only `database_name` lives in a COMMENT (a .dist
// template or leftover) must NOT be flagged — the needle is anchored to a non-comment line.
func TestDetectUnmigratedConfigsParametersCommentNotFlagged(t *testing.T) {
	sshtest.RequireTools(t, "bash", "grep")
	home := t.TempDir()
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer src.Close()
	commented := filepath.Join(home, "commented")
	if err := os.MkdirAll(filepath.Join(commented, "app", "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commented, "app", "config", "parameters.php"),
		[]byte("<?php\n// example: 'database_name' => 'CHANGEME',\n#   database_name: foo\nreturn [];\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(home, "live")
	if err := os.MkdirAll(filepath.Join(live, "app", "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, "app", "config", "parameters.php"),
		[]byte("<?php\nreturn ['parameters' => [\n    'database_name' => 'shop',\n]];\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	apps, err := DetectUnmigratedConfigs(bg, src, []string{commented, live}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 || apps[0].Docroot != live {
		t.Fatalf("only the LIVE parameters.php must be flagged (not the commented one); got %+v", apps)
	}
}
