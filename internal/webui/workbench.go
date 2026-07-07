package webui

import (
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

//go:embed templates/workbench_list.html templates/workbench_detail.html templates/workbench_screens.html templates/workbench_new.html templates/_theme.html
var workbenchTemplatesFS embed.FS

type workbenchServer struct {
	store *workbench.Store
	tpl   *template.Template
	csrf  string
	dir   string // shared artifact dir (== server.dir): read-only artifact reads
	// jobBusy reports whether the shared single-writer slot is currently held,
	// so the view-model can reconcile a running job journal against the live
	// slot (running + free slot → interrupted). Wired in New(); nil-safe.
	jobBusy func() bool
}

func newWorkbenchServer(store *workbench.Store, dir, csrf string) (*workbenchServer, error) {
	funcMap := template.FuncMap{
		"fmtTime": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			return t.UTC().Format("2006-01-02 15:04:05 UTC")
		},
		"shortSHA": func(s string) string {
			if len(s) > 12 {
				return s[:12]
			}
			return s
		},
		"statusClass": func(s workbench.Status) string {
			switch s {
			case workbench.StatusArchived, workbench.StatusCutoverDone:
				return "completed"
			case workbench.StatusBlocked, workbench.StatusFailed:
				return "failed"
			case workbench.StatusApplyInProgress:
				return "running"
			default:
				return ""
			}
		},
		"list": func(args ...any) []any {
			return args
		},
		"statusLabel":    statusLabelIT,
		"stepLabel":      stepLabelIT,
		"manualTitleIT":  manualTitleIT,
		"manualActionIT": manualActionIT,
		// Flight Director timeline: state → dot class and Italian state label.
		"fdDot": func(state string) string {
			switch state {
			case "done":
				return "done"
			case "doing":
				return "ready"
			case "warn":
				return "partial"
			default:
				return "todo"
			}
		},
		"fdStateLabel": func(state string) string {
			switch state {
			case "done":
				return "Fatto"
			case "doing":
				return "In corso"
			case "warn":
				return "Attenzione"
			default:
				return "Da fare"
			}
		},
	}
	tpl, err := template.New("").Funcs(funcMap).ParseFS(workbenchTemplatesFS,
		"templates/workbench_list.html",
		"templates/workbench_detail.html",
		"templates/workbench_screens.html",
		"templates/workbench_new.html",
		"templates/_theme.html",
	)
	if err != nil {
		return nil, fmt.Errorf("parse workbench templates: %w", err)
	}
	return &workbenchServer{store: store, tpl: tpl, csrf: csrf, dir: dir}, nil
}

func (ws *workbenchServer) handleCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	source := strings.TrimSpace(r.FormValue("source_profile"))
	dest := strings.TrimSpace(r.FormValue("destination_profile"))

	if name == "" || source == "" || dest == "" {
		http.Error(w, "name, source_profile and destination_profile are all required", http.StatusBadRequest)
		return
	}

	sess, err := ws.store.Create(name, source, dest, time.Now())
	if err != nil {
		http.Error(w, "create session: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/workbench/session/"+sess.ID, http.StatusSeeOther)
}

func (ws *workbenchServer) handleList(w http.ResponseWriter, r *http.Request) {
	sessions, warnings, err := ws.store.List()
	if err != nil {
		http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := struct {
		Sessions []workbench.Session
		Warnings []string
		CSRF     string
	}{sessions, warnings, ws.csrf}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := ws.tpl.ExecuteTemplate(w, "workbench_list.html", data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// handleDetail renders the Panoramica (screen 1) — the base detail route.
func (ws *workbenchServer) handleDetail(w http.ResponseWriter, r *http.Request, sessionID string) {
	ws.handleScreen(w, r, sessionID, screenPanoramica)
}

// screenTemplates maps a screen segment to its top-level template name.
var screenTemplates = map[string]string{
	screenPanoramica: "workbench_detail.html",
	screenPreflight:  "screen_preflight",
	screenInventario: "screen_inventario",
	screenMigrazione: "screen_migrazione",
	screenConferme:   "screen_conferme",
	screenApplica:    "screen_applica",
	screenChiusura:   "screen_chiusura",
}

// handleScreen builds the read-only view-model from the shared artifact dir and
// renders the guided-path screen. Unknown screen → 404.
func (ws *workbenchServer) handleScreen(w http.ResponseWriter, r *http.Request, sessionID, screen string) {
	tplName, ok := screenTemplates[screen]
	if !ok {
		http.NotFound(w, r)
		return
	}
	sess, err := ws.store.Get(sessionID)
	if err != nil {
		if errors.Is(err, workbench.ErrSessionNotFound) || errors.Is(err, workbench.ErrInvalidSessionID) {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	busy := false
	if ws.jobBusy != nil {
		busy = ws.jobBusy()
	}
	view := buildWorkbenchView(ws.dir, ws.csrf, screen, sess, busy)
	// One-shot flashes from a redirect round-trip: the Fase 2 scope confirm
	// (?scope=) or the Fase 3 orchestrator outcome (?migrate=). At most one is set.
	view.Flash = scopeFlash(r.URL.Query().Get("scope"))
	if m := migrateFlash(r.URL.Query().Get("migrate")); m != "" {
		view.Flash = m
	}
	// Operator-First UX: OPERATOR mode by default; ?mode=expert reveals the
	// technical surfaces. Presentation only — never a gate, never persisted.
	if r.URL.Query().Get("mode") == "expert" {
		view.Expert = true
		view.ModeQuery = "?mode=expert"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := ws.tpl.ExecuteTemplate(w, tplName, view); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func (ws *workbenchServer) handleSetStatus(w http.ResponseWriter, r *http.Request, sessionID string) {
	status := strings.TrimSpace(r.FormValue("status"))
	force := r.FormValue("force") == "1"
	reason := r.FormValue("reason")

	if !workbench.ValidStatus(workbench.Status(status)) {
		http.Error(w, "invalid status", http.StatusBadRequest)
		return
	}

	_, err := ws.store.SetStatus(sessionID, workbench.Status(status), force, reason, time.Now().UTC())
	if err != nil {
		if errors.Is(err, workbench.ErrSessionNotFound) {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, workbench.ErrTransitionDenied) {
			http.Error(w, "transition denied: "+err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	http.Redirect(w, r, "/workbench/session/"+sessionID, http.StatusSeeOther)
}

func (ws *workbenchServer) handleAttach(w http.ResponseWriter, r *http.Request, sessionID string) {
	kind := strings.TrimSpace(r.FormValue("kind"))
	path := strings.TrimSpace(r.FormValue("path"))

	if kind == "" || path == "" {
		http.Error(w, "kind and path are required", http.StatusBadRequest)
		return
	}
	if !workbench.ValidArtifactKind(workbench.ArtifactKind(kind)) {
		http.Error(w, "unknown artifact kind", http.StatusBadRequest)
		return
	}

	_, err := ws.store.AttachArtifact(sessionID, workbench.ArtifactKind(kind), path, time.Now().UTC())
	if err != nil {
		if errors.Is(err, workbench.ErrSessionNotFound) {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, workbench.ErrUnknownArtifactKind) || errors.Is(err, workbench.ErrInvalidSessionID) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	http.Redirect(w, r, "/workbench/session/"+sessionID, http.StatusSeeOther)
}
