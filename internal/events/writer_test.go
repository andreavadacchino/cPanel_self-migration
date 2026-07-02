package events

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriterCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	ev := Event{
		RunID:   "test",
		TS:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Level:   LevelInfo,
		Phase:   PhaseConnect,
		Type:    EventPhaseStarted,
		Message: "test event",
	}
	if err := w.Write(ev); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("file is empty")
	}
	// Must be valid JSON.
	var parsed Event
	if err := json.Unmarshal(b[:len(b)-1], &parsed); err != nil { // trim trailing newline
		t.Fatalf("invalid JSON: %v\nraw: %s", err, b)
	}
	if parsed.RunID != "test" {
		t.Errorf("run_id = %q, want %q", parsed.RunID, "test")
	}
}

func TestWriterJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for i := 0; i < 5; i++ {
		ev := Event{
			RunID:   "test",
			TS:      time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
			Level:   LevelInfo,
			Phase:   PhaseConnect,
			Type:    EventPhaseStarted,
			Message: "event",
		}
		if err := w.Write(ev); err != nil {
			t.Fatalf("Write[%d]: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	count := 0
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Errorf("line %d: invalid JSON: %v", count, err)
		}
		count++
	}
	if count != 5 {
		t.Errorf("got %d events, want 5", count)
	}
}

func TestWriterNoSecretsInOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	ev := Event{
		RunID:   "test",
		TS:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Level:   LevelInfo,
		Phase:   PhaseConnect,
		Type:    EventPhaseStarted,
		Message: "connecting",
		Source:  HostRef{IP: "1.2.3.4", User: "myuser"},
		Dest:    HostRef{IP: "5.6.7.8", User: "destuser"},
	}
	if err := w.Write(ev); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := strings.ToLower(string(b))
	// HostRef must not have password/token fields.
	for _, bad := range []string{"password", "ssh_pass", "cpanel_token", "secret"} {
		if strings.Contains(s, bad) {
			t.Errorf("output contains %q: %s", bad, b)
		}
	}
}

func TestWriterRedactsDataSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	ev := Event{
		RunID:   "test",
		TS:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Level:   LevelInfo,
		Phase:   PhaseConnect,
		Type:    EventPhaseCompleted,
		Message: "done",
		Data:    map[string]any{"password": "s3cr3t", "user": "admin"},
	}
	if err := w.Write(ev); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(b)
	if strings.Contains(s, "s3cr3t") {
		t.Errorf("written event contains unredacted password: %s", s)
	}
	if !strings.Contains(s, "admin") {
		t.Errorf("written event missing non-secret value 'admin': %s", s)
	}
	if !strings.Contains(s, "<redacted>") {
		t.Errorf("written event missing redaction placeholder: %s", s)
	}
}

// TestWriterRedactsStructDataSecrets pins the PR 7C hardening: a TYPED
// struct payload (like the apply phase event data) must go through the same
// key-based redaction net as a map[string]any — a sensitive field name in a
// future payload type cannot silently bypass RedactMap.
func TestWriterRedactsStructDataSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	type item struct {
		Item   string `json:"item"`
		Status string `json:"status"`
		Note   string `json:"note,omitempty"`
	}
	type payload struct {
		Items []item `json:"items"`
		Token string `json:"token"`
	}
	ev := Event{
		RunID:   "test",
		TS:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Level:   LevelInfo,
		Phase:   PhaseMigrateMail,
		Type:    EventPhaseCompleted,
		Message: "done",
		Data: payload{
			Items: []item{{Item: "info@example.com", Status: "failed", Note: "account step: timeout"}},
			Token: "s3cr3t-token",
		},
	}
	if err := w.Write(ev); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(b)
	if strings.Contains(s, "s3cr3t-token") {
		t.Errorf("struct payload bypassed redaction, token leaked: %s", s)
	}
	if !strings.Contains(s, "<redacted>") {
		t.Errorf("written event missing redaction placeholder: %s", s)
	}
	for _, want := range []string{"info@example.com", "failed", "account step: timeout"} {
		if !strings.Contains(s, want) {
			t.Errorf("non-sensitive payload content %q lost in redaction: %s", want, s)
		}
	}
}

func TestWriterCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "events.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("file was not created")
	}
}
