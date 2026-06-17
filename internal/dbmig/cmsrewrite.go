package dbmig

import (
	"regexp"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/wpconfig"
)

// This file is the WRITE-side counterpart of cmsconfig.go: for each CMS whose
// credentials cmsconfig.go can READ, a rewriter replaces the database name/user/
// password IN PLACE on the destination config, preserving quoting, formatting and
// every unrelated setting (the same targeted-substitution approach as
// wpconfig.Rewrite, never a re-serialization). RewriteSiteConfig (transfer.go)
// dispatches to these by Kind and verifies the result by RE-READING with the
// matching cmsconfig.go parser, so a value a rewriter could not place is surfaced
// instead of silently shipped. Formats are implemented incrementally; a kind with
// no entry here is reported as an *UnsupportedRewriteError (a manual action).

// siteRewriter pairs a value rewriter with the read parser used to verify it.
type siteRewriter struct {
	rewrite func(content, dbName, dbUser, dbPassword string) string
	parse   configParser
}

// siteRewriters maps a CMS kind to its rewriter. WordPress is handled separately
// (wpconfig.Rewrite via RewriteWPConfig); a kind absent here is unsupported.
var siteRewriters = map[Kind]siteRewriter{
	KindJoomla:     {rewriteJoomla, parseJoomla},
	KindDrupal:     {rewriteDrupal, parseDrupal},
	KindMoodle:     {rewriteMoodle, parseMoodle},
	KindMagento:    {rewriteMagento, parseMagentoEnv},
	KindPrestaShop: {rewritePrestaShop, parsePrestaShop},
	KindOpenCart:   {rewriteOpenCart, parseOpenCart},
	KindLaravel:    {rewriteDotEnv, parseDotEnv},
}

// assignPre is the WRITE-side mirror of phpAssign's prefix: it matches up to and
// including the `=` of `[$visibility] $name =`, so replaceQuotedAfter can swap the
// quoted value that follows.
func assignPre(name string) string {
	return `(?:public|protected|private|var)?\s*\$` + regexp.QuoteMeta(name) + `\s*=\s*`
}

// replaceQuotedAfter replaces the value of the single- or double-quoted PHP string
// literal that follows the `pre` regex, re-escaping the new value for the quote
// style actually used and preserving the quotes and all surrounding text. Returns
// (content, false) unchanged when no such literal is found. Mirrors the READ-side
// extractQuoted so the write target is exactly what the parser reads back.
//
// Matching runs on a comment-stripped copy so a commented-out decoy assignment is
// never the line we edit (which would leave the live credential pointing at the
// source database while the read-after-write verify, sharing the same blind spot,
// reports success). StripComments preserves byte offsets, so the match indices
// apply directly to the ORIGINAL content. The leftmost of the two quote forms wins
// rather than always preferring single quotes.
func replaceQuotedAfter(content, pre, value string) (string, bool) {
	stripped := wpconfig.StripComments(content)
	loc, quote := leftmostQuoted(matchQuoted(pre, "'").FindStringSubmatchIndex(stripped), "'",
		matchQuoted(pre, `"`).FindStringSubmatchIndex(stripped), `"`)
	if loc == nil {
		return content, false
	}
	// loc[3] = end of group 1 (after the opening quote); loc[6] = start of group 3
	// (the closing quote). The span between is exactly the old value (group 2).
	return content[:loc[3]] + wpconfig.Escape(value, quote) + content[loc[6]:], true
}

// matchQuoted builds the rewrite regex for a `pre`-prefixed literal delimited by
// quote ("'" or "\""): group 1 = pre + opening quote, group 2 = value (PHP escapes
// allowed so an escaped delimiter is matched in full), group 3 = closing quote.
func matchQuoted(pre, quote string) *regexp.Regexp {
	return regexp.MustCompile(`(` + pre + quote + `)((?:\\.|[^` + quote + `\\])*)(` + quote + `)`)
}

// rewriteJoomla rewrites a Joomla configuration.php: `public $db` / `$user` /
// `$password`. $host and $dbprefix are left untouched, matching the name/user/
// password-only rewrite the migration does for every CMS.
func rewriteJoomla(content, dbName, dbUser, dbPassword string) string {
	out := content
	if dbName != "" {
		out, _ = replaceQuotedAfter(out, assignPre("db"), dbName)
	}
	if dbUser != "" {
		out, _ = replaceQuotedAfter(out, assignPre("user"), dbUser)
	}
	if dbPassword != "" {
		out, _ = replaceQuotedAfter(out, assignPre("password"), dbPassword)
	}
	return out
}

