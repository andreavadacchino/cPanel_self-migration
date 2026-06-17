package migrate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/dbmig"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
	"github.com/tis24dev/cPanel_self-migration/internal/wpconfig"
)

// fakeUAPI is a cpanel.Runner that returns canned UAPI JSON per (module,fn). It
// lets provisionDest be exercised without a real cPanel. The key is "Module fn"
// as it appears in the generated script ("uapi --output=json Mysql create_user").
type fakeUAPI struct {
	// reply maps a "Module fn" prefix to the JSON the host would return.
	reply map[string]string
	calls []string // ordered record of which (module,fn) were invoked
}

func (f *fakeUAPI) RunScript(_ context.Context, script string, _ map[string]string) ([]byte, error) {
	for key, json := range f.reply {
		// The script starts with "uapi --output=json <Module> <fn> ...".
		if strings.Contains(script, " "+key+" ") || strings.Contains(script, " "+key) {
			f.calls = append(f.calls, key)
			return []byte(json), nil
		}
	}
	return nil, errors.New("fakeUAPI: no canned reply for script: " + script)
}

const uapiOK = `{"result":{"data":null,"errors":null,"status":1}}`

func uapiErr(msg string) string {
	return `{"result":{"data":null,"errors":["` + msg + `"],"status":0}}`
}

// TestApplyDBsRefusesPlanCollision: two source databases that collapse to one
// destination database (acc_blog and the already-dest-prefixed dest_blog both map to
// dest_blog under destUser "dest") must be FAILED before any provision/empty/import,
// so neither overwrites the other on the destination and the run is non-zero. The
// refusal happens before any remote op, so no real cPanel is needed.
func TestApplyDBsRefusesPlanCollision(t *testing.T) {
	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir())), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "s", "d", "now")
	if err != nil {
		t.Fatal(err)
	}
	pd := migrationData{
		Databases:             []cpanel.DatabaseEntry{{Database: "acc_blog"}, {Database: "dest_blog"}},
		SrcMySQLRestrictions:  testMySQLPrefix("acc_"),
		DestMySQLRestrictions: testMySQLPrefix("dest_"),
	}
	creds, failed, _, _, err := applyDBs(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, "srcu", "srcp", nil, false)
	if err != nil {
		t.Fatalf("applyDBs: %v", err)
	}
	if failed != 2 {
		t.Errorf("failed = %d, want 2 (both colliding databases refused)", failed)
	}
	if len(creds) != 0 {
		t.Errorf("no credential may be recorded for a refused database, got %v", creds)
	}
	out := file.String()
	if !strings.Contains(out, "unsafe plan collision") {
		t.Errorf("report should explain the refusal:\n%s", out)
	}
	// The refusal must happen BEFORE any remote provisioning, so the fail reason is the
	// collision, never a downstream uapi/provisioning error. (If the conflict check were
	// removed, these items would instead fail at provisionDest and the report would carry
	// that error, not "unsafe plan collision".)
	if strings.Contains(out, "command not found") || strings.Contains(out, "uapi") {
		t.Errorf("a refused database must not reach provisioning:\n%s", out)
	}
}

// TestApplyDBsFailsLoudOnDumpProbeError: a single live, non-colliding, valid-named
// database WILL be dumped, so applyDBs probes the source mysqldump for
// --set-gtid-purged support BEFORE the loop. A broken/absent source mysqldump must
// abort the whole step with a clear error (fail loud) rather than silently guess the
// flag — guessing is unsafe in both directions (flag-on-MariaDB breaks the dump,
// flag-off-on-MySQL-GTID breaks the import). A fake mysqldump that exits non-zero is
// prepended to PATH so the probe fails deterministically regardless of the host.
func TestApplyDBsFailsLoudOnDumpProbeError(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "mysqldump"), []byte("#!/bin/sh\nexit 2\n"), 0o755); err != nil { // #nosec G306 -- test fake must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir())), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "s", "d", "now")
	if err != nil {
		t.Fatal(err)
	}
	pd := migrationData{
		Databases:             []cpanel.DatabaseEntry{{Database: "acc_blog"}},
		SrcMySQLRestrictions:  testMySQLPrefix("acc_"),
		DestMySQLRestrictions: testMySQLPrefix("dest_"),
	}
	if _, _, _, _, err := applyDBs(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, "srcu", "srcp", nil, false); err == nil || !strings.Contains(err.Error(), "probe source mysqldump") {
		t.Fatalf("applyDBs must fail loud on a source-mysqldump probe error, got: %v", err)
	}
}

// TestApplyDBsRecordsCredsOnlyAfterImport: a database that FAILS before the data
// import completes (here provisionDest fails — the throwaway dest has no UAPI) must
// NOT leave a verify credential behind. Recording the credential before
// provision/empty/import would make verifyDBs try to verify a database that never
// migrated and print contradictory output instead of a clean failed state. The dump
// probe must succeed first (the fake mysqldump answers --help), so the failure is at
// provisioning, not the probe.
func TestApplyDBsRecordsCredsOnlyAfterImport(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "mysqldump"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { // #nosec G306 -- test fake must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	pool := &sshx.Pool{Src: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir())), Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "s", "d", "now")
	if err != nil {
		t.Fatal(err)
	}
	pd := migrationData{
		Databases:             []cpanel.DatabaseEntry{{Database: "acc_blog"}},
		SrcMySQLRestrictions:  testMySQLPrefix("acc_"),
		DestMySQLRestrictions: testMySQLPrefix("dest_"),
	}
	creds, failed, _, _, err := applyDBs(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep, "srcu", "srcp", nil, false)
	if err != nil {
		t.Fatalf("applyDBs returned error: %v", err)
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1 (provisioning fails on the throwaway dest)", failed)
	}
	if len(creds) != 0 {
		t.Errorf("a database that failed before import must leave NO verify credential, got %v", creds)
	}
}

func TestApplyDBsSkipsAllTypeBlockedConfigsBeforeProvision(t *testing.T) {
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "s", "d", "now")
	if err != nil {
		t.Fatal(err)
	}
	pd := migrationData{
		SrcDocroots: []cpanel.DomainDataEntry{{Domain: "Example.COM", DocumentRoot: "/home/src/example.com"}},
		DomainTypeIssues: map[string]DomainTypeIssue{
			"example.com": {
				Domain:           "Example.COM",
				SourceType:       model.Addon,
				ExpectedDestType: model.Addon,
				DestinationName:  "example.com.",
				DestinationType:  model.Parked,
				DestDocrootType:  "parked_domain",
				WarnMail:         true,
				BlockWeb:         true,
				BlockDBConfig:    true,
			},
		},
		Databases: []cpanel.DatabaseEntry{{Database: "src_db", Users: []string{"src_user"}}},
		DBUsers:   []cpanel.DBUserEntry{{User: "src_user", Databases: []string{"src_db"}}},
		SiteCreds: []dbmig.SiteCreds{{
			Docroot:    "/home/src/example.com",
			ConfigPath: "/home/src/example.com/wp-config.php",
			Kind:       dbmig.KindWordPress,
			Creds: wpconfig.Creds{
				DBName:     "src_db",
				DBUser:     "src_user",
				DBPassword: "pw",
			},
		}},
	}

	creds, failed, cfgUnrewritten, _, err := applyDBs(context.Background(), &sshx.Pool{}, pd, logx.NewTo(io.Discard, 0), rep, "srcu", "srcp", nil, false)
	if err != nil {
		t.Fatalf("applyDBs: %v", err)
	}
	if len(creds) != 0 || failed != 1 || cfgUnrewritten != 0 {
		t.Fatalf("creds=%v failed=%d cfgUnrewritten=%d, want controlled DB failure before provisioning/import and no partial", creds, failed, cfgUnrewritten)
	}
	out := file.String()
	if !strings.Contains(out, "[db FAIL]") || !strings.Contains(out, "destination domain type compatibility") {
		t.Fatalf("report should show DB failure due to domain type compatibility:\n%s", out)
	}
	if strings.Contains(out, "uapi") || strings.Contains(out, "command not found") {
		t.Fatalf("skip must happen before remote provisioning/import:\n%s", out)
	}
}

// TestCompareDBsWarnsOnPlanCollision: the dry-run must surface a data-destroying plan
// collision as a warning so the operator sees it before --apply.
func TestCompareDBsWarnsOnPlanCollision(t *testing.T) {
	var buf bytes.Buffer
	log := logx.NewTo(&buf, 0)
	pd := migrationData{
		Databases:             []cpanel.DatabaseEntry{{Database: "acc_blog"}, {Database: "dest_blog"}},
		SrcMySQLRestrictions:  testMySQLPrefix("acc_"),
		DestMySQLRestrictions: testMySQLPrefix("dest_"),
	}
	compareDBs(pd, log, nil)
	if !strings.Contains(buf.String(), "unsafe DB plan") {
		t.Errorf("dry-run should warn about the collision:\n%s", buf.String())
	}
}

func TestCompareDBsShowsDomainTypeIssueSkip(t *testing.T) {
	var buf bytes.Buffer
	log := logx.NewTo(&buf, 0)
	pd := migrationData{
		SrcDocroots: []cpanel.DomainDataEntry{{Domain: "example.com", DocumentRoot: "/home/src/example.com"}},
		DomainTypeIssues: map[string]DomainTypeIssue{
			"example.com": {
				Domain:           "example.com",
				SourceType:       model.Addon,
				ExpectedDestType: model.Addon,
				DestinationName:  "example.com",
				DestinationType:  model.Parked,
				DestDocrootType:  "parked_domain",
				BlockWeb:         true,
				BlockDBConfig:    true,
			},
		},
		Databases: []cpanel.DatabaseEntry{{Database: "src_db", Users: []string{"src_user"}}},
		DBUsers:   []cpanel.DBUserEntry{{User: "src_user", Databases: []string{"src_db"}}},
		SiteCreds: []dbmig.SiteCreds{{
			Docroot:    "/home/src/example.com",
			ConfigPath: "/home/src/example.com/wp-config.php",
			Kind:       dbmig.KindWordPress,
			Creds: wpconfig.Creds{
				DBName:     "src_db",
				DBUser:     "src_user",
				DBPassword: "pw",
			},
		}},
	}

	compareDBs(pd, log, nil)
	out := buf.String()
	if !strings.Contains(out, "skip") || !strings.Contains(out, "destination domain type compatibility") {
		t.Fatalf("dry-run should preview the DB skip/manual type issue:\n%s", out)
	}
}

