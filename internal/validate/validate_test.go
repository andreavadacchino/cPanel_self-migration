package validate

import "testing"

func TestMailboxUser(t *testing.T) {
	ok := []string{
		"info", "first.last", "no-reply", "github_support", "a1",
		".hidden", // a leading dot alone is not traversal — must stay valid (permissive)
		"a..b",    // ".." as a substring (not the whole value) is a single segment — valid
		"a.",      // a trailing dot is not a path component — valid
	}
	for _, s := range ok {
		if err := MailboxUser(s); err != nil {
			t.Errorf("MailboxUser(%q) should be valid, got %v", s, err)
		}
	}
	// "." and ".." are path-traversal components: a mailbox user is a single path
	// segment in $HOME/mail/<dom>/<user>, so those would resolve to the wrong dir.
	bad := []string{"", "a b", "a/b", "a\\b", "a\nb", "a\x00b", "a\tb", ".", ".."}
	for _, s := range bad {
		if err := MailboxUser(s); err == nil {
			t.Errorf("MailboxUser(%q) should be invalid", s)
		}
	}
}

func TestDomain(t *testing.T) {
	ok := []string{
		"domain4.example", "addon1.example", "sub1.example", "domain3.example",
		"xn--mnchen-3ya.de", // punycode IDN — must be accepted (permissive)
	}
	for _, s := range ok {
		if err := Domain(s); err != nil {
			t.Errorf("Domain(%q) should be valid, got %v", s, err)
		}
	}
	bad := []string{
		"", ".domain4.example", "domain4.example.", "tis24..it",
		"a b.it", "a/b.it", "a;b.it", "a$b.it", "a`b.it", "a\nb.it",
	}
	for _, s := range bad {
		if err := Domain(s); err == nil {
			t.Errorf("Domain(%q) should be invalid", s)
		}
	}
}

func TestDBName(t *testing.T) {
	ok := []string{"srcacct_wp694", "destacct_u1", "db1", "a_b_c"}
	for _, s := range ok {
		if err := DBName(s); err != nil {
			t.Errorf("DBName(%q) should be valid, got %v", s, err)
		}
	}
	bad := []string{"", "a b", "a;b", "a'b", "a\"b", "a`b", "a/b", "a$b", "a\nb"}
	for _, s := range bad {
		if err := DBName(s); err == nil {
			t.Errorf("DBName(%q) should be invalid", s)
		}
	}
}

func TestRelPath(t *testing.T) {
	ok := []string{
		"wp-config.php",
		"site2.example/test/wp-config.php",
		"cur/1.M2.host:2,S",
		"a/b/c.txt",
		".Sent Items/cur/1.M2.host:2,S", // IMAP folder name with a space
		"uploads/my photo.jpg",          // web upload with a space
		"a file with spaces.txt",        // leading/internal spaces are fine
		"-weird.txt",                    // a leading dash is a valid filename (NUL list handles it)
	}
	for _, s := range ok {
		if err := RelPath(s); err != nil {
			t.Errorf("RelPath(%q) should be valid, got %v", s, err)
		}
	}
	bad := []string{
		"",
		"/etc/passwd",      // absolute
		"../../etc/passwd", // traversal
		"a/../../b",        // traversal in the middle
		"..",               // bare parent
		"a/b\x00c",         // NUL (record delimiter)
		"a/b\tc",           // TAB (field delimiter)
		"a/b\nc",           // newline
		"a/b\rc",           // CR
	}
	for _, s := range bad {
		if err := RelPath(s); err == nil {
			t.Errorf("RelPath(%q) should be invalid", s)
		}
	}
	// A filename that merely CONTAINS ".." but is not a "../" component is fine.
	if err := RelPath("my..file.txt"); err != nil {
		t.Errorf(`RelPath("my..file.txt") should be valid (".." not a path component), got %v`, err)
	}
}
