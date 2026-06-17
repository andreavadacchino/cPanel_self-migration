package webfiles

import (
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/validate"
)

// S4 fault-injection (parser/DoS robustness) for the docroot listing/manifest
// record parsers. The find->tar pipe records come from the SOURCE host and are
// untrusted: a record whose path is absolute, contains a `..` component, a TAB, or
// a control byte must NEVER be accepted (it would let tar read/write outside the
// docroot), and no input may panic. The pure parsers parseFileLine /
// parseManifestRecord / parseDigestRecord / parseDigestOutput / parseSize carry the
// guard; these cases complement webfiles_test.go with overflow, control-byte, and
// truncation shapes, plus fuzz harnesses asserting the never-accept-an-unsafe-path
// invariant.

// TestFaultSimFileLineRejectsHostilePaths feeds list records whose path or fields
// are malformed/hostile. Each must be dropped (ok=false); a traversal/absolute/
// control-byte path must be flagged unsafe so the caller can refuse to certify.
func TestFaultSimFileLineRejectsHostilePaths(t *testing.T) {
	unsafeCases := map[string]string{
		"parent traversal": "f\t10\t../escape",
		"nested traversal": "f\t10\ta/../../escape",
		"absolute path":    "f\t10\t/etc/passwd",
		"newline in path":  "f\t10\ta\nb",
		"cr in path":       "f\t10\ta\rb",
	}
	for name, rec := range unsafeCases {
		f, ok, unsafe := parseFileLine(rec)
		if ok {
			t.Errorf("%s: parseFileLine(%q) accepted %v, want dropped", name, rec, f)
		}
		if !unsafe {
			t.Errorf("%s: parseFileLine(%q) unsafe=false, want true (must be flagged)", name, rec)
		}
	}

	// Benign-malformed records: dropped, but NOT flagged unsafe (so the caller does
	// not raise the "unsafe path" alarm for an ordinary garbled line).
	benign := map[string]string{
		"too few fields":   "f\t10",
		"bad type":         "x\t10\tok.txt",
		"non-numeric size": "f\tNaN\tok.txt",
		"size overflow":    "f\t99999999999999999999999\tok.txt",
		"empty path":       "f\t10\t",
		"NODIR sentinel":   "NODIR",
		"empty":            "",
	}
	for name, rec := range benign {
		_, ok, unsafe := parseFileLine(rec)
		if ok {
			t.Errorf("%s: parseFileLine(%q) accepted, want dropped", name, rec)
		}
		if unsafe {
			t.Errorf("%s: parseFileLine(%q) unsafe=true, want false (benign malformed)", name, rec)
		}
	}
}

// TestFaultSimManifestRecordRejectsHostilePaths mirrors the above for the 5-field
// manifest record, including the tab-in-filename truncation that must be caught as
// unsafe (the tail leaks into the otherwise-empty link field for an f/d entry).
func TestFaultSimManifestRecordRejectsHostilePaths(t *testing.T) {
	unsafe := map[string]string{
		"parent traversal": "f\t644\t10\t../escape\t",
		"absolute path":    "f\t644\t10\t/etc/passwd\t",
		"tab in filename":  "f\t644\t10\tdir\tfile", // SplitN truncates -> tail in link field
		"control in path":  "f\t644\t10\ta\x01b\t",
	}
	for name, rec := range unsafe {
		rel, _, ok, uns := parseManifestRecord(rec)
		if ok {
			t.Errorf("%s: parseManifestRecord(%q) accepted rel=%q, want dropped", name, rec, rel)
		}
		if !uns {
			t.Errorf("%s: parseManifestRecord(%q) unsafe=false, want true", name, rec)
		}
	}
	benign := map[string]string{
		"too few fields":   "f\t644\t10\tok.txt", // only 4 fields
		"bad type":         "z\t644\t10\tok.txt\t",
		"non-numeric size": "f\t644\tNaN\tok.txt\t",
		"empty record":     "",
		"NODIR":            "NODIR",
	}
	for name, rec := range benign {
		_, _, ok, uns := parseManifestRecord(rec)
		if ok {
			t.Errorf("%s: parseManifestRecord(%q) accepted, want dropped", name, rec)
		}
		if uns {
			t.Errorf("%s: parseManifestRecord(%q) unsafe=true, want false", name, rec)
		}
	}
}

