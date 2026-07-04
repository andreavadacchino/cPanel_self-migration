package workbench

import "testing"

func TestValidStatus(t *testing.T) {
	for _, s := range AllStatuses {
		if !ValidStatus(s) {
			t.Errorf("ValidStatus(%q) = false, want true", s)
		}
	}
	invalids := []Status{"", "unknown", "DRAFT", "Draft", "ready"}
	for _, s := range invalids {
		if ValidStatus(s) {
			t.Errorf("ValidStatus(%q) = true, want false", s)
		}
	}
}

func TestAllStatusesNoDuplicates(t *testing.T) {
	seen := map[Status]bool{}
	for _, s := range AllStatuses {
		if seen[s] {
			t.Fatalf("duplicate status %q in AllStatuses", s)
		}
		seen[s] = true
	}
	if len(AllStatuses) != 14 {
		t.Fatalf("len(AllStatuses) = %d, want 14", len(AllStatuses))
	}
}

func TestValidStep(t *testing.T) {
	for _, s := range AllSteps {
		if !ValidStep(s) {
			t.Errorf("ValidStep(%q) = false, want true", s)
		}
	}
	invalids := []Step{"", "unknown", "SETUP", "Setup"}
	for _, s := range invalids {
		if ValidStep(s) {
			t.Errorf("ValidStep(%q) = true, want false", s)
		}
	}
}

func TestAllStepsNoDuplicates(t *testing.T) {
	seen := map[Step]bool{}
	for _, s := range AllSteps {
		if seen[s] {
			t.Fatalf("duplicate step %q in AllSteps", s)
		}
		seen[s] = true
	}
	if len(AllSteps) != 12 {
		t.Fatalf("len(AllSteps) = %d, want 12", len(AllSteps))
	}
}

func TestValidArtifactKind(t *testing.T) {
	for _, k := range AllArtifactKinds {
		if !ValidArtifactKind(k) {
			t.Errorf("ValidArtifactKind(%q) = false, want true", k)
		}
	}
	invalids := []ArtifactKind{"", "unknown", "host_yaml", "backup_raw"}
	for _, k := range invalids {
		if ValidArtifactKind(k) {
			t.Errorf("ValidArtifactKind(%q) = true, want false", k)
		}
	}
}

func TestAllArtifactKindsNoDuplicates(t *testing.T) {
	seen := map[ArtifactKind]bool{}
	for _, k := range AllArtifactKinds {
		if seen[k] {
			t.Fatalf("duplicate kind %q in AllArtifactKinds", k)
		}
		seen[k] = true
	}
	if len(AllArtifactKinds) != 17 {
		t.Fatalf("len(AllArtifactKinds) = %d, want 17", len(AllArtifactKinds))
	}
}
