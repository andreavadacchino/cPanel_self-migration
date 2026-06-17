package dbmig

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// TestDeepTablesRowCountCorrelation: every base table must get a COUNT(*) back. If a
// table is seeded from the meta pass but no row count correlates to its name, the
// table would silently keep Rows=0 on both sides and "match" — DeepTables must error
// instead. The positive case confirms a clean correlation merges the counts.
func TestDeepTablesRowCountCorrelation(t *testing.T) {
	// metaCmd emits HEX(table_name); the row-count reply labels each COUNT(*) with
	// HEX(name) too. DeepTables decodes both before correlating.
	meta := "V\t10.5.0-MariaDB\nA\t" + hexName("t1") + "\t0\nA\t" + hexName("t2") + "\t0\n" // two base tables
	rowCountReply := func(reply string) fnRunner {
		return fnRunner(func(_ string, env map[string]string) ([]byte, error) {
			if _, isRowCount := env["SQL"]; isRowCount {
				return []byte(reply), nil
			}
			return []byte(meta), nil
		})
	}

	// Only t1 correlates; t2 got no count -> loud error.
	if _, err := DeepTables(context.Background(), rowCountReply(hexName("t1")+"\t10\n"), "db", "u", "p"); err == nil {
		t.Error("DeepTables must error when a seeded table gets no row count (name/label mismatch)")
	}

	// Both correlate -> no error, counts merged.
	info, err := DeepTables(context.Background(), rowCountReply(hexName("t1")+"\t10\n"+hexName("t2")+"\t20\n"), "db", "u", "p")
	if err != nil {
		t.Fatalf("unexpected error on a clean correlation: %v", err)
	}
	if info.Tables["t1"].Rows != 10 || info.Tables["t2"].Rows != 20 {
		t.Errorf("row counts not merged: %+v", info.Tables)
	}
}

// TestDeepTablesExoticNameCorrelates is the regression for the HEX-label fix: a table
// whose name contains a BACKSLASH and a TAB must correlate its COUNT(*) cleanly. Under
// the old RAW-name path the embedded tab made the meta line ("A\t<name>\t<ai>") and the
// row-count label split into the wrong number of fields, so the name the meta pass
// seeded never equalled the label the count pass returned and DeepTables failed loudly
// with a name/label mismatch even though nothing was wrong. With metaCmd emitting
// HEX(table_name) and the row-count label being HEX(name), both sides are pure hex (no
// tab, no backslash on the wire), split cleanly, and decode back to the same exotic
// name, so the count merges with no spurious error.
func TestDeepTablesExoticNameCorrelates(t *testing.T) {
	const exotic = "dir\\tab\tname" // backslash AND a real tab inside the name
	const plain = "posts"
	meta := "V\t10.5.0-MariaDB\n" +
		"A\t" + hexName(exotic) + "\t0\n" +
		"A\t" + hexName(plain) + "\t0\n"
	r := fnRunner(func(_ string, env map[string]string) ([]byte, error) {
		if _, isRowCount := env["SQL"]; isRowCount {
			// Row-count reply: HEX(name)\t<count>, one per table, exactly as the real
			// mysql -B client emits for the HEX-labelled rowCountSQL.
			return []byte(hexName(exotic) + "\t10\n" + hexName(plain) + "\t20\n"), nil
		}
		return []byte(meta), nil
	})

	info, err := DeepTables(context.Background(), r, "db", "u", "p")
	if err != nil {
		t.Fatalf("exotic table name must correlate cleanly with HEX labels, got error: %v", err)
	}
	// Keys are the DECODED true names (proves the meta-side decode); counts are merged
	// (proves the HEX row-count label decoded back to the same exotic name).
	if got := info.Tables[exotic].Rows; got != 10 {
		t.Errorf("exotic table rows = %d, want 10 (HEX label must correlate): %+v", got, info.Tables)
	}
	if got := info.Tables[plain].Rows; got != 20 {
		t.Errorf("plain table rows = %d, want 20: %+v", got, info.Tables)
	}
	if len(info.Tables) != 2 {
		t.Errorf("want exactly 2 tables, got %d: %+v", len(info.Tables), info.Tables)
	}
}

