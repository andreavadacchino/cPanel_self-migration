package events

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunReportJSON(t *testing.T) {
	r := RunReport{
		RunID:   "run-20260701-153000",
		Version: "2.2.1",
		Mode:    "dry-run",
		Scope: ReportScope{
			Mail:          true,
			Files:         true,
			Databases:     true,
			DomainFilter:  "",
			MailboxFilter: "",
		},
		Source:          HostRef{IP: "1.2.3.4", User: "srcuser"},
		Dest:            HostRef{IP: "5.6.7.8", User: "destuser"},
		StartedAt:       time.Date(2026, 7, 1, 15, 30, 0, 0, time.UTC),
		FinishedAt:      time.Date(2026, 7, 1, 15, 35, 0, 0, time.UTC),
		ExitStatus:      ExitSuccess,
		PhasesCompleted: []Phase{PhaseConnect, PhaseAnalyzeMail},
		Warnings:        []string{},
		Errors:          []string{},
	}

	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(b)

	for _, want := range []string{
		`"run_id"`, `"version"`, `"mode"`, `"scope"`,
		`"source"`, `"destination"`, `"started_at"`, `"finished_at"`,
		`"exit_status"`, `"phases_completed"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("report missing field %s", want)
		}
	}
}

func TestRunReportRoundTrip(t *testing.T) {
	r := RunReport{
		RunID:      "test",
		Version:    "1.0.0",
		Mode:       "apply",
		ExitStatus: ExitFailed,
		Errors:     []string{"something went wrong"},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got RunReport
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.RunID != r.RunID {
		t.Errorf("RunID: got %q, want %q", got.RunID, r.RunID)
	}
	if got.ExitStatus != r.ExitStatus {
		t.Errorf("ExitStatus: got %q, want %q", got.ExitStatus, r.ExitStatus)
	}
	if len(got.Errors) != 1 {
		t.Fatalf("Errors: got %d, want 1", len(got.Errors))
	}
}

func TestWriteReport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	r := RunReport{
		RunID:      "test",
		Version:    "2.2.1",
		Mode:       "dry-run",
		ExitStatus: ExitSuccess,
		Source:     HostRef{IP: "1.2.3.4", User: "u"},
	}
	if err := WriteReport(path, r); err != nil {
		t.Fatalf("WriteReport: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var got RunReport
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.RunID != "test" {
		t.Errorf("RunID: got %q, want %q", got.RunID, "test")
	}
}

func TestExitStatusConstants(t *testing.T) {
	statuses := []ExitStatus{ExitSuccess, ExitFailed, ExitInterrupted}
	seen := make(map[ExitStatus]bool)
	for _, s := range statuses {
		if s == "" {
			t.Error("empty exit status")
		}
		if seen[s] {
			t.Errorf("duplicate exit status %q", s)
		}
		seen[s] = true
	}
}

func TestReportNoSecrets(t *testing.T) {
	r := RunReport{
		RunID:  "test",
		Source: HostRef{IP: "1.2.3.4", User: "admin"},
		Dest:   HostRef{IP: "5.6.7.8", User: "dest"},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := strings.ToLower(string(b))
	for _, bad := range []string{"password", "ssh_pass", "token", "secret"} {
		if strings.Contains(s, bad) {
			t.Errorf("report contains %q: %s", bad, string(b))
		}
	}
}
