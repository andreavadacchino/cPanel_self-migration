package webui

// Flight Director shell — presentation-only view-model helpers (PR "flight
// director shell"). This file adds an at-a-glance RISK BADGE and a phase
// TIMELINE derived entirely from facts the engine already produces
// (governance status, on-disk artifacts, the job journal, the wizard scope).
//
// It introduces NO operational logic: it never writes, never connects, never
// gates the exec handler. Like workbench_view.go it is a pure translation of
// on-disk facts into honest Italian UI data — an unreliable signal is reported
// as such, never dressed up as certainty (no fake "green").

import (
	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// riskBadge is the single most important honest signal about where the session
// stands, shown in the persistent Flight Director header. Class is a badge
// modifier already styled by the theme (draft|active|done|error|warn).
type riskBadge struct {
	Label string
	Class string
}

// buildRiskBadge picks ONE badge by descending urgency. The order is
// deliberate: an in-flight/interrupted job is the most immediate operator
// concern, then hard governance failures, then a checklist blocker, then a
// wizard session still missing its credentials, and only then the calm resting
// states. It never returns a "done"/green badge unless the evidence supports it.
func buildRiskBadge(status workbench.Status, f artifactFacts, scope contentScope, job *jobJournal, jobLive bool) riskBadge {
	// 1. Live / near-live job signals — UNCONDITIONAL top priority. An exec is
	// reachable even on a terminal session (the exec path has no status gate), and
	// losing sight of a running/interrupted job is exactly what the job journal
	// (#70) exists to prevent, so it must win even over a terminal status.
	if jobLive {
		return riskBadge{"Job in corso", "active"}
	}
	if job != nil {
		switch job.State {
		case jobStateInterrupted:
			return riskBadge{"Job interrotto", "error"}
		case jobStateFailed:
			return riskBadge{"Ultimo job fallito", "error"}
		}
	}
	// 2. Terminal, closed-out states win over any stale artifact still on disk:
	// a finished/archived session must never be re-flagged "Bloccante" by an
	// intermediate checklist that was never regenerated after cutover.
	switch status {
	case workbench.StatusCutoverDone:
		return riskBadge{"Cutover completato", "done"}
	case workbench.StatusArchived:
		return riskBadge{"Archiviata", "draft"}
	}
	// 3. Hard governance failures.
	switch status {
	case workbench.StatusBlocked, workbench.StatusFailed:
		return riskBadge{"Attenzione", "error"}
	}
	// 4. Checklist blocker — distinguish migration-blocking from cutover-only
	// blocking (dogfooding #4 §6.2). A checklist can be OverallBlocked for the
	// CUTOVER while the migration itself is legitimately startable
	// (ApplyBlocked=false): the header must not shout an error-red "Bloccante"
	// next to an active "Avvia migrazione". Migration-blocking uses the same
	// oracle as nextAction/applyBlocked and stays red; cutover-only blocking is a
	// milder "Bloccante cutover" warning.
	if f.Checklist != nil {
		applyBlocked := f.Checklist.ApplyBlocked ||
			f.Checklist.OverallStatus == accountinventory.OverallNotReady
		switch {
		case applyBlocked:
			return riskBadge{"Bloccante migrazione", "error"}
		case f.Checklist.OverallStatus == accountinventory.OverallBlocked:
			return riskBadge{"Bloccante cutover", "warn"}
		}
	}
	// 5. A wizard session whose credentials (host.yaml) are not set yet.
	if scope.HasSetup && !f.HostYAMLPresent {
		return riskBadge{"Configurazione richiesta", "warn"}
	}
	// 6. Calm resting states.
	switch status {
	case workbench.StatusReadyForCutover:
		return riskBadge{"Pronto per il cutover", "done"}
	case workbench.StatusDraft, workbench.StatusPreflightRequired:
		return riskBadge{"Da configurare", "draft"}
	}
	// 7. Default: mid-flight, no red flag.
	return riskBadge{"In corso", "active"}
}

// timelineStep is one phase in the left rail: an Italian label, the real screen
// route it links to, a synthetic State (done|doing|todo|warn) and whether it is
// the phase currently on screen.
type timelineStep struct {
	Label   string
	Screen  string
	State   string
	Current bool
}

// buildTimeline derives the seven-phase rail from existing facts. Each phase
// maps to a REAL screen route (no invented routes) and its state is read off
// concrete signals — artifact presence, governance status, the job journal —
// so the rail can never claim progress the artifacts do not show.
func buildTimeline(screen string, status workbench.Status, f artifactFacts, scope contentScope, job *jobJournal, jobLive bool) []timelineStep {
	hasChecklist := f.Checklist != nil
	applyBlocked := hasChecklist && (f.Checklist.ApplyBlocked || f.Checklist.OverallStatus == accountinventory.OverallNotReady)
	pending := len(pendingConfirmations(f))

	// Setup: done once credentials exist; a wizard session still missing them is
	// flagged so the operator sees the gap on the rail, not just in the header.
	setupState := "todo"
	switch {
	case f.HostYAMLPresent:
		setupState = "done"
	case scope.HasSetup:
		setupState = "warn"
	}

	inventoryReady := f.InventorySourcePresent && f.InventoryDestPresent

	preState := "todo"
	if inventoryReady {
		preState = "done"
	}

	// Fotografia account is "done" once the checklist (its counts) exists; if the
	// inventory is collected but the checklist is not generated yet it is "In
	// corso", so the rail never contradicts the "Stato per fase → Inventario"
	// widget that keys on the same inventory presence.
	invState := "todo"
	switch {
	case hasChecklist:
		invState = "done"
	case inventoryReady:
		invState = "doing"
	}

	// A successful cutover retires any stale intermediate blocker/pending: the
	// migration is done, so these phases are done regardless of an old checklist.
	migState := "todo"
	switch {
	case status == workbench.StatusCutoverDone:
		migState = "done"
	case applyBlocked:
		migState = "warn"
	case hasChecklist:
		migState = "done"
	}

	confState := "todo"
	switch {
	case status == workbench.StatusCutoverDone:
		confState = "done"
	case !hasChecklist:
		confState = "todo"
	case pending > 0:
		confState = "warn"
	default:
		confState = "done"
	}

	appState := applyPhaseState(status, f, scope, job, jobLive)

	// Chiusura is "done" once the session is closed — a completed cutover or an
	// archived (read-only) session — and "In corso" when it is ready but not yet
	// closed.
	closeState := "todo"
	switch status {
	case workbench.StatusCutoverDone, workbench.StatusArchived:
		closeState = "done"
	case workbench.StatusReadyForCutover:
		closeState = "doing"
	}

	steps := []timelineStep{
		{"Panoramica", screenPanoramica, setupState, false},
		{"Preflight", screenPreflight, preState, false},
		{"Fotografia account", screenInventario, invState, false},
		{"Cosa verrà migrato", screenMigrazione, migState, false},
		{"Conferme operatore", screenConferme, confState, false},
		{"Applica e verifica", screenApplica, appState, false},
		{"Chiusura", screenChiusura, closeState, false},
	}
	for i := range steps {
		if steps[i].Screen == screen {
			steps[i].Current = true
		}
	}
	return steps
}

// applyPhaseState summarises the Apply/verify phase. A running exec is "doing";
// an interrupted one is "warn"; a done/verified state is "done" only when every
// in-scope verify is clean (reusing missingVerifies so the rail agrees with the
// banner).
func applyPhaseState(status workbench.Status, f artifactFacts, scope contentScope, job *jobJournal, jobLive bool) string {
	if jobLive {
		return "doing"
	}
	if job != nil && job.State == jobStateInterrupted {
		return "warn"
	}
	switch status {
	case workbench.StatusApplyInProgress:
		return "doing"
	case workbench.StatusApplyDone, workbench.StatusVerificationRequired:
		if len(missingVerifies(f, scope)) == 0 {
			return "done"
		}
		return "doing"
	case workbench.StatusReadyForCutover, workbench.StatusCutoverDone:
		return "done"
	}
	return "todo"
}
