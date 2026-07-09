package webui

// Platform UI V2 — operator-first SaaS shell (read-only presentation layer).
//
// This file builds the platformPage read-model, a product-shaped projection
// that the /platform screens render. It is an ADAPTER, not a second engine:
//
//   - the session screens (Piano, Cockpit, Task, Report, Comparativa) reuse the
//     view-model buildWorkbenchView already computes (Plan, Cockpit, Cutover,
//     Confirms) — the readiness/gating truth stays single-sourced in the
//     workbench layer, so the platform can never disagree with the expert view;
//   - the dashboard derives its tiles/rows from store.List() STATUSES only
//     (status is per-session; the artifact dir is shared, so per-session
//     artifact facts would cross-attribute — deliberately avoided);
//   - it NEVER writes, NEVER connects, NEVER gates: every mutating CTA delegates
//     to the existing, tested workbench POST handlers;
//   - it invents NO data: a value with no honest source degrades to an explicit
//     fallback (empty state, "non disponibile"), never a fake number.
//
// Everything here is a pure translation of on-disk facts + governance status
// into Italian product UI data, exactly like workbench_view.go.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// platformNav is the honest sidebar/active-tab model. Only WIRED destinations
// are represented; the mockup's aspirational items (Team, Modelli migrazione,
// Account, Impostazioni) are intentionally omitted rather than shown
// non-functional.
type platformNav struct {
	Active    string // "migrations" | "tasks" | "report"
	ExpertURL string // /workbench (list) or /workbench/session/:id
}

// platformHeader is the top-bar brand/subtitle (search + user chip are pure
// decorative chrome rendered by the template, with no backend).
type platformHeader struct {
	Brand    string
	Subtitle string
}

// platformStat is one dashboard KPI tile, derived from real session states.
type platformStat struct {
	Key   string // active|waiting|done|manual
	Label string
	Count int
	Class string
}

// platformMigRow is one row of the dashboard migrations table. Progress is an
// honest 1..7 stepper position (StepIndex/StepTotal), never a fabricated %.
type platformMigRow struct {
	ID          string
	Domain      string
	Name        string
	Source      string
	Dest        string
	StatusLabel string
	StatusClass string
	NextAction  string
	NextURL     string
	Updated     string
	StepIndex   int
	StepTotal   int
}

// platformActivity is one entry of the dashboard "Attività recenti" feed,
// aggregated from real session timelines (bounded).
type platformActivity struct {
	Title  string
	Detail string
	When   string
	Class  string
}

// platformStep is one node of the 7-step product stepper (Setup … Chiusura),
// matching the approved mockups. States use the timeline vocabulary.
type platformStep struct {
	Index   int
	Label   string
	State   string // done|doing|warn|todo
	Current bool
	Caption string
	URL     string
}

// platformSrcDst are the source→destination chips of the session header.
type platformSrcDst struct {
	Source string
	Dest   string
}

// platformCompareRow is one area row of the source↔destination comparison
// (schermate Piano/Cockpit/Comparativa). Reuses the honest count model from
// the cockpit (HasCount=false → "—", never a guess).
type platformCompareRow struct {
	Label      string
	HasCount   bool
	Source     int
	Dest       int
	State      string
	StateClass string
}

// platformTimelineRow is one entry of the report timeline.
type platformTimelineRow struct {
	When  string
	Label string
	Class string
}

type platformExecAction struct {
	Name    string
	Label   string
	Detail  string
	Return  string
	Primary bool
}

type platformGovernance struct {
	Show         bool
	CanBlock     bool
	CanRecover   bool
	RecoveryTo   []workbench.Status
	CurrentLabel string
}

// platformReport is the final-report read-model (schermata 6). Completed gates
// the success banner; HasTechReport reflects report.json presence (no PDF is
// generated — the technical report lives in the artifacts / expert mode).
type platformReport struct {
	Completed      bool
	CompletedLabel string
	Domain         string
	Source         string
	Dest           string
	Duration       string
	HasTechReport  bool
	Areas          []platformCompareRow
	Timeline       []platformTimelineRow
}

