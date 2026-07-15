package webui

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
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
	if !j.tryReserveFor("dns verify", startedAt) {
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
	// existing holder identity is left intact (no partial clobber on the loser).
	if j.tryReserveFor("dns apply", startedAt.Add(time.Hour)) {
		t.Fatal("tryReserveFor on a busy slot must fail")
	}
	if j.tryReserve() {
		t.Fatal("tryReserve on a busy slot must fail")
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
	if !j.tryReserve() {
		t.Fatal("tryReserve on a free slot must succeed")
	}
	if _, _, ok := j.reservedHolder(); ok {
		t.Fatal("tryReserve must not publish an exec identity")
	}
	j.release()

	// A fresh reservation after release carries the NEW identity only — no stale
	// action or started-at survives from the first holder.
	newStart := time.Unix(1700009999, 0).UTC()
	if !j.tryReserveFor("migrate content", newStart) {
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
	// busyMessage's «action != ""» guard — covered by
	// TestBusyMessageEmptyHolderActionFallsThrough.
}

// TestExecBusy409NamesActionWithinReserveWindow deterministically pins the
// concurrent window that made TestJobJournalReadable409 flaky: the winning
// /exec reserves the single-writer slot and only afterwards persists the job
// journal. A second /exec that arrives in between loses the slot and gets a
// 409 — and it must still name the running action, not fall back to the
// generic "un'operazione è già in corso" message.
//
// The window is closed with a channel-driven test hook (no sleeps): the hook
// stops the winner exactly inside the reserve→journal gap while the probe runs.
// Pre-fix (slot published before the identity) this fails; post-fix (identity
// published atomically with the slot) it passes.
func TestExecBusy409NamesActionWithinReserveWindow(t *testing.T) {
	h, _, sessID, csrf, _ := newJournalEnv(t)

	reserved := make(chan struct{})  // winner has taken the slot, journal not yet written
	probeDone := make(chan struct{}) // release the winner after the probe

	execAfterReserveHook = func() {
		close(reserved)
		<-probeDone
	}
	defer func() { execAfterReserveHook = nil }()

	form := url.Values{"csrf": {csrf}, "action": {"dns_verify"}}
	done := make(chan struct{})
	go func() {
		doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)
		close(done)
	}()

	<-reserved // the slot is held; we are inside the reserve→journal window
	rr := doWorkbenchReq(h, http.MethodPost, "/workbench/session/"+sessID+"/exec", form)
	close(probeDone) // let the winner persist the journal and finish
	<-done

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
