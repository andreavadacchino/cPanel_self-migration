package dbmig

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestDumpCmdIsReadOnlyAndCpanelSafe(t *testing.T) {
	// Both builder variants must not lock (read-only invariant), must carry the flags
	// a non-root cPanel user needs, and must include routines + events (off by default
	// — their omission silently drops stored procedures/functions/events).
	for _, dumpCmd := range []string{BuildDumpCmd(false), BuildDumpCmd(true)} {
		for _, want := range []string{"--single-transaction", "--no-tablespaces", "--quick", "--routines", "--events", "mysqldump"} {
			if !strings.Contains(dumpCmd, want) {
				t.Errorf("dump cmd missing %q: %s", want, dumpCmd)
			}
		}
		// Credentials come from env, never argv: no -p<pass>/--password, password is via
		// MYSQL_PWD. (Match the real password-flag forms, not a bare "-p" substring —
		// that would false-trip on --set-gtid-purged.)
		for _, bad := range []string{"--password", `-p"`, "-p'", "-p$"} {
			if strings.Contains(dumpCmd, bad) {
				t.Errorf("dump cmd must not put a password in argv (%q): %s", bad, dumpCmd)
			}
		}
		if !strings.Contains(dumpCmd, `"$DB_NAME"`) || !strings.Contains(dumpCmd, `"$DB_USER"`) {
			t.Errorf("dump cmd must take db/user from env vars: %s", dumpCmd)
		}
	}
	// The GTID flag is version-gated: present ONLY when the source mysqldump supports
	// it (MySQL), omitted otherwise (MariaDB rejects the unknown option).
	if strings.Contains(BuildDumpCmd(false), "--set-gtid-purged") {
		t.Errorf("MariaDB-safe dump must NOT carry --set-gtid-purged: %s", BuildDumpCmd(false))
	}
	if !strings.Contains(BuildDumpCmd(true), "--set-gtid-purged=OFF") {
		t.Errorf("MySQL dump must carry --set-gtid-purged=OFF: %s", BuildDumpCmd(true))
	}
}

func TestImportCmdUsesEnv(t *testing.T) {
	if !strings.Contains(importCmd, "mysql ") {
		t.Errorf("importCmd should run mysql: %s", importCmd)
	}
	if strings.Contains(importCmd, "-p") {
		t.Errorf("importCmd must not put a password in argv: %s", importCmd)
	}
	if !strings.Contains(importCmd, `"$DB_NAME"`) {
		t.Errorf("importCmd must take db from env: %s", importCmd)
	}
	// It must strip DEFINER clauses (so a non-SUPER dest user can create
	// routines/events/triggers) and the strip MUST be anchored to comment lines
	// (^/*!) so INSERT data is never touched.
	if !strings.Contains(importCmd, "DEFINER=") {
		t.Errorf("importCmd must strip DEFINER clauses: %s", importCmd)
	}
	if !strings.Contains(importCmd, `/^\/\*!/`) {
		t.Errorf("DEFINER strip must be anchored to comment lines (^/*!), not applied to data: %s", importCmd)
	}
	// mysql must be the LAST pipeline stage and the pipeline must run under
	// `set -o pipefail` so a non-final stage (sed) dying is NOT masked by mysql's
	// clean-EOF exit 0. The pipeline body ends in `"$DB_NAME"` (mysql last); the
	// whole thing is wrapped in `bash -c '...'`, so importCmd itself ends in `'`.
	if !strings.Contains(importCmd, "set -o pipefail") {
		t.Errorf("import pipeline must run under `set -o pipefail`: %s", importCmd)
	}
	if !strings.HasPrefix(importCmd, "bash -c ") {
		t.Errorf("import pipeline must be wrapped in `bash -c` to force pipefail-capable bash: %s", importCmd)
	}
	if !strings.HasSuffix(strings.TrimSpace(importPipeline), `"$DB_NAME"`) {
		t.Errorf("mysql must be the final pipeline stage: %s", importPipeline)
	}
}

func TestDumpEnvCarriesPasswordOutOfBand(t *testing.T) {
	env := dumpEnv("destacct_wp694", "destacct", "secret")
	if env["DB_NAME"] != "destacct_wp694" || env["DB_USER"] != "destacct" {
		t.Errorf("dumpEnv names wrong: %+v", env)
	}
	if env["MYSQL_PWD"] != "secret" {
		t.Errorf("password must travel as MYSQL_PWD, got %+v", env)
	}
}

