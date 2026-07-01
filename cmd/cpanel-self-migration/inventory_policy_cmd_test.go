package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

// writeDiffFileFor produces a real inventory_diff.json by running the real
// pipeline: two inventories → DiffInventories → WriteDiffJSON.
func writeDiffFileFor(t *testing.T, dir string, mutate func(dest *accountinventory.NormalizedInventory)) string {
	t.Helper()
	src := accountinventory.NewEmptyInventory("u", "1.2.3.4", "source")
	src.Domains = []accountinventory.DomainEntry{{Name: "main.example", Type: "main"}}
	src.Mailboxes = []accountinventory.MailboxEntry{{Email: "info@main.example", Domain: "main.example", User: "info"}}
	dest := src
	dest.Mailboxes = append([]accountinventory.MailboxEntry{}, src.Mailboxes...)
	if mutate != nil {
		mutate(&dest)
	}
	d := accountinventory.DiffInventories(src, dest)
	d.SourceFile, d.DestinationFile, d.GeneratedAt = "s.json", "d.json", "t"
	path := filepath.Join(dir, "inventory_diff.json")
	if err := accountinventory.WriteDiffJSON(path, d); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInventoryPolicyCmdWithBlockerExitsZero(t *testing.T) {
	dir := t.TempDir()
	diff := writeDiffFileFor(t, dir, func(dest *accountinventory.NormalizedInventory) {
		dest.Mailboxes = nil // mailbox removed → blocker
	})
	outJSON := filepath.Join(dir, "policy.json")
	outMD := filepath.Join(dir, "policy.md")

	code := runInventoryPolicyCmd([]string{"--diff", diff, "--output-json", outJSON, "--output-md", outMD})
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (blockers are findings, not process errors)", code)
	}
	b, err := os.ReadFile(outJSON)
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]any
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if r["overall_status"] != "blocked" {
		t.Errorf("overall_status = %v, want blocked", r["overall_status"])
	}
	md, err := os.ReadFile(outMD)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "POL-MAILBOX-REMOVED") {
		t.Error("markdown missing the blocker finding")
	}
}

func TestInventoryPolicyCmdMissingFile(t *testing.T) {
	if code := runInventoryPolicyCmd([]string{"--diff", "/nonexistent/diff.json"}); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}

func TestInventoryPolicyCmdInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runInventoryPolicyCmd([]string{"--diff", bad}); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}

func TestInventoryPolicyCmdNotADiff(t *testing.T) {
	dir := t.TempDir()
	notDiff := filepath.Join(dir, "other.json")
	if err := os.WriteFile(notDiff, []byte(`{"mode":"something-else"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runInventoryPolicyCmd([]string{"--diff", notDiff}); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}

func TestInventoryPolicyCmdBadFlags(t *testing.T) {
	if code := runInventoryPolicyCmd([]string{"--no-such-flag"}); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}

func TestInventoryPolicyCmdMissingDiffFlag(t *testing.T) {
	if code := runInventoryPolicyCmd([]string{}); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}
