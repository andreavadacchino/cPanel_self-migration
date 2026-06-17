package dbmig

import (
	"context"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// Runner is the subset of *sshx.Client this package needs for the read-only
// counts and the wp-config rewrite (satisfied by it). The streaming dump bridge
// uses *sshx.Client directly (see transfer.go).
type Runner interface {
	RunScript(ctx context.Context, script string, env map[string]string) ([]byte, error)
}

// dumpCmd is the read-only SOURCE command that streams a full mysqldump (schema
// + data + routines + events) of one database to stdout. It is the database
// analogue of the tar bridge's `tar -c`.
//
//	--single-transaction : a consistent snapshot WITHOUT locking tables — no
//	                       writes on the source, satisfying the read-only invariant.
//	--quick              : stream rows instead of buffering a whole table in RAM.
//	--no-tablespaces     : REQUIRED for a cPanel (non-root) user — without it
//	                       mysqldump tries to read tablespace metadata and fails
//	                       with "Access denied; you need PROCESS privilege".
//	--routines           : include stored procedures and functions. They are OFF
//	                       by default in mysqldump, so without this a DB's stored
//	                       logic is SILENTLY dropped (table counts still match, so
//	                       verification passes) — unrecoverable once the source is
//	                       gone. (Triggers are already included by default.)
//	--events             : include scheduled EVENTs, likewise OFF by default.
//	--default-character-set=utf8mb4 : preserve multibyte data faithfully.
//
// NOTE: routines/events/triggers carry a DEFINER= clause naming the SOURCE
// MySQL user, which does not exist on the destination; the import strips it (see
// importCmd) so a non-SUPER destination user can create them.
//
// Credentials are passed via environment ONLY: $DB_USER (the cPanel account
// user, which is also a MySQL user able to see all the account's databases) and
// $MYSQL_PWD (its password — mysqldump reads MYSQL_PWD from the environment, so
// the password never appears in argv or the process list). The database name is
// passed via $DB_NAME and expanded by the shell, never interpolated into Go.
//
// baseDumpFlags is the MariaDB-safe set of flags (no GTID flag). BuildDumpCmd adds
// the MySQL-only --set-gtid-purged=OFF on top when the source supports it.
const baseDumpFlags = `--no-tablespaces --single-transaction --quick ` +
	`--routines --events --default-character-set=utf8mb4 -u "$DB_USER" "$DB_NAME"`

// baseDumpCmd is the source dump command WITHOUT --set-gtid-purged. It is the
// MariaDB-safe baseline and the fallback CopyDatabase uses when a Transfer carries
// no resolved DumpCmd (e.g. the zero-value Transfer{} in tests).
const baseDumpCmd = `mysqldump ` + baseDumpFlags

// BuildDumpCmd returns the source mysqldump command, adding --set-gtid-purged=OFF
// iff gtidOff is true — i.e. the SOURCE mysqldump supports it (MySQL). On a
// GTID-enabled MySQL source, mysqldump's default (--set-gtid-purged=AUTO) injects a
// `SET @@GLOBAL.GTID_PURGED=...` (and SET @@SESSION.sql_log_bin=0) into the dump;
// both require SUPER, which the non-SUPER per-database destination import user does
// not have, so the import fails (ERROR 1227/3546). =OFF suppresses only those two
// SET statements (data/schema/routines/events/triggers are byte-identical) and is
// safe whether or not GTIDs are in use. It must be OMITTED on MariaDB, whose
// mysqldump does not know the option and errors out — see SrcSupportsGtidPurged.
// The flag goes among the options, before the positional -u/$DB_NAME args.
func BuildDumpCmd(gtidOff bool) string {
	if gtidOff {
		return `mysqldump --set-gtid-purged=OFF ` + baseDumpFlags
	}
	return baseDumpCmd
}

// SrcSupportsGtidPurged reports whether the SOURCE mysqldump understands
// --set-gtid-purged (true on MySQL, false on MariaDB, whose mysqldump has no such
// option and errors if given it). It is meant to be probed ONCE per run: the source
// server is constant for the whole migration.
//
// The probe runs `mysqldump --help` (a local, no-connect, exit-0 operation on both
// vendors) and glob-matches the option name in its output — deliberately NOT `grep`,
// whose exit-1-on-no-match would be indistinguishable from a real failure. The shell
// distinguishes three outcomes: PROBE_YES (supported), PROBE_NO (ran, option absent
// = MariaDB), and a non-zero exit when mysqldump itself cannot run (absent/broken),
// which surfaces as a RunScript error. A genuine probe failure is returned as an
// error (not silently treated as "unsupported"): guessing is unsafe in both
// directions (the flag breaks a MariaDB dump; its absence breaks a MySQL-GTID
// import), so the caller should fail loudly rather than pick a default.
func SrcSupportsGtidPurged(ctx context.Context, c Runner) (bool, error) {
	const script = `if out="$(mysqldump --help 2>/dev/null)"; then
  case "$out" in
    *--set-gtid-purged*) echo PROBE_YES ;;
    *) echo PROBE_NO ;;
  esac
else
  echo PROBE_ERR
  exit 3
fi`
	out, err := c.RunScript(ctx, script, nil)
	if err != nil {
		return false, fmt.Errorf("probe source mysqldump --set-gtid-purged support: %w", err)
	}
	switch strings.TrimSpace(string(out)) {
	case "PROBE_YES":
		return true, nil
	case "PROBE_NO":
		return false, nil
	default:
		return false, fmt.Errorf("probe source mysqldump --set-gtid-purged support: unexpected output %q", strings.TrimSpace(string(out)))
	}
}

// stripDefinerSed removes mysqldump's DEFINER=<user>@<host> clauses from the
// dump stream. They name the SOURCE MySQL user, which does not exist on the
// destination; a non-SUPER cPanel user cannot create a view/routine/trigger/event
// with a foreign definer and the import would fail with "ERROR 1227 ... SUPER".
// Dropping the clause makes each object inherit the importing (destination) user
// as its definer, which is exactly the per-database user that owns the schema.
//
// mysqldump emits DEFINER in TWO distinct shapes (verified against real
// mysqldump/mariadb-dump output), so the strip needs TWO rules:
//
//  1. VIEW / TRIGGER / EVENT — inside a version-gated comment on a /*!-prefixed
//     line, e.g. `/*!50017 DEFINER=`u`@`h`*/` or `/*!50013 DEFINER=`u`@`h` SQL
//     SECURITY DEFINER */`. Rule 1 is line-anchored to `/^\/\*!/` and CONTEXT-
//     anchored to the comment opener `/*!<digits> `, keeping that opener (group \1)
//     and removing only the clause: `/*!50017 */`, `/*!50013 SQL SECURITY DEFINER */`.
//  2. PROCEDURE / FUNCTION — on a BARE `CREATE DEFINER=`u`@`h` PROCEDURE|FUNCTION …`
//     line (NOT wrapped in a /*! comment), so rule 1's line address would miss it
//     and the foreign DEFINER would survive → the non-SUPER import fails ERROR 1227
//     for any DB with a stored routine. Rule 2 is line-anchored to `/^CREATE DEFINER=/`
//     and strips the leading clause, keeping `CREATE FUNCTION`/`CREATE PROCEDURE`.
//
// Both rules are line- AND context/start-anchored so a body STRING LITERAL or INSERT
// value that merely contains `DEFINER=`x`@`y“ (or a fake `/*!… */` comment) is left
// intact — a global `s#DEFINER=…#` would have corrupted such DDL bodies/data.
// Dropping the clause makes each object inherit the importing (destination) user as
// its definer, which is exactly the per-database user that owns the schema.
//
// The pattern (single-quoted, so the shell does not touch it) is:
//
//	sed '/^\/\*!/ s#\(/\*![0-9]* \)DEFINER=`u`@`h` *#\1#g
//	     /^CREATE DEFINER=/ s#^CREATE DEFINER=`u`@`h` \(FUNCTION\|PROCEDURE\)#CREATE \1#'
const stripDefinerSed = "sed '/^\\/\\*!/ s#\\(/\\*![0-9]* \\)DEFINER=`[^`]*`@`[^`]*` *#\\1#g; /^CREATE DEFINER=/ s#^CREATE DEFINER=`[^`]*`@`[^`]*` \\(FUNCTION\\|PROCEDURE\\)#CREATE \\1#'"

// importPipeline is the inner DESTINATION pipeline body: strip DEFINER clauses
// (see stripDefinerSed), then feed the stream to mysql. Kept as a named piece so
// the doc and tests can read the stages independently of the pipefail wrapper.
const importPipeline = stripDefinerSed +
	` | mysql --default-character-set=utf8mb4 -u "$DB_USER" "$DB_NAME"`

// importCmd is the DESTINATION command that loads a SQL stream into a database.
// It strips DEFINER clauses (see stripDefinerSed) before feeding the stream to
// mysql, under `bash -c` with `set -o pipefail` so the command's exit status
// reflects a failure in ANY stage, not just mysql (the rightmost stage). Without
// pipefail a `sed` that died mid-stream (killed, OOM) could be masked by a mysql
// that read the truncated stream to a statement boundary and exited 0 — a silently
// INCOMPLETE import reported as success. `bash -c` is forced because the user's
// login shell may be sh/dash/jailshell, where `pipefail` is unavailable; the
// codebase already hard-depends on bash (RunScript / webfiles / maildir run
// `bash -s`). The inner $DB_USER/$DB_NAME/$MYSQL_PWD are expanded by this inner
// bash from its inherited environment (Setenv-delivered, or inline-export-delivered
// on AcceptEnv-rejecting servers — see sshx.WithEnv), and the SQL stream on stdin
// reaches sed (the first stage) through `bash -c` unchanged. Same credential
// discipline: $DB_USER + $MYSQL_PWD from the environment, the target database from
// $DB_NAME. The database and user must already exist (the apply step creates them
// first via UAPI).
var importCmd = `bash -c '` + sshx.SingleQuoteEscape("set -o pipefail; "+importPipeline) + `'`

// dumpEnv builds the environment for dumpCmd/importCmd. MYSQL_PWD carries the
// password out-of-band (not in mysql's own argv). Returned as a map so both
// RunScript and the streaming bridge can deliver it via SSH Setenv (with an
// inline-export fallback), keeping the password out of the command string.
func dumpEnv(dbName, dbUser, password string) map[string]string {
	return map[string]string{
		"DB_NAME":   dbName,
		"DB_USER":   dbUser,
		"MYSQL_PWD": password,
	}
}

// countTablesCmd is a read-only command (usable on either side) that prints the
// number of base tables in a database, for the post-copy verification. It uses
// the same credential discipline. Output: a single integer, or an error on
// stderr (non-zero exit) if the DB is inaccessible.
const countTablesCmd = `mysql --default-character-set=utf8mb4 -u "$DB_USER" -N -B -e ` +
	`"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_type='BASE TABLE'" "$DB_NAME"`

// parseCount parses the single-integer output of countTablesCmd. ok is false on
// empty/unparseable output. Pure; unit-tested.
func parseCount(out string) (n int, ok bool) {
	s := strings.TrimSpace(out)
	if s == "" {
		return 0, false
	}
	// Take the last whitespace-separated token, tolerating a leading warning
	// line (e.g. the insecure-password notice if MYSQL_PWD were ever unset).
	fields := strings.Fields(s)
	v, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil {
		return 0, false
	}
	return v, true
}

// ObjectCounts is the per-database count of non-base-table schema objects, used
// by the post-copy verification to confirm routines/events/triggers/views (now
// carried by mysqldump --routines/--events and the default trigger dump) actually
// landed on the destination — the base-table count alone misses all of them.
type ObjectCounts struct {
	Routines int // stored procedures + functions
	Events   int // scheduled events
	Triggers int
	Views    int
}

// Total reports the sum of all object counts (0 => the database has none).
func (o ObjectCounts) Total() int { return o.Routines + o.Events + o.Triggers + o.Views }

// SchemaFingerprint is the named object set of one MySQL schema. Counts alone are
// not enough for migration verification: source tables a,b and destination tables
// a,x both have count=2 but are not equivalent.
type SchemaFingerprint struct {
	Tables   map[string]struct{}
	Views    map[string]struct{}
	Triggers map[string]struct{}
	Routines map[string]struct{} // labels are "PROCEDURE name" or "FUNCTION name"
	Events   map[string]struct{}
}

func newSchemaFingerprint() SchemaFingerprint {
	return SchemaFingerprint{
		Tables:   map[string]struct{}{},
		Views:    map[string]struct{}{},
		Triggers: map[string]struct{}{},
		Routines: map[string]struct{}{},
		Events:   map[string]struct{}{},
	}
}

// ObjectCounts returns the legacy object-count summary for report rendering.
func (s SchemaFingerprint) ObjectCounts() ObjectCounts {
	return ObjectCounts{
		Routines: len(s.Routines),
		Events:   len(s.Events),
		Triggers: len(s.Triggers),
		Views:    len(s.Views),
	}
}

// schemaFingerprintCmd reads the exact names of every schema object in one
// read-only round-trip. The database name is selected by mysql's trailing
// "$DB_NAME" argument; it is never interpolated into the SQL.
//
// Output tags:
//
//	T<TAB><hex(base table)>
//	V<TAB><hex(view)>
//	G<TAB><hex(trigger)>
//	R<TAB><PROCEDURE|FUNCTION><TAB><hex(routine)>
//	E<TAB><hex(event)>
const schemaFingerprintCmd = `mysql --default-character-set=utf8mb4 -u "$DB_USER" -N -B -e ` +
	`"SELECT 'T', HEX(table_name) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_type='BASE TABLE' ORDER BY table_name; ` +
	`SELECT 'V', HEX(table_name) FROM information_schema.views WHERE table_schema=DATABASE() ORDER BY table_name; ` +
	`SELECT 'G', HEX(trigger_name) FROM information_schema.triggers WHERE trigger_schema=DATABASE() ORDER BY trigger_name; ` +
	`SELECT 'R', routine_type, HEX(routine_name) FROM information_schema.routines WHERE routine_schema=DATABASE() ORDER BY routine_type, routine_name; ` +
	`SELECT 'E', HEX(event_name) FROM information_schema.events WHERE event_schema=DATABASE() ORDER BY event_name" ` +
	`"$DB_NAME"`

func parseSchemaFingerprint(out string) SchemaFingerprint {
	fp := newSchemaFingerprint()
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		switch {
		case f[0] == "T" && len(f) >= 2:
			if name, ok := decodeHexName(f[1]); ok {
				fp.Tables[name] = struct{}{}
			}
		case f[0] == "V" && len(f) >= 2:
			if name, ok := decodeHexName(f[1]); ok {
				fp.Views[name] = struct{}{}
			}
		case f[0] == "G" && len(f) >= 2:
			if name, ok := decodeHexName(f[1]); ok {
				fp.Triggers[name] = struct{}{}
			}
		case f[0] == "R" && len(f) >= 3:
			if name, ok := decodeHexName(f[2]); ok {
				fp.Routines[strings.ToUpper(f[1])+" "+name] = struct{}{}
			}
		case f[0] == "E" && len(f) >= 2:
			if name, ok := decodeHexName(f[1]); ok {
				fp.Events[name] = struct{}{}
			}
		}
	}
	return fp
}

