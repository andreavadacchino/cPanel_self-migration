package phpserialize

import "testing"

// asMap returns m[key] as a sub-map if present and a map, else nil. A test-only
// navigation helper (production code reads scalars via AsString); kept here so the
// nested-structure tests below stay readable.
func asMap(m map[string]Value, key string) map[string]Value {
	if v, ok := m[key]; ok {
		if sub, ok := v.(map[string]Value); ok {
			return sub
		}
	}
	return nil
}

func TestUnserializeScalars(t *testing.T) {
	cases := []struct {
		in   string
		want Value
	}{
		{`s:5:"hello";`, "hello"},
		{`i:42;`, int64(42)},
		{`i:-7;`, int64(-7)},
		{`b:1;`, true},
		{`b:0;`, false},
		{`N;`, nil},
		{`d:3.5;`, 3.5},
	}
	for _, c := range cases {
		got, err := Unserialize(c.in)
		if err != nil {
			t.Errorf("Unserialize(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Unserialize(%q) = %v (%T), want %v (%T)", c.in, got, got, c.want, c.want)
		}
	}
}

func TestUnserializeStringWithSpecialChars(t *testing.T) {
	// A password containing the delimiters that would break a naive parser:
	// quotes, semicolons, braces. Length-prefixed parsing must handle them.
	pw := `p"a;s{s}1`
	in := `s:9:"` + pw + `";`
	got, err := Unserialize(in)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if got != pw {
		t.Errorf("got %q, want %q", got, pw)
	}
}

func TestUnserializeArray(t *testing.T) {
	// a:2:{s:3:"foo";s:3:"bar";i:0;i:99;}
	in := `a:2:{s:3:"foo";s:3:"bar";i:0;i:99;}`
	got, err := Unserialize(in)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	m, ok := got.(map[string]Value)
	if !ok {
		t.Fatalf("expected map, got %T", got)
	}
	if AsString(m, "foo") != "bar" {
		t.Errorf("foo = %v, want bar", m["foo"])
	}
	if m["0"] != int64(99) {
		t.Errorf("key 0 = %v, want 99", m["0"])
	}
}

func TestUnserializeRejectsInflatedCount(t *testing.T) {
	// The DoS case from the report: a huge declared count with almost no body.
	// Must be rejected cheaply (no giant preallocation, no OOM), not looped.
	_, err := Unserialize(`a:999999999:{}`)
	if err == nil {
		t.Fatal("expected an error for an inflated array count, got nil")
	}
}

func TestUnserializeRejectsCountAboveLimit(t *testing.T) {
	// A count above maxArrayElements but with enough bytes to pass the
	// remaining-input check must still be rejected by the hard cap.
	body := make([]byte, 0, maxArrayElements+50)
	body = append(body, []byte("a:"+itoa(maxArrayElements+1)+":{")...)
	for len(body) < maxArrayElements+30 {
		body = append(body, 'x')
	}
	if _, err := Unserialize(string(body)); err == nil {
		t.Fatal("expected an error for count above maxArrayElements")
	}
}

func TestUnserializeRejectsDeepNesting(t *testing.T) {
	// Build nesting deeper than maxDepth: a:1:{i:0;a:1:{i:0; ... }}.
	var b []byte
	depth := maxDepth + 5
	for i := 0; i < depth; i++ {
		b = append(b, []byte("a:1:{i:0;")...)
	}
	b = append(b, []byte("i:0;")...) // innermost value
	for i := 0; i < depth; i++ {
		b = append(b, '}')
	}
	if _, err := Unserialize(string(b)); err == nil {
		t.Fatal("expected an error for nesting deeper than maxDepth")
	}
}

