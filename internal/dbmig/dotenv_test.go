package dbmig

import (
	"strings"
	"testing"
)

// A Laravel .env using the optional `export ` prefix must be DISCOVERED and REWRITTEN
// (the old `^\s*KEY=` anchor ignored it, so the site was silently left on the old DB).
func TestDotEnvExportPrefix(t *testing.T) {
	cfg := "export DB_DATABASE=old_db\nexport DB_USERNAME=old_u\nexport DB_PASSWORD=old_p\nDB_HOST=127.0.0.1\n"
	got := parseDotEnv(cfg)
	if got.DBName != "old_db" || got.DBUser != "old_u" || got.DBPassword != "old_p" {
		t.Fatalf("export-prefixed parse wrong: %+v", got)
	}
	out := rewriteDotEnv(cfg, "new_db", "new_u", "new_p")
	if !strings.Contains(out, "export DB_DATABASE='new_db'") {
		t.Errorf("rewrite must preserve the export prefix and set the value:\n%s", out)
	}
	if r := parseDotEnv(out); r.DBName != "new_db" || r.DBUser != "new_u" || r.DBPassword != "new_p" {
		t.Errorf("export-prefixed round-trip wrong: %+v\n%s", r, out)
	}
}

// phpdotenv (createImmutable) binds the LAST occurrence of a duplicated key. The parser
// must read it and the rewriter must edit it, so the value PHP uses is the migrated one.
func TestDotEnvDuplicateLastWins(t *testing.T) {
	cfg := "DB_DATABASE=first\nDB_USERNAME=u\nDB_PASSWORD=p\nDB_DATABASE=last\n"
	if got := parseDotEnv(cfg); got.DBName != "last" {
		t.Fatalf("duplicate DB_DATABASE: parsed %q, want the LAST value 'last'", got.DBName)
	}
	out := rewriteDotEnv(cfg, "new_db", "u", "p")
	// The first (dead) duplicate is left untouched; the LAST (bound) one is rewritten.
	if !strings.Contains(out, "DB_DATABASE=first") {
		t.Errorf("rewrite must not touch the dead first duplicate:\n%s", out)
	}
	if !strings.Contains(out, "DB_DATABASE='new_db'") {
		t.Errorf("rewrite must edit the LAST (bound) duplicate:\n%s", out)
	}
	if got := parseDotEnv(out); got.DBName != "new_db" {
		t.Errorf("after rewrite the bound value = %q, want new_db\n%s", got.DBName, out)
	}
}

// A KEY=-looking line INSIDE a multi-line double-quoted value is part of that value,
// not an assignment. The decoy is placed BEFORE the real assignment so the OLD
// first-match line regex would read the decoy: the tokenizer must skip the value and
// bind the real DB_DATABASE, and the rewriter must edit the real line, not the decoy.
func TestDotEnvMultilineDecoyNotMatched(t *testing.T) {
	cfg := "APP_KEY=\"head\nDB_DATABASE=decoy\ntail\"\n" +
		"DB_DATABASE=real_db\nDB_USERNAME=u\nDB_PASSWORD=p\n"
	if got := parseDotEnv(cfg); got.DBName != "real_db" {
		t.Fatalf("decoy inside a multi-line value was matched: parsed %q, want real_db", got.DBName)
	}
	out := rewriteDotEnv(cfg, "new_db", "u", "p")
	if !strings.Contains(out, "DB_DATABASE=decoy") {
		t.Errorf("rewriter corrupted the decoy line inside the value:\n%s", out)
	}
	if !strings.Contains(out, "DB_DATABASE='new_db'") {
		t.Errorf("rewriter must edit the REAL assignment line:\n%s", out)
	}
	if got := parseDotEnv(out); got.DBName != "new_db" {
		t.Errorf("rewrite must edit the REAL assignment, not the decoy: got %q\n%s", got.DBName, out)
	}
}

// A malformed .env (an unterminated quote) cannot be trusted: the parser reads nothing
// (so discovery never associates a half-parsed value) and the rewriter leaves it
// unchanged (so the read-after-write verify fails loudly instead of shipping a guess).
func TestDotEnvMalformedFailsClosed(t *testing.T) {
	cfg := "DB_DATABASE=real\nAPP_KEY=\"never closes\n"
	if got := parseDotEnv(cfg); got.DBName != "" {
		t.Fatalf("malformed .env must parse as empty, got DBName=%q", got.DBName)
	}
	out := rewriteDotEnv(cfg, "new_db", "u", "p")
	if out != cfg {
		t.Errorf("malformed .env must be left unchanged by the rewriter:\n%s", out)
	}
	if credsSet(parseDotEnv(out), "new_db", "u", "p") {
		t.Error("a malformed .env must NOT read as a successful cutover")
	}
}