// TestFaultSimDigestOutputNeverPanics throws degenerate gather/digest output at the
// status parser: garbage, conflicting markers, and overflow byte counts must parse
// to a clean tri-state without panicking.
//
// It also locks in the S4-01 FIX: UNREADABLE is now STICKY — neither a following
// ABSENT nor a following "<bytes>|<count>" total downgrades it, so the fail-closed
// verdict is order-independent (matching the guarded ABSENT branch and parseSize).
func TestFaultSimDigestOutputNeverPanics(t *testing.T) {
	cases := []string{
		"",
		"UNREADABLE\nABSENT\n100|5\n", // S4-01 fixed: UNREADABLE is sticky -> sizeUnreadable
		"ABSENT\nUNREADABLE\n",        // stays sizeUnreadable
		"99999999999999999999999|5",   // byte count overflow -> not a clean present
		"100|NaN",                     // bad count
		"DIGEST\n",                    // DIGEST marker with no hex
		"NODIGEST\n100|5\n",           // tools missing but totals present
		"\x00\x00|\x00",
		"|||||",
	}
	for _, out := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("parseDigestOutput panicked on %q: %v", out, r)
				}
			}()
			_, _, _, _ = parseDigestOutput(out)
		}()
	}
	// UNREADABLE must fail closed regardless of line order (the S4-01 fix).
	if _, _, _, status := parseDigestOutput("UNREADABLE\n"); status != sizeUnreadable {
		t.Errorf("bare UNREADABLE = status %v, want sizeUnreadable", status)
	}
	if _, _, _, status := parseDigestOutput("100|5\nUNREADABLE"); status != sizeUnreadable {
		t.Errorf("total then UNREADABLE = status %v, want sizeUnreadable", status)
	}
	// S4-01 FIXED: an UNREADABLE marker FOLLOWED BY a total line must now stay
	// sizeUnreadable (sticky), not downgrade to present.
	if _, _, _, status := parseDigestOutput("UNREADABLE\n100|5\n"); status != sizeUnreadable {
		t.Errorf("S4-01: UNREADABLE-then-total = status %v, want sizeUnreadable (fail closed)", status)
	}
	// And UNREADABLE followed by ABSENT then a total stays unreadable too.
	if _, _, _, status := parseDigestOutput("UNREADABLE\nABSENT\n100|5\n"); status != sizeUnreadable {
		t.Errorf("S4-01: UNREADABLE-then-ABSENT-then-total = status %v, want sizeUnreadable", status)
	}
}

// TestFaultSimGatherStreamUnreadableSticky locks in the S4-01 SIBLING fix: in the
// one-shot gather stream, a later present/absent frame for a domain that was already
// reported UNREADABLE (a duplicate/interleaved frame) must NOT downgrade the verdict to
// a clean present — UNREADABLE is fail-closed and sticky, in either order.
func TestFaultSimGatherStreamUnreadableSticky(t *testing.T) {
	// UNREADABLE first, then a present frame for the same domain.
	in := "DOC\td.com\nUNREADABLE\nDOC\td.com\n100\n200\nEND\nALLDONE\n"
	res, err := ParseGatherStream(strings.NewReader(in), 1, GatherHooks{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res["d.com"].Unreadable {
		t.Errorf("UNREADABLE-then-present: verdict must stay Unreadable, got %+v", res["d.com"])
	}
	// Present first, then UNREADABLE for the same domain: UNREADABLE must win (fail closed).
	in2 := "DOC\td.com\n100\nEND\nDOC\td.com\nUNREADABLE\nALLDONE\n"
	res2, err := ParseGatherStream(strings.NewReader(in2), 1, GatherHooks{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res2["d.com"].Unreadable {
		t.Errorf("present-then-UNREADABLE: UNREADABLE must win, got %+v", res2["d.com"])
	}
}

// FuzzParseFileList asserts the streaming list parser never panics AND never
// accepts a path that fails the traversal guard — the property that keeps a hostile
// source listing from steering tar outside the docroot. Run:
//
//	go test ./internal/webfiles -run x -fuzz FuzzParseFileList -fuzztime 60s
func FuzzParseFileList(f *testing.F) {
	seeds := []string{
		"f\t10\tindex.html",
		"d\t0\tsubdir",
		"l\t0\tlink",
		"f\t10\t../escape",
		"f\t10\t/etc/passwd",
		"f\tNaN\tx",
		"NODIR",
		"f\t10\ta\x00b",
		"",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, rec string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("parseFileLine panicked on %q: %v", rec, r)
			}
		}()
		fe, ok, _ := parseFileLine(rec)
		if ok {
			if err := validate.RelPath(fe.RelPath); err != nil {
				t.Fatalf("parseFileLine ACCEPTED an unsafe path %q (%v) from %q", fe.RelPath, err, rec)
			}
			if fe.Size < 0 {
				t.Fatalf("parseFileLine accepted a negative size %d from %q", fe.Size, rec)
			}
		}
	})
}

// FuzzParseManifestRecord asserts the same never-accept-an-unsafe-path invariant
// for the richer 5-field manifest record (including a TAB inside an f/d relpath).
// Run:
//
//	go test ./internal/webfiles -run x -fuzz FuzzParseManifestRecord -fuzztime 60s
func FuzzParseManifestRecord(f *testing.F) {
	seeds := []string{
		"f\t644\t10\tindex.html\t",
		"l\t777\t0\tlink\ttarget",
		"d\t755\t0\tsubdir\t",
		"f\t644\t10\t../escape\t",
		"f\t644\t10\tdir\tfile",
		"H\tdeadbeef\tindex.html",
		"",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, rec string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("parseManifestRecord panicked on %q: %v", rec, r)
			}
		}()
		rel, e, ok, _ := parseManifestRecord(rec)
		if ok {
			if err := validate.RelPath(rel); err != nil {
				t.Fatalf("parseManifestRecord ACCEPTED an unsafe path %q (%v) from %q", rel, err, rec)
			}
			if strings.ContainsRune(rel, '\t') {
				t.Fatalf("parseManifestRecord accepted a relpath with a TAB %q from %q", rel, rec)
			}
			if e.Size < 0 {
				t.Fatalf("parseManifestRecord accepted a negative size %d from %q", e.Size, rec)
			}
		}
		// The digest-record and status parsers must also never panic on the same input.
		_, _, _ = parseDigestRecord(rec)
		_, _, _, _ = parseDigestOutput(rec)
	})
}
