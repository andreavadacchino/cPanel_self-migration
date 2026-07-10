// Package events provides structured event emission for migration runs.
// Events are written as JSONL (one JSON object per line) to a file,
// without replacing the existing human-readable stdout output.
package events

import (
	"fmt"
	"strings"
	"time"
)

// CurrentFormatVersion is the version of the events.jsonl and report.json
// document formats. It is NOT the executor build version: that one travels in
// RunReport.Version and comes from internal/version.
//
// The writers stamp it, so no call site has to remember it. Consumers reject a
// document whose format_version is absent, zero, or greater than this.
const CurrentFormatVersion = 1

type Level string

const (
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

type Phase string

const (
	PhaseConnect       Phase = "connect"
	PhaseAnalyzeMail   Phase = "analyze_mail"
	PhaseAnalyzeFiles  Phase = "analyze_files"
	PhaseAnalyzeDB     Phase = "analyze_db"
	PhaseGatherData    Phase = "gather_data"
	PhaseCompareMail   Phase = "compare_mail"
	PhaseCompareFiles  Phase = "compare_files"
	PhaseCompareDB     Phase = "compare_db"
	PhaseCreateDomains Phase = "create_domains"
	PhaseMigrateMail   Phase = "migrate_mail"
	PhaseVerifyMail    Phase = "verify_mail"
	PhaseCopyFiles     Phase = "copy_files"
	PhaseVerifyFiles   Phase = "verify_files"
	PhaseMigrateDB     Phase = "migrate_db"
	PhaseVerifyDB      Phase = "verify_db"
)

type EventType string

const (
	EventPhaseStarted   EventType = "phase_started"
	EventPhaseCompleted EventType = "phase_completed"
	EventPhaseSkipped   EventType = "phase_skipped"
	EventPhaseFailed    EventType = "phase_failed"
	EventRunStarted     EventType = "run_started"
	EventRunCompleted   EventType = "run_completed"
	EventRunFailed      EventType = "run_failed"
)

type HostRef struct {
	IP   string `json:"ip"`
	User string `json:"user"`
}

// Event is one JSONL line. FormatVersion is stamped by Writer.Write, not by
// the emitters, so an event built in-process carries the zero value until it
// is written.
//
// Source and Dest are non-pointer structs: `omitempty` does not apply to
// structs, so "source" and "destination" are always present in the JSON, even
// as {"ip":"","user":""}. The schema depends on that.
type Event struct {
	FormatVersion int       `json:"format_version"`
	RunID         string    `json:"run_id"`
	TS            time.Time `json:"ts"`
	Level         Level     `json:"level"`
	Phase         Phase     `json:"phase"`
	Type          EventType `json:"event"`
	Message       string    `json:"message"`
	Source        HostRef   `json:"source,omitempty"`
	Dest          HostRef   `json:"destination,omitempty"`
	Data          any       `json:"data,omitempty"`
}

type Emitter struct {
	Emit func(Event)
}

func (e Emitter) Send(ev Event) {
	if e.Emit != nil {
		e.Emit(ev)
	}
}

func NewRunID(t time.Time) string {
	return t.Format("run-20060102-150405")
}

func ValidateRunID(id string) error {
	if id == "" {
		return fmt.Errorf("run-id must not be empty")
	}
	if len(id) > 128 {
		return fmt.Errorf("run-id must not exceed 128 characters")
	}
	if strings.ContainsAny(id, "/\\\x00") {
		return fmt.Errorf("run-id must not contain slashes or null bytes")
	}
	return nil
}