func TestParseCount(t *testing.T) {
	cases := map[string]struct {
		n  int
		ok bool
	}{
		"88\n":    {88, true},
		"  0  ":   {0, true},
		"":        {0, false},
		"abc":     {0, false},
		"warn\n5": {5, true}, // tolerate a leading warning line, take last token
	}
	for in, want := range cases {
		n, ok := parseCount(in)
		if n != want.n || ok != want.ok {
			t.Errorf("parseCount(%q) = (%d,%v), want (%d,%v)", in, n, ok, want.n, want.ok)
		}
	}
}

func TestCharsetsCmdScopesToDatabase(t *testing.T) {
	// Both SELECTs must scope to DATABASE() (no name spliced into SQL) and read the
	// schema default plus per-table collation — the inputs the mojibake check needs.
	for _, want := range []string{
		"information_schema.schemata",
		"default_character_set_name",
		"table_collation",
		"DATABASE()",
		`"$DB_NAME"`,
	} {
		if !strings.Contains(charsetsCmd, want) {
			t.Errorf("charsetsCmd missing %q:\n%s", want, charsetsCmd)
		}
	}
}

func TestParseCharsets(t *testing.T) {
	out := "DB\tutf8mb4\tutf8mb4_general_ci\n" +
		"T\twp_posts\tutf8mb4_general_ci\n" +
		"T\twp_users\tutf8mb4_unicode_ci\n"
	ci, ok := parseCharsets(out)
	if !ok {
		t.Fatalf("parseCharsets should succeed: %+v", ci)
	}
	if ci.DBCharset != "utf8mb4" || ci.DBCollation != "utf8mb4_general_ci" {
		t.Errorf("db charset/collation = %q/%q", ci.DBCharset, ci.DBCollation)
	}
	if ci.Tables["wp_posts"] != "utf8mb4_general_ci" || ci.Tables["wp_users"] != "utf8mb4_unicode_ci" {
		t.Errorf("table collations wrong: %+v", ci.Tables)
	}
	// No DB line -> unreadable (not a clean empty charset).
	if _, ok := parseCharsets("garbage\n"); ok {
		t.Error("missing DB line must be !ok")
	}
	if _, ok := parseCharsets(""); ok {
		t.Error("empty output must be !ok")
	}
	// A leading warning line is tolerated.
	if ci, ok := parseCharsets("Warning: using a password\nDB\tlatin1\tlatin1_swedish_ci\n"); !ok || ci.DBCharset != "latin1" {
		t.Errorf("leading warning not tolerated: ok=%v ci=%+v", ok, ci)
	}
}

func TestSchemaFingerprintCmdIsScopedAndHexEncoded(t *testing.T) {
	for _, want := range []string{
		"information_schema.tables",
		"information_schema.views",
		"information_schema.triggers",
		"information_schema.routines",
		"information_schema.events",
		"HEX(table_name)",
		"HEX(trigger_name)",
		"HEX(routine_name)",
		"HEX(event_name)",
		"DATABASE()",
		`"$DB_NAME"`,
	} {
		if !strings.Contains(schemaFingerprintCmd, want) {
			t.Errorf("schemaFingerprintCmd missing %q:\n%s", want, schemaFingerprintCmd)
		}
	}
}