func TestUnserializeRejectsOversizedInput(t *testing.T) {
	big := make([]byte, maxInputBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	if _, err := Unserialize(string(big)); err == nil {
		t.Fatal("expected an error for input larger than maxInputBytes")
	}
}

// itoa is a tiny local int->string to avoid importing strconv in the test.
func itoa(n int) string {
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

func TestUnserializeNestedSoftaculousShape(t *testing.T) {
	// Mirrors one Softaculous installation entry (trimmed): an outer array whose
	// value is an inner array with the DB fields and a nested array (fileindex).
	in := `a:1:{s:8:"26_49851";a:4:{` +
		`s:8:"softpath";s:14:"/home/u/site.x";` +
		`s:6:"softdb";s:7:"u_wp123";` + // "u_wp123" is 7 bytes
		`s:9:"fileindex";a:1:{i:0;s:9:"index.php";}` + // array value: no trailing ;
		`s:10:"softdbpass";s:5:"p!p@1";` +
		`}}`
	got, err := Unserialize(in)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	outer := got.(map[string]Value)
	inst := asMap(outer, "26_49851")
	if inst == nil {
		t.Fatal("installation entry missing")
	}
	if AsString(inst, "softpath") != "/home/u/site.x" {
		t.Errorf("softpath = %q", AsString(inst, "softpath"))
	}
	if AsString(inst, "softdb") != "u_wp123" {
		t.Errorf("softdb = %q", AsString(inst, "softdb"))
	}
	if AsString(inst, "softdbpass") != "p!p@1" {
		t.Errorf("softdbpass = %q", AsString(inst, "softdbpass"))
	}
	// Nested array is parsed and skippable.
	if fi := asMap(inst, "fileindex"); fi == nil || AsString(fi, "0") != "index.php" {
		t.Errorf("fileindex not parsed: %v", inst["fileindex"])
	}
}

// unserializeNoPanic calls Unserialize and turns a panic into a test failure, so
// a malformed/hostile blob can NEVER crash the migration (the registry is read
// from the source and must degrade gracefully, not panic).
func unserializeNoPanic(t *testing.T, in string) (v Value, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Unserialize(%q) PANICKED: %v", in, r)
		}
	}()
	return Unserialize(in)
}

// TestUnserializeRejectsMalformedStringLength covers the string-length panics: a
// negative length and a length near/over MaxInt64 must return an error, not slice
// out of range.
func TestUnserializeRejectsMalformedStringLength(t *testing.T) {
	cases := []string{
		`s:-5:"hello";`,               // negative length
		`s:9223372036854775807:"x";`,  // MaxInt64: d.pos+int(n) would overflow to negative
		`s:99999999999999999999:"x";`, // overflows int64 entirely (ParseInt range error)
		`s:10:"abc";`,                 // declared longer than the actual bytes
	}
	for _, in := range cases {
		if v, err := unserializeNoPanic(t, in); err == nil {
			t.Errorf("Unserialize(%q) = %v, want an error", in, v)
		}
	}
}

// TestUnserializeRejectsTruncatedInput feeds inputs cut off at every structural
// boundary. Each must error (not panic) — in particular `s:5` and `a:1`, where a
// missing delimiter previously pushed the cursor past the end.
func TestUnserializeRejectsTruncatedInput(t *testing.T) {
	cases := []string{
		``,          // empty
		`s`,         // bare type byte
		`s:`,        // missing length
		`s:5`,       // length, no ':' delimiter (readIntUntil over-advance case)
		`s:5:`,      // missing opening quote
		`s:5:"ab`,   // string body truncated
		`s:3:"abc`,  // missing closing ";
		`i`,         // bare int
		`i:`,        // int, no digits/delimiter
		`i:42`,      // int, missing ';'
		`a`,         // bare array
		`a:`,        // array, no count
		`a:1`,       // count, no ':' delimiter
		`a:1:`,      // missing '{'
		`a:1:{`,     // open brace, no elements
		`a:1:{i:0;`, // one key, missing value + close
		`b`,         // bare bool
		`b:`,        // bool, no value
		`d:`,        // double, no number
		`N`,         // null missing ';'
	}
	for _, in := range cases {
		if v, err := unserializeNoPanic(t, in); err == nil {
			t.Errorf("Unserialize(%q) = %v, want an error (truncated/malformed)", in, v)
		}
	}
}

// TestUnserializeNeverPanicsOnAnyPrefix exercises every truncation point of a
// valid, non-trivial blob: no prefix may panic, every short prefix must error,
// and the full string must still parse. This is the broad safety net against any
// slice/index overrun on partial input.
func TestUnserializeNeverPanicsOnAnyPrefix(t *testing.T) {
	full := `a:1:{s:8:"26_49851";a:3:{` +
		`s:8:"softpath";s:14:"/home/u/site.x";` +
		`s:6:"softdb";s:7:"u_wp123";` +
		`s:10:"softdbpass";s:5:"p!p@1";` +
		`}}`
	for i := 0; i <= len(full); i++ {
		in := full[:i]
		_, err := unserializeNoPanic(t, in)
		switch {
		case i < len(full) && err == nil:
			t.Errorf("prefix of length %d (%q) parsed without error, want a truncation error", i, in)
		case i == len(full) && err != nil:
			t.Errorf("full input failed to parse: %v", err)
		}
	}
}
