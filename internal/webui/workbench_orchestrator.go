package webui

// Fase 3 — Smart Migration Orchestrator.
//
// One strong confirmation starts, in sequence, every area that is AUTOMATIC,
// SAFE and IN-SCOPE, then stops at the first real failure. It does NOT add any
// new writer, CLI subcommand or artifact: it composes the SAME write/verify
// invocations the individual /exec actions already run (via the shared argv
// builders in workbench_exec.go) behind the SAME single-writer slot, job
// journal and confirmation gate.
//
// Non-negotiable invariants (roadmap §9, §14):
//   - ONE strong confirmation, not one per phase (validateStrongConfirmation);
//   - the scope must be confirmed (Fase 2) and a Setup must exist — a legacy
//     session is never auto-startable;
//   - the whole plan is RECOMPUTED server-side (artifactFacts + contentScope +
//     migrationPlan): the UI's saved scope is never trusted blindly;
//   - contentScope is finally a REAL server-side gate here — the orchestrator
//     never passes --file/--db/--mail for an excluded area and never runs
//     email_apply/cron_apply for an excluded one;
//   - the checklist apply-gate (isApplyBlockedByChecklist) is re-checked BEFORE
//     every write phase, not just once at the start;
//   - DNS is NEVER in the auto-run, even when IncludeDNS is true;
//   - verify runs after every phase that has a real verifier (email/cron), with
//     --fail-on-drift so a post-apply drift stops the run; migrate_content has
//     no clean verifier, so its phase is "completed with report", never a faked
//     clean verify;
//   - stop-on-first-failure: a failed write OR a failed verify stops the run,
//     the remaining phases are recorded as not-run, and NO rollback is attempted.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/version"
)

// orchestratorAction is the operator-facing name of the smart-migration job. It
// appears in the job journal and in busyMessage (« … già in corso …»).
const orchestratorAction = "migrazione automatica"

// migrationPhaseState is the outcome of one orchestrator phase.
type migrationPhaseState string

const (
	phaseNotRun              migrationPhaseState = "not_run"
	phaseCompleted           migrationPhaseState = "completed"             // applied + verified clean
	phaseCompletedWithReport migrationPhaseState = "completed_with_report" // applied, no clean verifier (content)
	phaseFailed              migrationPhaseState = "failed"
)

// orchestratorStep is one runner invocation (write or verify) with the artifact
// to attach on success.
type orchestratorStep struct {
	name     string
	argv     []string
	artifact *artifactOutput
}

// orchestratorPhase is one auto-run area: a write step and an optional verifier.
// reportOnly marks a phase (content) that has no reliable clean verifier — it is
// "completed with report", never a faked clean verify.
type orchestratorPhase struct {
	key        string
	label      string
	write      orchestratorStep
	verify     *orchestratorStep
	reportOnly bool
}

// phaseOutcome is the recorded result of one phase for the timeline/UI.
type phaseOutcome struct {
	Key    string
	Label  string
	State  migrationPhaseState
	Detail string
}

// orchStopKind classifies WHY a run stopped, so the redirect code is derived
// from a typed field set explicitly at each stop site — never by sniffing the
// Italian human copy (which is free to change without breaking control flow).
type orchStopKind int

const (
	stopNone    orchStopKind = iota // completed all phases
	stopGate                        // checklist turned blocking mid-run
	stopFailure                     // a write or verify phase failed
)

// orchestrationResult is the aggregate outcome of a run.
type orchestrationResult struct {
	Outcomes   []phaseOutcome
	Stopped    bool         // a phase failed or the gate closed mid-run
	StopKind   orchStopKind // typed stop classification (drives the redirect code)
	StopReason string       // human copy for the stop
	Err        error        // non-nil when stopped (drives the journal terminal state)
}

