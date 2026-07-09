package webui

// Platform UI V2 — operator-first SaaS shell (routing + handlers).
//
// The /platform surface is a NEW presentation layer parallel to /workbench: a
// product shell that renders the platformPage read-model (platform_view.go).
// It never writes and never gates. Every mutating call — create a migration,
// confirm scope, start migration, register a confirmation, run a single
// apply/verify, rollback, DNS — is delegated to the EXISTING, tested workbench
// POST handlers; the platform templates simply point their forms/links there.
// The only exception is the platform wizard, which reuses parseWizardSubmission
// (the same validation the workbench wizard uses) and redirects into /platform.
//
// Request-level security (loopback + anti-rebinding Host + Origin + CSRF on
// POST) is inherited unchanged from server.route()/server.post().

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

//go:embed templates/platform_theme.html templates/platform_dashboard.html templates/platform_wizard.html templates/platform_plan.html templates/platform_cockpit.html templates/platform_tasks.html templates/platform_report.html templates/platform_compare.html
var platformTemplatesFS embed.FS

// platformServer renders the /platform screens. It shares the store, artifact
// dir, CSRF token and the live-slot probe with the rest of the ui.
type platformServer struct {
	store   *workbench.Store
	tpl     *template.Template
	csrf    string
	dir     string
	jobBusy func() bool
	cfgMu   *sync.Mutex
}

func sessionWorkDir(sess *workbench.Session, fallback string) string {
	if sess != nil && strings.TrimSpace(sess.ArtifactDir) != "" {
		return sess.ArtifactDir
	}
	return fallback
}

func newPlatformServer(store *workbench.Store, dir, csrf string, jobBusy func() bool, cfgMu *sync.Mutex) (*platformServer, error) {
	funcMap := template.FuncMap{
		"manualTitleIT":    manualTitleIT,
		"manualActionIT":   manualActionIT,
		"statusBadgeClass": statusClassPlatform,
		"statusLabel":      statusLabelIT,
		"stepPct":          stepPct,
		"list":             func(args ...any) []any { return args },
		"sub":              func(a, b int) int { return a - b },
	}
	tpl, err := template.New("").Funcs(funcMap).ParseFS(platformTemplatesFS,
		"templates/platform_theme.html",
		"templates/platform_dashboard.html",
		"templates/platform_wizard.html",
		"templates/platform_plan.html",
		"templates/platform_cockpit.html",
		"templates/platform_tasks.html",
		"templates/platform_report.html",
		"templates/platform_compare.html",
	)
	if err != nil {
		return nil, fmt.Errorf("parse platform templates: %w", err)
	}
	return &platformServer{store: store, tpl: tpl, csrf: csrf, dir: dir, jobBusy: jobBusy, cfgMu: cfgMu}, nil
}

// platformScreenTemplates maps a session screen segment to its template.
var platformScreenTemplates = map[string]string{
	"cockpit": "platform_cockpit.html",
	"plan":    "platform_plan.html",
	"tasks":   "platform_tasks.html",
	"report":  "platform_report.html",
	"compare": "platform_compare.html",
}