func TestParseSchemaFingerprint(t *testing.T) {
	out := strings.Join([]string{
		"T\t" + hexName("wp_posts"),
		"T\t" + hexName("has\ttab"),
		"V\t" + hexName("active_users"),
		"G\t" + hexName("trig`one"),
		"R\tPROCEDURE\t" + hexName("cleanup old"),
		"R\tFUNCTION\t" + hexName("calc.score"),
		"E\t" + hexName("nightly"),
		"Warning: ignored line",
		"bad\t" + hexName("ignored"),
	}, "\n")
	fp := parseSchemaFingerprint(out)
	for _, name := range []string{"wp_posts", "has\ttab"} {
		if _, ok := fp.Tables[name]; !ok {
			t.Errorf("table %q missing from %+v", name, fp.Tables)
		}
	}
	if _, ok := fp.Views["active_users"]; !ok {
		t.Errorf("view missing: %+v", fp.Views)
	}
	if _, ok := fp.Triggers["trig`one"]; !ok {
		t.Errorf("trigger missing: %+v", fp.Triggers)
	}
	if _, ok := fp.Routines["PROCEDURE cleanup old"]; !ok {
		t.Errorf("procedure missing: %+v", fp.Routines)
	}
	if _, ok := fp.Routines["FUNCTION calc.score"]; !ok {
		t.Errorf("function missing: %+v", fp.Routines)
	}
	if _, ok := fp.Events["nightly"]; !ok {
		t.Errorf("event missing: %+v", fp.Events)
	}
	if got := fp.ObjectCounts(); got.Routines != 2 || got.Events != 1 || got.Triggers != 1 || got.Views != 1 {
		t.Errorf("ObjectCounts = %+v", got)
	}
}

func TestDropSchemaSQL(t *testing.T) {
	fp := newSchemaFingerprint()
	fp.Tables["wp_posts"] = struct{}{}
	fp.Tables["odd`table"] = struct{}{}
	fp.Views["v"] = struct{}{}
	fp.Triggers["trg"] = struct{}{}
	fp.Events["ev"] = struct{}{}
	fp.Routines["PROCEDURE cleanup"] = struct{}{}
	fp.Routines["FUNCTION calc"] = struct{}{}

	sql := dropSchemaSQL(fp)
	for _, want := range []string{
		"SET FOREIGN_KEY_CHECKS=0;",
		"DROP VIEW IF EXISTS `v`;",
		"DROP TRIGGER IF EXISTS `trg`;",
		"DROP EVENT IF EXISTS `ev`;",
		"DROP PROCEDURE IF EXISTS `cleanup`;",
		"DROP FUNCTION IF EXISTS `calc`;",
		"DROP TABLE IF EXISTS `odd``table`;",
		"DROP TABLE IF EXISTS `wp_posts`;",
		"SET FOREIGN_KEY_CHECKS=1;",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("dropSchemaSQL missing %q:\n%s", want, sql)
		}
	}
	if empty := dropSchemaSQL(newSchemaFingerprint()); empty != "" {
		t.Errorf("empty schema should need no SQL, got %q", empty)
	}
}

func hexName(s string) string {
	return strings.ToUpper(hex.EncodeToString([]byte(s)))
}

func TestRewriteWPConfigScriptHasHomeGuardAndAtomicWrite(t *testing.T) {
	s := writeConfigScript()
	// Canonical (not lexical) HOME containment: both HOME and the config are resolved
	// before the prefix check, so a ".." path or a symlinked config that escapes HOME
	// is refused, and a /home<->/home2-symlinked HOME still matches.
	for _, want := range []string{"canon_existing_path", `"$home_real"/?*`, `*/../*`, "exit 21"} {
		if !strings.Contains(s, want) {
			t.Errorf("rewrite script must canonically guard the target under HOME (missing %q):\n%s", want, s)
		}
	}
	// The lexical-only guard must be gone (it accepted ../ escapes that resolve out of HOME).
	if strings.Contains(s, `"$HOME"/?*`) {
		t.Errorf("rewrite script must not use the lexical HOME glob (escapable):\n%s", s)
	}
	// Unpredictable temp via mktemp in the config's dir — NOT a fixed, symlink-plantable name.
	if !strings.Contains(s, "mktemp") || strings.Contains(s, ".dbmig.tmp") || !strings.Contains(s, "mv -f") {
		t.Errorf("rewrite script must mktemp a temp file (no fixed .dbmig.tmp) then mv it atomically:\n%s", s)
	}
	// Fail closed: the write and the mv must each abort (non-zero) instead of clobbering.
	if !strings.Contains(s, "exit 23") || !strings.Contains(s, "exit 24") {
		t.Errorf("rewrite script must fail closed on a failed write/mv:\n%s", s)
	}
	// Content via env, printf %s (not echo), never interpolated.
	if !strings.Contains(s, `printf '%s' "$NEWCONTENT"`) {
		t.Errorf("rewrite script must write $NEWCONTENT via printf %%s: %s", s)
	}
}

