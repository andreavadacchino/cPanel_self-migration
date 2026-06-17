package sshx

import (
	"bufio"
	"strings"
	"testing"
)

// scanNull must split a NUL-delimited stream into exactly the records between
// NULs, preserving spaces/newlines inside a record and yielding no spurious
// empty trailing token after a terminating NUL.
func TestScanNull(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"terminated", "a\x00b\x00", []string{"a", "b"}},
		{"unterminated tail", "a\x00b", []string{"a", "b"}},
		{"spaces and newlines kept", ".Sent Items/cur/1\x00a\nb\x00", []string{".Sent Items/cur/1", "a\nb"}},
		{"leading dash kept", "-weird.txt\x00", []string{"-weird.txt"}},
		{"single record no nul", "only", []string{"only"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := bufio.NewScanner(strings.NewReader(tc.in))
			sc.Split(scanNull)
			var got []string
			for sc.Scan() {
				got = append(got, sc.Text())
			}
			if err := sc.Err(); err != nil {
				t.Fatalf("scan error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("record[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