// TestRewriteDestConfigsDemotesRemoteHost drives the read-after-write reread end to
// end: a dest config whose name/user/password rewrite SUCCEEDS but whose DB_HOST is a
// remote server (never rewritten) is demoted to NOT rewritten (so the run ends
// non-zero), while a local-host config rewrites cleanly.
func TestRewriteDestConfigsDemotesRemoteHost(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	home := t.TempDir()
	dest := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer dest.Close()

	srcDocroot := "/home/u/public_html/site.com"
	srcConfig := srcDocroot + "/wp-config.php"
	destDocroot := filepath.Join(home, "public_html", "site.com")
	destConfig := filepath.Join(destDocroot, "wp-config.php") // = MapConfigPath(srcConfig, …)
	if err := os.MkdirAll(destDocroot, 0o755); err != nil {
		t.Fatal(err)
	}
	pd := migrationData{
		SrcDocroots:  []cpanel.DomainDataEntry{{Domain: "site.com", DocumentRoot: srcDocroot}},
		DestDocroots: []cpanel.DomainDataEntry{{Domain: "site.com", DocumentRoot: destDocroot}},
	}
	it := dbmig.DBPlanItem{
		SrcDB: "old_db", DestDB: "new_db", DestUser: "new_user",
		Configs: []dbmig.DBConfigRef{{ConfigPath: srcConfig, Kind: dbmig.KindWordPress}},
	}
	newReporter := func() (*report.Reporter, *bytes.Buffer) {
		var file bytes.Buffer
		rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
		if err != nil {
			t.Fatal(err)
		}
		return rep, &file
	}

	// Remote host: name/user/password rewrite, but the site still points at 10.0.0.5.
	remote := "<?php\ndefine('DB_NAME','old_db');\ndefine('DB_USER','old_user');\n" +
		"define('DB_PASSWORD','old_pass');\ndefine('DB_HOST','10.0.0.5');\n$table_prefix='wp_';\n"
	if err := os.WriteFile(destConfig, []byte(remote), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, file := newReporter()
	rewritten, notRewritten, _ := rewriteDestConfigs(context.Background(), dest, pd, it, "new_pass", false, logx.NewTo(io.Discard, 0), rep)
	if rewritten != 0 || notRewritten != 1 {
		t.Fatalf("remote-host config: rewritten=%d notRewritten=%d, want 0 and 1 (demoted)", rewritten, notRewritten)
	}
	if !strings.Contains(file.String(), "does not point at the destination DB") {
		t.Errorf("report should explain the demotion:\n%s", file.String())
	}
	got, _ := os.ReadFile(destConfig)
	if !strings.Contains(string(got), "new_db") || !strings.Contains(string(got), "10.0.0.5") {
		t.Errorf("rewrite should land name but leave the host: %s", got)
	}

	// Local host (no DB_HOST define => local): rewrites cleanly, not demoted.
	local := "<?php\ndefine('DB_NAME','old_db');\ndefine('DB_USER','old_user');\n" +
		"define('DB_PASSWORD','old_pass');\n$table_prefix='wp_';\n"
	if err := os.WriteFile(destConfig, []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, _ = newReporter()
	rewritten, notRewritten, _ = rewriteDestConfigs(context.Background(), dest, pd, it, "new_pass", false, logx.NewTo(io.Discard, 0), rep)
	if rewritten != 1 || notRewritten != 0 {
		t.Errorf("local-host config: rewritten=%d notRewritten=%d, want 1 and 0 (clean)", rewritten, notRewritten)
	}
}

// TestReportUnmigratedDBConfigs: a source docroot carrying a DB-config format the tool does
// not discover/rewrite (Magento 1 local.xml) with NO handled config is surfaced as a MANUAL
// line and counted (folded into the non-zero outcome); a docroot that yielded a handled
// config is suppressed even with a marker.
func TestReportUnmigratedDBConfigs(t *testing.T) {
	sshtest.RequireTools(t, "bash", "grep")
	home := t.TempDir()
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer src.Close()
	docroot := filepath.Join(home, "shop")
	if err := os.MkdirAll(filepath.Join(docroot, "app", "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docroot, "app", "etc", "local.xml"),
		[]byte("<config><connection><dbname>shop</dbname></connection></config>"), 0o644); err != nil {
		t.Fatal(err)
	}
	pd := migrationData{SrcDocroots: []cpanel.DomainDataEntry{{Domain: "shop.com", DocumentRoot: docroot}}}

	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	if n := reportUnmigratedDBConfigs(context.Background(), src, pd, logx.NewTo(io.Discard, 0), rep); n != 1 {
		t.Fatalf("unhandled Magento 1 local.xml: count=%d, want 1", n)
	}
	if !strings.Contains(file.String(), "Magento 1 detected") {
		t.Errorf("report must surface the unmigrated app:\n%s", file.String())
	}

	// Suppressed when the docroot yielded a handled config.
	pd.SiteCreds = []dbmig.SiteCreds{{Docroot: docroot, Creds: wpconfig.Creds{DBName: "shop_db"}}}
	var file2 bytes.Buffer
	rep2, _ := report.NewReporter(io.Discard, &file2, "src", "dst", "now")
	if n := reportUnmigratedDBConfigs(context.Background(), src, pd, logx.NewTo(io.Discard, 0), rep2); n != 0 {
		t.Fatalf("a handled docroot must be suppressed: count=%d, want 0", n)
	}
}

// TestReportUnmigratedDBConfigsRegistryOnlyDoesNotSuppress: a docroot whose ONLY "handled"
// credential is a Softaculous registry entry (FromRegistry — a credential fallback, never a
// rewrite target) must NOT suppress detection of an unsupported DB-config marker. The DB may
// migrate via the registry creds, but a coexisting Magento 1 / PrestaShop 1.7 / Symfony /
// SilverStripe config is never rewritten, so it still points at the OLD database and must
// surface as a MANUAL line. Mirrors BuildPlanWithMapping, which already excludes FromRegistry
// creds from the rewrite list (finding codex_issue #2).
func TestReportUnmigratedDBConfigsRegistryOnlyDoesNotSuppress(t *testing.T) {
	sshtest.RequireTools(t, "bash", "grep")
	home := t.TempDir()
	src := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer src.Close()
	docroot := filepath.Join(home, "shop")
	if err := os.MkdirAll(filepath.Join(docroot, "app", "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docroot, "app", "etc", "local.xml"),
		[]byte("<config><connection><dbname>shop</dbname></connection></config>"), 0o644); err != nil {
		t.Fatal(err)
	}
	pd := migrationData{
		SrcDocroots: []cpanel.DomainDataEntry{{Domain: "shop.com", DocumentRoot: docroot}},
		// Only a registry-sourced credential covers this docroot. It supplies owner/password
		// but is NOT a rewrite target, so it must not mark the docroot handled.
		SiteCreds: []dbmig.SiteCreds{{
			Docroot:      docroot,
			FromRegistry: true,
			Creds:        wpconfig.Creds{DBName: "shop_db"},
		}},
	}
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	if n := reportUnmigratedDBConfigs(context.Background(), src, pd, logx.NewTo(io.Discard, 0), rep); n != 1 {
		t.Fatalf("registry-only docroot must NOT suppress an unsupported marker: count=%d, want 1", n)
	}
	if !strings.Contains(file.String(), "Magento 1 detected") {
		t.Errorf("report must surface the unmigrated app despite the registry-only cred:\n%s", file.String())
	}

	// A real (rewritable) cred coexisting with the registry cred on the SAME docroot must
	// STILL suppress: the real config is a rewrite target, so the docroot is genuinely
	// handled. Guards against a regression that drops a docroot whenever ANY of its creds is
	// FromRegistry, instead of only skipping the registry cred itself.
	pd.SiteCreds = append(pd.SiteCreds, dbmig.SiteCreds{Docroot: docroot, Creds: wpconfig.Creds{DBName: "shop_db"}})
	var file2 bytes.Buffer
	rep2, _ := report.NewReporter(io.Discard, &file2, "src", "dst", "now")
	if n := reportUnmigratedDBConfigs(context.Background(), src, pd, logx.NewTo(io.Discard, 0), rep2); n != 0 {
		t.Fatalf("a real cred alongside a registry cred must still suppress: count=%d, want 0", n)
	}
}

// TestRewriteDestConfigsStructurallyAmbiguousTierPolicy: a config whose value/host checks
// pass after rewrite but whose DB_NAME is structurally ambiguous (a heredoc-embedded decoy
// the rewrite edits while the live define still points at the source DB — finding V35) is a
// SOFT "not independently verified" note at the default tier (counted as notVerified, run
// stays zero) and a HARD failure under --deep-verify (counted as notRewritten).
func TestRewriteDestConfigsStructurallyAmbiguousTierPolicy(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	home := t.TempDir()
	dest := sshtest.DialExec(t, sshtest.NewExecServer(t, home))
	defer dest.Close()

	srcDocroot := "/home/u/public_html/site.com"
	srcConfig := srcDocroot + "/wp-config.php"
	destDocroot := filepath.Join(home, "public_html", "site.com")
	destConfig := filepath.Join(destDocroot, "wp-config.php")
	if err := os.MkdirAll(destDocroot, 0o755); err != nil {
		t.Fatal(err)
	}
	pd := migrationData{
		SrcDocroots:  []cpanel.DomainDataEntry{{Domain: "site.com", DocumentRoot: srcDocroot}},
		DestDocroots: []cpanel.DomainDataEntry{{Domain: "site.com", DocumentRoot: destDocroot}},
	}
	it := dbmig.DBPlanItem{
		SrcDB: "old_db", DestDB: "new_db", DestUser: "new_user",
		Configs: []dbmig.DBConfigRef{{ConfigPath: srcConfig, Kind: dbmig.KindWordPress}},
	}
	// A heredoc decoy DB_NAME (leftmost in the blind view) the rewrite edits, plus the
	// real live DB_NAME left pointing at the source DB. After rewrite the blind value/host
	// checks pass while the live define is unchanged -> structurally ambiguous.
	initial := "<?php\n$h = <<<EOT\ndefine('DB_NAME','old_db');\nEOT;\n" +
		"define('DB_NAME','old_db');\ndefine('DB_USER','old_user');\n" +
		"define('DB_PASSWORD','old_pass');\n$table_prefix='wp_';\n"
	newReporter := func() (*report.Reporter, *bytes.Buffer) {
		var file bytes.Buffer
		rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
		if err != nil {
			t.Fatal(err)
		}
		return rep, &file
	}

	// Default tier: SOFT note, run stays zero.
	if err := os.WriteFile(destConfig, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, file := newReporter()
	rw, nr, nv := rewriteDestConfigs(context.Background(), dest, pd, it, "new_pass", false, logx.NewTo(io.Discard, 0), rep)
	if rw != 0 || nr != 0 || nv != 1 {
		t.Fatalf("default tier: rewritten=%d notRewritten=%d notVerified=%d, want 0/0/1 (soft note)\n%s", rw, nr, nv, file.String())
	}
	if !strings.Contains(file.String(), "not independently verified") {
		t.Errorf("default report should carry the UNVERIFIED note:\n%s", file.String())
	}

	// --deep tier: HARD failure (notRewritten), so the run ends non-zero.
	if err := os.WriteFile(destConfig, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, file = newReporter()
	rw, nr, nv = rewriteDestConfigs(context.Background(), dest, pd, it, "new_pass", true, logx.NewTo(io.Discard, 0), rep)
	if rw != 0 || nr != 1 || nv != 0 {
		t.Fatalf("--deep tier: rewritten=%d notRewritten=%d notVerified=%d, want 0/1/0 (hard)\n%s", rw, nr, nv, file.String())
	}
	if !strings.Contains(file.String(), "deep-verify") {
		t.Errorf("--deep report should mark it not provable under --deep-verify:\n%s", file.String())
	}
}

// TestDBResultLine pins the per-database outcome decision: a fully-rewritten database
// is a clean [db ok] (partial=false), but a database whose data migrated while a site
// config was left NOT rewritten is [db PARTIAL] and partial=true — which applyDBs turns
// into a non-zero run outcome, so a broken-but-green cutover is impossible.
func TestDBResultLine(t *testing.T) {
	ok, partial := dbResultLine("d_db", 10, 0, 2048, true)
	if partial {
		t.Errorf("nUnrewritten=0 must be a clean result, got partial=true")
	}
	if !strings.Contains(ok, "[db ok]") {
		t.Errorf("clean result should be [db ok], got %q", ok)
	}
	// A failed post-import table count must not render a misleading "0 tables" success.
	unknown, _ := dbResultLine("d_db", 0, 0, 2048, false)
	if !strings.Contains(unknown, "table count unavailable") {
		t.Errorf("count-unavailable result must say so, not '0 tables': %q", unknown)
	}
	bad, partial := dbResultLine("d_db", 10, 2, 2048, true)
	if !partial {
		t.Errorf("nUnrewritten>0 must be partial=true (incomplete cutover)")
	}
	if !strings.Contains(bad, "[db PARTIAL]") || !strings.Contains(bad, "OLD database") {
		t.Errorf("unrewritten result should be a [db PARTIAL] line, got %q", bad)
	}
}

// TestIsAlreadyExistsLocalized is the regression for the "re-run fails on
// databases that already exist" bug: the destination cPanel is LOCALIZED in
// Polish, so create_user returned "… już istnieje", not "already exists". The
// old check matched only the English phrase and therefore treated a normal
// "already exists" as a hard failure, aborting the database (and skipping its
// data import).
func TestIsAlreadyExistsLocalized(t *testing.T) {
	// The exact message observed on the destination (Polish), as wrapped by
	// parseUAPI: "Mysql::create_user: status=0 errors=[Nie można utworzyć
	// użytkownika „destacct_wp395”, ponieważ już istnieje.]".
	polish := errors.New(`Mysql::create_user: status=0 errors=[Nie można utworzyć użytkownika „destacct_wp395”, ponieważ już istnieje.]`)
	if !isAlreadyExists(polish) {
		t.Errorf("Polish 'już istnieje' must be recognized as already-exists: %v", polish)
	}

	// Proof of the regression: the real message does NOT contain the English
	// phrase, so the old single-language check would have missed it.
	if got := errors.New("already exists"); !isAlreadyExists(got) {
		t.Error("English phrase must still be recognized")
	}

	// Real cPanel translations, taken verbatim from github.com/CpanelInc/cplocales
	// for BOTH messages create_user / create_database can emit. Several languages
	// phrase the two differently, so both forms must be recognized.
	realMessages := []string{
		// Polish (verified live on the destination): user + database.
		`Nie można utworzyć użytkownika „vh_wp1”, ponieważ już istnieje.`,
		`Baza danych programu [asis,MySQL] o nazwie „vh_wp1” już istnieje.`,
		// Spanish: user + database.
		`No se puede crear al usuario “u” porque ya existe.`,
		`Ya existe una base de datos [asis,MySQL] con el nombre “d”.`,
		// French: user + database.
		`L’utilisateur « u » ne peut pas être créé, car il existe déjà.`,
		`Une base de données [asis,MySQL] nommée « d » existe déjà.`,
		// Italian: user ("esiste già") AND database ("già esistente") — different!
		`L’utente “u” non può essere creato perché esiste già.`,
		`Database [asis,MySQL] con il nome “d” già esistente.`,
		// Portuguese (pt_BR): user + database.
		`O usuário “u” não pode ser criado porque já existe.`,
		`Um banco de dados [asis,MySQL] com o nome “d” já existe.`,
		// Dutch: user ("al bestaat") AND database ("bestaat al") — different order!
		`De gebruiker ’u’ kan niet worden gemaakt omdat deze al bestaat.`,
		`Er bestaat al een [asis,MySQL]-database met de naam ’d’.`,
		// Ukrainian: user ("вже існує") AND database ("уже існує") — в- vs у-!
		`Не вдається створити користувача „u”: такий користувач вже існує.`,
		`База даних [asis,MySQL] з іменем „d” уже існує.`,
		// Japanese + Chinese: user + database.
		`ユーザー “u” は既に存在するため、作成できません。`,
		`“d” という名前の [asis,MySQL] データベースは既に存在します。`,
		`用户“u”已存在，无法创建。`,
		`名称为“d”的 [asis,MySQL] 数据库已存在。`,
	}
	for _, msg := range realMessages {
		if !isAlreadyExists(errors.New(msg)) {
			t.Errorf("real cPanel locale message should be recognized as already-exists: %q", msg)
		}
	}
}

// TestProvisionDestProceedsOnUnrecognizedLocale is the direct answer to "does it
// understand the warning in Polish AND other languages?": the migration must NOT
// depend on the language at all. Here create_user / create_database fail with an
// "already exists" message in a locale that is NOT in alreadyExistsMarkers
// (Turkish). provisionDest must still proceed — calling set_password and
// set_privileges — and return nil, because correctness comes from those steps +
// the import, not from parsing the error text.
func TestProvisionDestProceedsOnUnrecognizedLocale(t *testing.T) {
	// Sanity: confirm Turkish is genuinely NOT recognized, so this test really
	// exercises the "unknown language" path.
	turkish := "zaten mevcut" // Turkish for "already exists" — intentionally not in the list
	if isAlreadyExists(errors.New(turkish)) {
		t.Fatalf("test premise broken: %q is recognized; pick a truly unknown locale", turkish)
	}

	f := &fakeUAPI{reply: map[string]string{
		"Mysql create_user":                uapiErr("Kullanıcı oluşturulamadı: " + turkish), // fails, unknown language
		"Mysql set_password":               uapiOK,
		"Mysql create_database":            uapiErr("Veritabanı " + turkish), // fails, unknown language
		"Mysql set_privileges_on_database": uapiOK,
	}}
	it := dbmig.DBPlanItem{SrcDB: "srcacct_wp1", DestDB: "vh_wp1", DestUser: "vh_wp1"}

	err := provisionDest(context.Background(), f, it, "pw")
	if err != nil {
		t.Fatalf("provisionDest must proceed despite unrecognized-locale 'already exists' errors, got: %v", err)
	}
	// It must have gone on to the steps that carry the real outcome.
	if !contains(f.calls, "Mysql set_password") {
		t.Error("must call set_password even after create_user failed")
	}
	if !contains(f.calls, "Mysql set_privileges_on_database") {
		t.Error("must call set_privileges even after create_database failed")
	}
}

// TestProvisionDestFailsOnRealError proves the other side: a genuine failure of a
// step that MUST succeed (set_password) aborts provisionDest, so a real problem
// is never hidden — regardless of the create_* outcome.
func TestProvisionDestFailsOnRealError(t *testing.T) {
	f := &fakeUAPI{reply: map[string]string{
		"Mysql create_user":  uapiOK,
		"Mysql set_password": uapiErr("Access denied"), // a step that must succeed fails
	}}
	it := dbmig.DBPlanItem{SrcDB: "srcacct_wp1", DestDB: "vh_wp1", DestUser: "vh_wp1"}

	err := provisionDest(context.Background(), f, it, "pw")
	if err == nil {
		t.Fatal("provisionDest must fail when set_password (a required step) fails")
	}
	if !strings.Contains(err.Error(), "Access denied") {
		t.Errorf("error should surface the real cause, got: %v", err)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// TestIsAlreadyExistsRejectsRealErrors ensures a genuine, unrelated failure is
// NOT mistaken for "already exists" (which would wrongly downgrade its log and,
// historically, suppress it). These must return false.
func TestIsAlreadyExistsRejectsRealErrors(t *testing.T) {
	for _, msg := range []string{
		"Mysql::create_user: status=0 errors=[Access denied for user]",
		"Mysql::create_database: status=0 errors=[Disk quota exceeded]",
		"connection refused",
		"",
	} {
		if isAlreadyExists(errors.New(msg)) {
			t.Errorf("real error must NOT be treated as already-exists: %q", msg)
		}
	}
	if isAlreadyExists(nil) {
		t.Error("nil error must not be already-exists")
	}
}

// TestDBVerdict covers the verify decision: a real divergence, a match, and the
// previously-conflated "couldn't read a count" case which must now surface as
// readErr (UNREADABLE) instead of a misleading 0/0 DIFF.
func TestDBVerdict(t *testing.T) {
	readErr := errors.New("connection reset")
	zero := dbmig.ObjectCounts{}
	withView := dbmig.ObjectCounts{Views: 1}

	cases := []struct {
		name                   string
		srcT, destT            int
		srcO, destO            dbmig.ObjectCounts
		schemaDelta            dbSchemaDiff
		errS, errD             error
		wantMatch, wantReadErr bool
	}{
		{"equal schema", 5, 5, zero, zero, dbSchemaDiff{}, nil, nil, true, false},
		{"different table counts", 5, 4, zero, zero, dbSchemaDiff{MissingTables: []string{"b"}}, nil, nil, false, false},
		{"equal table count but table names differ", 2, 2, zero, zero, dbSchemaDiff{MissingTables: []string{"b"}, ExtraTables: []string{"x"}}, nil, nil, false, false},
		{"equal counts but object names differ", 5, 5, withView, withView, dbSchemaDiff{MissingViews: []string{"v1"}, ExtraViews: []string{"v2"}}, nil, nil, false, false},
		{"equal tables, objects differ", 5, 5, withView, zero, dbSchemaDiff{MissingViews: []string{"v1"}}, nil, nil, false, false},
		{"source schema unreadable", 0, 0, zero, zero, dbSchemaDiff{}, readErr, nil, false, true},
		{"dest schema unreadable", 0, 0, zero, zero, dbSchemaDiff{}, nil, readErr, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			match, re := dbVerdict(c.srcT, c.destT, c.srcO, c.destO, c.schemaDelta, c.errS, c.errD)
			if match != c.wantMatch || re != c.wantReadErr {
				t.Errorf("dbVerdict = (match=%v, readErr=%v), want (match=%v, readErr=%v)",
					match, re, c.wantMatch, c.wantReadErr)
			}
		})
	}
}

// TestDiffCharsets covers the mojibake-detection comparison: identical
// fingerprints match; a schema-default change is a DB diff; a per-table collation
// change is a table diff; a table missing on the destination is NOT reported here
// (the table-count verdict already owns that).
func TestDiffCharsets(t *testing.T) {
	src := dbmig.CharsetInfo{
		DBCharset: "utf8mb4", DBCollation: "utf8mb4_general_ci",
		Tables: map[string]string{"a": "utf8mb4_general_ci", "b": "utf8mb4_general_ci"},
	}
	if d, td := diffCharsets(src, src); d || len(td) != 0 {
		t.Errorf("identical charsets must match: dbDiff=%v tableDiffs=%v", d, td)
	}

	// Schema default re-encoded (the classic utf8 -> latin1 mojibake setup).
	mojibake := dbmig.CharsetInfo{DBCharset: "latin1", DBCollation: "latin1_swedish_ci", Tables: src.Tables}
	if d, _ := diffCharsets(src, mojibake); !d {
		t.Error("schema default charset change must be a DB diff")
	}

	// One table re-collated.
	reColl := dbmig.CharsetInfo{
		DBCharset: "utf8mb4", DBCollation: "utf8mb4_general_ci",
		Tables: map[string]string{"a": "latin1_swedish_ci", "b": "utf8mb4_general_ci"},
	}
	d, td := diffCharsets(src, reColl)
	if d || len(td) != 1 || td[0] != "a (utf8mb4_general_ci->latin1_swedish_ci)" {
		t.Errorf("one table re-collated: dbDiff=%v tableDiffs=%v", d, td)
	}

	// A table missing on the destination is not a collation diff (counts catch it).
	missing := dbmig.CharsetInfo{
		DBCharset: "utf8mb4", DBCollation: "utf8mb4_general_ci",
		Tables: map[string]string{"a": "utf8mb4_general_ci"},
	}
	if _, td := diffCharsets(src, missing); len(td) != 0 {
		t.Errorf("missing table must not be a collation diff: %v", td)
	}
}

func TestCsVerdict(t *testing.T) {
	cases := []struct {
		name            string
		comparable      bool
		dbDiff          bool
		nTableDiffs     int
		wantOK          bool
		wantDefaultOnly bool
	}{
		{"clean match", true, false, 0, true, false},
		// Only the DB default differs, all tables match -> SOFT advisory.
		{"db default only", true, true, 0, false, true},
		// A table re-collated -> hard DIFF, NEVER soft, even if the db default also differs.
		{"table recollated", true, false, 1, false, false},
		{"db default + table recollated", true, true, 2, false, false},
		// An unreadable side is neither OK nor a soft default-only case (stays UNVERIFIED).
		{"not comparable", false, true, 0, false, false},
		{"not comparable no diff", false, false, 0, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, defOnly := csVerdict(tc.comparable, tc.dbDiff, tc.nTableDiffs)
			if ok != tc.wantOK || defOnly != tc.wantDefaultOnly {
				t.Errorf("csVerdict(%v,%v,%d) = (ok=%v, defaultOnly=%v); want (ok=%v, defaultOnly=%v)",
					tc.comparable, tc.dbDiff, tc.nTableDiffs, ok, defOnly, tc.wantOK, tc.wantDefaultOnly)
			}
			// Safety invariant: defaultOnly and OK are mutually exclusive, and defaultOnly
			// can never be true when a table collation differs (that must stay hard).
			if ok && defOnly {
				t.Error("csOK and csDBDefaultOnly must be mutually exclusive")
			}
			if defOnly && tc.nTableDiffs > 0 {
				t.Error("csDBDefaultOnly must never be true when a table collation differs")
			}
		})
	}
}

func TestDiffSchemaSameCountsDifferentNames(t *testing.T) {
	src := dbmig.SchemaFingerprint{
		Tables:   map[string]struct{}{"a": {}, "b": {}},
		Views:    map[string]struct{}{"v1": {}},
		Triggers: map[string]struct{}{"trg1": {}},
		Routines: map[string]struct{}{"PROCEDURE p1": {}},
		Events:   map[string]struct{}{"ev1": {}},
	}
	dest := dbmig.SchemaFingerprint{
		Tables:   map[string]struct{}{"a": {}, "x": {}},
		Views:    map[string]struct{}{"v2": {}},
		Triggers: map[string]struct{}{"trg2": {}},
		Routines: map[string]struct{}{"PROCEDURE p2": {}},
		Events:   map[string]struct{}{"ev2": {}},
	}

	d := diffSchema(src, dest)
	if d.Empty() {
		t.Fatal("same object counts with different names must not be considered equal")
	}
	for name, got := range map[string][]string{
		"missing tables":   d.MissingTables,
		"extra tables":     d.ExtraTables,
		"missing views":    d.MissingViews,
		"extra views":      d.ExtraViews,
		"missing triggers": d.MissingTriggers,
		"extra triggers":   d.ExtraTriggers,
		"missing routines": d.MissingRoutines,
		"extra routines":   d.ExtraRoutines,
		"missing events":   d.MissingEvents,
		"extra events":     d.ExtraEvents,
	} {
		if len(got) != 1 {
			t.Fatalf("%s = %v, want one entry", name, got)
		}
	}
	if !strings.Contains(d.Detail(), "missing tables: b") || !strings.Contains(d.Detail(), "extra tables: x") {
		t.Errorf("Detail should name table drift, got %q", d.Detail())
	}
}

// TestDiffDeepTables covers the deep verify comparison: missing/extra tables,
// exact row-count mismatches, and equal-row checksum mismatches are HARD
// divergences; AUTO_INCREMENT drift is informational.
func TestDiffDeepTables(t *testing.T) {
	src := dbmig.DeepDBInfo{Version: "10.5", Tables: map[string]dbmig.DeepTable{
		"a": {Rows: 100, AutoIncr: 101},
		"b": {Rows: 50, AutoIncr: 51},
		"c": {Rows: 7, AutoIncr: 8},
	}}
	dest := dbmig.DeepDBInfo{Version: "10.5", Tables: map[string]dbmig.DeepTable{
		"a": {Rows: 98, AutoIncr: 101}, // 2 rows lost -> HARD
		"b": {Rows: 50, AutoIncr: 60},  // rows equal, autoincr drift -> info only
		// "c" missing on dest -> HARD
		"x": {Rows: 1, AutoIncr: 2}, // extra on dest -> HARD
	}}
	srcCk := map[string]string{"a": "X", "b": "Y", "c": "C"}
	destCk := map[string]string{"a": "X", "b": "Z"} // b: same rows, different checksum -> HARD

	res := diffDeepTables(src, dest, srcCk, destCk)
	if !res.HardDiff() {
		t.Fatal("missing/extra/row/checksum differences must be a hard deep diff")
	}
	if len(res.MissingTables) != 1 || res.MissingTables[0] != "c" {
		t.Errorf("MissingTables = %v, want [c]", res.MissingTables)
	}
	if len(res.ExtraTables) != 1 || res.ExtraTables[0] != "x" {
		t.Errorf("ExtraTables = %v, want [x]", res.ExtraTables)
	}
	if len(res.RowDiffs) != 1 || res.RowDiffs[0] != "a (100->98)" {
		t.Errorf("RowDiffs = %v, want [a (100->98)]", res.RowDiffs)
	}
	if len(res.ChecksumDiffs) != 1 || res.ChecksumDiffs[0] != "b" {
		t.Errorf("ChecksumDiffs = %v, want [b]", res.ChecksumDiffs)
	}
	if len(res.AutoIncrDiffs) != 1 || res.AutoIncrDiffs[0] != "b (51->60)" {
		t.Errorf("AutoIncrDiffs = %v, want [b (51->60)]", res.AutoIncrDiffs)
	}

	// Without checksums (versions differed), no soft checksum diffs are produced.
	noCk := diffDeepTables(src, dest, nil, nil)
	if len(noCk.ChecksumDiffs) != 0 {
		t.Errorf("no-checksum path must yield no ChecksumDiffs: %v", noCk.ChecksumDiffs)
	}
	// A row mismatch takes precedence over a checksum mismatch on the same table.
	if len(noCk.RowDiffs) != 1 {
		t.Errorf("RowDiffs should still be found without checksums: %v", noCk.RowDiffs)
	}
}

// TestDiffDeepTablesContentUnchecked is the Step 14 deep-verify regression: a
// common, equal-row table whose content checksum is missing or NULL ("") on either
// side must be reported ContentUnchecked (→ UNVERIFIED), never silently passed by
// empty-string equality, and never miscounted as a hard ChecksumDiff.
func TestDiffDeepTablesContentUnchecked(t *testing.T) {
	base := func() (dbmig.DeepDBInfo, dbmig.DeepDBInfo) {
		return dbmig.DeepDBInfo{Version: "10.5", Tables: map[string]dbmig.DeepTable{"t": {Rows: 100, AutoIncr: 10}}},
			dbmig.DeepDBInfo{Version: "10.5", Tables: map[string]dbmig.DeepTable{"t": {Rows: 100, AutoIncr: 10}}}
	}

	// 1) present + EQUAL checksums -> clean pass.
	src, dest := base()
	if r := diffDeepTables(src, dest, map[string]string{"t": "ABC"}, map[string]string{"t": "ABC"}); r.HardDiff() || r.ContentUnchecked || len(r.ChecksumDiffs) != 0 {
		t.Errorf("equal present checksums must be a clean pass: %+v", r)
	}

	// 2) present + DIFFERENT checksums -> hard ChecksumDiff (not unchecked).
	src, dest = base()
	if r := diffDeepTables(src, dest, map[string]string{"t": "ABC"}, map[string]string{"t": "XYZ"}); !r.HardDiff() || r.ContentUnchecked || len(r.ChecksumDiffs) != 1 {
		t.Errorf("differing present checksums must be a hard ChecksumDiff, not unchecked: %+v", r)
	}

	// 3) checksum MISSING from one side (key absent -> "") -> ContentUnchecked.
	src, dest = base()
	r := diffDeepTables(src, dest, map[string]string{"t": "ABC"}, map[string]string{})
	if !r.ContentUnchecked || len(r.ChecksumDiffs) != 0 || r.HardDiff() || r.UncheckedReason == "" {
		t.Errorf("a missing checksum on one side must be ContentUnchecked with a reason, not a diff/pass: %+v", r)
	}

	// 4) CORE REGRESSION: NULL ("") on BOTH sides -> ContentUnchecked (old code passed).
	src, dest = base()
	r = diffDeepTables(src, dest, map[string]string{"t": ""}, map[string]string{"t": ""})
	if !r.ContentUnchecked || len(r.ChecksumDiffs) != 0 || r.HardDiff() {
		t.Errorf("two NULL/empty checksums must be ContentUnchecked, not a silent pass: %+v", r)
	}

	// 5) nil maps (versions differed) -> diffDeepTables sets nothing here (deepDB does).
	src, dest = base()
	if r := diffDeepTables(src, dest, nil, nil); r.ContentUnchecked || len(r.ChecksumDiffs) != 0 || r.HardDiff() {
		t.Errorf("nil checksum maps must not produce ContentUnchecked/diffs in diffDeepTables: %+v", r)
	}

	// 6) MIX in one DB: an OK table, a real ChecksumDiff, and a both-NULL unproven
	//    table coexist without cross-contamination, and the reason names the unproven
	//    table (not the OK or differing one).
	tbls := func() map[string]dbmig.DeepTable {
		return map[string]dbmig.DeepTable{"ok": {Rows: 100}, "bad": {Rows: 100}, "unp": {Rows: 100}}
	}
	r = diffDeepTables(
		dbmig.DeepDBInfo{Version: "10.5", Tables: tbls()},
		dbmig.DeepDBInfo{Version: "10.5", Tables: tbls()},
		map[string]string{"ok": "A", "bad": "B", "unp": ""},
		map[string]string{"ok": "A", "bad": "Z", "unp": ""})
	if len(r.ChecksumDiffs) != 1 || r.ChecksumDiffs[0] != "bad" {
		t.Errorf("MIX: ChecksumDiffs must be exactly [bad], got %v", r.ChecksumDiffs)
	}
	if !r.ContentUnchecked || !strings.Contains(r.UncheckedReason, "unp") {
		t.Errorf("MIX: the both-NULL table must be ContentUnchecked and named in the reason: %+v", r)
	}
	if strings.Contains(r.UncheckedReason, "bad") || strings.Contains(r.UncheckedReason, "ok") {
		t.Errorf("MIX: the unchecked reason must name only the unproven table: %q", r.UncheckedReason)
	}
	if !r.HardDiff() {
		t.Error("MIX: a real ChecksumDiff alongside an unchecked table is still a hard diff")
	}
}

func TestDeepDBResultUnverifiedReason(t *testing.T) {
	res := deepDBResult{ContentUnchecked: true, UncheckedReason: "checksum read failed"}
	if res.HardDiff() {
		t.Error("unchecked content is an UNVERIFIED state, not a schema/content diff by itself")
	}
	if got := res.UnverifiedReason(); got != "checksum read failed" {
		t.Errorf("UnverifiedReason = %q", got)
	}
	if got := (deepDBResult{}).UnverifiedReason(); !strings.Contains(got, "fingerprint") {
		t.Errorf("zero-value UnverifiedReason should explain missing fingerprint, got %q", got)
	}
}

// TestDiffObjectBodies: only objects present on BOTH sides with a different body hash
// are reported (a missing/extra object is the name-set diff's job, not this one).
func TestDiffObjectBodies(t *testing.T) {
	src := map[string]string{"view v": "A", "procedure p": "B", "trigger g": "C", "event e": "D"}
	dest := map[string]string{"view v": "X", "procedure p": "B", "trigger g": "C2", "event e": "D"}
	got := strings.Join(diffObjectBodies(src, dest), ",")
	want := "trigger g,view v" // sorted; p and e are equal
	if got != want {
		t.Errorf("diffObjectBodies = %q, want %q", got, want)
	}
	// An object only on one side is NOT a body diff (it is a missing/extra name elsewhere).
	only := diffObjectBodies(map[string]string{"view only": "A"}, map[string]string{})
	if len(only) != 0 {
		t.Errorf("a one-sided object must not be a body diff: %v", only)
	}
	// Fully equal -> no diffs; HardDiff stays false on a body-only result.
	if d := diffObjectBodies(src, src); len(d) != 0 {
		t.Errorf("equal bodies must yield no diff: %v", d)
	}
	if (deepDBResult{ObjectDiffs: []string{"view v"}}).HardDiff() != true {
		t.Error("a populated ObjectDiffs must count as a hard diff")
	}
}

func TestVerifyDBsTableSetDriftIsDivergent(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	srcHome := t.TempDir()
	dstHome := t.TempDir()
	installFakeMySQL(t, srcHome, dstHome)

	pool := &sshx.Pool{
		Src:  sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)),
		Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome)),
	}
	defer pool.Src.Close()
	defer pool.Dest.Close()

	pd := migrationData{
		Databases: []cpanel.DatabaseEntry{{
			Database: "srcacct_db",
			Users:    []string{"srcacct_u"},
		}},
		SrcMySQLRestrictions:  testMySQLPrefix("srcacct_"),
		DestMySQLRestrictions: testMySQLPrefix("destacct_"),
	}
	var screen, file bytes.Buffer
	rep, err := report.NewReporter(&screen, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	destCreds := map[string]destCred{"destacct_db": {user: "destacct_u", pass: "pw"}}

	realDiff, err := verifyDBs(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep,
		"srcacct", "srcpw", nil, destCreds, false)
	if err != nil {
		t.Fatalf("verifyDBs: %v", err)
	}
	if realDiff != 1 {
		t.Fatalf("realDiff = %d, want 1 for src tables {a,b} vs dest {a,x}", realDiff)
	}
	out := file.String()
	for _, want := range []string{"[db verify DIFF]", "schema:", "missing tables: b", "extra tables: x"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "[db verify OK]") {
		t.Errorf("report must not contain OK for schema drift:\n%s", out)
	}
}

func installFakeMySQL(t *testing.T, srcHome, dstHome string) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/usr/bin/env bash
set -eu
args="$*"
if [[ "$args" == *"information_schema.tables"* && "$args" == *"information_schema.views"* ]]; then
  if [[ "$HOME" == "$SRC_HOME" ]]; then
    printf 'T\t61\nT\t62\n'
  else
    printf 'T\t61\nT\t78\n'
  fi
  exit 0
fi
if [[ "$args" == *"default_character_set_name"* ]]; then
  printf 'DB\tutf8mb4\tutf8mb4_general_ci\n'
  if [[ "$HOME" == "$SRC_HOME" ]]; then
    printf 'T\ta\tutf8mb4_general_ci\nT\tb\tutf8mb4_general_ci\n'
  else
    printf 'T\ta\tutf8mb4_general_ci\nT\tx\tutf8mb4_general_ci\n'
  fi
  exit 0
fi
# Deep meta (metaCmd): version + HEX base-table names (a,b src / a,x dest). NOT views.
# 61=a 62=b 78=x (metaCmd reads HEX(table_name)).
if [[ "$args" == *"IFNULL(auto_increment"* ]]; then
  printf 'V\t10.5.0-MariaDB\n'
  if [[ "$HOME" == "$SRC_HOME" ]]; then
    printf 'A\t61\t1\nA\t62\t1\n'
  else
    printf 'A\t61\t1\nA\t78\t1\n'
  fi
  exit 0
fi
# Deep row counts ($SQL COUNT(*)): HEX-labelled; common table 'a' (61) equal.
if [[ "$args" == *"COUNT(*)"* ]]; then
  if [[ "$HOME" == "$SRC_HOME" ]]; then
    printf '61\t10\n62\t20\n'
  else
    printf '61\t10\n78\t20\n'
  fi
  exit 0
fi
# Content checksum (same version => runs at default now). Output is schema-qualified
# (<DB_NAME>.<table>); parseChecksums strips the prefix. Common table 'a' equal both
# sides so the headline stays the schema drift (missing b / extra x), not content.
if [[ "$args" == *"CHECKSUM TABLE"* ]]; then
  if [[ "$HOME" == "$SRC_HOME" ]]; then
    printf 'srcacct_db.a\t111\n'
  else
    printf 'destacct_db.a\t111\n'
  fi
  exit 0
fi
# Object bodies (V12): this DB has no non-table objects -> empty output.
if [[ "$args" == *"MD5("* ]]; then
  exit 0
fi
echo "unexpected mysql invocation: $args" >&2
exit 2
`
	mysql := filepath.Join(bin, "mysql")
	if err := os.WriteFile(mysql, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SRC_HOME", srcHome)
	t.Setenv("DST_HOME", dstHome)
}

// installFakeMySQLCharset is a verifyDBs harness focused on the charset leg: the
// schema-fingerprint query returns equal tables {a,b} (or drifted {a,x} on the
// destination when schemaDrift) so the schema verdict is controllable, and the
// charset query returns equal utf8mb4 metadata UNLESS failCharset selects a side
// to fail (exit non-zero — "src", "dest", or "both"), simulating an unreadable
// charset, or charsetDiff makes the destination return latin1 (a real mojibake
// divergence) while the schema stays equal.
func installFakeMySQLCharset(t *testing.T, srcHome, dstHome, failCharset string, schemaDrift, charsetDiff, dbDefaultOnly bool) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/usr/bin/env bash
set -eu
args="$*"
# Charset query: has default_character_set_name, NOT information_schema.views.
if [[ "$args" == *"default_character_set_name"* ]]; then
  if [[ "$FAIL_CS" == "both" ]] || \
     [[ "$FAIL_CS" == "dest" && "$HOME" != "$SRC_HOME" ]] || \
     [[ "$FAIL_CS" == "src"  && "$HOME" == "$SRC_HOME" ]]; then
    echo "ERROR 1045 (28000): charset read denied" >&2
    exit 1
  fi
  if [[ "$CS_DIFF" == "1" && "$HOME" != "$SRC_HOME" ]]; then
    printf 'DB\tlatin1\tlatin1_swedish_ci\n'
    printf 'T\ta\tlatin1_swedish_ci\nT\tb\tlatin1_swedish_ci\n'
  elif [[ "$CS_DB_DEFAULT_ONLY" == "1" && "$HOME" != "$SRC_HOME" ]]; then
    # Only the schema DEFAULT collation differs; per-table collations stay EQUAL to
    # the source, so the verdict must be the SOFT default-only case (not mojibake).
    printf 'DB\tutf8mb4\tutf8mb4_unicode_520_ci\n'
    printf 'T\ta\tutf8mb4_general_ci\nT\tb\tutf8mb4_general_ci\n'
  else
    printf 'DB\tutf8mb4\tutf8mb4_general_ci\n'
    printf 'T\ta\tutf8mb4_general_ci\nT\tb\tutf8mb4_general_ci\n'
  fi
  exit 0
fi
# Schema-fingerprint query: both .tables AND .views. Equal {a,b} unless drift.
if [[ "$args" == *"information_schema.tables"* && "$args" == *"information_schema.views"* ]]; then
  if [[ "$SCHEMA_DRIFT" == "1" && "$HOME" != "$SRC_HOME" ]]; then
    printf 'T\t61\nT\t78\n'
  else
    printf 'T\t61\nT\t62\n'
  fi
  exit 0
fi
# Deep meta (metaCmd): version + HEX base-table names following SCHEMA_DRIFT. NOT views.
# 61=a 62=b 78=x (metaCmd reads HEX(table_name)).
if [[ "$args" == *"IFNULL(auto_increment"* ]]; then
  printf 'V\t10.5.0-MariaDB\n'
  if [[ "$SCHEMA_DRIFT" == "1" && "$HOME" != "$SRC_HOME" ]]; then
    printf 'A\t61\t1\nA\t78\t1\n'
  else
    printf 'A\t61\t1\nA\t62\t1\n'
  fi
  exit 0
fi
# Deep row counts ($SQL COUNT(*)): HEX-labelled; EQUAL counts both sides.
if [[ "$args" == *"COUNT(*)"* ]]; then
  if [[ "$SCHEMA_DRIFT" == "1" && "$HOME" != "$SRC_HOME" ]]; then
    printf '61\t10\n78\t20\n'
  else
    printf '61\t10\n62\t20\n'
  fi
  exit 0
fi
# Content checksum (same version => default tier). Equal hashes per common table so
# the charset/schema legs own these verdicts, never the content leg.
if [[ "$args" == *"CHECKSUM TABLE"* ]]; then
  if [[ "$HOME" == "$SRC_HOME" ]]; then
    printf 'srcacct_db.a\t111\nsrcacct_db.b\t222\n'
  elif [[ "$SCHEMA_DRIFT" == "1" ]]; then
    printf 'destacct_db.a\t111\ndestacct_db.x\t999\n'
  else
    printf 'destacct_db.a\t111\ndestacct_db.b\t222\n'
  fi
  exit 0
fi
# Object bodies (V12): no non-table objects in these charset fixtures -> empty.
if [[ "$args" == *"MD5("* ]]; then
  exit 0
fi
echo "unexpected mysql invocation: $args" >&2
exit 2
`
	mysql := filepath.Join(bin, "mysql")
	if err := os.WriteFile(mysql, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SRC_HOME", srcHome)
	t.Setenv("DST_HOME", dstHome)
	t.Setenv("FAIL_CS", failCharset)
	t.Setenv("SCHEMA_DRIFT", boolEnv(schemaDrift))
	t.Setenv("CS_DIFF", boolEnv(charsetDiff))
	t.Setenv("CS_DB_DEFAULT_ONLY", boolEnv(dbDefaultOnly))
}

func boolEnv(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// runVerifyDBsForCharset drives verifyDBs (default tier) for the single planned
// DB srcacct_db -> destacct_db and returns (realDiff, screen, file).
func runVerifyDBsForCharset(t *testing.T, srcHome, dstHome string) (int, string, string) {
	t.Helper()
	pool := &sshx.Pool{
		Src:  sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)),
		Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome)),
	}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	pd := migrationData{
		Databases:             []cpanel.DatabaseEntry{{Database: "srcacct_db", Users: []string{"srcacct_u"}}},
		SrcMySQLRestrictions:  testMySQLPrefix("srcacct_"),
		DestMySQLRestrictions: testMySQLPrefix("destacct_"),
	}
	var screen, file bytes.Buffer
	rep, err := report.NewReporter(&screen, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	destCreds := map[string]destCred{"destacct_db": {user: "destacct_u", pass: "pw"}}
	realDiff, err := verifyDBs(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep,
		"srcacct", "srcpw", nil, destCreds, false)
	if err != nil {
		t.Fatalf("verifyDBs: %v", err)
	}
	return realDiff, screen.String(), file.String()
}

// TestVerifyDBsCharsetUnreadableIsUnverified is the Step 14 regression: a charset
// read failure on either side must report UNVERIFIED and fail closed (realDiff>0),
// not a silent OK and not a phantom mojibake DIFF. The schema is equal on both
// sides, so the unreadable charset is the sole cause of the non-OK verdict.
func TestVerifyDBsCharsetUnreadableIsUnverified(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	for _, tc := range []struct{ side, wantSide string }{
		{"dest", "destination"},
		{"src", "source"},
		{"both", "either side's"},
	} {
		t.Run(tc.side, func(t *testing.T) {
			srcHome, dstHome := t.TempDir(), t.TempDir()
			installFakeMySQLCharset(t, srcHome, dstHome, tc.side, false, false, false)
			realDiff, screen, file := runVerifyDBsForCharset(t, srcHome, dstHome)
			if realDiff != 1 {
				t.Fatalf("realDiff = %d, want 1 (an unreadable charset must fail closed):\n%s", realDiff, screen+file)
			}
			out := screen + file
			if !strings.Contains(out, "UNVERIFIED") {
				t.Errorf("must report UNVERIFIED for an unreadable charset:\n%s", out)
			}
			if strings.Contains(file, "[db verify OK]") {
				t.Errorf("must not emit the OK file line when encoding is unverified:\n%s", file)
			}
			// The persisted file line must carry the real UNVERIFIED tag, not collapse
			// to [db verify DIFF] (the reporting-fidelity fix).
			if !strings.Contains(file, "[db verify UNVERIFIED]") {
				t.Errorf("the persisted file line must be UNVERIFIED, not collapsed to DIFF:\n%s", file)
			}
			if strings.Contains(out, "mojibake") || strings.Contains(out, "encoding differs") {
				t.Errorf("an unreadable charset is UNVERIFIED, not a mojibake DIFF:\n%s", out)
			}
			if !strings.Contains(file, "encoding UNVERIFIED") {
				t.Errorf("file report must carry the encoding-unverified reason:\n%s", file)
			}
			if !strings.Contains(out, tc.wantSide) {
				t.Errorf("must name %q as the unreadable side:\n%s", tc.wantSide, out)
			}
		})
	}
}

// TestVerifyDBsCharsetReadableEqualStaysOK is the anti-over-fire guard: a DB with
// equal schema and a readable, equal charset on both sides stays a clean OK.
func TestVerifyDBsCharsetReadableEqualStaysOK(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	srcHome, dstHome := t.TempDir(), t.TempDir()
	installFakeMySQLCharset(t, srcHome, dstHome, "", false, false, false)
	realDiff, screen, file := runVerifyDBsForCharset(t, srcHome, dstHome)
	if realDiff != 0 {
		t.Fatalf("realDiff = %d, want 0 (equal schema + readable equal charset is a clean OK):\n%s", realDiff, screen+file)
	}
	if !strings.Contains(file, "[db verify OK]") {
		t.Errorf("a genuinely-equal DB must emit the OK file line:\n%s", file)
	}
	if strings.Contains(file, "UNVERIFIED") {
		t.Errorf("the fix must not over-fire UNVERIFIED on a readable equal charset:\n%s", file)
	}
}

// TestVerifyDBsSchemaDiffOutranksCharsetUnreadable: when a DB has BOTH a real
// schema divergence AND an unreadable charset, the concrete schema diff is the
// headline (UNVERIFIED is only the residual) and the DB is counted once.
func TestVerifyDBsSchemaDiffOutranksCharsetUnreadable(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	srcHome, dstHome := t.TempDir(), t.TempDir()
	installFakeMySQLCharset(t, srcHome, dstHome, "dest", true, false, false)
	realDiff, screen, file := runVerifyDBsForCharset(t, srcHome, dstHome)
	if realDiff != 1 {
		t.Fatalf("realDiff = %d, want 1 (counted once even with two problems):\n%s", realDiff, screen+file)
	}
	if !strings.Contains(screen+file, "schema differs") {
		t.Errorf("a real schema diff must outrank the charset-unverified residual:\n%s", screen+file)
	}
	if strings.Contains(screen, "encoding could not be read") {
		t.Errorf("schema diff should be the screen headline, not the encoding-unverified residual:\n%s", screen)
	}
}

// TestVerifyDBsCharsetDiffersIsMojibakeDiff is the positive guard for the OTHER
// branch of the rewritten detail gate: a readable BUT different charset (the
// destination re-encoded to latin1) with equal schema must still render a real
// "encoding differs (mojibake risk)" DIFF — never UNVERIFIED — and emit the
// charset detail line. This locks the surviving mojibake path the fix touched.
func TestVerifyDBsCharsetDiffersIsMojibakeDiff(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	srcHome, dstHome := t.TempDir(), t.TempDir()
	installFakeMySQLCharset(t, srcHome, dstHome, "", false, true, false)
	realDiff, screen, file := runVerifyDBsForCharset(t, srcHome, dstHome)
	if realDiff != 1 {
		t.Fatalf("realDiff = %d, want 1 (a real charset divergence is a hard DIFF):\n%s", realDiff, screen+file)
	}
	out := screen + file
	if !strings.Contains(out, "encoding differs (mojibake risk)") {
		t.Errorf("a readable charset divergence must render a mojibake DIFF:\n%s", out)
	}
	if !strings.Contains(file, "      encoding: ") {
		t.Errorf("the charset detail line must be emitted for a real divergence:\n%s", file)
	}
	if strings.Contains(out, "UNVERIFIED") {
		t.Errorf("a readable-but-different charset is a DIFF, not UNVERIFIED:\n%s", out)
	}
}

// TestVerifyDBsDBDefaultCollationOnlyIsSoftOK: when ONLY the schema DEFAULT
// collation differs while every table collation matches and schema/rows/content
// are clean, the verdict is a SOFT advisory OK (realDiff==0), not a hard mojibake
// DIFF — the default governs only future tables and touches no migrated byte. This
// is the residual cross-version case the apply-time normalization could not fix.
func TestVerifyDBsDBDefaultCollationOnlyIsSoftOK(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	srcHome, dstHome := t.TempDir(), t.TempDir()
	installFakeMySQLCharset(t, srcHome, dstHome, "", false, false, true)
	realDiff, screen, file := runVerifyDBsForCharset(t, srcHome, dstHome)
	if realDiff != 0 {
		t.Fatalf("realDiff = %d, want 0 (a db-default-only diff is a soft OK):\n%s", realDiff, screen+file)
	}
	out := screen + file
	if strings.Contains(out, "mojibake") || strings.Contains(out, "encoding differs") {
		t.Errorf("a db-default-only diff must NOT render a mojibake DIFF:\n%s", out)
	}
	if strings.Contains(out, "UNVERIFIED") {
		t.Errorf("a readable db-default-only diff is a soft OK, not UNVERIFIED:\n%s", out)
	}
	if !strings.Contains(file, "[db verify OK]") {
		t.Errorf("the default-only soft case must still emit the OK file line:\n%s", file)
	}
	if !strings.Contains(file, "encoding (soft):") || !strings.Contains(file, "utf8mb4_unicode_520_ci") {
		t.Errorf("the soft advisory detail (with the differing default) must be in the report file:\n%s", file)
	}
}

// installFakeMySQLRows is a verifyDBs harness where schema {a,b} and charset
// (utf8mb4) are IDENTICAL on both sides, so the ONLY possible divergence is the
// per-table row count: the source is fixed at a=10/b=20, the destination returns
// destRowsA/destRowsB. Same server version both sides, but the default verify never
// fetches a checksum, so equal counts stay a clean OK (no spurious UNVERIFIED).
func installFakeMySQLRows(t *testing.T, srcHome, dstHome, destRowsA, destRowsB string) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/usr/bin/env bash
set -eu
args="$*"
if [[ "$args" == *"default_character_set_name"* ]]; then
  printf 'DB\tutf8mb4\tutf8mb4_general_ci\n'
  printf 'T\ta\tutf8mb4_general_ci\nT\tb\tutf8mb4_general_ci\n'
  exit 0
fi
if [[ "$args" == *"information_schema.tables"* && "$args" == *"information_schema.views"* ]]; then
  printf 'T\t61\nT\t62\n'
  exit 0
fi
# metaCmd reads HEX(table_name): 61=a 62=b. Row-count labels are HEX too.
if [[ "$args" == *"IFNULL(auto_increment"* ]]; then
  printf 'V\t10.5.0-MariaDB\nA\t61\t1\nA\t62\t1\n'
  exit 0
fi
if [[ "$args" == *"COUNT(*)"* ]]; then
  if [[ "$HOME" == "$SRC_HOME" ]]; then
    printf '61\t10\n62\t20\n'
  else
    printf '61\t%s\n62\t%s\n' "$DEST_ROWS_A" "$DEST_ROWS_B"
  fi
  exit 0
fi
# Content checksum (same version, schema {a,b} identical): equal hashes both sides, so
# the equal-rows case stays a clean OK; the row-drift case never reaches here (a row
# mismatch is excluded from the checksum set, reported as a RowDiff instead).
if [[ "$args" == *"CHECKSUM TABLE"* ]]; then
  if [[ "$HOME" == "$SRC_HOME" ]]; then
    printf 'srcacct_db.a\t111\nsrcacct_db.b\t222\n'
  else
    printf 'destacct_db.a\t111\ndestacct_db.b\t222\n'
  fi
  exit 0
fi
# Object bodies (V12): no non-table objects in the rows fixture -> empty.
if [[ "$args" == *"MD5("* ]]; then
  exit 0
fi
echo "unexpected mysql invocation: $args" >&2
exit 2
`
	mysql := filepath.Join(bin, "mysql")
	if err := os.WriteFile(mysql, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SRC_HOME", srcHome)
	t.Setenv("DST_HOME", dstHome)
	t.Setenv("DEST_ROWS_A", destRowsA)
	t.Setenv("DEST_ROWS_B", destRowsB)
}

// TestVerifyDBsRowCountDriftFailsDefault is the V26 regression: a destination whose
// schema + charset match the source but whose data was imported EMPTY (0 rows) must
// now FAIL the DEFAULT verify (deep=false) with "row counts differ" — the old
// metadata-only default certified this silent data loss as a clean [db verify OK].
func TestVerifyDBsRowCountDriftFailsDefault(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	srcHome, dstHome := t.TempDir(), t.TempDir()
	installFakeMySQLRows(t, srcHome, dstHome, "0", "0") // destination tables imported EMPTY
	realDiff, screen, file := runVerifyDBsForCharset(t, srcHome, dstHome)
	if realDiff != 1 {
		t.Fatalf("realDiff = %d, want 1 (empty dest tables with identical schema must fail the DEFAULT verify):\n%s", realDiff, screen+file)
	}
	out := screen + file
	if !strings.Contains(out, "row counts differ") {
		t.Errorf("the default verify must name lost rows as the cause:\n%s", out)
	}
	if !strings.Contains(file, "rows: a (10->0)") || !strings.Contains(file, "rows: b (20->0)") {
		t.Errorf("the file report must carry the per-table row drift:\n%s", file)
	}
	if strings.Contains(file, "[db verify OK]") {
		t.Errorf("silent data loss must NEVER render as [db verify OK]:\n%s", file)
	}
	if strings.Contains(out, "schema differs") || strings.Contains(out, "mojibake") {
		t.Errorf("with identical schema+charset the cause must be rows, not schema/encoding:\n%s", out)
	}
}

// TestVerifyDBsEqualRowCountsStayOKDefault is the anti-over-fire guard for V26:
// identical schema, charset AND row counts must stay a clean [db verify OK] under
// the default verify — promoting row counts must not false-fail a genuine 1:1 copy.
func TestVerifyDBsEqualRowCountsStayOKDefault(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	srcHome, dstHome := t.TempDir(), t.TempDir()
	installFakeMySQLRows(t, srcHome, dstHome, "10", "20") // destination counts EQUAL the source
	realDiff, screen, file := runVerifyDBsForCharset(t, srcHome, dstHome)
	if realDiff != 0 {
		t.Fatalf("realDiff = %d, want 0 (equal schema+charset+row counts is a clean OK):\n%s", realDiff, screen+file)
	}
	if !strings.Contains(file, "[db verify OK]") {
		t.Errorf("a genuinely-equal DB must stay [db verify OK]:\n%s", file)
	}
	if strings.Contains(file, "row counts differ") || strings.Contains(file, "UNVERIFIED") {
		t.Errorf("equal row counts must not over-fire a diff/unverified:\n%s", file)
	}
}

// installFakeMySQLChecksum is a verifyDBs harness where schema {a,b}, charset and the
// per-table row counts are IDENTICAL on both sides, so the ONLY possible divergence is
// the per-table CONTENT CHECKSUM. The destination server VERSION is destVersion (so
// same-version vs cross-version can be exercised) and the destination content hash for
// table 'a' is destCkA (so equal vs different content can be exercised); table 'b' is
// always equal, isolating 'a' as the content variable.
func installFakeMySQLChecksum(t *testing.T, srcHome, dstHome, destVersion, destCkA string) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/usr/bin/env bash
set -eu
args="$*"
if [[ "$args" == *"default_character_set_name"* ]]; then
  printf 'DB\tutf8mb4\tutf8mb4_general_ci\n'
  printf 'T\ta\tutf8mb4_general_ci\nT\tb\tutf8mb4_general_ci\n'
  exit 0
fi
if [[ "$args" == *"information_schema.tables"* && "$args" == *"information_schema.views"* ]]; then
  printf 'T\t61\nT\t62\n'
  exit 0
fi
# metaCmd: version + HEX base-table names (61=a 62=b). The dest carries DEST_VERSION so a
# cross-version pair can be exercised.
if [[ "$args" == *"IFNULL(auto_increment"* ]]; then
  if [[ "$HOME" == "$SRC_HOME" ]]; then
    printf 'V\t10.5.0-MariaDB\n'
  else
    printf 'V\t%s\n' "$DEST_VERSION"
  fi
  printf 'A\t61\t1\nA\t62\t1\n'
  exit 0
fi
# Equal row counts both sides (a=10,b=20): rows are never the cause here.
if [[ "$args" == *"COUNT(*)"* ]]; then
  printf '61\t10\n62\t20\n'
  exit 0
fi
# Content checksum: <DB_NAME>.<table>\t<hash>; parseChecksums strips the prefix. Table
# 'b' equal both sides; table 'a' differs only when DEST_CK_A != 111.
if [[ "$args" == *"CHECKSUM TABLE"* ]]; then
  if [[ "$HOME" == "$SRC_HOME" ]]; then
    printf 'srcacct_db.a\t111\nsrcacct_db.b\t222\n'
  else
    printf 'destacct_db.a\t%s\ndestacct_db.b\t222\n' "$DEST_CK_A"
  fi
  exit 0
fi
# Object bodies (V12): no non-table objects in the checksum fixture -> empty.
if [[ "$args" == *"MD5("* ]]; then
  exit 0
fi
echo "unexpected mysql invocation: $args" >&2
exit 2
`
	mysql := filepath.Join(bin, "mysql")
	if err := os.WriteFile(mysql, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SRC_HOME", srcHome)
	t.Setenv("DST_HOME", dstHome)
	t.Setenv("DEST_VERSION", destVersion)
	t.Setenv("DEST_CK_A", destCkA)
}

// TestVerifyDBsContentChecksumDiffersFailsDefault closes V10: identical schema, charset,
// row counts AND server version, but table 'a' has a DIFFERENT content checksum on the
// destination. Before this change the default verify never ran CHECKSUM TABLE, so this
// same-size byte divergence passed as a clean [db verify OK]. Now the same-version
// content checksum is part of the DEFAULT verify, so it must FAIL (realDiff>0) with
// "content checksums differ".
func TestVerifyDBsContentChecksumDiffersFailsDefault(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	srcHome, dstHome := t.TempDir(), t.TempDir()
	installFakeMySQLChecksum(t, srcHome, dstHome, "10.5.0-MariaDB", "999") // same version, dest 'a' content differs
	realDiff, screen, file := runVerifyDBsForCharset(t, srcHome, dstHome)
	if realDiff != 1 {
		t.Fatalf("realDiff = %d, want 1 (same-version content checksum mismatch must fail the DEFAULT verify):\n%s", realDiff, screen+file)
	}
	out := screen + file
	if !strings.Contains(out, "content checksums differ") {
		t.Errorf("the default verify must name the content checksum as the cause:\n%s", out)
	}
	if !strings.Contains(file, "content: 1 table(s) checksum differs (same version): a") {
		t.Errorf("the file report must carry the per-table content drift for table a:\n%s", file)
	}
	if strings.Contains(file, "[db verify OK]") {
		t.Errorf("silent content corruption must NEVER render as [db verify OK]:\n%s", file)
	}
	if strings.Contains(out, "row counts differ") || strings.Contains(out, "schema differs") || strings.Contains(out, "mojibake") {
		t.Errorf("with identical schema+charset+rows the cause must be content, not schema/rows/encoding:\n%s", out)
	}
}

// TestVerifyDBsCrossVersionContentStaysOKDefault pins the "cross-version resta
// conteggio-solo" rule: when source (10.5 MariaDB) and destination (8.0 MySQL) run
// DIFFERENT server versions, CHECKSUM TABLE is not comparable across engines, so the
// DEFAULT verify must NOT fail on content — equal schema + equal row counts is a clean
// OK with a soft "content not byte-verified" note, never a failure.
func TestVerifyDBsCrossVersionContentStaysOKDefault(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	srcHome, dstHome := t.TempDir(), t.TempDir()
	// Dest is MySQL 8.0 (different from the source's 10.5 MariaDB). The dest checksum for
	// 'a' is deliberately different (999) to prove it is NEVER consulted across versions.
	installFakeMySQLChecksum(t, srcHome, dstHome, "8.0.36", "999")
	realDiff, screen, file := runVerifyDBsForCharset(t, srcHome, dstHome)
	if realDiff != 0 {
		t.Fatalf("realDiff = %d, want 0 (cross-version content is count-only; equal counts is a clean OK):\n%s", realDiff, screen+file)
	}
	if !strings.Contains(file, "[db verify OK]") {
		t.Errorf("a cross-version DB with equal counts must stay [db verify OK]:\n%s", file)
	}
	out := screen + file
	if strings.Contains(out, "content checksums differ") || strings.Contains(out, "UNVERIFIED") {
		t.Errorf("a cross-version checksum must NOT fail or UNVERIFY the default verify:\n%s", out)
	}
	if !strings.Contains(file, "content not byte-verified") {
		t.Errorf("the default file report must carry the soft cross-version content note:\n%s", file)
	}
}

// TestVerifyDBsCrossVersionContentUnverifiedUnderDeep is the strictness counterpart: the
// SAME cross-version input as the default test, but under --deep-verify (deep=true) must
// report UNVERIFIED and fail closed (realDiff>0) — proving --deep stays strict where the
// default is lenient.
func TestVerifyDBsCrossVersionContentUnverifiedUnderDeep(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	srcHome, dstHome := t.TempDir(), t.TempDir()
	installFakeMySQLChecksum(t, srcHome, dstHome, "8.0.36", "999")

	pool := &sshx.Pool{
		Src:  sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)),
		Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome)),
	}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	pd := migrationData{
		Databases:             []cpanel.DatabaseEntry{{Database: "srcacct_db", Users: []string{"srcacct_u"}}},
		SrcMySQLRestrictions:  testMySQLPrefix("srcacct_"),
		DestMySQLRestrictions: testMySQLPrefix("destacct_"),
	}
	var screen, file bytes.Buffer
	rep, err := report.NewReporter(&screen, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	destCreds := map[string]destCred{"destacct_db": {user: "destacct_u", pass: "pw"}}
	realDiff, err := verifyDBs(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep,
		"srcacct", "srcpw", nil, destCreds, true) // deep=true
	if err != nil {
		t.Fatalf("verifyDBs: %v", err)
	}
	if realDiff != 1 {
		t.Fatalf("realDiff = %d, want 1 (--deep must fail closed on cross-version content):\n%s", realDiff, screen.String()+file.String())
	}
	out := screen.String() + file.String()
	if !strings.Contains(out, "UNVERIFIED") {
		t.Errorf("--deep cross-version content must report UNVERIFIED:\n%s", out)
	}
	if !strings.Contains(file.String(), "[db verify UNVERIFIED]") {
		t.Errorf("the persisted line must be UNVERIFIED under --deep, not OK/DIFF:\n%s", file.String())
	}
	if strings.Contains(file.String(), "[db verify OK]") {
		t.Errorf("--deep must not certify cross-version content as OK:\n%s", file.String())
	}
}

