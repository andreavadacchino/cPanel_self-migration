// Package wpconfig parses and rewrites WordPress wp-config.php database
// credentials. It is used by the database-migration flow for two read-only and
// one write purpose:
//
//   - READ (source, read-only): discover which database/user/password a docroot
//     uses, so a database can be mapped to the domain(s) that reference it and
//     its password reused on the destination.
//   - WRITE (destination only): rewrite DB_NAME/DB_USER/DB_PASSWORD in a copied
//     wp-config.php so the migrated site points at the destination-prefixed
//     database (srcacct_ -> destacct_), preserving everything else (table
//     prefix, salts, custom constants, formatting).
//
// The rewrite is a targeted value substitution on the exact define() lines, NOT
// a re-serialization, so unrelated content and the file's formatting survive.
package wpconfig

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// Creds holds the database connection settings parsed from a wp-config.php.
// TablePrefix is informational (two installs can share one database with
// different prefixes); the migration never changes it.
type Creds struct {
	DBName      string
	DBUser      string
	DBPassword  string
	DBHost      string
	TablePrefix string
}

// defineRes returns the single- and double-quoted REWRITE regexes for a define()
// of constName. RE2 has no backreferences, so — exactly like defineValueRes on the
// READ path — we cannot require "the closing quote equals the opening quote" in a
// single pattern. A lone tolerant ['"] close would let a value like "abc');xyz" be
// cut at the inner ') and corrupt the rewrite. Instead each variant pins ONE quote,
// and its value class excludes that unescaped delimiter, so the value runs to its
// true closing quote (PHP escapes \\, \', \", \$ are still allowed inside).
//
// Each regex captures 3 groups so replaceDefine can rebuild the line verbatim:
// group 1 = everything up to and incl. the opening value quote; group 2 = the value;
// group 3 = the closing quote + trailing ") ;".
//
//	define( 'DB_NAME', 'value' );      define("DB_NAME","value");
func defineRes(constName string) (sq, dq *regexp.Regexp) {
	q := regexp.QuoteMeta(constName)
	pre := `(define\s*\(\s*['"]` + q + `['"]\s*,\s*`
	sq = regexp.MustCompile(pre + `')((?:\\.|[^'\\])*)('\s*\)\s*;?)`)
	dq = regexp.MustCompile(pre + `")((?:\\.|[^"\\])*)("\s*\)\s*;?)`)
	return sq, dq
}

// Value-extraction regexes for the READ path. RE2 has no backreferences, so we
// cannot match "same opening and closing quote with escapes between" in one
// pattern; instead we try a single-quoted form and a double-quoted form and take
// whichever matches. Each value class accepts PHP escape sequences (\\.) so a
// value CONTAINING the delimiter (escaped) is read in full, and the opposite,
// unescaped quote is allowed too.
//
//	single: 'value where \' and \\ are escapes, " is literal'
//	double: "value where \" \\ \$ are escapes, ' is literal"
func defineValueRes(constName string) (sq, dq *regexp.Regexp) {
	q := regexp.QuoteMeta(constName)
	pre := `define\s*\(\s*['"]` + q + `['"]\s*,\s*`
	sq = regexp.MustCompile(pre + `'((?:\\.|[^'\\])*)'\s*\)`)
	dq = regexp.MustCompile(pre + `"((?:\\.|[^"\\])*)"\s*\)`)
	return sq, dq
}

// prefix value regexes ($table_prefix = '...'; or "...";).
var (
	prefixSQ = regexp.MustCompile(`\$table_prefix\s*=\s*'((?:\\.|[^'\\])*)'\s*;?`)
	prefixDQ = regexp.MustCompile(`\$table_prefix\s*=\s*"((?:\\.|[^"\\])*)"\s*;?`)
)

// Parse extracts the database credentials from wp-config.php content. Missing
// constants yield empty fields (the caller decides what is required); it never
// errors on a partial file. Read-only — it does not modify the input. Values are
// PHP-unescaped, so a password containing an escaped quote is returned with its
// real characters.
func Parse(content string) Creds {
	var c Creds
	c.DBName = extractDefine(content, "DB_NAME")
	c.DBUser = extractDefine(content, "DB_USER")
	c.DBPassword = extractDefine(content, "DB_PASSWORD")
	c.DBHost = extractDefine(content, "DB_HOST")
	c.TablePrefix = extractPrefix(content)
	logx.Debug("wpconfig parse: %s (found=%v)", c.String(), c.DBName != "")
	return c
}

// extractDefine reads one define() constant's value and PHP-unescapes it. It
// matches on a comment-stripped copy so a define() inside a comment (e.g. the
// stock `define('DB_NAME', 'database_name_here')` example a host leaves above the
// real one) is ignored, and takes the leftmost of the single-/double-quoted forms
// rather than always preferring single quotes. Returns "" if absent.
func extractDefine(content, constName string) string {
	stripped := StripComments(content)
	sq, dq := defineValueRes(constName)
	loc, quote := leftmost(sq.FindStringSubmatchIndex(stripped), "'",
		dq.FindStringSubmatchIndex(stripped), `"`)
	if loc == nil {
		return ""
	}
	return phpUnescape(stripped[loc[2]:loc[3]], quote)
}

// extractPrefix reads $table_prefix (single- or double-quoted), unescaped,
// ignoring commented-out lines and preferring the leftmost match.
func extractPrefix(content string) string {
	stripped := StripComments(content)
	loc, quote := leftmost(prefixSQ.FindStringSubmatchIndex(stripped), "'",
		prefixDQ.FindStringSubmatchIndex(stripped), `"`)
	if loc == nil {
		return ""
	}
	return phpUnescape(stripped[loc[2]:loc[3]], quote)
}

