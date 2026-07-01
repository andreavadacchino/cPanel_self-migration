package cpanel

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// UAPI DNS::parse_zone
// ---------------------------------------------------------------------------

func TestParseDNSZoneUAPI(t *testing.T) {
	data := fixture(t, "dns_parse_zone.json")
	records, err := parseUAPIDNSZone(data)
	if err != nil {
		t.Fatalf("parseUAPIDNSZone: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("expected records, got none")
	}

	want := map[string]bool{"A": false, "AAAA": false, "CNAME": false, "MX": false, "TXT": false, "NS": false}
	for _, r := range records {
		want[r.Type] = true
	}
	for typ, found := range want {
		if !found {
			t.Errorf("missing record type %s", typ)
		}
	}
}

func TestUAPIDNSBase64Decode(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("example.com."))
	got, err := decodeB64Field(encoded)
	if err != nil {
		t.Fatalf("decodeB64Field: %v", err)
	}
	if got != "example.com." {
		t.Errorf("got %q, want %q", got, "example.com.")
	}
}

func TestUAPIDNSBase64NonUTF8(t *testing.T) {
	raw := []byte{0xff, 0xfe, 0x00, 0x01}
	encoded := base64.StdEncoding.EncodeToString(raw)
	got, err := decodeB64Field(encoded)
	if err != nil {
		t.Fatalf("should not error on non-UTF8: %v", err)
	}
	if got == "" {
		t.Error("should return non-empty string for non-UTF8 data")
	}
}

func TestUAPIDNSBase64InvalidEncoding(t *testing.T) {
	_, err := decodeB64Field("!!!not-base64!!!")
	if err == nil {
		t.Error("expected error for invalid base64")
	}
}

func TestUAPIDNSTXTMultiSegmentJoined(t *testing.T) {
	data := fixture(t, "dns_parse_zone.json")
	records, err := parseUAPIDNSZone(data)
	if err != nil {
		t.Fatalf("parseUAPIDNSZone: %v", err)
	}
	// Real servers split long TXT (DKIM) into 255-char segments; RFC 1035
	// concatenates them. The parser must join all segments, not keep only
	// the first (which would truncate DKIM keys).
	found := false
	for _, r := range records {
		if r.Type == "TXT" && r.Name == "dkim._domainkey.example.com." {
			found = true
			want := "v=DKIM1; k=rsa; p=MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AQEFAAOCAQ8AMIIBCgKCAQEA"
			if r.TxtData != want {
				t.Errorf("multi-segment TXT not joined:\ngot  %q\nwant %q", r.TxtData, want)
			}
			if r.Value != want {
				t.Errorf("multi-segment TXT Value not joined: %q", r.Value)
			}
		}
	}
	if !found {
		t.Fatal("multi-segment TXT record not found in fixture")
	}
}

func TestUAPIDNSParseError(t *testing.T) {
	data := []byte(`{"result":{"data":null,"errors":["The function \"parse_zone\" does not exist in module \"DNS\"."],"status":0}}`)
	_, err := parseUAPIDNSZone(data)
	if err == nil {
		t.Fatal("expected error for UAPI failure")
	}
}

// ---------------------------------------------------------------------------
// API2 ZoneEdit::fetchzone_records
// ---------------------------------------------------------------------------

func TestParseAPI2DNSZone(t *testing.T) {
	data := fixture(t, "dns_fetchzone_records.json")
	records, err := parseAPI2DNSZone(data)
	if err != nil {
		t.Fatalf("parseAPI2DNSZone: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("expected records, got none")
	}

	want := map[string]bool{
		"A": false, "AAAA": false, "CNAME": false,
		"MX": false, "TXT": false, "NS": false, "SOA": false,
	}
	for _, r := range records {
		if _, ok := want[r.Type]; ok {
			want[r.Type] = true
		}
	}
	for typ, found := range want {
		if !found {
			t.Errorf("missing record type %s", typ)
		}
	}
}

func TestAPI2DNSRecordFields(t *testing.T) {
	data := fixture(t, "dns_fetchzone_records.json")
	records, err := parseAPI2DNSZone(data)
	if err != nil {
		t.Fatalf("parseAPI2DNSZone: %v", err)
	}

	// Field values mirror the live-server shape: cPanel api2 omits the
	// trailing dot on exchange/cname/nsdname and quotes MX preference ("10").
	tests := []struct {
		typ     string
		check   func(DNSRecord) bool
		desc    string
	}{
		{"A", func(r DNSRecord) bool { return r.Address == "192.168.1.1" }, "address"},
		{"AAAA", func(r DNSRecord) bool { return r.Address == "2001:db8::1" }, "IPv6 address"},
		{"CNAME", func(r DNSRecord) bool { return r.Target == "example.com" }, "cname target"},
		{"MX", func(r DNSRecord) bool { return r.Exchange == "mail.example.com" && r.Priority == 10 }, "exchange+priority from quoted string"},
		{"TXT", func(r DNSRecord) bool { return r.TxtData == "v=spf1 include:_spf.google.com ~all" }, "txtdata"},
		{"NS", func(r DNSRecord) bool { return r.Target == "ns1.example.com" }, "nsdname"},
	}

	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			found := false
			for _, r := range records {
				if r.Type == tt.typ && tt.check(r) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("no %s record with expected %s", tt.typ, tt.desc)
			}
		})
	}
}

