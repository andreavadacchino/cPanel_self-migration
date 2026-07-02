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
		"Email list_forwarders":          loadFixture(t, "email_forwarders.json"),
		"Email list_auto_responders":     loadFixture(t, "email_autoresponders.json"),
		"Mysql list_databases":           wrapUAPI(`[{"database":"src_wp","disk_usage":1024,"users":["src_admin"]}]`),
		"Mysql list_users":               wrapUAPI(`[{"user":"src_admin","short_user":"admin","databases":["src_wp"]}]`),
		"Ftp list_ftp_with_disk":         loadFixture(t, "ftp_list.json"),
		"SSL list_certs":                 loadFixture(t, "ssl_list_certs.json"),
		"LangPHP php_get_vhost_versions": loadFixture(t, "php_vhost_versions.json"),
		"ZoneEdit fetchzone_records":     loadFixture(t, "dns_fetchzone_records.json"),
		"crontab -l":                     []byte(fakeCrontabOutput),
	}}
}

// fakeCrontabOutput mimics the marker-based crontab fetch script output.
const fakeCrontabOutput = `MAILTO=admin@main.example
# nightly backup
0 3 * * * /usr/local/bin/backup.sh --password=supersecret
@daily /usr/bin/php /home/u/cron.php
#30 2 * * 0 /bin/disabled-weekly.sh
__CRONTAB_RC:0__
`

