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

//go:embed templates/workbench_list.html templates/workbench_detail.html
var workbenchTemplatesFS embed.FS

type workbenchServer struct {
	store *workbench.Store
	tpl   *template.Template
	csrf  string
}

func newWorkbenchServer(store *workbench.Store, csrf string) (*workbenchServer, error) {
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
	}
	tpl, err := template.New("").Funcs(funcMap).ParseFS(workbenchTemplatesFS,
		"templates/workbench_list.html",
		"templates/workbench_detail.html",
	)
	if err != nil {
		return nil, fmt.Errorf("parse workbench templates: %w", err)
	}
	return &workbenchServer{store: store, tpl: tpl, csrf: csrf}, nil
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

func (ws *workbenchServer) handleDetail(w http.ResponseWriter, r *http.Request, sessionID string) {
	sess, err := ws.store.Get(sessionID)
	if err != nil {
		if errors.Is(err, workbench.ErrSessionNotFound) || errors.Is(err, workbench.ErrInvalidSessionID) {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data := struct {
		Session     *workbench.Session
		CSRF        string
		AllStatuses []workbench.Status
		AllKinds    []workbench.ArtifactKind
	}{sess, ws.csrf, workbench.AllStatuses, workbench.AllArtifactKinds}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := ws.tpl.ExecuteTemplate(w, "workbench_detail.html", data); err != nil {
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
