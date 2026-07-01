package cpanel

import (
	"strings"
	"testing"
)

func TestParseListSSLCerts(t *testing.T) {
	data, err := parseUAPI[[]SSLCertEntry]("SSL", "list_certs", fixture(t, "ssl_list_certs.json"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 2 {
		t.Fatalf("got %d certs, want 2", len(data))
	}
	if data[0].ID != "cert_abc123" {
		t.Errorf("[0] id = %q", data[0].ID)
	}
	if !strings.Contains(data[0].Domains, "main.example") {
		t.Errorf("[0] domains = %q, want to contain main.example", data[0].Domains)
	}
	if data[0].IssuerCN != "R3" {
		t.Errorf("[0] issuer = %q", data[0].IssuerCN)
	}
	if data[0].NotAfter == 0 {
		t.Error("[0] not_after is zero")
	}
}

func TestParseListSSLCertsEmpty(t *testing.T) {
	empty := []byte(`{"result":{"data":[],"errors":null,"messages":null,"status":1}}`)
	data, err := parseUAPI[[]SSLCertEntry]("SSL", "list_certs", empty)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("got %d, want 0", len(data))
	}
}

func TestSSLCertNoPrivateKey(t *testing.T) {
	raw := fixture(t, "ssl_list_certs.json")
	s := strings.ToLower(string(raw))
	for _, bad := range []string{"private", "key_pem", "BEGIN RSA"} {
		if strings.Contains(s, strings.ToLower(bad)) {
			t.Errorf("fixture contains private key material: %q", bad)
		}
	}
}