func TestMapConfigPath(t *testing.T) {
	// SRC main docroot == public_html, DEST under public_html/<dom>.
	got := MapConfigPath(
		"/home/srcacct/public_html/wp-config.php",
		"/home/srcacct/public_html",
		"/home/destacct/public_html/main.example")
	want := "/home/destacct/public_html/main.example/wp-config.php"
	if got != want {
		t.Errorf("MapConfigPath = %q, want %q", got, want)
	}
	// Nested install (shared DB second config).
	got = MapConfigPath(
		"/home/srcacct/site2.example/test/wp-config.php",
		"/home/srcacct/site2.example",
		"/home/destacct/public_html/site2.example")
	want = "/home/destacct/public_html/site2.example/test/wp-config.php"
	if got != want {
		t.Errorf("nested MapConfigPath = %q, want %q", got, want)
	}
	// Defensive: path not under srcDocroot returned unchanged.
	got = MapConfigPath("/elsewhere/wp-config.php", "/home/srcacct/x", "/home/destacct/y")
	if got != "/elsewhere/wp-config.php" {
		t.Errorf("non-matching path should be unchanged, got %q", got)
	}
	// Boundary-aware: a SIBLING docroot that merely shares a name prefix must NOT
	// match. "/home/u/site2/..." is not under "/home/u/site" (a plain HasPrefix
	// would wrongly map it to "/dest/site2/wp-config.php").
	got = MapConfigPath("/home/u/site2/wp-config.php", "/home/u/site", "/dest/site")
	if got != "/home/u/site2/wp-config.php" {
		t.Errorf("sibling docroot must not be mapped, got %q", got)
	}
	// The docroot itself maps to the destination docroot.
	got = MapConfigPath("/home/u/site", "/home/u/site", "/dest/site")
	if got != "/dest/site" {
		t.Errorf("exact docroot should map to dest docroot, got %q", got)
	}
}

func TestFindConfigsScriptIsReadOnlyBounded(t *testing.T) {
	s := findConfigsScript()
	if !strings.Contains(s, "-maxdepth 3") {
		t.Errorf("find should be depth-bounded: %s", s)
	}
	if !strings.Contains(s, "wp-config.php") || !strings.Contains(s, `"$DOCROOT"`) {
		t.Errorf("find should look for wp-config under $DOCROOT: %s", s)
	}
	// No mutation verbs (the only redirection is 2>/dev/null for stderr, which is
	// harmless; a real write would be `> file` or `-delete`/rm/mv).
	for _, bad := range []string{"-delete", "rm ", "mv ", "> "} {
		if strings.Contains(s, bad) {
			t.Errorf("find script must be read-only, found %q: %s", bad, s)
		}
	}
}

func TestCountTablesCmdIsBaseTablesOnly(t *testing.T) {
	if !strings.Contains(countTablesCmd, "BASE TABLE") {
		t.Errorf("count should restrict to base tables (exclude views): %s", countTablesCmd)
	}
	if !strings.Contains(countTablesCmd, "DATABASE()") {
		t.Errorf("count should scope to the selected DB: %s", countTablesCmd)
	}
}

// TestCountTablesCmdHasNoSQLInjection guards against reintroducing the pattern
// that was removed with existsCmd: the database name must NOT be interpolated
// into the SQL string. It is scoped via the SQL function DATABASE() and selected
// by passing $DB_NAME as a positional argument to mysql (which runs USE), so a
// crafted name can never alter the query.
func TestCountTablesCmdHasNoSQLInjection(t *testing.T) {
	// The name reaches the DB only as an argument, expanded by the shell as
	// "$DB_NAME" at the end of the command — never spliced inside the -e SQL.
	if !strings.Contains(countTablesCmd, `DATABASE()`) {
		t.Errorf("DB must be selected via DATABASE(), not by name in the SQL: %s", countTablesCmd)
	}
	if strings.Contains(countTablesCmd, "schema_name = '$DB_NAME'") ||
		strings.Contains(countTablesCmd, "table_schema = '$DB_NAME'") {
		t.Errorf("DB name must NOT be interpolated into the SQL string: %s", countTablesCmd)
	}
	// $DB_NAME must appear exactly once, as the trailing positional arg (USE).
	if n := strings.Count(countTablesCmd, "$DB_NAME"); n != 1 {
		t.Errorf("expected $DB_NAME once (as positional arg), found %d: %s", n, countTablesCmd)
	}
	if !strings.HasSuffix(strings.TrimSpace(countTablesCmd), `"$DB_NAME"`) {
		t.Errorf("$DB_NAME must be the trailing positional argument (USE target): %s", countTablesCmd)
	}
}

