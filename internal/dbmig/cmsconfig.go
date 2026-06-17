package dbmig

import (
	"regexp"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/wpconfig"
)

// This file recognizes the database credentials of the common database-backed
// PHP applications, so the migration finds them regardless of which CMS a site
// runs. Two facts shape the design:
//
//   - Several apps share a config FILENAME (config.php is used by Moodle,
//     SuiteCRM, OpenCart, phpBB, MyBB(inc/), Dolibarr(conf/) …; settings.php by
//     Drupal and WordPress-internals; Settings.php by SMF). So we cannot pick a
//     parser by filename alone.
//   - Each app's credentials sit in a DISTINCTIVE variable/array shape (e.g.
//     $sugar_config for SuiteCRM, $g_database_name for MantisBT, $CFG->dbname for
//     Moodle). So we try a set of parsers and accept the FIRST that yields a
//     non-empty database name. The distinctive shapes make false matches
//     effectively impossible.
//
// DokuWiki is intentionally absent: it stores data in flat files, no database.

// cmsConfigNames are the configuration filenames searched under each docroot.
// Order is by specificity (distinctive names first) only for readability; the
// actual parser choice is content-based (see parseCMSConfig). Generic names
// (config.php, settings.php) are included because many apps use them.
var cmsConfigNames = []string{
	"wp-config.php",          // WordPress
	"configuration.php",      // Joomla, Chamilo (older)
	"LocalConfiguration.php", // TYPO3 (<=v9 / classic)
	"LocalSettings.php",      // MediaWiki
	"config_inc.php",         // MantisBT
	"config.inc.php",         // Coppermine, phpMyAdmin-style
	"database.inc.php",       // Piwigo
	"conf.php",               // Dolibarr
	"env.php",                // Magento (app/etc/env.php)
	"config.ini.php",         // Matomo
	"database.php",           // Concrete CMS, Laravel(config/)
	"global.inc.php",         // CubeCart
	"settings.php",           // Drupal (also WP-internal false positives)
	"Settings.php",           // SMF (capital S)
	".env",                   // Laravel / modern apps
	"config.php",             // Moodle, SuiteCRM, OpenCart, phpBB, MyBB(inc/), generic
}

// Kind identifies which CMS/application a config file belongs to. It is threaded
// from discovery (parseCMSConfig) through the plan (SiteCreds, DBConfigRef) to the
// destination rewrite, so RewriteSiteConfig can dispatch to the matching writer
// instead of assuming WordPress. The zero value KindUnknown means "no recognized
// format". The string values are human-facing (logs / the manual-action report).
type Kind string

const (
	KindUnknown    Kind = ""
	KindWordPress  Kind = "WordPress"
	KindJoomla     Kind = "Joomla"
	KindDrupal     Kind = "Drupal"
	KindMoodle     Kind = "Moodle"
	KindMagento    Kind = "Magento"
	KindPrestaShop Kind = "PrestaShop"
	KindOpenCart   Kind = "OpenCart"
	KindLaravel    Kind = "Laravel"
	KindSuiteCRM   Kind = "SuiteCRM"
	KindMantisBT   Kind = "MantisBT"
	KindDolibarr   Kind = "Dolibarr"
	KindMyBB       Kind = "MyBB"
	KindphpBB      Kind = "phpBB"
	KindSMF        Kind = "SMF"
	KindChamilo    Kind = "Chamilo"
	KindPiwigo     Kind = "Piwigo"
	KindCoppermine Kind = "Coppermine"
	KindMediaWiki  Kind = "MediaWiki"
	KindNextcloud  Kind = "Nextcloud"
	KindTYPO3      Kind = "TYPO3"
	KindConcrete   Kind = "Concrete"
	KindCubeCart   Kind = "CubeCart"
	KindLimeSurvey Kind = "LimeSurvey"
	KindMatomo     Kind = "Matomo"
)

// configParser tries to extract DB credentials in one app's format. It returns
// DBName == "" when the content is not in that format.
type configParser func(content string) wpconfig.Creds

// cmsParser pairs a format parser with the Kind it recognizes.
type cmsParser struct {
	kind  Kind
	parse configParser
}