// buildOrchestratorPhases derives the auto-run phases server-side from the
// recomputed scope + facts. This is the SAME classification the read-model
// migrationPlan uses (files/db/email are always automatic when in scope;
// email-config/cron are automatic ONLY when their plan already exists), applied
// to the executable steps. DNS is deliberately absent — never auto-run.
func buildOrchestratorPhases(dir string, f artifactFacts, scope contentScope) []orchestratorPhase {
	var phases []orchestratorPhase

	// Content: a single migrate_content phase covering the in-scope content areas.
	// contentScope is the real gate here — an excluded area contributes no flag.
	if scope.IncludeFiles || scope.IncludeDatabases || scope.IncludeEmailContent {
		phases = append(phases, orchestratorPhase{
			key:   "content",
			label: "Contenuti",
			write: orchestratorStep{
				name:     actionRegistry["migrate_content"].name,
				argv:     migrateContentArgv(dir, scope.IncludeEmailContent, scope.IncludeFiles, scope.IncludeDatabases, ""),
				artifact: actionRegistry["migrate_content"].artifact,
			},
			// migrate_content has no standalone clean verifier: use its own report,
			// don't invent a fake "verify clean" (roadmap §9, prompt id=3z8ucp).
			reportOnly: true,
		})
	}

	// Email config: automatic ONLY when the email plan exists (safe/automatic
	// classification not faked otherwise) AND the area is in scope.
	if scope.IncludeEmailConfig && f.Email.PlanPresent {
		phases = append(phases, orchestratorPhase{
			key:   "email_config",
			label: "Configurazioni email",
			write: orchestratorStep{
				name:     actionRegistry["email_apply"].name,
				argv:     emailApplyArgv(dir),
				artifact: actionRegistry["email_apply"].artifact,
			},
			verify: &orchestratorStep{
				name:     actionRegistry["email_verify"].name,
				argv:     emailVerifyArgv(dir, true), // gate on drift
				artifact: actionRegistry["email_verify"].artifact,
			},
		})
	}

	// Cron: same posture — automatic only when the cron plan exists.
	if scope.IncludeCron && f.Cron.PlanPresent {
		phases = append(phases, orchestratorPhase{
			key:   "cron",
			label: "Cron",
			write: orchestratorStep{
				name:     actionRegistry["cron_apply"].name,
				argv:     cronApplyArgv(dir),
				artifact: actionRegistry["cron_apply"].artifact,
			},
			verify: &orchestratorStep{
				name:     actionRegistry["cron_verify"].name,
				argv:     cronVerifyArgv(dir, true),
				artifact: actionRegistry["cron_verify"].artifact,
			},
		})
	}

	return phases
}

// Copy for the partial-state stops (roadmap §9 / prompt ids h0c5k6, mh0fa7).
const (
	orchestratorGateStoppedMsg = "La migrazione è stata interrotta perché la verifica migrazione ora segnala problemi bloccanti. Le fasi già completate restano registrate nel report."
	orchestratorNotRunDetail   = "Non eseguita: la migrazione si è fermata prima di questa fase."
)

// runOrchestration executes the phases in sequence with a per-phase gate
// re-check, stop-on-first-failure, and best-effort artifact attach + journal
// phase updates. It never rolls back. base is the parent context; EACH step
// gets its OWN execTimeout clock (mirroring the single-action /exec semantics),
// so a long content phase can never eat the whole run's budget and cause a
// later write to be killed mid-flight by a shared, artificial deadline.
func (ws *workbenchExecServer) runOrchestration(base context.Context, sessionID string, phases []orchestratorPhase, startedAt time.Time) orchestrationResult {
	return ws.runOrchestrationWithGate(base, sessionID, phases, startedAt, isApplyBlockedByChecklist)
}