// routePlatform dispatches the /platform/* routes. Returns true if it handled
// the request. Kept on *server (like routeWorkbench) so it can reuse s.post for
// the CSRF-gated wizard POST.
func (s *server) routePlatform(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path

	if path == "/platform" || path == "/platform/" {
		http.Redirect(w, r, "/platform/migrations", http.StatusSeeOther)
		return true
	}

	if path == "/platform/migrations" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, "GET")
			return true
		}
		s.platform.handleDashboard(w, r)
		return true
	}

	if path == "/platform/migrations/new" {
		switch r.Method {
		case http.MethodGet:
			s.platform.handleWizardForm(w, r)
		case http.MethodPost:
			s.post(w, r, s.platform.handleWizardCreate)
		default:
			methodNotAllowed(w, "GET, POST")
		}
		return true
	}

	const prefix = "/platform/migrations/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	rest := path[len(prefix):]
	var id, screen string
	if idx := strings.IndexByte(rest, '/'); idx >= 0 {
		id = rest[:idx]
		screen = rest[idx+1:]
	} else {
		id = rest
	}
	if id == "" {
		http.NotFound(w, r)
		return true
	}
	// Mutating OPERATOR-FLOW actions kept inside the platform (CSRF via s.post),
	// each reusing the same store, config writer, runner and single-writer slot:
	//   - scope: metadata mutation (shared applyScopeConfirm core);
	//   - smart-start: platform-only guided start with popup confirmation and
	//     preflight-then-migrate sequencing;
	//   - accept: operator acceptance of a manual action (s.saveAcceptTo);
	//   - exec: read-only operator actions kept in-platform (run analysis,
	//     verify DNS/email/cron).
	//   - status: governance block/unblock from the operator cockpit.
	// The technical break-glass writes (single apply, DNS Danger Zone, rollback,
	// force-transition) deliberately stay in Modalità esperto.
	switch screen {
	case "scope":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, "POST")
			return true
		}
		s.post(w, r, func(w http.ResponseWriter, r *http.Request) {
			s.platform.handleConfirmScope(w, r, id)
		})
		return true
	case "smart-start":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, "POST")
			return true
		}
		if s.wbExec == nil {
			http.NotFound(w, r)
			return true
		}
		s.post(w, r, func(w http.ResponseWriter, r *http.Request) {
			sess, err := s.platform.store.Get(id)
			if err != nil {
				http.Error(w, "migrazione non trovata", http.StatusNotFound)
				return
			}
			exec := *s.wbExec
			exec.dir = sessionWorkDir(sess, s.wbExec.dir)
			exec.handleSmartStartMigration(w, r, id, "/platform/migrations/"+id)
		})
		return true
	case "start-migration":
		http.NotFound(w, r)
		return true
	case "accept":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, "POST")
			return true
		}
		s.post(w, r, func(w http.ResponseWriter, r *http.Request) {
			sess, err := s.platform.store.Get(id)
			if err != nil {
				http.Error(w, "migrazione non trovata", http.StatusNotFound)
				return
			}
			s.saveAcceptInDir(w, r, "/platform/migrations/"+id+"/tasks", sessionWorkDir(sess, s.dir))
		})
		return true
	case "exec":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, "POST")
			return true
		}
		if s.wbExec == nil {
			http.NotFound(w, r)
			return true
		}
		s.post(w, r, func(w http.ResponseWriter, r *http.Request) {
			sess, err := s.platform.store.Get(id)
			if err != nil {
				http.Error(w, "migrazione non trovata", http.StatusNotFound)
				return
			}
			action := strings.TrimSpace(r.FormValue("action"))
			switch action {
			case "run_pipeline", "dns_verify", "email_verify", "cron_verify":
			default:
				http.Error(w, "azione non disponibile nel percorso operatore", http.StatusBadRequest)
				return
			}
			ret := strings.TrimSpace(r.FormValue("return_to"))
			switch ret {
			case "", "cockpit":
				ret = ""
			case "plan", "tasks", "report", "compare":
			default:
				http.Error(w, "return_to non valido", http.StatusBadRequest)
				return
			}
			exec := *s.wbExec
			exec.dir = sessionWorkDir(sess, s.wbExec.dir)
			exec.handleExecRedirect(w, r, id, platformSessionURL(id, ret))
		})
		return true
	case "status":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, "POST")
			return true
		}
		s.post(w, r, func(w http.ResponseWriter, r *http.Request) {
			s.platform.handleSetStatus(w, r, id)
		})
		return true
	case "events":
		// Live progress stream (SSE). GET only, no mutation → no CSRF; the
		// loopback + Host/Origin gate in route() already applies.
		if r.Method != http.MethodGet {
			methodNotAllowed(w, "GET")
			return true
		}
		s.platform.handleSessionEvents(w, r, id)
		return true
	}
	if screen == "" {
		screen = "cockpit"
	}
	if _, ok := platformScreenTemplates[screen]; !ok {
		http.NotFound(w, r)
		return true
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return true
	}
	s.platform.handleSession(w, r, id, screen)
	return true
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (ps *platformServer) handleDashboard(w http.ResponseWriter, r *http.Request) {
	page, err := buildPlatformDashboard(ps.store, ps.csrf)
	if err != nil {
		http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	ps.render(w, "platform_dashboard.html", page)
}

// defaultPlatformWizardView is the fresh platform wizard form. The common
// content areas are pre-selected for a smoother operator flow; DNS is
// DELIBERATELY left off — touching DNS must always be an explicit choice (same
// posture as workbench.ContentSelection).
func defaultPlatformWizardView(csrf string) wizardView {
	v := defaultWizardView(csrf)
	v.Content = wizardContentForm{Files: true, Databases: true, Email: true, EmailConfig: true, Cron: true, DNS: false}
	return v
}

func (ps *platformServer) handleWizardForm(w http.ResponseWriter, r *http.Request) {
	ps.render(w, "platform_wizard.html", defaultPlatformWizardView(ps.csrf))
}

// handleWizardCreate validates via the SHARED parseWizardSubmission (identical
// to the workbench wizard) and, on success, creates the session and redirects
// into the platform cockpit. On error it re-renders the platform wizard (400).
func (ps *platformServer) handleWizardCreate(w http.ResponseWriter, r *http.Request) {
	sub, v, ok := parseWizardSubmission(r, ps.csrf)
	if !ok {
		ps.renderCode(w, "platform_wizard.html", v, http.StatusBadRequest)
		return
	}
	cfg, err := ps.parseWizardConfig(r)
	if err != nil {
		v.Errors = append(v.Errors, "Credenziali non valide: "+err.Error())
		ps.renderCode(w, "platform_wizard.html", v, http.StatusBadRequest)
		return
	}
	sess, err := ps.store.CreateWithSetup(sub.Name, sub.SrcProfile, sub.DstProfile, sub.Setup, time.Now().UTC())
	if err != nil {
		http.Error(w, "create session: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := ps.store.SetStatus(sess.ID, workbench.StatusPreflightRequired, false, "", time.Now().UTC()); err != nil {
		http.Error(w, "initialize session: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := writeValidatedConfigAt(sessionWorkDir(sess, ps.dir), cfg); err != nil {
		// Clean up ONLY the freshly created session directory, captured explicitly
		// from sess.ArtifactDir (…/<id>/artifacts → its parent is the session dir).
		// Never route this through sessionWorkDir: an empty ArtifactDir would make
		// it fall back to ps.dir and RemoveAll the parent of the whole working tree.
		if sess.ArtifactDir != "" {
			_ = os.RemoveAll(filepath.Dir(sess.ArtifactDir))
		}
		http.Error(w, "save config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if hasAutomaticArea(sub.Setup.Content) {
		if _, err := ps.store.ConfirmScope(sess.ID, sub.Setup.Content, time.Now().UTC()); err != nil {
			http.Error(w, "confirm scope: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/platform/migrations/"+sess.ID, http.StatusSeeOther)
}

func (ps *platformServer) parseWizardConfig(r *http.Request) (yamlConfig, error) {
	c, err := parseConfigForm(r, config.Config{}, configFormNames{
		SrcIP: "src_host", SrcPort: "src_port", SrcUser: "src_account", SrcPass: "src_pass",
		DestIP: "dst_host", DestPort: "dst_port", DestUser: "dst_account", DestPass: "dst_pass",
	})
	if err != nil {
		return yamlConfig{}, err
	}
	if err := validateConfigCandidate(c); err != nil {
		return yamlConfig{}, err
	}
	return c, nil
}

// jobLiveNow mirrors workbenchServer.jobLiveNow: an exec is genuinely in flight
// (journal running after reconciliation against the live slot).
func (ps *platformServer) jobLiveNow(dir string) bool {
	busy := false
	if ps.jobBusy != nil {
		busy = ps.jobBusy()
	}
	job := reconcileJobJournal(dir, busy)
	return job != nil && job.State == jobStateRunning
}

// handleConfirmScope confirms the migration scope from the platform Piano
// screen and returns to it — reusing the SAME shared core as the workbench
// (applyScopeConfirm): identical edit gate, preset resolution and metadata
// mutation, only the redirect stays in /platform so the operator is never
// bounced into the expert workbench.
func (ps *platformServer) handleConfirmScope(w http.ResponseWriter, r *http.Request, sessionID string) {
	sess, err := ps.store.Get(sessionID)
	if err != nil {
		http.Error(w, "migrazione non trovata", http.StatusNotFound)
		return
	}
	dir := sessionWorkDir(sess, ps.dir)
	query, code, msg := applyScopeConfirm(ps.store, dir, ps.jobLiveNow(dir), r, sessionID)
	if code != 0 {
		http.Error(w, msg, code)
		return
	}
	http.Redirect(w, r, "/platform/migrations/"+sessionID+query, http.StatusSeeOther)
}

func platformStatusFlash(code string) string {
	switch code {
	case "blocked":
		return "Migrazione segnata come bloccata dalla nuova UI."
	case "updated":
		return "Stato della migrazione aggiornato dalla nuova UI."
	default:
		return ""
	}
}

func (ps *platformServer) handleSetStatus(w http.ResponseWriter, r *http.Request, sessionID string) {
	sess, err := ps.store.Get(sessionID)
	if err != nil {
		http.Error(w, "migrazione non trovata", http.StatusNotFound)
		return
	}
	action := strings.TrimSpace(r.FormValue("gov_action"))
	switch action {
	case "block":
		if _, err := ps.store.SetStatus(sessionID, workbench.StatusBlocked, false, r.FormValue("reason"), time.Now().UTC()); err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		http.Redirect(w, r, "/platform/migrations/"+sessionID+"?gov=blocked", http.StatusSeeOther)
		return
	case "recover":
		to := workbench.Status(strings.TrimSpace(r.FormValue("status")))
		// Validate against the SAME set the UI exposes (RecoveryTo): this rejects a
		// forged POST forcing StatusArchived (never offered) or a recover on a
		// session that is not Blocked/Failed. Status is metadata — the real
		// write-gates are unchanged — but it must not drift from what the UI shows.
		if !statusInSet(to, recoveryTargets(sess)) {
			http.Error(w, "stato di recupero non valido", http.StatusBadRequest)
			return
		}
		if _, err := ps.store.SetStatus(sessionID, to, true, r.FormValue("reason"), time.Now().UTC()); err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		http.Redirect(w, r, "/platform/migrations/"+sessionID+"?gov=updated", http.StatusSeeOther)
		return
	default:
		http.Error(w, "azione governance non valida", http.StatusBadRequest)
		return
	}
}

func (ps *platformServer) handleSession(w http.ResponseWriter, r *http.Request, id, screen string) {
	tplName := platformScreenTemplates[screen]
	sess, err := ps.store.Get(id)
	if err != nil {
		if errors.Is(err, workbench.ErrSessionNotFound) || errors.Is(err, workbench.ErrInvalidSessionID) {
			http.Error(w, "migrazione non trovata", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	busy := false
	if ps.jobBusy != nil {
		busy = ps.jobBusy()
	}
	dir := sessionWorkDir(sess, ps.dir)
	page := buildPlatformSession(dir, ps.dir, ps.csrf, sess, busy, screen)
	// One-shot flashes from a redirect round-trip through the workbench POST
	// handlers (scope confirm / orchestrator outcome), same query contract.
	page.Flash = scopeFlash(r.URL.Query().Get("scope"))
	if m := migrateFlash(r.URL.Query().Get("migrate")); m != "" {
		page.Flash = m
	}
	if m := platformStatusFlash(r.URL.Query().Get("gov")); m != "" {
		page.Flash = m
	}
	// After an async smart-start the 303 fired when the job STARTED (carrying
	// ?migrate=started), so the real result is not in the query. Once the
	// background run settles, surface its persisted outcome from the job journal
	// so a meta-refresh / SSE reload of the same URL shows the actual result
	// instead of the stale "started" flash. Scoped to THIS session's journal
	// (single-account dir) and only for a terminal run.
	if jj, ok := readJobJournal(dir); ok && jj.SessionID == id &&
		jj.State != jobStateRunning && jj.Outcome != "" {
		page.Flash = jj.Outcome
	}
	ps.render(w, tplName, page)
}

func (ps *platformServer) render(w http.ResponseWriter, name string, data any) {
	ps.renderCode(w, name, data, http.StatusOK)
}

// renderCode renders into a buffer first so a template failure yields a clean
// 500 (headers not yet written) instead of a partial page under a wrong status.
func (ps *platformServer) renderCode(w http.ResponseWriter, name string, data any, code int) {
	var buf bytes.Buffer
	if err := ps.tpl.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(buf.Bytes())
}
