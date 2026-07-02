package accountinventory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildTestVerifyReport exercises the real plan→verify path so the writer
// fixtures stay honest: one applied add, one manual NS, one pending
// CNAME, one drifted skip, plus an untracked live rrset and a manual zone.
func buildTestVerifyReport(t *testing.T) DNSVerifyReport {
	t.Helper()
	src := planInventory("source", "1.2.3.4",
		planZone("example.com",
			aRec("new", "194.76.118.193", 300),
			cnameRec("www", "example.com.", 300),
			aRec("same", "194.76.118.193", 300),
			nsRec("example.com.", "ns1.old.example.", 86400)),
		planZone("missing.example",
			aRec("missing.example.", "194.76.118.193", 300)))
	dest := planInventory("destination", "5.6.7.8",
		planZone("example.com", aRec("same", "38.224.109.78", 300)))
	p, err := BuildDNSPlan(src, dest, nil, map[string]string{"194.76.118.193": "38.224.109.78"})
	if err != nil {
		t.Fatal(err)
	}

	rep := VerifyDNSPlan(p, liveZone("example.com",
		aRec("new", "38.224.109.78", 300),                // add → applied
		aRec("same", "5.5.5.5", 300),                     // skip → drift
		nsRec("example.com.", "ns1.new.example.", 86400), // manual → manual_review
		txtRec("postplan", "added later", 300),           // untracked
	))
	rep.GeneratedAt = "t"
	rep.PlanFile, rep.PlanSHA256 = "dns_import_plan.json", "ccc"
	return rep
}

func TestWriteDNSVerifyJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "verify.json")
	rep := buildTestVerifyReport(t)
	if err := WriteDNSVerifyJSON(path, rep); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got DNSVerifyReport
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "dns-verify" || got.FormatVersion != 1 {
		t.Errorf("mode/version = %q/%d", got.Mode, got.FormatVersion)
	}
	if got.PlanSHA256 != "ccc" {
		t.Error("plan sha256 not persisted")
	}
	if got.Summary != rep.Summary {
		t.Errorf("summary lost: %+v vs %+v", got.Summary, rep.Summary)
	}
	if got.Clean {
		t.Error("fixture report must not be clean (drift + pending + manual zone)")
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("json file mode = %o, want 600", perm)
	}
}

func TestWriteDNSVerifyMarkdown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "verify.md")
	if err := WriteDNSVerifyMarkdown(path, buildTestVerifyReport(t)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	md := string(b)

	if !strings.Contains(md, "NOT CLEAN") {
		t.Error("markdown must state the verdict")
	}
	// Drift demands operator attention first.
	iDrift := strings.Index(md, "DRIFT")
	iApplied := strings.Index(md, "APPLIED")
	if iDrift < 0 || iApplied < 0 || iDrift > iApplied {
		t.Errorf("drift section must come before applied (drift@%d applied@%d)", iDrift, iApplied)
	}
	for _, want := range []string{
		"same.example.com.", // drifted skip
		"5.5.5.5",           // observed drift value
		"38.224.109.78",     // expected value
		"missing.example",   // manual zone
		"postplan",          // untracked rrset
		"manual zone",       // gate explanation mentions manual zones
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
}

func TestWriteDNSVerifyMarkdownCleanAndEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "verify.md")
	rep := VerifyDNSPlan(DNSPlan{Zones: []PlanZone{}}, nil)
	if err := WriteDNSVerifyMarkdown(path, rep); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	md := string(b)
	if !strings.Contains(md, "CLEAN") || strings.Contains(md, "NOT CLEAN") {
		t.Errorf("empty plan verifies clean, markdown says otherwise:\n%s", md)
	}
	if !strings.Contains(md, "No zones") {
		t.Error("empty report should say there is nothing verified")
	}
}

// The markdown is an operator report: long TXT/DKIM material must never be
// re-exposed in full (mdCell previews only) — checklist rule, same here.
func TestWriteDNSVerifyMarkdownDoesNotExposeLongTXT(t *testing.T) {
	longDKIM := "v=DKIM1; k=rsa; p=" + strings.Repeat("A", 400)
	src := planInventory("source", "1.2.3.4", planZone("example.com",
		txtRec("default._domainkey", longDKIM, 300)))
	dest := planInventory("destination", "5.6.7.8", planZone("example.com"))
	p, err := BuildDNSPlan(src, dest, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rep := VerifyDNSPlan(p, liveZone("example.com", txtRec("default._domainkey", longDKIM, 300)))

	dir := t.TempDir()
	path := filepath.Join(dir, "verify.md")
	if err := WriteDNSVerifyMarkdown(path, rep); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), longDKIM) {
		t.Error("markdown exposes the full DKIM value — cells must go through mdCell")
	}
}

// TestDNSVerifyMarkdownGolden pins the full operator report. Refresh with:
//
//	UPDATE_GOLDEN=1 go test ./internal/accountinventory/ -run TestDNSVerifyMarkdownGolden
func TestDNSVerifyMarkdownGolden(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "verify.md")
	if err := WriteDNSVerifyMarkdown(path, buildTestVerifyReport(t)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	goldenPath := filepath.Join("..", "testdata", "dns_verify_report.md.golden")
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
		t.Errorf("verify markdown differs from golden.\nGOT:\n%s\nWANT:\n%s", got, want)
	}
}