func TestAPI2DNSRawRecordPreserved(t *testing.T) {
	data := fixture(t, "dns_fetchzone_records.json")
	records, err := parseAPI2DNSZone(data)
	if err != nil {
		t.Fatalf("parseAPI2DNSZone: %v", err)
	}

	foundRaw := false
	for _, r := range records {
		if r.Raw != nil {
			foundRaw = true
			var m map[string]interface{}
			if err := json.Unmarshal(r.Raw, &m); err != nil {
				t.Errorf("raw field is not valid JSON: %v", err)
			}
			break
		}
	}
	if !foundRaw {
		t.Error("expected at least one record with raw metadata (:RAW type)")
	}
}

func TestAPI2DNSParseError(t *testing.T) {
	data := []byte(`{"cpanelresult":{"data":[],"event":{"result":0},"error":"Zone not found"}}`)
	_, err := parseAPI2DNSZone(data)
	if err == nil {
		t.Fatal("expected error for API2 failure")
	}
}

func TestAPI2DNSMalformedJSON(t *testing.T) {
	_, err := parseAPI2DNSZone([]byte(`{{{not json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestAPI2DNSEventResultAsString(t *testing.T) {
	data := []byte(`{"cpanelresult":{"data":[{"line":1,"type":"A","name":"x.com.","address":"1.2.3.4","ttl":14400,"class":"IN","record":"x"}],"event":{"result":"1"}}}`)
	records, err := parseAPI2DNSZone(data)
	if err != nil {
		t.Fatalf("event.result as string '1' should succeed: %v", err)
	}
	if len(records) != 1 {
		t.Errorf("expected 1 record, got %d", len(records))
	}
}

func TestAPI2DNSEventResultStringFailure(t *testing.T) {
	data := []byte(`{"cpanelresult":{"data":[],"event":{"result":"0"},"error":"Zone not found"}}`)
	_, err := parseAPI2DNSZone(data)
	if err == nil {
		t.Fatal("event.result '0' should fail")
	}
}

// ---------------------------------------------------------------------------
// RunAPI2 infrastructure
// ---------------------------------------------------------------------------

func TestAPI2ArgsScript(t *testing.T) {
	script, env := api2ArgsScript("ZoneEdit", "fetchzone_records", map[string]string{"domain": "example.com"})
	if !findSubstringInTest(script, "cpapi2 --output=json") {
		t.Errorf("script missing cpapi2 command: %s", script)
	}
	if !findSubstringInTest(script, "ZoneEdit") {
		t.Errorf("script missing module: %s", script)
	}
	if !findSubstringInTest(script, "fetchzone_records") {
		t.Errorf("script missing function: %s", script)
	}
	if len(env) == 0 {
		t.Error("expected env vars for args")
	}
}

func findSubstringInTest(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Normalization common
// ---------------------------------------------------------------------------

func TestDNSRecordValueField(t *testing.T) {
	data := fixture(t, "dns_fetchzone_records.json")
	records, err := parseAPI2DNSZone(data)
	if err != nil {
		t.Fatalf("parseAPI2DNSZone: %v", err)
	}

	for _, r := range records {
		// Pseudo-records (:RAW comments, $TTL directive) and SOA carry
		// their payload in Raw, not Value.
		if r.Type == ":RAW" || r.Type == "SOA" || r.Type == "$TTL" {
			continue
		}
		if r.Value == "" {
			t.Errorf("record type %s has empty Value field", r.Type)
		}
	}
}

func TestDNSRecordAllHaveTTLAndName(t *testing.T) {
	data := fixture(t, "dns_fetchzone_records.json")
	records, err := parseAPI2DNSZone(data)
	if err != nil {
		t.Fatalf("parseAPI2DNSZone: %v", err)
	}

	for _, r := range records {
		// :RAW comments and the $TTL directive have no owner name.
		if r.Type == ":RAW" || r.Type == "$TTL" {
			continue
		}
		if r.Name == "" {
			t.Errorf("record type %s line %d has empty Name", r.Type, r.Line)
		}
	}
}