// allParsers is the ordered list of format parsers. The first to yield a
// non-empty DBName wins. Distinctive formats come before generic ones to avoid a
// generic parser claiming a file that a specific parser would read more fully.
var allParsers = []cmsParser{
	{KindWordPress, parseWordPress},   // define('DB_NAME', …)            distinctive constants
	{KindSuiteCRM, parseSuiteCRM},     // $sugar_config['dbconfig'][…]
	{KindMantisBT, parseMantisBT},     // $g_database_name / $g_db_*
	{KindMoodle, parseMoodle},         // $CFG->dbname / dbuser / dbpass
	{KindDolibarr, parseDolibarr},     // $dolibarr_main_db_*
	{KindMyBB, parseMyBB},             // $config['database'][…]
	{KindphpBB, parsephpBB},           // $dbname/$dbuser/$dbpasswd/$table_prefix
	{KindSMF, parseSMF},               // $db_name/$db_user/$db_passwd
	{KindChamilo, parseChamilo},       // $_configuration['main_database'] / db_user
	{KindPiwigo, parsePiwigo},         // $conf['db_base'/'db_user'/'db_password']
	{KindCoppermine, parseCoppermine}, // $CONFIG['dbname'/'dbuser'/'dbpass']
	{KindMediaWiki, parseMediaWiki},   // $wgDBname / $wgDBuser / $wgDBpassword
	{KindJoomla, parseJoomla},         // public $db / $user / $password
	{KindDrupal, parseDrupal},         // $databases['default']['default']
	{KindNextcloud, parseNextcloud},   // 'dbname' => , 'dbuser' => (CONFIG array)
	{KindMagento, parseMagentoEnv},    // 'connection'=>['default'=>['dbname'=>…]]
	{KindTYPO3, parseTYPO3},           // ['DB']['Connections']['Default']['dbname']
	{KindConcrete, parseConcrete},     // 'databases'=>[…]['database'/'username']
	{KindPrestaShop, parsePrestaShop}, // define('_DB_NAME_', …)
	{KindOpenCart, parseOpenCart},     // define('DB_DATABASE', …)  (also AbanteCart)
	{KindCubeCart, parseCubeCart},     // $glob['dbdatabase'/'dbusername']
	{KindLimeSurvey, parseLimeSurvey}, // 'connectionString'=>'mysql:…dbname=…','username'=>
	{KindMatomo, parseMatomoINI},      // [database] dbname = …  (INI, not PHP)
	{KindLaravel, parseDotEnv},        // DB_DATABASE= (Laravel)
}

// parseCMSConfig tries every known format and returns the first that recognizes
// the content (non-empty DBName), along with its Kind. filename is accepted for
// API symmetry and future filename-specific tie-breaks, but selection is
// content-based. Pure.
func parseCMSConfig(filename, content string) (wpconfig.Creds, Kind) {
	for _, p := range allParsers {
		if c := p.parse(content); c.DBName != "" {
			logx.Debug("parseCMSConfig %s: matched %s (%s)", filename, p.kind, c.String())
			return c, p.kind
		}
	}
	logx.Debug("parseCMSConfig %s: no recognized format", filename)
	return wpconfig.Creds{}, KindUnknown
}

// ---------------- distinctive PHP variable / array helpers ----------------
//
// Each helper extracts a value and PHP-unescapes it (so a value CONTAINING an
// escaped quote/backslash is read in full). RE2 has no backreferences, so every
// helper tries a single-quoted then a double-quoted form and uses whichever
// matches — the same approach as wpconfig.Parse.

// phpAssign reads `$name = '...'` / "..."` (optionally preceded by a visibility
// keyword), unescaped. "" if absent.
func phpAssign(name, content string) string {
	q := regexp.QuoteMeta(name)
	pre := `(?:public|protected|private|var)?\s*\$` + q + `\s*=\s*`
	return extractQuoted(content, pre, `\s*;`)
}

// phpArrayKey reads `'key' => '...'` / "..."`, unescaped. "" if absent.
func phpArrayKey(key, content string) string {
	pre := `['"]` + regexp.QuoteMeta(key) + `['"]\s*=>\s*`
	return extractQuoted(content, pre, ``)
}

// blockAfter scopes a credential lookup to a specific nested sub-array. It walks
// the given array keys in sequence (each searched only WITHIN the block the
// previous key opened) and returns the BODY of the LAST key's array — the text between
// its `[`/`array(` opener and the matching `]`/`)` closer. Bounding BOTH ends is
// what stops a first-match key lookup (phpArrayKey) from picking up an UNRELATED
// component's 'password'/'username'/'host' — a cache, AMQP/queue, mail, or
// secondary-connection block in a large nested config (Magento env.php, TYPO3
// settings.php, Concrete database.php) — whether that block appears BEFORE the
// target (excluded by the start bound) or AFTER it when the target omits the key
// (excluded by the end bound: an unbounded suffix would otherwise leak the later
// section's value). Requiring the key to introduce an ARRAY also skips a
// same-named scalar decoy (e.g. Concrete's `'databases' => 'x'`). Returns "" if
// any key is missing or the array is never closed (fail-closed). Mirrors
// parseDrupal's block scoping for the deep `return [...]` configs.
func blockAfter(content string, keys ...string) string {
	start, end, ok := arrayBlockBounds(content, keys...)
	if !ok {
		return ""
	}
	return content[start:end]
}