func (ws *workbenchExecServer) runOrchestrationWithGate(base context.Context, sessionID string, phases []orchestratorPhase, startedAt time.Time, gateClosed func(string) bool) orchestrationResult {
	res := orchestrationResult{Outcomes: make([]phaseOutcome, len(phases))}
	for i, ph := range phases {
		res.Outcomes[i] = phaseOutcome{Key: ph.key, Label: ph.label, State: phaseNotRun, Detail: orchestratorNotRunDetail}
	}
	tail := &tailBuffer{limit: execTailLimit}

	for i, ph := range phases {
		// Gate re-check BEFORE every write phase (roadmap §14.3): a checklist that
		// turned blocking mid-run stops the orchestrator immediately.
		if gateClosed != nil && gateClosed(ws.dir) {
			res.Stopped = true
			res.StopKind = stopGate
			res.StopReason = orchestratorGateStoppedMsg
			res.Err = errors.New("apply gate closed during orchestration")
			return res
		}
		ws.journalPhaseRunning(sessionID, ph.label, startedAt)

		// Write step (own fresh timeout).
		if err := ws.runStep(base, tail, ph.write); err != nil {
			res.Outcomes[i] = phaseOutcome{ph.key, ph.label, phaseFailed,
				"Errore durante l'applicazione: " + err.Error()}
			res.Stopped = true
			res.StopKind = stopFailure
			res.StopReason = fmt.Sprintf("Migrazione interrotta durante «%s». Le fasi precedenti sono state completate. Nessun rollback automatico è stato eseguito.", ph.label)
			res.Err = err
			return res
		}
		ws.attachOrchestratorArtifact(sessionID, ph.write.artifact)

		// Verify step (email/cron): --fail-on-drift makes a dirty verify exit
		// non-zero, so a drift stops the run exactly like an apply failure.
		if ph.verify != nil {
			if err := ws.runStep(base, tail, *ph.verify); err != nil {
				res.Outcomes[i] = phaseOutcome{ph.key, ph.label, phaseFailed,
					"Verifica non superata dopo l'applicazione: " + err.Error()}
				res.Stopped = true
				res.StopKind = stopFailure
				res.StopReason = fmt.Sprintf("Migrazione interrotta: la verifica di «%s» non è stata superata. Le fasi precedenti restano registrate. Nessun rollback automatico è stato eseguito.", ph.label)
				res.Err = err
				return res
			}
			ws.attachOrchestratorArtifact(sessionID, ph.verify.artifact)
			res.Outcomes[i] = phaseOutcome{ph.key, ph.label, phaseCompleted, "Applicata e verificata."}
			continue
		}
		res.Outcomes[i] = phaseOutcome{ph.key, ph.label, phaseCompletedWithReport, "Applicata (report disponibile)."}
	}
	return res
}

func isSmartStartHardBlockedByChecklist(dir string) bool {
	f := readArtifactFacts(dir)
	return f.Checklist != nil && f.Checklist.ApplyBlocked
}

func hasAutoRunnableSelection(scope contentScope) bool {
	return scope.IncludeFiles || scope.IncludeDatabases || scope.IncludeEmailContent || scope.IncludeEmailConfig || scope.IncludeCron
}

func (ws *workbenchExecServer) runPreflightInline(base context.Context, sessionID string, startedAt time.Time) error {
	tail := &tailBuffer{limit: execTailLimit}
	ctx, cancel := context.WithTimeout(base, jobTimeout)
	defer cancel()
	for _, st := range pipelineSteps(ws.dir) {
		ws.journalPhaseRunning(sessionID, "Controllo iniziale: "+st.Name, startedAt)
		err := ws.runner(ctx, tail, st.Name, st.Argv)
		if err != nil && !st.Tolerant {
			return fmt.Errorf("%s: %w", st.Name, err)
		}
	}
	return nil
}

// runStep runs ONE orchestrator step with its OWN execTimeout, identical to the
// per-click budget of the single /exec action it mirrors.
func (ws *workbenchExecServer) runStep(base context.Context, tail *tailBuffer, st orchestratorStep) error {
	ctx, cancel := context.WithTimeout(base, execTimeout)
	defer cancel()
	return ws.runner(ctx, tail, st.name, st.argv)
}

// attachOrchestratorArtifact attaches a produced artifact best-effort: a MISSING
// file is silently skipped (the artifact facts are re-derived from disk anyway),
// but a genuine store error (disk full, permission, corruption) is logged so it
// is not completely invisible in the server logs. It never fails a phase that
// actually ran. Nil art = nothing to attach.
func (ws *workbenchExecServer) attachOrchestratorArtifact(sessionID string, art *artifactOutput) {
	if art == nil {
		return
	}
	p := filepath.Join(ws.dir, art.filename)
	if _, err := os.Stat(p); err != nil {
		return // no artifact produced (e.g. a step that writes nothing) — nothing to attach
	}
	if _, err := ws.store.AttachArtifact(sessionID, art.kind, p, time.Now().UTC()); err != nil {
		fmt.Fprintf(os.Stderr, "webui: orchestrator artifact attach %s failed: %v\n", art.filename, err)
	}
}

