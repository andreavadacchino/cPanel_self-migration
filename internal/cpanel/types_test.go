package cpanel

import (
	"encoding/json"
	"testing"
)

// TestFlexInt64 covers the number/string ambiguity (and the degenerate forms):
// flexInt64 must decode a bare number AND a quoted string, and never return an
// error (null/empty/non-numeric -> 0) so a surprising informational value cannot
// abort the surrounding decode.
func TestFlexInt64(t *testing.T) {
	cases := map[string]int64{
		`123`:                 123,
		`"123"`:               123,
		`0`:                   0,
		`"0"`:                 0,
		`-5`:                  -5,
		`"-5"`:                -5,
		`null`:                0,
		`""`:                  0,
		`"abc"`:               0, // non-numeric -> 0 (informational), no error
		`9223372036854775807`: 9223372036854775807,
	}
	for in, want := range cases {
		var f flexInt64
		if err := json.Unmarshal([]byte(in), &f); err != nil {
			t.Errorf("Unmarshal(%s) returned error %v, want none", in, err)
			continue
		}
		if int64(f) != want {
			t.Errorf("Unmarshal(%s) = %d, want %d", in, int64(f), want)
		}
	}
}

// TestDatabaseEntryDiskUsageNumberOrString proves the fix end-to-end: a
// list_databases payload whose disk_usage is a bare number on one row and a
// quoted string on another (plus null/empty) parses fully — the bug was that a
// single string value failed the ENTIRE []DatabaseEntry unmarshal.
func TestDatabaseEntryDiskUsageNumberOrString(t *testing.T) {
	in := `[
		{"database":"u_a","disk_usage":23363584,"users":["u1"]},
		{"database":"u_b","disk_usage":"12615680","users":["u2"]},
		{"database":"u_c","disk_usage":null,"users":[]},
		{"database":"u_d","disk_usage":"","users":[]}
	]`
	var got []DatabaseEntry
	if err := json.Unmarshal([]byte(in), &got); err != nil {
		t.Fatalf("unmarshal failed (the bug — a string disk_usage aborted the list): %v", err)
	}
	want := []struct {
		db   string
		disk int64
	}{
		{"u_a", 23363584},
		{"u_b", 12615680},
		{"u_c", 0},
		{"u_d", 0},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Database != w.db || int64(got[i].DiskUsage) != w.disk {
			t.Errorf("entry %d = {%q, %d}, want {%q, %d}", i, got[i].Database, int64(got[i].DiskUsage), w.db, w.disk)
		}
	}
}
