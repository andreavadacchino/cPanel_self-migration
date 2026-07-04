package workbench

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/version"
)

// AttachArtifact copies the file at srcPath into the session's artifact
// directory, computes its SHA256, and records it in the session. The kind
// must be one of the known ArtifactKinds. The source file must exist and be
// a regular file. The copy is atomic (write-temp + rename) to prevent
// partial artifacts on crash.
func (s *Store) AttachArtifact(sessionID string, kind ArtifactKind, srcPath string, now time.Time) (*Session, error) {
	if !ValidArtifactKind(kind) {
		return nil, fmt.Errorf("unknown artifact kind %q", kind)
	}
	if !isCleanID(sessionID) {
		return nil, fmt.Errorf("invalid session id %q", sessionID)
	}

	info, err := os.Stat(srcPath)
	if err != nil {
		return nil, fmt.Errorf("artifact source: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("artifact source %q is not a regular file", srcPath)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sess, err := s.readSession(sessionID)
	if err != nil {
		return nil, err
	}

	// Collision-proof destination: use a random suffix
	randBytes := make([]byte, 4)
	if _, err := rand.Read(randBytes); err != nil {
		return nil, fmt.Errorf("generate artifact suffix: %w", err)
	}
	ext := filepath.Ext(srcPath)
	if ext == "" {
		ext = ".json"
	}
	destName := fmt.Sprintf("%s_%s_%s%s", kind, now.UTC().Format("20060102_150405"), hex.EncodeToString(randBytes), ext)
	destPath := filepath.Join(sess.ArtifactDir, destName)

	hash, err := copyFileAtomic(srcPath, destPath)
	if err != nil {
		return nil, fmt.Errorf("copy artifact: %w", err)
	}

	entry := ArtifactEntry{
		Kind:       kind,
		Path:       destPath,
		SHA256:     hash,
		AttachedAt: now,
	}
	sess.Artifacts = append(sess.Artifacts, entry)
	sess.UpdatedAt = now
	sess.Timeline = append(sess.Timeline, TimelineEvent{
		Timestamp:   now,
		Action:      "attach_artifact",
		Reason:      fmt.Sprintf("kind=%s sha256=%s", kind, hash),
		ToolVersion: version.String(),
	})

	if err := s.writeSession(sessionID, sess); err != nil {
		os.Remove(destPath)
		return nil, err
	}
	return sess, nil
}

// copyFileAtomic copies src to dst atomically and returns the SHA256 hex digest.
func copyFileAtomic(src, dst string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return "", err
	}

	h := sha256.New()
	w := io.MultiWriter(out, h)

	if _, err := io.Copy(w, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}

	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
