package accountinventory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleDiff(t *testing.T) InventoryDiff {
	t.Helper()
	src := baseInventory()
	dest := baseInventory()
	dest.Domains = append(dest.Domains, DomainEntry{Name: "new.example", Type: "addon"})
	dest.PHP.Items[0].Version = "ea-php83"
	dest.Cron.Jobs[0].CommandSHA256 = "sha256:eeff"
	dest.Cron.Jobs[0].CommandRedacted = "/bin/dump db | gzip --token=[REDACTED]"
	d := DiffInventories(src, dest)
	d.SourceFile = "inventory_source.json"
	d.DestinationFile = "inventory_destination.json"
	d.GeneratedAt = "2026-07-01T00:00:00Z"
	return d
}

func TestWriteDiffJSONNoNulls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inventory_diff.json")
	if err := WriteDiffJSON(path, sampleDiff(t)); err != nil {
		t.Fatalf("WriteDiffJSON: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	var walk func(v any, path string)
	walk = func(v any, path string) {
		switch val := v.(type) {
		case nil:
			t.Errorf("null at %s", path)
		case map[string]any:
			for k, c := range val {
				walk(c, path+"."+k)
			}
		case []any:
			for i, c := range val {
				walk(c, path+"["+string(rune('0'+i%10))+"]")
			}
		}
	}
	walk(m, "$")

	for _, want := range []string{`"mode"`, `"inventory-diff"`, `"summary"`, `"sections"`, `"source_file"`, `"generated_at"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON missing %s", want)
		}
	}
}

func TestWriteDiffMarkdown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inventory_diff.md")
	if err := WriteDiffMarkdown(path, sampleDiff(t)); err != nil {
		t.Fatalf("WriteDiffMarkdown: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"# Inventory Diff",
		"new.example",        // added domain visible
		"ea-php81", "ea-php83", // changed values visible
		"[REDACTED]", // cron detail redacted
	} {
		if !strings.Contains(s, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
	// The piped cron command must be table-escaped.
	if strings.Contains(s, " | gzip") {
		t.Error("unescaped pipe leaked into a markdown table")
	}
	if !strings.Contains(s, `\|`) {
		t.Error("expected escaped pipe in markdown")
	}
}

func TestWriteDiffMarkdownCleanDiff(t *testing.T) {
	d := DiffInventories(baseInventory(), baseInventory())
	d.SourceFile, d.DestinationFile, d.GeneratedAt = "a.json", "b.json", "t"
	dir := t.TempDir()
	path := filepath.Join(dir, "diff.md")
	if err := WriteDiffMarkdown(path, d); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(strings.ToLower(string(b)), "no differences") {
		t.Errorf("clean diff should say so:\n%s", b)
	}
}

func TestDiffOutputNoSecretKeywordValues(t *testing.T) {
	// The inputs are already redacted; the diff must not synthesize any
	// secret-looking content of its own.
	d := sampleDiff(t)
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ToLower(string(b))
	// "password"/"token" may appear ONLY inside redacted command strings
	// (e.g. "--password=[REDACTED]"), never followed by a real value.
	for _, frag := range []string{"password=", "token="} {
		for idx := strings.Index(s, frag); idx >= 0; {
			rest := s[idx+len(frag):]
			if !strings.HasPrefix(rest, "[redacted]") {
				t.Errorf("unredacted %s found in diff output near: %.60s", frag, s[idx:])
			}
			next := strings.Index(rest, frag)
			if next < 0 {
				break
			}
			idx = idx + len(frag) + next
		}
	}
}
