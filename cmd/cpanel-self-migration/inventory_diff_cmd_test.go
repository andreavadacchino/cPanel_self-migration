package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

func writeInventoryFile(t *testing.T, dir, name string, mutate func(*accountinventory.NormalizedInventory)) string {
	t.Helper()
	inv := accountinventory.NewEmptyInventory("u", "1.2.3.4", "source")
	inv.Domains = []accountinventory.DomainEntry{{Name: "main.example", Type: "main"}}
	if mutate != nil {
		mutate(&inv)
	}
	path := filepath.Join(dir, name)
	if err := accountinventory.WriteInventoryJSON(path, inv); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInventoryDiffCmdSuccess(t *testing.T) {
	dir := t.TempDir()
	src := writeInventoryFile(t, dir, "src.json", nil)
	dest := writeInventoryFile(t, dir, "dest.json", func(inv *accountinventory.NormalizedInventory) {
		inv.Domains = append(inv.Domains, accountinventory.DomainEntry{Name: "new.example", Type: "addon"})
	})
	outJSON := filepath.Join(dir, "diff.json")
	outMD := filepath.Join(dir, "diff.md")

	code := runInventoryDiffCmd([]string{
		"--source", src, "--destination", dest,
		"--output-json", outJSON, "--output-md", outMD,
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (differences are not an error)", code)
	}

	b, err := os.ReadFile(outJSON)
	if err != nil {
		t.Fatalf("diff.json not written: %v", err)
	}
	var d map[string]any
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("diff.json invalid: %v", err)
	}
	if d["mode"] != "inventory-diff" {
		t.Errorf("mode = %v", d["mode"])
	}
	if d["generated_at"] == nil || d["generated_at"] == "" {
		t.Error("generated_at missing")
	}
	if _, err := os.Stat(outMD); err != nil {
		t.Errorf("diff.md not written: %v", err)
	}
}

func TestInventoryDiffCmdMissingFile(t *testing.T) {
	dir := t.TempDir()
	src := writeInventoryFile(t, dir, "src.json", nil)
	code := runInventoryDiffCmd([]string{
		"--source", src, "--destination", filepath.Join(dir, "nope.json"),
		"--output-json", filepath.Join(dir, "d.json"), "--output-md", filepath.Join(dir, "d.md"),
	})
	if code != 1 {
		t.Errorf("exit code = %d, want 1 for missing file", code)
	}
}

func TestInventoryDiffCmdInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	src := writeInventoryFile(t, dir, "src.json", nil)
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{{{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	code := runInventoryDiffCmd([]string{"--source", src, "--destination", bad})
	if code != 1 {
		t.Errorf("exit code = %d, want 1 for invalid JSON", code)
	}
}

func TestInventoryDiffCmdNotAnInventory(t *testing.T) {
	dir := t.TempDir()
	src := writeInventoryFile(t, dir, "src.json", nil)
	notInv := filepath.Join(dir, "other.json")
	if err := os.WriteFile(notInv, []byte(`{"hello":"world"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code := runInventoryDiffCmd([]string{"--source", src, "--destination", notInv})
	if code != 1 {
		t.Errorf("exit code = %d, want 1 for non-inventory JSON", code)
	}
}

func TestInventoryDiffCmdMissingRequiredFlags(t *testing.T) {
	if code := runInventoryDiffCmd([]string{}); code == 0 {
		t.Error("missing --source/--destination must not exit 0")
	}
}

func TestInventoryDiffCmdDefaultOutputPaths(t *testing.T) {
	dir := t.TempDir()
	src := writeInventoryFile(t, dir, "src.json", nil)
	dest := writeInventoryFile(t, dir, "dest.json", nil)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	if code := runInventoryDiffCmd([]string{"--source", src, "--destination", dest}); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	for _, f := range []string{"inventory_diff.json", "inventory_diff.md"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("default output %s not written: %v", f, err)
		}
	}
}
