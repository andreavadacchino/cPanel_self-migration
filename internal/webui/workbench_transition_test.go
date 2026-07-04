package webui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// ---------------------------------------------------------------------------
// Test: auto-transition to ready_for_cutover when all verify reports CLEAN
// ---------------------------------------------------------------------------

func TestAutoTransitionReadyForCutover(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := workbench.NewStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	sess, err := store.Create("testaccount", "src", "dst", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Advance to verification_required
	for _, s := range []workbench.Status{
		workbench.StatusPreflightRequired,
		workbench.StatusInventoryReady,
		workbench.StatusChecklistReady,
		workbench.StatusReadyForApply,
		workbench.StatusApplyInProgress,
		workbench.StatusApplyDone,
		workbench.StatusVerificationRequired,
	} {
		if _, err := store.SetStatus(sess.ID, s, false, "", time.Now()); err != nil {
			t.Fatalf("SetStatus %s: %v", s, err)
		}
	}

	// Write CLEAN verify reports for all 3 tracks
	writeVerifyReport(t, dir, "dns_verify_report.json", true)
	writeVerifyReport(t, dir, "email_verify_report.json", true)
	writeVerifyReport(t, dir, "cron_verify_report.json", true)

	// Attempt auto-transition
	transitioned := tryAutoTransitionReadyForCutover(store, sess.ID, dir)
	if !transitioned {
		t.Error("expected auto-transition to ready_for_cutover, got false")
	}

	// Verify session is now ready_for_cutover
	updated, err := store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != workbench.StatusReadyForCutover {
		t.Errorf("status = %s, want ready_for_cutover", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// Test: NO auto-transition when one verify is NOT CLEAN
// ---------------------------------------------------------------------------

func TestNoAutoTransitionWhenVerifyNotClean(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := workbench.NewStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	sess, err := store.Create("testaccount", "src", "dst", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []workbench.Status{
		workbench.StatusPreflightRequired,
		workbench.StatusInventoryReady,
		workbench.StatusChecklistReady,
		workbench.StatusReadyForApply,
		workbench.StatusApplyInProgress,
		workbench.StatusApplyDone,
		workbench.StatusVerificationRequired,
	} {
		if _, err := store.SetStatus(sess.ID, s, false, "", time.Now()); err != nil {
			t.Fatalf("SetStatus %s: %v", s, err)
		}
	}

	// DNS clean, email NOT clean, cron clean
	writeVerifyReport(t, dir, "dns_verify_report.json", true)
	writeVerifyReport(t, dir, "email_verify_report.json", false) // NOT CLEAN
	writeVerifyReport(t, dir, "cron_verify_report.json", true)

	transitioned := tryAutoTransitionReadyForCutover(store, sess.ID, dir)
	if transitioned {
		t.Error("should NOT transition when email verify is not clean")
	}

	updated, err := store.Get(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != workbench.StatusVerificationRequired {
		t.Errorf("status = %s, want verification_required (unchanged)", updated.Status)
	}
}

// ---------------------------------------------------------------------------
// Test: NO auto-transition when verify report is MISSING
// ---------------------------------------------------------------------------

func TestNoAutoTransitionWhenVerifyMissing(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := workbench.NewStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	sess, err := store.Create("testaccount", "src", "dst", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []workbench.Status{
		workbench.StatusPreflightRequired,
		workbench.StatusInventoryReady,
		workbench.StatusChecklistReady,
		workbench.StatusReadyForApply,
		workbench.StatusApplyInProgress,
		workbench.StatusApplyDone,
		workbench.StatusVerificationRequired,
	} {
		if _, err := store.SetStatus(sess.ID, s, false, "", time.Now()); err != nil {
			t.Fatalf("SetStatus %s: %v", s, err)
		}
	}

	// Only dns present
	writeVerifyReport(t, dir, "dns_verify_report.json", true)
	// email and cron missing

	transitioned := tryAutoTransitionReadyForCutover(store, sess.ID, dir)
	if transitioned {
		t.Error("should NOT transition when verify reports are missing")
	}
}

// ---------------------------------------------------------------------------
// Test: NO auto-transition from wrong state (e.g. already blocked)
// ---------------------------------------------------------------------------

func TestNoAutoTransitionFromWrongState(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := workbench.NewStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	sess, err := store.Create("testaccount", "src", "dst", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Advance to verification_required, then force to blocked
	for _, s := range []workbench.Status{
		workbench.StatusPreflightRequired,
		workbench.StatusInventoryReady,
		workbench.StatusChecklistReady,
		workbench.StatusReadyForApply,
		workbench.StatusApplyInProgress,
		workbench.StatusApplyDone,
		workbench.StatusVerificationRequired,
	} {
		if _, err := store.SetStatus(sess.ID, s, false, "", time.Now()); err != nil {
			t.Fatalf("SetStatus %s: %v", s, err)
		}
	}
	// Force to blocked
	if _, err := store.SetStatus(sess.ID, workbench.StatusBlocked, false, "", time.Now()); err != nil {
		t.Fatalf("SetStatus blocked: %v", err)
	}

	// All reports clean
	writeVerifyReport(t, dir, "dns_verify_report.json", true)
	writeVerifyReport(t, dir, "email_verify_report.json", true)
	writeVerifyReport(t, dir, "cron_verify_report.json", true)

	transitioned := tryAutoTransitionReadyForCutover(store, sess.ID, dir)
	if transitioned {
		t.Error("should NOT transition from blocked state even with clean reports")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func writeVerifyReport(t *testing.T, dir, filename string, clean bool) {
	t.Helper()
	report := map[string]any{
		"clean": clean,
		"summary": map[string]int{
			"applied": 3,
			"pending": 0,
			"drift":   0,
		},
	}
	if !clean {
		report["summary"] = map[string]int{
			"applied": 2,
			"pending": 0,
			"drift":   1,
		}
	}
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), b, 0600); err != nil {
		t.Fatal(err)
	}
}
