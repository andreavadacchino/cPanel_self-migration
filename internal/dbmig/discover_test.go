package dbmig

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
)

var bg = context.Background()

// fnRunner is a function-backed Runner: each test supplies the canned response
// logic, so the read-only discovery scripts (find/cat/grep/registry) and the
// count queries run without SSH or a real cPanel.
type fnRunner func(script string, env map[string]string) ([]byte, error)

func (f fnRunner) RunScript(_ context.Context, script string, env map[string]string) ([]byte, error) {
	return f(script, env)
}

// wpConfig builds a minimal but real wp-config.php the WordPress parser accepts.
func wpConfig(name, user, pass string) string {
	return "<?php\n" +
		"define('DB_NAME', '" + name + "');\n" +
		"define('DB_USER', '" + user + "');\n" +
		"define('DB_PASSWORD', '" + pass + "');\n" +
		"$table_prefix = 'wp_';\n"
}

// vfsRunner simulates the SOURCE filesystem for the discovery scripts: find (list
// files under a docroot), cat (read a file), grep (first file mentioning a db
// name), and the Softaculous registry read.
func vfsRunner(files map[string]string, registry string) Runner {
	return fnRunner(func(script string, env map[string]string) ([]byte, error) {
		switch {
		case strings.Contains(script, "softaculous"):
			return []byte(registry), nil
		case strings.Contains(script, "find ") && strings.Contains(script, "maxdepth"):
			dr := env["DOCROOT"]
			var out []string
			for p := range files {
				if p == dr || strings.HasPrefix(p, dr+"/") {
					out = append(out, p)
				}
			}
			sort.Strings(out)
			return []byte(strings.Join(out, "\n")), nil
		case strings.HasPrefix(strings.TrimSpace(script), "cat "):
			return []byte(files[env["FILE"]]), nil
		case strings.Contains(script, "grep -rlF"):
			// The real script greps for files mentioning the db name and emits a
			// bounded, LC_ALL=C-sorted list (sort | head -n N), not just the first
			// hit — so the caller can parse candidates in order until one yields
			// creds. Mirror that here: return ALL matches, sorted.
			root, name := env["ROOT"], env["DBNAME"]
			var m []string
			for p, c := range files {
				if (p == root || strings.HasPrefix(p, root+"/")) && strings.Contains(c, name) {
					m = append(m, p)
				}
			}
			sort.Strings(m)
			if len(m) > 0 {
				return []byte(strings.Join(m, "\n") + "\n"), nil
			}
			return nil, nil
		}
		return nil, nil
	})
}

func TestDiscoverSiteCreds(t *testing.T) {
	files := map[string]string{
		"/home/u/site/wp-config.php": wpConfig("srcacct_shop", "srcacct_shopu", "secret"),
		"/home/u/site/readme.txt":    "not a config", // listed by find, skipped by the parser
	}
	got, err := DiscoverSiteCreds(bg, vfsRunner(files, ""), []string{"/home/u/site", ""})
	if err != nil {
		t.Fatalf("DiscoverSiteCreds: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d creds, want 1: %+v", len(got), got)
	}
	if got[0].DBName != "srcacct_shop" || got[0].DBUser != "srcacct_shopu" || got[0].DBPassword != "secret" {
		t.Errorf("creds = %+v", got[0])
	}
	if got[0].ConfigPath != "/home/u/site/wp-config.php" {
		t.Errorf("ConfigPath = %q", got[0].ConfigPath)
	}
}

func TestSearchCredsByDBName(t *testing.T) {
	files := map[string]string{
		"/home/u/app/wp-config.php": wpConfig("srcacct_app", "srcacct_appu", "pw"),
	}
	got, err := SearchCredsByDBName(bg, vfsRunner(files, ""), []string{"/home/u/app", ""},
		[]string{"srcacct_app", "srcacct_missing"})
	if err != nil {
		t.Fatalf("SearchCredsByDBName: %v", err)
	}
	if len(got) != 1 || got[0].DBName != "srcacct_app" {
		t.Fatalf("got %+v, want only srcacct_app", got)
	}
	if got[0].Docroot != "/home/u/app" {
		t.Errorf("Docroot = %q, want the matched docroot", got[0].Docroot)
	}
	// No db names -> nil, no scan.
	if r, _ := SearchCredsByDBName(bg, vfsRunner(files, ""), []string{"/home/u/app"}, nil); r != nil {
		t.Errorf("empty dbNames -> %v, want nil", r)
	}
}

