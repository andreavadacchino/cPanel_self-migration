package wpconfig

import "testing"

const sampleConfig = `<?php
/** The name of the database for WordPress */
define( 'DB_NAME', 'srcacct_wp694' );

/** Database username */
define( 'DB_USER', 'srcacct_u1' );

/** Database password */
define( 'DB_PASSWORD', 'S3cr3tP@ss' );

/** Database hostname */
define( 'DB_HOST', '127.0.0.1' );

define( 'DB_CHARSET', 'utf8mb4' );

$table_prefix = 'wpid_';

define( 'WP_DEBUG', false );
`

func TestParseStandard(t *testing.T) {
	c := Parse(sampleConfig)
	if c.DBName != "srcacct_wp694" {
		t.Errorf("DBName = %q, want srcacct_wp694", c.DBName)
	}
	if c.DBUser != "srcacct_u1" {
		t.Errorf("DBUser = %q, want srcacct_u1", c.DBUser)
	}
	if c.DBPassword != "S3cr3tP@ss" {
		t.Errorf("DBPassword = %q, want S3cr3tP@ss", c.DBPassword)
	}
	if c.DBHost != "127.0.0.1" {
		t.Errorf("DBHost = %q, want 127.0.0.1", c.DBHost)
	}
	if c.TablePrefix != "wpid_" {
		t.Errorf("TablePrefix = %q, want wpid_", c.TablePrefix)
	}
}

func TestParseDoubleQuotesAndTightSpacing(t *testing.T) {
	cfg := `<?php
define("DB_NAME","mydb");
define ("DB_USER" , "myuser") ;
define( "DB_PASSWORD" , "p\nope" );
$table_prefix="wp_";
`
	c := Parse(cfg)
	if c.DBName != "mydb" {
		t.Errorf("DBName = %q, want mydb", c.DBName)
	}
	if c.DBUser != "myuser" {
		t.Errorf("DBUser = %q, want myuser", c.DBUser)
	}
	// note: the value regex stops at the first quote, and \n here is literal
	// backslash-n in the source, not a newline, so the value is `p\nope`? No —
	// the value contains no quote, so it captures up to the closing quote.
	if c.TablePrefix != "wp_" {
		t.Errorf("TablePrefix = %q, want wp_", c.TablePrefix)
	}
}

func TestParseValueContainingQuotes(t *testing.T) {
	// Single-quoted password with an ESCAPED single quote and a literal double
	// quote: PHP value is  pa'ss"word
	cfg := `<?php define( 'DB_PASSWORD', 'pa\'ss"word' );`
	c := Parse(cfg)
	if c.DBPassword != `pa'ss"word` {
		t.Errorf("single-quoted DBPassword = %q, want pa'ss\"word", c.DBPassword)
	}

	// Double-quoted password with escaped " and $ and a literal single quote:
	// PHP value is  a"b$c'd
	cfg = `<?php define("DB_PASSWORD","a\"b\$c'd");`
	c = Parse(cfg)
	if c.DBPassword != `a"b$c'd` {
		t.Errorf("double-quoted DBPassword = %q, want a\"b$c'd", c.DBPassword)
	}

	// Backslash in the value: 'a\\b' is the PHP literal a\b
	cfg = `<?php define( 'DB_PASSWORD', 'a\\b' );`
	c = Parse(cfg)
	if c.DBPassword != `a\b` {
		t.Errorf("backslash DBPassword = %q, want a\\b", c.DBPassword)
	}
}

func TestParseRewriteRoundTripWithQuotedPassword(t *testing.T) {
	// The DEST flow READS the copied config, then REWRITES it. A password with a
	// quote must survive: Parse must read the new value back as its real chars.
	cfg := `<?php
define( 'DB_NAME', 'old_db' );
define( 'DB_USER', 'old_user' );
define( 'DB_PASSWORD', 'oldpass' );
$table_prefix = 'wp_';
`
	pw := `s3cr't"$x\y` // every special class
	out := Rewrite(cfg, "v_db", "v_user", pw)
	got := Parse(out)
	if got.DBName != "v_db" || got.DBUser != "v_user" {
		t.Errorf("name/user not rewritten: %+v", got)
	}
	if got.DBPassword != pw {
		t.Errorf("round-trip password = %q, want %q", got.DBPassword, pw)
	}
	if got.TablePrefix != "wp_" {
		t.Errorf("table prefix must survive: %q", got.TablePrefix)
	}
}