// platformPage is the unified read-model for every /platform screen.
type platformPage struct {
	Screen string // dashboard|cockpit|plan|tasks|report|compare
	CSRF   string
	Nav    platformNav
	Header platformHeader

	// Dashboard
	Stats      []platformStat
	Migrations []platformMigRow
	Activity   []platformActivity

	// Session screens (adapter over workbenchView)
	Session            *workbench.Session
	Steps              []platformStep
	CurrentStepIndex   int // 1..7 stepper position of the current status (honest progress)
	SrcDst             platformSrcDst
	Plan               migrationPlan
	Cockpit            cockpitModel
	Cutover            cutoverVerdict
	Risk               riskBadge
	Tasks              []accountinventory.ManualAction
	TasksPending       int // not yet accepted (still to resolve) — see buildPlatformSession
	TasksTotal         int
	Compare            []platformCompareRow
	Report             platformReport
	ExpertURL          string
	ExpertPreflightURL string
	ExpertApplyURL     string
	RunAnalysis        *platformExecAction
	VerifyActions      []platformExecAction
	Governance         platformGovernance
	Flash              string
	// HeroCTAURL is the target of the cockpit's single dominant CTA. It is
	// empty for a passive "wait" CTA (a job is in flight) or for "start" (the
	// cockpit renders the smart-start form inline). For "link" it points to the
	// mapped platform screen or to the expert workbench when the control is
	// intentionally technical.
	HeroCTAURL string
}

// platformScreenForWorkbench maps a workbench screen segment to the platform
// sub-route that owns the same operator step.
func platformScreenForWorkbench(seg string) string {
	switch seg {
	case screenPreflight, screenInventario, screenMigrazione:
		return "plan"
	case screenConferme:
		return "tasks"
	case screenChiusura:
		return "report"
	default: // screenPanoramica, screenApplica → the cockpit
		return ""
	}
}

// platformCTAURL resolves the cockpit CTA target. See platformPage.HeroCTAURL.
func platformCTAURL(id, expertURL string, cta cockpitCTA) string {
	switch cta.Kind {
	case "wait":
		return ""
	case "start":
		// The cockpit renders the strong-confirmation start FORM inline (posting
		// to /platform/migrations/:id/start-migration), so no link is needed.
		return ""
	default:
		if cta.Screen == screenPanoramica {
			return platformSessionURL(id, "")
		}
		seg := platformScreenForWorkbench(cta.Screen)
		if seg == "" {
			// The prescribed action (governance/unblock, run analysis, single
			// apply/verify) has no control on the platform primary path — its
			// workbench screen is the Panoramica/Azioni avanzate. Routing to the
			// mapped "" segment would self-loop back to this very cockpit page (a
			// dead end whose CTA text asks for a governance action that isn't
			// here). Send the operator to Modalità esperto, where it lives.
			return expertURL
		}
		return platformSessionURL(id, seg)
	}
}

func platformFailedAttemptURL(id string, v workbenchView) string {
	if v.Cockpit.CTA.Kind == "link" && v.Cockpit.CTA.Screen == screenPanoramica &&
		((v.Job != nil && (v.Job.State == jobStateFailed || v.Job.State == jobStateInterrupted)) ||
			v.Session.Status == workbench.StatusFailed) {
		return platformSessionURL(id, "")
	}
	return ""
}

func decoratePlatformCTA(status workbench.Status, url, expertURL string, cta cockpitCTA, plan migrationPlan) cockpitCTA {
	if cta.Kind != "link" {
		return cta
	}
	switch cta.Screen {
	case screenPanoramica:
		switch status {
		case workbench.StatusInventoryReady:
			cta.Label = "Apri modalità avanzata per eseguire l'analisi"
			cta.Detail = "L'analisi completa che genera la verifica migrazione è disponibile nella modalità avanzata."
		case workbench.StatusBlocked:
			cta.Label = "Gestisci il blocco della migrazione"
			cta.Detail = "Dal cockpit puoi segnare il recupero verso lo stato corretto senza uscire dalla nuova UI."
		case workbench.StatusFailed:
			cta.Label = "Gestisci il recupero della migrazione"
			cta.Detail = "Dal cockpit puoi aggiornare lo stato dopo avere verificato l'ultimo tentativo."
		case workbench.StatusArchived:
			cta.Label = "Apri modalità avanzata"
			if url != expertURL {
				return cta
			}
		default:
			if url != expertURL {
				return cta
			}
			cta.Label = "Apri modalità avanzata per continuare"
		}
	case screenApplica:
		if url != expertURL {
			return cta
		}
		switch status {
		case workbench.StatusReadyForApply:
			if plan.Blocked {
				cta.Label = "Apri modalità avanzata per risolvere i blocchi"
				cta.Detail = "Le azioni tecniche per sbloccare l'applicazione sono disponibili nella modalità avanzata."
			} else {
				cta.Label = "Apri modalità avanzata per applicare le modifiche"
			}
		case workbench.StatusApplyDone, workbench.StatusVerificationRequired:
			cta.Label = "Apri modalità avanzata per eseguire le verifiche"
			cta.Detail = "Le verifiche tecniche restano nella modalità avanzata."
		default:
			cta.Label = "Apri modalità avanzata per continuare"
		}
	}
	return cta
}