// installFakeMySQLObjBody is a verifyDBs harness for the V12 object-body check: the
// schema NAME sets are identical on both sides (one base table 'a' plus one view 'v',
// trigger 'g', routine 'p', event 'e'), charset/rows/table-checksum are equal both
// sides, so the ONLY possible divergence is a non-table object's BODY hash. The view
// body hash and routine body hash on the destination are destViewHash/destProcHash; the
// dest server version is destVersion (so same-version vs cross-version can be
// exercised); trigger and event bodies stay equal both sides.
func installFakeMySQLObjBody(t *testing.T, srcHome, dstHome, destVersion, destViewHash, destProcHash string) {
	t.Helper()
	bin := t.TempDir()
	// HEX names: 61=a (table), 76=v (view), 67=g (trigger), 70=p (routine), 65=e (event).
	script := `#!/usr/bin/env bash
set -eu
args="$*"
if [[ "$args" == *"default_character_set_name"* ]]; then
  printf 'DB\tutf8mb4\tutf8mb4_general_ci\n'
  printf 'T\ta\tutf8mb4_general_ci\n'
  exit 0
fi
# Schema fingerprint (names): one base table + one of each object kind, EQUAL name sets
# both sides (so the name diff is empty and the BODY hash is the only variable).
if [[ "$args" == *"information_schema.tables"* && "$args" == *"information_schema.views"* ]]; then
  printf 'T\t61\nV\t76\nG\t67\nR\tPROCEDURE\t70\nE\t65\n'
  exit 0
fi
# Deep meta: version (dest carries DEST_VERSION) + the single base table 'a'.
if [[ "$args" == *"IFNULL(auto_increment"* ]]; then
  if [[ "$HOME" == "$SRC_HOME" ]]; then printf 'V\t10.5.0-MariaDB\n'; else printf 'V\t%s\n' "$DEST_VERSION"; fi
  printf 'A\t61\t1\n'
  exit 0
fi
if [[ "$args" == *"COUNT(*)"* ]]; then
  printf '61\t10\n'
  exit 0
fi
if [[ "$args" == *"CHECKSUM TABLE"* ]]; then
  if [[ "$HOME" == "$SRC_HOME" ]]; then printf 'srcacct_db.a\t111\n'; else printf 'destacct_db.a\t111\n'; fi
  exit 0
fi
# Object bodies (V12): view 'v' (76) + routine 'p' (70) carry parameterizable dest
# hashes; trigger 'g' (67) and event 'e' (65) are equal both sides.
if [[ "$args" == *"MD5("* ]]; then
  if [[ "$HOME" == "$SRC_HOME" ]]; then
    printf 'V\t76\tVH1\nG\t67\tGH1\nR\tPROCEDURE\t70\tPH1\nE\t65\tEH1\n'
  else
    printf 'V\t76\t%s\nG\t67\tGH1\nR\tPROCEDURE\t70\t%s\nE\t65\tEH1\n' "$DEST_VIEW_HASH" "$DEST_PROC_HASH"
  fi
  exit 0
fi
echo "unexpected mysql invocation: $args" >&2
exit 2
`
	mysql := filepath.Join(bin, "mysql")
	if err := os.WriteFile(mysql, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SRC_HOME", srcHome)
	t.Setenv("DST_HOME", dstHome)
	t.Setenv("DEST_VERSION", destVersion)
	t.Setenv("DEST_VIEW_HASH", destViewHash)
	t.Setenv("DEST_PROC_HASH", destProcHash)
}

// TestVerifyDBsViewBodyDiffersFailsDefault closes V12: identical schema NAME sets,
// charset, row counts, table checksum AND server version, but the VIEW 'v' has a
// different body fingerprint on the destination (e.g. a botched DEFINER-strip altered
// the definition). The name-only diff was blind to it, so it passed as a clean
// [db verify OK]. Now the body fingerprint is part of the verify, so it must FAIL.
func TestVerifyDBsViewBodyDiffersFailsDefault(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	srcHome, dstHome := t.TempDir(), t.TempDir()
	installFakeMySQLObjBody(t, srcHome, dstHome, "10.5.0-MariaDB", "VH2", "PH1") // same version, view body differs
	realDiff, screen, file := runVerifyDBsForCharset(t, srcHome, dstHome)
	if realDiff != 1 {
		t.Fatalf("realDiff = %d, want 1 (same-version view body mismatch must fail the DEFAULT verify):\n%s", realDiff, screen+file)
	}
	out := screen + file
	if !strings.Contains(out, "object definition differs") {
		t.Errorf("the default verify must name the object body as the cause:\n%s", out)
	}
	if !strings.Contains(file, "object definitions differ (same version): view v") {
		t.Errorf("the file report must name the changed view:\n%s", file)
	}
	if strings.Contains(file, "[db verify OK]") {
		t.Errorf("a same-name-different-body view must NEVER render as [db verify OK]:\n%s", file)
	}
	if strings.Contains(out, "missing views") || strings.Contains(out, "extra views") || strings.Contains(out, "row counts differ") {
		t.Errorf("with identical name sets+rows the cause must be the body, not a missing/extra name or rows:\n%s", out)
	}
}

// TestVerifyDBsRoutineBodyDiffersFailsDefault is the routine-body counterpart: same
// names/charset/rows/version, but PROCEDURE 'p' has a different body hash -> DIFF.
func TestVerifyDBsRoutineBodyDiffersFailsDefault(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	srcHome, dstHome := t.TempDir(), t.TempDir()
	installFakeMySQLObjBody(t, srcHome, dstHome, "10.5.0-MariaDB", "VH1", "PH2") // view equal, routine body differs
	realDiff, screen, file := runVerifyDBsForCharset(t, srcHome, dstHome)
	if realDiff != 1 {
		t.Fatalf("realDiff = %d, want 1 (same-version routine body mismatch must fail the DEFAULT verify):\n%s", realDiff, screen+file)
	}
	out := screen + file
	if !strings.Contains(out, "object definition differs") {
		t.Errorf("the default verify must name the object body as the cause:\n%s", out)
	}
	if !strings.Contains(file, "object definitions differ (same version): procedure p") {
		t.Errorf("the file report must name the changed routine with its type label:\n%s", file)
	}
	if strings.Contains(file, "[db verify OK]") {
		t.Errorf("a same-name-different-body routine must NEVER render as [db verify OK]:\n%s", file)
	}
}

// TestVerifyDBsObjectBodyCrossVersionStaysOKDefault pins the cross-version rule
// (mirroring V10): when src (10.5 MariaDB) and dest (8.0 MySQL) differ in server
// version, object definitions canonicalize differently and cannot be byte-compared, so
// the DEFAULT verify must NOT fail on a body delta — equal names is a clean OK with a
// soft "not byte-verified" note, never a failure.
func TestVerifyDBsObjectBodyCrossVersionStaysOKDefault(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	srcHome, dstHome := t.TempDir(), t.TempDir()
	installFakeMySQLObjBody(t, srcHome, dstHome, "8.0.36", "VH2", "PH2") // bodies differ but versions differ too
	realDiff, screen, file := runVerifyDBsForCharset(t, srcHome, dstHome)
	if realDiff != 0 {
		t.Fatalf("realDiff = %d, want 0 (cross-version object bodies are name-only; equal names is a clean OK):\n%s", realDiff, screen+file)
	}
	if !strings.Contains(file, "[db verify OK]") {
		t.Errorf("a cross-version DB with equal names must stay [db verify OK]:\n%s", file)
	}
	out := screen + file
	if strings.Contains(out, "object definition differs") || strings.Contains(out, "UNVERIFIED") {
		t.Errorf("a cross-version body delta must NOT fail or UNVERIFY the default verify:\n%s", out)
	}
	if !strings.Contains(file, "content not byte-verified") {
		t.Errorf("the default file report must carry the soft cross-version note:\n%s", file)
	}
}

// TestVerifyDBsObjectBodyCrossVersionUnverifiedUnderDeep is the strictness counterpart:
// the SAME cross-version input under --deep-verify must report UNVERIFIED and fail
// closed, proving --deep stays strict where the default is lenient.
func TestVerifyDBsObjectBodyCrossVersionUnverifiedUnderDeep(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	srcHome, dstHome := t.TempDir(), t.TempDir()
	installFakeMySQLObjBody(t, srcHome, dstHome, "8.0.36", "VH2", "PH2")

	pool := &sshx.Pool{
		Src:  sshtest.DialExec(t, sshtest.NewExecServer(t, srcHome)),
		Dest: sshtest.DialExec(t, sshtest.NewExecServer(t, dstHome)),
	}
	defer pool.Src.Close()
	defer pool.Dest.Close()
	pd := migrationData{
		Databases:             []cpanel.DatabaseEntry{{Database: "srcacct_db", Users: []string{"srcacct_u"}}},
		SrcMySQLRestrictions:  testMySQLPrefix("srcacct_"),
		DestMySQLRestrictions: testMySQLPrefix("destacct_"),
	}
	var screen, file bytes.Buffer
	rep, err := report.NewReporter(&screen, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	destCreds := map[string]destCred{"destacct_db": {user: "destacct_u", pass: "pw"}}
	realDiff, err := verifyDBs(context.Background(), pool, pd, logx.NewTo(io.Discard, 0), rep,
		"srcacct", "srcpw", nil, destCreds, true) // deep=true
	if err != nil {
		t.Fatalf("verifyDBs: %v", err)
	}
	if realDiff != 1 {
		t.Fatalf("realDiff = %d, want 1 (--deep must fail closed on cross-version object bodies):\n%s", realDiff, screen.String()+file.String())
	}
	if !strings.Contains(file.String(), "[db verify UNVERIFIED]") {
		t.Errorf("the persisted line must be UNVERIFIED under --deep:\n%s", file.String())
	}
}

// TestVerifyDBsEqualObjectBodiesStayOKDefault is the anti-over-fire guard for V12:
// identical names, charset, rows AND identical body hashes for view+trigger+routine+
// event must stay a clean [db verify OK] — adding the body fingerprint must not
// false-fail a genuine 1:1 copy of a DB that has objects.
func TestVerifyDBsEqualObjectBodiesStayOKDefault(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	srcHome, dstHome := t.TempDir(), t.TempDir()
	installFakeMySQLObjBody(t, srcHome, dstHome, "10.5.0-MariaDB", "VH1", "PH1") // every body hash equal, same version
	realDiff, screen, file := runVerifyDBsForCharset(t, srcHome, dstHome)
	if realDiff != 0 {
		t.Fatalf("realDiff = %d, want 0 (equal names+bodies+rows+charset is a clean OK):\n%s", realDiff, screen+file)
	}
	if !strings.Contains(file, "[db verify OK]") {
		t.Errorf("a genuinely-equal DB with objects must stay [db verify OK]:\n%s", file)
	}
	for _, bad := range []string{"definition differs", "UNVERIFIED", "not byte-verified"} {
		if strings.Contains(file, bad) {
			t.Errorf("equal object bodies must not over-fire %q:\n%s", bad, file)
		}
	}
}

// installFakeMySQLViewsOnly is a verifyDBs harness for a database with NO base table and
// a single VIEW 'v'. It isolates the object-body leg: with no checkable base table the
// table-checksum leg never runs, so the view body (same-version) or the cross-version
// object-bodies-unverified note is the SOLE driver of the verdict — the object-body code
// is exercised on its own, not masked by the table checksum.
func installFakeMySQLViewsOnly(t *testing.T, srcHome, dstHome, destVersion, destViewHash string) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/usr/bin/env bash
set -eu
args="$*"
if [[ "$args" == *"default_character_set_name"* ]]; then
  printf 'DB\tutf8mb4\tutf8mb4_general_ci\n'
  exit 0
fi
# Schema fingerprint: one view 'v' (76), no base table, equal both sides.
if [[ "$args" == *"information_schema.tables"* && "$args" == *"information_schema.views"* ]]; then
  printf 'V\t76\n'
  exit 0
fi
# Deep meta: version only (no base table -> no A line). Dest carries DEST_VERSION.
if [[ "$args" == *"IFNULL(auto_increment"* ]]; then
  if [[ "$HOME" == "$SRC_HOME" ]]; then printf 'V\t10.5.0-MariaDB\n'; else printf 'V\t%s\n' "$DEST_VERSION"; fi
  exit 0
fi
# Object bodies: the lone view 'v'; dest hash is DEST_VIEW_HASH.
if [[ "$args" == *"MD5("* ]]; then
  if [[ "$HOME" == "$SRC_HOME" ]]; then printf 'V\t76\tVH1\n'; else printf 'V\t76\t%s\n' "$DEST_VIEW_HASH"; fi
  exit 0
fi
echo "unexpected mysql invocation: $args" >&2
exit 2
`
	mysql := filepath.Join(bin, "mysql")
	if err := os.WriteFile(mysql, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SRC_HOME", srcHome)
	t.Setenv("DST_HOME", dstHome)
	t.Setenv("DEST_VERSION", destVersion)
	t.Setenv("DEST_VIEW_HASH", destViewHash)
}

// TestVerifyDBsViewsOnlyBodyDiffersFailsDefault proves the object-body leg works with NO
// base table: a views-only DB whose single view body differs (same version) must FAIL,
// driven solely by the object-body fingerprint (no table checksum involved).
func TestVerifyDBsViewsOnlyBodyDiffersFailsDefault(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	srcHome, dstHome := t.TempDir(), t.TempDir()
	installFakeMySQLViewsOnly(t, srcHome, dstHome, "10.5.0-MariaDB", "VH2")
	realDiff, screen, file := runVerifyDBsForCharset(t, srcHome, dstHome)
	if realDiff != 1 {
		t.Fatalf("realDiff = %d, want 1 (a views-only DB with a differing view body must fail):\n%s", realDiff, screen+file)
	}
	if !strings.Contains(screen+file, "object definition differs") {
		t.Errorf("the cause must be the object body, with no base table involved:\n%s", screen+file)
	}
}

// TestVerifyDBsViewsOnlyCrossVersionStaysOKDefault isolates the cross-version OBJECT
// branch: with no base table, the "object definitions not byte-compared across server
// versions" note can only come from the object-body leg. At default it is a soft note
// (OK), proving cross-version object bodies do not false-fail and the object leg (not the
// table-checksum leg) set the reason.
func TestVerifyDBsViewsOnlyCrossVersionStaysOKDefault(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	srcHome, dstHome := t.TempDir(), t.TempDir()
	installFakeMySQLViewsOnly(t, srcHome, dstHome, "8.0.36", "VH2") // bodies differ but versions differ too
	realDiff, screen, file := runVerifyDBsForCharset(t, srcHome, dstHome)
	if realDiff != 0 {
		t.Fatalf("realDiff = %d, want 0 (cross-version object bodies are not byte-comparable; clean OK):\n%s", realDiff, screen+file)
	}
	if !strings.Contains(file, "[db verify OK]") {
		t.Errorf("a cross-version views-only DB must stay [db verify OK]:\n%s", file)
	}
	if !strings.Contains(file, "object definitions not byte-compared across server versions") {
		t.Errorf("the soft note must come from the OBJECT leg (no base table to set it):\n%s", file)
	}
	if strings.Contains(screen+file, "object definition differs") {
		t.Errorf("a cross-version object body delta must NOT be a hard diff:\n%s", screen+file)
	}
}

// TestVerifyDBsNoCredentialIsUnverifiedLine proves the persisted file line for a DB
// with no captured credential (its migration did not complete) is [db verify
// UNVERIFIED], not the old collapsed [db verify DIFF]. This path short-circuits
// before any mysql call, so no fake/SSH is needed; it also pins the realDiff==0
// invariant (a no-credential UNVERIFIED is already tallied by dbFailed/FailedDomains).
func TestVerifyDBsNoCredentialIsUnverifiedLine(t *testing.T) {
	pd := migrationData{
		Databases:             []cpanel.DatabaseEntry{{Database: "srcacct_db", Users: []string{"srcacct_u"}}},
		SrcMySQLRestrictions:  testMySQLPrefix("srcacct_"),
		DestMySQLRestrictions: testMySQLPrefix("destacct_"),
	}
	var screen, file bytes.Buffer
	rep, err := report.NewReporter(&screen, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	realDiff, err := verifyDBs(context.Background(), &sshx.Pool{}, pd, logx.NewTo(io.Discard, 0), rep,
		"srcacct", "srcpw", nil, map[string]destCred{}, false)
	if err != nil {
		t.Fatalf("verifyDBs: %v", err)
	}
	if realDiff != 0 {
		t.Fatalf("realDiff = %d, want 0 (a no-credential UNVERIFIED is counted elsewhere)", realDiff)
	}
	if !strings.Contains(file.String(), "[db verify UNVERIFIED]") {
		t.Errorf("a no-credential DB must render the UNVERIFIED file tag, not DIFF:\n%s", file.String())
	}
	if strings.Contains(file.String(), "[db verify DIFF]") {
		t.Errorf("a no-credential DB must not collapse to DIFF:\n%s", file.String())
	}
}

// installFakeMySQLSchemaFail is a verifyDBs harness whose schema-fingerprint query
// (information_schema.tables + .views) exits non-zero on failSide ("src"/"dest"),
// so dbVerdict reports a count-read error → the UNREADABLE branch. The charset query
// still succeeds (equal both sides) so the charset leg is not the cause.
func installFakeMySQLSchemaFail(t *testing.T, srcHome, dstHome, failSide string) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/usr/bin/env bash
set -eu
args="$*"
if [[ "$args" == *"information_schema.tables"* && "$args" == *"information_schema.views"* ]]; then
  if [[ "$FAIL_SCHEMA" == "dest" && "$HOME" != "$SRC_HOME" ]] || \
     [[ "$FAIL_SCHEMA" == "src"  && "$HOME" == "$SRC_HOME" ]]; then
    echo "ERROR 1045 (28000): schema read denied" >&2
    exit 1
  fi
  printf 'T\t61\nT\t62\n'
  exit 0
fi
if [[ "$args" == *"default_character_set_name"* ]]; then
  printf 'DB\tutf8mb4\tutf8mb4_general_ci\n'
  printf 'T\ta\tutf8mb4_general_ci\nT\tb\tutf8mb4_general_ci\n'
  exit 0
fi
echo "unexpected mysql invocation: $args" >&2
exit 2
`
	mysql := filepath.Join(bin, "mysql")
	if err := os.WriteFile(mysql, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SRC_HOME", srcHome)
	t.Setenv("DST_HOME", dstHome)
	t.Setenv("FAIL_SCHEMA", failSide)
}

// TestVerifyDBsCountUnreadableIsUnreadableLine proves the persisted file line for a
// DB whose table count could not be read on one side is [db verify UNREADABLE], not
// the old collapsed [db verify DIFF].
func TestVerifyDBsCountUnreadableIsUnreadableLine(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	srcHome, dstHome := t.TempDir(), t.TempDir()
	installFakeMySQLSchemaFail(t, srcHome, dstHome, "dest")
	realDiff, screen, file := runVerifyDBsForCharset(t, srcHome, dstHome)
	if realDiff != 1 {
		t.Fatalf("realDiff = %d, want 1 (an unreadable count must fail closed):\n%s", realDiff, screen+file)
	}
	if !strings.Contains(file, "[db verify UNREADABLE]") {
		t.Errorf("a count-read failure must render the UNREADABLE file tag:\n%s", file)
	}
	if strings.Contains(file, "[db verify OK]") || strings.Contains(file, "[db verify DIFF]") {
		t.Errorf("UNREADABLE must not collapse to OK/DIFF:\n%s", file)
	}
	if !strings.Contains(screen, "UNREADABLE") {
		t.Errorf("screen must say UNREADABLE:\n%s", screen)
	}
}

func TestUnreadableSide(t *testing.T) {
	e := errors.New("x")
	if got := unreadableSide(e, nil); got != "the source" {
		t.Errorf("source-only = %q", got)
	}
	if got := unreadableSide(nil, e); got != "the destination" {
		t.Errorf("dest-only = %q", got)
	}
	if got := unreadableSide(e, e); got != "either side's" {
		t.Errorf("both = %q", got)
	}
}

// TestRewriteDestConfigsSurfacesUnresolved: a referencing config whose docroot
// cannot be resolved (no source docroot contains it, or its domain has no
// destination docroot) must be reported as a MANUAL action and counted as NOT
// rewritten — never silently dropped while the DB line shows a clean success.
func TestRewriteDestConfigsSurfacesUnresolved(t *testing.T) {
	var screen, file bytes.Buffer
	rep, err := report.NewReporter(&screen, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	log := logx.NewTo(io.Discard, 0)

	// site.com resolves a source docroot but has NO destination docroot (branch 2);
	// /elsewhere is under no source docroot at all (branch 1).
	pd := migrationData{
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "site.com", DocumentRoot: "/home/u/public_html/site.com"},
		},
	}
	it := dbmig.DBPlanItem{
		SrcDB: "old_db", DestDB: "new_db", DestUser: "new_user",
		Configs: []dbmig.DBConfigRef{
			{ConfigPath: "/home/u/public_html/site.com/wp-config.php", Kind: dbmig.KindWordPress},
			{ConfigPath: "/elsewhere/wp-config.php", Kind: dbmig.KindWordPress},
		},
	}
	// dest is unused on both unresolved branches (they continue before any rewrite).
	rewritten, notRewritten, _ := rewriteDestConfigs(context.Background(), nil, pd, it, "pw", false, log, rep)
	if rewritten != 0 || notRewritten != 2 {
		t.Fatalf("rewritten=%d notRewritten=%d, want 0 and 2", rewritten, notRewritten)
	}
	got := file.String()
	if !strings.Contains(got, "MANUAL") || !strings.Contains(got, "new_db") {
		t.Errorf("report must carry a MANUAL line naming the target DB:\n%s", got)
	}
	for _, want := range []string{"/home/u/public_html/site.com/wp-config.php", "/elsewhere/wp-config.php"} {
		if !strings.Contains(got, want) {
			t.Errorf("config %q must be surfaced, not dropped; report:\n%s", want, got)
		}
	}
}

func TestRewriteDestConfigsCanonicalDestinationCollisionIsManual(t *testing.T) {
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	pd := migrationData{
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "example.com", DocumentRoot: "/home/src/example.com"},
		},
		DestDocroots: []cpanel.DomainDataEntry{
			{Domain: "Example.COM", DocumentRoot: "/home/dst/a"},
			{Domain: "example.com.", DocumentRoot: "/home/dst/b"},
		},
	}
	it := dbmig.DBPlanItem{
		SrcDB: "old_db", DestDB: "new_db", DestUser: "new_user",
		Configs: []dbmig.DBConfigRef{
			{ConfigPath: "/home/src/example.com/wp-config.php", Kind: dbmig.KindWordPress},
		},
	}

	rewritten, notRewritten, _ := rewriteDestConfigs(context.Background(), nil, pd, it, "pw", false, logx.NewTo(io.Discard, 0), rep)
	if rewritten != 0 || notRewritten != 1 {
		t.Fatalf("rewritten=%d notRewritten=%d, want 0 and 1", rewritten, notRewritten)
	}
	out := file.String()
	if !strings.Contains(out, "MANUAL") || !strings.Contains(out, "canonical domain collision") {
		t.Fatalf("collision should be reported as a manual config action:\n%s", out)
	}
}

