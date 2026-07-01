package events

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNewRunID(t *testing.T) {
	ts := time.Date(2026, 7, 1, 15, 30, 0, 0, time.UTC)
	got := NewRunID(ts)
	if got != "run-20260701-153000" {
		t.Errorf("NewRunID(%v) = %q, want %q", ts, got, "run-20260701-153000")
	}
}

func TestValidateRunID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{name: "valid generated", id: "run-20260701-153000"},
		{name: "valid custom", id: "my-migration-2026"},
		{name: "valid alphanumeric", id: "abc123"},
		{name: "valid with dashes", id: "a-b-c"},
		{name: "valid with underscores", id: "a_b_c"},
		{name: "valid with dots", id: "run.1.2"},
		{name: "empty", id: "", wantErr: true},
		{name: "contains slash", id: "a/b", wantErr: true},
		{name: "contains backslash", id: `a\b`, wantErr: true},
		{name: "contains null", id: "a\x00b", wantErr: true},
		{name: "too long", id: strings.Repeat("a", 129), wantErr: true},
		{name: "max length", id: strings.Repeat("a", 128)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRunID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRunID(%q) error = %v, wantErr %v", tt.id, err, tt.wantErr)
			}
		})
	}
}

func TestEventJSON(t *testing.T) {
	ev := Event{
		RunID:   "run-20260701-153000",
		TS:      time.Date(2026, 7, 1, 15, 30, 0, 123000000, time.UTC),
		Level:   LevelInfo,
		Phase:   PhaseConnect,
		Type:    EventPhaseStarted,
		Message: "Connecting to source and destination",
		Source:  HostRef{IP: "1.2.3.4", User: "srcuser"},
		Dest:    HostRef{IP: "5.6.7.8", User: "destuser"},
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(b)

	// Must contain all required fields.
	for _, want := range []string{
		`"run_id":"run-20260701-153000"`,
		`"level":"info"`,
		`"phase":"connect"`,
		`"event":"phase_started"`,
		`"source":{"ip":"1.2.3.4","user":"srcuser"}`,
		`"destination":{"ip":"5.6.7.8","user":"destuser"}`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON missing %q in %s", want, s)
		}
	}
	// Must NOT contain passwords, tokens, or other secrets.
	for _, bad := range []string{"password", "token", "secret", "key"} {
		if strings.Contains(strings.ToLower(s), bad) {
			t.Errorf("JSON contains sensitive keyword %q: %s", bad, s)
		}
	}
}

func TestEventJSONRoundTrip(t *testing.T) {
	ev := Event{
		RunID:   "test-run",
		TS:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Level:   LevelWarn,
		Phase:   PhaseAnalyzeMail,
		Type:    EventPhaseCompleted,
		Message: "done",
		Source:  HostRef{IP: "10.0.0.1", User: "u1"},
		Dest:    HostRef{IP: "10.0.0.2", User: "u2"},
		Data:    map[string]any{"mailboxes": 42},
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got Event
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.RunID != ev.RunID {
		t.Errorf("RunID: got %q, want %q", got.RunID, ev.RunID)
	}
	if got.Level != ev.Level {
		t.Errorf("Level: got %q, want %q", got.Level, ev.Level)
	}
	if got.Phase != ev.Phase {
		t.Errorf("Phase: got %q, want %q", got.Phase, ev.Phase)
	}
	if got.Type != ev.Type {
		t.Errorf("Type: got %q, want %q", got.Type, ev.Type)
	}
}

func TestPhaseConstants(t *testing.T) {
	phases := []Phase{
		PhaseConnect, PhaseAnalyzeMail, PhaseAnalyzeFiles,
		PhaseAnalyzeDB, PhaseGatherData, PhaseCompareMail,
		PhaseCompareFiles, PhaseCompareDB, PhaseCreateDomains,
		PhaseMigrateMail, PhaseVerifyMail, PhaseCopyFiles,
		PhaseVerifyFiles, PhaseMigrateDB, PhaseVerifyDB,
	}
	seen := make(map[Phase]bool)
	for _, p := range phases {
		if p == "" {
			t.Error("empty phase constant")
		}
		if seen[p] {
			t.Errorf("duplicate phase %q", p)
		}
		seen[p] = true
	}
}

func TestLevelConstants(t *testing.T) {
	levels := []Level{LevelInfo, LevelWarn, LevelError}
	seen := make(map[Level]bool)
	for _, l := range levels {
		if l == "" {
			t.Error("empty level constant")
		}
		if seen[l] {
			t.Errorf("duplicate level %q", l)
		}
		seen[l] = true
	}
}

func TestEventTypeConstants(t *testing.T) {
	types := []EventType{
		EventPhaseStarted, EventPhaseCompleted, EventPhaseSkipped,
		EventPhaseFailed, EventRunStarted, EventRunCompleted, EventRunFailed,
	}
	seen := make(map[EventType]bool)
	for _, et := range types {
		if et == "" {
			t.Error("empty event type constant")
		}
		if seen[et] {
			t.Errorf("duplicate event type %q", et)
		}
		seen[et] = true
	}
}

func TestEmitterNilSafe(t *testing.T) {
	var em Emitter
	// Must not panic with nil Emit.
	em.Send(Event{Phase: PhaseConnect, Type: EventPhaseStarted})
}

func TestEmitterCallsFunc(t *testing.T) {
	var called bool
	em := Emitter{Emit: func(e Event) { called = true }}
	em.Send(Event{Phase: PhaseConnect, Type: EventPhaseStarted})
	if !called {
		t.Error("Emitter.Send did not call Emit func")
	}
}