// sessionDomain is the operator-facing name of a session: its wizard primary
// domain, else its free-text name, else its id (never empty).
func sessionDomain(s workbench.Session) string {
	if s.Setup != nil && strings.TrimSpace(s.Setup.PrimaryDomain) != "" {
		return s.Setup.PrimaryDomain
	}
	if strings.TrimSpace(s.Name) != "" {
		return s.Name
	}
	return s.ID
}

// statusBucket classifies a status into one of the four dashboard tiles. Every
// status lands in exactly one bucket. blocked/failed count as "waiting" (they
// are not progressing and wait for the operator) — an honest, coarse grouping,
// not a fake success.
func statusBucket(s workbench.Status) string {
	switch s {
	case workbench.StatusCutoverDone, workbench.StatusArchived:
		return "done"
	case workbench.StatusManualActionsRequired, workbench.StatusVerificationRequired:
		return "manual"
	case workbench.StatusApplyInProgress:
		return "active"
	default:
		return "waiting"
	}
}

// statusClassPlatform maps a status to a badge color class for the row/state.
func statusClassPlatform(s workbench.Status) string {
	switch s {
	case workbench.StatusCutoverDone, workbench.StatusArchived:
		return "done"
	case workbench.StatusManualActionsRequired, workbench.StatusVerificationRequired:
		return "manual"
	case workbench.StatusApplyInProgress:
		return "active"
	case workbench.StatusBlocked, workbench.StatusFailed:
		return "error"
	default:
		return "waiting"
	}
}

// statusStepIndex maps a governance status to its position (1..7) on the
// product stepper (Setup, Preflight, Piano, Scope, Migrazione, Task manuali,
// Chiusura). Honest coarse mapping used for the dashboard progress column and
// the session stepper.
func statusStepIndex(s workbench.Status) int {
	switch s {
	case workbench.StatusDraft:
		return 1
	case workbench.StatusPreflightRequired:
		return 2
	case workbench.StatusInventoryReady, workbench.StatusChecklistReady, workbench.StatusBlocked:
		return 3
	case workbench.StatusManualActionsRequired:
		return 4
	case workbench.StatusReadyForApply, workbench.StatusApplyInProgress, workbench.StatusFailed:
		return 5
	case workbench.StatusApplyDone, workbench.StatusVerificationRequired:
		return 6
	case workbench.StatusReadyForCutover, workbench.StatusCutoverDone, workbench.StatusArchived:
		return 7
	default:
		return 1
	}
}

// dashboardNextAction is the per-row "prossima azione", derived from STATUS
// ONLY (never from shared artifact facts, which would cross-attribute between
// sessions). Returns the operator label and the platform sub-route segment.
func dashboardNextAction(s workbench.Status) (label, seg string) {
	switch s {
	case workbench.StatusDraft, workbench.StatusPreflightRequired:
		return "Apri controllo iniziale", "plan"
	case workbench.StatusInventoryReady, workbench.StatusChecklistReady:
		return "Rivedi il piano", "plan"
	case workbench.StatusManualActionsRequired:
		return "Apri task manuali", "tasks"
	case workbench.StatusReadyForApply:
		return "Apri cockpit per avviare", ""
	case workbench.StatusApplyInProgress:
		return "Monitora", ""
	case workbench.StatusApplyDone, workbench.StatusVerificationRequired:
		return "Verifica finale", "tasks"
	case workbench.StatusReadyForCutover:
		return "Vai alla chiusura", "report"
	case workbench.StatusCutoverDone, workbench.StatusArchived:
		return "Apri report", "report"
	case workbench.StatusBlocked:
		return "Controlla bloccanti", "plan"
	case workbench.StatusFailed:
		return "Controlla l'errore", ""
	default:
		return "Apri migrazione", ""
	}
}

