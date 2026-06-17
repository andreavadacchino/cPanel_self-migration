package dbmig

import (
	"fmt"
	"regexp"

	"github.com/tis24dev/cPanel_self-migration/internal/wpconfig"
)

// ConfigAmbiguity is the structural, PHP-free second opinion on a rewritten config (see
// wpconfig.CheckDefineConstant / CheckQuotedCutover). For each kind whose credential shape
// it understands it checks every DB credential the cutover depends on and reports the first
// that is structurally ambiguous — i.e. the tool cannot prove the value the rewrite acted on
// is the value PHP's runtime would use (a constant/property defined more than once, a
// non-literal definition, a heredoc/HTML/comment/non-property decoy edited instead of the
// live one, or — for a .env — a key set on multiple lines where phpdotenv binds the last but
// the rewrite edits the first). covered is false for kinds whose credential shape is not yet
// understood (their verify is unchanged), so this only ever ADDS scrutiny, never removes it.
//
// It is independent of any runtime oracle (no PHP, no DB connection) and so is always
// available as the baseline V35 check; a future oracle can promote an ambiguous result to
// verified, but this layer alone never certifies an ambiguous cutover green.
//
// Being PHP-free and single-file, it does NOT follow include()/require() nor resolve runtime
// expressions/control flow (getenv(), a constructor reassignment) — those residuals need the
// runtime PHP-eval oracle (a later phase); they are documented, not silently swallowed.
func ConfigAmbiguity(kind Kind, content string) (reason string, ambiguous, covered bool) {
	switch kind {
	case KindWordPress:
		return defineAmbiguity(content, "DB_NAME", "DB_USER", "DB_PASSWORD")
	case KindPrestaShop:
		return defineAmbiguity(content, "_DB_NAME_", "_DB_USER_", "_DB_PASSWD_")
	case KindOpenCart:
		return defineAmbiguity(content, "DB_DATABASE", "DB_USERNAME", "DB_PASSWORD")
	case KindJoomla:
		// public $db / $user / $password: the credential is a JConfig CLASS PROPERTY, which
		// is what `new JConfig` binds. The certifier anchor REQUIRES the visibility keyword
		// (and tolerates a 7.4+ type) so it reads the property — NOT a bare top-level
		// `$db = …` decoy the rewriter (assignPre's visibility is OPTIONAL, leftmost) might
		// have edited instead. blindVal is what the rewriter actually targeted; a mismatch, or
		// a second matching property (another class's `$db` — requireUnique), is flagged.
		return assignAmbiguity(content, wpconfig.BindFirst, true, []credCheck{
			{joomlaPropAnchor("db"), phpAssign("db", content), "Joomla $db"},
			{joomlaPropAnchor("user"), phpAssign("user", content), "Joomla $user"},
			{joomlaPropAnchor("password"), phpAssign("password", content), "Joomla $password"},
		})
	case KindMoodle:
		// $CFG->dbname / dbuser / dbpass: object property — a later assignment OVERWRITES, so
		// PHP binds the LAST; the rewriter edits the leftmost. A duplicate with diverging
		// values is the divergence this catches (a same-value duplicate stays clean).
		return assignAmbiguity(content, wpconfig.BindLast, false, []credCheck{
			{regexp.MustCompile(cfgPropPre("dbname")), moodleCfgValue(content, "dbname"), "Moodle $CFG->dbname"},
			{regexp.MustCompile(cfgPropPre("dbuser")), moodleCfgValue(content, "dbuser"), "Moodle $CFG->dbuser"},
			{regexp.MustCompile(cfgPropPre("dbpass")), moodleCfgValue(content, "dbpass"), "Moodle $CFG->dbpass"},
		})
	case KindLaravel:
		// The Laravel .env parser/rewriter (dotEnvValue / setDotEnv) now locate the SAME
		// occurrence phpdotenv binds — the LAST `export `-aware assignment, found by a
		// tokenizer that skips over quoted/multi-line values — so dimension 1's re-read
		// reads exactly what the runtime uses. There is no line-vs-runtime blind spot left
		// for a structural second opinion to add (a duplicate is correctly rewritten on its
		// bound line; a malformed .env fails dimension 1 outright). Covered, never ambiguous.
		return "", false, true
	case KindDrupal:
		// $databases['default']['default'][...] : an array block. The rewriter finds the
		// block on RAW content (the LAST ['default']['default'], comments/heredoc/HTML/string
		// included) and edits the FIRST of each key. PHP runs only executable code and a
		// duplicate array key keeps the LAST.
		return arrayKindAmbiguity(content, parseDrupal, drupalBlockSpan,
			[]string{"database", "username", "password"}, "Drupal default-connection")
	case KindMagento:
		// app/etc/env.php 'db'->'connection'->'default'[...] : same array-block shape; the
		// bound array-block scan (arrayBlockBounds) does not model heredocs, so a stray
		// bracket in a heredoc can mis-bound the block — the masked parse catches it.
		return arrayKindAmbiguity(content, parseMagentoEnv, magentoBlockSpan,
			[]string{"dbname", "username", "password"}, "Magento db connection")
	default:
		return "", false, false
	}
}

