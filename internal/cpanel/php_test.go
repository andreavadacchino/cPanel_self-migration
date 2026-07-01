package cpanel

import (
	"testing"
)

func TestParseListPHPVersions(t *testing.T) {
	data, err := parseUAPI[[]PHPVhostEntry]("LangPHP", "php_get_vhost_versions", fixture(t, "php_vhost_versions.json"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 2 {
		t.Fatalf("got %d vhosts, want 2", len(data))
	}
	if data[0].Vhost != "main.example" {
		t.Errorf("[0] vhost = %q", data[0].Vhost)
	}
	if data[0].Version != "ea-php81" {
		t.Errorf("[0] version = %q", data[0].Version)
	}
	if data[0].MainDomain != 1 {
		t.Errorf("[0] main_domain = %d, want 1", data[0].MainDomain)
	}
	if data[1].Version != "ea-php74" {
		t.Errorf("[1] version = %q", data[1].Version)
	}
}

func TestParseListPHPVersionsEmpty(t *testing.T) {
	empty := []byte(`{"result":{"data":[],"errors":null,"messages":null,"status":1}}`)
	data, err := parseUAPI[[]PHPVhostEntry]("LangPHP", "php_get_vhost_versions", empty)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("got %d, want 0", len(data))
	}
}
