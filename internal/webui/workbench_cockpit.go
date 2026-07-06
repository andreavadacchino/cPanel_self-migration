package webui

// Modern Migration Cockpit (Fase 4) — presentation-only read-model.
//
// This file aggregates facts the engine ALREADY produced (the migration
// checklist, the migration plan, the job journal, the events.jsonl run
// monitor) into a single cockpit view. It introduces NO writer, NO CLI, NO new
// artifact and — the hard rule — invents NO data: every value degrades to an
// explicit "not available" when its source is missing, never a fake number and
// never a fake green. Like workbench_flightdirector.go it never writes, never
// connects and never gates the exec handler.

import (
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// cockpitModel is the aggregated cockpit shown on the session landing
// (Panoramica). Every field is derived, read-only, and safe to render even when
// the underlying artifacts are absent.
type cockpitModel struct {
	StateLabel string // operator-facing "where we are" (e.g. "Pronta per migrare")
	StateClass string // badge modifier: draft|active|done|error|warn
	CTA        cockpitCTA
	Steps      []cockpitStep
	Comparison []cockpitCompareRow
	HasCompare bool
	Plan       cockpitPlanBuckets
	Monitor    cockpitMonitor
	// Blocker taxonomy made explicit (dogfooding #4 §6.2): migration-blocking
	// (stops the apply) vs cutover-blocking (does not stop the migration).
	MigrationBlockers []migrationPlanIssue
	MigrationBlocked  bool
	CutoverBlockers   []string
	PendingCutover    []accountinventory.ManualAction
}

// cockpitCTA is the single dominant call-to-action of the cockpit hero. Kind
// selects how the template renders it:
//   - "start": render the strong-confirmation start-migration form inline;
//   - "link":  a primary button linking to Screen (a workbench route);
//   - "wait":  a passive status (a job is in flight, nothing to click).
type cockpitCTA struct {
	Label  string
	Detail string
	Kind   string
	Screen string // workbench screen segment for Kind=="link"
}

// cockpitStep is one step of the modern horizontal stepper. It reuses the
// seven-phase Flight Director timeline (same routes, same synthetic states) so
// the cockpit and the left rail can never disagree; only the label is friendlier
// and a human caption is added.
type cockpitStep struct {
	Label   string
	Screen  string
	State   string // done | doing | warn | todo (timeline vocabulary)
	Current bool
	Caption string
}

// cockpitStepLabels give the horizontal stepper friendlier labels than the rail
// while keeping the SAME underlying routes/states — no new routes, no new
// taxonomy the rail does not already back.
var cockpitStepLabels = map[string]string{
	screenPanoramica: "Setup",
	screenPreflight:  "Preflight",
	screenInventario: "Analisi",
	screenMigrazione: "Piano & scope",
	screenConferme:   "Conferme",
	screenApplica:    "Migrazione",
	screenChiusura:   "Chiusura",
}

func buildCockpitSteps(timeline []timelineStep) []cockpitStep {
	out := make([]cockpitStep, 0, len(timeline))
	for _, t := range timeline {
		label := cockpitStepLabels[t.Screen]
		if label == "" {
			label = t.Label
		}
		out = append(out, cockpitStep{
			Label:   label,
			Screen:  t.Screen,
			State:   t.State,
			Current: t.Current,
			Caption: stepCaption(t.State, t.Current),
		})
	}
	return out
}

// stepCaption is the per-step human microcopy shown under the stepper label.
func stepCaption(state string, current bool) string {
	switch state {
	case "done":
		return "completato"
	case "doing":
		return "in corso"
	case "warn":
		return "richiede attenzione"
	default:
		if current {
			return "sei qui"
		}
		return "da fare"
	}
}

// ---- Comparativa sorgente ↔ destinazione ------------------------------------

// cockpitCompareRow is one area of the source↔destination comparison. Counts
// come ONLY from the migration checklist sections the webui already parses;
// areas with no honest single count (web files are not counted; email
// configuration spans several sections) show HasCount=false ("—"), never a
// guess.
type cockpitCompareRow struct {
	Key        string
	Label      string
	HasCount   bool
	Source     int
	Dest       int
	State      string
	StateClass string
	Action     string
}

// compareCountSection maps a plan area to the checklist section whose
// source/destination counts describe it. Semantics are honest: "mailboxes" is a
// count of mailboxes (NOT email messages), "databases" a count of databases
// (NOT tables), "dns" a count of zones, "cron" a count of jobs.
var compareCountSection = map[string]string{
	"databases": "databases",
	"email":     "mailboxes",
	"cron":      "cron",
	"dns":       "dns",
}

func buildCockpitComparison(f artifactFacts, plan migrationPlan) []cockpitCompareRow {
	if f.Checklist == nil {
		return nil
	}
	sections := make(map[string]accountinventory.ChecklistSection, len(f.Checklist.Sections))
	for _, s := range f.Checklist.Sections {
		sections[s.Section] = s
	}
	rows := make([]cockpitCompareRow, 0, len(plan.Areas))
	for _, a := range plan.Areas {
		row := cockpitCompareRow{Key: a.Key, Label: a.Label}
		if secName, ok := compareCountSection[a.Key]; ok {
			if sec, ok := sections[secName]; ok {
				row.HasCount = true
				row.Source = sec.SourceCount
				row.Dest = sec.DestinationCount
			}
		}
		row.State, row.StateClass, row.Action = compareAreaState(a, f)
		rows = append(rows, row)
	}
	return rows
}

// compareAreaState derives the honest per-area comparison status from the plan
// category (authoritative for automatic/manual/excluded) plus the presence of a
// write artifact (report.json / <area>_apply_report.json). Migration/cutover
// blockers are shown in their own dedicated section, not attributed per-row, to
// avoid mis-mapping checklist sections to plan areas.
func compareAreaState(a migrationPlanArea, f artifactFacts) (state, class, action string) {
	if !a.Included {
		return "Escluso dallo scope", "draft", "Non migrato"
	}
	switch a.Category {
	case planManualVerifiable: // DNS in the primary flow
		return "Manuale / verificabile", "warn", "Task manuale"
	case planInformational:
		return "Da pianificare", "draft", "Genera il piano"
	}
	done := false
	switch a.Key {
	case "files", "databases", "email":
		done = f.ContentApplyPresent
	case "email_config":
		done = f.Email.ApplyPresent
	case "cron":
		done = f.Cron.ApplyPresent
	}
	if done {
		return "Migrato", "done", "Completato"
	}
	return "Da migrare", "active", "Automatico"
}

// ---- Piano semplificato (3 bucket) ------------------------------------------

// cockpitPlanBuckets regroups the plan areas into the three cards the cockpit
// shows: what runs automatically, what stays manual/verifiable, and what is
// excluded or not yet ready.
type cockpitPlanBuckets struct {
	Automatic []migrationPlanArea
	Manual    []migrationPlanArea
	Excluded  []migrationPlanArea
}

func bucketPlanAreas(plan migrationPlan) cockpitPlanBuckets {
	var b cockpitPlanBuckets
	for _, a := range plan.Areas {
		switch {
		case !a.Included:
			b.Excluded = append(b.Excluded, a)
		case a.AutoRunnable:
			b.Automatic = append(b.Automatic, a)
		default:
			// In-scope but not auto-runnable: manual/verifiable (DNS) or
			// informational (email/cron without a plan yet).
			b.Manual = append(b.Manual, a)
		}
	}
	return b
}

// ---- Execution monitor -------------------------------------------------------

// cockpitMonitorPhase is one high-level orchestrator phase (Contenuti,
// Configurazioni email, Cron, DNS) with an honest execution state.
type cockpitMonitorPhase struct {
	Label      string
	State      string // not_run|running|completed|completed_with_report|failed|skipped|manual
	StateLabel string
	StateClass string
	Detail     string
}

// cockpitRunPhase is a per-phase item-level line of the content migration,
// available ONLY when events.jsonl exists (migrate_content). Its Summary is the
// real, bounded, secret-redacted summary the run monitor already computes.
type cockpitRunPhase struct {
	Label      string
	StateLabel string
	StateClass string
	Summary    string
}

// cockpitMonitor is the execution monitor block. Active decides whether it is
// shown at all. Log carries only safe lines (run-monitor errors, already
// redacted by construction, plus the job journal error string) — NEVER a raw
// process tail (which is not persisted and not redacted).
type cockpitMonitor struct {
	Active    bool
	JobLive   bool
	Job       *jobJournal
	Phases    []cockpitMonitorPhase
	RunPhases []cockpitRunPhase
	Log       []string
}

func phaseStateLabel(s string) string {
	switch s {
	case "running":
		return "In corso"
	case "completed":
		return "Completato e verificato"
	case "completed_with_report":
		return "Completato — vedi report"
	case "failed":
		return "Fallito"
	case "skipped":
		return "Non incluso"
	case "manual":
		return "Manuale"
	default:
		return "Da eseguire"
	}
}

func phaseStateClass(s string) string {
	switch s {
	case "running":
		return "active"
	case "completed", "completed_with_report":
		return "done"
	case "failed":
		return "error"
	case "manual":
		return "warn"
	default: // not_run, skipped
		return "draft"
	}
}

// buildCockpitMonitor derives the execution monitor from the job journal, the
// artifact facts and the (optional) events.jsonl run monitor. It never claims a
// phase ran unless an artifact/journal proves it.
func buildCockpitMonitor(f artifactFacts, scope contentScope, job *jobJournal, jobLive bool, run *runMonitor) cockpitMonitor {
	m := cockpitMonitor{Job: job, JobLive: jobLive}
	m.Active = job != nil || f.ContentApplyPresent || f.Email.ApplyPresent || f.Cron.ApplyPresent || run != nil

	content := cockpitMonitorPhase{
		Label:  "Contenuti (posta, file, database)",
		State:  contentPhaseState(f, scope, job, jobLive, run),
		Detail: "Migrazione di posta, file del sito e database in un solo passo (migrate_content).",
	}
	emailCfg := cockpitMonitorPhase{
		Label:  "Configurazioni email",
		State:  configPhaseState(scope.IncludeEmailConfig, f.Email, "Configurazioni email", job, jobLive),
		Detail: "Inoltri, risponditori e filtri applicati secondo il piano email.",
	}
	cron := cockpitMonitorPhase{
		Label:  "Cron",
		State:  configPhaseState(scope.IncludeCron, f.Cron, "Cron", job, jobLive),
		Detail: "Cron job applicati secondo il piano cron.",
	}
	dns := cockpitMonitorPhase{Label: "DNS"}
	if scope.IncludeDNS {
		dns.State = "manual"
		dns.Detail = "Il DNS non viene mai eseguito automaticamente: resta un task manuale/verificabile."
	} else {
		dns.State = "skipped"
		dns.Detail = "DNS non incluso in questa migrazione."
	}

	m.Phases = []cockpitMonitorPhase{content, emailCfg, cron, dns}
	for i := range m.Phases {
		m.Phases[i].StateLabel = phaseStateLabel(m.Phases[i].State)
		m.Phases[i].StateClass = phaseStateClass(m.Phases[i].State)
	}

	if run != nil {
		for _, p := range run.Phases {
			m.RunPhases = append(m.RunPhases, cockpitRunPhase{
				Label:      runPhaseLabelIT(p.Phase),
				StateLabel: runStateLabelIT(p.State),
				StateClass: runStateClassIT(p.State),
				Summary:    p.Summary,
			})
		}
		m.Log = append(m.Log, run.Errors...)
	}
	if job != nil && job.State == jobStateFailed && strings.TrimSpace(job.Error) != "" {
		m.Log = append(m.Log, "Ultimo errore del job: "+job.Error)
	}
	return m
}

// contentPhaseState reports the state of the single content migration phase.
// The job.Phase comparisons below match the orchestrator's Italian phase labels
// ("Contenuti"/"Configurazioni email"/"Cron" in buildOrchestratorPhases,
// workbench_orchestrator.go) — the same stringly-typed convention the rest of
// the webui uses. If those labels are renamed the monitor loses only the "In
// corso" highlight; it never shows anything false (it degrades to not_run).
func contentPhaseState(f artifactFacts, scope contentScope, job *jobJournal, jobLive bool, run *runMonitor) string {
	if !(scope.IncludeFiles || scope.IncludeDatabases || scope.IncludeEmailContent) {
		return "skipped"
	}
	if run != nil && run.State == "running" {
		return "running"
	}
	if jobLive && job != nil && job.Phase == "Contenuti" {
		return "running"
	}
	if f.ContentApplyPresent {
		return "completed_with_report"
	}
	if run != nil && run.State == "failed" {
		return "failed"
	}
	if job != nil && job.State == jobStateFailed && job.Phase == "Contenuti" {
		return "failed"
	}
	return "not_run"
}

// configPhaseState reports the state of an auto-run config phase (email/cron).
// Without a plan the orchestrator cannot auto-run the area, so it stays
// "not_run" (with an honest detail) rather than claiming anything.
func configPhaseState(inScope bool, af areaFacts, phaseLabel string, job *jobJournal, jobLive bool) string {
	if !inScope {
		return "skipped"
	}
	if jobLive && job != nil && job.Phase == phaseLabel {
		return "running"
	}
	switch {
	case af.ApplyPresent && af.VerifyPresent && af.VerifyClean:
		return "completed"
	case af.ApplyPresent:
		return "completed_with_report"
	default:
		return "not_run"
	}
}

// runPhaseLabelIT maps an events.jsonl phase name to an Italian label. Unknown
// phases fall back to the raw name (a visible defect, not a crash).
func runPhaseLabelIT(phase string) string {
	switch phase {
	case "connect":
		return "Connessione"
	case "create_domains":
		return "Creazione domini"
	case "migrate_mail":
		return "Posta (per casella)"
	case "verify_mail":
		return "Verifica posta"
	case "copy_files":
		return "File del sito"
	case "verify_files":
		return "Verifica file"
	case "migrate_db":
		return "Database"
	case "verify_db":
		return "Verifica database"
	default:
		return phase
	}
}

func runStateLabelIT(state string) string {
	switch state {
	case "running":
		return "In corso"
	case "completed":
		return "Completato"
	case "failed":
		return "Fallito"
	case "skipped":
		return "Saltato"
	default:
		return state
	}
}

func runStateClassIT(state string) string {
	switch state {
	case "running":
		return "active"
	case "completed":
		return "done"
	case "failed":
		return "error"
	default: // skipped
		return "draft"
	}
}

// ---- Start gate + next action reconciliation (dogfooding #4 §6.1) -----------

// startAllowed is the SINGLE source of truth for "may the operator start the
// automatic migration right now". The hero state, the dominant CTA and the
// reconciled next action all consult it, so they can never contradict each
// other (the go-review that caught a hero-vs-CTA split: a failed/applied
// session must never show a live "Avvia migrazione" form). It mirrors the
// server-side start preconditions plus the states where starting is nonsensical
// or dangerous: a live/failed/interrupted job, an already-applied content
// migration, and hard governance stops.
func startAllowed(status workbench.Status, f artifactFacts, plan migrationPlan, job *jobJournal) bool {
	switch status {
	case workbench.StatusBlocked, workbench.StatusFailed,
		workbench.StatusApplyInProgress, workbench.StatusApplyDone,
		workbench.StatusVerificationRequired, workbench.StatusReadyForCutover,
		workbench.StatusCutoverDone, workbench.StatusArchived:
		return false
	}
	if f.ContentApplyPresent {
		return false
	}
	if job != nil && (job.State == jobStateFailed || job.State == jobStateInterrupted) {
		return false
	}
	// StartEnabled already implies !jobLive (see buildWorkbenchView).
	return plan.StartEnabled
}

// failedJobNextScreen routes "check the last failure" to the screen that owns
// the failed action: the orchestrator's start form lives on "Piano & scope",
// while every single /exec action (dns_apply, email_verify, rollbacks, …) lives
// on "Azioni avanzate".
func failedJobNextScreen(job *jobJournal) string {
	if job != nil && job.Action == orchestratorAction {
		return screenMigrazione
	}
	return screenApplica
}

// reconcileNextAction aligns the persistent "Prossima azione" with the plan
// readiness. The seeded-artifact dissonance (Panoramica said "esegui il
// preflight" while the Piano said "pronto per migrare") happens because the two
// derive from different sources. This overrides ONLY the artifact-derived,
// higher-confidence cases and otherwise returns next unchanged, so the guidance
// on every screen matches what the plan actually allows.
func reconcileNextAction(next recommendedAction, status workbench.Status, f artifactFacts, plan migrationPlan, job *jobJournal, jobLive bool) recommendedAction {
	if jobLive {
		return recommendedAction{Text: "Osserva l'avanzamento della migrazione in corso", Screen: screenPanoramica}
	}
	if job != nil && (job.State == jobStateFailed || job.State == jobStateInterrupted) {
		verb := "fallito"
		if job.State == jobStateInterrupted {
			verb = "interrotto"
		}
		return recommendedAction{Text: "Controlla l'ultimo tentativo (" + verb + ") e decidi come procedere", Screen: failedJobNextScreen(job), Detail: job.Error}
	}
	// Post-apply / terminal / governance-stopped statuses: the status-driven
	// nextAction already computes the correct guidance (verify → cutover →
	// archive; or "sblocca da Governance"). Do not push "Avvia migrazione".
	switch status {
	case workbench.StatusApplyInProgress, workbench.StatusApplyDone,
		workbench.StatusVerificationRequired, workbench.StatusReadyForCutover,
		workbench.StatusCutoverDone, workbench.StatusArchived,
		workbench.StatusBlocked, workbench.StatusFailed:
		return next
	}
	if f.ContentApplyPresent {
		return next
	}
	if !plan.Ready {
		return next // keep status-derived guidance (preflight, connessioni, …)
	}
	if plan.Blocked {
		return recommendedAction{Text: "Risolvi i problemi che bloccano la migrazione", Screen: screenMigrazione}
	}
	if !plan.ScopeConfirmed {
		return recommendedAction{Text: "Conferma cosa vuoi migrare", Screen: screenMigrazione}
	}
	if startAllowed(status, f, plan, job) {
		return recommendedAction{Text: "Avvia la migrazione automatica", Screen: screenMigrazione}
	}
	if !plan.CanStartMigration {
		return recommendedAction{Text: "Nessuna area automatica: gestisci i task manuali e il cutover", Screen: screenChiusura}
	}
	return next
}

// ---- Hero state + CTA -------------------------------------------------------

// buildCockpit assembles the whole cockpit model. next MUST already be the
// reconciled next action (so the hero, the header and the CTA agree).
func buildCockpit(status workbench.Status, f artifactFacts, scope contentScope, plan migrationPlan, cut cutoverVerdict, timeline []timelineStep, next recommendedAction, job *jobJournal, jobLive bool, run *runMonitor) cockpitModel {
	c := cockpitModel{
		Steps:             buildCockpitSteps(timeline),
		Plan:              bucketPlanAreas(plan),
		Monitor:           buildCockpitMonitor(f, scope, job, jobLive, run),
		MigrationBlockers: plan.Blockers,
		MigrationBlocked:  plan.Blocked,
		CutoverBlockers:   cut.BlockersCutover,
		PendingCutover:    cut.PendingConfirmations,
	}
	c.Comparison = buildCockpitComparison(f, plan)
	c.HasCompare = len(c.Comparison) > 0
	allowed := startAllowed(status, f, plan, job)
	c.StateLabel, c.StateClass = cockpitHeroState(f, plan, cut, job, jobLive, allowed)
	c.CTA = cockpitCTAFrom(next, jobLive, allowed)
	return c
}

// cockpitHeroState is the operator-facing "where are we" line of the hero. The
// "Pronta per migrare" verdict uses the shared startAllowed gate, so it can
// never claim ready-to-start over a failed/applied/governance-stopped session.
func cockpitHeroState(f artifactFacts, plan migrationPlan, cut cutoverVerdict, job *jobJournal, jobLive, allowed bool) (label, class string) {
	switch {
	case jobLive:
		return "Migrazione in corso", "active"
	case job != nil && job.State == jobStateInterrupted:
		return "Migrazione interrotta", "error"
	case job != nil && job.State == jobStateFailed:
		return "Ultimo tentativo fallito", "error"
	case !plan.Ready:
		return "Preflight da eseguire", "draft"
	case plan.Blocked:
		return "Bloccante migrazione", "error"
	case f.ContentApplyPresent && cut.CanShutdown:
		return "Pronta per il cutover", "done"
	case f.ContentApplyPresent:
		return "Migrazione automatica avviata — task aperti", "active"
	case !plan.ScopeConfirmed:
		return "Conferma lo scope", "warn"
	case allowed:
		return "Pronta per migrare", "done"
	case !plan.CanStartMigration:
		return "Nessuna area automatica", "warn"
	default:
		return "In preparazione", "draft"
	}
}

// cockpitCTAFrom turns the reconciled next action + the shared start gate into
// the single dominant CTA. Only when starting is genuinely allowed does the CTA
// render the real strong-confirmation form inline (Kind=="start"); otherwise it
// mirrors the reconciled next action as a primary link (or a passive wait).
func cockpitCTAFrom(next recommendedAction, jobLive, allowed bool) cockpitCTA {
	if jobLive {
		return cockpitCTA{Label: "Migrazione in corso", Detail: next.Detail, Kind: "wait"}
	}
	if allowed {
		return cockpitCTA{
			Label:  "Avvia migrazione",
			Detail: "Eseguiremo solo le aree selezionate e sicure, una fase dopo l'altra. Il DNS non verrà modificato automaticamente.",
			Kind:   "start",
			Screen: screenMigrazione,
		}
	}
	return cockpitCTA{Label: next.Text, Detail: next.Detail, Kind: "link", Screen: next.Screen}
}