// arrayKindAmbiguity is the array/block-kind certifier (Drupal settings.php, Magento
// env.php). It catches three structural divergences without executing PHP:
//
//   - NON-CODE block: the rewriter selects its array block on RAW content (so it can pick a
//     block sitting inside a heredoc, inline-HTML, comment, OR a regular string literal),
//     while PHP runs only executable code. If the selected block's body is entirely
//     whitespace once string/heredoc/comment/HTML bodies are blanked, the rewrite edited a
//     non-code decoy.
//   - VALUE divergence: a raw parse vs a heredoc/HTML/comment-masked parse read a different
//     value (e.g. a heredoc that mis-bounds the bound array-block scan).
//   - DUPLICATE key: a PHP array literal keeps the LAST of a repeated key, but the rewriter
//     (phpArrayKey/replaceArrayKeyIn) reads and edits the FIRST.
//
// parse is the kind's parser; blockSpan returns the byte span [start,end) of the bound
// block's BODY in the given content (used on raw for the non-code check and on masked for
// the duplicate-key scan); keys are the credential keys to duplicate-check.
func arrayKindAmbiguity(content string, parse func(string) wpconfig.Creds, blockSpan func(string) (int, int, bool), keys []string, label string) (string, bool, bool) {
	// 1) The rewriter's selected block (on raw content) must be executable code, not a block
	//    that lives inside a string/heredoc/comment/HTML the site never runs.
	if s, e, ok := blockSpan(content); ok && blockInNonCode(content, s, e) {
		return label + " block was edited inside a string/heredoc/comment, not executable code", true, true
	}
	// 2) The block's resolved value must be the same with heredoc/HTML/comment blanked.
	masked := wpconfig.MaskNonCode(content)
	blind := parse(content)
	aware := parse(masked)
	if blind.DBName != aware.DBName || blind.DBUser != aware.DBUser || blind.DBPassword != aware.DBPassword {
		return label + " resolves differently in executable code than the rewrite targeted (a decoy block was edited instead of the live one)", true, true
	}
	// 3) No duplicate key in the live block where PHP keeps the LAST but the rewrite the FIRST.
	//    blockStrip is the SAME block with regular string BODIES additionally blanked (offsets
	//    preserved), so arrayKeyDupDivergence can tell a real array key (its opening quote is a
	//    structural delimiter, preserved) from the literal text `'key' =>` sitting INSIDE a
	//    value string (its bytes are blanked) and not over-count the latter as a duplicate.
	if s, e, ok := blockSpan(masked); ok {
		block := masked[s:e]
		strip := wpconfig.MaskNonCodeAndStrings(content)
		blockStrip := strip[s:e]
		for _, k := range keys {
			if r, amb := arrayKeyDupDivergence(block, blockStrip, k, label); amb {
				return r, true, true
			}
		}
	}
	return "", false, true
}

// blockInNonCode reports whether content[start:end) is entirely whitespace once all
// non-executable text (comments, heredoc/nowdoc bodies, inline-HTML, AND regular
// string-literal bodies) is blanked — i.e. the array block lives inside a string/heredoc/
// comment and is never run. A real code block keeps its structural bytes (quotes, `=>`,
// commas, brackets), so a single non-space byte means it is executable.
func blockInNonCode(content string, start, end int) bool {
	if end <= start {
		return false
	}
	masked := wpconfig.MaskNonCodeAndStrings(content)
	if end > len(masked) {
		end = len(masked)
	}
	for i := start; i < end; i++ {
		switch masked[i] {
		case ' ', '\t', '\n', '\r':
		default:
			return false
		}
	}
	return true
}