func workbenchExpertURL(id, screen string) string {
	u := "/workbench/session/" + id
	if screen != "" {
		u += "/" + screen
	}
	return u + "?mode=expert"
}

// stepPct is the honest completion percentage of the stepper (index/total),
// used only to draw the mini progress bar — never presented as a byte/ETA %.
func stepPct(index, total int) int {
	if total <= 0 {
		return 0
	}
	if index < 0 {
		index = 0
	}
	if index > total {
		index = total
	}
	return index * 100 / total
}

// platformSessionURL builds the platform route for a session screen segment.
func platformSessionURL(id, seg string) string {
	u := "/platform/migrations/" + id
	if seg != "" {
		u += "/" + seg
	}
	return u
}

// buildPlatformStats counts sessions into the four dashboard tiles.
func buildPlatformStats(sessions []workbench.Session) []platformStat {
	counts := map[string]int{}
	for _, s := range sessions {
		counts[statusBucket(s.Status)]++
	}
	return []platformStat{
		{Key: "active", Label: "In corso", Count: counts["active"], Class: "active"},
		{Key: "waiting", Label: "In attesa", Count: counts["waiting"], Class: "waiting"},
		{Key: "done", Label: "Completate", Count: counts["done"], Class: "done"},
		{Key: "manual", Label: "Con task manuali", Count: counts["manual"], Class: "manual"},
	}
}

// buildPlatformRows projects sessions into table rows (newest first).
func buildPlatformRows(sessions []workbench.Session) []platformMigRow {
	rows := make([]platformMigRow, 0, len(sessions))
	for _, s := range sessions {
		label, seg := dashboardNextAction(s.Status)
		rows = append(rows, platformMigRow{
			ID:          s.ID,
			Domain:      sessionDomain(s),
			Name:        s.Name,
			Source:      fallbackDash(s.SourceProfile),
			Dest:        fallbackDash(s.DestinationProfile),
			StatusLabel: statusLabelIT(s.Status),
			StatusClass: statusClassPlatform(s.Status),
			NextAction:  label,
			NextURL:     platformSessionURL(s.ID, seg),
			Updated:     humanTime(s.UpdatedAt),
			StepIndex:   statusStepIndex(s.Status),
			StepTotal:   len(platformStepDefs),
		})
	}
	// Newest first: store.List returns ascending by CreatedAt, so reverse.
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows
}

// fallbackDash returns an em-dash for an empty field so a row never renders a
// blank cell.
func fallbackDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// activityMaxRows bounds the aggregated recent-activity feed.
const activityMaxRows = 8

// buildActivity aggregates the most recent timeline events across all sessions
// into the dashboard feed. Real data only, bounded.
func buildActivity(sessions []workbench.Session) []platformActivity {
	type ev struct {
		t      time.Time
		domain string
		te     workbench.TimelineEvent
	}
	var all []ev
	for _, s := range sessions {
		d := sessionDomain(s)
		for _, te := range s.Timeline {
			all = append(all, ev{te.Timestamp, d, te})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].t.After(all[j].t) })
	if len(all) > activityMaxRows {
		all = all[:activityMaxRows]
	}
	out := make([]platformActivity, 0, len(all))
	for _, e := range all {
		title, class := activityTitle(e.te)
		out = append(out, platformActivity{
			Title:  title,
			Detail: e.domain,
			When:   humanTime(e.t),
			Class:  class,
		})
	}
	return out
}

// activityTitle maps a timeline event to an operator-facing title + color class.
func activityTitle(te workbench.TimelineEvent) (string, string) {
	switch te.Action {
	case "status_change", "forced_status_change":
		return "Stato: " + statusLabelIT(te.ToStatus), statusClassPlatform(te.ToStatus)
	case "scope_confirmed":
		return "Scope confermato", "active"
	case "attach_artifact":
		return "Report allegato", "waiting"
	default:
		return "Aggiornamento migrazione", "waiting"
	}
}

