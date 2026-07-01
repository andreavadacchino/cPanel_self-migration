package cpanel

import (
	"testing"
)

func TestParseListForwarders(t *testing.T) {
	data, err := parseUAPI[[]ForwarderEntry]("Email", "list_forwarders", fixture(t, "email_forwarders.json"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 2 {
		t.Fatalf("got %d forwarders, want 2", len(data))
	}
	if data[0].Dest != "info@main.example" {
		t.Errorf("[0] dest = %q, want %q", data[0].Dest, "info@main.example")
	}
	if data[0].Forward != "admin@gmail.com" {
		t.Errorf("[0] forward = %q, want %q", data[0].Forward, "admin@gmail.com")
	}
	if data[1].Forward != "sales@company.com, backup@company.com" {
		t.Errorf("[1] forward = %q", data[1].Forward)
	}
}

func TestParseListForwardersEmpty(t *testing.T) {
	empty := []byte(`{"result":{"data":[],"errors":null,"messages":null,"status":1}}`)
	data, err := parseUAPI[[]ForwarderEntry]("Email", "list_forwarders", empty)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("got %d, want 0", len(data))
	}
}

func TestParseListAutoresponders(t *testing.T) {
	data, err := parseUAPI[[]AutoresponderEntry]("Email", "list_auto_responders", fixture(t, "email_autoresponders.json"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 1 {
		t.Fatalf("got %d autoresponders, want 1", len(data))
	}
	if data[0].Email != "info" {
		t.Errorf("email = %q, want %q", data[0].Email, "info")
	}
	if data[0].Domain != "main.example" {
		t.Errorf("domain = %q, want %q", data[0].Domain, "main.example")
	}
	if data[0].Subject != "Out of office" {
		t.Errorf("subject = %q", data[0].Subject)
	}
	if data[0].Interval != 24 {
		t.Errorf("interval = %d, want 24", data[0].Interval)
	}
}

func TestParseListAutorespondersEmpty(t *testing.T) {
	empty := []byte(`{"result":{"data":[],"errors":null,"messages":null,"status":1}}`)
	data, err := parseUAPI[[]AutoresponderEntry]("Email", "list_auto_responders", empty)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("got %d, want 0", len(data))
	}
}
