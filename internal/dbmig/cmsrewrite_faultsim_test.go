package dbmig

import (
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/wpconfig"
)

// S1 fault-injection for the CMS config rewriters/certifier (highest blast radius:
// a silent DB-credential corruption). Invariants:
//
//   - a hostile credential value (every PHP delimiter/escape) round-trips through
//     rewrite->reparse for the PHP-literal kinds, so it is never cut or mis-escaped;
//   - a value the .env format CANNOT represent fails the read-after-write reparse
//     (parse != value) rather than shipping a wrong password silently;
//   - the certifier (ConfigAmbiguity) and every rewriter never panic on arbitrary
//     content, and the bounded array-block scan never returns an out-of-range span.
//
// The semantic decoy cases (heredoc/HTML/in-string/duplicate/last-wins) are already
// covered exhaustively by TestConfigAmbiguity; this file adds the corruption-safety
// and DoS/robustness layer plus fuzz harnesses.

// cmsHostilePasswords are nasty-but-representable credential values for the PHP
// string-literal kinds (phpEscape can encode any of them).
var cmsHostilePasswords = []string{
	`n3w;p@ss`,
	`pa'ss\wd`,
	`quote"and$dollar`,
	`semi;colon);paren}brace{`,
	`back\\slash`,
	`'`, `"`, `\`, `$`,
	`a'b"c\d$e`,
	`');DROP TABLE x;--`,
	`héllo·wörld`,
}

// cmsKind pairs a kind's valid base config with its rewriter+parser. Each base config
// has all three DB fields present so the rewriter can place every value.
type cmsKind struct {
	name    string
	cfg     string
	rewrite func(content, dbName, dbUser, dbPassword string) string
	parse   func(string) wpconfig.Creds
}

// stringAwareKinds are the kinds whose READ + WRITE paths read a full quoted string
// literal (value class `(?:\\.|[^q\\])*`) or a string-aware bounded array scan, so any
// value — including one containing `);` / `];` — round-trips faithfully. Drupal is now
// INCLUDED: the S1-01 fix made parseDrupal/rewriteDrupal use the string-aware
// drupalBlockSpan bounds (see TestFaultSimDrupalBracketValueRoundTrips).
func stringAwareKinds() []cmsKind {
	return []cmsKind{
		{"Drupal", "<?php\n$databases['default']['default'] = array('database'=>'d','username'=>'u','password'=>'OLD','host'=>'127.0.0.1','prefix'=>'dr_');\n", rewriteDrupal, parseDrupal},
		{"Joomla", "<?php\nclass JConfig {\n  public $host = 'localhost';\n  public $user = 'u';\n  public $password = 'OLD';\n  public $db = 'd';\n  public $dbprefix = 'jos_';\n}\n", rewriteJoomla, parseJoomla},
		{"Moodle", "<?php\n$CFG = new stdClass();\n$CFG->dbhost = 'localhost';\n$CFG->dbname = 'd';\n$CFG->dbuser = 'u';\n$CFG->dbpass = 'OLD';\n$CFG->prefix = 'mdl_';\n", rewriteMoodle, parseMoodle},
		{"Magento", "<?php\nreturn ['db'=>['connection'=>['default'=>['host'=>'localhost','dbname'=>'d','username'=>'u','password'=>'OLD']]],'table_prefix'=>'mg_'];\n", rewriteMagento, parseMagentoEnv},
		{"PrestaShop", "<?php\ndefine('_DB_SERVER_','localhost');\ndefine('_DB_NAME_','d');\ndefine('_DB_USER_','u');\ndefine('_DB_PASSWD_','OLD');\ndefine('_DB_PREFIX_','ps_');\n", rewritePrestaShop, parsePrestaShop},
		{"OpenCart", "<?php\ndefine('DB_HOSTNAME','localhost');\ndefine('DB_DATABASE','d');\ndefine('DB_USERNAME','u');\ndefine('DB_PASSWORD','OLD');\ndefine('DB_PREFIX','oc_');\n", rewriteOpenCart, parseOpenCart},
	}
}

