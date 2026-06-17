package dbmig

import (
	"strings"
	"testing"
)

func TestParseTableColumns(t *testing.T) {
	out := encodeHexName("wp_posts") + "\t" + encodeHexName("ID") + "\n" +
		encodeHexName("wp_posts") + "\t" + encodeHexName("post_title") + "\n" +
		encodeHexName("wp_users") + "\t" + encodeHexName("user_login") + "\n" +
		"\n" + // blank line skipped
		"ZZ\tZZ\n" + // undecodable hex -> skipped
		"noTab\n" // no tab -> skipped
	m := parseTableColumns(out)
	if len(m) != 2 {
		t.Fatalf("got %d tables, want 2: %v", len(m), m)
	}
	if got := m["wp_posts"]; len(got) != 2 || got[0] != "ID" || got[1] != "post_title" {
		t.Errorf("wp_posts columns = %v, want [ID post_title] (ordered)", got)
	}
	if got := m["wp_users"]; len(got) != 1 || got[0] != "user_login" {
		t.Errorf("wp_users columns = %v, want [user_login]", got)
	}
}

func TestContentFingerprintSQL(t *testing.T) {
	// Empty / no-column inputs yield no query.
	if s := contentFingerprintSQL(nil); s != "" {
		t.Errorf("nil cols must yield empty SQL, got %q", s)
	}
	if s := contentFingerprintSQL(map[string][]string{"t": {}}); s != "" {
		t.Errorf("a table with no columns must be skipped, got %q", s)
	}

	sql := contentFingerprintSQL(map[string][]string{"wp_posts": {"ID", "post_title"}})
	// Per-row hash: '#'-joined values + a NULL bitmap (so NULL != '') + a per-column
	// byte-length vector (so a value-boundary shift cannot forge an equal row hash).
	wantRow := "MD5(CONCAT_WS('#',`ID`,`post_title`," +
		"CONCAT_WS(',',ISNULL(`ID`),ISNULL(`post_title`))," +
		"CONCAT_WS(',',COALESCE(OCTET_LENGTH(`ID`),0),COALESCE(OCTET_LENGTH(`post_title`),0))))"
	if !strings.Contains(sql, wantRow) {
		t.Errorf("row hash expression missing.\n got: %s\nwant substring: %s", sql, wantRow)
	}
	// Two BIT_XOR-ed 64-bit halves of the MD5, folded together.
	for _, frag := range []string{
		"BIT_XOR(CAST(CONV(SUBSTRING(h,1,16),16,10) AS UNSIGNED))",
		"BIT_XOR(CAST(CONV(SUBSTRING(h,17,16),16,10) AS UNSIGNED))",
		"FROM `wp_posts`",
		"'" + encodeHexName("wp_posts") + "' AS t",
	} {
		if !strings.Contains(sql, frag) {
			t.Errorf("SQL missing %q\n got: %s", frag, sql)
		}
	}

	// Multiple tables: deterministic, sorted, UNION ALL-joined.
	multi := contentFingerprintSQL(map[string][]string{"b_tbl": {"x"}, "a_tbl": {"y"}})
	if strings.Count(multi, "UNION ALL") != 1 {
		t.Errorf("two tables must be one UNION ALL: %s", multi)
	}
	if strings.Index(multi, "'"+encodeHexName("a_tbl")+"'") > strings.Index(multi, "'"+encodeHexName("b_tbl")+"'") {
		t.Errorf("tables must be emitted in sorted order: %s", multi)
	}
}

// A backtick in a table/column name must be doubled (quoteIdent), so it cannot break
// out of the identifier in the generated SQL.
func TestContentFingerprintSQLQuotesIdentifiers(t *testing.T) {
	sql := contentFingerprintSQL(map[string][]string{"od`d": {"c`1"}})
	if !strings.Contains(sql, "FROM `od``d`") {
		t.Errorf("table backtick not doubled: %s", sql)
	}
	if !strings.Contains(sql, "ISNULL(`c``1`)") {
		t.Errorf("column backtick not doubled: %s", sql)
	}
}

func TestParseFingerprints(t *testing.T) {
	out := encodeHexName("wp_posts") + "\t" + "aabb00112233445566778899aabbccdd" + "\n" +
		encodeHexName("wp_empty") + "\t" + "00000000000000000000000000000000" + "\n" +
		"\n" + // blank skipped
		"ZZ\tdeadbeef\n" // undecodable label -> skipped
	m := parseFingerprints(out)
	if len(m) != 2 {
		t.Fatalf("got %d entries, want 2: %v", len(m), m)
	}
	if m["wp_posts"] != "aabb00112233445566778899aabbccdd" {
		t.Errorf("wp_posts fp = %q", m["wp_posts"])
	}
	if m["wp_empty"] != "00000000000000000000000000000000" {
		t.Errorf("wp_empty fp = %q", m["wp_empty"])
	}
}
