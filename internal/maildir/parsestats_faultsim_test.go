package maildir

import (
	"strings"
	"testing"
)

// S4 fault-injection (parser/DoS robustness) for the maildir stats parsers. The
// production readers GetFolderStats/GetBoxStats run parseFolderStatsStrict /
// parseBoxStatsStrict over LIVE remote output, which is untrusted. The strict
// parsers must FAIL CLOSED (return an error) on anything malformed — never a
// silent under-count that could mask a real shortfall — and never panic. These
// cases complement stats_test.go with integer-overflow, whitespace, and
// control-byte shapes, plus fuzz harnesses over both the strict and lenient forms.

// TestFaultSimFolderStatsStrictRejectsOverflow covers a count or UIDVALIDITY that
// is numerically out of range: strconv must reject it and the strict parser must
// surface the error rather than wrap/truncate to a bogus value.
func TestFaultSimFolderStatsStrictRejectsOverflow(t *testing.T) {
	cases := map[string]string{
		"count overflows int":     "INBOX\t99999999999999999999999\tV1\n",
		"uidvalidity overflows":   "INBOX\t4\tV99999999999999999999999\n", // > uint64
		"count is whitespace":     "INBOX\t   \tV1\n",
		"count has embedded sign": "INBOX\t5-\tV1\n",
		"tab-only line":           "\t\t\n",
		"control byte in count":   "INBOX\t5\x00\tV1\n",
	}
	for name, in := range cases {
		if got, err := parseFolderStatsStrict(in); err == nil {
			t.Errorf("%s: parseFolderStatsStrict(%q) = (%+v, nil), want an error (fail closed)", name, in, got)
		}
	}
}

// TestFaultSimBoxStatsStrictRejectsOverflow mirrors the above for the aggregate
// box-stats reader.
func TestFaultSimBoxStatsStrictRejectsOverflow(t *testing.T) {
	cases := map[string]string{
		"count overflows int":   "99999999999999999999999|V1",
		"uidvalidity overflows": "4|V99999999999999999999999",
		"count is whitespace":   "   |V1",
		"empty count with sep":  "|V1", // Atoi("") fails -> error, not a silent zero
	}
	for name, in := range cases {
		if got, err := parseBoxStatsStrict(in); err == nil {
			t.Errorf("%s: parseBoxStatsStrict(%q) = (%+v, nil), want an error (fail closed)", name, in, got)
		}
	}
}

// TestFaultSimFolderStatsStrictManyFolders feeds a large, well-formed batch to
// confirm the parser scales linearly and does not choke (no panic / no pathological
// slowdown) on a mailbox with many subfolders.
func TestFaultSimFolderStatsStrictManyFolders(t *testing.T) {
	var b strings.Builder
	const n = 50_000
	for i := 0; i < n; i++ {
		b.WriteString(".Folder")
		b.WriteString(itoaLocal(i))
		b.WriteString("\t1\tV1\n")
	}
	got, err := parseFolderStatsStrict(b.String())
	if err != nil {
		t.Fatalf("parseFolderStatsStrict(many) errored: %v", err)
	}
	if len(got) != n {
		t.Fatalf("parsed %d folders, want %d", len(got), n)
	}
}

// TestFaultSimUIDValidityNeverPanics exercises validUIDValidity / parseUIDValidity
// with degenerate tokens; both must return cleanly (false / "") without panicking
// on a short or all-control string.
func TestFaultSimUIDValidityNeverPanics(t *testing.T) {
	for _, s := range []string{"", "V", "\x00", "V\x00", "V" + strings.Repeat("9", 40), " V1 ", "VV1"} {
		_ = validUIDValidity(s)
	}
	for _, s := range []string{"", "\x00 \x00", "V1", strings.Repeat("\t", 8)} {
		_ = parseUIDValidity(s)
	}
}

// FuzzParseFolderStats asserts both stats parsers never panic on arbitrary output,
// and (the safety property that matters) that whenever the STRICT parser returns a
// map, every count in it is non-negative — a negative count must always be an error,
// never a silently-accepted value that could net away a real shortfall. Run:
//
//	go test ./internal/maildir -run x -fuzz FuzzParseFolderStats -fuzztime 60s
func FuzzParseFolderStats(f *testing.F) {
	seeds := []string{
		"INBOX\t120\tV1687\n.Sent\t8\tV1688\n",
		".Trash\t0\t\n",
		"bad line\n",
		".Tabbed\tName\t3\tV1690\n",
		"INBOX\t-5\tV1\n",
		"INBOX\t99999999999999999999\tV1\n",
		"\t\t\n",
		"\x00\x00\x00",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, out string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("folder-stats parser panicked on %q: %v", out, r)
			}
		}()
		_ = parseFolderStats(out) // lenient: must not panic
		if m, err := parseFolderStatsStrict(out); err == nil {
			for label, fs := range m {
				if fs.Count < 0 {
					t.Fatalf("parseFolderStatsStrict accepted a negative count %d for %q from %q", fs.Count, label, out)
				}
			}
		}
	})
}

// FuzzParseBoxStats asserts the box-stats parsers never panic and that the strict
// form never returns a negative count. Run:
//
//	go test ./internal/maildir -run x -fuzz FuzzParseBoxStats -fuzztime 60s
func FuzzParseBoxStats(f *testing.F) {
	for _, s := range []string{"6863|V1687370761", "0|", "42", "", "-5|V1", "|V1", "\x00|\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, out string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("box-stats parser panicked on %q: %v", out, r)
			}
		}()
		_ = parseBoxStats(out)
		if bs, err := parseBoxStatsStrict(out); err == nil && bs.MsgCount < 0 {
			t.Fatalf("parseBoxStatsStrict accepted a negative count %d from %q", bs.MsgCount, out)
		}
	})
}

// itoaLocal is a tiny int->string to avoid importing strconv just for the
// many-folders test (the package under test already owns strconv usage).
func itoaLocal(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
