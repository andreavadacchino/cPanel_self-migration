package cpanel

import (
	"strings"
	"testing"
)

// These tests pin the parsers against the ACTUAL shapes returned by a live
// cPanel 110.0 (build 131), captured during the PR5B smoke test. Synthetic
// fixtures used narrower shapes and hid three real bugs.

// FTP diskused arrives as a quoted string ("57632.08") on some accounts and
// as a bare float (13558.40) on others — a plain int64 field failed the whole
// unmarshal and dropped the ENTIRE FTP section.
func TestFTPRealServerDiskUsedStringAndFloat(t *testing.T) {
	data := fixture(t, "ftp_list_realserver.json")
	entries, err := parseUAPI[[]FTPAccountEntry]("Ftp", "list_ftp_with_disk", data)
	if err != nil {
		t.Fatalf("real-server FTP response failed to parse: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	byLogin := map[string]FTPAccountEntry{}
	for _, e := range entries {
		byLogin[e.Login] = e
	}
	// String "57632.08" → truncated to 57632.
	if got := byLogin["anonimo@doctorbike.it"].DiskUsed; got != 57632 {
		t.Errorf("quoted-string diskused = %d, want 57632", got)
	}
	// Bare float 13558.40 → truncated to 13558.
	if got := byLogin["italplant"].DiskUsed; got != 13558 {
		t.Errorf("float diskused = %d, want 13558", got)
	}
}

// SSL domains arrives as an ARRAY of strings; a plain string field failed the
// whole unmarshal and dropped the ENTIRE SSL section.
func TestSSLRealServerDomainsArray(t *testing.T) {
	data := fixture(t, "ssl_list_realserver.json")
	entries, err := parseUAPI[[]SSLCertEntry]("SSL", "list_certs", data)
	if err != nil {
		t.Fatalf("real-server SSL response failed to parse: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	// The SAN list is flattened to a comma-joined string the diff/policy
	// layers already key on.
	if got := entries[0].Domains; got != "*.doctorbike.it,doctorbike.it" {
		t.Errorf("domains = %q, want comma-joined SAN list", got)
	}
	if !strings.Contains(string(entries[1].Domains), "www.shop.doctorbike.it") {
		t.Errorf("second cert domains = %q", entries[1].Domains)
	}
}

// flexInt64 must truncate a float / float-string to its integer part rather
// than silently collapsing to 0 (which would zero out every FTP disk figure).
func TestFlexInt64AcceptsFloats(t *testing.T) {
	cases := []struct {
		raw  string
		want int64
	}{
		{`123`, 123},
		{`"123"`, 123},
		{`57632.08`, 57632},
		{`"57632.08"`, 57632},
		{`13558.40`, 13558},
		{`null`, 0},
		{`""`, 0},
		{`"abc"`, 0},
	}
	for _, c := range cases {
		var f flexInt64
		if err := f.UnmarshalJSON([]byte(c.raw)); err != nil {
			t.Errorf("UnmarshalJSON(%s) error: %v", c.raw, err)
		}
		if int64(f) != c.want {
			t.Errorf("flexInt64(%s) = %d, want %d", c.raw, int64(f), c.want)
		}
	}
}

// Cron redaction must mask a "secure=" auth token, not just "token=": real
// PrestaShop cron jobs authenticate with secure=<token>, which was leaking
// verbatim into the inventory and diff.
func TestRedactCronSecureToken(t *testing.T) {
	in := "wget -O- https://shop.example.it/modules/ets_seo/cronjob.php secure=ZdtUCJ64yUQe > /dev/null"
	got := RedactCronCommand(in)
	if strings.Contains(got, "ZdtUCJ64yUQe") {
		t.Errorf("secure= token leaked: %q", got)
	}
	if !strings.Contains(got, "cronjob.php") {
		t.Errorf("legit command mangled: %q", got)
	}
	// The space-separated form (secure=X as a bare arg) and the query-string
	// form (?...&secure=X) must both be caught.
	q := "curl 'https://x.y/cron?token=aa&secure=SEEKRETVALUE&id=1'"
	if got := RedactCronCommand(q); strings.Contains(got, "SEEKRETVALUE") {
		t.Errorf("query-string secure= leaked: %q", got)
	}
}

// Email list_pops_with_disk on a live server carries NO "diskusedquota"
// field; disk usage is in "_diskused" (bytes, as a quoted string). The old
// binding left every mailbox's disk usage at 0.
func TestEmailRealServerDiskUsedBytes(t *testing.T) {
	data := fixture(t, "email_list_pops_realserver.json")
	entries, err := parseUAPI[[]EmailAccountEntry]("Email", "list_pops_with_disk", data)
	if err != nil {
		t.Fatalf("real-server email response failed to parse: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	if got := int64(entries[0].DiskUsedBytes); got != 3779010736 {
		t.Errorf("disk used = %d, want 3779010736 (from _diskused)", got)
	}
	if got := int64(entries[1].DiskUsedBytes); got != 1085280485 {
		t.Errorf("disk used = %d, want 1085280485", got)
	}
	if got := int64(entries[2].DiskUsedBytes); got != 15545469799 {
		t.Errorf("disk used = %d, want 15545469799", got)
	}
}