// platformStepDefs is the fixed 7-step product stepper. seg is the platform
// sub-route the step links to ("" = the cockpit route).
var platformStepDefs = []struct {
	label string
	seg   string
}{
	{"Setup", ""},
	{"Preflight", "plan"},
	{"Piano", "plan"},
	{"Scope", "plan"},
	{"Migrazione", ""},
	{"Task manuali", "tasks"},
	{"Chiusura", "report"},
}

// buildPlatformSteps derives the session stepper from the governance status.
// Steps before the current index are done, the current is doing (warn when the
// session is blocked/failed), the rest are todo.
func buildPlatformSteps(status workbench.Status, id string) []platformStep {
	return buildPlatformStepsAt(status, id, statusStepIndex(status))
}

func buildPlatformStepsAt(status workbench.Status, id string, cur int) []platformStep {
	warn := status == workbench.StatusBlocked || status == workbench.StatusFailed
	steps := make([]platformStep, 0, len(platformStepDefs))
	for i, d := range platformStepDefs {
		idx := i + 1
		state := "todo"
		switch {
		case status == workbench.StatusCutoverDone || status == workbench.StatusArchived:
			state = "done"
		case idx < cur:
			state = "done"
		case idx == cur:
			if warn {
				state = "warn"
			} else {
				state = "doing"
			}
		}
		steps = append(steps, platformStep{
			Index:   idx,
			Label:   d.label,
			State:   state,
			Current: idx == cur,
			Caption: stepCaption(state, idx == cur),
			URL:     platformSessionURL(id, d.seg),
		})
	}
	return steps
}

func platformCurrentStepIndex(sess *workbench.Session, v workbenchView) int {
	if v.Plan.Ready && !v.Plan.ScopeConfirmed {
		return 4
	}
	return statusStepIndex(sess.Status)
}

// mapCompareRows adapts the cockpit comparison into the platform row shape.
func mapCompareRows(rows []cockpitCompareRow) []platformCompareRow {
	out := make([]platformCompareRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, platformCompareRow{
			Label:      r.Label,
			HasCount:   r.HasCount,
			Source:     r.Source,
			Dest:       r.Dest,
			State:      r.State,
			StateClass: r.StateClass,
		})
	}
	return out
}

// buildReportTimeline projects the session timeline into report rows.
func buildReportTimeline(events []workbench.TimelineEvent) []platformTimelineRow {
	out := make([]platformTimelineRow, 0, len(events))
	for _, te := range events {
		var label, class string
		switch te.Action {
		case "status_change", "forced_status_change":
			label = statusLabelIT(te.ToStatus)
			class = statusClassPlatform(te.ToStatus)
		case "scope_confirmed":
			label, class = "Scope confermato", "active"
		case "attach_artifact":
			label, class = "Report allegato", "waiting"
		default:
			label, class = te.Action, "waiting"
		}
		out = append(out, platformTimelineRow{When: humanTime(te.Timestamp), Label: label, Class: class})
	}
	return out
}

// buildPlatformReport assembles the final-report read-model for a session.
func buildPlatformReport(sess *workbench.Session, v workbenchView) platformReport {
	completed := sess.Status == workbench.StatusCutoverDone || sess.Status == workbench.StatusArchived
	hasAnyTechReport := v.Facts.ContentApplyPresent || v.Facts.DNS.ApplyPresent || v.Facts.Email.ApplyPresent || v.Facts.Cron.ApplyPresent
	r := platformReport{
		Completed:     completed,
		Domain:        sessionDomain(*sess),
		Source:        fallbackDash(sess.SourceProfile),
		Dest:          fallbackDash(sess.DestinationProfile),
		HasTechReport: hasAnyTechReport,
		Areas:         mapCompareRows(v.Cockpit.Comparison),
		Timeline:      buildReportTimeline(sess.Timeline),
	}
	if completed {
		r.CompletedLabel = "Migrazione completata il " + humanTime(sess.UpdatedAt)
		if d := sess.UpdatedAt.Sub(sess.CreatedAt); d > 0 {
			r.Duration = humanDuration(d)
		}
	}
	return r
}