func TestQuoteIdent(t *testing.T) {
	if got := quoteIdent("wp_posts"); got != "`wp_posts`" {
		t.Errorf("quoteIdent = %q", got)
	}
	// A backtick in the name must be doubled (cannot break out of the quotes).
	if got := quoteIdent("ev`il"); got != "`ev``il`" {
		t.Errorf("quoteIdent escape = %q", got)
	}
}

func TestRowCountSQL(t *testing.T) {
	if rowCountSQL(nil) != "" {
		t.Error("no tables must yield empty SQL")
	}
	// Labels are HEX(name) so a name with backslash/tab/newline can never break the
	// literal nor diverge from how the count is later correlated; the FROM identifier
	// stays backtick-escaped. HEX("a")=61, HEX("b`x")=626078.
	got := rowCountSQL([]string{"a", "b`x"})
	want := "SELECT '61' AS n, COUNT(*) AS c FROM `a`" +
		" UNION ALL SELECT '626078' AS n, COUNT(*) AS c FROM `b``x`"
	if got != want {
		t.Errorf("rowCountSQL =\n%q\nwant\n%q", got, want)
	}
}

func TestChecksumSQL(t *testing.T) {
	if checksumSQL(nil) != "" {
		t.Error("no tables must yield empty SQL")
	}
	if got := checksumSQL([]string{"a", "b"}); got != "CHECKSUM TABLE `a`,`b`" {
		t.Errorf("checksumSQL = %q", got)
	}
}

func TestParseRowCounts(t *testing.T) {
	out := "wp_posts\t1234\nwp_users\t3\nbad line\nweird\tNaN\n"
	got := parseRowCounts(out)
	want := map[string]int64{"wp_posts": 1234, "wp_users": 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseRowCounts = %v, want %v", got, want)
	}
	// A table name containing a space is preserved (split on the LAST tab).
	if got := parseRowCounts("my table\t7\n"); got["my table"] != 7 {
		t.Errorf("spaced name lost: %v", got)
	}
}

func TestParseChecksums(t *testing.T) {
	// Real CHECKSUM TABLE output reports the Table column schema-qualified; the keys
	// must come back BARE so they line up with the meta/row-count maps (else the
	// content check is dead — the original bug).
	out := "wpdb.wp_posts\t551122\nwpdb.wp_meta\tNULL\n"
	got := parseChecksums(out, "wpdb")
	if got["wp_posts"] != "551122" {
		t.Errorf("checksum must be keyed by the bare table name: %v", got)
	}
	if got["wp_meta"] != "" {
		t.Errorf("NULL checksum must map to empty string, got %q", got["wp_meta"])
	}
	// Defensive: an unqualified Table column still parses.
	if g := parseChecksums("wp_users\t9\n", "wpdb"); g["wp_users"] != "9" {
		t.Errorf("bare table name should still parse: %v", g)
	}
	// Only the schema prefix is stripped; a dot inside the table name survives.
	if g := parseChecksums("wpdb.od.d\t7\n", "wpdb"); g["od.d"] != "7" {
		t.Errorf("only the schema prefix should be stripped: %v", g)
	}
}

