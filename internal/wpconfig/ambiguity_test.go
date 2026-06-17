package wpconfig

import (
	"regexp"
	"testing"
)

// A clean wp-config with exactly one literal define per constant must NOT be flagged:
// the rewrite's target is PHP's first-wins choice.
func TestCheckDefineConstantCleanIsNotAmbiguous(t *testing.T) {
	content := "<?php\ndefine('DB_NAME','dest_db');\ndefine('DB_USER','dest_user');\n" +
		"define('DB_PASSWORD','dest_pass');\n$table_prefix='wp_';\n"
	for _, c := range []string{"DB_NAME", "DB_USER", "DB_PASSWORD"} {
		if a := CheckDefineConstant(content, c); a.Ambiguous {
			t.Errorf("%s: clean single literal define must not be ambiguous: %q", c, a.Reason)
		}
	}
}

// A commented-out decoy define above the live one is masked the same way by both the
// blind and the aware view, so the cutover stays provable (not ambiguous).
func TestCheckDefineConstantCommentDecoyIsNotAmbiguous(t *testing.T) {
	content := "<?php\n// define('DB_NAME','OLD_decoy');\ndefine('DB_NAME','dest_db');\n"
	if a := CheckDefineConstant(content, "DB_NAME"); a.Ambiguous {
		t.Errorf("a commented decoy must not make a clean live define ambiguous: %q", a.Reason)
	}
}

// A genuine duplicate where PHP's FIRST define is the one the rewrite edited stays clean
// (PHP first-wins binds the first; the later copy is ignored). This guards against the
// over-eager n>=2 false-DIFF.
func TestCheckDefineConstantFirstWinsDuplicateIsNotAmbiguous(t *testing.T) {
	content := "<?php\ndefine('DB_NAME','dest_db');\ndefine('DB_USER','u');\n" +
		"define('DB_PASSWORD','p');\ndefine('DB_NAME','leftover_old');\n"
	if a := CheckDefineConstant(content, "DB_NAME"); a.Ambiguous {
		t.Errorf("a duplicate whose FIRST define is correct must not be flagged: %q", a.Reason)
	}
}

// First define is a non-literal expression PHP resolves at runtime; the rewrite edited a
// later literal. The shared parser reads the literal and passes; the certifier must flag
// it (PHP uses the expression, not the rewritten literal).
func TestCheckDefineConstantFirstNonLiteralIsAmbiguous(t *testing.T) {
	content := "<?php\ndefine('DB_NAME', getenv('REAL_DB') ?: 'fallback');\n" +
		"define('DB_NAME','dest_db');\n"
	a := CheckDefineConstant(content, "DB_NAME")
	if !a.Ambiguous {
		t.Fatal("a non-literal first define must be ambiguous (PHP resolves it at runtime)")
	}
}

// A complete literal define() inside a heredoc body BEFORE the live one: the blind parser
// (StripComments, leftmost) reads the decoy inside the heredoc, the aware view blanks it
// and reads the real live define — they disagree, so it is ambiguous.
func TestCheckDefineConstantHeredocDecoyIsAmbiguous(t *testing.T) {
	content := "<?php\n$help = <<<EOT\ndefine('DB_NAME','dest_db');\nEOT;\n" +
		"define('DB_NAME','real_src_db');\n"
	a := CheckDefineConstant(content, "DB_NAME")
	if !a.Ambiguous {
		t.Fatal("a heredoc-embedded decoy define before the live one must be ambiguous")
	}
	// Sanity: the blind view DOES pick the heredoc decoy (the false-OK the shared parser
	// would certify), proving this test exercises the real divergence.
	if blind := extractDefine(content, "DB_NAME"); blind != "dest_db" {
		t.Fatalf("premise: blind parser should read the heredoc decoy %q, got %q", "dest_db", blind)
	}
}

// A conditional fallback whose guarded literal the rewrite edits, while an earlier
// non-literal define is what PHP binds, is ambiguous (the first live define is non-literal).
func TestCheckDefineConstantConditionalFallbackIsAmbiguous(t *testing.T) {
	content := "<?php\ndefine('DB_NAME', $_ENV['DB']);\n" +
		"if (!defined('DB_NAME')) define('DB_NAME','dest_db');\n"
	if a := CheckDefineConstant(content, "DB_NAME"); !a.Ambiguous {
		t.Fatal("a non-literal first define with a guarded literal fallback must be ambiguous")
	}
}

// A heredoc that does NOT contain a define for the constant must not perturb a clean
// live define (no false-DIFF from heredoc masking).
func TestCheckDefineConstantBenignHeredocIsNotAmbiguous(t *testing.T) {
	content := "<?php\n$sql = <<<SQL\nSELECT * FROM users WHERE name = 'admin';\nSQL;\n" +
		"define('DB_NAME','dest_db');\n"
	if a := CheckDefineConstant(content, "DB_NAME"); a.Ambiguous {
		t.Errorf("a benign heredoc must not make a clean define ambiguous: %q", a.Reason)
	}
}

// maskNonCode must blank heredoc bodies and comments while preserving byte offsets and
// leaving regular string literals intact.
func TestMaskNonCode(t *testing.T) {
	content := "<?php\n$x = <<<EOT\ndefine('DB_NAME','decoy');\nEOT;\n" +
		"// define('DB_NAME','c');\ndefine('DB_NAME','real');\n"
	masked := maskNonCode(content)
	if len(masked) != len(content) {
		t.Fatalf("maskNonCode must preserve length: got %d want %d", len(masked), len(content))
	}
	// The heredoc-body decoy and the comment decoy must be gone; the live define stays.
	if got := len(defineHeadRe("DB_NAME").FindAllStringIndex(masked, -1)); got != 1 {
		t.Fatalf("masked text must contain exactly the 1 live define head, got %d:\n%s", got, masked)
	}
	if v, lit, present := firstLiveDefine(masked, "DB_NAME"); !present || !lit || v != "real" {
		t.Fatalf("first live define = (%q, lit=%v, present=%v), want (real,true,true)", v, lit, present)
	}
	// Newlines preserved (offset stability proxy).
	for i := range content {
		if content[i] == '\n' && masked[i] != '\n' {
			t.Fatalf("newline at %d was altered", i)
		}
	}
}