// TestWPCredsMatch covers the verification that makes RewriteWPConfig fail loudly
// instead of silently "succeeding" when a define could not be set: a wp-config
// missing the DB_PASSWORD define cannot be rewritten to carry it (wpconfig.Rewrite
// leaves an absent define absent), so the result must NOT match the intended creds.
func TestWPCredsMatch(t *testing.T) {
	full := "<?php\n" +
		"define('DB_NAME', 'destacct_wp694');\n" +
		"define('DB_USER', 'destacct_u');\n" +
		"define('DB_PASSWORD', 'p@ss');\n" +
		"$table_prefix = 'wp_';\n"
	if !wpCredsMatch(full, "destacct_wp694", "destacct_u", "p@ss") {
		t.Error("complete wp-config with the intended creds should match")
	}
	if wpCredsMatch(full, "destacct_wp694", "destacct_u", "different") {
		t.Error("a differing password must not match")
	}
	// Empty args mean 'leave unchanged' (wpconfig.Rewrite semantics): not required.
	if !wpCredsMatch(full, "destacct_wp694", "", "") {
		t.Error("empty user/password args must be skipped")
	}
	// A wp-config with NO DB_PASSWORD define must not match the intended password —
	// this is what turns a silent false success into an error in RewriteWPConfig.
	missingPass := "<?php\n" +
		"define('DB_NAME', 'destacct_wp694');\n" +
		"define('DB_USER', 'destacct_u');\n" +
		"$table_prefix = 'wp_';\n"
	if wpCredsMatch(missingPass, "destacct_wp694", "destacct_u", "p@ss") {
		t.Error("a wp-config without a DB_PASSWORD define must not match the intended password")
	}
}

func TestParseObjectCounts(t *testing.T) {
	cases := []struct {
		in   string
		want ObjectCounts
		ok   bool
	}{
		{"2\t1\t0\t3\n", ObjectCounts{Routines: 2, Events: 1, Triggers: 0, Views: 3}, true},
		{"  0\t0\t0\t0  ", ObjectCounts{}, true},
		{"5\t0\t1\t0", ObjectCounts{Routines: 5, Triggers: 1}, true},
		{"warn line\n2\t1\t0\t3", ObjectCounts{Routines: 2, Events: 1, Views: 3}, true}, // leading warning tolerated (last 4)
		{"", ObjectCounts{}, false},
		{"1\t2\t3", ObjectCounts{}, false},    // too few fields
		{"a\tb\tc\td", ObjectCounts{}, false}, // non-numeric
	}
	for _, c := range cases {
		got, ok := parseObjectCounts(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("parseObjectCounts(%q) = (%+v,%v), want (%+v,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// TestCountObjectsCmdIsScopedAndInjectionSafe mirrors the table-count guards: the
// query must cover all four object kinds, scope to the selected DB via DATABASE(),
// pick the DB only via the trailing positional $DB_NAME (never spliced into SQL),
// and keep the password out of argv.
func TestCountObjectsCmdIsScopedAndInjectionSafe(t *testing.T) {
	for _, want := range []string{
		"information_schema.routines", "information_schema.events",
		"information_schema.triggers", "information_schema.views", "DATABASE()",
	} {
		if !strings.Contains(countObjectsCmd, want) {
			t.Errorf("countObjectsCmd missing %q: %s", want, countObjectsCmd)
		}
	}
	if strings.Contains(countObjectsCmd, "-p") {
		t.Errorf("countObjectsCmd must not put a password in argv: %s", countObjectsCmd)
	}
	if n := strings.Count(countObjectsCmd, "$DB_NAME"); n != 1 {
		t.Errorf("expected $DB_NAME once (positional arg), found %d: %s", n, countObjectsCmd)
	}
	if !strings.HasSuffix(strings.TrimSpace(countObjectsCmd), `"$DB_NAME"`) {
		t.Errorf("$DB_NAME must be the trailing positional argument: %s", countObjectsCmd)
	}
}
