package phpserialize

import "testing"

// S4 fault-injection (parser/DoS robustness). The only input this package decodes
// is the per-account Softaculous registry (~/.softaculous/installations.php), read
// from the SOURCE host and therefore UNTRUSTED. A corrupt or hostile blob must
// degrade to a clean error, never a panic, hang, or OOM. These cases complement
// phpserialize_test.go with length-prefix lies, embedded delimiter/control bytes,
// and array-count lies; helpers unserializeNoPanic and itoa live there.

// TestFaultSimStringLengthLies feeds a declared length that disagrees with the
// real body. The closing `";` must then fail to match (clean error), and an
// over-declared length must hit the overrun guard — never an out-of-range slice.
func TestFaultSimStringLengthLies(t *testing.T) {
	cases := []string{
		`s:3:"abcdef";`,     // declared shorter than body: closing delimiter mismatches
		`s:6:"abc";`,        // declared longer than the bytes remaining: overrun rejected
		`s:5:"abc";`,        // declared longer than body but inside input: delimiter mismatch
		`s:1:"";`,           // declared 1 over an empty body
		`s:2147483648:"x";`, // length just over int32: rejected before any int(n) slice
	}
	for _, in := range cases {
		if v, err := unserializeNoPanic(t, in); err == nil {
			t.Errorf("Unserialize(%q) = %v, want an error (string length lie)", in, v)
		}
	}
}

// TestFaultSimStringWithEmbeddedDelimiters proves the anti-ambiguity property the
// package exists for: a length-prefixed string body may legitimately contain the
// bytes that would break a naive delimiter scanner (NUL, TAB, newline, quote,
// semicolon, brace). These must round-trip intact, NOT be truncated or rejected.
func TestFaultSimStringWithEmbeddedDelimiters(t *testing.T) {
	body := "a\x00b\tc\nd\"e;f}g{h"
	in := "s:" + itoa(len(body)) + `:"` + body + `";`
	got, err := unserializeNoPanic(t, in)
	if err != nil {
		t.Fatalf("Unserialize(%q) error: %v", in, err)
	}
	if got != body {
		t.Errorf("Unserialize(%q) = %q, want %q (embedded delimiters must survive)", in, got, body)
	}
}

// TestFaultSimArrayCountLies covers a declared element count that disagrees with
// the body: too many declared (truncated body) errors mid-parse; too few declared
// leaves the loop short of the closing brace, so the `}` expectation fails. Either
// way it is a clean error, never an infinite loop or panic.
func TestFaultSimArrayCountLies(t *testing.T) {
	cases := []string{
		`a:5:{i:0;N;}`, // declares 5 pairs, only 1 present: errors reading the 2nd key
		`a:1:{i:0;}`,   // declares 1 pair, only the key present: errors reading the value
		`a:0:{i:0;N;}`, // declares 0 but body has content: '}' expected, got 'i'
		`a:2:{i:0;N;}`, // declares 2 pairs, 1 present then '}' where a key is expected
	}
	for _, in := range cases {
		if v, err := unserializeNoPanic(t, in); err == nil {
			t.Errorf("Unserialize(%q) = %v, want an error (array count lie)", in, v)
		}
	}
}

// TestFaultSimNonScalarArrayKeys feeds array keys that PHP would never emit (an
// array or null as a key). keyString must stringify them without panicking; the
// decode either succeeds with a synthesized key or errors cleanly.
func TestFaultSimNonScalarArrayKeys(t *testing.T) {
	cases := []string{
		`a:1:{N;i:0;}`,       // null key -> stringified, value 0
		`a:1:{b:1;s:1:"x";}`, // bool key -> "true"
		`a:1:{a:0:{}i:0;}`,   // array key -> "map[]" via keyString default
		`a:1:{d:1.5;i:0;}`,   // float key
	}
	for _, in := range cases {
		// Any of these may decode or error; the invariant is only "no panic".
		_, _ = unserializeNoPanic(t, in)
	}
}

// TestFaultSimMalformedNumbers covers numeric fields that strconv must reject
// without the decoder advancing past the end of input.
func TestFaultSimMalformedNumbers(t *testing.T) {
	cases := []string{
		`i:;`,                     // empty int
		`i:--5;`,                  // double sign
		`i:0x10;`,                 // hex not accepted
		`i:99999999999999999999;`, // overflows int64
		`d:;`,                     // empty double
		`d:abc;`,                  // non-numeric double
		`a:abc:{}`,                // non-numeric array count
		`s:abc:"x";`,              // non-numeric string length
		`b:;`,                     // truncated bool (no value byte before ';')
	}
	for _, in := range cases {
		if v, err := unserializeNoPanic(t, in); err == nil {
			t.Errorf("Unserialize(%q) = %v, want an error (malformed number)", in, v)
		}
	}
}

// TestFaultSimNeverPanicsOnArbitraryBytes is a broad safety net: a grab-bag of
// hostile / control-byte / random-looking inputs must each either parse or return
// an error, but never panic.
func TestFaultSimNeverPanicsOnArbitraryBytes(t *testing.T) {
	cases := []string{
		"",
		"\x00\x00\x00",
		"s:\xff\xff:\"x\";",
		"a:\x00:{}",
		"O:8:\"stdClass\":0:{}", // objects are rejected, not parsed
		"i:\n;",
		"a:1:{",
		"}}}}}}",
		"s:4:\"\x00\x01\x02\x03\";",
		"d:1e999999;",
		"\x80\x81\x82\x83\x84",
	}
	for _, in := range cases {
		_, _ = unserializeNoPanic(t, in)
	}
}

// FuzzUnserialize asserts the package's core safety property over arbitrary bytes:
// the decoder never panics, regardless of how malformed or hostile the input. Run:
//
//	go test ./internal/phpserialize -run x -fuzz FuzzUnserialize -fuzztime 60s
func FuzzUnserialize(f *testing.F) {
	seeds := []string{
		`s:5:"hello";`,
		`i:42;`,
		`d:3.5;`,
		`b:1;`,
		`N;`,
		`a:2:{s:3:"foo";s:3:"bar";i:0;i:99;}`,
		`a:1:{s:8:"26_49851";a:1:{s:8:"softpath";s:5:"/home";}}`,
		`a:999999999:{}`,
		`s:-5:"x";`,
		`s:2147483648:"x";`,
		"O:1:\"X\":0:{}",
		"\x00\xff",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Unserialize panicked on %q: %v", s, r)
			}
		}()
		// A returned value or a returned error are both acceptable; the resource caps
		// (maxInputBytes/maxArrayElements/maxDepth) bound the work. Only a panic/hang
		// is a bug.
		_, _ = Unserialize(s)
	})
}
