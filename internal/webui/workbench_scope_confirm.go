package webui

// Fase 2 — Scope Confirmation after Preflight.
//
// After the preflight, the operator reviews the Migration Plan (Fase 1) and then
// confirms/refines WHAT to migrate automatically. This file is the small,
// testable core of that step:
//
//   - preset → ContentSelection mapping (DNS is never in a preset's automatic
//     set; it is a manual/verifiable inclusion via its own checkbox);
//   - the edit gate (scope is frozen once a migration write has run or a job is
//     live), so a confirmed scope stays consistent with what was applied;
//   - the confirm handler: CSRF-protected metadata mutation of Session.Setup,
//     NOT a migration write. It does NOT enable the one-click start, does NOT
//     touch /exec, actionRegistry, pipelineSteps or the strong-confirmation gate,
//     and does NOT make contentScope a server-side gate (that is Fase 3).

import (
	"net/http"
	"net/url"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// scopePresets maps a preset id to the AUTOMATIC content areas it selects. DNS
// is deliberately absent — it is manual/verifiable and only added via the
// independent "includi DNS" checkbox.
var scopePresets = map[string]workbench.ContentSelection{
	"all_safe":  {Files: true, Databases: true, Email: true, EmailConfig: true, Cron: true},
	"site":      {Files: true, Databases: true},
	"email":     {Email: true, EmailConfig: true},
	"files":     {Files: true},
	"databases": {Databases: true},
}

// presetContent resolves the submitted preset (+ the independent DNS checkbox)
// into a ContentSelection. "custom" reads each area checkbox directly. Returns
// ok=false for an unknown preset (tamper/programmer error).
func presetContent(preset string, form url.Values) (workbench.ContentSelection, bool) {
	if preset == "custom" {
		return workbench.ContentSelection{
			Files:       form.Get("files") == "1",
			Databases:   form.Get("databases") == "1",
			Email:       form.Get("email") == "1",
			EmailConfig: form.Get("email_config") == "1",
			Cron:        form.Get("cron") == "1",
			DNS:         form.Get("dns") == "1",
		}, true
	}
	c, ok := scopePresets[preset]
	if !ok {
		return workbench.ContentSelection{}, false
	}
	// DNS may be added on top of any preset as a manual/verifiable task.
	if form.Get("dns") == "1" {
		c.DNS = true
	}
	return c, true
}

// hasAutomaticArea reports whether the selection includes at least one area the
// one-click orchestrator could run automatically. DNS does NOT count — a
// DNS-only selection is not an automatic migration.
func hasAutomaticArea(c workbench.ContentSelection) bool {
	return c.Files || c.Databases || c.Email || c.EmailConfig || c.Cron
}

// canEditScope reports whether the migration scope may still be changed. It is
// frozen once a migration write has run (a content or per-area apply report
// exists) or while a job is live — so a confirmed scope never diverges from what
// was actually applied.
func canEditScope(f artifactFacts, jobLive bool) bool {
	if jobLive {
		return false
	}
	if f.ContentApplyPresent {
		return false
	}
	if f.DNS.ApplyPresent || f.Email.ApplyPresent || f.Cron.ApplyPresent {
		return false
	}
	return true
}

// scopeFlash maps the ?scope= query value to a one-shot human message.
func scopeFlash(v string) string {
	switch v {
	case "updated":
		return "Scope aggiornato."
	case "need_area":
		return "Seleziona almeno un'area automatica (file, database, email o cron) da migrare."
	default:
		return ""
	}
}

// jobLiveNow reports whether an exec is genuinely in flight (journal running
// after reconciliation against the live slot) — same signal the view uses.
func (ws *workbenchServer) jobLiveNow() bool {
	busy := false
	if ws.jobBusy != nil {
		busy = ws.jobBusy()
	}
	job := reconcileJobJournal(ws.dir, busy)
	return job != nil && job.State == jobStateRunning
}

// handleConfirmScope updates the migration content selection after the preflight
// and marks the scope confirmed. CSRF is enforced by the caller (server.post).
func (ws *workbenchServer) handleConfirmScope(w http.ResponseWriter, r *http.Request, sessionID string) {
	if _, err := ws.store.Get(sessionID); err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	// Edit gate: frozen once a write has run or a job is live. Note: this reads
	// the facts outside the store flock, so a narrow TOCTOU exists if a write
	// started between here and ConfirmScope. Acceptable for this single-operator,
	// single-session tool — the real write gate remains the strong per-account
	// confirmation, and a stale scope confirmation is a metadata-only race.
	f := readArtifactFacts(ws.dir)
	if !canEditScope(f, ws.jobLiveNow()) {
		http.Error(w, "Lo scope non è più modificabile: una migrazione è già stata avviata o è in corso.", http.StatusForbidden)
		return
	}

	content, ok := presetContent(r.FormValue("preset"), r.Form)
	if !ok {
		http.Error(w, "preset non valido", http.StatusBadRequest)
		return
	}

	dest := "/workbench/session/" + sessionID + "/" + screenMigrazione
	// A confirmed scope must contain at least one automatic area — DNS-only is
	// not an automatic migration. Bounce back with a human flash, no mutation.
	if !hasAutomaticArea(content) {
		http.Redirect(w, r, dest+"?scope=need_area", http.StatusSeeOther)
		return
	}

	if _, err := ws.store.ConfirmScope(sessionID, content, time.Now().UTC()); err != nil {
		http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, dest+"?scope=updated", http.StatusSeeOther)
}
