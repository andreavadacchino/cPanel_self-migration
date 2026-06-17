package migrate

import (
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/validate"
)

// S4 fault-injection (parser/DoS robustness) for the source-analysis record
// parsers. collectAnalysis/collectMailboxes stream NUL-delimited records from the
// SOURCE host into parseAnalysis/parseMailboxes; the bytes are untrusted. A
// malformed record must be skipped (never panic), and crucially the parser must
// NEVER surface a domain or mailbox user that fails validate.* — those identifiers
// flow into destination shell scripts, so an unvalidated one would be a real hazard.
// These complement collect_test.go with control-byte, overflow-field, and
// safety-invariant fuzzing.

// TestFaultSimParseAnalysisNeverPanics throws degenerate frames at parseAnalysis:
// embedded control bytes, ragged field counts, mixed NUL/newline framing, and a
// huge record count. Each must parse to a (possibly empty) result without panicking.
func TestFaultSimParseAnalysisNeverPanics(t *testing.T) {
	cases := []string{
		"",
		"\x00\x00\x00",
		"M",                               // bare type, no fields
		"M\t",                             // type with one empty field
		"D",                               // bare domain row
		"X\tjunk\tjunk\tjunk\tjunk",       // unknown record type
		"M\tok.example\tuser\t2\tSHA-512", // active flag not 0/1
		"M\tok.example\tuser\t1\tBOGUS",   // unknown scheme
		"D\tok.example\textra\tfields",    // wrong field count for D
		"\n\n\n",
		strings.Repeat("D\tok.example\n", 1000),
	}
	for _, in := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("parseAnalysis panicked on %q: %v", in, r)
				}
			}()
			_ = parseAnalysis(in)
		}()
	}
}

// TestFaultSimParseAnalysisDropsHostileIdentifiers confirms a record whose domain
// or mailbox user is unsafe (traversal, separator, control byte) is dropped rather
// than carried through — fail closed on identifiers bound for dest scripts.
func TestFaultSimParseAnalysisDropsHostileIdentifiers(t *testing.T) {
	out := "D\tgood.example\x00" +
		"M\tgood.example\tinfo\t1\tSHA-512\x00" +
		"M\tgood.example\t..\t1\tSHA-512\x00" + // mailbox user "." traversal component
		"M\tgood.example\ta/b\t1\tSHA-512\x00" + // user with a path separator
		"M\t../evil\tinfo\t1\tSHA-512\x00" + // domain traversal
		"M\tgood.example\tinfo\x00bad\t1\tSHA-512\x00" // NUL splits the record early
	domains := parseAnalysis(out)
	if len(domains) != 1 || domains[0].Name != "good.example" {
		t.Fatalf("got domains %+v, want only good.example", domains)
	}
	if len(domains[0].Mailboxes) != 1 || domains[0].Mailboxes[0].User != "info" {
		t.Fatalf("got mailboxes %+v, want only info", domains[0].Mailboxes)
	}
}

// FuzzParseAnalysis asserts parseAnalysis never panics and never emits an
// identifier that fails the validators it is supposed to enforce. Run:
//
//	go test ./internal/migrate -run x -fuzz FuzzParseAnalysis -fuzztime 60s
func FuzzParseAnalysis(f *testing.F) {
	seeds := []string{
		"D\tdomain2.example\nM\tdomain2.example\tinfo\t1\tSHA-512\n",
		"D\tvalid.example\x00M\tvalid.example\tinfo\t1\tSHA-512\x00",
		"M\t../evil\tinfo\t1\tSHA-512",
		"M\tok.example\ta/b\t1\tSHA-512",
		"X\tjunk",
		"\x00\x00",
		"",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, out string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("parseAnalysis panicked on %q: %v", out, r)
			}
		}()
		for _, d := range parseAnalysis(out) {
			if err := validate.Domain(d.Name); err != nil {
				t.Fatalf("parseAnalysis emitted an invalid domain %q (%v) from %q", d.Name, err, out)
			}
			for _, mb := range d.Mailboxes {
				if err := validate.MailboxUser(mb.User); err != nil {
					t.Fatalf("parseAnalysis emitted an invalid mailbox user %q (%v) from %q", mb.User, err, out)
				}
				if !validAnalysisScheme(mb.Scheme) {
					t.Fatalf("parseAnalysis emitted an invalid scheme %q from %q", mb.Scheme, out)
				}
			}
		}
	})
}

// FuzzParseMailboxes asserts the inventory parser never panics and never emits an
// invalid domain/user (both the NUL/tab and legacy pipe framings). Run:
//
//	go test ./internal/migrate -run x -fuzz FuzzParseMailboxes -fuzztime 60s
func FuzzParseMailboxes(f *testing.F) {
	seeds := []string{
		"M\taddon1.example\tinfo\t$6$rfzE0OGZ$Xq/n.Ro7.P\x00",
		"addon1.example|info|$6$rfzE0OGZ$Xq/n.Ro7.P\n",
		"M\tbad|domain\tinfo\t$6$bad\x00",
		"M\tvalid.example\tbad/user\t$6$bad\x00",
		"\x00",
		"",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, out string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("parseMailboxes panicked on %q: %v", out, r)
			}
		}()
		for _, mb := range parseMailboxes(out) {
			if err := validate.Domain(mb.Domain); err != nil {
				t.Fatalf("parseMailboxes emitted an invalid domain %q (%v) from %q", mb.Domain, err, out)
			}
			if err := validate.MailboxUser(mb.User); err != nil {
				t.Fatalf("parseMailboxes emitted an invalid user %q (%v) from %q", mb.User, err, out)
			}
		}
	})
}
