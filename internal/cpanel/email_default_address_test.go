package cpanel

import "testing"

// Real capture facts (PR7E_PRE_CAPTURES.md fact 2): the no-args call
// returns EVERY domain including subdomains, and the cPanel default
// value embeds literal double quotes inside the JSON string.
func TestParseListDefaultAddressesRealServer(t *testing.T) {
	data, err := parseUAPI[[]DefaultAddressEntry]("Email", "list_default_address", fixture(t, "email_default_address_realserver.json"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 7 {
		t.Fatalf("got %d domains, want 7", len(data))
	}
	const failDefault = `":fail: No Such User Here"`
	byDomain := map[string]string{}
	for _, e := range data {
		byDomain[e.Domain] = e.DefaultAddress
	}
	if got := byDomain["doctorbike.it"]; got != failDefault {
		t.Errorf("doctorbike.it default = %q, want %q (embedded quotes preserved)", got, failDefault)
	}
	// Subdomains are part of the same single response.
	if _, ok := byDomain["noleggio.doctorbike.it"]; !ok {
		t.Errorf("subdomain noleggio.doctorbike.it missing from %v", byDomain)
	}
	if _, ok := byDomain["italplant.com"]; !ok {
		t.Errorf("italplant.com missing from %v", byDomain)
	}
}

func TestParseListDefaultAddressesEmpty(t *testing.T) {
	empty := []byte(`{"result":{"data":[],"errors":null,"messages":null,"status":1}}`)
	data, err := parseUAPI[[]DefaultAddressEntry]("Email", "list_default_address", empty)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("got %d, want 0", len(data))
	}
}