// journalPhaseRunning updates the job journal with the current phase so a
// refresh shows "migrazione automatica — fase «…»". Best-effort (observability).
func (ws *workbenchExecServer) journalPhaseRunning(sessionID, phase string, startedAt time.Time) {
	_ = writeJobJournal(ws.dir, jobJournal{
		SessionID:   sessionID,
		Action:      orchestratorAction,
		StartedAt:   startedAt,
		UpdatedAt:   time.Now().UTC(),
		State:       jobStateRunning,
		Phase:       phase,
		ToolVersion: version.String(),
	})
}

// handleStartMigration is POST /workbench/session/<id>/start-migration. CSRF is
// enforced by the caller (server.post). It validates the preconditions, ONE
// strong confirmation, recomputes the plan server-side, reserves the shared
// single-writer slot, runs the phases, records the outcome and redirects to the
// migration screen with a human flash.
// handleStartMigration runs the one-click orchestrator behind a single strong
// confirmation. destBase is the URL to redirect to (without query): the
// workbench passes its Piano screen, the platform passes its cockpit — every
// ?migrate= outcome lands on the caller's own surface. All gates, the strong
// confirmation, the single-writer slot and the server-side recompute are
// IDENTICAL regardless of destBase (presentation-only difference).
func (ws *workbenchExecServer) handleStartMigration(w http.ResponseWriter, r *http.Request, sessionID, destBase string) {
	sess, err := ws.store.Get(sessionID)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	dest := destBase

	// A legacy/advanced session (no wizard Setup) is never auto-startable: the
	// orchestrator needs a confirmed scope, which lives on Setup.
	if sess.Setup == nil {
		http.Redirect(w, r, dest+"?migrate=needs_setup", http.StatusSeeOther)
		return
	}
	// Scope must be confirmed after the preflight (Fase 2) before an auto-run.
	if sess.Setup.ScopeConfirmedAt == nil {
		http.Redirect(w, r, dest+"?migrate=scope_unconfirmed", http.StatusSeeOther)
		return
	}
	// ONE strong confirmation for the whole migration (not one per phase).
	if err := validateStrongConfirmation(r, sess); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	// Recompute EVERYTHING server-side: never trust the saved scope alone.
	f := readArtifactFacts(ws.dir)
	scope := deriveContentScope(sess)
	plan := buildMigrationPlan(f, scope)

	// Same oracle as the read-model CTA: not startable → explain why (blocked vs
	// nothing-automatic), no mutation, no slot held.
	if !plan.CanStartMigration {
		code := "blocked"
		if plan.Ready && !plan.Blocked {
			code = "no_auto"
		}
		http.Redirect(w, r, dest+"?migrate="+code, http.StatusSeeOther)
		return
	}

	phases := buildOrchestratorPhases(ws.dir, f, scope)
	if len(phases) == 0 {
		// Defensive: CanStartMigration already implies at least one automatic area.
		http.Redirect(w, r, dest+"?migrate=no_auto", http.StatusSeeOther)
		return
	}

	// Reserve the shared single-writer slot (mutually exclusive with /run,
	// /accept and /exec). A busy slot is a readable 409, not opaque.
	if !ws.job.tryReserve() {
		writeBusy409(w, ws.dir, ws.job)
		return
	}
	startedAt := time.Now().UTC()
	startJobJournal(ws.dir, sessionID, orchestratorAction, startedAt)
	var runErr error
	stopPhase := orchestratorAction
	defer func() {
		// Without this recover a panic here would run the defer with runErr==nil,
		// closing the journal as "completed" — a dishonest terminal state for a
		// run that actually died. Recover it into a failed journal and a plain
		// 500, consistent with the async smart-start path. (net/http recovers the
		// panic per-connection anyway; we just make the journal tell the truth.)
		rec := recover()
		if rec != nil && runErr == nil {
			runErr = fmt.Errorf("errore interno durante la migrazione: %v", rec)
		}
		finishJobJournal(ws.dir, sessionID, orchestratorAction, stopPhase, startedAt, time.Now().UTC(), runErr, "")
		ws.job.release()
		if rec != nil {
			http.Error(w, "errore interno durante la migrazione", http.StatusInternalServerError)
		}
	}()

	// Each phase step gets its own execTimeout inside runOrchestration (mirroring
	// the single /exec action), so no shared deadline can kill a later write.
	result := ws.runOrchestration(ws.base, sessionID, phases, startedAt)
	runErr = result.Err
	if p := lastRunningPhase(result.Outcomes); p != "" {
		stopPhase = p
	}

	// Record ONE forced same-status timeline event summarising the phases (mirror
	// of handleExec's timeline write). Re-read to avoid a TOCTOU clobber.
	freshSess, getErr := ws.store.Get(sessionID)
	if getErr == nil {
		reason := "avvio migrazione: " + summarizeOutcomes(result.Outcomes)
		if result.Stopped {
			reason += " [interrotta]"
		}
		_, _ = ws.store.SetStatus(sessionID, freshSess.Status, true, reason, time.Now().UTC())
	}

	http.Redirect(w, r, dest+"?migrate="+resultCode(result, scope), http.StatusSeeOther)
}