// arrayBlockBounds walks keys to the LAST key's array and returns the byte span
// [start,end) of that array's BODY in content: start just past the opener, end at
// the matching closer. The FIRST key is matched leftmost anywhere (the walk starts
// mid-tree: the outer wrapper key — e.g. Magento 'db' — is intentionally not
// listed). Every SUBSEQUENT key must be a DIRECT child (depth 0) of the block the
// previous key opened, and each level is bounded at BOTH ends via matchingArrayClose.
// So an intermediate key can latch neither a same-named SIBLING block after the
// current block's close (e.g. a later 'cache'=>'default' when 'connection' omits
// 'default') NOR a same-named block nested DEEPER inside a sibling sub-array (e.g.
// 'connection'=>['indexer'=>['default'=>...], 'default'=>...] must pick the direct
// child, not indexer's). ok is false if any key is missing as a direct child of its
// parent block or any array is unterminated (fail-closed). The walk and the bracket
// scan run on a comment-stripped copy (wpconfig.StripComments, which preserves byte
// offsets) so a commented-out decoy key is never selected and a bracket inside a
// comment never miscounts nesting; because offsets are preserved, the returned span
// also indexes the ORIGINAL content, which the in-place rewriter (rewriteMagento)
// relies on.
func arrayBlockBounds(content string, keys ...string) (start, end int, ok bool) {
	stripped := wpconfig.StripComments(content)
	lo, hi := 0, len(stripped)
	for i, k := range keys {
		var bodyStart int
		if i == 0 {
			re := regexp.MustCompile(`['"]` + regexp.QuoteMeta(k) + `['"]\s*=>\s*(?:\[|array\s*\()`)
			loc := re.FindStringIndex(stripped[lo:hi])
			if loc == nil {
				return 0, 0, false
			}
			bodyStart = lo + loc[1] // just past this key's `[` or `(` opener
		} else {
			bs, found := childArrayKeyOpen(stripped, lo, hi, k)
			if !found {
				return 0, 0, false
			}
			bodyStart = bs
		}
		opener := stripped[bodyStart-1] // '[' or '('
		closer := byte(']')
		if opener == '(' {
			closer = ')'
		}
		bodyEnd := matchingArrayClose(stripped, bodyStart, opener, closer)
		if bodyEnd < 0 {
			return 0, 0, false
		}
		// Bound the NEXT key's direct-child search to THIS block's body.
		lo, hi = bodyStart, bodyEnd
	}
	return lo, hi, true
}

// childArrayKeyOpen finds `'key' => [`/`array(` whose key token sits at the TOP
// level (depth 0) of s[lo:hi] — i.e. a DIRECT child of the current block — and
// returns the index just past its opener. A same-named key nested inside a sibling
// sub-array (depth > 0) is skipped, so the walk cannot latch a decoy block deeper in
// the window; a same-named key with a SCALAR value (no `[`/`array(` after `=>`) is
// also skipped. Bracket counting is string-aware: a `[`/`(`/`]`/`)` inside a quoted
// value does not change depth (the whole literal is skipped before its brackets are
// seen). Returns ok=false if no direct-child array match exists (fail-closed). s must
// be comment-stripped (matching arrayBlockBounds).
func childArrayKeyOpen(s string, lo, hi int, key string) (bodyStart int, ok bool) {
	re := regexp.MustCompile(`^['"]` + regexp.QuoteMeta(key) + `['"]\s*=>\s*(?:\[|array\s*\()`)
	depth := 0
	for i := lo; i < hi; i++ {
		switch s[i] {
		case '[', '(':
			depth++
		case ']', ')':
			if depth > 0 {
				depth--
			}
		case '\'', '"':
			if depth == 0 {
				if loc := re.FindStringIndex(s[i:hi]); loc != nil {
					return i + loc[1], true
				}
			}
			i = skipPHPStringLiteral(s, i) // skip the whole literal (and any brackets within)
		}
	}
	return 0, false
}

