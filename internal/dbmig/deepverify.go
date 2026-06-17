package dbmig

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// This file implements the --deep-verify database checks: EXACT per-table row
// counts (engine-independent, the reliable content-loss signal) plus, when both
// sides run the IDENTICAL server version, a per-table CHECKSUM TABLE content hash.
//
// CHECKSUM TABLE is deliberately gated on equal versions: its algorithm is NOT
// comparable across engines/versions (MariaDB vs MySQL, or different major lines
// produce different checksums for byte-identical data), so comparing it across a
// version boundary would flag every table as different. Row counts have no such
// caveat — COUNT(*) is exact and identical everywhere — so they are the hard
// signal; a checksum mismatch (same version, same row count) is a soft "investigate"
// hint, never a hard failure.
//
// Dynamic SQL (built from the database's own table names) is passed to mysql via
// the $SQL environment variable, never interpolated into the shell command. Every
// identifier is backtick-escaped and every row-count label is the HEX of the table
// name (matching metaCmd's HEX(table_name)), so an exotic name round-trips through
// mysql -B output without escaping ambiguity and a hostile table name can neither
// break out of the shell nor out of the SQL.

// DeepTable is one base table's deep-verify attributes.
type DeepTable struct {
	Rows     int64
	AutoIncr int64
	Checksum string // "" unless a (version-gated) CHECKSUM TABLE was run
}

// DeepDBInfo is a database's deep fingerprint: the server version (used to gate
// the cross-version-unsafe checksum) and the per-table rows + AUTO_INCREMENT.
type DeepDBInfo struct {
	Version string
	Tables  map[string]DeepTable
}

// quoteIdent backtick-quotes a MySQL identifier, doubling any embedded backtick so
// a table name can never break out of the quotes.
func quoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// rowCountSQL builds a single statement that returns one "<hex(name)>\t<count>" row
// per table via UNION ALL of HEX-labelled COUNT(*)s (the label, not row order, keys
// the result). The label is the HEX of the table name so it round-trips through
// mysql -B output identically to metaCmd's HEX(table_name), letting DeepTables
// correlate counts back to names even for exotic names (backslash/tab/newline that a
// raw string literal would escape inconsistently). Empty names yields "" (the caller
// skips the query). Pure.
func rowCountSQL(names []string) string {
	if len(names) == 0 {
		return ""
	}
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = fmt.Sprintf("SELECT '%s' AS n, COUNT(*) AS c FROM %s", encodeHexName(n), quoteIdent(n))
	}
	return strings.Join(parts, " UNION ALL ")
}

// checksumSQL builds "CHECKSUM TABLE `a`,`b`,..." for the given tables, or "" for
// none. Pure.
func checksumSQL(names []string) string {
	if len(names) == 0 {
		return ""
	}
	idents := make([]string, len(names))
	for i, n := range names {
		idents[i] = quoteIdent(n)
	}
	return "CHECKSUM TABLE " + strings.Join(idents, ",")
}

// parseRowCounts parses "<name>\t<count>" lines into a map. A non-integer count is
// skipped. Pure.
func parseRowCounts(out string) map[string]int64 {
	m := map[string]int64{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		i := strings.LastIndex(line, "\t")
		if i < 0 {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(line[i+1:]), 10, 64)
		if err != nil {
			continue
		}
		m[line[:i]] = n
	}
	return m
}

// parseChecksums parses CHECKSUM TABLE output ("<table>\t<checksum>") into a map
// keyed by BARE table name. MySQL reports the Table column schema-qualified
// ("<schema>.<table>") even when the statement named the table bare, so the known
// schema prefix is stripped here to match the bare names the meta/row-count maps
// (and the deep-table diff) use. Without this every lookup missed and the checksum
// content check silently did nothing. A NULL checksum (an unchecksummable table) is
// stored as "" so the diff layer can tell "no content proof" from a real hash:
// diffDeepTables treats an empty checksum on either side as content-UNVERIFIED (never
// a silent pass), and only two present, non-empty, equal checksums pass. Pure.
func parseChecksums(out, schema string) map[string]string {
	prefix := schema + "."
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		i := strings.LastIndex(line, "\t")
		if i < 0 {
			continue
		}
		name := strings.TrimPrefix(line[:i], prefix)
		ck := strings.TrimSpace(line[i+1:])
		if ck == "NULL" {
			ck = ""
		}
		m[name] = ck
	}
	return m
}