// arrayKeyPre is the WRITE-side mirror of phpArrayKey's prefix: it matches up to
// and including the `=>` of `'key' =>`, so replaceQuotedAfter can swap the quoted
// value that follows.
func arrayKeyPre(key string) string {
	return `['"]` + regexp.QuoteMeta(key) + `['"]\s*=>\s*`
}

// replaceArrayKeyIn replaces the value of the FIRST `'key' => '...'` within block,
// mirroring phpArrayKey's read. Returns block unchanged when the key is absent.
func replaceArrayKeyIn(block, key, value string) string {
	out, ok := replaceQuotedAfter(block, arrayKeyPre(key), value)
	if !ok {
		logx.Debug("replaceArrayKeyIn: key %q not found in the bounded block (%d bytes); left unwritten (fail-closed) — read-after-write verify will surface it", key, len(block))
	}
	return out
}

// rewriteDrupal rewrites the DEFAULT connection in a Drupal settings.php:
// 'database'/'username'/'password' inside $databases['default']['default']. It
// edits the SAME block parseDrupal reads — the LAST ['default']['default']
// assignment (the live connection the installer appends after any commented
// example), or the historical any-$databases block as a fallback (Drupal 7's
// nested form). 'host'/'prefix' are left untouched.
func rewriteDrupal(content, dbName, dbUser, dbPassword string) string {
	// Edit the SAME string-aware block the parser/certifier read (drupalBlockSpan ->
	// matchingArrayClose), so a value containing `);`/`];` cannot truncate the bounded
	// block at an inner bracket inside a string literal (S1-01). The read-after-write
	// reparse (parseDrupal, now identically bounded) then confirms the value landed.
	bs, be, ok := drupalBlockSpan(content)
	if !ok {
		logx.Debug("rewriteDrupal: no $databases['default']['default'] (or any $databases) block found; config left unchanged (verify will fail)")
		return content
	}
	block := content[bs:be]
	if dbName != "" {
		block = replaceArrayKeyIn(block, "database", dbName)
	}
	if dbUser != "" {
		block = replaceArrayKeyIn(block, "username", dbUser)
	}
	if dbPassword != "" {
		block = replaceArrayKeyIn(block, "password", dbPassword)
	}
	return content[:bs] + block + content[be:]
}

// rewriteMagento rewrites a Magento app/etc/env.php: dbname/username/password
// inside the db 'connection'=>'default' block (scoped exactly like
// parseMagentoEnv via arrayBlockBounds, so a password/host from the queue/cache
// section — before OR after the block — is not touched). host and table_prefix are
// left untouched. The edit is confined to the bounded 'default' sub-array
// (content[start:end]); the prefix and the tail after the block are re-appended
// verbatim. If a key is absent inside the block, replaceArrayKeyIn leaves the block
// unchanged (fail-closed) rather than clobbering a later section, and the
// read-after-write reparse in rewriteVia then surfaces the unset credential.
func rewriteMagento(content, dbName, dbUser, dbPassword string) string {
	start, end, ok := arrayBlockBounds(content, "connection", "default")
	if !ok {
		logx.Debug("rewriteMagento: 'connection'->'default' db block not found/unterminated; config left unchanged (verify will fail)")
		return content
	}
	block := content[start:end]
	if dbName != "" {
		block = replaceArrayKeyIn(block, "dbname", dbName)
	}
	if dbUser != "" {
		block = replaceArrayKeyIn(block, "username", dbUser)
	}
	if dbPassword != "" {
		block = replaceArrayKeyIn(block, "password", dbPassword)
	}
	return content[:start] + block + content[end:]
}

// cfgPropPre is the WRITE-side mirror of parseMoodle's read prefix for a
// `$CFG->prop =` assignment.
func cfgPropPre(prop string) string {
	return `\$CFG->` + regexp.QuoteMeta(prop) + `\s*=\s*`
}

// definePre is the WRITE-side mirror of phpDefine's read prefix for a
// `define('NAME', ...)` constant.
func definePre(name string) string {
	return `define\(\s*['"]` + regexp.QuoteMeta(name) + `['"]\s*,\s*`
}