func TestParseMissingFields(t *testing.T) {
	c := Parse(`<?php define( 'DB_NAME', 'only_name' );`)
	if c.DBName != "only_name" {
		t.Errorf("DBName = %q", c.DBName)
	}
	if c.DBUser != "" || c.DBPassword != "" || c.DBHost != "" {
		t.Errorf("expected empty user/pass/host, got %+v", c)
	}
}

func TestRewriteReplacesPrefixAndPreservesRest(t *testing.T) {
	out := Rewrite(sampleConfig, "destacct_wp694", "destacct_u1", "")
	c := Parse(out)
	if c.DBName != "destacct_wp694" {
		t.Errorf("rewritten DBName = %q", c.DBName)
	}
	if c.DBUser != "destacct_u1" {
		t.Errorf("rewritten DBUser = %q", c.DBUser)
	}
	// Empty password arg => password unchanged.
	if c.DBPassword != "S3cr3tP@ss" {
		t.Errorf("password should be unchanged, got %q", c.DBPassword)
	}
	// Untouched bits survive.
	if c.DBHost != "127.0.0.1" {
		t.Errorf("DBHost should survive, got %q", c.DBHost)
	}
	if c.TablePrefix != "wpid_" {
		t.Errorf("table prefix must NEVER change, got %q", c.TablePrefix)
	}
	// Unrelated constants survive verbatim.
	if !contains(out, "define( 'WP_DEBUG', false );") {
		t.Error("WP_DEBUG line must be preserved verbatim")
	}
	if !contains(out, "define( 'DB_CHARSET', 'utf8mb4' );") {
		t.Error("DB_CHARSET line must be preserved verbatim")
	}
}

func TestRewriteAllThree(t *testing.T) {
	out := Rewrite(sampleConfig, "destacct_wp694", "destacct_u1", "newpass123")
	c := Parse(out)
	if c.DBName != "destacct_wp694" || c.DBUser != "destacct_u1" || c.DBPassword != "newpass123" {
		t.Errorf("rewrite all three failed: %+v", c)
	}
}

func TestRewriteEscapesSingleQuotePassword(t *testing.T) {
	// A single-quoted define with a password containing a single quote MUST be
	// escaped, or the resulting PHP is broken.
	cfg := `<?php define( 'DB_PASSWORD', 'old' );`
	out := Rewrite(cfg, "", "", `pa'ss`)
	want := `<?php define( 'DB_PASSWORD', 'pa\'ss' );`
	if out != want {
		t.Errorf("Rewrite =\n  %q\nwant\n  %q", out, want)
	}
	// A backslash must also be escaped (and not double-escape the ones we add).
	out = Rewrite(cfg, "", "", `a\b'c`)
	want = `<?php define( 'DB_PASSWORD', 'a\\b\'c' );`
	if out != want {
		t.Errorf("backslash+quote: Rewrite =\n  %q\nwant\n  %q", out, want)
	}
}

func TestRewriteEscapesDoubleQuotePassword(t *testing.T) {
	// A double-quoted define: ", \ and $ are special and must be escaped.
	cfg := `<?php define("DB_PASSWORD","old");`
	out := Rewrite(cfg, "", "", `p"a$b\c`)
	want := `<?php define("DB_PASSWORD","p\"a\$b\\c");`
	if out != want {
		t.Errorf("Rewrite =\n  %q\nwant\n  %q", out, want)
	}
	// A single quote inside a double-quoted string is NOT special -> left as-is.
	out = Rewrite(cfg, "", "", `it's`)
	want = `<?php define("DB_PASSWORD","it's");`
	if out != want {
		t.Errorf("single quote in double-quoted string should be literal: %q", out)
	}
}

// Round-trip safety: after rewriting with an escaped password, re-parsing the
// file (which the DEST step does before rewriting) must NOT mis-handle it.
// (Parse uses the same regex; this documents the current limit: the value regex
// stops at the first quote, so a parsed value won't contain a quote — which is
// fine because we only WRITE the escape, we don't need to round-trip an
// embedded quote through Parse.)
func TestPhpEscapeUnit(t *testing.T) {
	cases := []struct{ in, quote, want string }{
		{`abc`, `'`, `abc`},
		{`a'b`, `'`, `a\'b`},
		{`a\b`, `'`, `a\\b`},
		{`a"b`, `'`, `a"b`},  // " not special in single-quoted
		{`a$b`, `'`, `a$b`},  // $ not special in single-quoted
		{`a"b`, `"`, `a\"b`}, // " special in double-quoted
		{`a$b`, `"`, `a\$b`}, // $ special in double-quoted
		{`a\b`, `"`, `a\\b`}, // \ special in both
		{`a'b`, `"`, `a'b`},  // ' not special in double-quoted
	}
	for _, c := range cases {
		if got := phpEscape(c.in, c.quote); got != c.want {
			t.Errorf("phpEscape(%q, %q) = %q, want %q", c.in, c.quote, got, c.want)
		}
	}
}