func wrapAPI2(data string) []byte {
	return []byte(fmt.Sprintf(`{"cpanelresult":{"data":%s,"event":{"result":1}}}`, data))
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

func TestCollectForwardersAndAutoresponders(t *testing.T) {
	runner := newFakeRunnerFromFixtures(t)
	ctx := context.Background()

	result, err := Collect(ctx, runner, nil, HostInfo{User: "u", Host: "h"}, HostInfo{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(result.Source.Forwarders) == 0 {
		t.Error("expected forwarders")
	}
	found := false
	for _, f := range result.Source.Forwarders {
		if f.Source == "info@main.example" && f.Destination == "admin@gmail.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing expected forwarder info@main.example -> admin@gmail.com, got: %+v", result.Source.Forwarders)
	}
	if len(result.Source.Autoresponders) == 0 {
		t.Error("expected autoresponders")
	}
}

func TestCollectForwardersWarningNotFatal(t *testing.T) {
	runner := &fakeRunner{responses: map[string][]byte{
		"DomainInfo list_domains":   loadFixture(t, "domaininfo_list.json"),
		"DomainInfo domains_data":   loadFixture(t, "domaininfo_domains_data.json"),
		"Email list_pops_with_disk": loadFixture(t, "email_list_pops.json"),
		"Mysql list_databases":      wrapUAPI(`[]`),
		"Mysql list_users":          wrapUAPI(`[]`),
	}}
	ctx := context.Background()

	result, err := Collect(ctx, runner, nil, HostInfo{User: "u", Host: "h"}, HostInfo{})
	if err != nil {
		t.Fatalf("Collect should not fail on forwarder error: %v", err)
	}
	if len(result.Source.Forwarders) != 0 {
		t.Errorf("Forwarders should be empty, got %d", len(result.Source.Forwarders))
	}
	hasWarning := false
	for _, w := range result.Source.Warnings {
		if contains(w, "forwarder") || contains(w, "Forwarder") {
			hasWarning = true
			break
		}
	}
	if !hasWarning {
		t.Errorf("expected warning about forwarders, got: %v", result.Source.Warnings)
	}
}

func TestCollectFTPSSLPHP(t *testing.T) {
	runner := newFakeRunnerFromFixtures(t)
	ctx := context.Background()
	result, err := Collect(ctx, runner, nil, HostInfo{User: "u", Host: "h"}, HostInfo{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !result.Source.FTP.Available {
		t.Error("FTP should be available")
	}
	if len(result.Source.FTP.Items) != 2 {
		t.Errorf("FTP items = %d, want 2", len(result.Source.FTP.Items))
	}
	if !result.Source.SSL.Available {
		t.Error("SSL should be available")
	}
	if len(result.Source.SSL.Items) != 2 {
		t.Errorf("SSL items = %d, want 2", len(result.Source.SSL.Items))
	}
	if !result.Source.PHP.Available {
		t.Error("PHP should be available")
	}
	if len(result.Source.PHP.Items) != 2 {
		t.Errorf("PHP items = %d, want 2", len(result.Source.PHP.Items))
	}
	if result.Source.FTP.SourceFunction != "Ftp::list_ftp_with_disk" {
		t.Errorf("FTP source_function = %q", result.Source.FTP.SourceFunction)
	}
}

func TestCollectFTPFailOthersOK(t *testing.T) {
	runner := &fakeRunner{responses: map[string][]byte{
		"DomainInfo list_domains":        loadFixture(t, "domaininfo_list.json"),
		"DomainInfo domains_data":        loadFixture(t, "domaininfo_domains_data.json"),
		"Email list_pops_with_disk":      loadFixture(t, "email_list_pops.json"),
		"Email list_forwarders":          loadFixture(t, "email_forwarders.json"),
		"Email list_auto_responders":     loadFixture(t, "email_autoresponders.json"),
		"Mysql list_databases":           wrapUAPI(`[]`),
		"Mysql list_users":               wrapUAPI(`[]`),
		"SSL list_certs":                 loadFixture(t, "ssl_list_certs.json"),
		"LangPHP php_get_vhost_versions": loadFixture(t, "php_vhost_versions.json"),
	}}
	ctx := context.Background()
	result, err := Collect(ctx, runner, nil, HostInfo{User: "u", Host: "h"}, HostInfo{})
	if err != nil {
		t.Fatalf("Collect should not fail: %v", err)
	}
	if result.Source.FTP.Available {
		t.Error("FTP should be unavailable (no fixture)")
	}
	if len(result.Source.FTP.Warnings) == 0 {
		t.Error("FTP should have a warning")
	}
	if !result.Source.SSL.Available {
		t.Error("SSL should still be available")
	}
	if !result.Source.PHP.Available {
		t.Error("PHP should still be available")
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

// ---------------------------------------------------------------------------
// DNS collection tests
// ---------------------------------------------------------------------------

func TestCollectDNSAPI2Success(t *testing.T) {
	runner := newFakeRunnerFromFixtures(t)
	ctx := context.Background()
	result, err := Collect(ctx, runner, nil, HostInfo{User: "u", Host: "h"}, HostInfo{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !result.Source.DNS.Available {
		t.Error("DNS should be available")
	}
	if len(result.Source.DNS.Zones) == 0 {
		t.Fatal("expected at least one DNS zone")
	}
	found := false
	for _, z := range result.Source.DNS.Zones {
		if z.Available && z.Method == "api2" {
			found = true
			if len(z.Records) == 0 {
				t.Errorf("zone %s has no records", z.Zone)
			}
			if z.SourceFunction != "ZoneEdit::fetchzone_records" {
				t.Errorf("zone %s source_function = %q", z.Zone, z.SourceFunction)
			}
		}
	}
	if !found {
		t.Error("expected at least one zone with method=api2")
	}
}

func TestCollectDNSUAPISuccessBypass(t *testing.T) {
	runner := &fakeRunner{responses: map[string][]byte{
		"DomainInfo list_domains":        loadFixture(t, "domaininfo_list.json"),
		"DomainInfo domains_data":        loadFixture(t, "domaininfo_domains_data.json"),
		"Email list_pops_with_disk":      loadFixture(t, "email_list_pops.json"),
		"Email list_forwarders":          loadFixture(t, "email_forwarders.json"),
		"Email list_auto_responders":     loadFixture(t, "email_autoresponders.json"),
		"Mysql list_databases":           wrapUAPI(`[]`),
		"Mysql list_users":               wrapUAPI(`[]`),
		"Ftp list_ftp_with_disk":         loadFixture(t, "ftp_list.json"),
		"SSL list_certs":                 loadFixture(t, "ssl_list_certs.json"),
		"LangPHP php_get_vhost_versions": loadFixture(t, "php_vhost_versions.json"),
		"DNS parse_zone":                 loadFixture(t, "dns_parse_zone.json"),
	}}
	ctx := context.Background()
	result, err := Collect(ctx, runner, nil, HostInfo{User: "u", Host: "h"}, HostInfo{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	found := false
	for _, z := range result.Source.DNS.Zones {
		if z.Available && z.Method == "uapi" {
			found = true
			if z.SourceFunction != "DNS::parse_zone" {
				t.Errorf("zone %s source_function = %q", z.Zone, z.SourceFunction)
			}
		}
	}
	if !found {
		t.Error("expected at least one zone with method=uapi")
	}
}

func TestCollectDNSUAPIFailFallbackAPI2(t *testing.T) {
	uapiFail := []byte(`{"result":{"data":null,"errors":["The function \"parse_zone\" does not exist in module \"DNS\"."],"status":0}}`)
	runner := &fakeRunner{responses: map[string][]byte{
		"DomainInfo list_domains":        loadFixture(t, "domaininfo_list.json"),
		"DomainInfo domains_data":        loadFixture(t, "domaininfo_domains_data.json"),
		"Email list_pops_with_disk":      loadFixture(t, "email_list_pops.json"),
		"Email list_forwarders":          loadFixture(t, "email_forwarders.json"),
		"Email list_auto_responders":     loadFixture(t, "email_autoresponders.json"),
		"Mysql list_databases":           wrapUAPI(`[]`),
		"Mysql list_users":               wrapUAPI(`[]`),
		"Ftp list_ftp_with_disk":         loadFixture(t, "ftp_list.json"),
		"SSL list_certs":                 loadFixture(t, "ssl_list_certs.json"),
		"LangPHP php_get_vhost_versions": loadFixture(t, "php_vhost_versions.json"),
		"DNS parse_zone":                 uapiFail,
		"ZoneEdit fetchzone_records":     loadFixture(t, "dns_fetchzone_records.json"),
	}}
	ctx := context.Background()
	result, err := Collect(ctx, runner, nil, HostInfo{User: "u", Host: "h"}, HostInfo{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, z := range result.Source.DNS.Zones {
		if z.Available && z.Method != "api2" {
			t.Errorf("zone %s should have method=api2, got %s (UAPI should have failed)", z.Zone, z.Method)
		}
	}
}

func TestCollectDNSBothFailWarning(t *testing.T) {
	uapiFail := []byte(`{"result":{"data":null,"errors":["DNS not available"],"status":0}}`)
	api2Fail := []byte(`{"cpanelresult":{"data":[],"event":{"result":0},"error":"Zone not found"}}`)
	runner := &fakeRunner{responses: map[string][]byte{
		"DomainInfo list_domains":        loadFixture(t, "domaininfo_list.json"),
		"DomainInfo domains_data":        loadFixture(t, "domaininfo_domains_data.json"),
		"Email list_pops_with_disk":      loadFixture(t, "email_list_pops.json"),
		"Email list_forwarders":          loadFixture(t, "email_forwarders.json"),
		"Email list_auto_responders":     loadFixture(t, "email_autoresponders.json"),
		"Mysql list_databases":           wrapUAPI(`[]`),
		"Mysql list_users":               wrapUAPI(`[]`),
		"Ftp list_ftp_with_disk":         loadFixture(t, "ftp_list.json"),
		"SSL list_certs":                 loadFixture(t, "ssl_list_certs.json"),
		"LangPHP php_get_vhost_versions": loadFixture(t, "php_vhost_versions.json"),
		"DNS parse_zone":                 uapiFail,
		"ZoneEdit fetchzone_records":     api2Fail,
	}}
	ctx := context.Background()
	result, err := Collect(ctx, runner, nil, HostInfo{User: "u", Host: "h"}, HostInfo{})
	if err != nil {
		t.Fatalf("Collect should not fail on DNS error: %v", err)
	}
	if result.Source.DNS.Available {
		t.Error("DNS should be unavailable when both UAPI and API2 fail")
	}
	hasWarning := false
	for _, z := range result.Source.DNS.Zones {
		if len(z.Warnings) > 0 {
			hasWarning = true
		}
		if z.Method != "unavailable" {
			t.Errorf("zone %s method should be unavailable, got %s", z.Zone, z.Method)
		}
	}
	if !hasWarning {
		t.Error("expected warnings on failed DNS zones")
	}
}

func TestCollectDNSFailNotFatal(t *testing.T) {
	runner := &fakeRunner{responses: map[string][]byte{
		"DomainInfo list_domains":        loadFixture(t, "domaininfo_list.json"),
		"DomainInfo domains_data":        loadFixture(t, "domaininfo_domains_data.json"),
		"Email list_pops_with_disk":      loadFixture(t, "email_list_pops.json"),
		"Email list_forwarders":          loadFixture(t, "email_forwarders.json"),
		"Email list_auto_responders":     loadFixture(t, "email_autoresponders.json"),
		"Mysql list_databases":           wrapUAPI(`[]`),
		"Mysql list_users":               wrapUAPI(`[]`),
		"Ftp list_ftp_with_disk":         loadFixture(t, "ftp_list.json"),
		"SSL list_certs":                 loadFixture(t, "ssl_list_certs.json"),
		"LangPHP php_get_vhost_versions": loadFixture(t, "php_vhost_versions.json"),
	}}
	ctx := context.Background()
	result, err := Collect(ctx, runner, nil, HostInfo{User: "u", Host: "h"}, HostInfo{})
	if err != nil {
		t.Fatalf("Collect should not fail: %v", err)
	}
	if len(result.Source.Domains) == 0 {
		t.Error("Domains should still be collected")
	}
	if result.Source.FTP.Available != true {
		t.Error("FTP should still be available")
	}
}

func TestCollectDNSSkipsSubdomains(t *testing.T) {
	runner := newFakeRunnerFromFixtures(t)
	ctx := context.Background()
	result, err := Collect(ctx, runner, nil, HostInfo{User: "u", Host: "h"}, HostInfo{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, z := range result.Source.DNS.Zones {
		for _, d := range result.Source.Domains {
			if d.Name == z.Zone && d.Type == "sub" {
				t.Errorf("subdomain %s should not appear as a DNS zone", z.Zone)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Cron collection tests
// ---------------------------------------------------------------------------

func TestCollectCronPresent(t *testing.T) {
	runner := newFakeRunnerFromFixtures(t)
	ctx := context.Background()
	result, err := Collect(ctx, runner, nil, HostInfo{User: "u", Host: "h"}, HostInfo{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	cron := result.Source.Cron
	if !cron.Available {
		t.Fatal("cron should be available")
	}
	if cron.Method != "ssh_crontab_l" {
		t.Errorf("method = %q, want ssh_crontab_l", cron.Method)
	}
	if cron.SourceCommand != "crontab -l" {
		t.Errorf("source_command = %q", cron.SourceCommand)
	}
	if len(cron.Jobs) != 3 {
		t.Errorf("jobs = %d, want 3 (2 enabled + 1 disabled)", len(cron.Jobs))
	}
	if cron.DisabledJobsCount != 1 {
		t.Errorf("disabled = %d, want 1", cron.DisabledJobsCount)
	}
	if cron.CommentsCount != 1 {
		t.Errorf("comments = %d, want 1", cron.CommentsCount)
	}
	if len(cron.Environment) != 1 || cron.Environment[0].Name != "MAILTO" {
		t.Errorf("environment = %+v", cron.Environment)
	}
	for _, j := range cron.Jobs {
		if contains(j.CommandRedacted, "supersecret") {
			t.Errorf("secret leaked in job command: %q", j.CommandRedacted)
		}
	}
}

func TestCollectCronNoCrontabForUser(t *testing.T) {
	runner := newFakeRunnerFromFixtures(t)
	runner.responses["crontab -l"] = []byte("no crontab for u\n__CRONTAB_RC:1__\n")
	ctx := context.Background()
	result, err := Collect(ctx, runner, nil, HostInfo{User: "u", Host: "h"}, HostInfo{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	cron := result.Source.Cron
	if !cron.Available {
		t.Error("empty crontab is still 'available'")
	}
	if len(cron.Jobs) != 0 {
		t.Errorf("jobs = %d, want 0", len(cron.Jobs))
	}
	if len(cron.Warnings) == 0 {
		t.Error("expected light warning for empty crontab")
	}
}

func TestCollectCronErrorNotFatal(t *testing.T) {
	runner := newFakeRunnerFromFixtures(t)
	delete(runner.responses, "crontab -l")
	ctx := context.Background()
	result, err := Collect(ctx, runner, nil, HostInfo{User: "u", Host: "h"}, HostInfo{})
	if err != nil {
		t.Fatalf("cron failure must not block inventory: %v", err)
	}
	cron := result.Source.Cron
	if cron.Available {
		t.Error("cron should be unavailable")
	}
	if cron.Method != "unavailable" {
		t.Errorf("method = %q, want unavailable", cron.Method)
	}
	if len(cron.Errors) == 0 {
		t.Error("hard failure must populate errors")
	}
	if len(result.Source.Domains) == 0 {
		t.Error("rest of inventory must still be collected")
	}
}

func TestCollectCronNoNullArrays(t *testing.T) {
	runner := newFakeRunnerFromFixtures(t)
	delete(runner.responses, "crontab -l")
	ctx := context.Background()
	result, err := Collect(ctx, runner, nil, HostInfo{User: "u", Host: "h"}, HostInfo{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	cron := result.Source.Cron
	if cron.Jobs == nil || cron.Environment == nil || cron.Warnings == nil || cron.Errors == nil {
		t.Errorf("cron slices must never be nil: %+v", cron)
	}
}

func TestCollectDNSNoNullArrays(t *testing.T) {
	runner := newFakeRunnerFromFixtures(t)
	ctx := context.Background()
	result, err := Collect(ctx, runner, nil, HostInfo{User: "u", Host: "h"}, HostInfo{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, z := range result.Source.DNS.Zones {
		if z.Records == nil {
			t.Errorf("zone %s records is nil, want empty slice", z.Zone)
		}
		if z.Warnings == nil {
			t.Errorf("zone %s warnings is nil, want empty slice", z.Zone)
		}
		if z.Errors == nil {
			t.Errorf("zone %s errors is nil, want empty slice", z.Zone)
		}
	}
}

func TestCollectMailboxWarningNotFatal(t *testing.T) {
	runner := &fakeRunner{responses: map[string][]byte{
		"DomainInfo list_domains": loadFixture(t, "domaininfo_list.json"),
		"DomainInfo domains_data": loadFixture(t, "domaininfo_domains_data.json"),
		"Mysql list_databases":    wrapUAPI(`[]`),
		"Mysql list_users":        wrapUAPI(`[]`),
		// list_filters succeeds while the mailbox list fails: the
		// filters section must surface the narrowed scope (PR 7E).
		"Email list_filters": wrapUAPI(`[]`),
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
	ef := result.Source.EmailFilters
	if !ef.Available {
		t.Fatalf("email filters section = %+v, want available", ef.ConfigSection)
	}
	scoped := false
	for _, w := range ef.Warnings {
		if contains(w, "account-level") {
			scoped = true
		}
	}
	if !scoped {
		t.Errorf("email filters must warn about account-level-only scope, got: %v", ef.Warnings)
	}
}

// FetchDNSZone (PR 6C) is the per-zone fetch extracted from collectDNS so
// `dns verify` re-fetches destination zones with the exact collector
// semantics: UAPI parse_zone first, API2 fallback, unavailable-with-warning.
func TestFetchDNSZoneUAPI(t *testing.T) {
	runner := &fakeRunner{responses: map[string][]byte{
		"DNS parse_zone": loadFixture(t, "dns_parse_zone.json"),
	}}
	zr := FetchDNSZone(context.Background(), runner, "example.com")
	if !zr.Available || zr.Method != "uapi" || zr.SourceFunction != "DNS::parse_zone" {
		t.Fatalf("zone = %+v, want available uapi DNS::parse_zone", zr)
	}
	if zr.Zone != "example.com" {
		t.Errorf("zone name = %q", zr.Zone)
	}
	if len(zr.Records) == 0 {
		t.Error("expected records from the fixture")
	}
}

func TestFetchDNSZoneFallbackAPI2(t *testing.T) {
	uapiFail := []byte(`{"result":{"data":null,"errors":["The function \"parse_zone\" does not exist in module \"DNS\"."],"status":0}}`)
	runner := &fakeRunner{responses: map[string][]byte{
		"DNS parse_zone":             uapiFail,
		"ZoneEdit fetchzone_records": loadFixture(t, "dns_fetchzone_records.json"),
	}}
	zr := FetchDNSZone(context.Background(), runner, "example.com")
	if !zr.Available || zr.Method != "api2" || zr.SourceFunction != "ZoneEdit::fetchzone_records" {
		t.Fatalf("zone = %+v, want available api2 ZoneEdit::fetchzone_records", zr)
	}
	if len(zr.Records) == 0 {
		t.Error("expected records from the fixture")
	}
}

func TestFetchDNSZoneUnavailable(t *testing.T) {
	uapiFail := []byte(`{"result":{"data":null,"errors":["boom"],"status":0}}`)
	runner := &fakeRunner{responses: map[string][]byte{
		"DNS parse_zone": uapiFail,
	}}
	zr := FetchDNSZone(context.Background(), runner, "example.com")
	if zr.Available || zr.Method != "unavailable" {
		t.Fatalf("zone = %+v, want unavailable", zr)
	}
	if len(zr.Warnings) != 1 || !contains(zr.Warnings[0], "example.com") {
		t.Errorf("warnings = %v, want one naming the zone", zr.Warnings)
	}
}
