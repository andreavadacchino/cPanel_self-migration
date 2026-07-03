package accountinventory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestChecklistSourcesAreOfflineByConstruction pins the read-only,
// offline invariant of the checklist line of work (PR 7A): no checklist
// source may import a network/SSH/cPanel-client package or reference a
// write primitive. The checklist composes artifact FILES — nothing else.
func TestChecklistSourcesAreOfflineByConstruction(t *testing.T) {
	forbiddenImports := []string{
		"internal/sshx", "internal/cpanel", "internal/migrate",
		"golang.org/x/crypto/ssh", "\"net\"", "\"net/http\"",
	}
	writeCalls := []string{
		"mass_edit_zone", "add_pop", "addaddondomain", "addsubdomain",
		"create_user", "create_database", "set_privileges",
		// email-config writers (PR 2B-1/2B-3): the checklist stays offline.
		"add_forwarder", "set_default_address", "delete_forwarder",
		"add_auto_responder", "delete_auto_responder",
		"store_filter", "delete_filter", "setmxcheck",
		// cron writer (PR 2A): the checklist stays offline.
		"InstallCrontab", "crontab -",
		// DNS writer (PR 6D): the checklist stays offline.
		"mass_edit_zone",
	}
	files, err := filepath.Glob("checklist*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no checklist*.go files found — glob broken?")
	}
	for _, f := range files {
		if f == "checklist_safety_test.go" {
			continue // this file names the verbs on purpose
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			code, _, _ := strings.Cut(strings.TrimSpace(line), "//")
			for _, imp := range forbiddenImports {
				if strings.Contains(code, imp) {
					t.Errorf("%s:%d imports/references %q — the checklist is offline by construction", f, i+1, imp)
				}
			}
			for _, verb := range writeCalls {
				if strings.Contains(code, verb) {
					t.Errorf("%s:%d references write call %q in code — the checklist never applies anything", f, i+1, verb)
				}
			}
		}
	}
}

// TestChecklistNeverClaimsEvidenceWithoutReport is the honesty invariant
// in its most direct form: across every section of a checklist built
// WITHOUT a migration report, migrated_by_tool is false and the evidence
// level is "none" — even though source and destination are identical.
func TestChecklistNeverClaimsEvidenceWithoutReport(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")

	c := BuildChecklist(chkInput(src, dest, nil, nil))

	for _, s := range c.Sections {
		if s.MigratedByTool {
			t.Errorf("section %s claims migrated_by_tool without any migration report", s.Section)
		}
		if s.MigrationEvidence != EvidenceNone {
			t.Errorf("section %s evidence = %q without any migration report", s.Section, s.MigrationEvidence)
		}
	}
}

// TestChecklistEvidenceNeverPerItem pins that v0 cannot produce per_item
// evidence: the apply flow does not emit per-item events yet (PR 7C), so
// any per_item value here would be an invented claim.
func TestChecklistEvidenceNeverPerItem(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	for _, s := range c.Sections {
		if s.MigrationEvidence == EvidencePerItem {
			t.Errorf("section %s claims per_item evidence — nothing can produce it before PR 7C", s.Section)
		}
	}
}
