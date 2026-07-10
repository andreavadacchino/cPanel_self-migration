package events

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type ExitStatus string

const (
	ExitSuccess     ExitStatus = "success"
	ExitFailed      ExitStatus = "failed"
	ExitInterrupted ExitStatus = "interrupted"
)

type ReportScope struct {
	Mail          bool   `json:"mail"`
	Files         bool   `json:"files"`
	Databases     bool   `json:"databases"`
	DomainFilter  string `json:"domain_filter,omitempty"`
	MailboxFilter string `json:"mailbox_filter,omitempty"`
}

// RunReport is report.json. FormatVersion is stamped by WriteReport.
//
// Version is the executor build version (internal/version), NOT the document
// format version. The two are deliberately distinct fields.
type RunReport struct {
	FormatVersion   int               `json:"format_version"`
	RunID           string            `json:"run_id"`
	Version         string            `json:"version"`
	Mode            string            `json:"mode"`
	Scope           ReportScope       `json:"scope"`
	Source          HostRef           `json:"source"`
	Dest            HostRef           `json:"destination"`
	StartedAt       time.Time         `json:"started_at"`
	FinishedAt      time.Time         `json:"finished_at"`
	ExitStatus      ExitStatus        `json:"exit_status"`
	PhasesCompleted []Phase           `json:"phases_completed"`
	Warnings        []string          `json:"warnings"`
	Errors          []string          `json:"errors"`
	Artifacts       map[string]string `json:"artifacts,omitempty"`
}

func WriteReport(path string, r RunReport) error {
	r.FormatVersion = CurrentFormatVersion
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("events: create directory %s: %w", dir, err)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("events: marshal report: %w", err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("events: write %s: %w", path, err)
	}
	return nil
}