// TestFaultSimCMSRewriteRoundTripHostilePasswords: for every string-aware kind, a
// hostile password must rewrite and read back EXACTLY (no corruption), with name/user
// also placed correctly.
func TestFaultSimCMSRewriteRoundTripHostilePasswords(t *testing.T) {
	for _, k := range stringAwareKinds() {
		for _, pw := range cmsHostilePasswords {
			out := k.rewrite(k.cfg, "rt_db", "rt_user", pw)
			got := k.parse(out)
			if got.DBPassword != pw {
				t.Errorf("%s: password round-trip of %q = %q\n--- rewritten ---\n%s", k.name, pw, got.DBPassword, out)
			}
			if got.DBName != "rt_db" || got.DBUser != "rt_user" {
				t.Errorf("%s: name/user not placed: %+v (pw=%q)", k.name, got, pw)
			}
		}
	}
}

// TestFaultSimDrupalBracketValueRoundTrips locks in the S1-01 FIX: parseDrupal /
// rewriteDrupal now bound the $databases['default']['default'] block with the
// string-aware drupalBlockSpan (-> matchingArrayClose), which skips brackets inside
// string literals. So a credential value containing `);` or `];` round-trips faithfully
// (rewrite -> reparse == value) instead of truncating the block and failing the
// read-after-write verify on an otherwise-correct rewrite.
func TestFaultSimDrupalBracketValueRoundTrips(t *testing.T) {
	const cfg = "<?php\n$databases['default']['default'] = array('database'=>'d','username'=>'u','password'=>'OLD','host'=>'127.0.0.1','prefix'=>'dr_');\n"
	for _, pw := range []string{`semi);colon`, `a];b`, `');DROP TABLE x;--`, `p);q];r`, `}brace);{`} {
		out := rewriteDrupal(cfg, "rt_db", "rt_user", pw)
		got := parseDrupal(out)
		if got.DBPassword != pw {
			t.Errorf("S1-01: Drupal round-trip of %q = %q\n--- rewritten ---\n%s", pw, got.DBPassword, out)
		}
		if got.DBName != "rt_db" || got.DBUser != "rt_user" {
			t.Errorf("S1-01: name/user not placed for pw=%q: %+v", pw, got)
		}
		if !credsSet(got, "rt_db", "rt_user", pw) {
			t.Errorf("S1-01: verify should now pass for %q (round-trip), credsSet=false", pw)
		}
	}
}

// TestFaultSimDotEnvRoundTripAndFailClosed covers the Laravel .env quoting contract:
// a representable value round-trips; a value with a single quote AND a $/\/" (no safe
// phpdotenv form) must NOT read back as itself, so the production read-after-write
// reparse rejects it instead of shipping a wrong password.
func TestFaultSimDotEnvRoundTripAndFailClosed(t *testing.T) {
	const base = "APP_ENV=prod\nDB_HOST=127.0.0.1\nDB_DATABASE=old_db\nDB_USERNAME=old_user\nDB_PASSWORD=OLD\n"
	representable := []string{`n3w pass`, `with$dollar`, `back\slash`, `O'Brien`, `');DROP--`, `"quoted"`, `héllo`}
	for _, pw := range representable {
		out := rewriteDotEnv(base, "nd", "nu", pw)
		if got := parseDotEnv(out).DBPassword; got != pw {
			t.Errorf("dotenv representable round-trip of %q = %q\n%s", pw, got, out)
		}
	}
	// Single quote AND a special char: dotEnvQuote has no safe form, so it must fail
	// closed (parse != pw -> credsSet false -> RewriteSiteConfig rejects it).
	for _, pw := range []string{`a'b$c`, `x'y\z`, "line'one\ntwo"} {
		out := rewriteDotEnv(base, "nd", "nu", pw)
		if credsSet(parseDotEnv(out), "nd", "nu", pw) {
			t.Errorf("dotenv must FAIL closed on the unrepresentable value %q, but it read back clean:\n%s", pw, out)
		}
	}
}