// rewritePrestaShop rewrites a PrestaShop config: define('_DB_NAME_'/'_DB_USER_'/
// '_DB_PASSWD_'). _DB_SERVER_ and _DB_PREFIX_ are left untouched.
func rewritePrestaShop(content, dbName, dbUser, dbPassword string) string {
	out := content
	if dbName != "" {
		out, _ = replaceQuotedAfter(out, definePre("_DB_NAME_"), dbName)
	}
	if dbUser != "" {
		out, _ = replaceQuotedAfter(out, definePre("_DB_USER_"), dbUser)
	}
	if dbPassword != "" {
		out, _ = replaceQuotedAfter(out, definePre("_DB_PASSWD_"), dbPassword)
	}
	return out
}

// rewriteOpenCart rewrites an OpenCart/AbanteCart config.php:
// define('DB_DATABASE'/'DB_USERNAME'/'DB_PASSWORD'). DB_HOSTNAME and DB_PREFIX are
// left untouched.
func rewriteOpenCart(content, dbName, dbUser, dbPassword string) string {
	out := content
	if dbName != "" {
		out, _ = replaceQuotedAfter(out, definePre("DB_DATABASE"), dbName)
	}
	if dbUser != "" {
		out, _ = replaceQuotedAfter(out, definePre("DB_USERNAME"), dbUser)
	}
	if dbPassword != "" {
		out, _ = replaceQuotedAfter(out, definePre("DB_PASSWORD"), dbPassword)
	}
	return out
}

// rewriteDotEnv rewrites a Laravel .env: DB_DATABASE/DB_USERNAME/DB_PASSWORD (the
// DB_HOST is left untouched). The value is single-quoted: phpdotenv treats a
// single-quoted value as a LITERAL (no ${VAR} interpolation and no escapes), so a
// password containing '$' or spaces is preserved, and the read side (dotEnvValue)
// recovers it by trimming the surrounding quotes.
func rewriteDotEnv(content, dbName, dbUser, dbPassword string) string {
	out := content
	if dbName != "" {
		out = setDotEnv(out, "DB_DATABASE", dbName)
	}
	if dbUser != "" {
		out = setDotEnv(out, "DB_USERNAME", dbUser)
	}
	if dbPassword != "" {
		out = setDotEnv(out, "DB_PASSWORD", dbPassword)
	}
	return out
}

// setDotEnv replaces the value of the LAST `KEY=` assignment — the one phpdotenv
// binds — with value, single-quoted. It locates the assignment with the same
// tokenizer dotEnvValue reads (dotEnvScan: `export `-aware, quote/multi-line-aware),
// so the rewrite edits exactly the occurrence the runtime uses and the read-after-write
// reparse agrees. Returns content unchanged if the key is absent or the .env is
// malformed (an unterminated quote), which then surfaces as a rewrite-verify failure.
func setDotEnv(content, key, value string) string {
	assigns, ok := dotEnvScan(content)
	if !ok {
		return content
	}
	idx := -1
	for i, a := range assigns {
		if a.key == key {
			idx = i // last assignment wins
		}
	}
	if idx < 0 {
		return content
	}
	a := assigns[idx]
	return content[:a.valStart] + dotEnvQuote(value) + content[a.valEnd:]
}

// dotEnvQuote renders value as a phpdotenv scalar that reads back AS value. Single
// quotes are a pure literal — no escapes, no $-interpolation — but cannot contain a
// single quote; double quotes can hold a single quote but reinterpret \ " and $. So:
//   - no single quote  -> single-quote it (covers anything, incl. $ and \);
//   - has a single quote but none of $ \ " or a newline -> double-quote it (covers
//     e.g. O'Brien, which a bare single-quote would break);
//   - has a single quote AND one of those -> no safe phpdotenv form exists, so
//     single-quote it and let RewriteSiteConfig's reparse REJECT the broken result
//     (dotEnvValue reads it the way PHP would) instead of shipping a wrong password.
func dotEnvQuote(value string) string {
	if !strings.ContainsRune(value, '\'') {
		return "'" + value + "'"
	}
	if !strings.ContainsAny(value, "$\\\"\n\r") {
		return `"` + value + `"`
	}
	return "'" + value + "'"
}

// rewriteMoodle rewrites a Moodle config.php: $CFG->dbname/dbuser/dbpass.
// $CFG->dbhost and $CFG->prefix are left untouched.
func rewriteMoodle(content, dbName, dbUser, dbPassword string) string {
	out := content
	if dbName != "" {
		out, _ = replaceQuotedAfter(out, cfgPropPre("dbname"), dbName)
	}
	if dbUser != "" {
		out, _ = replaceQuotedAfter(out, cfgPropPre("dbuser"), dbUser)
	}
	if dbPassword != "" {
		out, _ = replaceQuotedAfter(out, cfgPropPre("dbpass"), dbPassword)
	}
	return out
}