func decodeHexName(s string) (string, bool) {
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// encodeHexName is the inverse of decodeHexName: it renders a name as the uppercase
// hex of its bytes, matching MySQL's HEX() output. Used to label per-table row counts
// so an exotic name (backslash/tab/newline) round-trips through mysql -B output
// without the escaping ambiguity a raw string literal would suffer.
func encodeHexName(s string) string {
	return strings.ToUpper(hex.EncodeToString([]byte(s)))
}

// GetSchemaFingerprint returns the exact named-object set of a database. It is the
// authoritative metadata verification input; CountTables/CountObjects are only
// summaries.
func GetSchemaFingerprint(ctx context.Context, c Runner, dbName, user, pass string) (SchemaFingerprint, error) {
	out, err := c.RunScript(ctx, schemaFingerprintCmd, dumpEnv(dbName, user, pass))
	if err != nil {
		return SchemaFingerprint{}, fmt.Errorf("read schema fingerprint for %s: %w", dbName, err)
	}
	fp := parseSchemaFingerprint(string(out))
	logx.Debug("SchemaFingerprint %s: tables=%d views=%d routines=%d events=%d triggers=%d",
		dbName, len(fp.Tables), len(fp.Views), len(fp.Routines), len(fp.Events), len(fp.Triggers))
	return fp, nil
}

// EmptyDatabase drops every existing object in a destination schema before the
// import. A migration overwrites the destination; importing over a dirty schema can
// leave extra tables/objects behind or make the post-copy count checks pass by
// accident. The caller must have already created/granted the destination user.
func EmptyDatabase(ctx context.Context, c Runner, dbName, user, pass string) error {
	fp, err := GetSchemaFingerprint(ctx, c, dbName, user, pass)
	if err != nil {
		return fmt.Errorf("empty database %s: %w", dbName, err)
	}
	sql := dropSchemaSQL(fp)
	if sql == "" {
		return nil
	}
	env := dumpEnv(dbName, user, pass)
	env["SQL"] = sql
	if _, err := c.RunScript(ctx, dynamicSQLCmd, env); err != nil {
		return fmt.Errorf("empty database %s: %w", dbName, err)
	}
	logx.Debug("EmptyDatabase %s: dropped existing objects before import", dbName)
	return nil
}

// safeCollationToken matches a MySQL/MariaDB character-set or collation name
// (letters, digits, underscore only). A value that does not match is never
// spliced into SQL — normalization is skipped instead. cPanel charset/collation
// names always fit this shape; rejecting anything else keeps the ALTER/probe SQL
// injection-proof without needing to quote-escape these tokens. Anchored with
// \A...\z (whole-string, end-of-TEXT) — not ^...$ — so a trailing newline in a
// crafted value from the source server cannot slip through, independent of any
// future regex-flag change.
var safeCollationToken = regexp.MustCompile(`\A[A-Za-z0-9_]+\z`)

// NormalizeDBDefault sets the DESTINATION database's DEFAULT character set and
// collation to (charset, collation) — the source schema's default — so the
// post-migration verify's database-default check matches and a future table
// created without an explicit COLLATE inherits the source's. It changes ONLY the
// schema default; existing tables/data are untouched (their per-table collations
// already travel in the dump), so it cannot mojibake migrated data.
//
// It is best-effort and cross-version-safe. If (charset, collation) does not
// exist on the destination server — the classic case being a MySQL-8
// utf8mb4_0900_* default on a MariaDB destination — it makes NO change and
// returns applied=false with a human reason; the caller logs it and the verify
// soft-classifies the residual default-only difference. It returns an error only
// when a probe/ALTER round-trip itself fails (the caller treats that as
// non-fatal: the data already migrated).
func NormalizeDBDefault(ctx context.Context, c Runner, dbName, user, pass, charset, collation string) (applied bool, reason string, err error) {
	if !safeCollationToken.MatchString(charset) || !safeCollationToken.MatchString(collation) {
		return false, fmt.Sprintf("source default %q/%q is not a plain charset/collation token", charset, collation), nil
	}
	supported, err := collationSupported(ctx, c, dbName, user, pass, charset, collation)
	if err != nil {
		return false, "", err
	}
	if !supported {
		return false, fmt.Sprintf("collation %s (charset %s) does not exist on the destination server", collation, charset), nil
	}
	// charset/collation are validated tokens (safeCollationToken), so they are safe
	// unquoted; the db name is backtick-quoted. dbName/user/pass travel via env.
	env := dumpEnv(dbName, user, pass)
	env["SQL"] = "ALTER DATABASE " + quoteIdent(dbName) + " CHARACTER SET " + charset + " COLLATE " + collation
	if _, err := c.RunScript(ctx, dynamicSQLCmd, env); err != nil {
		return false, "", fmt.Errorf("alter database %s default to %s/%s: %w", dbName, charset, collation, err)
	}
	logx.Debug("NormalizeDBDefault %s: set default to %s/%s", dbName, charset, collation)
	return true, "", nil
}

// collationSupported reports whether (charset, collation) exists on the server
// reachable through the given destination DB connection (read-only). charset and
// collation MUST already be safeCollationToken-validated by the caller, so they
// can be embedded as SQL string literals without further escaping.
func collationSupported(ctx context.Context, c Runner, dbName, user, pass, charset, collation string) (bool, error) {
	env := dumpEnv(dbName, user, pass)
	env["SQL"] = "SELECT COUNT(*) FROM information_schema.COLLATIONS " +
		"WHERE COLLATION_NAME='" + collation + "' AND CHARACTER_SET_NAME='" + charset + "'"
	out, err := c.RunScript(ctx, dynamicSQLCmd, env)
	if err != nil {
		return false, fmt.Errorf("probe collation %s on %s: %w", collation, dbName, err)
	}
	n, ok := parseCount(string(out))
	if !ok {
		return false, fmt.Errorf("probe collation %s on %s: unparseable output %q", collation, dbName, logx.Snippet(out, 80))
	}
	return n > 0, nil
}

func dropSchemaSQL(fp SchemaFingerprint) string {
	if len(fp.Tables)+len(fp.Views)+len(fp.Triggers)+len(fp.Routines)+len(fp.Events) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("SET FOREIGN_KEY_CHECKS=0;\n")
	for _, name := range sortedSet(fp.Views) {
		b.WriteString("DROP VIEW IF EXISTS ")
		b.WriteString(quoteIdent(name))
		b.WriteString(";\n")
	}
	for _, name := range sortedSet(fp.Triggers) {
		b.WriteString("DROP TRIGGER IF EXISTS ")
		b.WriteString(quoteIdent(name))
		b.WriteString(";\n")
	}
	for _, name := range sortedSet(fp.Events) {
		b.WriteString("DROP EVENT IF EXISTS ")
		b.WriteString(quoteIdent(name))
		b.WriteString(";\n")
	}
	for _, label := range sortedSet(fp.Routines) {
		typ, name, ok := strings.Cut(label, " ")
		if !ok {
			continue
		}
		switch typ {
		case "PROCEDURE", "FUNCTION":
			b.WriteString("DROP ")
			b.WriteString(typ)
			b.WriteString(" IF EXISTS ")
			b.WriteString(quoteIdent(name))
			b.WriteString(";\n")
		}
	}
	for _, name := range sortedSet(fp.Tables) {
		b.WriteString("DROP TABLE IF EXISTS ")
		b.WriteString(quoteIdent(name))
		b.WriteString(";\n")
	}
	b.WriteString("SET FOREIGN_KEY_CHECKS=1;")
	return b.String()
}

func sortedSet(m map[string]struct{}) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// countObjectsCmd counts those objects in ONE round-trip and prints a single
// tab-separated line "<routines>\t<events>\t<triggers>\t<views>".
//
// Same SQL-injection discipline as countTablesCmd: every subquery scopes to the
// selected database via DATABASE(); the name is chosen only by the trailing
// positional "$DB_NAME" (which mysql runs USE on), never spliced into the SQL.
// (The event count is independent of the server's event_scheduler setting — an
// imported event is counted whether or not the scheduler is running it.)
const countObjectsCmd = `mysql --default-character-set=utf8mb4 -u "$DB_USER" -N -B -e ` +
	`"SELECT ` +
	`(SELECT COUNT(*) FROM information_schema.routines WHERE routine_schema=DATABASE()), ` +
	`(SELECT COUNT(*) FROM information_schema.events WHERE event_schema=DATABASE()), ` +
	`(SELECT COUNT(*) FROM information_schema.triggers WHERE trigger_schema=DATABASE()), ` +
	`(SELECT COUNT(*) FROM information_schema.views WHERE table_schema=DATABASE())" ` +
	`"$DB_NAME"`

// parseObjectCounts parses countObjectsCmd's "<routines>\t<events>\t<triggers>\t
// <views>" line. ok is false unless four integer fields are present; a leading
// warning line is tolerated by taking the LAST four tokens. Pure; unit-tested.
func parseObjectCounts(out string) (ObjectCounts, bool) {
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) < 4 {
		return ObjectCounts{}, false
	}
	tail := fields[len(fields)-4:]
	n := make([]int, 4)
	for i, f := range tail {
		v, err := strconv.Atoi(f)
		if err != nil {
			return ObjectCounts{}, false
		}
		n[i] = v
	}
	return ObjectCounts{Routines: n[0], Events: n[1], Triggers: n[2], Views: n[3]}, true
}