func TestParseMeta(t *testing.T) {
	// metaCmd emits HEX(table_name); parseMeta must decode it back to the true name so
	// the auto_increment map, the ordered names, and (downstream) the DeepDBInfo.Tables
	// keys are the real bare names the diff/report display.
	out := "V\t10.5.18-MariaDB\nA\t" + hexName("wp_posts") + "\t51\nA\t" + hexName("wp_users") + "\t3\n"
	version, ai, names, ok := parseMeta(out)
	if !ok || version != "10.5.18-MariaDB" {
		t.Fatalf("version = %q ok=%v", version, ok)
	}
	if ai["wp_posts"] != 51 || ai["wp_users"] != 3 {
		t.Errorf("auto_increment = %v", ai)
	}
	if !reflect.DeepEqual(names, []string{"wp_posts", "wp_users"}) {
		t.Errorf("names = %v (must be sorted)", names)
	}
	// Missing version line -> unreadable.
	if _, _, _, ok := parseMeta("A\t" + hexName("t") + "\t1\n"); ok {
		t.Error("missing V line must be !ok")
	}
	// A non-hex table name (corrupt/truncated output) must fail closed, not silently
	// drop the table: HEX() never emits undecodable output, so this can only be bad input.
	if _, _, _, ok := parseMeta("V\t10.5\nA\tZZ\t1\n"); ok {
		t.Error("undecodable hex name must force !ok (fail closed)")
	}
}

func TestDynamicSQLCmdPassesQueryViaEnv(t *testing.T) {
	// The dynamic query must travel in $SQL, never interpolated into the command.
	if !strings.Contains(dynamicSQLCmd, `-e "$SQL"`) || !strings.Contains(dynamicSQLCmd, `"$DB_NAME"`) {
		t.Errorf("dynamicSQLCmd must run $SQL against $DB_NAME: %s", dynamicSQLCmd)
	}
}

func TestParseObjectBodies(t *testing.T) {
	out := "V\t" + hexName("v") + "\tVH1\n" +
		"G\t" + hexName("g") + "\tGH1\n" +
		"R\tPROCEDURE\t" + hexName("p") + "\tPH1\n" +
		"R\tFUNCTION\t" + hexName("f") + "\tFH1\n" +
		"E\t" + hexName("e") + "\tEH1\n"
	got := parseObjectBodies(out)
	want := map[string]string{
		"view v":      "VH1",
		"trigger g":   "GH1",
		"procedure p": "PH1",
		"function f":  "FH1",
		"event e":     "EH1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseObjectBodies =\n%v\nwant\n%v", got, want)
	}
	// An exotic name (HEX round-trips a backslash) keys cleanly; a malformed row is skipped.
	ex := parseObjectBodies("V\t" + hexName(`a\b`) + "\tZ\nV\tnothex\tQ\nG\tonlytwo\n")
	if ex[`view a\b`] != "Z" || len(ex) != 1 {
		t.Errorf("exotic/malformed handling = %v", ex)
	}
}

func TestObjectBodyCmdIsDefinerIndependentAndScoped(t *testing.T) {
	// Must fingerprint the DEFINER-independent body columns, HEX-encode names, hash
	// server-side, and scope every clause to DATABASE().
	for _, want := range []string{
		"view_definition", "action_statement", "routine_definition", "event_definition",
		"HEX(table_name)", "HEX(trigger_name)", "HEX(routine_name)", "HEX(event_name)",
		"MD5(", "DATABASE()",
	} {
		if !strings.Contains(objectBodyCmd, want) {
			t.Errorf("objectBodyCmd must contain %q:\n%s", want, objectBodyCmd)
		}
	}
	// The DEFINER and SQL SECURITY are rewritten on import (V13) and must NOT be compared.
	for _, bad := range []string{"DEFINER", "security_type", "SECURITY_TYPE"} {
		if strings.Contains(objectBodyCmd, bad) {
			t.Errorf("objectBodyCmd must NOT read %q (rewritten on import):\n%s", bad, objectBodyCmd)
		}
	}
	// A literal backtick would trigger shell command substitution inside the -e "...";
	// the view schema-qualifier strip must use CHAR(96) instead.
	if strings.Contains(objectBodyCmd, "`") {
		t.Errorf("objectBodyCmd must not contain a literal backtick (use CHAR(96)):\n%s", objectBodyCmd)
	}
}
