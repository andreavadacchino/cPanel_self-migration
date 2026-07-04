package workbench

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/version"
)

// Store manages migration sessions on the local filesystem.
// Sessions are stored as individual JSON files under root/<session-id>/session.json.
// All writes are atomic (write-temp + fsync + rename). The mutex serializes
// in-process access. Cross-process serialization is not implemented because
// this is a single-operator CLI tool — each invocation is short-lived and
// sequential. If Store is ever embedded in a long-running server, add flock.
type Store struct {
	mu   sync.Mutex
	root string
}

// NewStore creates a Store rooted at dir, creating the directory with 0700
// permissions if it does not exist.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	// Ensure permissions even if directory already existed
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, fmt.Errorf("chmod store dir: %w", err)
	}
	return &Store{root: dir}, nil
}

// Create initializes a new migration session and persists it.
func (s *Store) Create(name, sourceProfile, destProfile string, now time.Time) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id, err := generateID(now)
	if err != nil {
		return nil, err
	}

	sessDir := filepath.Join(s.root, id)
	if err := os.Mkdir(sessDir, 0700); err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("session id collision %q (retry)", id)
		}
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	artifactDir := filepath.Join(sessDir, "artifacts")
	if err := os.Mkdir(artifactDir, 0700); err != nil {
		return nil, fmt.Errorf("create artifact dir: %w", err)
	}

	sess := &Session{
		ID:                 id,
		Name:               name,
		SourceProfile:      sourceProfile,
		DestinationProfile: destProfile,
		Status:             StatusDraft,
		CurrentStep:        StepSetup,
		ArtifactDir:        artifactDir,
		CreatedAt:          now,
		UpdatedAt:          now,
		LastError:          "",
		Artifacts:          []ArtifactEntry{},
		Timeline:           []TimelineEvent{},
		ToolVersion:        version.String(),
	}

	if err := s.writeSession(id, sess); err != nil {
		os.RemoveAll(sessDir)
		return nil, err
	}
	return sess, nil
}

// List returns all sessions ordered by created_at ascending.
func (s *Store) List() ([]Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("read store dir: %w", err)
	}

	var sessions []Session
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sess, err := s.readSession(e.Name())
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping corrupted session %q: %v\n", e.Name(), err)
			continue
		}
		sessions = append(sessions, *sess)
	}

	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].CreatedAt.Equal(sessions[j].CreatedAt) {
			return sessions[i].ID < sessions[j].ID
		}
		return sessions[i].CreatedAt.Before(sessions[j].CreatedAt)
	})

	if sessions == nil {
		sessions = []Session{}
	}
	return sessions, nil
}

// Get retrieves a single session by ID.
func (s *Store) Get(id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readSession(id)
}

// SetStatus transitions a session to a new status. If force is true, the
// transition matrix is bypassed but a non-empty reason is required. The
// transition is recorded in the session timeline.
func (s *Store) SetStatus(id string, to Status, force bool, reason string, now time.Time) (*Session, error) {
	if !ValidStatus(to) {
		return nil, fmt.Errorf("invalid target status %q", to)
	}
	if force {
		trimmed := strings.TrimSpace(reason)
		if len(trimmed) < 10 {
			return nil, fmt.Errorf("--force requires a reason of at least 10 characters (got %d)", len(trimmed))
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sess, err := s.readSession(id)
	if err != nil {
		return nil, err
	}

	if !force {
		if err := ValidateTransition(sess.Status, to); err != nil {
			return nil, err
		}
	}

	event := TimelineEvent{
		Timestamp:   now,
		Action:      "status_change",
		FromStatus:  sess.Status,
		ToStatus:    to,
		Reason:      reason,
		ToolVersion: version.String(),
	}
	if force {
		event.Action = "forced_status_change"
	}

	sess.Status = to
	sess.UpdatedAt = now
	sess.Timeline = append(sess.Timeline, event)

	if err := s.writeSession(id, sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// writeSession atomically persists the session (write-temp + rename).
// The folderID parameter is the validated directory name — never derived
// from sess.ID to prevent path traversal via crafted JSON content.
func (s *Store) writeSession(folderID string, sess *Session) error {
	if !isCleanID(folderID) {
		return fmt.Errorf("invalid folder id %q", folderID)
	}
	sessDir := filepath.Join(s.root, folderID)
	if err := os.MkdirAll(sessDir, 0700); err != nil {
		return fmt.Errorf("ensure session dir: %w", err)
	}

	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	data = append(data, '\n')

	target := filepath.Join(sessDir, "session.json")
	tmp, err := os.CreateTemp(sessDir, "session-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp session: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp session: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("sync temp session: %w", err)
	}
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("chmod temp session: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp session: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename session: %w", err)
	}
	return nil
}

// readSession loads a session from disk.
func (s *Store) readSession(id string) (*Session, error) {
	if !isCleanID(id) {
		return nil, fmt.Errorf("invalid session id %q", id)
	}
	path := filepath.Join(s.root, id, "session.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session %q not found", id)
		}
		return nil, fmt.Errorf("read session %q: %w", id, err)
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("parse session %q: %w", id, err)
	}
	return &sess, nil
}

// generateID creates a stable session ID in the format mig_YYYYMMDD_<random>.
func generateID(now time.Time) (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return fmt.Sprintf("mig_%s_%s", now.UTC().Format("20060102"), hex.EncodeToString(b)), nil
}

// isCleanID validates that an ID contains no path separators or traversal.
func isCleanID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for _, c := range id {
		if c == '/' || c == '\\' || c == 0 {
			return false
		}
	}
	return filepath.Base(id) == id
}