// CharsetInfo is a database's character-set fingerprint: the schema default
// charset/collation plus each base table's collation. The post-copy verify
// compares it SRC vs DEST to catch a migration that landed under the wrong
// encoding — the classic cPanel "utf8 dumped, latin1 imported" mojibake, which a
// table/row count is blind to (the counts still match, the bytes are mangled).
type CharsetInfo struct {
	DBCharset   string
	DBCollation string
	Tables      map[string]string // table name -> collation
}

// charsetsCmd is a read-only command (either side) printing the schema default
// charset/collation and every base table's collation in ONE round-trip. Same SQL
// discipline as countObjectsCmd: every clause scopes to DATABASE(); the name is
// chosen only by the trailing positional "$DB_NAME". The connection charset is
// fixed to utf8mb4 so the metadata strings themselves are reported faithfully.
//
// Output (tab-separated, -N -B), DB line first then one T line per table:
//
//	DB<TAB><charset><TAB><collation>
//	T<TAB><table><TAB><collation>
const charsetsCmd = `mysql --default-character-set=utf8mb4 -u "$DB_USER" -N -B -e ` +
	`"SELECT 'DB', default_character_set_name, default_collation_name FROM information_schema.schemata WHERE schema_name=DATABASE(); ` +
	`SELECT 'T', table_name, table_collation FROM information_schema.tables WHERE table_schema=DATABASE() AND table_type='BASE TABLE' ORDER BY table_name" ` +
	`"$DB_NAME"`

