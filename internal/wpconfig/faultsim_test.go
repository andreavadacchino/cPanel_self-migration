package wpconfig

import (
	"strings"
	"testing"
)

// S4-method fault-injection for surface S1 (rewriter PHP/CMS config — highest blast
// radius: a silent DB-credential corruption). The wp-config.php content is arbitrary
// PHP written by the site owner (untrusted). The invariants under test:
//
//   - no parser/mask/rewriter ever panics on hostile input;
//   - the comment/heredoc/HTML masks PRESERVE byte length (the certifier slices
//     strip[s:e] / block[s:e] by offset, so a length drift would mis-slice or panic);
//   - a rewritten DB value round-trips: Parse(Rewrite(content, v)) == v, so a value
//     containing quotes/backslashes/'$'/';'/')' can never be cut or re-escaped wrong;
//   - the structural certifier refuses to certify a decoy define (never green).

// hostilePasswords are real-world-nasty credential values: every PHP string
// delimiter and escape, plus the bytes that broke naive cutover scanners.
var hostilePasswords = []string{
	`p'a;s{s}1`,
	`quote"and$dollar`,
	`back\slash\\end`,
	`semi;colon);paren`,
	`'`, `"`, `\`, `$`, `\\`, `\'`, `\"`,
	`a'b"c\d$e`,
	`');DROP TABLE x;--`,
	`héllo·wörld`, // multi-byte: must survive byte-for-byte
	"tab\tinside",
}

// TestFaultSimMaskOffsetsPreserved is the load-bearing invariant: StripComments,
// maskNonCode and maskNonCodeAndStrings must return a string of the SAME byte length
// as their input (they blank in place, never delete), because the array/block-kind
// certifier indexes the masked copies by offsets taken from the raw content.
func TestFaultSimMaskOffsetsPreserved(t *testing.T) {
	inputs := []string{
		"",
		"<?php define('DB_NAME','x');",
		"<?php $s='/* not a comment */ // nor this'; # real\n$h=<<<EOT\nbody EOT not closed",
		"<?php $h=<<<'NOWDOC'\ndefine('DB_NAME','decoy');\nNOWDOC;\n",
		"plain html <?= 'x' ?> trailing html with /* unterminated",
		"<?php '" + strings.Repeat("\\", 50) + "\n", // dangling escapes + unterminated string
		"<?php \"unterminated double",
		"\x00<?php\x00define\x00",
		strings.Repeat("<?php /*", 200),
		"<?php $x = '" + strings.Repeat("a'b\\c", 100) + "';",
	}
	for _, in := range inputs {
		for name, f := range map[string]func(string) string{
			"StripComments":         StripComments,
			"maskNonCode":           maskNonCode,
			"maskNonCodeAndStrings": maskNonCodeAndStrings,
		} {
			out := f(in)
			if len(out) != len(in) {
				t.Errorf("%s(%q): length %d != input length %d (offset preservation broken)", name, in, len(out), len(in))
			}
		}
	}
}

// TestFaultSimRewriteRoundTripHostilePasswords proves the WordPress rewrite never
// corrupts a hostile credential: after rewriting DB_PASSWORD to a nasty value, Parse
// must read back EXACTLY that value, for both single- and double-quoted defines.
func TestFaultSimRewriteRoundTripHostilePasswords(t *testing.T) {
	templates := map[string]string{
		"single-quoted": "<?php\ndefine('DB_NAME','n');\ndefine('DB_USER','u');\ndefine('DB_PASSWORD','OLD');\n",
		"double-quoted": "<?php\ndefine(\"DB_NAME\",\"n\");\ndefine(\"DB_USER\",\"u\");\ndefine(\"DB_PASSWORD\",\"OLD\");\n",
	}
	for style, tmpl := range templates {
		for _, pw := range hostilePasswords {
			out := Rewrite(tmpl, "", "", pw)
			got := Parse(out).DBPassword
			if got != pw {
				t.Errorf("%s: round-trip of %q = %q (rewrite/parse corrupted the value)\n--- rewritten ---\n%s", style, pw, got, out)
			}
			// And the rest of the file is intact: name/user unchanged.
			if c := Parse(out); c.DBName != "n" || c.DBUser != "u" {
				t.Errorf("%s: rewrite of password disturbed other fields: %+v", style, c)
			}
		}
	}
}

