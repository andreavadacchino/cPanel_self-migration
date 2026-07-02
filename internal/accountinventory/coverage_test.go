package accountinventory

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// PR 1A — coverage manifest. The checklist must declare EVERY area the tool
// knows about with its coverage state, so an area we do not collect can never
// be silently absent from the operator's picture. The manifest is purely
// declarative: it must never create actions, never touch the summary counts,
// and never change the overall verdict.

func TestCoverageRegistryLockstepWithChecklistSections(t *testing.T) {
	areas := CoverageAreas()
	byName := map[string]CoverageArea{}
	for _, a := range areas {
		if _, dup := byName[a.Area]; dup {
			t.Fatalf("duplicate coverage area %q", a.Area)
		}
		byName[a.Area] = a
	}
	// Every checklist section must be declared in the registry with the
	// matching state — adding a section without updating the registry fails.
	for _, name := range checklistSectionOrder {
		a, ok := byName[name]
		if !ok {
			t.Fatalf("checklist section %q missing from the coverage registry", name)
		}
		want := CoverageCovered
		if name == "quota_package" || name == "server_level_config" {
			want = CoverageRootOnly
		}
		if a.State != want {
			t.Errorf("area %q state = %q, want %q", name, a.State, want)
		}
	}
	// And the registry must not invent covered/root_only areas that are not
	// checklist sections (not_collected areas are exactly the extra ones).
	sections := map[string]bool{}
	for _, name := range checklistSectionOrder {
		sections[name] = true
	}
	for _, a := range areas {
		switch a.State {
		case CoverageCovered, CoverageRootOnly:
			if !sections[a.Area] {
				t.Errorf("area %q has state %q but is not a checklist section", a.Area, a.State)
			}
		case CoverageNotCollected:
			if sections[a.Area] {
				t.Errorf("area %q is a checklist section but declared not_collected", a.Area)
			}
			if a.Note == "" {
				t.Errorf("not_collected area %q must carry a note explaining what is at stake", a.Area)
			}
		default:
			t.Errorf("area %q has invalid state %q", a.Area, a.State)
		}
	}
}

func TestCoverageRegistryIsDeterministicAndOrdered(t *testing.T) {
	a1, a2 := CoverageAreas(), CoverageAreas()
	if len(a1) == 0 {
		t.Fatal("coverage registry is empty")
	}
	for i := range a1 {
		if a1[i] != a2[i] {
			t.Fatalf("registry not deterministic at %d: %+v vs %+v", i, a1[i], a2[i])
		}
	}
	// CoverageAreas must return a copy: mutating it must not corrupt the registry.
	a1[0].Note = "mutated"
	if fresh := CoverageAreas(); fresh[0].Note == "mutated" {
		t.Fatal("CoverageAreas must return a copy of the registry, not the backing slice")
	}
	// not_collected areas are sorted by name (deterministic docs/diffs).
	var nc []string
	for _, a := range a2 {
		if a.State == CoverageNotCollected {
			nc = append(nc, a.Area)
		}
	}
	if !sort.StringsAreSorted(nc) {
		t.Errorf("not_collected areas must be sorted by name: %v", nc)
	}
	if len(nc) == 0 {
		t.Error("registry must declare the known not-collected areas (they are the point of PR 1A)")
	}
}

func TestBuildChecklistEmbedsCoverageManifestWithoutSideEffects(t *testing.T) {
	src := chkInventory("source", "src.example", "acct")
	dest := chkInventory("destination", "dest.example", "acct")
	c := BuildChecklist(chkInput(src, dest, nil, nil))

	want := CoverageAreas()
	if len(c.CoverageManifest) != len(want) {
		t.Fatalf("manifest has %d areas, want %d", len(c.CoverageManifest), len(want))
	}
	for i := range want {
		if c.CoverageManifest[i] != want[i] {
			t.Errorf("manifest[%d] = %+v, want %+v", i, c.CoverageManifest[i], want[i])
		}
	}

	// Purely declarative: no manual action and no section may originate from a
	// not_collected area, and the summary must not count manifest entries.
	ncState := map[string]bool{}
	for _, a := range want {
		if a.State == CoverageNotCollected {
			ncState[a.Area] = true
		}
	}
	for _, ma := range c.ManualActions {
		if ncState[ma.Section] {
			t.Errorf("manual action %s references not_collected area %q — the manifest must not generate actions", ma.ID, ma.Section)
		}
	}
	if len(c.Sections) != len(checklistSectionOrder) {
		t.Errorf("sections = %d, want %d — the manifest must not add sections", len(c.Sections), len(checklistSectionOrder))
	}
}

func TestChecklistMarkdownRendersCoverageManifest(t *testing.T) {
	src := chkInventory("source", "src.example", "acct")
	dest := chkInventory("destination", "dest.example", "acct")
	c := BuildChecklist(chkInput(src, dest, nil, nil))

	path := filepath.Join(t.TempDir(), "migration_checklist.md")
	if err := WriteChecklistMarkdown(path, c); err != nil {
		t.Fatalf("WriteChecklistMarkdown: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	md := string(b)
	if !strings.Contains(md, "## Coverage") {
		t.Fatalf("markdown missing the Coverage section")
	}
	for _, a := range CoverageAreas() {
		if !strings.Contains(md, a.Area) || !strings.Contains(md, string(a.State)) {
			t.Errorf("markdown Coverage table missing area %q (state %s)", a.Area, a.State)
		}
	}
}