// TestFaultSimArrayBlockBoundsFailClosed: an unterminated or malformed nested array
// must make arrayBlockBounds / blockAfter fail closed (ok=false / "") rather than
// return a span that runs off the end.
func TestFaultSimArrayBlockBoundsFailClosed(t *testing.T) {
	cases := map[string]string{
		"unterminated default block": "<?php return ['db'=>['connection'=>['default'=>['dbname'=>'x'",
		"missing inner key":          "<?php return ['db'=>['connection'=>['other'=>['dbname'=>'x']]]];",
		"bracket only in a string":   "<?php $s = \"'connection' => ['default' => [\";",
		"empty":                      "",
		"just brackets":              "[[[[[[",
	}
	for name, content := range cases {
		s, e, ok := arrayBlockBounds(content, "connection", "default")
		if ok && !(0 <= s && s <= e && e <= len(content)) {
			t.Errorf("%s: arrayBlockBounds returned an out-of-range span [%d,%d) for len %d", name, s, e, len(content))
		}
	}
}

// TestFaultSimDrupalCommentedDecoyAfterLiveReadsLive locks in the S1-02 FIX:
// drupalBlockSpan now locates the block on a COMMENT-STRIPPED copy, so a commented-out
// $databases['default']['default'] decoy AFTER the live block is blanked and never
// selected. parseDrupal reads the LIVE block, rewriteDrupal edits the LIVE block, and
// the cutover is correct — so the certifier no longer needs to flag it (it is no longer
// a decoy edit). There was previously only a test for a commented decoy BEFORE the live
// block (TestParseDrupalIgnoresCommentedDocblockExample); this covers the AFTER case.
func TestFaultSimDrupalCommentedDecoyAfterLiveReadsLive(t *testing.T) {
	live := "<?php\n$databases['default']['default'] = array('database'=>'live_db','username'=>'live_u','password'=>'live_p','host'=>'h');\n"
	decoys := map[string]string{
		"line comment":  live + "// $databases['default']['default'] = array('database'=>'decoy_db','username'=>'decoy_u','password'=>'decoy_p');\n",
		"block comment": live + "/*\n$databases['default']['default'] = array('database'=>'decoy_db','username'=>'decoy_u','password'=>'decoy_p');\n*/\n",
	}
	for name, content := range decoys {
		// The parser reads the LIVE block, NOT the commented decoy.
		if got := parseDrupal(content); got.DBName != "live_db" || got.DBPassword != "live_p" {
			t.Errorf("S1-02: %s: parseDrupal = %+v, want the live block (live_db/live_p)", name, got)
		}
		// The cutover is now correct, so the certifier does not flag it ambiguous.
		if _, amb, _ := ConfigAmbiguity(KindDrupal, content); amb {
			t.Errorf("S1-02: %s: ConfigAmbiguity = ambiguous, want clean (live block read/edited)", name)
		}
		// The rewriter edits the LIVE block and round-trips; the decoy stays commented.
		out := rewriteDrupal(content, "nd", "nu", "np")
		if got := parseDrupal(out); got.DBName != "nd" || got.DBPassword != "np" {
			t.Errorf("S1-02: %s: rewrite did not edit the live block: %+v", name, got)
		}
		if strings.Contains(out, "decoy_db") == false {
			t.Errorf("S1-02: %s: the commented decoy should be left untouched", name)
		}
	}
}

// TestFaultSimLimeSurveyCommentedDecoy locks in the S1-02 SIBLING fix: parseLimeSurvey
// reads the connectionString DSN dbname on a comment-stripped copy, so a commented-out
// connectionString decoy ABOVE the live one is not read as the source DB name.
func TestFaultSimLimeSurveyCommentedDecoy(t *testing.T) {
	const cfg = "<?php return array('components'=>array('db'=>array(\n" +
		"// 'connectionString' => 'mysql:host=old;dbname=decoy_db',\n" +
		"'connectionString' => 'mysql:host=localhost;dbname=live_db',\n" +
		"'username' => 'live_u', 'password' => 'live_p', 'tablePrefix' => 'lime_',\n" +
		")));\n"
	if got := parseLimeSurvey(cfg); got.DBName != "live_db" {
		t.Errorf("S1-02 sibling: LimeSurvey DBName = %q, want live_db (commented decoy must be ignored)", got.DBName)
	}
	// Block-comment form too.
	const cfg2 = "<?php return array('db'=>array(\n" +
		"/* 'connectionString' => 'mysql:host=old;dbname=decoy_db', */\n" +
		"'connectionString' => 'mysql:host=localhost;dbname=live_db', 'username'=>'u', 'password'=>'p',\n" +
		"));\n"
	if got := parseLimeSurvey(cfg2); got.DBName != "live_db" {
		t.Errorf("S1-02 sibling: LimeSurvey DBName (block comment) = %q, want live_db", got.DBName)
	}
}

