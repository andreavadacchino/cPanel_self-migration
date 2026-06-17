package domainname

import "testing"

func TestKey(t *testing.T) {
	cases := map[string]string{
		"Example.COM":       "example.com",
		"example.com.":      "example.com",
		"XN--MNCHEN-3YA.DE": "xn--mnchen-3ya.de",
		"münchen.example":   "münchen.example",
		"München.EXAMPLE.":  "münchen.example",
		"example.com..":     "example.com",
		" example.com ":     " example.com ",
		"":                  "",
	}
	for in, want := range cases {
		if got := Key(in); got != want {
			t.Errorf("Key(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHasAndEqual(t *testing.T) {
	set := map[string]bool{Key("Example.COM."): true}
	if !Has(set, "example.com") {
		t.Fatal("Has should compare canonical keys")
	}
	if !Equal("XN--MNCHEN-3YA.DE", "xn--mnchen-3ya.de.") {
		t.Fatal("Equal should compare canonical keys")
	}
}
