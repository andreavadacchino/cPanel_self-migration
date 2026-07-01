package accountinventory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type fakeRunner struct {
	responses map[string][]byte
}

func (f *fakeRunner) RunScript(_ context.Context, script string, _ map[string]string) ([]byte, error) {
	for key, resp := range f.responses {
		if len(script) > 0 && contains(script, key) {
			return resp, nil
		}
	}
	return nil, fmt.Errorf("fakeRunner: no response for script containing any known key")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && findSubstring(s, sub)
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func newFakeRunnerFromFixtures(t *testing.T) *fakeRunner {
	return &fakeRunner{responses: map[string][]byte{
		"DomainInfo list_domains":        loadFixture(t, "domaininfo_list.json"),
		"DomainInfo domains_data":        loadFixture(t, "domaininfo_domains_data.json"),
		"Email list_pops_with_disk":      loadFixture(t, "email_list_pops.json"),
		"Mysql list_databases":           wrapUAPI(`[{"database":"src_wp","disk_usage":1024,"users":["src_admin"]}]`),
		"Mysql list_users":              wrapUAPI(`[{"user":"src_admin","short_user":"admin","databases":["src_wp"]}]`),
	}}
}

func wrapUAPI(data string) []byte {
	return []byte(fmt.Sprintf(`{"result":{"data":%s,"errors":null,"messages":null,"status":1}}`, data))
}

func TestCollectSourceOnly(t *testing.T) {
	runner := newFakeRunnerFromFixtures(t)
	ctx := context.Background()

	result, err := Collect(ctx, runner, nil, HostInfo{User: "srcuser", Host: "1.2.3.4"}, HostInfo{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if result.Source.Account.User != "srcuser" {
		t.Errorf("Source.Account.User = %q, want %q", result.Source.Account.User, "srcuser")
	}
	if result.Source.Account.Side != "source" {
		t.Errorf("Source.Account.Side = %q, want %q", result.Source.Account.Side, "source")
	}
	if len(result.Source.Domains) == 0 {
		t.Error("Source.Domains is empty")
	}
	if result.Dest != nil {
		t.Error("Dest should be nil when no dest runner provided")
	}
}

func TestCollectWithDestination(t *testing.T) {
	src := newFakeRunnerFromFixtures(t)
	dest := newFakeRunnerFromFixtures(t)
	ctx := context.Background()

	result, err := Collect(ctx, src, dest, HostInfo{User: "src", Host: "1.1.1.1"}, HostInfo{User: "dst", Host: "2.2.2.2"})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if result.Source.Account.User != "src" {
		t.Errorf("Source user = %q", result.Source.Account.User)
	}
	if result.Dest == nil {
		t.Fatal("Dest should not be nil")
	}
	if result.Dest.Account.User != "dst" {
		t.Errorf("Dest user = %q", result.Dest.Account.User)
	}
	if result.Dest.Account.Side != "destination" {
		t.Errorf("Dest side = %q", result.Dest.Account.Side)
	}
}

func TestCollectDomainsFatal(t *testing.T) {
	runner := &fakeRunner{responses: map[string][]byte{}}
	ctx := context.Background()

	_, err := Collect(ctx, runner, nil, HostInfo{User: "u", Host: "h"}, HostInfo{})
	if err == nil {
		t.Fatal("expected fatal error when domains cannot be listed")
	}
}

func TestCollectMailboxWarningNotFatal(t *testing.T) {
	runner := &fakeRunner{responses: map[string][]byte{
		"DomainInfo list_domains": loadFixture(t, "domaininfo_list.json"),
		"DomainInfo domains_data": loadFixture(t, "domaininfo_domains_data.json"),
		"Mysql list_databases":    wrapUAPI(`[]`),
		"Mysql list_users":        wrapUAPI(`[]`),
	}}
	ctx := context.Background()

	result, err := Collect(ctx, runner, nil, HostInfo{User: "u", Host: "h"}, HostInfo{})
	if err != nil {
		t.Fatalf("Collect should not fail on mailbox error: %v", err)
	}
	if len(result.Source.Mailboxes) != 0 {
		t.Errorf("Mailboxes should be empty, got %d", len(result.Source.Mailboxes))
	}
	found := false
	for _, w := range result.Source.Warnings {
		if contains(w, "mailbox") || contains(w, "email") || contains(w, "Email") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a warning about mailboxes, got: %v", result.Source.Warnings)
	}
}
