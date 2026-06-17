package dbmig

import (
	"strings"
	"testing"
)

// S2 fault-injection for the deep-verify parsers (DB content fail-closed). The output
// comes from mysql on the remote host; a truncated, malformed, or exotic-name response
// must degrade to a clean/fail-closed result, never a panic or a silent pass.
// deepverify_test.go covers the happy paths; this adds hostile shapes + a fuzz net.
//
// Key fail-closed contracts asserted: parseMeta returns ok=false on undecodable HEX
// (so the row-count path, which has no independent backstop, fails closed); parseChecksums
// maps a NULL checksum to "" (so diffDeepTables treats it as UNVERIFIED, never an
// empty-string match).

func TestFaultSimDeepParsersNeverPanic(t *testing.T) {
	hostile := []string{
		"",
		"\x00\x00\x00",
		"no tabs here",
		"\t\t\t",
		"V",                // bare V, no version
		"A\tZZZZ\t5",       // A row with undecodable hex name
		"A\t4E4F\t",        // missing auto_increment field
		"onlyname\t",       // trailing tab, empty count
		"name\tnotanumber", // non-numeric count
		strings.Repeat("x\t1\n", 5000),
		"R\tPROCEDURE\tdeadbeef", // object row missing body field
		"\r\n\r\n",
	}
	for _, out := range hostile {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("deep parser panicked on %q: %v", out, r)
				}
			}()
			_ = parseRowCounts(out)
			_ = parseChecksums(out, "myschema")
			_, _, _, _ = parseMeta(out)
			_ = parseObjectBodies(out)
		}()
	}
}

// TestFaultSimParseMetaFailsClosedOnBadHex: a corrupt/truncated meta response whose
// table name is not decodable HEX must set ok=false, so DeepTables fails closed rather
// than silently dropping a table from the row-count correlation.
func TestFaultSimParseMetaFailsClosedOnBadHex(t *testing.T) {
	// Valid V line + an A row whose name is NOT valid hex (odd length / non-hex chars).
	out := "V\t8.0.34\nA\tNOTHEX\t0\n"
	if _, _, _, ok := parseMeta(out); ok {
		t.Errorf("parseMeta with an undecodable hex name = ok true, want false (fail closed)")
	}
	// No V line at all -> ok false.
	if _, _, _, ok := parseMeta("A\t7400\t0\n"); ok {
		t.Errorf("parseMeta with no version line = ok true, want false")
	}
	// All-good: valid V + valid hex ("t" = 74) -> ok true.
	if _, _, _, ok := parseMeta("V\t8.0.34\nA\t74\t3\n"); !ok {
		t.Errorf("parseMeta with valid version+hex = ok false, want true")
	}
	// ORDER-INDEPENDENCE (the sticky fail-closed fix): a bad-hex A line BEFORE the V
	// line must STILL fail closed — a later V must not re-upgrade ok, and the corrupt
	// table must never be silently dropped into a clean names set.
	if _, _, names, ok := parseMeta("A\tZZZZ\t9\nV\t8.0.34\n"); ok || len(names) != 0 {
		t.Errorf("parseMeta bad-hex-A-before-V = (ok=%v, names=%v), want (false, [])", ok, names)
	}
	if _, _, _, ok := parseMeta("A\tZZZZ\t9\nA\t74\t3\nV\t8.0.34\n"); ok {
		t.Errorf("parseMeta corrupt+good A before V = ok true, want false (never silently drop a table)")
	}
}

// TestFaultSimParseMetaEmptyVersionDownstreamSafe documents a fuzz-surfaced edge:
// a "V\t\t" line (present but EMPTY version) makes parseMeta return ok=true with
// version="". This is intentional and downstream-SAFE, not a fail-open: deepDB gates
// CHECKSUM TABLE / ObjectBodies on `Version != "" && srcVersion == destVersion`
// (apply_dbs.go:1003), so an empty/unknown version skips the checksum and is reported
// ContentUnchecked ("server versions differ or are unknown") — a fail under --deep,
// never a silent pass. VERSION() never returns empty on a live server, so this only
// arises on corrupt/truncated output, which the gating already fails closed.
func TestFaultSimParseMetaEmptyVersionDownstreamSafe(t *testing.T) {
	version, _, _, ok := parseMeta("V\t\t")
	if !ok || version != "" {
		t.Errorf("parseMeta(\"V\\t\\t\") = (version=%q, ok=%v), want (\"\", true) — pinned behavior", version, ok)
	}
}

// TestFaultSimParseChecksumsNullBecomesEmpty: a NULL checksum (an unchecksummable
// table) must be stored as "" so the diff layer treats it as content-UNVERIFIED, never
// a real hash that could match another NULL.
func TestFaultSimParseChecksumsNullBecomesEmpty(t *testing.T) {
	m := parseChecksums("myschema.t1\t12345\nmyschema.t2\tNULL\n", "myschema")
	if m["t1"] != "12345" {
		t.Errorf("t1 checksum = %q, want 12345", m["t1"])
	}
	if v, ok := m["t2"]; !ok || v != "" {
		t.Errorf("NULL checksum for t2 = (%q, present=%v), want (\"\", true)", v, ok)
	}
}

// FuzzDeepVerifyParsers asserts the four deep-verify parsers never panic on arbitrary
// mysql-output bytes. Run:
//
//	go test ./internal/dbmig -run x -fuzz FuzzDeepVerifyParsers -fuzztime 60s
func FuzzDeepVerifyParsers(f *testing.F) {
	seeds := []string{
		"t1\t10\nt2\t20\n",
		"myschema.t1\tABC123\nmyschema.t2\tNULL\n",
		"V\t8.0.34\nA\t74\t5\n",
		"V\tview1\tdeadbeef\nG\t7400\tcafe\nR\tPROCEDURE\t74\thash\nE\t7400\tbeef\n",
		"A\tZZZZ\t1",
		"\x00\xff",
		"",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, out string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("deep parser panicked on %q: %v", out, r)
			}
		}()
		// No-panic is the invariant. (parseMeta may report ok=true with an EMPTY version
		// for a "V\t\t" line; that is downstream-safe — see
		// TestFaultSimParseMetaEmptyVersionDownstreamSafe — so it is NOT asserted here.)
		_ = parseRowCounts(out)
		_ = parseChecksums(out, "s")
		_, _, _, _ = parseMeta(out)
		_ = parseObjectBodies(out)
	})
}