// parseCharsets parses charsetsCmd output. ok is false unless the DB line is
// present (an empty/garbled result is treated as unreadable, not a clean empty
// charset). A leading warning line is tolerated. Pure; unit-tested.
func parseCharsets(out string) (CharsetInfo, bool) {
	ci := CharsetInfo{Tables: map[string]string{}}
	haveDB := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		switch {
		case f[0] == "DB" && len(f) >= 3:
			ci.DBCharset, ci.DBCollation = f[1], f[2]
			haveDB = true
		case f[0] == "T" && len(f) >= 3:
			ci.Tables[f[1]] = f[2]
		}
	}
	return ci, haveDB
}

// writeConfigScript returns a DESTINATION script that overwrites ONE site config
// file in place with new content supplied via the $NEWCONTENT environment
// variable. The whole new file (produced in Go by the per-CMS rewriter, so this
// script does NO value substitution) travels as an env var — never interpolated
// into the command line — and is written atomically (temp + mv) so a crash cannot
// leave a half-written config. Used for every CMS, not only WordPress.
//
// The path is passed via $WPCONFIG. Three hard guards make this safe even on a
// shared host: (1) CANONICAL containment — both $HOME and the (existing) config are
// resolved with realpath/readlink and the real config must live strictly under the
// real $HOME, so a "../" path or a symlinked config that escapes $HOME is refused
// (a lexical "$HOME"/* check could not see those, and cPanel's /home<->/home2
// symlinks require canonicalizing $HOME too). (2) UNPREDICTABLE temp via mktemp in
// the config's own directory — never a fixed, symlink-plantable name, so the write
// (which carries the DB password) cannot be redirected through a pre-planted symlink,
// and the final mv stays same-filesystem/atomic. (3) FAIL CLOSED on the write/mv —
// a failed write (ENOSPC/quota) removes the temp and exits non-zero instead of
// clobbering the live config with an empty file. `printf %s` (not echo) preserves
// backslashes/leading dashes in the content.
func writeConfigScript() string {
	return `set -u
canon_existing_path() {
  if command -v realpath >/dev/null 2>&1; then realpath -e -- "$1" 2>/dev/null && return 0; fi
  if command -v readlink >/dev/null 2>&1; then readlink -e -- "$1" 2>/dev/null && return 0; fi
  return 10
}
p="$WPCONFIG"
case "$p" in
  /*) : ;;
  *) echo "GUARD: config path must be absolute: $p" >&2; exit 21 ;;
esac
case "/$p/" in
  */../*) echo "GUARD: refuse config path containing '..': $p" >&2; exit 21 ;;
esac
[ -f "$p" ] || { echo "GUARD: config not found: $p" >&2; exit 22 ; }
home_real="$(canon_existing_path "$HOME")" || { echo "GUARD: cannot resolve HOME: $HOME" >&2; exit 21 ; }
p_real="$(canon_existing_path "$p")" || { echo "GUARD: cannot resolve config path: $p" >&2; exit 21 ; }
case "$p_real" in
  "$home_real"/?*) : ;;
  *) echo "GUARD: refuse to write config outside HOME: $p -> $p_real" >&2; exit 21 ;;
esac
[ -f "$p_real" ] || { echo "GUARD: resolved config is not a regular file: $p_real" >&2; exit 22 ; }
dir="$(dirname -- "$p_real")"
tmp="$(mktemp "$dir/.dbmig.XXXXXX")" || { echo "GUARD: cannot create temp file in $dir" >&2; exit 23 ; }
printf '%s' "$NEWCONTENT" > "$tmp" || { rm -f "$tmp"; echo "GUARD: failed to write new config to temp file" >&2; exit 23 ; }
chmod 600 "$tmp" 2>/dev/null || true
chmod --reference="$p_real" "$tmp" 2>/dev/null || true
mv -f "$tmp" "$p_real" || { rm -f "$tmp"; echo "GUARD: failed to install new config" >&2; exit 24 ; }
echo OK
`
}

// NOTE on MYSQL_PWD: the dump/import credentials are delivered over the SSH channel
// via Setenv (see (*sshx.Client).trySetenv), so MYSQL_PWD never enters the exec
// command STRING — not the dump command, and not the `sed | mysql` import PIPELINE
// (where bash, unlike a single command, keeps the inlined command in its own argv).
// On a server that rejects the env channel (AcceptEnv) the bridge does NOT inline the
// password into argv: MYSQL_PWD is delivered through the command's stdin and exported
// by a `read` prologue (see sshx.secretStdinPrologue), so it lands only in the remote
// process ENVIRON. Either way MYSQL_PWD is visible in the remote process environment
// for the command's duration — unavoidable for the MYSQL_PWD mechanism, but
// /proc/PID/environ is owner/root-only, never the world-readable /proc/PID/cmdline.