// matchingArrayClose returns the index in s of the closer that balances an array
// whose opener sits at body-1 (so s[body] is the first byte inside it), or -1 if
// it is never closed. It counts nested openers/closers of the SAME family (a PHP
// array is delimited by `[`…`]` or `array(`…`)`, and each family is independently
// balanced in valid source), skipping any bracket that sits inside a single- or
// double-quoted PHP string literal so a value like a DSN or a password containing
// `]`/`)` does not miscount. s must be comment-stripped, so comment bodies are
// already neutralized. (Heredoc/nowdoc bodies are not modeled — they do not occur
// in the machine-generated env.php/settings.php/database.php this reads; a stray
// bracket there fails closed, which is the safe direction.)
func matchingArrayClose(s string, body int, opener, closer byte) int {
	depth := 1
	for i := body; i < len(s); i++ {
		switch s[i] {
		case '\'', '"':
			i = skipPHPStringLiteral(s, i)
		case opener:
			depth++
		case closer:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// skipPHPStringLiteral returns the index of the closing quote of the PHP string
// literal whose opening quote is at s[i]. A backslash escapes the next byte (so an
// escaped quote does not end the literal), matching how StripComments and
// extractQuoted scan literals. Returns len(s)-1 on an unterminated literal so the
// caller's loop ends and the surrounding scan fails closed.
func skipPHPStringLiteral(s string, i int) int {
	q := s[i]
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++ // skip the escaped byte
		case q:
			return j
		}
	}
	return len(s) - 1
}

// phpDefine reads `define('NAME', '...')` / "...")`, unescaped. "" if absent.
func phpDefine(name, content string) string {
	pre := `define\(\s*['"]` + regexp.QuoteMeta(name) + `['"]\s*,\s*`
	return extractQuoted(content, pre, ``)
}

// firstGroup returns capture group 1 of the first match, or "". Used for the few
// values that are NOT a standalone quoted literal (e.g. a DB name embedded inside
// LimeSurvey's connectionString), where extractQuoted does not apply.
func firstGroup(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 { // len(nil) == 0, so this also covers no match
		return ""
	}
	return m[1]
}

// extractQuoted finds a single- or double-quoted PHP string literal preceded by
// the pre regex (and optionally followed by post), allowing PHP escape sequences
// inside, and returns its unescaped value. "" if neither form matches.
//
// Matching runs on a comment-stripped copy so a commented-out assignment (a stock
// example shipped above the live one, or a kept-for-reference old credential) is
// never read instead of the live value, and the leftmost of the two quote forms
// wins rather than always preferring single quotes - so a single-quoted decoy
// that follows the live double-quoted value can no longer shadow it.
func extractQuoted(content, pre, post string) string {
	stripped := wpconfig.StripComments(content)
	sq := regexp.MustCompile(pre + `'((?:\\.|[^'\\])*)'` + post)
	dq := regexp.MustCompile(pre + `"((?:\\.|[^"\\])*)"` + post)
	loc, quote := leftmostQuoted(sq.FindStringSubmatchIndex(stripped), "'",
		dq.FindStringSubmatchIndex(stripped), `"`)
	if loc == nil {
		return ""
	}
	return wpconfig.Unescape(stripped[loc[2]:loc[3]], quote)
}

// leftmostQuoted picks whichever of two FindStringSubmatchIndex results begins
// earlier in the text, with its quote style. Either may be nil; on a tie (not
// possible for two different patterns) the first/single-quoted form wins. It is
// the dbmig-side mirror of wpconfig's leftmost selection, shared by the CMS config
// read (extractQuoted) and write (replaceQuotedAfter) paths.
func leftmostQuoted(a []int, qa string, b []int, qb string) ([]int, string) {
	switch {
	case a == nil:
		return b, qb
	case b == nil:
		return a, qa
	case b[0] < a[0]:
		return b, qb
	default:
		return a, qa
	}
}

// ---------------- per-application parsers ----------------

// WordPress: define('DB_NAME','…') etc.
func parseWordPress(content string) wpconfig.Creds { return wpconfig.Parse(content) }

// Joomla configuration.php: public $db / $user / $password / $host / $dbprefix.
func parseJoomla(content string) wpconfig.Creds {
	c := wpconfig.Creds{
		DBName:      phpAssign("db", content),
		DBUser:      phpAssign("user", content),
		DBPassword:  phpAssign("password", content),
		DBHost:      phpAssign("host", content),
		TablePrefix: phpAssign("dbprefix", content),
	}
	// Guard: $user/$password are too generic; require the Joomla-specific $db AND
	// a JConfig marker, else let another parser claim it.
	if c.DBName != "" && strings.Contains(content, "JConfig") {
		return c
	}
	if c.DBName != "" && phpAssign("dbprefix", content) != "" {
		return c
	}
	return wpconfig.Creds{}
}

// Drupal settings.php: the DEFAULT connection $databases['default']['default'].
//
// Two things make a naive match read the WRONG credentials:
//   - default.settings.php (which the live settings.php is copied from) ships a
//     commented `@code` example — `$databases['default']['default'] = [ 'database'
//     => 'databasename', 'username' => 'sqlusername', … ];` — and the installer
//     APPENDS the real connection after it. So the FIRST match is the placeholder
//     example, never the real DB; only the LAST match is the live connection.
//   - a site may declare secondary connections ($databases['migrate']/['external']
//     /…), sometimes BEFORE the default one; matching any $databases[…] (the old
//     behavior) could grab a secondary connection's creds.
//
// So drupalDefault anchors to ['default']['default'] specifically and parseDrupal
// takes the LAST match. drupalAny is the permissive historical pattern, used only
// as a fallback when the anchored form is absent — chiefly Drupal 7, whose
// installer writes the fully-nested $databases = array('default' => array('default'
// => array(…))) form. (A D7 file still carrying the commented D8-style example
// matches that example via the anchored form, same as before; D7 is EOL.)
var (
	drupalDefault = regexp.MustCompile(`\$databases\s*\[\s*['"]default['"]\s*\]\s*\[\s*['"]default['"]\s*\]\s*=\s*(?:array\s*\(|\[)([\s\S]*?)(?:\)|\])\s*;`)
	drupalAny     = regexp.MustCompile(`\$databases\b[\s\S]*?(?:array\s*\(|\[)([\s\S]*?)(?:\)|\])\s*;`)
)

func parseDrupal(content string) wpconfig.Creds {
	// Bound the block with the SAME string-aware span the certifier/rewriter use
	// (drupalBlockSpan -> matchingArrayClose), NOT the drupalDefault/drupalAny regex's
	// own non-greedy `(?:\)|\])\s*;` end: that end truncates the block at a `);`/`];`
	// sitting inside a value string (e.g. a password "a);b"), which under-reads the
	// credentials (S1-01). The regex still LOCATES the block; its body capture is
	// discarded in favor of the string-aware close.
	s, e, ok := drupalBlockSpan(content)
	if !ok {
		return wpconfig.Creds{}
	}
	block := content[s:e]
	return wpconfig.Creds{
		DBName:      phpArrayKey("database", block),
		DBUser:      phpArrayKey("username", block),
		DBPassword:  phpArrayKey("password", block),
		DBHost:      phpArrayKey("host", block),
		TablePrefix: phpArrayKey("prefix", block),
	}
}

// Laravel .env: DB_DATABASE= etc.
func parseDotEnv(content string) wpconfig.Creds {
	return wpconfig.Creds{
		DBName:     dotEnvValue(content, "DB_DATABASE"),
		DBUser:     dotEnvValue(content, "DB_USERNAME"),
		DBPassword: dotEnvValue(content, "DB_PASSWORD"),
		DBHost:     dotEnvValue(content, "DB_HOST"),
	}
}

// dotEnvValue returns the value phpdotenv (createImmutable, which Laravel uses) would
// bind for key: the LAST assignment of the key, with an optional `export ` prefix
// stripped, located by a real tokenizer (dotEnvScan) that skips over quoted —
// including multi-line double-quoted — values so a KEY=-looking line INSIDE another
// value is never mistaken for an assignment. A malformed .env (unterminated quote)
// reads as not-found, so discovery never associates a half-parsed value.
func dotEnvValue(content, key string) string {
	assigns, ok := dotEnvScan(content)
	if !ok {
		return ""
	}
	raw, found := "", false
	for _, a := range assigns {
		if a.key == key {
			raw, found = a.rawValue, true // last assignment wins
		}
	}
	if !found {
		return ""
	}
	return dotEnvUnquote(strings.TrimSpace(raw))
}

// dotEnvAssign is one located KEY=VALUE assignment: rawValue is the value token as
// written (quotes included, surrounding whitespace trimmed) for dotEnvUnquote, and
// [valStart,valEnd) is its byte span (just after '=' to the end of the value token)
// so setDotEnv can replace exactly that.
type dotEnvAssign struct {
	key      string
	rawValue string
	valStart int
	valEnd   int
}

// dotEnvScan tokenizes a .env for the purpose of locating assignments the way
// phpdotenv parses them: it skips blank lines and `#` comments, accepts an optional
// `export ` prefix, takes the first `=` as the name/value separator, and — crucially
// — skips OVER a quoted value so content inside a value (e.g. a decoy `DB_DATABASE=`
// line embedded in a multi-line double-quoted secret) is never counted as an
// assignment. Quote handling mirrors phpdotenv: a DOUBLE-quoted value may span lines
// (and honours `\` escapes); a SINGLE-quoted value is single-line (an unterminated one
// is a parse error). Returns assignments in source order; ok is false if a quote is
// left unterminated (malformed: callers fail closed). It does not decode values (use
// dotEnvUnquote) nor resolve ${VAR}.
func dotEnvScan(content string) (assigns []dotEnvAssign, ok bool) {
	i, n := 0, len(content)
	isKeyChar := func(b byte) bool {
		return b == '_' || b == '.' ||
			(b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
	}
	nextLine := func(p int) int {
		for p < n && content[p] != '\n' {
			p++
		}
		if p < n {
			p++ // past the newline
		}
		return p
	}
	for i < n {
		j := i
		for j < n && (content[j] == ' ' || content[j] == '\t') {
			j++
		}
		// Blank line or comment.
		if j >= n || content[j] == '\n' || content[j] == '#' {
			i = nextLine(j)
			continue
		}
		// Optional `export ` (must be followed by whitespace, else it is a key name).
		k := j
		if strings.HasPrefix(content[k:], "export") && k+6 < n && (content[k+6] == ' ' || content[k+6] == '\t') {
			k += 6
			for k < n && (content[k] == ' ' || content[k] == '\t') {
				k++
			}
		}
		// Key, then optional whitespace, then '='.
		ks := k
		for k < n && isKeyChar(content[k]) {
			k++
		}
		key := content[ks:k]
		m := k
		for m < n && (content[m] == ' ' || content[m] == '\t') {
			m++
		}
		if key == "" || m >= n || content[m] != '=' {
			i = nextLine(j) // not an assignment line
			continue
		}
		m++ // past '='
		valStart := m
		// Locate the value extent. Skip leading whitespace to see if it is quoted.
		p := m
		for p < n && (content[p] == ' ' || content[p] == '\t') {
			p++
		}
		var valEnd int
		if p < n && (content[p] == '\'' || content[p] == '"') {
			quote := content[p]
			q := p + 1
			closed := false
			for q < n {
				c := content[q]
				if quote == '"' {
					if c == '\\' && q+1 < n {
						q += 2 // an escaped char inside double quotes cannot close the value
						continue
					}
				} else if c == '\n' {
					// phpdotenv multi-lines only DOUBLE-quoted values; a single-quoted value
					// is single-line, and an unterminated one is a parse error (it throws).
					break // leave closed=false -> malformed, fail closed
				}
				if c == quote {
					q++
					closed = true
					break
				}
				q++
			}
			if !closed {
				return assigns, false // unterminated quote -> malformed, fail closed
			}
			valEnd = q
		} else {
			// Unquoted: the value runs to end of line (dotEnvUnquote trims any inline comment).
			e := m
			for e < n && content[e] != '\n' {
				e++
			}
			valEnd = e
		}
		assigns = append(assigns, dotEnvAssign{
			key:      key,
			rawValue: strings.TrimSpace(content[valStart:valEnd]),
			valStart: valStart,
			valEnd:   valEnd,
		})
		i = nextLine(valEnd)
	}
	return assigns, true
}

// dotEnvUnquote decodes a single .env value (already trimmed of surrounding whitespace)
// with phpdotenv's quote semantics — NOT strings.Trim, which strips outer quotes
// indiscriminately and so reads 'a'b' as "a'b" while PHP reads "a". Mirroring PHP here is
// what lets the post-write reparse reject a value the dest would misread.
func dotEnvUnquote(raw string) string {
	if raw == "" {
		return ""
	}
	switch raw[0] {
	case '\'':
		// Single-quoted: a literal that ENDS at the next single quote (no escapes).
		if i := strings.IndexByte(raw[1:], '\''); i >= 0 {
			return raw[1 : 1+i]
		}
		return raw[1:]
	case '"':
		// Double-quoted: up to the next unescaped ", undoing \" and \\.
		return dotEnvUnquoteDouble(raw[1:])
	default:
		// Unquoted: up to whitespace or an inline comment.
		if i := strings.IndexAny(raw, " \t#"); i >= 0 {
			return raw[:i]
		}
		return raw
	}
}

// dotEnvUnquoteDouble returns the contents of a double-quoted .env value (s begins
// just after the opening quote): everything up to the next UNescaped ", with \" and
// \\ undone.
func dotEnvUnquoteDouble(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\') {
			b.WriteByte(s[i+1])
			i++
			continue
		}
		if s[i] == '"' {
			break // closing quote
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// PrestaShop: define('_DB_NAME_', …) etc.
func parsePrestaShop(content string) wpconfig.Creds {
	return wpconfig.Creds{
		DBName:      phpDefine("_DB_NAME_", content),
		DBUser:      phpDefine("_DB_USER_", content),
		DBPassword:  phpDefine("_DB_PASSWD_", content),
		DBHost:      phpDefine("_DB_SERVER_", content),
		TablePrefix: phpDefine("_DB_PREFIX_", content),
	}
}

// OpenCart & AbanteCart: define('DB_DATABASE', …) etc.
func parseOpenCart(content string) wpconfig.Creds {
	return wpconfig.Creds{
		DBName:      phpDefine("DB_DATABASE", content),
		DBUser:      phpDefine("DB_USERNAME", content),
		DBPassword:  phpDefine("DB_PASSWORD", content),
		DBHost:      phpDefine("DB_HOSTNAME", content),
		TablePrefix: phpDefine("DB_PREFIX", content),
	}
}

// MediaWiki LocalSettings.php: $wgDBname etc.
func parseMediaWiki(content string) wpconfig.Creds {
	return wpconfig.Creds{
		DBName:      phpAssign("wgDBname", content),
		DBUser:      phpAssign("wgDBuser", content),
		DBPassword:  phpAssign("wgDBpassword", content),
		DBHost:      phpAssign("wgDBserver", content),
		TablePrefix: phpAssign("wgDBprefix", content),
	}
}

// phpBB config.php: $dbname / $dbuser / $dbpasswd / $dbhost / $table_prefix.
func parsephpBB(content string) wpconfig.Creds {
	c := wpconfig.Creds{
		DBName:      phpAssign("dbname", content),
		DBUser:      phpAssign("dbuser", content),
		DBPassword:  phpAssign("dbpasswd", content),
		DBHost:      phpAssign("dbhost", content),
		TablePrefix: phpAssign("table_prefix", content),
	}
	// $dbpasswd is distinctive to phpBB-style; require it to avoid clashing with
	// Coppermine which uses $CONFIG[...] not bare $dbname.
	if c.DBName != "" && (c.DBPassword != "" || strings.Contains(content, "$dbpasswd")) {
		return c
	}
	return wpconfig.Creds{}
}

// MyBB inc/config.php: $config['database']['database'/'username'/'password'/'hostname'].
func parseMyBB(content string) wpconfig.Creds {
	dbCfg := func(key string) string {
		pre := `\$config\s*\[\s*['"]database['"]\s*\]\s*\[\s*['"]` +
			regexp.QuoteMeta(key) + `['"]\s*\]\s*=\s*`
		return extractQuoted(content, pre, ``)
	}
	return wpconfig.Creds{
		DBName:      dbCfg("database"),
		DBUser:      dbCfg("username"),
		DBPassword:  dbCfg("password"),
		DBHost:      dbCfg("hostname"),
		TablePrefix: dbCfg("table_prefix"),
	}
}

// SMF Settings.php: $db_name / $db_user / $db_passwd / $db_server / $db_prefix.
func parseSMF(content string) wpconfig.Creds {
	return wpconfig.Creds{
		DBName:      phpAssign("db_name", content),
		DBUser:      phpAssign("db_user", content),
		DBPassword:  phpAssign("db_passwd", content),
		DBHost:      phpAssign("db_server", content),
		TablePrefix: phpAssign("db_prefix", content),
	}
}

// Moodle config.php: $CFG->dbname / dbuser / dbpass / dbhost / prefix.
func parseMoodle(content string) wpconfig.Creds {
	cfg := func(prop string) string {
		return extractQuoted(content, `\$CFG->`+regexp.QuoteMeta(prop)+`\s*=\s*`, ``)
	}
	return wpconfig.Creds{
		DBName:      cfg("dbname"),
		DBUser:      cfg("dbuser"),
		DBPassword:  cfg("dbpass"),
		DBHost:      cfg("dbhost"),
		TablePrefix: cfg("prefix"),
	}
}

// Dolibarr conf.php: $dolibarr_main_db_name / _user / _pass / _host.
func parseDolibarr(content string) wpconfig.Creds {
	return wpconfig.Creds{
		DBName:     phpAssign("dolibarr_main_db_name", content),
		DBUser:     phpAssign("dolibarr_main_db_user", content),
		DBPassword: phpAssign("dolibarr_main_db_pass", content),
		DBHost:     phpAssign("dolibarr_main_db_host", content),
	}
}

// SuiteCRM config.php: $sugar_config['dbconfig']['db_name'/'db_user_name'/…].
func parseSuiteCRM(content string) wpconfig.Creds {
	dbc := func(key string) string { return phpArrayKey(key, content) }
	// Require the SuiteCRM marker so generic 'db_name' keys elsewhere don't match.
	if !strings.Contains(content, "dbconfig") {
		return wpconfig.Creds{}
	}
	return wpconfig.Creds{
		DBName:     dbc("db_name"),
		DBUser:     dbc("db_user_name"),
		DBPassword: dbc("db_password"),
		DBHost:     dbc("db_host_name"),
	}
}

// MantisBT config_inc.php: $g_database_name / $g_db_username / $g_db_password.
func parseMantisBT(content string) wpconfig.Creds {
	return wpconfig.Creds{
		DBName:     phpAssign("g_database_name", content),
		DBUser:     phpAssign("g_db_username", content),
		DBPassword: phpAssign("g_db_password", content),
		DBHost:     phpAssign("g_hostname", content),
	}
}

// Nextcloud config/config.php: a $CONFIG array with 'dbname'/'dbuser'/'dbpassword'.
func parseNextcloud(content string) wpconfig.Creds {
	if !strings.Contains(content, "$CONFIG") {
		return wpconfig.Creds{}
	}
	return wpconfig.Creds{
		DBName:      phpArrayKey("dbname", content),
		DBUser:      phpArrayKey("dbuser", content),
		DBPassword:  phpArrayKey("dbpassword", content),
		DBHost:      phpArrayKey("dbhost", content),
		TablePrefix: phpArrayKey("dbtableprefix", content),
	}
}

// Coppermine include/config.inc.php: $CONFIG['dbname'/'dbuser'/'dbpass'/'dbserver'].
func parseCoppermine(content string) wpconfig.Creds {
	cfg := func(key string) string {
		pre := `\$CONFIG\s*\[\s*['"]` + regexp.QuoteMeta(key) + `['"]\s*\]\s*=\s*`
		return extractQuoted(content, pre, ``)
	}
	c := wpconfig.Creds{
		DBName:      cfg("dbname"),
		DBUser:      cfg("dbuser"),
		DBPassword:  cfg("dbpass"),
		DBHost:      cfg("dbserver"),
		TablePrefix: cfg("table_prefix"),
	}
	return c
}

// Chamilo configuration.php: $_configuration['main_database'/'db_user'/'db_password'/'db_host'].
func parseChamilo(content string) wpconfig.Creds {
	cfg := func(key string) string {
		pre := `\$_configuration\s*\[\s*['"]` + regexp.QuoteMeta(key) + `['"]\s*\]\s*=\s*`
		return extractQuoted(content, pre, ``)
	}
	return wpconfig.Creds{
		DBName:     cfg("main_database"),
		DBUser:     cfg("db_user"),
		DBPassword: cfg("db_password"),
		DBHost:     cfg("db_host"),
	}
}

// Piwigo local/config/database.inc.php: $conf['db_base'/'db_user'/'db_password'/'db_host'].
func parsePiwigo(content string) wpconfig.Creds {
	cfg := func(key string) string {
		pre := `\$conf\s*\[\s*['"]` + regexp.QuoteMeta(key) + `['"]\s*\]\s*=\s*`
		return extractQuoted(content, pre, ``)
	}
	return wpconfig.Creds{
		DBName:      cfg("db_base"),
		DBUser:      cfg("db_user"),
		DBPassword:  cfg("db_password"),
		DBHost:      cfg("db_host"),
		TablePrefix: cfg("prefix"),
	}
}

// Magento app/etc/env.php: nested 'db'=>'connection'=>'default'=>['dbname'/'username'/'password'/'host'].
// The credential lookups are SCOPED to the 'default' connection block: env.php
// also carries 'password'/'host' under the queue (AMQP), cache, and crypt
// sections, which often precede the db section — a whole-file first match would
// otherwise attach a non-database credential. (table_prefix is Magento-unique to
// the db section, so it is safe to read from the whole file.)
func parseMagentoEnv(content string) wpconfig.Creds {
	block := blockAfter(content, "connection", "default")
	if block == "" {
		logx.Debug("parseMagentoEnv: 'connection'->'default' block not found or unterminated; not treating as Magento")
		return wpconfig.Creds{}
	}
	return wpconfig.Creds{
		DBName:      phpArrayKey("dbname", block),
		DBUser:      phpArrayKey("username", block),
		DBPassword:  phpArrayKey("password", block),
		DBHost:      phpArrayKey("host", block),
		TablePrefix: phpArrayKey("table_prefix", content),
	}
}

// TYPO3 LocalConfiguration.php / settings.php: ['DB']['Connections']['Default']['dbname'/'user'/'password'/'host'].
// Scoped to the 'Default' connection block so a 'password'/'host' from another
// section (e.g. MAIL/SMTP, or a secondary connection) is not mistaken for the DB's.
func parseTYPO3(content string) wpconfig.Creds {
	block := blockAfter(content, "Connections", "Default")
	if block == "" {
		logx.Debug("parseTYPO3: 'Connections'->'Default' block not found or unterminated; not treating as TYPO3")
		return wpconfig.Creds{}
	}
	return wpconfig.Creds{
		DBName:     phpArrayKey("dbname", block),
		DBUser:     phpArrayKey("user", block),
		DBPassword: phpArrayKey("password", block),
		DBHost:     phpArrayKey("host", block),
	}
}

// Concrete CMS application/config/database.php: 'databases'=>[<conn>=>['database'/'username'/'password'/'server']].
// Scoped to the 'databases' block so a 'password'/'server' from a cache/redis
// connection declared earlier (e.g. under 'connections') is not mistaken for the
// DB's. blockAfter requires `'databases' => [`, skipping a scalar decoy such as
// `'databases' => 'x'`.
func parseConcrete(content string) wpconfig.Creds {
	block := blockAfter(content, "databases")
	if block == "" {
		logx.Debug("parseConcrete: 'databases' block not found or unterminated; not treating as Concrete")
		return wpconfig.Creds{}
	}
	return wpconfig.Creds{
		DBName:     phpArrayKey("database", block),
		DBUser:     phpArrayKey("username", block),
		DBPassword: phpArrayKey("password", block),
		DBHost:     phpArrayKey("server", block),
	}
}

// CubeCart includes/global.inc.php: $glob['dbdatabase'/'dbusername'/'dbpassword'/'dbhost'].
func parseCubeCart(content string) wpconfig.Creds {
	cfg := func(key string) string {
		pre := `\$glob\s*\[\s*['"]` + regexp.QuoteMeta(key) + `['"]\s*\]\s*=\s*`
		return extractQuoted(content, pre, ``)
	}
	return wpconfig.Creds{
		DBName:      cfg("dbdatabase"),
		DBUser:      cfg("dbusername"),
		DBPassword:  cfg("dbpassword"),
		DBHost:      cfg("dbhost"),
		TablePrefix: cfg("dbprefix"),
	}
}

// LimeSurvey application/config/config.php: 'connectionString'=>'mysql:…dbname=NAME…',
// plus 'username'/'password' in the db component.
var limeDBName = regexp.MustCompile(`connectionString['"]\s*=>\s*['"][^'"]*dbname=([^;'"]+)`)

func parseLimeSurvey(content string) wpconfig.Creds {
	// Read the connectionString DSN on a COMMENT-STRIPPED copy so a commented-out
	// decoy connectionString is not read instead of the live DSN (S1-02 class). The
	// username/password/tablePrefix below already go through phpArrayKey -> extractQuoted,
	// which strips comments; only this DSN regex ran on raw content.
	name := firstGroup(limeDBName, wpconfig.StripComments(content))
	if name == "" {
		return wpconfig.Creds{}
	}
	return wpconfig.Creds{
		DBName:      name,
		DBUser:      phpArrayKey("username", content),
		DBPassword:  phpArrayKey("password", content),
		TablePrefix: phpArrayKey("tablePrefix", content),
	}
}

// Matomo config/config.ini.php: INI with a [database] section (NOT PHP array).
//
//	[database]
//	host = "localhost"
//	username = "u"
//	password = "p"
//	dbname = "d"
//	tables_prefix = "matomo_"
func parseMatomoINI(content string) wpconfig.Creds {
	sec := iniSection(content, "database")
	if sec == "" {
		return wpconfig.Creds{}
	}
	return wpconfig.Creds{
		DBName:      iniValue(sec, "dbname"),
		DBUser:      iniValue(sec, "username"),
		DBPassword:  iniValue(sec, "password"),
		DBHost:      iniValue(sec, "host"),
		TablePrefix: iniValue(sec, "tables_prefix"),
	}
}

// iniSection returns the body of the named [section] up to the next [section] or
// end of input. Returns "" if absent.
func iniSection(content, name string) string {
	re := regexp.MustCompile(`(?ms)^\s*\[` + regexp.QuoteMeta(name) + `\]\s*$(.*?)(?:^\s*\[[^\]]+\]\s*$|\z)`)
	m := re.FindStringSubmatch(content)
	if m == nil {
		return ""
	}
	return m[1]
}

// iniValue reads `key = "value"` or `key = value` from an INI section body.
func iniValue(section, key string) string {
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(key) + `\s*=\s*(.*)$`)
	m := re.FindStringSubmatch(section)
	if m == nil {
		return ""
	}
	return strings.Trim(strings.TrimSpace(m[1]), `"'`)
}
