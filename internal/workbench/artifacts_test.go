package workbench

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAttachArtifactCopiesFile(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	sess, _ := s.Create("test", "src", "dst", now)

	// Create a source artifact file
	srcFile := filepath.Join(t.TempDir(), "checklist.json")
	content := []byte(`{"status":"ready"}`)
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatal(err)
	}

	updated, err := s.AttachArtifact(sess.ID, ArtifactMigrationChecklist, srcFile, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	if len(updated.Artifacts) != 1 {
		t.Fatalf("artifacts len = %d, want 1", len(updated.Artifacts))
	}
	art := updated.Artifacts[0]
	if art.Kind != ArtifactMigrationChecklist {
		t.Errorf("kind = %q", art.Kind)
	}

	// Verify the file was COPIED into the session dir
	copiedData, err := os.ReadFile(art.Path)
	if err != nil {
		t.Fatalf("read copied artifact: %v", err)
	}
	if string(copiedData) != string(content) {
		t.Error("copied content differs")
	}

	// Original removal should not affect the copy
	os.Remove(srcFile)
	if _, err := os.ReadFile(art.Path); err != nil {
		t.Error("copy lost when original removed")
	}
}

func TestAttachArtifactComputesSHA256(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	sess, _ := s.Create("test", "src", "dst", now)

	content := []byte(`{"data":"value"}`)
	srcFile := filepath.Join(t.TempDir(), "data.json")
	os.WriteFile(srcFile, content, 0644)

	updated, _ := s.AttachArtifact(sess.ID, ArtifactPolicyReport, srcFile, now)
	art := updated.Artifacts[0]

	h := sha256.Sum256(content)
	expected := hex.EncodeToString(h[:])
	if art.SHA256 != expected {
		t.Errorf("sha256 = %q, want %q", art.SHA256, expected)
	}
}

func TestAttachArtifactRejectsUnknownKind(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	sess, _ := s.Create("test", "src", "dst", now)

	srcFile := filepath.Join(t.TempDir(), "data.json")
	os.WriteFile(srcFile, []byte("x"), 0644)

	_, err := s.AttachArtifact(sess.ID, "host_yaml", srcFile, now)
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestAttachArtifactRejectsMissingFile(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	sess, _ := s.Create("test", "src", "dst", now)

	_, err := s.AttachArtifact(sess.ID, ArtifactDNSPlan, "/nonexistent/file.json", now)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestAttachArtifactPermissions(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	sess, _ := s.Create("test", "src", "dst", now)

	srcFile := filepath.Join(t.TempDir(), "data.json")
	os.WriteFile(srcFile, []byte("x"), 0644)

	updated, _ := s.AttachArtifact(sess.ID, ArtifactDNSPlan, srcFile, now)
	art := updated.Artifacts[0]

	info, err := os.Stat(art.Path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("artifact perm = %o, want 0600", perm)
	}
}

func TestAttachArtifactPathTraversal(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	sess, _ := s.Create("test", "src", "dst", now)

	// Try to use a traversal path as session ID
	_, err := s.AttachArtifact("../../../etc/passwd", ArtifactDNSPlan, "/tmp/x", now)
	if err == nil {
		t.Fatal("expected error for path traversal in session ID")
	}

	// Valid session but source file with traversal in name should still work
	// (we copy by content, the destination filename is derived from kind)
	srcFile := filepath.Join(t.TempDir(), "normal.json")
	os.WriteFile(srcFile, []byte("ok"), 0644)
	updated, err := s.AttachArtifact(sess.ID, ArtifactDNSPlan, srcFile, now)
	if err != nil {
		t.Fatalf("valid attach failed: %v", err)
	}
	// The copied file must be inside the session's artifact dir
	rel, relErr := filepath.Rel(sess.ArtifactDir, updated.Artifacts[0].Path)
	if relErr != nil || len(rel) > 1 && rel[:2] == ".." {
		t.Errorf("artifact stored outside session dir: %s", updated.Artifacts[0].Path)
	}
}

func TestAttachArtifactMultipleSameKind(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	sess, _ := s.Create("test", "src", "dst", now)

	srcFile := filepath.Join(t.TempDir(), "v1.json")
	os.WriteFile(srcFile, []byte("v1"), 0644)
	s.AttachArtifact(sess.ID, ArtifactDNSPlan, srcFile, now)

	srcFile2 := filepath.Join(t.TempDir(), "v2.json")
	os.WriteFile(srcFile2, []byte("v2"), 0644)
	updated, _ := s.AttachArtifact(sess.ID, ArtifactDNSPlan, srcFile2, now.Add(time.Second))

	// Both versions are kept (append, not replace)
	if len(updated.Artifacts) != 2 {
		t.Fatalf("artifacts len = %d, want 2", len(updated.Artifacts))
	}
}