func TestRewriteDestConfigsBlockedDomainKeepsReason(t *testing.T) {
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	reason := `addon label collision: cPanel would use internal addon subdomain label "mysiteexample" for my-site.example, mysite.example`
	pd := migrationData{
		SrcDocroots: []cpanel.DomainDataEntry{{
			Domain:       "my-site.example",
			DocumentRoot: "/home/src/my-site.example",
		}},
		BlockedDomains: map[string]string{
			"my-site.example": reason,
		},
	}
	it := dbmig.DBPlanItem{
		SrcDB: "old_db", DestDB: "new_db", DestUser: "new_user",
		Configs: []dbmig.DBConfigRef{
			{ConfigPath: "/home/src/my-site.example/wp-config.php", Kind: dbmig.KindWordPress},
		},
	}

	rewritten, notRewritten, _ := rewriteDestConfigs(context.Background(), nil, pd, it, "pw", false, logx.NewTo(io.Discard, 0), rep)
	if rewritten != 0 || notRewritten != 1 {
		t.Fatalf("rewritten=%d notRewritten=%d, want 0 and 1", rewritten, notRewritten)
	}
	out := file.String()
	if !strings.Contains(out, "MANUAL") || !strings.Contains(out, "addon label collision") || !strings.Contains(out, "mysiteexample") {
		t.Fatalf("blocked-domain reason should be reported as a manual config action:\n%s", out)
	}
	if strings.Contains(out, "no destination docroot") {
		t.Fatalf("blocked-domain config must not fall through to a generic docroot reason:\n%s", out)
	}
}

