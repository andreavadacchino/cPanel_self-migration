package cpanel

import (
	"encoding/json"
	"strings"
	"testing"
)

// S4 fault-injection (parser/DoS robustness) for the UAPI JSON decode path. The
// JSON comes from the cPanel host over SSH; an exotic build or a corrupted/hostile
// response must degrade to a clean error, never a panic or stack overflow. parseUAPI
// must (a) error on unparseable JSON, (b) error on a non-success status, and (c)
// tolerate flexInt64 fields arriving as any JSON type without failing the decode.
// These complement runner_api_test.go / types_test.go with deeply-nested, huge, and
// wrong-type shapes plus a fuzz harness.

// TestFaultSimParseUAPIRejectsMalformed feeds JSON that must fail the decode (not
// panic). encoding/json's nesting-depth limit turns a pathologically deep payload
// into an error rather than a stack overflow.
func TestFaultSimParseUAPIRejectsMalformed(t *testing.T) {
	cases := map[string][]byte{
		"empty":           []byte(""),
		"not json":        []byte("not json at all"),
		"truncated":       []byte(`{"result":{"data":`),
		"nul bytes":       []byte("{\x00\x00}"),
		"deep array":      []byte(strings.Repeat("[", 200000)), // depth limit -> error, no overflow
		"deep object":     []byte(strings.Repeat(`{"a":`, 200000)),
		"wrong root type": []byte(`["not","an","object"]`),
		"result not obj":  []byte(`{"result":42}`),
	}
	for name, in := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s: parseUAPI panicked on %q: %v", name, in, r)
				}
			}()
			if _, err := parseUAPI[ListDomainsData]("M", "f", in); err == nil {
				t.Errorf("%s: parseUAPI(%.40q) = nil error, want an error", name, in)
			}
		}()
	}
}

// TestFaultSimParseUAPINonSuccessAlwaysErrors confirms any status other than 1
// (including a missing, zero, negative, or huge status) is reported as an error so
// a failed call can never be mistaken for success.
func TestFaultSimParseUAPINonSuccessAlwaysErrors(t *testing.T) {
	cases := []string{
		`{"result":{"data":{},"status":0}}`,
		`{"result":{"data":{},"status":-1}}`,
		`{"result":{"data":{}}}`, // status field absent -> defaults to 0 -> error
		`{"result":{"data":{},"status":2,"errors":["boom"]}}`,
		`{"result":{"data":{},"status":1.0}}`, // non-integer status: decode error, still not success
	}
	for _, in := range cases {
		if _, err := parseUAPI[ListDomainsData]("M", "f", []byte(in)); err == nil {
			t.Errorf("parseUAPI(%q) = nil error, want an error (non-success/garbled status)", in)
		}
	}
}

// TestFaultSimFlexInt64NeverFailsDecode asserts a disk_usage field arriving as any
// JSON type (number, quoted string, null, bool, object, array) never aborts the
// surrounding decode: the field is informational, so an exotic value yields 0, not
// a failed migration.
func TestFaultSimFlexInt64NeverFailsDecode(t *testing.T) {
	tmpl := `{"result":{"status":1,"data":[{"database":"d","disk_usage":%s,"users":[]}]}}`
	for _, du := range []string{`123`, `"456"`, `null`, `true`, `{}`, `[]`, `"not a number"`, `1.5`, `"  789  "`} {
		in := []byte(strings.Replace(tmpl, "%s", du, 1))
		got, err := parseUAPI[[]DatabaseEntry]("Mysql", "list_databases", in)
		if err != nil {
			t.Errorf("parseUAPI with disk_usage=%s errored: %v", du, err)
			continue
		}
		if len(got) != 1 || got[0].Database != "d" {
			t.Errorf("parseUAPI with disk_usage=%s = %+v, want one entry named d", du, got)
		}
	}
}

// TestFaultSimErrStringsNeverPanics throws degenerate error/messages payloads at
// errStrings: it must return cleanly (a slice or nil) for any JSON, never panic.
func TestFaultSimErrStringsNeverPanics(t *testing.T) {
	for _, raw := range []string{``, `null`, `[]`, `["a","b"]`, `"solo"`, `{"x":1}`, `42`, `[1,2,3]`, "\x00", `[`} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("errStrings panicked on %q: %v", raw, r)
				}
			}()
			_ = errStrings(json.RawMessage(raw))
		}()
	}
}

// FuzzUAPIDecode asserts the JSON decode path never panics over arbitrary bytes,
// across two representative envelope shapes (a flat struct and a flexInt64-bearing
// list). Run:
//
//	go test ./internal/cpanel -run x -fuzz FuzzUAPIDecode -fuzztime 60s
func FuzzUAPIDecode(f *testing.F) {
	seeds := []string{
		`{"result":{"status":1,"data":{"main_domain":"x.example","addon_domains":[]}}}`,
		`{"result":{"status":0,"errors":["boom"]}}`,
		`{"result":{"status":1,"data":[{"database":"d","disk_usage":"123","users":["u"]}]}}`,
		`{"result":{"data":{}}}`,
		`not json`,
		`{`,
		`[[[[[[`,
		"{\x00}",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("UAPI decode panicked on %q: %v", in, r)
			}
		}()
		b := []byte(in)
		_, _ = parseUAPI[ListDomainsData]("M", "f", b)
		_, _ = parseUAPI[[]DatabaseEntry]("M", "f", b)
		_, _ = parseUAPI[DomainsData]("M", "f", b)
		_ = errStrings(json.RawMessage(b))
	})
}
