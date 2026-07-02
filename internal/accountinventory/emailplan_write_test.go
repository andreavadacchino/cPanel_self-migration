package accountinventory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func epTestPlan() EmailApplyPlan {
	src := epInventory("source", "srcacct", "example.com")
	src.Forwarders = []ForwarderEntry{
		{Source: "info@example.com", Destination: "someone@gmail.com", Domain: "example.com"},
		{Source: "multi@example.com", Destination: "a@x.com, b@y.com", Domain: "example.com"},
	}
	src.DefaultAddresses.Items = []DefaultAddressEntry{
		{Domain: "example.com", DefaultAddress: "someone@gmail.com"},
	}
	dest := epInventory("destination", "destacct", "example.com")
	dest.Forwarders = []ForwarderEntry{
		{Source: "old@example.com", Destination: "keep@z.com", Domain: "example.com"},
	}
	dest.DefaultAddresses.Items = []DefaultAddressEntry{
		{Domain: "example.com", DefaultAddress: "destacct"},
	}
	p := BuildEmailPlan(src, dest, nil)
	p.GeneratedAt = "2026-07-03T00:00:00Z"
	p.SourceFile, p.SourceSHA256 = "src.json", "aaaa"
	p.DestinationFile, p.DestinationSHA256 = "dest.json", "bbbb"
	return p
}

func TestWriteEmailPlanJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := epTestPlan()
	path := filepath.Join(dir, "email_apply_plan.json")
	if err := WriteEmailPlanJSON(path, p); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got EmailApplyPlan
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "email-apply-plan" || got.FormatVersion != 1 {
		t.Errorf("mode/version = %q/%d", got.Mode, got.FormatVersion)
	}
	if len(got.Ops) != len(p.Ops) {
		t.Errorf("ops = %d, want %d", len(got.Ops), len(p.Ops))
	}
	if got.Summary != p.Summary {
		t.Errorf("summary = %+v, want %+v", got.Summary, p.Summary)
	}
	// The file must be mode 0600 (contains addresses).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestWriteEmailPlanMarkdown(t *testing.T) {
	dir := t.TempDir()
	p := epTestPlan()
	path := filepath.Join(dir, "email_apply_plan.md")
	if err := WriteEmailPlanMarkdown(path, p); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	md := string(b)

	// The fresh-default assumption travels verbatim (design rule).
	if !strings.Contains(md, freshDefaultAssumption) {
		t.Error("markdown does not carry the fresh-default assumption verbatim")
	}
	// Manual section comes before create (manual-first ordering).
	iManual := strings.Index(md, "## MANUAL")
	iCreate := strings.Index(md, "## CREATE")
	if iManual < 0 || iCreate < 0 || iManual > iCreate {
		t.Errorf("manual-first ordering broken: manual@%d create@%d", iManual, iCreate)
	}
	if !strings.Contains(md, "## SET") {
		t.Error("missing SET section")
	}
	if !strings.Contains(md, "Destination-only items") {
		t.Error("missing destination-only informational section")
	}
	if !strings.Contains(md, "never deletes destination-only resources") {
		t.Error("missing never-delete statement")
	}
}
