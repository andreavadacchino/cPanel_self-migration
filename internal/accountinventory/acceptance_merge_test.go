package accountinventory

import "testing"

func acc(key, reason, by string) OperatorAcceptance {
	return OperatorAcceptance{ActionKey: key, Reason: reason, AcceptedBy: by, AcceptedAt: "2026-07-02T10:00:00Z"}
}

func TestMergeAcceptanceInsert(t *testing.T) {
	got := MergeAcceptance(nil, "migration_checklist.json", "abc", acc("AK-1", "r1", "andrea"))
	if got.Mode != AcceptanceFileMode || got.FormatVersion != 1 {
		t.Errorf("mode/version = %q/%d", got.Mode, got.FormatVersion)
	}
	if got.ChecklistFile != "migration_checklist.json" || got.ChecklistSHA256 != "abc" {
		t.Errorf("checklist binding = %q/%q", got.ChecklistFile, got.ChecklistSHA256)
	}
	if len(got.Acceptances) != 1 || got.Acceptances[0].ActionKey != "AK-1" {
		t.Fatalf("acceptances = %+v, want the inserted entry", got.Acceptances)
	}
}

func TestMergeAcceptanceUpsertByKey(t *testing.T) {
	base := MergeAcceptance(nil, "migration_checklist.json", "abc", acc("AK-1", "old", "andrea"))
	base = MergeAcceptance(&base, "migration_checklist.json", "abc", acc("AK-2", "r2", "andrea"))
	// Re-accept AK-1 with a new reason/author → UPDATE in place, not a dup.
	got := MergeAcceptance(&base, "migration_checklist.json", "abc", acc("AK-1", "new reason", "luca"))

	if len(got.Acceptances) != 2 {
		t.Fatalf("acceptances = %d, want 2 (upsert, not duplicate)", len(got.Acceptances))
	}
	var a1 *OperatorAcceptance
	for i := range got.Acceptances {
		if got.Acceptances[i].ActionKey == "AK-1" {
			a1 = &got.Acceptances[i]
		}
	}
	if a1 == nil || a1.Reason != "new reason" || a1.AcceptedBy != "luca" {
		t.Errorf("AK-1 = %+v, want the updated reason/author", a1)
	}
	// Order is stable: AK-1 keeps its original position (first).
	if got.Acceptances[0].ActionKey != "AK-1" || got.Acceptances[1].ActionKey != "AK-2" {
		t.Errorf("order = %s,%s, want AK-1,AK-2 preserved", got.Acceptances[0].ActionKey, got.Acceptances[1].ActionKey)
	}
}

func TestMergeAcceptanceRestampsSHA(t *testing.T) {
	base := MergeAcceptance(nil, "migration_checklist.json", "sha-X", acc("AK-1", "r1", "andrea"))
	// A later accept, after the checklist was regenerated (new sha), must
	// re-stamp the WHOLE file to the current sha so the strict hash check
	// keeps matching on the next regeneration.
	got := MergeAcceptance(&base, "migration_checklist.json", "sha-Y", acc("AK-2", "r2", "andrea"))
	if got.ChecklistSHA256 != "sha-Y" {
		t.Errorf("sha = %q, want the current sha-Y", got.ChecklistSHA256)
	}
}

func TestMergeAcceptancePreservesExistingWhenUpsertingDifferentKey(t *testing.T) {
	base := AcceptanceFile{
		Mode: AcceptanceFileMode, FormatVersion: 1,
		ChecklistFile: "migration_checklist.json", ChecklistSHA256: "old",
		Acceptances: []OperatorAcceptance{acc("AK-1", "r1", "a"), acc("AK-2", "r2", "b")},
	}
	got := MergeAcceptance(&base, "migration_checklist.json", "new", acc("AK-3", "r3", "c"))
	if len(got.Acceptances) != 3 {
		t.Fatalf("acceptances = %d, want 3 (two preserved + one added)", len(got.Acceptances))
	}
	for i, k := range []string{"AK-1", "AK-2", "AK-3"} {
		if got.Acceptances[i].ActionKey != k {
			t.Errorf("pos %d = %s, want %s", i, got.Acceptances[i].ActionKey, k)
		}
	}
}