// arrayKeyAnchor matches `'key' =>` up to the value (the WRITE-side mirror of phpArrayKey).
// The key NAME is itself a string literal, so this anchor cannot run on the string-blanking
// mask (which would blank the key name); arrayKeyDupDivergence scans the strings-INTACT block
// and discriminates real keys from in-string text via blockStrip instead.
func arrayKeyAnchor(key string) *regexp.Regexp {
	return regexp.MustCompile(`['"]` + regexp.QuoteMeta(key) + `['"]\s*=>\s*`)
}

// arrayKeyDupDivergence flags a key that appears more than once as a REAL array element in the
// live block with diverging FIRST vs LAST literal values: a PHP array literal keeps the LAST,
// while the rewriter (phpArrayKey / replaceArrayKeyIn) reads and edits the FIRST. A non-literal
// last value reads "" and so also diverges from a literal first (correctly: PHP resolves it at
// runtime, the rewrite cannot prove it).
//
// block is the strings-INTACT view (heredoc/HTML/comment blanked) — values are read from it.
// blockStrip is the SAME span with regular string BODIES additionally blanked (offsets aligned
// with block). The anchor is matched on block (so the key NAME, itself a string literal, is
// visible), but an occurrence is only counted when its opening-quote byte is PRESERVED in
// blockStrip — i.e. it is a structural array-key delimiter, not the literal text `'key' =>`
// sitting inside a value string (whose bytes are blanked). Without this filter, a site admin's
// comment value like "legacy 'dbname' => 'old_db'" would be miscounted as a second key and a
// correctly-rewritten config FALSELY flagged (a release-blocking false-DIFF under --deep).
//
// It counts the key at ANY nesting depth inside the block (it does not re-scope to the
// connection's top level), so a same-named key in a nested sub-array (e.g. a 'database' under
// an 'opts' array) over-counts — a conservative SOFT over-flag, never a false-OK, and not seen
// in machine-generated Drupal/Magento configs which list each credential key once.
func arrayKeyDupDivergence(block, blockStrip, key, label string) (string, bool) {
	all := arrayKeyAnchor(key).FindAllStringIndex(block, -1)
	var locs [][]int
	for _, l := range all {
		// l[0] is the key's opening quote. A real key delimiter survives string-blanking;
		// an in-string occurrence has its quote blanked to a space.
		if l[0] < len(blockStrip) && blockStrip[l[0]] != ' ' {
			locs = append(locs, l)
		}
	}
	if len(locs) <= 1 {
		return "", false
	}
	first := readArrayLiteral(block, locs[0][1])
	last := readArrayLiteral(block, locs[len(locs)-1][1])
	if first != last {
		return fmt.Sprintf("%s '%s' appears %d times in the block; PHP keeps the LAST but the rewrite edits the FIRST", label, key, len(locs)), true
	}
	return "", false
}

// readArrayLiteral reads the quoted string literal at s[pos:] after skipping whitespace,
// PHP-unescaped; "" if the value is not a quoted literal (a runtime expression).
func readArrayLiteral(s string, pos int) string {
	i := pos
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	if i >= len(s) || (s[i] != '\'' && s[i] != '"') {
		return ""
	}
	q := s[i]
	i++
	var sb []byte
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) {
			sb = append(sb, s[i], s[i+1])
			i += 2
			continue
		}
		if s[i] == q {
			break
		}
		sb = append(sb, s[i])
		i++
	}
	return wpconfig.Unescape(string(sb), string(q))
}

// drupalBlockSpan returns the byte span [start,end) of the BODY of the block
// parseDrupal/rewriteDrupal select — the LAST $databases['default']['default'] match (the
// live connection appended after any example), or the drupalAny fallback. The block's BODY
// START comes from the same anchor the rewriter uses (just past the `array(`/`[` opener),
// but the END is found STRING-AWARE via matchingArrayClose — NOT the drupalDefault regex's
// non-greedy `(?:\)|\])\s*;`, which would truncate the block at a `);`/`];` sitting inside a
// value string (e.g. `'init_commands'=>'SET NAMES utf8);'`) and so hide a later duplicate
// key (a real FALSE-OK the duplicate-key check must see). Magento already bounds string-aware.
//
// The anchor is LOCATED on a COMMENT-STRIPPED copy (StripComments preserves byte offsets),
// so a commented-out $databases['default']['default'] decoy — which can sit AFTER the live
// block — is blanked and never selected as the last match (S1-02). Without this the last
// RAW match could be the comment, making the parser read commented credentials and the
// rewriter edit a comment. (Heredoc/HTML/in-string decoys are deliberately NOT blanked
// here — they remain visible so the certifier's blockInNonCode / blind-vs-aware checks
// still flag them; this only makes the parser/rewriter comment-aware, matching the
// StripComments discipline of extractQuoted/phpArrayKey.)
func drupalBlockSpan(content string) (int, int, bool) {
	loc := wpconfig.StripComments(content) // offsets preserved; commented decoys blanked
	if ms := drupalDefault.FindAllStringSubmatchIndex(loc, -1); len(ms) > 0 {
		return drupalBodyEnd(content, ms[len(ms)-1][2]) // group-1 body start = just past the opener
	}
	if m := drupalAny.FindStringSubmatchIndex(loc); m != nil {
		return drupalBodyEnd(content, m[2])
	}
	return 0, 0, false
}

