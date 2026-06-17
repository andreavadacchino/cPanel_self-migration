package maildir

import "testing"

func TestParseBoxStats(t *testing.T) {
	cases := []struct {
		in   string
		want BoxStats
	}{
		{"6863|V1687370761", BoxStats{6863, "V1687370761"}},
		{"0|", BoxStats{0, ""}},
		{"  14667 | V123 ", BoxStats{14667, "V123"}},
		{"", BoxStats{0, ""}},
		{"42", BoxStats{42, ""}},
	}
	for _, c := range cases {
		if got := parseBoxStats(c.in); got != c.want {
			t.Errorf("parseBoxStats(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParseFolderStats(t *testing.T) {
	out := "INBOX\t120\tV1687\n" +
		".Sent\t8\tV1688\n" +
		".Trash\t0\t\n" + // empty folder, no uidvalidity
		"bad line without tabs\n" +
		".BadCount\tNaN\tV9999\n" +
		".BadUID\t4\tbogus\n" +
		".Tabbed\tName\t3\tV1690\n" + // a folder NAME containing a tab must be kept whole
		".Folder With Spaces\t3\tV1689\n" // a folder name with spaces is kept
	got := parseFolderStats(out)
	want := map[string]FolderStats{
		"INBOX":               {Count: 120, UIDValidity: "V1687"},
		".Sent":               {Count: 8, UIDValidity: "V1688"},
		".Trash":              {Count: 0, UIDValidity: ""},
		".Tabbed\tName":       {Count: 3, UIDValidity: "V1690"},
		".Folder With Spaces": {Count: 3, UIDValidity: "V1689"},
	}
	if len(got) != len(want) {
		t.Fatalf("parseFolderStats parsed %d folders, want %d: %+v", len(got), len(want), got)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("folder %q = %+v, want %+v", k, got[k], w)
		}
	}
	for _, bad := range []string{".BadCount", ".BadUID"} {
		if _, ok := got[bad]; ok {
			t.Errorf("malformed folder %q should have been skipped: %+v", bad, got[bad])
		}
	}
}

func TestParseBoxStatsStrict(t *testing.T) {
	ok := []struct {
		in   string
		want BoxStats
	}{
		{"6863|V1687370761", BoxStats{6863, "V1687370761"}},
		{"0|", BoxStats{0, ""}},
		{"  14667 | V123 ", BoxStats{14667, "V123"}},
		{"", BoxStats{0, ""}},     // genuinely empty -> clean zero
		{"12|", BoxStats{12, ""}}, // non-empty box, no UIDVALIDITY: allowed at parse time
	}
	for _, c := range ok {
		got, err := parseBoxStatsStrict(c.in)
		if err != nil || got != c.want {
			t.Errorf("parseBoxStatsStrict(%q) = (%+v, %v), want (%+v, nil)", c.in, got, err, c.want)
		}
	}
	bad := []string{
		"42",      // no separator -> garbled
		"NaN|V1",  // non-numeric count
		"-5|V1",   // negative count
		"3|bogus", // invalid UIDVALIDITY
		"3|123",   // UIDVALIDITY without the leading V
	}
	for _, in := range bad {
		if got, err := parseBoxStatsStrict(in); err == nil {
			t.Errorf("parseBoxStatsStrict(%q) = (%+v, nil), want an error (fail closed)", in, got)
		}
	}
}

func TestParseFolderStatsStrict(t *testing.T) {
	// Accepts: valid rows, an empty folder with no UID, a non-empty folder with NO UID
	// (kept for the classifier to flag), and labels containing spaces or tabs.
	out := "INBOX\t120\tV1687\n" +
		".Sent\t8\tV1688\n" +
		".Trash\t0\t\n" + // empty folder, no UIDVALIDITY is fine
		".NoUID\t4\t\n" + // NON-empty folder with no UID: kept, NOT dropped
		".Tabbed\tName\t3\tV1690\n" + // a folder NAME containing a tab is kept whole
		".Folder With Spaces\t3\tV1689\n"
	got, err := parseFolderStatsStrict(out)
	if err != nil {
		t.Fatalf("parseFolderStatsStrict(valid) errored: %v", err)
	}
	want := map[string]FolderStats{
		"INBOX":               {Count: 120, UIDValidity: "V1687"},
		".Sent":               {Count: 8, UIDValidity: "V1688"},
		".Trash":              {Count: 0, UIDValidity: ""},
		".NoUID":              {Count: 4, UIDValidity: ""},
		".Tabbed\tName":       {Count: 3, UIDValidity: "V1690"},
		".Folder With Spaces": {Count: 3, UIDValidity: "V1689"},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d folders, want %d: %+v", len(got), len(want), got)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("folder %q = %+v, want %+v", k, got[k], w)
		}
	}

	// Rejects (fail closed, not a silent skip): malformed structure, bad/negative
	// count, invalid UIDVALIDITY, and a duplicate label.
	bad := map[string]string{
		"no tabs at all":    "lonelyline\n",
		"single field":      "INBOX\t5\n",
		"empty label":       "\t5\tV1\n",
		"non-numeric count": ".X\tNaN\tV1\n",
		"negative count":    ".X\t-3\tV1\n",
		"invalid uid":       ".X\t4\tbogus\n",
		"duplicate label":   "INBOX\t1\tV1\nINBOX\t2\tV2\n",
	}
	for name, in := range bad {
		if got, err := parseFolderStatsStrict(in); err == nil {
			t.Errorf("%s: parseFolderStatsStrict(%q) = (%+v, nil), want an error (fail closed)", name, in, got)
		}
	}
}

func TestParseUIDValidity(t *testing.T) {
	cases := map[string]string{
		"3 V1687370761 N123 G7a8b": "V1687370761",
		"1 V1700000000":            "V1700000000",
		"":                         "",
		"justonefield":             "",
		"1 not-a-uid":              "",
	}
	for in, want := range cases {
		if got := parseUIDValidity(in); got != want {
			t.Errorf("parseUIDValidity(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidUIDValidity(t *testing.T) {
	for _, s := range []string{"V1", "V0", "V1687370761", "V4294967295"} {
		if !validUIDValidity(s) {
			t.Errorf("validUIDValidity(%q) = false, want true", s)
		}
	}
	// UIDVALIDITY is unsigned: a sign or a negative must be rejected (Atoi accepted "-1").
	for _, s := range []string{"", "V", "X1", "V-1", "V+1", "V12abc", "1687370761", "V 1", "V1.0"} {
		if validUIDValidity(s) {
			t.Errorf("validUIDValidity(%q) = true, want false", s)
		}
	}
}

func TestConsistent(t *testing.T) {
	a := BoxStats{100, "V1"}
	if !a.Consistent(BoxStats{100, "V1"}) {
		t.Error("identical stats should be consistent")
	}
	if a.Consistent(BoxStats{100, "V2"}) {
		t.Error("differing UIDVALIDITY should not be consistent")
	}
	if a.Consistent(BoxStats{99, "V1"}) {
		t.Error("differing count should not be consistent")
	}
	if (BoxStats{0, "V1"}).Consistent(BoxStats{0, "V1"}) {
		t.Error("zero count should never be consistent")
	}
	if a.Consistent(BoxStats{100, ""}) {
		t.Error("empty UIDVALIDITY should not be consistent")
	}
}
