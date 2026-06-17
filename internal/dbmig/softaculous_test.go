package dbmig

import "testing"

// A two-install registry mirroring the real Softaculous shape (trimmed to the
// fields we read, with correct byte lengths), including a leading PHP guard.
const softaculousSample = `<?php exit; ?>` +
	`a:2:{` +
	`s:8:"26_49851";a:6:{` +
	`s:8:"softpath";s:28:"/home/srcacct/addon1.example";` +
	`s:10:"softdomain";s:14:"addon1.example";` +
	`s:6:"softdb";s:13:"srcacct_wp694";` +
	`s:10:"softdbuser";s:10:"srcacct_u1";` +
	`s:10:"softdbpass";s:10:"p!p327!8pS";` +
	`s:8:"dbprefix";s:5:"wpid_";` +
	`}` +
	`s:8:"26_36971";a:6:{` +
	`s:8:"softpath";s:29:"/home/srcacct/domain4.example";` +
	`s:10:"softdomain";s:15:"domain4.example";` +
	`s:6:"softdb";s:13:"srcacct_wp590";` +
	`s:10:"softdbuser";s:13:"srcacct_wp590";` +
	`s:10:"softdbpass";s:10:"upIb932)S!";` +
	`s:8:"dbprefix";s:5:"wpqv_";` +
	`}` +
	`}`

func credForDB(creds []SiteCreds, db string) (SiteCreds, bool) {
	for _, c := range creds {
		if c.DBName == db {
			return c, true
		}
	}
	return SiteCreds{}, false
}

func TestParseSoftaculousExtractsCreds(t *testing.T) {
	creds := parseSoftaculous(softaculousSample)
	if len(creds) != 2 {
		t.Fatalf("expected 2 installs, got %d", len(creds))
	}

	addon, ok := credForDB(creds, "srcacct_wp694")
	if !ok {
		t.Fatal("wp694 not found")
	}
	if addon.DBUser != "srcacct_u1" {
		t.Errorf("wp694 user = %q, want srcacct_u1", addon.DBUser)
	}
	if addon.DBPassword != "p!p327!8pS" {
		t.Errorf("wp694 password = %q (special chars must survive)", addon.DBPassword)
	}
	if addon.Docroot != "/home/srcacct/addon1.example" {
		t.Errorf("wp694 docroot = %q", addon.Docroot)
	}
	if addon.TablePrefix != "wpid_" {
		t.Errorf("wp694 prefix = %q", addon.TablePrefix)
	}

	// The key win: the "orphan" wp590 (its docroot files are gone) still has
	// credentials here.
	orphan, ok := credForDB(creds, "srcacct_wp590")
	if !ok {
		t.Fatal("wp590 not found — Softaculous should recover its creds")
	}
	if orphan.DBPassword != "upIb932)S!" {
		t.Errorf("wp590 password = %q, want upIb932)S!", orphan.DBPassword)
	}
}

func TestParseSoftaculousEmptyOrGarbage(t *testing.T) {
	if got := parseSoftaculous(""); got != nil {
		t.Errorf("empty input should yield nil, got %v", got)
	}
	if got := parseSoftaculous("not serialized at all"); got != nil {
		t.Errorf("garbage should yield nil, got %v", got)
	}
}

func TestSerializedPayloadSkipsGuard(t *testing.T) {
	got := serializedPayload(`<?php die("x"); ?>a:1:{i:0;i:1;}`)
	if got != `a:1:{i:0;i:1;}` {
		t.Errorf("serializedPayload = %q", got)
	}
}
