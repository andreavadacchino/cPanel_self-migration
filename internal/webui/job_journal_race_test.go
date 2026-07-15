package webui

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

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