// metaCmd reads the server version plus each base table's name + AUTO_INCREMENT in
// one round-trip. The table name is read as HEX(table_name) (same discipline as
// schemaFingerprintCmd) so an exotic name (backslash/tab/newline) cannot corrupt the
// tab-delimited output nor diverge from the row-count correlation. Output:
//
//	V<TAB><version>
//	A<TAB><hex(table)><TAB><auto_increment>
const metaCmd = `mysql --default-character-set=utf8mb4 -u "$DB_USER" -N -B -e ` +
	`"SELECT 'V', VERSION(); ` +
	`SELECT 'A', HEX(table_name), IFNULL(auto_increment,0) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_type='BASE TABLE' ORDER BY table_name" ` +
	`"$DB_NAME"`

// dynamicSQLCmd runs the query held in $SQL against $DB_NAME. The query is built in
// Go (identifier/label escaped) and passed via the environment, never interpolated
// into the command line.
const dynamicSQLCmd = `mysql --default-character-set=utf8mb4 -u "$DB_USER" -N -B -e "$SQL" "$DB_NAME"`

// parseMeta parses metaCmd output into the version and a name->auto_increment map
// (plus the ordered names). Table names arrive HEX-encoded and are decoded back to
// their true bytes here, so an exotic name (backslash/tab/newline) can neither
// corrupt the tab-split nor diverge from the row-count correlation. ok is false if
// the version line is absent OR any table name fails to hex-decode: mysql's HEX()
// never emits undecodable output, so a decode failure means corrupt/truncated input
// and the row-count path (which has no independent backstop) must fail closed rather
// than silently drop a table. Pure.
//
// The fail-closed verdict is ORDER-INDEPENDENT: a hex-decode failure is STICKY, so it
// cannot be re-upgraded by a `V` line that arrives after it (the real metaCmd emits V
// first, but a garbled/truncated/interleaved stream must still fail closed). `ok` is
// therefore tracked as "saw a version" AND "no hex failure" separately, not a single
// last-write-wins flag.
func parseMeta(out string) (version string, autoIncr map[string]int64, names []string, ok bool) {
	autoIncr = map[string]int64{}
	var sawVersion, hexFailed bool
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		switch {
		case f[0] == "V" && len(f) >= 2:
			version = f[1]
			sawVersion = true
		case f[0] == "A" && len(f) >= 3:
			name, okHex := decodeHexName(f[1])
			if !okHex {
				hexFailed = true // sticky: a later V must not erase this failure
				continue
			}
			ai, _ := strconv.ParseInt(strings.TrimSpace(f[2]), 10, 64)
			autoIncr[name] = ai
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return version, autoIncr, names, sawVersion && !hexFailed
}

// DeepTables returns the deep fingerprint (version + per-table rows + AUTO_INCREMENT)
// of a database on the given side, read-only. It runs the meta query, then an exact
// COUNT(*) per table. Checksums are NOT included (they are version-gated and fetched
// separately by ChecksumTables only when both sides' versions match).
func DeepTables(ctx context.Context, c Runner, dbName, user, pass string) (DeepDBInfo, error) {
	metaOut, err := c.RunScript(ctx, metaCmd, dumpEnv(dbName, user, pass))
	if err != nil {
		return DeepDBInfo{}, fmt.Errorf("row-count meta for %s: %w", dbName, err)
	}
	version, autoIncr, names, ok := parseMeta(string(metaOut))
	if !ok {
		return DeepDBInfo{}, fmt.Errorf("row-count meta for %s: unparseable output %q", dbName, logx.Snippet(metaOut, 160))
	}
	info := DeepDBInfo{Version: version, Tables: make(map[string]DeepTable, len(names))}
	for _, n := range names {
		info.Tables[n] = DeepTable{AutoIncr: autoIncr[n]}
	}
	if sql := rowCountSQL(names); sql != "" {
		env := dumpEnv(dbName, user, pass)
		env["SQL"] = sql
		rcOut, err := c.RunScript(ctx, dynamicSQLCmd, env)
		if err != nil {
			return DeepDBInfo{}, fmt.Errorf("row counts for %s: %w", dbName, err)
		}
		matched := 0
		for label, rows := range parseRowCounts(string(rcOut)) {
			n, okHex := decodeHexName(label)
			if !okHex {
				continue // uncorrelated label -> caught by the count guard below
			}
			if e, exists := info.Tables[n]; exists {
				e.Rows = rows
				info.Tables[n] = e
				matched++
			}
		}
		// Every base table must correlate its COUNT(*) back to its information_schema
		// name. The label is the HEX of the true name (as is metaCmd's table name), so
		// the two key identically even for exotic names; a miss here means a table really
		// got no row count back (dropped between reads, or a corrupt/truncated line) and
		// would otherwise keep Rows=0 on BOTH sides and silently "match", a false
		// content-OK. Fail loudly so deepDB reports UNVERIFIED with a visible reason
		// instead of certifying a row count it never actually read.
		if matched != len(names) {
			return DeepDBInfo{}, fmt.Errorf("row counts for %s: correlated %d of %d table(s): name/label mismatch", dbName, matched, len(names))
		}
	}
	logx.Debug("DeepTables %s: version=%s, %d table(s)", dbName, version, len(names))
	return info, nil
}

// ChecksumTables runs CHECKSUM TABLE for the given tables, read-only, returning a
// name->checksum map. The caller invokes it ONLY when both sides report the same
// server version (CHECKSUM TABLE is not comparable across versions/engines).
func ChecksumTables(ctx context.Context, c Runner, dbName, user, pass string, names []string) (map[string]string, error) {
	sql := checksumSQL(names)
	if sql == "" {
		return map[string]string{}, nil
	}
	env := dumpEnv(dbName, user, pass)
	env["SQL"] = sql
	out, err := c.RunScript(ctx, dynamicSQLCmd, env)
	if err != nil {
		return nil, fmt.Errorf("checksum tables for %s: %w", dbName, err)
	}
	return parseChecksums(string(out), dbName), nil
}

// objectBodyCmd reads a DEFINER-INDEPENDENT content fingerprint of every non-table
// schema object's BODY (view/trigger/routine/event) in one read-only round-trip. It
// answers finding V12: the name-set fingerprint is blind to a same-name object whose
// definition changed (e.g. a botched DEFINER-strip corrupting a body, or a partial
// import). The DEFINER and SQL SECURITY are deliberately EXCLUDED (the import rewrites
// them, see stripDefinerSed, so comparing them would false-positive on every migrated
// object) by reading only the *_DEFINITION/ACTION_STATEMENT body columns.
//
// Two server quirks are handled in SQL:
//   - VIEW_DEFINITION is re-canonicalized by the server and embeds the schema name
//     (`db`.`t`.`c`), so a DB rename (the normal cPanel migration case) would look like
//     a body change; REPLACE strips the view's own `<schema>`. qualifier. A literal
//     backtick cannot appear here (it would trigger shell command substitution inside
//     the double-quoted -e), so CHAR(96) is used.
//   - a NULL body (an external/native routine) is mapped to CHAR(0) so it hashes
//     distinctly from an empty body; CONCAT_WS would otherwise skip a NULL argument.
//
// EVENT status/schedule-time columns are excluded (the import commonly disables events
// and rewrites STARTS/ENDS). Names are HEX-encoded so exotic names round-trip.
//
// Two known heuristic limits: (1) the view schema-qualifier strip is a plain-text
// REPLACE, so a view whose body embeds the literal text `<schema>`. inside a STRING can
// over-fire a spurious same-version diff (fail-safe: it over-reports, never a false OK);
// (2) the routine hash covers the body + return type but NOT the full parameter list, so
// a param-only change with an identical body is not detected (a faithful dump reproduces
// the signature, so this narrow residual bites only a corrupt/partial import). Output:
//
//	V<TAB><hex(view)><TAB><md5>
//	G<TAB><hex(trigger)><TAB><md5>
//	R<TAB><PROCEDURE|FUNCTION><TAB><hex(routine)><TAB><md5>
//	E<TAB><hex(event)><TAB><md5>
const objectBodyCmd = `mysql --default-character-set=utf8mb4 -u "$DB_USER" -N -B -e ` +
	`"SELECT 'V', HEX(table_name), MD5(CONCAT_WS(0x1e, IF(view_definition IS NULL, CHAR(0), REPLACE(view_definition, CONCAT(CHAR(96), table_schema, CHAR(96), '.'), '')), COALESCE(check_option,''))) FROM information_schema.views WHERE table_schema=DATABASE(); ` +
	`SELECT 'G', HEX(trigger_name), MD5(CONCAT_WS(0x1e, COALESCE(action_timing,''), COALESCE(event_manipulation,''), COALESCE(action_orientation,''), IF(action_statement IS NULL, CHAR(0), action_statement))) FROM information_schema.triggers WHERE trigger_schema=DATABASE(); ` +
	`SELECT 'R', routine_type, HEX(routine_name), MD5(CONCAT_WS(0x1e, routine_type, COALESCE(dtd_identifier,''), IF(routine_definition IS NULL, CHAR(0), routine_definition))) FROM information_schema.routines WHERE routine_schema=DATABASE(); ` +
	`SELECT 'E', HEX(event_name), MD5(CONCAT_WS(0x1e, IF(event_definition IS NULL, CHAR(0), event_definition), COALESCE(interval_value,''), COALESCE(interval_field,''), COALESCE(on_completion,''))) FROM information_schema.events WHERE event_schema=DATABASE()" ` +
	`"$DB_NAME"`

// parseObjectBodies parses objectBodyCmd output into a label->body-hash map. Labels are
// "view <name>" / "trigger <name>" / "procedure <name>" / "function <name>" / "event
// <name>" so the diff can name the diverged object. Names arrive HEX-encoded (decoded
// here); a row with an undecodable name or too few fields is skipped. Pure.
func parseObjectBodies(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		switch {
		case f[0] == "V" && len(f) >= 3:
			if n, ok := decodeHexName(f[1]); ok {
				m["view "+n] = f[2]
			}
		case f[0] == "G" && len(f) >= 3:
			if n, ok := decodeHexName(f[1]); ok {
				m["trigger "+n] = f[2]
			}
		case f[0] == "R" && len(f) >= 4:
			if n, ok := decodeHexName(f[2]); ok {
				m[strings.ToLower(f[1])+" "+n] = f[3]
			}
		case f[0] == "E" && len(f) >= 3:
			if n, ok := decodeHexName(f[1]); ok {
				m["event "+n] = f[2]
			}
		}
	}
	return m
}

// ObjectBodies returns a DEFINER-independent body fingerprint (label -> MD5) of every
// view/trigger/routine/event in a database, read-only. The caller compares src vs dest
// only at an IDENTICAL server version (definitions are server-canonicalized and not
// comparable across versions, like CHECKSUM TABLE).
func ObjectBodies(ctx context.Context, c Runner, dbName, user, pass string) (map[string]string, error) {
	out, err := c.RunScript(ctx, objectBodyCmd, dumpEnv(dbName, user, pass))
	if err != nil {
		return nil, fmt.Errorf("object bodies for %s: %w", dbName, err)
	}
	bodies := parseObjectBodies(string(out))
	logx.Debug("ObjectBodies %s: %d non-table object(s) fingerprinted", dbName, len(bodies))
	return bodies, nil
}