// FuzzConfigAmbiguity is the S1 catch-net for the certifier + every rewriter: over
// arbitrary content, none may panic, across every dispatched kind. Run:
//
//	go test ./internal/dbmig -run x -fuzz FuzzConfigAmbiguity -fuzztime 60s
func FuzzConfigAmbiguity(f *testing.F) {
	seeds := []string{
		"<?php define('DB_NAME','n');define('DB_USER','u');define('DB_PASSWORD','p');",
		"<?php class JConfig { public $db='d'; public $user='u'; public $password='p'; public $dbprefix='j_'; }",
		"<?php $databases['default']['default']=array('database'=>'d','username'=>'u','password'=>'p');",
		"<?php return ['db'=>['connection'=>['default'=>['dbname'=>'d','username'=>'u','password'=>'p']]]];",
		"<?php $CFG->dbname='d';$CFG->dbuser='u';$CFG->dbpass='p';",
		"DB_DATABASE=d\nDB_USERNAME=u\nDB_PASSWORD=p\n",
		"<?php $h=<<<EOT\ndefine('DB_NAME','x');\nEOT;\ndefine('DB_NAME','y');",
		"<?php define('DB_'.'NAME','x');",
		"",
		"\x00\xff",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	kinds := []Kind{KindWordPress, KindJoomla, KindDrupal, KindMoodle, KindMagento, KindPrestaShop, KindOpenCart, KindLaravel}
	f.Fuzz(func(t *testing.T, content string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("S1 certifier/rewriter chain panicked on %q: %v", content, r)
			}
		}()
		_, _ = parseCMSConfig("f", content)
		for _, k := range kinds {
			_, _, _ = ConfigAmbiguity(k, content)
		}
		// Every rewriter must tolerate arbitrary content (it returns content unchanged
		// when it cannot place a value; never a panic).
		_ = rewriteJoomla(content, "d", "u", "p")
		_ = rewriteDrupal(content, "d", "u", "p")
		_ = rewriteMoodle(content, "d", "u", "p")
		_ = rewriteMagento(content, "d", "u", "p")
		_ = rewritePrestaShop(content, "d", "u", "p")
		_ = rewriteOpenCart(content, "d", "u", "p")
		_ = rewriteDotEnv(content, "d", "u", "p")
	})
}

// FuzzArrayBlockBounds asserts the bounded array-block scan (and its string-literal
// skipper) never panics and never returns an out-of-range span. Run:
//
//	go test ./internal/dbmig -run x -fuzz FuzzArrayBlockBounds -fuzztime 60s
func FuzzArrayBlockBounds(f *testing.F) {
	seeds := []string{
		"<?php return ['db'=>['connection'=>['default'=>['dbname'=>'x']]]];",
		"<?php ['connection'=>['default'=>[ unterminated",
		"['a'=>['b'=>'x'])]",
		"'connection' => ['default' => ['x'=>'])']]",
		"",
		"[[[[",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, content string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("array-block scan panicked on %q: %v", content, r)
			}
		}()
		if s, e, ok := arrayBlockBounds(content, "connection", "default"); ok {
			if !(0 <= s && s <= e && e <= len(content)) {
				t.Fatalf("arrayBlockBounds out-of-range span [%d,%d) for len %d on %q", s, e, len(content), content)
			}
		}
		if s, e, ok := drupalBlockSpan(content); ok {
			if !(0 <= s && s <= e && e <= len(content)) {
				t.Fatalf("drupalBlockSpan out-of-range span [%d,%d) for len %d on %q", s, e, len(content), content)
			}
		}
		_ = blockAfter(content, "connection", "default")
	})
}
