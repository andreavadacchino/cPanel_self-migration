package accountinventory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizedInventoryJSON(t *testing.T) {
	inv := NormalizedInventory{
		Account: AccountInfo{
			User:        "srcuser",
			Host:        "1.2.3.4",
			CollectedAt: "2026-07-01T15:30:00Z",
			Side:        "source",
		},
		Domains: []DomainEntry{
			{Name: "main.example", Type: "main", DocumentRoot: "/home/srcuser/public_html"},
			{Name: "addon.example", Type: "addon", DocumentRoot: "/home/srcuser/addon.example"},
		},
		Mailboxes: []MailboxEntry{
			{Email: "info@main.example", Domain: "main.example", User: "info", DiskUsage: 1024},
		},
		Databases: []DatabaseEntry{
			{Name: "srcuser_wp1", DiskUsage: 50000, Users: []string{"srcuser_admin"}},
		},
		Warnings: []string{},
	}

	b, err := json.MarshalIndent(inv, "", "  ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(b)

	for _, want := range []string{
		`"account"`, `"user"`, `"host"`, `"collected_at"`, `"side"`,
		`"domains"`, `"name"`, `"type"`, `"document_root"`,
		`"mailboxes"`, `"email"`, `"domain"`, `"disk_usage"`,
		`"databases"`, `"users"`,
		`"dns"`, `"zones"`,
		`"cron"`, `"jobs"`, `"source_command"`,
		`"warnings"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON missing %s", want)
		}
	}
	for _, bad := range []string{"password", "token", "secret", "hash"} {
		if strings.Contains(strings.ToLower(s), bad) {
			t.Errorf("JSON contains sensitive keyword %q", bad)
		}
	}
}

func TestNormalizedInventoryRoundTrip(t *testing.T) {
	inv := NormalizedInventory{
		Account:   AccountInfo{User: "u", Host: "h", CollectedAt: "t", Side: "source"},
		Domains:   []DomainEntry{{Name: "d", Type: "main"}},
		Mailboxes: []MailboxEntry{{Email: "a@d", Domain: "d", User: "a"}},
		Databases: []DatabaseEntry{{Name: "db", Users: []string{"u"}}},
		Warnings:  []string{"w1"},
	}
	b, err := json.Marshal(inv)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got NormalizedInventory
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Account.User != "u" {
		t.Errorf("Account.User = %q, want %q", got.Account.User, "u")
	}
	if len(got.Domains) != 1 {
		t.Errorf("Domains: got %d, want 1", len(got.Domains))
	}
	if len(got.Mailboxes) != 1 {
		t.Errorf("Mailboxes: got %d, want 1", len(got.Mailboxes))
	}
	if len(got.Databases) != 1 {
		t.Errorf("Databases: got %d, want 1", len(got.Databases))
	}
	if len(got.Warnings) != 1 {
		t.Errorf("Warnings: got %d, want 1", len(got.Warnings))
	}
}

func TestEmptyInventoryNoNulls(t *testing.T) {
	inv := NewEmptyInventory("user", "1.2.3.4", "source")
	b, err := json.Marshal(inv)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(b)
	for _, field := range []string{"domains", "mailboxes", "databases", "warnings", "zones", "jobs", "environment"} {
		if strings.Contains(s, `"`+field+`":null`) {
			t.Errorf("%s is null, want empty array", field)
		}
	}
}