// drupalBodyEnd returns the body span [bodyStart, close) for an array whose opener `(`/`[`
// sits at bodyStart-1, finding the matching close STRING-AWARE (a `)`/`]` inside a value
// string does not close the block). Fails closed (ok=false) on an unterminated array.
func drupalBodyEnd(content string, bodyStart int) (int, int, bool) {
	if bodyStart <= 0 || bodyStart > len(content) {
		return 0, 0, false
	}
	opener := content[bodyStart-1]
	closer := byte(']')
	if opener == '(' {
		closer = ')'
	}
	end := matchingArrayClose(wpconfig.StripComments(content), bodyStart, opener, closer)
	if end < 0 {
		return 0, 0, false
	}
	return bodyStart, end, true
}

// magentoBlockSpan returns the byte span of the db 'connection'->'default' block body
// (exactly the span rewriteMagento edits).
func magentoBlockSpan(content string) (int, int, bool) {
	return arrayBlockBounds(content, "connection", "default")
}

// joomlaPropAnchor matches a JConfig class-property declaration `<visibility> [type] $name =`
// — visibility REQUIRED (unlike the rewriter's optional-visibility assignPre, so a bare
// top-level `$name = …` decoy is excluded), with an OPTIONAL PHP 7.4+ type (`public string
// $db`, `public ?string $db`, `public readonly string $db`) so a typed property is not
// missed (which would spuriously flag a clean config).
func joomlaPropAnchor(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?:public|protected|private|var)\s+(?:readonly\s+)?(?:\??[\\A-Za-z_][\\A-Za-z0-9_]*\s+)?\$` + regexp.QuoteMeta(name) + `\s*=\s*`)
}

// defineAmbiguity runs the define()-constant certifier (plus the computed-name guard) over
// each DB constant for the define()-based kinds.
func defineAmbiguity(content string, consts ...string) (string, bool, bool) {
	// A define() with a computed/non-literal constant NAME (define('DB_'.'NAME', …)) can
	// bind any DB constant at runtime in a way the literal-name checks cannot see, so it
	// makes the whole config unprovable.
	if wpconfig.HasComputedDefineName(content) {
		return "a define() with a computed/non-literal constant name may bind a DB constant at runtime the rewrite cannot prove", true, true
	}
	for _, c := range consts {
		if a := wpconfig.CheckDefineConstant(content, c); a.Ambiguous {
			return a.Reason, true, true
		}
	}
	return "", false, true
}

// credCheck pairs an assignment/property anchor (the rewriter's WRITE-side prefix) with the
// value the shared parser read (blindVal) and a human label, for assignAmbiguity.
type credCheck struct {
	anchor   *regexp.Regexp
	blindVal string
	label    string
}

// assignAmbiguity runs CheckQuotedCutover for each credential and returns the first
// ambiguous verdict; covered is always true for the dispatched kinds. requireUnique flags a
// shape that must appear exactly once (a class property).
func assignAmbiguity(content string, bind wpconfig.Bind, requireUnique bool, cs []credCheck) (string, bool, bool) {
	for _, c := range cs {
		if a := wpconfig.CheckQuotedCutover(content, c.anchor, bind, requireUnique, c.blindVal, c.label); a.Ambiguous {
			return a.Reason, true, true
		}
	}
	return "", false, true
}

// moodleCfgValue reads what the Moodle parser/rewriter reads for a $CFG->prop (leftmost
// quoted literal on comment-stripped content) — the blind value the rewrite targets.
func moodleCfgValue(content, prop string) string {
	return extractQuoted(content, `\$CFG->`+regexp.QuoteMeta(prop)+`\s*=\s*`, ``)
}
