package cpanel

import (
	"testing"
)

func TestParseListEmailAccounts(t *testing.T) {
	data, err := parseUAPI[[]EmailAccountEntry]("Email", "list_pops_with_disk", fixture(t, "email_list_pops.json"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 3 {
		t.Fatalf("got %d accounts, want 3", len(data))
	}

	want := []struct {
		email string
		disk  int64
	}{
		{"admin@main.example", 51200},
		{"contact@addon.example", 0},
		{"info@main.example", 2048},
	}
	sorted := sortEmailAccounts(data)
	for i, w := range want {
		if sorted[i].Email != w.email {
			t.Errorf("[%d] email = %q, want %q", i, sorted[i].Email, w.email)
		}
		if sorted[i].DiskUsedQuota != w.disk {
			t.Errorf("[%d] disk = %d, want %d", i, sorted[i].DiskUsedQuota, w.disk)
		}
	}
}

func TestParseListEmailAccountsEmpty(t *testing.T) {
	empty := []byte(`{"result":{"data":[],"errors":null,"messages":null,"status":1}}`)
	data, err := parseUAPI[[]EmailAccountEntry]("Email", "list_pops_with_disk", empty)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("got %d accounts, want 0", len(data))
	}
}