// An unterminated heredoc blanks to EOF: the live define after it disappears, so the
// constant is reported absent-in-code (fail-closed, never a silent green).
func TestMaskNonCodeUnterminatedHeredocFailsClosed(t *testing.T) {
	content := "<?php\n$x = <<<EOT\nstill inside\ndefine('DB_NAME','dest_db');\n"
	if _, _, present := firstLiveDefine(maskNonCode(content), "DB_NAME"); present {
		t.Fatal("an unterminated heredoc must swallow the trailing define (fail-closed)")
	}
}

// G1 (refuter): a define() in inline-HTML mode (between ?> and <?php) is inert text PHP
// never executes; the shared parser reads it as the leftmost live define, but the certifier
// (maskNonCode is PHP-mode-aware) blanks it and sees the real code-mode define — they
// disagree, so it is flagged ambiguous.
func TestCheckDefineConstantHTMLModeDecoyIsAmbiguous(t *testing.T) {
	content := "<?php\n$x=1;\n?>\ndefine('DB_NAME','dest_db');\n<?php\n" +
		"define('DB_NAME','real_src_db');\n"
	a := CheckDefineConstant(content, "DB_NAME")
	if !a.Ambiguous {
		t.Fatal("a define() in inline-HTML mode (a ?> decoy) must be ambiguous")
	}
	// Premise: the blind parser DOES pick the HTML-mode decoy (the false-OK shape).
	if blind := extractDefine(content, "DB_NAME"); blind != "dest_db" {
		t.Fatalf("premise: blind parser should read the HTML decoy, got %q", blind)
	}
}

// A normal config with a trailing ?> (no decoy after it) must NOT be flagged.
func TestCheckDefineConstantTrailingCloseTagIsNotAmbiguous(t *testing.T) {
	content := "<?php\ndefine('DB_NAME','dest_db');\ndefine('DB_USER','u');\n?>\n"
	if a := CheckDefineConstant(content, "DB_NAME"); a.Ambiguous {
		t.Errorf("a trailing ?> with no HTML-mode decoy must not be flagged: %q", a.Reason)
	}
}

// CheckQuotedCutover (Phase 2): the assignment/property generalization.
func TestCheckQuotedCutover(t *testing.T) {
	// $CFG->dbname anchor (Moodle), last-wins.
	cfgAnchor := regexp.MustCompile(`\$CFG->dbname\s*=\s*`)
	// $db anchor (Joomla), first-wins.
	dbAnchor := regexp.MustCompile(`(?:public|protected|private|var)?\s*\$db\s*=\s*`)

	cases := []struct {
		name    string
		content string
		anchor  *regexp.Regexp
		bind    Bind
		unique  bool
		blind   string
		wantAmb bool
	}{
		{"clean single literal", "<?php\n$CFG->dbname = 'dest';\n", cfgAnchor, BindLast, false, "dest", false},
		{"last-wins divergence", "<?php\n$CFG->dbname = 'dest';\n$CFG->dbname = 'src';\n", cfgAnchor, BindLast, false, "dest", true},
		{"last-wins same value ok", "<?php\n$CFG->dbname = 'dest';\n$CFG->dbname = 'dest';\n", cfgAnchor, BindLast, false, "dest", false},
		{"non-literal value", "<?php\n$CFG->dbname = getenv('DB');\n", cfgAnchor, BindLast, false, "", true},
		{"heredoc decoy before live", "<?php\n$x=<<<EOT\npublic $db = 'dest';\nEOT;\nclass C{ public $db = 'src'; }\n", dbAnchor, BindFirst, false, "dest", true},
		{"absent in code but blind read one", "<?php\n// nothing live\n", cfgAnchor, BindLast, false, "dest", true},
		{"genuinely absent", "<?php\n// nothing\n", cfgAnchor, BindLast, false, "", false},
		{"unique violated (two declarations)", "<?php\npublic $db = 'a';\npublic $db = 'b';\n", dbAnchor, BindFirst, true, "a", true},
		{"unique satisfied (one declaration)", "<?php\npublic $db = 'a';\n", dbAnchor, BindFirst, true, "a", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := CheckQuotedCutover(c.content, c.anchor, c.bind, c.unique, c.blind, "x")
			if a.Ambiguous != c.wantAmb {
				t.Fatalf("ambiguous=%v, want %v (reason=%q)", a.Ambiguous, c.wantAmb, a.Reason)
			}
		})
	}
}

// G2 (refuter): a define() with a concatenated/computed constant NAME is unprovable.
func TestHasComputedDefineName(t *testing.T) {
	computed := "<?php\ndefine('DB_'.'NAME','src_db');\ndefine('DB_NAME','dest_db');\n"
	if !HasComputedDefineName(computed) {
		t.Fatal("define('DB_'.'NAME', …) must be detected as a computed name")
	}
	clean := "<?php\ndefine('DB_NAME','dest_db');\ndefine('WP_SITEURL','http://'.$h);\n" +
		"define('WP_DEBUG', true);\n"
	if HasComputedDefineName(clean) {
		t.Error("literal-named defines (even with concatenated VALUES) must not be flagged")
	}
}
