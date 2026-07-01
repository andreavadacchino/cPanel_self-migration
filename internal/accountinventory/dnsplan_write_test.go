package accountinventory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func buildTestPlan(t *testing.T) DNSPlan {
	t.Helper()
	src := planInventory("source", "1.2.3.4",
		planZone("example.com",
			aRec("new", "194.76.118.193", 300),
			aRec("ext", "203.0.113.9", 300),
			nsRec("example.com.", "ns1.old.example.", 86400),
			txtRec("_acme-challenge", "tok", 300)))
	dest := planInventory("destination", "5.6.7.8",
		planZone("example.com", aRec("only-dest", "9.9.9.9", 300)))
	p, err := BuildDNSPlan(src, dest, nil, map[string]string{"194.76.118.193": "38.224.109.78"})
	if err != nil {
		t.Fatal(err)
	}
	p.GeneratedAt = "t"
	p.SourceFile, p.SourceSHA256 = "s.json", "aaa"
	p.DestinationFile, p.DestinationSHA256 = "d.json", "bbb"
	return p
}

func TestWriteDNSPlanJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.json")
	p := buildTestPlan(t)
	if err := WriteDNSPlanJSON(path, p); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got DNSPlan
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "dns-import-plan" || got.FormatVersion != 1 {
		t.Errorf("mode/version = %q/%d", got.Mode, got.FormatVersion)
	}
	if got.SourceSHA256 != "aaa" || got.DestinationSHA256 != "bbb" {
		t.Error("input hashes not persisted")
	}
	if got.IPMap["194.76.118.193"] != "38.224.109.78" {
		t.Error("ip-map not embedded verbatim in the plan")
	}
	if got.Summary != p.Summary {
		t.Errorf("summary lost: %+v vs %+v", got.Summary, p.Summary)
	}
}

func TestWriteDNSPlanMarkdown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.md")
	if err := WriteDNSPlanMarkdown(path, buildTestPlan(t)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	md := string(b)

	// Manual items must be listed BEFORE the actionable ops.
	iManual := strings.Index(md, "MANUAL")
	iAdd := strings.Index(md, "ADD")
	if iManual < 0 || iAdd < 0 || iManual > iAdd {
		t.Errorf("manual section must come before add (manual@%d add@%d)", iManual, iAdd)
	}
	for _, want := range []string{
		"ext.example.com.",          // manual: unmapped address
		"203.0.113.9",               // named in the reason
		"new.example.com.",          // add
		"38.224.109.78",             // translated value
		"only-dest.example.com.",    // informational dest-only
		"194.76.118.193=38.224.109.78", // ip-map echoed for auditability
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
	if strings.Contains(md, "### DELETE") {
		t.Error("markdown contains a DELETE op section — the plan must never generate delete ops")
	}
}

func TestWriteDNSPlanMarkdownEmptyPlan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.md")
	p := DNSPlan{Mode: "dns-import-plan", FormatVersion: 1}
	if err := WriteDNSPlanMarkdown(path, p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "No zones") {
		t.Error("empty plan should say there is nothing to do")
	}
}
