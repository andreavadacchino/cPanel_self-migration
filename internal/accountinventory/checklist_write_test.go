package accountinventory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildTestChecklist assembles a rich, fully deterministic checklist:
// a blocking cron loss, a reissued-but-valid certificate, an expected
// docroot difference, a PHP version change, full apply evidence, and the
// synthetic sections of a mail-bearing account.
func buildTestChecklist(t *testing.T) MigrationChecklist {
	t.Helper()
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.Domains = []DomainEntry{{Name: "main.example", Type: "main", DocumentRoot: "/home/other/public_html"}}
	dest.Cron.Jobs = []CronJobEntry{}
	dest.PHP.Items = []PHPEntry{{Domain: "main.example", Version: "ea-php82"}}
	dest.SSL.Items = []SSLEntry{{
		Domains: "main.example", Issuer: "E1 (reissued)", ValidFrom: 1_790_000_000,
		ValidUntil: chkCertValidUntil, ValidationType: "dv",
	}}

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))
	c.GeneratedAt = "2026-07-02T10:00:00Z"
	c.Inputs = ChecklistInputs{
		SourceInventory:      ChecklistInputRef{File: "inventory_source.json", SHA256: "aaa", Present: true},
		DestinationInventory: ChecklistInputRef{File: "inventory_destination.json", SHA256: "bbb", Present: true},
		Diff:                 ChecklistInputRef{File: "inventory_diff.json", SHA256: "ccc", Present: true},
		Policy:               ChecklistInputRef{File: "policy_report.json", SHA256: "ddd", Present: true},
		DNSPlan:              ChecklistInputRef{Present: false},
		MigrationReport:      ChecklistInputRef{File: "report.json", SHA256: "eee", Present: true},
	}
	return c
}

func TestWriteChecklistJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checklist.json")
	c := buildTestChecklist(t)
	if err := WriteChecklistJSON(path, c); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got MigrationChecklist
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "migration-checklist" || got.FormatVersion != 1 {
		t.Errorf("mode/version = %q/%d", got.Mode, got.FormatVersion)
	}
	if got.OverallStatus != c.OverallStatus || got.Summary != c.Summary {
		t.Errorf("overall/summary lost in round-trip")
	}
	if len(got.Sections) != len(checklistSectionOrder) {
		t.Errorf("sections = %d, want %d", len(got.Sections), len(checklistSectionOrder))
	}
	if strings.Contains(string(b), ": null") {
		t.Error("checklist JSON contains null arrays")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("json file mode = %o, want 600", perm)
	}
}

func TestWriteChecklistMarkdown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checklist.md")
	c := buildTestChecklist(t)
	if err := WriteChecklistMarkdown(path, c); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	md := string(b)
	for _, want := range []string{
		"# Migration Checklist — srcacct",
		"**Overall: " + c.OverallStatus + "**",
		"## Before shutting down the old server",
		"## Post-cutover checks",
		"ADAPT_CRON_PATH",
		"email_routing",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
	if !strings.Contains(md, "not inventoried") {
		t.Error("markdown must spell out the not-inventoried state for the operator")
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("md file mode = %o, want 600", perm)
	}
}

// TestChecklistMarkdownGolden pins the full operator report. Refresh with:
//
//	UPDATE_GOLDEN=1 go test ./internal/accountinventory/ -run TestChecklistMarkdownGolden
func TestChecklistMarkdownGolden(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checklist.md")
	if err := WriteChecklistMarkdown(path, buildTestChecklist(t)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	goldenPath := filepath.Join("..", "testdata", "migration_checklist.md.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("update golden %s: %v", goldenPath, err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run once with UPDATE_GOLDEN=1 to create): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("checklist markdown differs from golden.\nGOT:\n%s\nWANT:\n%s", got, want)
	}
}

// The markdown is an operator report: long TXT/DKIM material must never be
// re-exposed in full (mdCell previews only). The JSON may carry what the
// diff already carried — no NEW exposure — but the report must not.
func TestChecklistMarkdownDoesNotExposeLongTXT(t *testing.T) {
	longDKIM := "v=DKIM1; k=rsa; p=" + strings.Repeat("A", 400)
	src := chkInventory("source", "1.2.3.4", "srcacct")
	src.DNS.Zones = []DNSZoneResult{planZone("main.example",
		aRec("main.example.", "1.2.3.4", 300),
		txtRec("default._domainkey", longDKIM, 300),
	)}
	dest := chkInventory("destination", "5.6.7.8", "srcacct")

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))
	c.GeneratedAt = "t"

	dir := t.TempDir()
	path := filepath.Join(dir, "checklist.md")
	if err := WriteChecklistMarkdown(path, c); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), longDKIM) {
		t.Error("markdown re-exposes the full DKIM TXT value; cells must stay previews")
	}
}
