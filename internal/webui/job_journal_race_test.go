package webui

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// TestReservedHolderPublishedAtomically pins the invariant behind the fix: an
// exec/orchestrator reservation publishes its identity together with the busy
// flag, an analysis-style tryReserve leaves it nil, and release clears it.
func TestReservedHolderPublishedAtomically(t *testing.T) {
	j := newJobManager(t.TempDir(), func(context.Context, io.Writer, string, []string) error { return nil }, nil)

	// Free slot: no holder, not busy.
	if _, _, ok := j.reservedHolder(); ok {
		t.Fatal("a free slot must have no reserved holder")
	}
	if j.running() {
		t.Fatal("a free slot must not report busy")
	}

	startedAt := time.Unix(1700000000, 0).UTC()
	if acquired, _ := j.tryReserveFor("dns verify", startedAt); !acquired {
		t.Fatal("tryReserveFor on a free slot must succeed")
	}
	// busy and identity are published together, under the same lock.
	if !j.running() {
		t.Fatal("tryReserveFor must publish busy=true with the identity")
	}
	action, at, ok := j.reservedHolder()
	if !ok || action != "dns verify" || !at.Equal(startedAt) {
		t.Fatalf("reservedHolder = (%q, %v, %v), want (dns verify, %v, true)", action, at, ok, startedAt)
	}

	// The slot is exclusive: a second reservation (any kind) is refused AND the
	// refusal returns an immutable conflict snapshotting the current holder under
	// the same lock (finding R3), while the holder itself is left intact.
	if acquired, conflict := j.tryReserveFor("dns apply", startedAt.Add(time.Hour)); acquired {
		t.Fatal("tryReserveFor on a busy slot must fail")
	} else if conflict.kind != slotHolderNamed || conflict.action != "dns verify" || !conflict.startedAt.Equal(startedAt) {
		t.Fatalf("refused tryReserveFor conflict = %+v, want named dns verify @ %v", conflict, startedAt)
	}
	if acquired, conflict := j.tryReserve(); acquired {
		t.Fatal("tryReserve on a busy slot must fail")
	} else if conflict.kind != slotHolderNamed || conflict.action != "dns verify" {
		t.Fatalf("refused tryReserve conflict = %+v, want named dns verify", conflict)
	}
	if a, _, ok := j.reservedHolder(); !ok || a != "dns verify" {
		t.Fatalf("a refused reservation altered the holder: got %q (ok=%v), want dns verify", a, ok)
	}

	j.release()
	if _, _, ok := j.reservedHolder(); ok {
		t.Fatal("reservedHolder must be cleared after release")
	}
	if j.running() {
		t.Fatal("release must clear busy")
	}

	// An analysis-style reservation (tryReserve) holds the slot but publishes no
	// exec identity, so a concurrent 409 falls back to the analysis snapshot.
	if acquired, _ := j.tryReserve(); !acquired {
		t.Fatal("tryReserve on a free slot must succeed")
	}
	if _, _, ok := j.reservedHolder(); ok {
		t.Fatal("tryReserve must not publish an exec identity")
	}
	j.release()

	// A fresh reservation after release carries the NEW identity only — no stale
	// action or started-at survives from the first holder.
	newStart := time.Unix(1700009999, 0).UTC()
	if acquired, _ := j.tryReserveFor("migrate content", newStart); !acquired {
		t.Fatal("tryReserveFor on a re-freed slot must succeed")
	}
	action, at, ok = j.reservedHolder()
	if !ok || action != "migrate content" || !at.Equal(newStart) {
		t.Fatalf("stale holder after re-reserve: (%q, %v, %v), want (migrate content, %v, true)", action, at, ok, newStart)
	}
	j.release()

	// Empty action: tryReserveFor stays PERMISSIVE because every production call
	// site passes a validated non-empty constant (an actionRegistry name or
	// orchestratorAction). The fail-closed backstop lives at the display layer —
	// an empty-action holder yields an anonymous conflict (generic 409), covered
	// by TestBusyMessageEmptyHolderActionIsAnonymous.
}

// newExecServer builds a workbenchExecServer directly (store + one session +
// working dir) so a test can set the per-INSTANCE afterExecReserve seam and
// drive handleExec with no package-global state. Mirrors the direct construction
// already used by TestOrchestratorPerPhaseTimeout.
func newExecServer(t *testing.T, runner StepRunner) (*workbenchExecServer, string) {
	t.Helper()
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := workbench.NewStore(storeDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	sess, err := store.Create("giorginisposi", "src", "dst", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte("src:\n  ip: 1.2.3.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runner == nil {
		runner = func(context.Context, io.Writer, string, []string) error { return nil }
	}
	ws := &workbenchExecServer{
		store: store, csrf: "csrf", runner: runner, base: context.Background(),
		job: newJobManager(dir, runner, context.Background()), dir: dir,
	}
	return ws, sess.ID
}

// execReq builds an /exec POST for the given action. CSRF is enforced by the
// router (server.post), not handleExec, so a direct handleExec call needs only
// the form body.
func execReq(sessID, action string) *http.Request {
	form := url.Values{"action": {action}}
	req := httptest.NewRequest(http.MethodPost, "/workbench/session/"+sessID+"/exec",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

// TestExecBusy409NamesActionWithinReserveWindow deterministically pins the
// concurrent window that made TestJobJournalReadable409 flaky: the winning
// /exec reserves the single-writer slot and only afterwards persists the job
// journal. A second /exec that arrives in between loses the slot and gets a
// 409 — and it must still name the running action, not the generic
// "un'operazione è già in corso" message.
//
// The window is held open by the per-instance afterExecReserve seam (no sleeps,
// no package global): the seam stops the winner exactly inside the
// reserve→journal gap while the probe runs. Pre-fix (slot published before the
// identity) this fails; post-fix (identity published atomically) it passes.
func TestExecBusy409NamesActionWithinReserveWindow(t *testing.T) {
	ws, sessID := newExecServer(t, nil)

	reserved := make(chan struct{})  // winner holds the slot; journal not yet written
	probeDone := make(chan struct{}) // release the winner after the probe
	var reserveOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(probeDone) }) }

	ws.afterExecReserve = func() {
		reserveOnce.Do(func() { close(reserved) })
		<-probeDone
	}

	winnerDone := make(chan struct{})
	go func() {
		defer close(winnerDone)
		ws.handleExec(httptest.NewRecorder(), execReq(sessID, "dns_verify"), sessID)
	}()
	// Always release and join the winner, even if an assertion below fails — no
	// goroutine is left blocked in the seam (release is idempotent; a closed
	// winnerDone is safe to receive from twice).
	t.Cleanup(func() { release(); <-winnerDone })

	// The reservation is the synchronisation; the timeout is only a diagnostic
	// guard against a winner that never reaches the window.
	select {
	case <-reserved:
	case <-time.After(2 * time.Second):
		t.Fatal("winner never reached the reserve→journal window")
	}

	// Inside the reserve→journal window: the slot is held but NO journal exists
	// yet, so a correct 409 can only name the action from the LIVE reserved
	// holder (the probe loses tryReserveFor and returns before the seam runs).
	rr := httptest.NewRecorder()
	ws.handleExec(rr, execReq(sessID, "dns_verify"), sessID)

	if rr.Code != http.StatusConflict {
		t.Fatalf("concurrent exec while slot held: code = %d, want 409", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "an execution is already in progress") {
		t.Errorf("409 still opaque: %q", body)
	}
	if !strings.Contains(body, "dns verify") {
		t.Errorf("409 within the reserve→journal window does not name the running action: %q", body)
	}
}