func TestDiscoverAllCreds(t *testing.T) {
	files := map[string]string{
		"/home/u/site/wp-config.php": wpConfig("srcacct_site", "siteuser", "sitepass"),
	}
	// softaculousSample (from softaculous_test.go) carries srcacct_wp694 + wp590.
	got, err := DiscoverAllCreds(bg, vfsRunner(files, softaculousSample),
		[]string{"/home/u/site"},
		[]string{"srcacct_wp694", "srcacct_site", "srcacct_orphan"})
	if err != nil {
		t.Fatalf("DiscoverAllCreds: %v", err)
	}
	for _, db := range []string{"srcacct_wp694", "srcacct_wp590", "srcacct_site"} {
		if _, ok := credForDB(got, db); !ok {
			t.Errorf("DiscoverAllCreds missing %s", db)
		}
	}
	// Registry entries are credentials-only (not rewrite targets); on-disk ones are.
	if reg, _ := credForDB(got, "srcacct_wp694"); !reg.FromRegistry {
		t.Error("registry cred should be tagged FromRegistry")
	}
	if site, _ := credForDB(got, "srcacct_site"); site.FromRegistry {
		t.Error("on-disk site cred must NOT be FromRegistry")
	}
}

func TestDiscoverSoftaculous(t *testing.T) {
	got, err := DiscoverSoftaculous(bg, vfsRunner(nil, softaculousSample))
	if err != nil || len(got) != 2 {
		t.Fatalf("DiscoverSoftaculous = %d creds, %v; want 2", len(got), err)
	}
	// Absent registry -> nil, no error (callers fall back to config parsing).
	got, err = DiscoverSoftaculous(bg, vfsRunner(nil, ""))
	if err != nil || got != nil {
		t.Errorf("absent registry -> %v, %v; want nil,nil", got, err)
	}
}

// Discovery must surface a read/find/grep failure (and the registry failure) as
// an error rather than silently returning empty.
func TestDiscoverErrorPaths(t *testing.T) {
	errOn := func(needle string) Runner {
		return fnRunner(func(script string, _ map[string]string) ([]byte, error) {
			if strings.Contains(script, needle) {
				return nil, context.Canceled
			}
			return nil, nil
		})
	}
	if _, err := DiscoverSiteCreds(bg, errOn("find "), []string{"/d"}); err == nil {
		t.Error("DiscoverSiteCreds must surface a find error")
	}
	if _, err := SearchCredsByDBName(bg, errOn("grep"), []string{"/d"}, []string{"db"}); err == nil {
		t.Error("SearchCredsByDBName must surface a grep error")
	}
	if _, err := DiscoverSoftaculous(bg, errOn("softaculous")); err == nil {
		t.Error("DiscoverSoftaculous must surface a read error")
	}
	if _, err := DiscoverAllCreds(bg, errOn("softaculous"), []string{"/d"}, []string{"db"}); err == nil {
		t.Error("DiscoverAllCreds must surface the softaculous error")
	}
}