// Comment and blank lines are ignored; a `# DB_DATABASE=` line is not an assignment.
// The trailing commented line pins comment-skipping for the LAST-wins selection (a
// tokenizer that counted it would bind "commented_after").
func TestDotEnvCommentsIgnored(t *testing.T) {
	cfg := "# DB_DATABASE=commented_out\n\n  # another\nDB_DATABASE=real\nDB_USERNAME=u\nDB_PASSWORD=p\n# DB_DATABASE=commented_after\n"
	if got := parseDotEnv(cfg); got.DBName != "real" {
		t.Fatalf("commented DB_DATABASE leaked: parsed %q, want real", got.DBName)
	}
}

// Edge cases the tokenizer must handle correctly (all phpdotenv-faithful): CRLF line
// endings, spaces around '=', a single-quoted single-line value containing '=' (must
// not start a new assignment), and an escaped quote inside a double-quoted value (must
// not close it, so a following assignment is still found).
func TestDotEnvEdgeCases(t *testing.T) {
	// CRLF: the trailing \r must be trimmed from the value.
	if got := parseDotEnv("DB_DATABASE=crlf_db\r\nDB_USERNAME=u\r\nDB_PASSWORD=p\r\n"); got.DBName != "crlf_db" {
		t.Errorf("CRLF: DBName = %q, want crlf_db", got.DBName)
	}
	// Spaces around '=' and a single-quoted value with an inner '=' (not a separator).
	cfg := "APP_NAME = 'My App = v2'\nDB_DATABASE = real\nDB_USERNAME=u\nDB_PASSWORD=p\n"
	if got := parseDotEnv(cfg); got.DBName != "real" {
		t.Errorf("spaces/quoted-= : DBName = %q, want real", got.DBName)
	}
	// Escaped quote inside a double-quoted value: it does not close the value, so the
	// later DB_DATABASE is still located.
	esc := "DB_PASSWORD=\"a\\\"b\"\nDB_DATABASE=real\nDB_USERNAME=u\n"
	got := parseDotEnv(esc)
	if got.DBName != "real" {
		t.Errorf("escaped-quote: DBName = %q, want real (the escaped \\\" must not close the value)", got.DBName)
	}
	if got.DBPassword != `a"b` {
		t.Errorf("escaped-quote: DBPassword = %q, want a\"b", got.DBPassword)
	}
	// An unterminated SINGLE quote is malformed (phpdotenv throws): fail closed.
	if _, ok := dotEnvScan("DB_DATABASE='open\nDB_USERNAME=u\n"); ok {
		t.Error("an unterminated single-quoted value must make dotEnvScan return ok=false")
	}
	// A single-quoted value whose closing quote is on a LATER line is multi-line, which
	// phpdotenv does not allow for single quotes (it throws). dotEnvScan must NOT span the
	// newline (that would hide the embedded DB_DATABASE=decoy): it fails closed. This pins
	// the single-line rule — a tokenizer that spanned single quotes would return ok=true.
	if _, ok := dotEnvScan("APP='head\nDB_DATABASE=decoy\ntail'\nDB_DATABASE=real\n"); ok {
		t.Error("a multi-line single-quoted value must be malformed (single quotes are single-line)")
	}
}

// dotEnvScan low-level: spacing around '=', export, and quoted values are located so the
// bound (last) assignment per key is correct and value spans exclude inner content.
func TestDotEnvScan(t *testing.T) {
	assigns, ok := dotEnvScan("export DB_DATABASE = 'a b'\nDB_DATABASE=second\n")
	if !ok || len(assigns) != 2 {
		t.Fatalf("scan = %+v ok=%v, want 2 assignments", assigns, ok)
	}
	if assigns[0].key != "DB_DATABASE" || assigns[0].rawValue != "'a b'" {
		t.Errorf("first assignment = %+v, want key DB_DATABASE rawValue 'a b'", assigns[0])
	}
	if assigns[1].rawValue != "second" {
		t.Errorf("second assignment rawValue = %q, want second", assigns[1].rawValue)
	}
	if _, ok := dotEnvScan(`DB_DATABASE="open`); ok {
		t.Error("an unterminated quote must make dotEnvScan return ok=false")
	}
}