func TestRewriteDestConfigsDomainTypeIssueIsManual(t *testing.T) {
	var file bytes.Buffer
	rep, err := report.NewReporter(io.Discard, &file, "src", "dst", "now")
	if err != nil {
		t.Fatal(err)
	}
	pd := migrationData{
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "Example.COM", DocumentRoot: "/home/src/example.com"},
		},
		DomainTypeIssues: map[string]DomainTypeIssue{
			"example.com": {
				Domain:           "Example.COM",
				SourceType:       model.Addon,
				ExpectedDestType: model.Addon,
				DestinationName:  "example.com.",
				DestinationType:  model.Parked,
				DestDocroot:      "/home/dst/public_html/other-site",
				DestDocrootType:  "parked_domain",
				WarnMail:         true,
				BlockWeb:         true,
				BlockDBConfig:    true,
			},
		},
	}
	it := dbmig.DBPlanItem{
		SrcDB: "old_db", DestDB: "new_db", DestUser: "new_user",
		Configs: []dbmig.DBConfigRef{
			{ConfigPath: "/home/src/example.com/wp-config.php", Kind: dbmig.KindWordPress},
		},
	}

	rewritten, notRewritten, _ := rewriteDestConfigs(context.Background(), nil, pd, it, "pw", false, logx.NewTo(io.Discard, 0), rep)
	if rewritten != 0 || notRewritten != 1 {
		t.Fatalf("rewritten=%d notRewritten=%d, want 0 and 1", rewritten, notRewritten)
	}
	out := file.String()
	if !strings.Contains(out, "MANUAL") || !strings.Contains(out, "destination domain type mismatch") {
		t.Fatalf("type mismatch should be reported as a manual config action:\n%s", out)
	}
}