func (ws *workbenchExecServer) handleSmartStartMigration(w http.ResponseWriter, r *http.Request, sessionID, destBase string) {
	sess, err := ws.store.Get(sessionID)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	dest := destBase
	if sess.Setup == nil {
		http.Redirect(w, r, dest+"?migrate=needs_setup", http.StatusSeeOther)
		return
	}
	if r.FormValue("confirm_start") != "1" {
		http.Error(w, "missing start confirmation", http.StatusForbidden)
		return
	}
	if _, err := os.Stat(filepath.Join(ws.dir, "host.yaml")); err != nil {
		http.Redirect(w, r, dest+"?migrate=config_missing", http.StatusSeeOther)
		return
	}
	scope := deriveContentScope(sess)
	if !hasAutoRunnableSelection(scope) {
		http.Redirect(w, r, dest+"?migrate=no_auto", http.StatusSeeOther)
		return
	}
	// Reserve the shared single-writer slot SYNCHRONOUSLY: a double submit is a
	// readable 409 before this handler returns. The slot is released by the
	// background goroutine below (NOT this handler), so it stays held for the
	// whole async run — mutually exclusive with /run, /accept, /exec as before.
	if !ws.job.tryReserve() {
		writeBusy409(w, ws.dir, ws.job)
		return
	}
	startedAt := time.Now().UTC()
	startJobJournal(ws.dir, sessionID, orchestratorAction, startedAt)

	// The migration runs in the BACKGROUND so the operator is redirected to the
	// cockpit immediately and follows the live progress there (meta-refresh, and
	// the SSE stream once mounted) instead of the browser hanging on this POST
	// for up to jobTimeout. The run uses ws.base (request-independent, already the
	// case before this refactor) so it survives the handler returning. A panic in
	// this request-independent goroutine would otherwise take down the whole ui
	// process (net/http only recovers per-connection), so it is recovered into a
	// failed journal — exactly like jobManager.execute.
	go func() {
		var runErr error
		outcome := ""
		stopPhase := orchestratorAction
		defer func() {
			if rec := recover(); rec != nil {
				runErr = fmt.Errorf("errore interno durante la migrazione: %v", rec)
				outcome = "Migrazione interrotta da un errore interno. Nessun rollback automatico è stato eseguito."
			}
			finishJobJournal(ws.dir, sessionID, orchestratorAction, stopPhase, startedAt, time.Now().UTC(), runErr, outcome)
			ws.job.release()
		}()

		// Re-read the session inside the goroutine (do not share the handler's
		// value across the goroutine boundary): the store is the concurrency
		// authority the rest of the ui already relies on.
		gsess, gerr := ws.store.Get(sessionID)
		if gerr != nil {
			runErr = gerr
			return
		}
		gscope := deriveContentScope(gsess)

		f := readArtifactFacts(ws.dir)
		if f.Checklist == nil || f.Checklist.OverallStatus == "NOT_READY" {
			if err := ws.runPreflightInline(ws.base, sessionID, startedAt); err != nil {
				runErr = err
				outcome = migrateFlash("preflight_failed")
				return
			}
		}
		if isSmartStartHardBlockedByChecklist(ws.dir) {
			runErr = errors.New("smart start hard block")
			outcome = migrateFlash("blocked")
			return
		}

		f = readArtifactFacts(ws.dir)
		phases := buildOrchestratorPhases(ws.dir, f, gscope)
		if len(phases) == 0 {
			outcome = migrateFlash("no_auto")
			return
		}

		result := ws.runOrchestrationWithGate(ws.base, sessionID, phases, startedAt, isSmartStartHardBlockedByChecklist)
		runErr = result.Err
		outcome = migrateFlash(resultCode(result, gscope))
		if p := lastRunningPhase(result.Outcomes); p != "" {
			stopPhase = p
		}
		if freshSess, getErr := ws.store.Get(sessionID); getErr == nil {
			reason := "avvio smart: " + summarizeOutcomes(result.Outcomes)
			if result.Stopped {
				reason += " [interrotta]"
			}
			_, _ = ws.store.SetStatus(sessionID, freshSess.Status, true, reason, time.Now().UTC())
		}
	}()

	http.Redirect(w, r, dest+"?migrate=started", http.StatusSeeOther)
}