func TestRewritePreservesQuotingStyle(t *testing.T) {
	cfg := `define("DB_NAME","old");`
	out := Rewrite(cfg, "new", "", "")
	want := `define("DB_NAME","new");`
	if out != want {
		t.Errorf("Rewrite = %q, want %q (double-quote style must survive)", out, want)
	}
}

func TestRewriteAbsentConstantNotAppended(t *testing.T) {
	cfg := `<?php define( 'DB_NAME', 'x' );`
	out := Rewrite(cfg, "y", "someuser", "somepass")
	// DB_USER / DB_PASSWORD were absent: they must NOT be invented.
	if contains(out, "DB_USER") || contains(out, "DB_PASSWORD") {
		t.Errorf("absent constants must not be appended: %q", out)
	}
	if !contains(out, "'y'") {
		t.Errorf("present constant should be rewritten: %q", out)
	}
}

func TestStringRedactsPassword(t *testing.T) {
	c := Creds{DBName: "d", DBUser: "u", DBPassword: "supersecret", DBHost: "h", TablePrefix: "p"}
	s := c.String()
	if contains(s, "supersecret") {
		t.Errorf("String() must not leak the password: %q", s)
	}
	if !contains(s, "(11 chars)") {
		t.Errorf("String() should report password length, got %q", s)
	}
}

// A host often leaves the stock commented example define above the live one, or a
// previous DB is kept commented out. Parse must read the LIVE constant, never the
// commented decoy (both the // line-comment and /* */ block-comment forms), and
// regardless of which quote style the decoy vs. the live line use.
func TestParseIgnoresCommentedDefine(t *testing.T) {
	cfg := `<?php
// define( 'DB_NAME', 'decoy_name_here' );
define( 'DB_NAME', 'real_db' );

/* old account, kept for reference:
define( 'DB_USER', 'decoy_user' );
*/
define( "DB_USER", "real_user" );

# define( 'DB_PASSWORD', 'decoy_pass' );
define( 'DB_PASSWORD', 'real_pass' );
`
	c := Parse(cfg)
	if c.DBName != "real_db" {
		t.Errorf("DBName = %q, want real_db (commented decoy must be ignored)", c.DBName)
	}
	if c.DBUser != "real_user" {
		t.Errorf("DBUser = %q, want real_user", c.DBUser)
	}
	if c.DBPassword != "real_pass" {
		t.Errorf("DBPassword = %q, want real_pass", c.DBPassword)
	}
}

// The rewrite must edit the LIVE define and leave the commented decoy verbatim;
// otherwise the live constant keeps pointing at the old database while the
// read-after-write check (same parser) reports success.
func TestRewriteIgnoresCommentedDefine(t *testing.T) {
	cfg := `<?php
// define( 'DB_NAME', 'decoy_name_here' );
define( 'DB_NAME', 'old_db' );
define( 'DB_USER', 'old_user' );
define( 'DB_PASSWORD', 'old_pass' );
`
	out := Rewrite(cfg, "new_db", "new_user", "new_pass")
	if c := Parse(out); c.DBName != "new_db" || c.DBUser != "new_user" || c.DBPassword != "new_pass" {
		t.Errorf("live define not rewritten: %+v", c)
	}
	// The commented decoy line must survive untouched.
	if !contains(out, "// define( 'DB_NAME', 'decoy_name_here' );") {
		t.Errorf("commented decoy must be preserved verbatim, got:\n%s", out)
	}
	if contains(out, "'decoy_name_here'") == false {
		t.Error("decoy value should still be present in the comment")
	}
}

// StripComments must not treat comment delimiters that appear INSIDE a string
// literal as comments, or a value containing // or # or /* would be truncated.
func TestStripCommentsHonorsStringLiterals(t *testing.T) {
	cfg := `<?php
define( 'DB_PASSWORD', 'a//b#c/*d' );
define( 'DB_HOST', "http://db.example:3306" );
`
	c := Parse(cfg)
	if c.DBPassword != `a//b#c/*d` {
		t.Errorf("DBPassword = %q, want a//b#c/*d (delimiters inside the string are literal)", c.DBPassword)
	}
	if c.DBHost != "http://db.example:3306" {
		t.Errorf("DBHost = %q, want http://db.example:3306", c.DBHost)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
