package accountinventory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func samplePolicyReport(t *testing.T) PolicyReport {
	t.Helper()
	d := diffWith("cron", removed(DiffEntry{
		Key: "/bin/dump db \\| gzip --password=[REDACTED]", Detail: "0 3 * * * enabled=true",
	}))
	d.Sections["php"] = changed(DiffFieldChange{Key: "main.example", Field: "version", Source: "ea-php74", Destination: "ea-php81"})
	r := EvaluatePolicy(d)
	r.InputDiff = "inventory_diff.json"
	r.GeneratedAt = "2026-07-01T00:00:00Z"
	return r
}

func TestWritePolicyJSONNoNulls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy_report.json")
	if err := WritePolicyJSON(path, samplePolicyReport(t)); err != nil {
		t.Fatalf("WritePolicyJSON: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	var walk func(v any, p string)
	walk = func(v any, p string) {
		switch val := v.(type) {
		case nil:
			t.Errorf("null at %s", p)
		case map[string]any:
			for k, c := range val {
				walk(c, p+"."+k)
			}
		case []any:
			for _, c := range val {
				walk(c, p+"[]")
			}
		}
	}
	walk(m, "$")
	if m["mode"] != "inventory-policy" {
		t.Errorf("mode = %v", m["mode"])
	}
	if m["overall_status"] != "blocked" {
		t.Errorf("overall_status = %v", m["overall_status"])
	}
}

func TestWritePolicyMarkdown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy_report.md")
	if err := WritePolicyMarkdown(path, samplePolicyReport(t)); err != nil {
		t.Fatalf("WritePolicyMarkdown: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"# Migration Policy Report",
		"**blocked**",
		"BLOCKER (1)", "REVIEW (1)",
		"POL-CRON-ENABLED-REMOVED", "POL-PHP-CHANGED",
		"[REDACTED]",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
	// Table-safety: the piped redacted command must not break rows.
	if strings.Contains(s, " | gzip") {
		t.Error("unescaped pipe leaked into a table row")
	}
}

func TestWritePolicyMarkdownNewlineInjectionSafe(t *testing.T) {
	// A crafted TXT record containing newlines must not break out of its
	// table cell (DNS values are attacker-influenced free text).
	d := diffWith("dns", changed(DiffFieldChange{
		Key: "zone main.example TXT evil.main.example.", Field: "records",
		Source:      "v=ok ttl=1",
		Destination: "v=evil ttl=1\n\n# Injected heading\n| fake | row |",
	}))
	r := EvaluatePolicy(d)
	r.InputDiff, r.GeneratedAt = "d.json", "t"
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.md")
	if err := WritePolicyMarkdown(path, r); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	// Inline occurrences inside a cell are harmless; breaking out means
	// the injected text starts its OWN line.
	if strings.Contains(string(b), "\n# Injected heading") {
		t.Error("newline in a record value broke out of its markdown cell")
	}
	if strings.Contains(string(b), "\n| fake | row |") {
		t.Error("injected table row escaped its cell")
	}
}

func TestWritePolicyMarkdownClean(t *testing.T) {
	r := EvaluatePolicy(emptyDiff())
	r.InputDiff, r.GeneratedAt = "d.json", "t"
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.md")
	if err := WritePolicyMarkdown(path, r); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "No findings") {
		t.Errorf("clean report should say so:\n%s", b)
	}
	if !strings.Contains(string(b), "**ready**") {
		t.Errorf("clean report must be ready:\n%s", b)
	}
}
