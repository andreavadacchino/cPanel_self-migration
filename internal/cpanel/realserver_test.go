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

// Email list_auto_responders on a live server (2B-2-pre capture, .78 build
// 11.136) returns ONLY {email, subject} per entry — and email is the FULL
// address, with NO domain/interval/is_html/start/stop fields. The synthetic
// 3A fixture had email=<local> plus every detail field inline, which hid
// two real facts: the inventoried interval was always 0, and the collector's
// email+"@"+domain concatenation would have produced "addr@domain@" on real
// data. Details come only from get_auto_responder (2B-2-pre facts 2-3).
func TestEmailRealServerListAutoresponders(t *testing.T) {
	data := fixture(t, "email_autoresponders_realserver.json")
	entries, err := parseUAPI[[]AutoresponderEntry]("Email", "list_auto_responders", data)
	if err != nil {
		t.Fatalf("real-server list_auto_responders failed to parse: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Email != "test-2b2pre@giorginisposi.it" {
		t.Errorf("email = %q (real servers return the FULL address)", entries[0].Email)
	}
	if entries[0].Subject != "Assenza — test 2B-2 àèì" {
		t.Errorf("subject = %q", entries[0].Subject)
	}
	if entries[0].Domain != "" {
		t.Errorf("domain = %q, want empty (absent on real servers)", entries[0].Domain)
	}
}

// Email get_auto_responder (2B-2-pre fact 3): body verbatim with the
// ensure-trailing-newline normalization applied by cPanel, from stripped of
// any <address> part, interval/is_html bare numbers, start/stop JSON null
// when unset (flexInt64 decodes null → 0).
func TestEmailRealServerGetAutoresponder(t *testing.T) {
	data := fixture(t, "email_get_autoresponder_realserver.json")
	det, err := parseUAPI[AutoresponderDetail]("Email", "get_auto_responder", data)
	if err != nil {
		t.Fatalf("real-server get_auto_responder failed to parse: %v", err)
	}
	wantBody := "Riga 1 con accenti àèìòù.\nRiga 2 con \"virgolette\" e 'apici' e $VAR e |pipe|.\n\nRiga 4 dopo una riga vuota — fine test 2B-2.\n"
	if det.Body != wantBody {
		t.Errorf("body = %q, want %q", det.Body, wantBody)
	}
	if det.From != "Test 2B2" {
		t.Errorf("from = %q (cPanel stores it stripped of the <address> part)", det.From)
	}
	if det.Subject != "Assenza — test 2B-2 àèì" {
		t.Errorf("subject = %q", det.Subject)
	}
	if int64(det.Interval) != 8 || int64(det.IsHTML) != 0 {
		t.Errorf("interval/is_html = %d/%d, want 8/0", int64(det.Interval), int64(det.IsHTML))
	}
	if int64(det.Start) != 0 || int64(det.Stop) != 0 {
		t.Errorf("start/stop = %d/%d, want 0/0 (JSON null)", int64(det.Start), int64(det.Stop))
	}
	if det.Charset != "utf-8" {
		t.Errorf("charset = %q", det.Charset)
	}
}

// get_auto_responder on an address WITHOUT an autoresponder returns
// status:1 with data:{charset:"utf-8"} — NOT an error (2B-2-pre fact 4).
// Existence is provable only via list_auto_responders; the parser must
// yield a zero-valued detail, not fail.
func TestEmailRealServerGetAutoresponderAbsent(t *testing.T) {
	absent := []byte(`{"apiversion":3,"module":"Email","func":"get_auto_responder","result":{"status":1,"messages":null,"metadata":{},"data":{"charset":"utf-8"},"errors":null,"warnings":null}}`)
	det, err := parseUAPI[AutoresponderDetail]("Email", "get_auto_responder", absent)
	if err != nil {
		t.Fatalf("absent-shape get_auto_responder failed to parse: %v", err)
	}
	if det.Subject != "" || det.Body != "" || det.From != "" {
		t.Errorf("absent shape should be zero-valued, got %+v", det)
	}
	if det.Charset != "utf-8" {
		t.Errorf("charset = %q", det.Charset)
	}
}
