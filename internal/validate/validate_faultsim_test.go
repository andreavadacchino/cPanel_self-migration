package validate

import (
	"strings"
	"testing"
)

// S3 fault-injection for internal/validate — the second-layer guard that rejects
// obviously-unsafe identifiers/paths before they flow into remote scripts. The
// security-relevant invariant: whatever these accept must NOT carry the dangerous
// shapes they exist to reject (a `..` component, an absolute path, a path separator,
// or a control byte). The fuzz harnesses assert that ACCEPTED inputs always satisfy
// those guarantees, over arbitrary bytes; the table tests pin representative classes.

func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// TestFaultSimRelPathRejectsTraversal pins the path-traversal classes RelPath must
// reject (it ALLOWS spaces deliberately).
func TestFaultSimRelPathRejectsTraversal(t *testing.T) {
	bad := []string{
		"", "/abs", "/etc/passwd", "..", "../x", "a/../b", "a/b/..", "x/../../etc",
		"a\tb", "a\nb", "a\x00b", "a\x7fb", "ctrl\x01",
	}
	for _, s := range bad {
		if err := RelPath(s); err == nil {
			t.Errorf("RelPath(%q) = nil, want rejected", s)
		}
	}
	ok := []string{"a", "a/b/c", "my photo.jpg", ".hidden", "a..b", "dir/.well-known/x", "spaces are fine/here"}
	for _, s := range ok {
		if err := RelPath(s); err != nil {
			t.Errorf("RelPath(%q) = %v, want accepted", s, err)
		}
	}
}

// FuzzRelPath: an ACCEPTED relative path is never absolute, never contains a `..`
// component, and never carries a control byte. Run:
//
//	go test ./internal/validate -run x -fuzz FuzzRelPath -fuzztime 60s
func FuzzRelPath(f *testing.F) {
	for _, s := range []string{"a/b", "../x", "/abs", "a\x00b", "my file.txt", "a..b", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if RelPath(s) != nil {
			return
		}
		if s == "" {
			t.Fatalf("RelPath accepted the empty string")
		}
		if strings.HasPrefix(s, "/") {
			t.Fatalf("RelPath accepted an absolute path %q", s)
		}
		for _, seg := range strings.Split(s, "/") {
			if seg == ".." {
				t.Fatalf("RelPath accepted a path with a .. component: %q", s)
			}
		}
		if hasControl(s) {
			t.Fatalf("RelPath accepted a path with a control byte: %q", s)
		}
	})
}

// FuzzMailboxUser: an ACCEPTED mailbox user is a single safe path segment — never
// ""/"."/"..", never a separator, never a control byte or space. Run:
//
//	go test ./internal/validate -run x -fuzz FuzzMailboxUser -fuzztime 60s
func FuzzMailboxUser(f *testing.F) {
	for _, s := range []string{"info", "first.last", ".", "..", "a/b", "a\\b", "a b", "a\x00b", ".hidden"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if MailboxUser(s) != nil {
			return
		}
		switch {
		case s == "" || s == "." || s == "..":
			t.Fatalf("MailboxUser accepted the traversal/empty segment %q", s)
		case strings.ContainsAny(s, "/\\"):
			t.Fatalf("MailboxUser accepted a separator in %q", s)
		case strings.ContainsRune(s, ' '):
			t.Fatalf("MailboxUser accepted a space in %q", s)
		case hasControl(s):
			t.Fatalf("MailboxUser accepted a control byte in %q", s)
		}
	})
}

// FuzzDomain: an ACCEPTED domain carries no control byte, space, or shell/URL-
// dangerous character, and no traversal-ish dotting. Run:
//
//	go test ./internal/validate -run x -fuzz FuzzDomain -fuzztime 60s
func FuzzDomain(f *testing.F) {
	for _, s := range []string{"example.com", "xn--mnchen-3ya.de", "a..b", ".lead", "trail.", "a b.it", "a;b", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if Domain(s) != nil {
			return
		}
		switch {
		case s == "":
			t.Fatalf("Domain accepted empty")
		case hasControl(s) || strings.ContainsRune(s, ' '):
			t.Fatalf("Domain accepted a control byte/space in %q", s)
		case strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") || strings.Contains(s, ".."):
			t.Fatalf("Domain accepted bad dotting in %q", s)
		case strings.ContainsAny(s, "/\\?#%&'\"`;|*$(){}[]<>!"):
			t.Fatalf("Domain accepted a dangerous character in %q", s)
		}
	})
}

// FuzzDBName: an ACCEPTED database name carries no control byte, space, or
// shell-dangerous character. Run:
//
//	go test ./internal/validate -run x -fuzz FuzzDBName -fuzztime 60s
func FuzzDBName(f *testing.F) {
	for _, s := range []string{"acct_wp123", "a-b_c", "a b", "a;b", "a`b", "a\x00b", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if DBName(s) != nil {
			return
		}
		switch {
		case s == "":
			t.Fatalf("DBName accepted empty")
		case hasControl(s) || strings.ContainsRune(s, ' '):
			t.Fatalf("DBName accepted a control byte/space in %q", s)
		case strings.ContainsAny(s, "/\\`'\";|*$(){}[]<>!?#&"):
			t.Fatalf("DBName accepted a dangerous character in %q", s)
		}
	})
}