func buildPlatformRunAnalysis(status workbench.Status, plan migrationPlan) *platformExecAction {
	if status == workbench.StatusApplyInProgress || status == workbench.StatusCutoverDone || status == workbench.StatusArchived {
		return nil
	}
	label := "Aggiorna controllo iniziale"
	if !plan.Ready {
		label = "Esegui controllo iniziale"
	}
	return &platformExecAction{
		Name:    "run_pipeline",
		Label:   label,
		Detail:  "Raccoglie fotografia, diff, policy, piano DNS e checklist usando gli artifact reali della sessione.",
		Return:  "plan",
		Primary: !plan.Ready,
	}
}

func buildPlatformVerifyActions(scope contentScope, f artifactFacts) []platformExecAction {
	var actions []platformExecAction
	if scope.IncludeDNS && f.DNS.PlanPresent {
		actions = append(actions, platformExecAction{
			Name: "dns_verify", Label: "Verifica DNS",
			Detail: "Controlla il piano DNS rispetto alla destinazione.",
			Return: "tasks",
		})
	}
	if scope.IncludeEmailConfig && f.Email.PlanPresent {
		actions = append(actions, platformExecAction{
			Name: "email_verify", Label: "Verifica configurazioni email",
			Detail: "Controlla inoltri, autoresponder, filtri e routing.",
			Return: "tasks",
		})
	}
	if scope.IncludeCron && f.Cron.PlanPresent {
		actions = append(actions, platformExecAction{
			Name: "cron_verify", Label: "Verifica cron",
			Detail: "Controlla i cron job applicati rispetto al piano.",
			Return: "tasks",
		})
	}
	return actions
}

func buildPlatformGovernance(sess *workbench.Session) platformGovernance {
	g := platformGovernance{CurrentLabel: statusLabelIT(sess.Status)}
	switch sess.Status {
	case workbench.StatusArchived, workbench.StatusCutoverDone:
		return g
	case workbench.StatusBlocked, workbench.StatusFailed:
		g.Show = true
		g.CanRecover = true
		for _, st := range workbench.AllStatuses {
			if st == sess.Status || st == workbench.StatusArchived {
				continue
			}
			g.RecoveryTo = append(g.RecoveryTo, st)
		}
		return g
	default:
		g.Show = true
		g.CanBlock = true
		return g
	}
}

// navForScreen maps a session screen to the active sidebar tab.
func navForScreen(screen string) string {
	switch screen {
	case "tasks":
		return "tasks"
	case "report":
		return "report"
	default:
		return "migrations"
	}
}

// buildPlatformSession assembles the read-model for a single session screen. It
// reuses buildWorkbenchView (built on the Panoramica so the cockpit + monitor
// are available) and maps the fields into the platform shape. Read-only.
func buildPlatformSession(dir, csrf string, sess *workbench.Session, jobBusy bool, screen string) platformPage {
	v := buildWorkbenchView(dir, csrf, screenPanoramica, sess, jobBusy)
	v.Cockpit = platformSmartCockpit(v, sess)
	expertURL := workbenchExpertURL(sess.ID, "")
	currentStep := platformCurrentStepIndex(sess, v)
	p := platformPage{
		Screen:             screen,
		CSRF:               csrf,
		Nav:                platformNav{Active: navForScreen(screen), ExpertURL: expertURL},
		Header:             platformHeader{Brand: sessionDomain(*sess), Subtitle: statusLabelIT(sess.Status)},
		Session:            sess,
		Steps:              buildPlatformStepsAt(sess.Status, sess.ID, currentStep),
		CurrentStepIndex:   currentStep,
		SrcDst:             platformSrcDst{Source: fallbackDash(sess.SourceProfile), Dest: fallbackDash(sess.DestinationProfile)},
		Plan:               v.Plan,
		Cockpit:            v.Cockpit,
		Cutover:            v.Cutover,
		Risk:               v.Risk,
		Tasks:              v.Confirms,
		Compare:            mapCompareRows(v.Cockpit.Comparison),
		Report:             buildPlatformReport(sess, v),
		ExpertURL:          expertURL,
		ExpertPreflightURL: workbenchExpertURL(sess.ID, screenPreflight),
		ExpertApplyURL:     workbenchExpertURL(sess.ID, screenApplica),
		RunAnalysis:        buildPlatformRunAnalysis(sess.Status, v.Plan),
		VerifyActions:      buildPlatformVerifyActions(v.Scope, v.Facts),
		Governance:         buildPlatformGovernance(sess),
	}
	p.HeroCTAURL = platformCTAURL(sess.ID, expertURL, v.Cockpit.CTA)
	if u := platformFailedAttemptURL(sess.ID, v); u != "" {
		p.HeroCTAURL = u
	}
	p.Cockpit.CTA = decoratePlatformCTA(sess.Status, p.HeroCTAURL, expertURL, p.Cockpit.CTA, p.Plan)
	// A task is "done" only when it has been Accepted. Non-acceptable actions
	// (blocking cron recreations, CONFIRM_MX_EXTERNAL — the engine forbids
	// waving them through) can never be Accepted, so they must stay counted as
	// REMAINING; counting only Acceptable&&!Accepted would silently tally them
	// as completed and overstate progress (honesty invariant).
	p.TasksTotal = len(p.Tasks)
	for _, t := range p.Tasks {
		if !t.Accepted {
			p.TasksPending++
		}
	}
	return p
}