// TestFaultSimCertifierRefusesDecoys feeds decoy shapes whose live define differs
// from what the heredoc/HTML-blind shared parser reads; the certifier must refuse
// (Ambiguous) so the cutover is never certified green.
func TestFaultSimCertifierRefusesDecoys(t *testing.T) {
	cases := map[string]string{
		// A NOWDOC body holds a complete define the blind StripComments parser reads as
		// leftmost, but maskNonCode blanks the body so the live define differs.
		"nowdoc decoy before live": "<?php\n$h = <<<'EOT'\ndefine('DB_NAME','decoy');\nEOT;\n" +
			"define('DB_NAME','live');\n",
		// First live define is a getenv() expression; the rewrite edited a later literal.
		"first live non-literal": "<?php\ndefine('DB_NAME', getenv('DBN'));\ndefine('DB_NAME','literal');\n",
		// The only define sits in inline-HTML (before the open tag): not executable code.
		"define only in inline html": "define('DB_NAME','x'); <?php $y=1;",
	}
	for name, content := range cases {
		if a := CheckDefineConstant(content, "DB_NAME"); !a.Ambiguous {
			t.Errorf("%s: CheckDefineConstant = not ambiguous, want refuse-to-certify\n%s", name, content)
		}
	}
}

// FuzzWPConfigParse is the S1 catch-net: over arbitrary bytes, the parse/mask/rewrite/
// certify chain must never panic, the masks must preserve byte length, and the BLIND
// read-after-write must be self-consistent (Parse after Rewrite reads back the value
// the rewrite wrote whenever a DB_NAME define was present). Run:
//
//	go test ./internal/wpconfig -run x -fuzz FuzzWPConfigParse -fuzztime 60s
func FuzzWPConfigParse(f *testing.F) {
	seeds := []string{
		"<?php\ndefine('DB_NAME','n');\ndefine('DB_USER','u');\ndefine('DB_PASSWORD','p');\n$table_prefix='wp_';\n",
		"<?php define(\"DB_NAME\",\"a\\\"b\");",
		"<?php $h=<<<EOT\ndefine('DB_NAME','x');\nEOT;\ndefine('DB_NAME','y');",
		"<?php define('DB_'.'NAME','x');",
		"<?php // define('DB_NAME','c')\ndefine('DB_NAME','real');",
		"plain <?= 'html' ?> <?php define('DB_NAME', getenv('X'));",
		"<?php '" + strings.Repeat("\\", 10),
		"\x00\xff<?php",
		"",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	const newName = "n3w_\\db'x\"$"
	f.Fuzz(func(t *testing.T, content string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("S1 chain panicked on %q: %v", content, r)
			}
		}()
		// Offset preservation (load-bearing for the array/block certifier).
		for name, fn := range map[string]func(string) string{
			"StripComments": StripComments, "maskNonCode": maskNonCode, "maskNonCodeAndStrings": maskNonCodeAndStrings,
		} {
			if got := fn(content); len(got) != len(content) {
				t.Fatalf("%s changed length %d -> %d on %q", name, len(content), len(got), content)
			}
		}
		c0 := Parse(content)
		_ = CheckDefineConstant(content, "DB_NAME")
		_ = HasComputedDefineName(content)
		// Blind read-after-write consistency: when a DB_NAME define exists, rewriting it
		// to newName and re-parsing must recover newName exactly (no corruption).
		if c0.DBName != "" {
			if got := Parse(Rewrite(content, newName, "", "")).DBName; got != newName {
				t.Fatalf("read-after-write divergence: DB_NAME rewrite of %q read back %q, want %q", content, got, newName)
			}
		}
	})
}
