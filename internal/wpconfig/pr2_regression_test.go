package wpconfig

import "testing"

// TestReplaceDefineMismatchedQuote guards the fix where a define value containing the
// OPPOSITE quote followed by ");" was cut short on rewrite (closing quote no longer
// has to match the opening one only by luck).
func TestReplaceDefineMismatchedQuote(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"double-quoted value containing ');",
			`define('DB_PASSWORD', "abc');xyz");`, `define('DB_PASSWORD', "NEW");`},
		{"single-quoted value containing \");",
			`define('DB_PASSWORD', 'abc");xyz');`, `define('DB_PASSWORD', 'NEW');`},
		{"normal single-quoted still works",
			`define('DB_PASSWORD', 'oldpw');`, `define('DB_PASSWORD', 'NEW');`},
	}
	for _, c := range cases {
		if got := replaceDefine(c.in, "DB_PASSWORD", "NEW"); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
	if p := Parse(`define('DB_PASSWORD', "abc');xyz");`).DBPassword; p != `abc');xyz` {
		t.Errorf("Parse round-trip of a value with ') got %q", p)
	}
}
