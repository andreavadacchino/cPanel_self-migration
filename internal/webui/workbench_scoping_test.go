package webui

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// TestApplyBlockedByPolicy verifies that a write action is refused (403)
// when the checklist has apply_blocked=true.
func TestApplyBlockedByPolicy(t *testing.T) {
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
	} {
		if _, err := store.SetStatus(sess.ID, s, false, "", time.Now()); err != nil {
			t.Fatalf("SetStatus %s: %v", s, err)
		}
	}

	// Write a checklist with apply_blocked=true
	checklist := map[string]any{"apply_blocked": true, "overall_status": "BLOCKED"}
	b, _ := json.Marshal(checklist)
	if err := os.WriteFile(filepath.Join(dir, "migration_checklist.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte("src:\n  ip: 1.2.3.4\n"), 0600); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRunner{}
	h, err := New(Options{Dir: dir, Runner: fr.run, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	csrf := fetchCSRF(t, h)

	// Try dns_apply (write action) — should be blocked
	form := url.Values{
		"csrf":            {csrf},
		"action":          {"dns_apply"},
		"confirm_account": {"testaccount"},
	}
	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sess.ID+"/exec", form)
	if rr.Code != http.StatusForbidden {
		t.Errorf("apply with apply_blocked: got %d, want 403; body: %s", rr.Code, rr.Body.String())
	}
	if len(fr.recorded()) > 0 {
		t.Error("subprocess invoked despite apply being blocked by policy")
	}
}

// TestApplyAllowedWithCutoverOnlyBlockers verifies that when the checklist
// is BLOCKED overall but apply_blocked=false (only cutover blockers),
// write actions are ALLOWED.
func TestApplyAllowedWithCutoverOnlyBlockers(t *testing.T) {
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
	} {
		if _, err := store.SetStatus(sess.ID, s, false, "", time.Now()); err != nil {
			t.Fatalf("SetStatus %s: %v", s, err)
		}
	}

	// Checklist BLOCKED overall but apply_blocked=false (cutover-only blockers)
	checklist := map[string]any{"apply_blocked": false, "overall_status": "BLOCKED"}
	b, _ := json.Marshal(checklist)
	if err := os.WriteFile(filepath.Join(dir, "migration_checklist.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte("src:\n  ip: 1.2.3.4\n"), 0600); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRunner{}
	h, err := New(Options{Dir: dir, Runner: fr.run, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	csrf := fetchCSRF(t, h)

	// dns_apply should be ALLOWED (cutover-only blockers don't gate apply)
	form := url.Values{
		"csrf":            {csrf},
		"action":          {"dns_apply"},
		"confirm_account": {"testaccount"},
	}
	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sess.ID+"/exec", form)
	if rr.Code == http.StatusForbidden {
		t.Errorf("apply rejected despite cutover-only blockers: %d %s", rr.Code, rr.Body.String())
	}

	calls := fr.recorded()
	if len(calls) == 0 {
		t.Error("subprocess not invoked — apply should be allowed with cutover-only blockers")
	}
}

// TestReadOnlyNotGatedByApplyBlocked verifies that read-only actions
// (verify, plans, pipeline) are never blocked by apply_blocked.
func TestReadOnlyNotGatedByApplyBlocked(t *testing.T) {
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
	} {
		if _, err := store.SetStatus(sess.ID, s, false, "", time.Now()); err != nil {
			t.Fatalf("SetStatus %s: %v", s, err)
		}
	}

	// apply_blocked=true
	checklist := map[string]any{"apply_blocked": true, "overall_status": "BLOCKED"}
	b, _ := json.Marshal(checklist)
	if err := os.WriteFile(filepath.Join(dir, "migration_checklist.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte("src:\n  ip: 1.2.3.4\n"), 0600); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRunner{}
	h, err := New(Options{Dir: dir, Runner: fr.run, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	csrf := fetchCSRF(t, h)

	// dns_verify is read-only — should NOT be blocked
	form := url.Values{
		"csrf":   {csrf},
		"action": {"dns_verify"},
	}
	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sess.ID+"/exec", form)
	if rr.Code == http.StatusForbidden {
		t.Errorf("read-only verify blocked by apply_blocked: %d", rr.Code)
	}

	if len(fr.recorded()) == 0 {
		t.Error("verify not invoked despite being read-only")
	}
}
