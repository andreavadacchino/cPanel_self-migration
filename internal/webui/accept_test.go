package webui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

// okRunner is a StepRunner that records the last argv and succeeds.
type recordingRunner struct{ last []string }

func (r *recordingRunner) run(_ context.Context, _ io.Writer, _ string, argv []string) error {
	r.last = append([]string{}, argv...)
	return nil
}

func okRunnerFn(_ context.Context, _ io.Writer, _ string, _ []string) error { return nil }

var okRunner = StepRunner(okRunnerFn)

// writeChecklistWithActions drops a minimal migration_checklist.json with a
// mix of acceptable and non-acceptable manual actions, so the accept flow
// has real keys to target.
func writeChecklistWithActions(t *testing.T, dir string) accountinventory.MigrationChecklist {
	t.Helper()
	c := accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1, Account: "demoacct",
		OverallStatus: accountinventory.OverallManualActionRequired,
		ManualActions: []accountinventory.ManualAction{
			{ID: "MA-001", Key: "AK-accept01", Type: "CONFIRM_EMAIL_ROUTING", Section: "email_routing",
				BlockingCutover: true, Acceptable: true, Title: "Confirm email routing", OperatorAction: "check"},
			{ID: "MA-002", Key: "AK-blockcron", Type: "RECREATE_CRON", Section: "cron",
				BlockingCutover: true, Acceptable: false, Title: "Recreate cron", OperatorAction: "recreate"},
		},
		Summary: accountinventory.ChecklistSummary{ManualActions: 2},
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "migration_checklist.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return c
}

func acceptForm(csrf, key, reason, operator string) url.Values {
	return url.Values{"csrf": {csrf}, "action_key": {key}, "reason": {reason}, "operator": {operator}}
}

func TestAcceptWritesAcceptancesAndRegenerates(t *testing.T) {
	dir := t.TempDir()
	writeChecklistWithActions(t, dir)

	rec := &recordingRunner{}
	h := newTestHandler(t, dir, rec.run)
	csrf := fetchCSRF(t, h)

	rr := doReq(h, http.MethodPost, "/accept", acceptForm(csrf, "AK-accept01", "verificato col cliente", "andrea"))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("POST /accept = %d (%s), want 303", rr.Code, rr.Body.String())
	}

	// acceptances.json written and valid.
	b, err := os.ReadFile(filepath.Join(dir, "acceptances.json"))
	if err != nil {
		t.Fatalf("acceptances.json not written: %v", err)
	}
	var af accountinventory.AcceptanceFile
	if err := json.Unmarshal(b, &af); err != nil {
		t.Fatal(err)
	}
	if af.Mode != accountinventory.AcceptanceFileMode || len(af.Acceptances) != 1 {
		t.Fatalf("acceptance file = %+v", af)
	}
	a := af.Acceptances[0]
	if a.ActionKey != "AK-accept01" || a.Reason != "verificato col cliente" || a.AcceptedBy != "andrea" || a.AcceptedAt == "" {
		t.Errorf("entry = %+v, want the posted values with a timestamp", a)
	}
	if af.ChecklistFile != "migration_checklist.json" || af.ChecklistSHA256 == "" {
		t.Errorf("checklist binding missing: %q/%q", af.ChecklistFile, af.ChecklistSHA256)
	}

	// The checklist was regenerated WITH the acceptances.
	joined := strings.Join(rec.last, " ")
	if !strings.Contains(joined, "inventory checklist") || !strings.Contains(joined, "--acceptances") {
		t.Errorf("regen argv = %q, want the checklist step composing --acceptances", joined)
	}
}

func TestAcceptNonAcceptableRefused(t *testing.T) {
	dir := t.TempDir()
	writeChecklistWithActions(t, dir)
	h := newTestHandler(t, dir, okRunner)
	csrf := fetchCSRF(t, h)

	rr := doReq(h, http.MethodPost, "/accept", acceptForm(csrf, "AK-blockcron", "voglio saltarlo", "andrea"))
	if rr.Code < 400 || rr.Code >= 500 {
		t.Fatalf("accept of a non-acceptable action = %d, want 4xx", rr.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "acceptances.json")); !os.IsNotExist(err) {
		t.Error("a refused acceptance must not be written")
	}
}

func TestAcceptUnknownKeyRefused(t *testing.T) {
	dir := t.TempDir()
	writeChecklistWithActions(t, dir)
	h := newTestHandler(t, dir, okRunner)
	csrf := fetchCSRF(t, h)

	rr := doReq(h, http.MethodPost, "/accept", acceptForm(csrf, "AK-doesnotexist", "r", "andrea"))
	if rr.Code < 400 || rr.Code >= 500 {
		t.Errorf("unknown key = %d, want 4xx", rr.Code)
	}
}

func TestAcceptRequiresReasonAndOperator(t *testing.T) {
	dir := t.TempDir()
	writeChecklistWithActions(t, dir)
	h := newTestHandler(t, dir, okRunner)
	csrf := fetchCSRF(t, h)

	if rr := doReq(h, http.MethodPost, "/accept", acceptForm(csrf, "AK-accept01", "", "andrea")); rr.Code < 400 {
		t.Errorf("empty reason = %d, want 4xx", rr.Code)
	}
	if rr := doReq(h, http.MethodPost, "/accept", acceptForm(csrf, "AK-accept01", "r", "")); rr.Code < 400 {
		t.Errorf("empty operator = %d, want 4xx", rr.Code)
	}
}

func TestAcceptWithoutChecklistRefused(t *testing.T) {
	dir := t.TempDir()
	h := newTestHandler(t, dir, okRunner)
	csrf := fetchCSRF(t, h)
	rr := doReq(h, http.MethodPost, "/accept", acceptForm(csrf, "AK-accept01", "r", "andrea"))
	if rr.Code < 400 || rr.Code >= 500 {
		t.Errorf("accept with no checklist = %d, want 4xx", rr.Code)
	}
}

func TestAcceptConflictWhileJobRunning(t *testing.T) {
	dir := t.TempDir()
	writeChecklistWithActions(t, dir)
	fr := &fakeRunner{gate: make(chan struct{})}
	h := newTestHandler(t, dir, fr.run)
	saveValidConfig(t, h)
	csrf := fetchCSRF(t, h)

	if rr := doReq(h, http.MethodPost, "/run", url.Values{"csrf": {csrf}}); rr.Code != http.StatusSeeOther {
		t.Fatalf("run = %d", rr.Code)
	}
	if rr := doReq(h, http.MethodPost, "/accept", acceptForm(csrf, "AK-accept01", "r", "andrea")); rr.Code != http.StatusConflict {
		t.Errorf("accept while a job runs = %d, want 409", rr.Code)
	}
	close(fr.gate)
	waitJob(t, h, "Run completed")
}

func TestAcceptRequiresCSRF(t *testing.T) {
	dir := t.TempDir()
	writeChecklistWithActions(t, dir)
	h := newTestHandler(t, dir, okRunner)
	if rr := doReq(h, http.MethodPost, "/accept", acceptForm("", "AK-accept01", "r", "andrea")); rr.Code != http.StatusForbidden {
		t.Errorf("no csrf = %d, want 403", rr.Code)
	}
}
