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
	Session          *workbench.Session
	Steps            []platformStep
	CurrentStepIndex int // 1..7 stepper position of the current status (honest progress)
	SrcDst           platformSrcDst
	Plan             migrationPlan
	Cockpit          cockpitModel
	Cutover          cutoverVerdict
	Tasks            []accountinventory.ManualAction
	TasksPending     int // acceptable & not accepted
	TasksTotal       int
	Compare          []platformCompareRow
	Report           platformReport
	ExpertURL        string
	Flash            string
	// HeroCTAURL is the target of the cockpit's single dominant CTA. It is
	// empty for a passive "wait" CTA (a job is in flight). For "start" it points
	// to the workbench Piano & scope screen — the strong-confirmation start form
	// lives there, tested — so the irreversible confirmation never runs off an
	// untested platform form. For "link" it points to the mapped platform screen.
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
		return expertURL + "/migrazione"
	default:
		return platformSessionURL(id, platformScreenForWorkbench(cta.Screen))
	}
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
		return "Esegui preflight", "plan"
	case workbench.StatusInventoryReady, workbench.StatusChecklistReady:
		return "Rivedi il piano", "plan"
	case workbench.StatusManualActionsRequired:
		return "Apri task manuali", "tasks"
	case workbench.StatusReadyForApply:
		return "Avvia migrazione", ""
	case workbench.StatusApplyInProgress:
		return "Monitora", ""
	case workbench.StatusApplyDone, workbench.StatusVerificationRequired:
		return "Verifica finale", "tasks"
	case workbench.StatusReadyForCutover:
		return "Vai alla chiusura", "report"
	case workbench.StatusCutoverDone, workbench.StatusArchived:
		return "Apri report", "report"
	case workbench.StatusBlocked:
		return "Risolvi bloccanti", "plan"
	case workbench.StatusFailed:
		return "Controlla l'errore", ""
	default:
		return "Apri migrazione", ""
	}
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
	case "artifact_attached":
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
	cur := statusStepIndex(status)
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
		case "artifact_attached":
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
	r := platformReport{
		Completed:     completed,
		Domain:        sessionDomain(*sess),
		Source:        fallbackDash(sess.SourceProfile),
		Dest:          fallbackDash(sess.DestinationProfile),
		HasTechReport: v.Facts.ContentApplyPresent,
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
	expertURL := "/workbench/session/" + sess.ID
	p := platformPage{
		Screen:           screen,
		CSRF:             csrf,
		Nav:              platformNav{Active: navForScreen(screen), ExpertURL: expertURL},
		Header:           platformHeader{Brand: sessionDomain(*sess), Subtitle: statusLabelIT(sess.Status)},
		Session:          sess,
		Steps:            buildPlatformSteps(sess.Status, sess.ID),
		CurrentStepIndex: statusStepIndex(sess.Status),
		SrcDst:           platformSrcDst{Source: fallbackDash(sess.SourceProfile), Dest: fallbackDash(sess.DestinationProfile)},
		Plan:             v.Plan,
		Cockpit:          v.Cockpit,
		Cutover:          v.Cutover,
		Tasks:            v.Confirms,
		Compare:          mapCompareRows(v.Cockpit.Comparison),
		Report:           buildPlatformReport(sess, v),
		ExpertURL:        expertURL,
	}
	p.HeroCTAURL = platformCTAURL(sess.ID, expertURL, v.Cockpit.CTA)
	p.TasksTotal = len(p.Tasks)
	for _, t := range p.Tasks {
		if t.Acceptable && !t.Accepted {
			p.TasksPending++
		}
	}
	return p
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

// humanTime formats a timestamp for the product UI (empty → em-dash).
func humanTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("02 Jan 2006, 15:04")
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
