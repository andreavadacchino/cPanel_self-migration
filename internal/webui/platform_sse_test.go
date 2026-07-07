package webui

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/events"
)

func TestBuildSSESnapshotNoJob(t *testing.T) {
	dir := t.TempDir()
	snap := buildSSESnapshot(dir, time.Now().UTC())
	if !snap.Done {
		t.Error("no job → snapshot must be Done (nothing to stream)")
	}
	if snap.State != "" {
		t.Errorf("state = %q, want empty without a job", snap.State)
	}
}

func TestBuildSSESnapshotRunningWithPhases(t *testing.T) {
	dir := t.TempDir()
	startJobJournal(dir, "s1", orchestratorAction, monT0)
	writeMonitorEvents(t, dir,
		monEv("run1", 0, "", events.EventRunStarted, events.LevelInfo, "started", nil),
		monEv("run1", time.Second, events.PhaseCopyFiles, events.EventPhaseStarted, events.LevelInfo, "", nil),
		monEv("run1", 2*time.Second, events.PhaseCopyFiles, events.EventPhaseCompleted, events.LevelInfo, "", nil),
		monEv("run1", 3*time.Second, events.PhaseMigrateMail, events.EventPhaseStarted, events.LevelInfo, "", nil),
	)
	snap := buildSSESnapshot(dir, monT0.Add(4*time.Second))
	if snap.Done {
		t.Error("a running job with fresh events must not be Done")
	}
	if snap.State != string(jobStateRunning) {
		t.Errorf("state = %q, want running", snap.State)
	}
	if len(snap.Phases) != 2 {
		t.Fatalf("phases = %d, want 2", len(snap.Phases))
	}
	if snap.Pct != 50 { // 1 completed of 2 seen phases
		t.Errorf("pct = %d, want 50 (1/2 phases)", snap.Pct)
	}
}

func TestBuildSSESnapshotCompleted(t *testing.T) {
	dir := t.TempDir()
	finishJobJournal(dir, "s1", orchestratorAction, orchestratorAction,
		monT0, monT0.Add(time.Minute), nil, "Migrazione automatica completata.")
	snap := buildSSESnapshot(dir, time.Now().UTC())
	if !snap.Done {
		t.Error("completed job → Done")
	}
	if snap.State != string(jobStateCompleted) {
		t.Errorf("state = %q, want completed", snap.State)
	}
	if snap.Pct != 100 {
		t.Errorf("pct = %d, want 100", snap.Pct)
	}
	if !strings.Contains(snap.Outcome, "completata") {
		t.Errorf("outcome = %q, want the completion message", snap.Outcome)
	}
}

func TestBuildSSESnapshotFailedIncludesError(t *testing.T) {
	dir := t.TempDir()
	finishJobJournal(dir, "s1", orchestratorAction, "Contenuti",
		monT0, monT0.Add(time.Minute), errors.New("scripted boom"), "Migrazione interrotta.")
	snap := buildSSESnapshot(dir, time.Now().UTC())
	if snap.State != string(jobStateFailed) {
		t.Errorf("state = %q, want failed", snap.State)
	}
	if !snap.Done {
		t.Error("failed job → Done")
	}
	if !strings.Contains(strings.Join(snap.Log, " | "), "scripted boom") {
		t.Errorf("log = %v, want the job error surfaced", snap.Log)
	}
}

// A terminal job makes the stream push a single data event with done:true and
// return immediately (no hang), so httptest can observe it end to end.
func TestHandleSessionEventsTerminalStreams(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	sess, _ := store.Create("giorgini", "s", "d", time.Now())
	finishJobJournal(dir, sess.ID, orchestratorAction, orchestratorAction,
		monT0, monT0.Add(time.Minute), nil, "Migrazione automatica completata.")
	h, err := New(Options{Dir: dir, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	rr := doReq(h, http.MethodGet, "/platform/migrations/"+sess.ID+"/events", nil)
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type = %q, want text/event-stream", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "data:") || !strings.Contains(body, `"done":true`) {
		t.Errorf("stream body = %q, want a data event with done:true", body)
	}
}

func TestHandleSessionEventsNotFound(t *testing.T) {
	dir := t.TempDir()
	store := mustStore(t, dir)
	h, err := New(Options{Dir: dir, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	if rr := doReq(h, http.MethodGet, "/platform/migrations/nonexistent/events", nil); rr.Code != http.StatusNotFound {
		t.Errorf("events for missing session = %d, want 404", rr.Code)
	}
}
