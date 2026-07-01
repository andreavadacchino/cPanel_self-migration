package cpanel

import (
	"testing"
)

func TestParseListFTPAccounts(t *testing.T) {
	data, err := parseUAPI[[]FTPAccountEntry]("Ftp", "list_ftp_with_disk", fixture(t, "ftp_list.json"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 2 {
		t.Fatalf("got %d accounts, want 2", len(data))
	}
	if data[0].Login != "main@main.example" {
		t.Errorf("[0] login = %q", data[0].Login)
	}
	if data[0].AcctType != "main" {
		t.Errorf("[0] accttype = %q", data[0].AcctType)
	}
	if data[1].Login != "uploads@main.example" {
		t.Errorf("[1] login = %q", data[1].Login)
	}
	if data[1].DiskUsed != 25 {
		t.Errorf("[1] diskused = %d, want 25", data[1].DiskUsed)
	}
}

func TestParseListFTPAccountsEmpty(t *testing.T) {
	empty := []byte(`{"result":{"data":[],"errors":null,"messages":null,"status":1}}`)
	data, err := parseUAPI[[]FTPAccountEntry]("Ftp", "list_ftp_with_disk", empty)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("got %d, want 0", len(data))
	}
}
