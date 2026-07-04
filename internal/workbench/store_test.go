package workbench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNewStoreCreatesDirectoryWithPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "migrations")
	_, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("root dir perm = %o, want 0700", perm)
	}
}

func TestCreateSession(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	sess, err := s.Create("giorginisposi", "old193", "new78", now)
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID == "" {
		t.Fatal("session ID is empty")
	}
	if sess.Name != "giorginisposi" {
		t.Errorf("name = %q, want giorginisposi", sess.Name)
	}
	if sess.SourceProfile != "old193" {
		t.Errorf("source = %q, want old193", sess.SourceProfile)
	}
	if sess.DestinationProfile != "new78" {
		t.Errorf("dest = %q, want new78", sess.DestinationProfile)
	}
	if sess.Status != StatusDraft {
		t.Errorf("status = %q, want draft", sess.Status)
	}
	if sess.CurrentStep != StepSetup {
		t.Errorf("step = %q, want setup", sess.CurrentStep)
	}
	if !sess.CreatedAt.Equal(now) {
		t.Errorf("created_at = %v, want %v", sess.CreatedAt, now)
	}
	if sess.Artifacts == nil {
		t.Error("artifacts is nil, want empty slice")
	}
	if sess.Timeline == nil {
		t.Error("timeline is nil, want empty slice")
	}

	// Session directory exists with 0700
	info, err := os.Stat(sess.ArtifactDir)
	if err != nil {
		t.Fatalf("artifact dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("artifact dir perm = %o, want 0700", perm)
	}

	// session.json exists with 0600
	sjPath := filepath.Join(s.root, sess.ID, "session.json")
	info, err = os.Stat(sjPath)
	if err != nil {
		t.Fatalf("session.json: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("session.json perm = %o, want 0600", perm)
	}
}

func TestListSessionsDeterministicOrder(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	_, _ = s.Create("beta", "src", "dst", now.Add(1*time.Second))
	_, _ = s.Create("alpha", "src", "dst", now)
	_, _ = s.Create("gamma", "src", "dst", now.Add(2*time.Second))

	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("len = %d, want 3", len(list))
	}
	// Ordered by created_at ascending
	if list[0].Name != "alpha" {
		t.Errorf("list[0].Name = %q, want alpha", list[0].Name)
	}
	if list[1].Name != "beta" {
		t.Errorf("list[1].Name = %q, want beta", list[1].Name)
	}
	if list[2].Name != "gamma" {
		t.Errorf("list[2].Name = %q, want gamma", list[2].Name)
	}

	// Second call: same order
	list2, _ := s.List()
	for i := range list {
		if list[i].ID != list2[i].ID {
			t.Errorf("list order unstable at %d", i)
		}
	}
}

func TestGetSession(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	created, _ := s.Create("test", "src", "dst", now)

	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != created.ID {
		t.Errorf("ID mismatch")
	}
	if got.Name != "test" {
		t.Errorf("Name = %q, want test", got.Name)
	}
}

func TestGetSessionNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing session")
	}
}

func TestUpdateStatus(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	sess, _ := s.Create("test", "src", "dst", now)

	updated, err := s.SetStatus(sess.ID, StatusPreflightRequired, false, "", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != StatusPreflightRequired {
		t.Errorf("status = %q, want preflight_required", updated.Status)
	}
	if !updated.UpdatedAt.Equal(now.Add(time.Minute)) {
		t.Error("updated_at not bumped")
	}
	if len(updated.Timeline) != 1 {
		t.Fatalf("timeline len = %d, want 1", len(updated.Timeline))
	}
	if updated.Timeline[0].FromStatus != StatusDraft {
		t.Errorf("timeline from = %q", updated.Timeline[0].FromStatus)
	}
	if updated.Timeline[0].ToStatus != StatusPreflightRequired {
		t.Errorf("timeline to = %q", updated.Timeline[0].ToStatus)
	}
}

func TestUpdateStatusRejectsInvalid(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	sess, _ := s.Create("test", "src", "dst", now)

	_, err := s.SetStatus(sess.ID, "bogus", false, "", now)
	if err == nil {
		t.Fatal("expected error for invalid status")
	}
}

func TestUpdateStatusRejectsIllegalTransition(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	sess, _ := s.Create("test", "src", "dst", now)

	// draft → cutover_done is not legal
	_, err := s.SetStatus(sess.ID, StatusCutoverDone, false, "", now)
	if err == nil {
		t.Fatal("expected error for illegal transition")
	}
}

func TestUpdateStatusForceBypassesTransitionMatrix(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	sess, _ := s.Create("test", "src", "dst", now)

	// draft → cutover_done would normally fail, but force allows it
	updated, err := s.SetStatus(sess.ID, StatusCutoverDone, true, "emergency override", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("force transition failed: %v", err)
	}
	if updated.Status != StatusCutoverDone {
		t.Errorf("status = %q, want cutover_done", updated.Status)
	}
	if updated.Timeline[0].Reason != "emergency override" {
		t.Errorf("reason = %q", updated.Timeline[0].Reason)
	}
}

func TestUpdateStatusForceRequiresReason(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	sess, _ := s.Create("test", "src", "dst", now)

	// Empty reason
	_, err := s.SetStatus(sess.ID, StatusCutoverDone, true, "", now)
	if err == nil {
		t.Fatal("expected error when force without reason")
	}
	// Too short reason
	_, err = s.SetStatus(sess.ID, StatusCutoverDone, true, "short", now)
	if err == nil {
		t.Fatal("expected error when force with too-short reason")
	}
	// Whitespace-only reason
	_, err = s.SetStatus(sess.ID, StatusCutoverDone, true, "         ", now)
	if err == nil {
		t.Fatal("expected error when force with whitespace-only reason")
	}
}

func TestArchiveSession(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	sess, _ := s.Create("test", "src", "dst", now)
	// Move to a state that can reach archived: use force
	s.SetStatus(sess.ID, StatusCutoverDone, true, "test override for archive", now)

	updated, err := s.SetStatus(sess.ID, StatusArchived, false, "", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != StatusArchived {
		t.Errorf("status = %q, want archived", updated.Status)
	}
}

func TestSessionJSONNoNullArrays(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	sess, _ := s.Create("test", "src", "dst", now)

	raw, err := os.ReadFile(filepath.Join(s.root, sess.ID, "session.json"))
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	// artifacts and timeline must be [] not null
	if m["artifacts"] == nil {
		t.Error("artifacts is null in JSON, want []")
	}
	if m["timeline"] == nil {
		t.Error("timeline is null in JSON, want []")
	}
}

func TestSessionIDStable(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	sess, _ := s.Create("test", "src", "dst", now)

	got, _ := s.Get(sess.ID)
	if got.ID != sess.ID {
		t.Error("ID changed after read-back")
	}
}