// Unescape is the exported PHP string-literal unescaper (see phpUnescape), so
// other packages' CMS parsers can reuse the same READ-path logic. quote is the
// delimiter ("'" or "\"") of the literal the value came from.
func Unescape(s, quote string) string { return phpUnescape(s, quote) }

// Escape is the exported PHP string-literal escaper (see phpEscape), so other
// packages' CMS rewriters can re-escape a value for the quote style a config uses
// — the WRITE-path counterpart of Unescape. quote is the delimiter ("'" or "\"").
func Escape(value, quote string) string { return phpEscape(value, quote) }

// phpUnescape reverses phpEscape for a value read from a PHP string literal
// delimited by quote. It is the inverse used on the READ path so a value with an
// escaped delimiter round-trips to its real characters.
//
//	single-quoted: \' -> ' and \\ -> \ (PHP leaves any other \x literal, but we
//	  only ever emit those two, and treating \x as x is wrong — so keep other
//	  backslashes as-is).
//	double-quoted: \" -> ", \$ -> $, \\ -> \ (and we keep other \x literal).
func phpUnescape(s, quote string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			next := s[i+1]
			unescape := next == '\\' ||
				(quote == "'" && next == '\'') ||
				(quote == `"` && (next == '"' || next == '$'))
			if unescape {
				b.WriteByte(next)
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Rewrite returns a copy of wp-config.php content with DB_NAME, DB_USER and
// DB_PASSWORD replaced by the given values, preserving each define()'s original
// quoting and the rest of the file verbatim. A constant that is absent is left
// absent (not appended) — a valid WordPress config always has all three, and
// silently inventing lines would be surprising. The table prefix is never
// touched.
//
// An empty replacement value for a field means "leave that field unchanged",
// so a caller can rewrite only the name/user and keep the existing password.
func Rewrite(content, dbName, dbUser, dbPassword string) string {
	logx.Debug("wpconfig rewrite: name=%v user=%v password=%v", dbName != "", dbUser != "", dbPassword != "")
	out := content
	if dbName != "" {
		out = replaceDefine(out, "DB_NAME", dbName)
	}
	if dbUser != "" {
		out = replaceDefine(out, "DB_USER", dbUser)
	}
	if dbPassword != "" {
		out = replaceDefine(out, "DB_PASSWORD", dbPassword)
	}
	return out
}

// replaceDefine substitutes the value of one define() constant, keeping the
// surrounding text (group 1 = prefix incl. the opening quote, group 3 = closing
// quote + tail) exactly as written. Only the FIRST occurrence is replaced
// (wp-config defines each constant once).
//
// The value is ESCAPED for the quote style the matched define uses, so a value
// containing a quote, backslash (or, in a double-quoted string, a '$') cannot break
// the PHP or change its meaning. This matters for reused DB passwords, which are
// arbitrary.
func replaceDefine(content, constName, value string) string {
	// Locate the define() on a comment-stripped copy so a commented-out decoy is
	// never the one we edit (which would leave the live constant pointing at the
	// old database while read-after-write verify, sharing the same blind spot,
	// reports success). StripComments preserves byte offsets, so the match indices
	// apply directly to the ORIGINAL content and any comment in the untouched
	// prefix/tail survives. Take the leftmost of the single-/double-quoted forms;
	// the matched variant fixes the quote style for escaping and line rebuild.
	stripped := StripComments(content)
	sq, dq := defineRes(constName)
	loc, quote := leftmost(sq.FindStringSubmatchIndex(stripped), "'",
		dq.FindStringSubmatchIndex(stripped), `"`)
	if loc == nil {
		logx.Debug("wpconfig: %s not found in wp-config, left unchanged", constName)
		return content
	}
	// loc: [2,3]=g1 (prefix incl. opening quote), [4,5]=g2 (value),
	// [6,7]=g3 (closing quote + tail). Rebuild g1 + escaped-value + g3.
	g1 := content[loc[2]:loc[3]]
	g3 := content[loc[6]:loc[7]]
	return content[:loc[0]] + g1 + phpEscape(value, quote) + g3 + content[loc[1]:]
}

// phpEscape escapes value for a PHP string literal delimited by quote ("'" or
// '"'), matching PHP's own escaping rules:
//
//   - single-quoted: only \ and ' are special -> backslash-escape both;
//   - double-quoted: \ " and $ are special (the last to prevent variable/$-expr
//     interpolation) -> backslash-escape all three.
//
// Backslash is replaced FIRST so we don't double-escape the backslashes we add.
func phpEscape(value, quote string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	if quote == `"` {
		value = strings.ReplaceAll(value, `"`, `\"`)
		value = strings.ReplaceAll(value, `$`, `\$`)
		return value
	}
	// default: single-quoted
	return strings.ReplaceAll(value, `'`, `\'`)
}

// String renders creds for debug logs WITHOUT exposing the password.
func (c Creds) String() string {
	return fmt.Sprintf("db=%s user=%s host=%s prefix=%s pass=%s",
		c.DBName, c.DBUser, c.DBHost, c.TablePrefix, redact(c.DBPassword))
}

func redact(s string) string {
	if s == "" {
		return "(none)"
	}
	return fmt.Sprintf("(%d chars)", len(s))
}
