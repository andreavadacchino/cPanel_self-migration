package accountinventory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteInventoryJSON(t *testing.T) {
	dir := t.TempDir()
	inv := NormalizedInventory{
		Account:   AccountInfo{User: "u", Host: "h", CollectedAt: "t", Side: "source"},
		Domains:   []DomainEntry{{Name: "d.com", Type: "main"}},
		Mailboxes: []MailboxEntry{},
		Databases: []DatabaseEntry{},
		Warnings:  []string{},
	}
	path := filepath.Join(dir, "inventory.json")
	if err := WriteInventoryJSON(path, inv); err != nil {
		t.Fatalf("WriteInventoryJSON: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var got NormalizedInventory
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got.Account.User != "u" {
		t.Errorf("user = %q", got.Account.User)
	}
	if len(got.Domains) != 1 {
		t.Errorf("domains = %d", len(got.Domains))
	}
}

func TestWriteInventoryJSONCreatesDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "inventory.json")
	inv := NewEmptyInventory("u", "h", "source")
	if err := WriteInventoryJSON(path, inv); err != nil {
		t.Fatalf("WriteInventoryJSON: %v", err)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("file not created")
	}
}

func TestWriteReport(t *testing.T) {
	dir := t.TempDir()
	result := CollectResult{
		Source: NormalizedInventory{
			Account:   AccountInfo{User: "src", Host: "1.2.3.4", CollectedAt: "2026-07-01", Side: "source"},
			Domains:   []DomainEntry{{Name: "main.example", Type: "main", DocumentRoot: "/home/src/public_html"}},
			Mailboxes: []MailboxEntry{{Email: "info@main.example", Domain: "main.example", User: "info"}},
			Databases: []DatabaseEntry{{Name: "src_wp", Users: []string{"src_admin"}}},
			Warnings:  []string{"test warning"},
		},
	}
	path := filepath.Join(dir, "report.md")
	if err := WriteReport(path, result); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		"# Account Inventory",
		"src", "1.2.3.4",
		"main.example", "main",
		"info@main.example",
		"src_wp",
		"test warning",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("report missing %q", want)
		}
	}
}

func TestWriteReportWithForwarders(t *testing.T) {
	dir := t.TempDir()
	result := CollectResult{
		Source: NormalizedInventory{
			Account:        AccountInfo{User: "u", Host: "h", CollectedAt: "t", Side: "source"},
			Domains:        []DomainEntry{},
			Mailboxes:      []MailboxEntry{},
			Databases:      []DatabaseEntry{},
			Forwarders:     []ForwarderEntry{{Source: "info@d.com", Destination: "admin@gmail.com", Domain: "d.com"}},
			Autoresponders: []AutoresponderEntry{{Email: "info@d.com", Domain: "d.com", Subject: "OOO", Interval: 24}},
			Warnings:       []string{},
		},
	}
	path := filepath.Join(dir, "report.md")
	if err := WriteReport(path, result); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		"Forwarders (1)", "info@d.com", "admin@gmail.com",
		"Autoresponders (1)", "OOO",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("report missing %q", want)
		}
	}
}

func TestAggregateWarnings(t *testing.T) {
	tests := []struct {
		name     string
		result   CollectResult
		wantLen  int
		contains []string
	}{
		{
			name: "source only with warnings",
			result: CollectResult{
				Source: NormalizedInventory{
					Warnings: []string{"mail unavailable"},
				},
			},
			wantLen:  1,
			contains: []string{"source: mail unavailable"},
		},
		{
			name: "source + dest both with warnings",
			result: CollectResult{
				Source: NormalizedInventory{
					Warnings: []string{"src warning"},
				},
				Dest: &NormalizedInventory{
					Warnings: []string{"dst warning 1", "dst warning 2"},
				},
			},
			wantLen:  3,
			contains: []string{"source: src warning", "destination: dst warning 1", "destination: dst warning 2"},
		},
		{
			name: "no warnings",
			result: CollectResult{
				Source: NormalizedInventory{Warnings: []string{}},
			},
			wantLen: 0,
		},
		{
			name: "dest nil no crash",
			result: CollectResult{
				Source: NormalizedInventory{Warnings: []string{"w"}},
				Dest:   nil,
			},
			wantLen:  1,
			contains: []string{"source: w"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AggregateWarnings(tt.result)
			if len(got) != tt.wantLen {
				t.Errorf("got %d warnings, want %d: %v", len(got), tt.wantLen, got)
			}
			for _, want := range tt.contains {
				found := false
				for _, w := range got {
					if strings.Contains(w, want) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("warnings missing %q in %v", want, got)
				}
			}
		})
	}
}

func TestWriteReportWithDest(t *testing.T) {
	dir := t.TempDir()
	dest := NormalizedInventory{
		Account:   AccountInfo{User: "dst", Host: "5.6.7.8", Side: "destination"},
		Domains:   []DomainEntry{},
		Mailboxes: []MailboxEntry{},
		Databases: []DatabaseEntry{},
		Warnings:  []string{},
	}
	result := CollectResult{
		Source: NewEmptyInventory("src", "1.1.1.1", "source"),
		Dest:   &dest,
	}
	path := filepath.Join(dir, "report.md")
	if err := WriteReport(path, result); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, "Destination") {
		t.Error("report missing destination section")
	}
	if !strings.Contains(s, "dst") {
		t.Error("report missing dest user")
	}
}
