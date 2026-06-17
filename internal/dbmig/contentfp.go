package dbmig

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// ContentFingerprint is the cross-engine answer to CHECKSUM TABLE: an
// engine-independent per-table content hash that IS comparable between different
// server flavors/versions (MySQL <-> MariaDB), where CHECKSUM TABLE is not.
//
// For each table it computes BIT_XOR (order-independent, so no ORDER BY and no
// GROUP_CONCAT length cap) of a per-row MD5 over the row's columns, joined with a
// '#' separator plus a trailing NULL bitmap so NULL is distinguished from an empty
// string. The 128-bit MD5 is folded as two 64-bit halves, each BIT_XOR-ed, then
// concatenated — a collision needs both 64-bit aggregates to collide, i.e. ~128-bit
// strength, so a match reliably certifies identical row data. Only standard SQL
// (MD5/CONCAT_WS/ISNULL/CONV/SUBSTRING/BIT_XOR/CAST/LPAD) is used, all present and
// identically defined on MySQL 8 and MariaDB 11.
//
// The one residual is value-to-string FORMATTING in CONCAT_WS: int/varchar/text/
// char/date/datetime/decimal stringify identically across engines, but FLOAT/DOUBLE
// and JSON CAN differ. The caller uses this UPGRADE-ONLY: a full match certifies the
// content; any mismatch (or read error) keeps the existing cross-version
// "content unchecked" verdict — it never turns into a false hard diff.

// tableColumnsSQL reads the column names (HEX-encoded, to survive exotic names)
// of every BASE TABLE in the current database, ordered by table then ordinal so the
// per-row concatenation is built in a stable column order.
const tableColumnsSQL = "SELECT HEX(c.TABLE_NAME), HEX(c.COLUMN_NAME) " +
	"FROM information_schema.COLUMNS c " +
	"JOIN information_schema.TABLES t ON t.TABLE_SCHEMA=c.TABLE_SCHEMA AND t.TABLE_NAME=c.TABLE_NAME " +
	"WHERE c.TABLE_SCHEMA=DATABASE() AND t.TABLE_TYPE='BASE TABLE' " +
	"ORDER BY c.TABLE_NAME, c.ORDINAL_POSITION"

// TableColumns returns the ordered column names of each base table in dbName, keyed
// by true (decoded) table name. Read-only.
func TableColumns(ctx context.Context, c Runner, dbName, user, pass string) (map[string][]string, error) {
	env := dumpEnv(dbName, user, pass)
	env["SQL"] = tableColumnsSQL
	out, err := c.RunScript(ctx, dynamicSQLCmd, env)
	if err != nil {
		return nil, fmt.Errorf("read columns for %s: %w", dbName, err)
	}
	return parseTableColumns(string(out)), nil
}

// parseTableColumns decodes the HEX(table)\tHEX(column) rows into an ordered
// per-table column list. A row whose either field fails to hex-decode is skipped
// (the caller fingerprints only the tables it can fully build, and a missing one
// simply does not certify). Pure.
func parseTableColumns(out string) map[string][]string {
	m := map[string][]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		i := strings.IndexByte(line, '\t')
		if i < 0 {
			continue
		}
		tbl, okT := decodeHexName(strings.TrimSpace(line[:i]))
		col, okC := decodeHexName(strings.TrimSpace(line[i+1:]))
		if !okT || !okC {
			continue
		}
		m[tbl] = append(m[tbl], col)
	}
	return m
}

// contentFingerprintSQL builds the engine-independent per-table content fingerprint
// query (UNION ALL across the given tables, in sorted order for determinism). Each
// table contributes one "<HEX(name)>\t<32-hex fingerprint>" row. A table with no
// columns is skipped. Returns "" when nothing is fingerprintable. Pure; unit-tested.
func contentFingerprintSQL(cols map[string][]string) string {
	names := make([]string, 0, len(cols))
	for t, cs := range cols {
		if len(cs) > 0 {
			names = append(names, t)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	parts := make([]string, len(names))
	for i, t := range names {
		vals := make([]string, len(cols[t]))
		nulls := make([]string, len(cols[t]))
		lens := make([]string, len(cols[t]))
		for j, c := range cols[t] {
			vals[j] = quoteIdent(c)
			nulls[j] = "ISNULL(" + quoteIdent(c) + ")"
			lens[j] = "COALESCE(OCTET_LENGTH(" + quoteIdent(c) + "),0)"
		}
		// Per row: MD5 over the '#'-joined column values, a trailing NULL bitmap (so
		// NULL != '' — CONCAT_WS skips a NULL argument, which is exactly why the bitmap is
		// needed), AND a per-column byte-length vector. The length vector removes the
		// CONCAT_WS separator ambiguity: without it, ('x#y','z') and ('x','y#z') both
		// concatenate to "x#y#z" and would collide; their length vectors (3,1) vs (1,3)
		// differ, so a value-boundary shift can no longer forge an equal row hash.
		rowHash := "MD5(CONCAT_WS('#'," + strings.Join(vals, ",") +
			",CONCAT_WS(','," + strings.Join(nulls, ",") + ")" +
			",CONCAT_WS(','," + strings.Join(lens, ",") + ")))"
		// Fold the 128-bit MD5 as two BIT_XOR-ed 64-bit halves (order-independent),
		// then concatenate. An empty table yields 32 zeros on both sides (BIT_XOR over
		// no rows is the neutral 0).
		fp := "LOWER(CONCAT(" +
			"LPAD(CONV(BIT_XOR(CAST(CONV(SUBSTRING(h,1,16),16,10) AS UNSIGNED)),10,16),16,'0')," +
			"LPAD(CONV(BIT_XOR(CAST(CONV(SUBSTRING(h,17,16),16,10) AS UNSIGNED)),10,16),16,'0')))"
		parts[i] = fmt.Sprintf("SELECT '%s' AS t, %s AS f FROM (SELECT %s AS h FROM %s) x",
			encodeHexName(t), fp, rowHash, quoteIdent(t))
	}
	return strings.Join(parts, " UNION ALL ")
}

// ContentFingerprints computes the engine-independent content fingerprint of each
// requested table (keyed by true table name), read-only. The column lists come from
// the caller (the SOURCE schema) so BOTH sides hash with the identical expression
// and column order; a destination missing a column makes its query error (the caller
// then does not certify), never a silent mismatch-as-match.
func ContentFingerprints(ctx context.Context, c Runner, dbName, user, pass string, cols map[string][]string) (map[string]string, error) {
	sql := contentFingerprintSQL(cols)
	if sql == "" {
		return map[string]string{}, nil
	}
	env := dumpEnv(dbName, user, pass)
	env["SQL"] = sql
	out, err := c.RunScript(ctx, dynamicSQLCmd, env)
	if err != nil {
		return nil, fmt.Errorf("content fingerprint for %s: %w", dbName, err)
	}
	return parseFingerprints(string(out)), nil
}

// parseFingerprints decodes the "<HEX(name)>\t<fingerprint>" rows into a
// name->fingerprint map. A row whose name fails to hex-decode is skipped (it then
// has no fingerprint and cannot certify). Pure.
func parseFingerprints(out string) map[string]string {
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
		name, ok := decodeHexName(strings.TrimSpace(line[:i]))
		if !ok {
			continue
		}
		m[name] = strings.TrimSpace(line[i+1:])
	}
	return m
}