// An unreadable config inside DiscoverSiteCreds is skipped (logged), not fatal.
func TestDiscoverSiteCredsSkipsUnreadable(t *testing.T) {
	r := fnRunner(func(script string, _ map[string]string) ([]byte, error) {
		switch {
		case strings.Contains(script, "find "):
			return []byte("/d/wp-config.php\n"), nil
		case strings.HasPrefix(strings.TrimSpace(script), "cat "):
			return nil, context.Canceled // unreadable
		}
		return nil, nil
	})
	got, err := DiscoverSiteCreds(bg, r, []string{"/d"})
	if err != nil {
		t.Fatalf("an unreadable config must be skipped, not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("unreadable config -> %d creds, want 0", len(got))
	}
}

// --- transfer.go: count queries via a canned Runner ---

func TestCountTables(t *testing.T) {
	r := fnRunner(func(string, map[string]string) ([]byte, error) { return []byte("7\n"), nil })
	n, err := CountTables(bg, r, "db", "u", "p")
	if err != nil || n != 7 {
		t.Fatalf("CountTables = %d, %v; want 7", n, err)
	}
	// Unparseable output -> error.
	rbad := fnRunner(func(string, map[string]string) ([]byte, error) { return []byte("not-a-number"), nil })
	if _, err := CountTables(bg, rbad, "db", "u", "p"); err == nil {
		t.Error("CountTables must error on unparseable output")
	}
	// RunScript error -> error.
	rerr := fnRunner(func(string, map[string]string) ([]byte, error) { return nil, context.Canceled })
	if _, err := CountTables(bg, rerr, "db", "u", "p"); err == nil {
		t.Error("CountTables must propagate the RunScript error")
	}
}

func TestCountObjects(t *testing.T) {
	r := fnRunner(func(string, map[string]string) ([]byte, error) { return []byte("2\t1\t0\t3\n"), nil })
	oc, err := CountObjects(bg, r, "db", "u", "p")
	if err != nil {
		t.Fatalf("CountObjects: %v", err)
	}
	if oc.Routines != 2 || oc.Events != 1 || oc.Triggers != 0 || oc.Views != 3 {
		t.Errorf("CountObjects = %+v", oc)
	}
	if oc.Total() != 6 {
		t.Errorf("Total = %d, want 6", oc.Total())
	}
	// Too few fields -> error.
	rbad := fnRunner(func(string, map[string]string) ([]byte, error) { return []byte("1 2"), nil })
	if _, err := CountObjects(bg, rbad, "db", "u", "p"); err == nil {
		t.Error("CountObjects must error on malformed output")
	}
}

// --- pure helpers ---

func TestObjectCountsTotal(t *testing.T) {
	if got := (ObjectCounts{Routines: 1, Events: 2, Triggers: 3, Views: 4}).Total(); got != 10 {
		t.Errorf("Total = %d, want 10", got)
	}
	if (ObjectCounts{}).Total() != 0 {
		t.Error("empty Total must be 0")
	}
}

func TestGeneratePassword(t *testing.T) {
	p, err := GeneratePassword(24)
	if err != nil || len(p) != 24 {
		t.Fatalf("GeneratePassword(24) = %q (len %d), %v", p, len(p), err)
	}
	for _, r := range p {
		if !strings.ContainsRune(passwordAlphabet, r) {
			t.Errorf("generated char %q is not in the cPanel-safe alphabet", r)
		}
	}
	// n < 1 falls back to the default length.
	if p0, _ := GeneratePassword(0); len(p0) != 24 {
		t.Errorf("GeneratePassword(0) len = %d, want default 24", len(p0))
	}
	// Two passwords must differ (cryptographically random, not constant).
	if p2, _ := GeneratePassword(24); p == p2 {
		t.Error("two generated passwords should not be identical")
	}
}

func TestFirstLine(t *testing.T) {
	if firstLine("a\nb\nc") != "a" {
		t.Error("firstLine should return the first line")
	}
	if firstLine("noNL") != "noNL" {
		t.Error("firstLine with no newline returns the whole string")
	}
}

func TestDocrootOf(t *testing.T) {
	docroots := []string{"/home/u/public_html", "/home/u/public_html/sub", ""}
	// Longest (most specific) containing docroot wins.
	if d := docrootOf(docroots, "/home/u/public_html/sub/wp-config.php"); d != "/home/u/public_html/sub" {
		t.Errorf("docrootOf = %q, want the nested docroot", d)
	}
	if d := docrootOf(docroots, "/home/u/public_html/site/x"); d != "/home/u/public_html" {
		t.Errorf("docrootOf = %q, want the outer docroot", d)
	}
	if d := docrootOf(docroots, "/etc/passwd"); d != "" {
		t.Errorf("docrootOf(unrelated) = %q, want empty", d)
	}
}

func TestMapConfigPathEdges(t *testing.T) {
	// Empty docroot args -> unchanged (defensive).
	if got := MapConfigPath("/a/wp-config.php", "", "/dest"); got != "/a/wp-config.php" {
		t.Errorf("empty src docroot -> %q, want unchanged", got)
	}
	// Path == srcDocroot -> destDocroot.
	if got := MapConfigPath("/home/u/site", "/home/u/site", "/dest/site"); got != "/dest/site" {
		t.Errorf("path==docroot -> %q, want /dest/site", got)
	}
	// Normal remap under the docroot.
	if got := MapConfigPath("/home/u/site/wp-config.php", "/home/u/site", "/dest/site"); got != "/dest/site/wp-config.php" {
		t.Errorf("remap -> %q", got)
	}
	// Sibling that only shares a name prefix -> NOT remapped (boundary-aware).
	if got := MapConfigPath("/home/u/site2/wp-config.php", "/home/u/site", "/dest/site"); got != "/home/u/site2/wp-config.php" {
		t.Errorf("sibling prefix must not be remapped, got %q", got)
	}
	// Trailing slashes on either docroot must not defeat the remap (inventory entries
	// can end in '/'). Both must map to the same clean destination config path.
	for _, tc := range []struct{ src, dr, ddr string }{
		{"/home/u/site/wp-config.php", "/home/u/site/", "/dest/site"},
		{"/home/u/site/wp-config.php", "/home/u/site", "/dest/site/"},
		{"/home/u/site/wp-config.php", "/home/u/site/", "/dest/site/"},
	} {
		if got := MapConfigPath(tc.src, tc.dr, tc.ddr); got != "/dest/site/wp-config.php" {
			t.Errorf("trailing-slash remap (%q,%q) -> %q, want /dest/site/wp-config.php", tc.dr, tc.ddr, got)
		}
	}
}

func TestWPCredsMatchEachFieldMismatch(t *testing.T) {
	full := wpConfig("n", "u", "p")
	if !wpCredsMatch(full, "n", "u", "p") {
		t.Error("matching creds should report true")
	}
	if wpCredsMatch(full, "DIFFERENT", "u", "p") {
		t.Error("DB_NAME mismatch must be false")
	}
	if wpCredsMatch(full, "n", "DIFFERENT", "p") {
		t.Error("DB_USER mismatch must be false")
	}
	if wpCredsMatch(full, "n", "u", "DIFFERENT") {
		t.Error("DB_PASSWORD mismatch must be false")
	}
}

func TestDeriveSrcPrefix(t *testing.T) {
	dbs := []cpanel.DatabaseEntry{{Database: "noprefix"}, {Database: "srcacct_shop"}}
	if p := deriveSrcPrefix(dbs); p != "srcacct_" {
		t.Errorf("deriveSrcPrefix = %q, want srcacct_", p)
	}
	if p := deriveSrcPrefix([]cpanel.DatabaseEntry{{Database: "noprefix"}}); p != "" {
		t.Errorf("deriveSrcPrefix(no prefixes) = %q, want empty", p)
	}
}

// TestSearchCredsByDBNameSkipsNonMatchingConfig covers the fail-closed branch: a
// file that MENTIONS the db name (so grep finds it) but does NOT parse to a config
// whose creds match is skipped, not returned as a bogus entry.
// TestSearchCredsByDBNameTriesMultipleCandidates: the last-resort search must not
// stop at the FIRST file that merely mentions the db name. Here a renamed SQL dump
// (.inc) cites the database in a comment and sorts BEFORE the real wp-config.php,
// but carries no parseable credentials. The search has to keep going and recover
// the creds from the config listed after it — otherwise the DB is wrongly treated
// as an orphan and never rewritten.
func TestSearchCredsByDBNameTriesMultipleCandidates(t *testing.T) {
	files := map[string]string{
		// Sorts first (b < w); mentions the db name but is not a config.
		"/home/u/app/backup.sql.inc": "-- MySQL dump\n-- Host: localhost  Database: srcacct_app\n" +
			"INSERT INTO `wp_options` VALUES (1,'siteurl','http://x');\n",
		"/home/u/app/wp-config.php": wpConfig("srcacct_app", "srcacct_appu", "pw"),
	}
	got, err := SearchCredsByDBName(bg, vfsRunner(files, ""), []string{"/home/u/app"}, []string{"srcacct_app"})
	if err != nil {
		t.Fatalf("SearchCredsByDBName: %v", err)
	}
	if len(got) != 1 || got[0].DBName != "srcacct_app" || got[0].DBUser != "srcacct_appu" {
		t.Fatalf("must recover creds from wp-config.php despite the earlier-sorted backup, got %+v", got)
	}
	if got[0].ConfigPath != "/home/u/app/wp-config.php" {
		t.Errorf("ConfigPath = %q, want the real config (not the backup .inc)", got[0].ConfigPath)
	}
}

func TestSearchCredsByDBNameSkipsNonMatchingConfig(t *testing.T) {
	files := map[string]string{
		"/home/u/app/notes.txt": "the database is srcacct_app but this file is not a config",
	}
	got, err := SearchCredsByDBName(bg, vfsRunner(files, ""), []string{"/home/u/app"}, []string{"srcacct_app"})
	if err != nil {
		t.Fatalf("SearchCredsByDBName: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a non-config mention must be skipped, got %+v", got)
	}
}