// lastRunningPhase returns the label of the phase that failed (or the last one
// that ran), for the journal's terminal phase field. Empty when none ran.
func lastRunningPhase(outs []phaseOutcome) string {
	last := ""
	for _, o := range outs {
		if o.State == phaseFailed {
			return o.Label
		}
		if o.State != phaseNotRun {
			last = o.Label
		}
	}
	return last
}

// summarizeOutcomes renders "content=completed_with_report, cron=not_run" for
// the timeline reason (machine-ish but auditable).
func summarizeOutcomes(outs []phaseOutcome) string {
	parts := make([]string, 0, len(outs))
	for _, o := range outs {
		parts = append(parts, o.Key+"="+string(o.State))
	}
	return strings.Join(parts, ", ")
}

// resultCode maps the run to a migrate-flash code from the TYPED stop kind (not
// from the human copy). A clean full run with DNS (or other manual tasks) opted
// in is "done_manual", otherwise "done".
func resultCode(res orchestrationResult, scope contentScope) string {
	switch res.StopKind {
	case stopGate:
		return "gate_stopped"
	case stopFailure:
		return "partial"
	}
	if scope.IncludeDNS {
		return "done_manual"
	}
	return "done"
}

// migrateFlash maps a ?migrate= code to a one-shot human message shown on the
// migration screen. Platform language: no argv/artifact/apply_blocked leaks.
func migrateFlash(code string) string {
	switch code {
	case "started":
		return "Migrazione avviata: l'avanzamento è mostrato qui sotto e si aggiorna in tempo reale."
	case "needs_setup":
		return "Questa sessione non ha una configurazione guidata: non può avviare la migrazione automatica."
	case "scope_unconfirmed":
		return "Conferma lo scope prima di avviare la migrazione."
	case "blocked":
		return "Migrazione non avviata: la verifica migrazione segnala problemi bloccanti. Risolvili e riesegui il preflight."
	case "config_missing":
		return "Completa le credenziali sorgente e destinazione prima di avviare la migrazione."
	case "preflight_failed":
		return "Controllo iniziale non completato: la migrazione non è partita perché mancano informazioni tecniche indispensabili."
	case "no_auto":
		return "Nessuna area automatica da avviare: le aree incluse (es. DNS) sono gestite come task manuali verificabili."
	case "done":
		return "Migrazione automatica completata."
	case "done_manual":
		return "Migrazione automatica completata. Restano task manuali aperti (es. DNS): il DNS non viene applicato automaticamente."
	case "partial":
		return "Migrazione interrotta al primo errore. Le fasi già completate restano registrate. Nessun rollback automatico è stato eseguito."
	case "gate_stopped":
		return orchestratorGateStoppedMsg
	default:
		return ""
	}
}