func platformSmartCockpit(v workbenchView, sess *workbench.Session) cockpitModel {
	c := v.Cockpit
	if !platformSmartStartAllowed(sess.Status, v.Facts, deriveContentScope(sess), v.Job, v.JobLive) {
		return c
	}
	c.StateLabel = "Pronta per avvio smart"
	c.StateClass = "done"
	c.CTA = cockpitCTA{
		Label:  "Avvia migrazione",
		Detail: "Prima eseguiamo il controllo iniziale, poi migriamo automaticamente le aree selezionate. Il DNS resta solo checklist/verifica.",
		Kind:   "start",
		Screen: screenMigrazione,
	}
	return c
}

func platformSmartStartAllowed(status workbench.Status, f artifactFacts, scope contentScope, job *jobJournal, jobLive bool) bool {
	if jobLive {
		return false
	}
	switch status {
	case workbench.StatusBlocked, workbench.StatusFailed,
		workbench.StatusApplyInProgress, workbench.StatusApplyDone,
		workbench.StatusVerificationRequired, workbench.StatusReadyForCutover,
		workbench.StatusCutoverDone, workbench.StatusArchived:
		return false
	}
	if f.ContentApplyPresent || !f.HostYAMLPresent || !scope.HasSetup || !hasAutoRunnableSelection(scope) {
		return false
	}
	if job != nil && (job.State == jobStateFailed || job.State == jobStateInterrupted) {
		return false
	}
	return f.Checklist == nil || !f.Checklist.ApplyBlocked
}

// buildPlatformDashboard assembles the dashboard read-model from the session
// store. Derives everything from real statuses/timelines; no artifact facts.
func buildPlatformDashboard(store *workbench.Store, csrf string) (platformPage, error) {
	sessions, _, err := store.List()
	if err != nil {
		return platformPage{}, err
	}
	p := platformPage{
		Screen:     "dashboard",
		CSRF:       csrf,
		Nav:        platformNav{Active: "migrations", ExpertURL: "/workbench"},
		Header:     platformHeader{Brand: "Migrazioni cPanel", Subtitle: "Monitora e gestisci le migrazioni cPanel in corso o completate."},
		Stats:      buildPlatformStats(sessions),
		Migrations: buildPlatformRows(sessions),
		Activity:   buildActivity(sessions),
		ExpertURL:  "/workbench",
	}
	return p, nil
}

// mesiIT are the Italian month abbreviations — Go's time formatting has no
// locale, so the operator UI (mockups show "20 mag 2025") maps them by hand
// rather than leaking English month names ("Jul"/"May") into an Italian page.
var mesiIT = [...]string{"gen", "feb", "mar", "apr", "mag", "giu", "lug", "ago", "set", "ott", "nov", "dic"}

// humanTime formats a timestamp for the product UI in Italian (empty → em-dash).
func humanTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	u := t.UTC()
	return fmt.Sprintf("%02d %s %d, %02d:%02d", u.Day(), mesiIT[u.Month()-1], u.Year(), u.Hour(), u.Minute())
}

// humanDuration formats a positive duration compactly (e.g. "28 min 44 sec").
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	total := int(d.Round(time.Second).Seconds())
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	switch {
	case h > 0:
		return strconv.Itoa(h) + " h " + strconv.Itoa(m) + " min"
	case m > 0:
		return strconv.Itoa(m) + " min " + strconv.Itoa(s) + " sec"
	default:
		return strconv.Itoa(s) + " sec"
	}
}
