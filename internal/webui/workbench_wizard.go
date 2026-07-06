package webui

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// New Migration Wizard — the operator-facing setup flow. It captures the
// non-secret DEFINITION of a migration (which account, from where, to where,
// what to move) and creates a workbench session from it. It never touches
// credentials: ssh_pass and friends stay in host.yaml (0600), configured
// separately via the dashboard. See internal/workbench.SetupMeta.

// wizardEndpointForm echoes back the raw endpoint fields (ports as strings so a
// bad value is redisplayed verbatim).
type wizardEndpointForm struct {
	Host    string
	Port    string
	Account string
}

// wizardContentForm mirrors workbench.ContentSelection for the template.
type wizardContentForm struct {
	Files       bool
	Databases   bool
	Email       bool
	EmailConfig bool
	Cron        bool
	DNS         bool
}

// wizardView is the render model for the wizard form: prior values (so an
// invalid submit is not retyped) plus any human-readable errors.
type wizardView struct {
	CSRF          string
	Errors        []string
	Name          string
	PrimaryDomain string
	Notes         string
	Src           wizardEndpointForm
	Dst           wizardEndpointForm
	Content       wizardContentForm
}

// defaultWizardView is the empty form with sensible defaults (SSH port 22).
func defaultWizardView(csrf string) wizardView {
	return wizardView{
		CSRF: csrf,
		Src:  wizardEndpointForm{Port: "22"},
		Dst:  wizardEndpointForm{Port: "22"},
	}
}

// formBool reports whether an HTML checkbox was submitted checked.
func formBool(r *http.Request, key string) bool {
	switch strings.ToLower(strings.TrimSpace(r.FormValue(key))) {
	case "1", "on", "true", "yes":
		return true
	}
	return false
}

func (ws *workbenchServer) handleNewForm(w http.ResponseWriter, r *http.Request) {
	ws.renderWizard(w, defaultWizardView(ws.csrf), http.StatusOK)
}

func (ws *workbenchServer) renderWizard(w http.ResponseWriter, v wizardView, code int) {
	// Render into a buffer first so a template failure yields a clean 500
	// (headers not yet sent) instead of a partial page under an already-written
	// status code — the form uses a non-200 status on validation errors.
	var buf bytes.Buffer
	if err := ws.tpl.ExecuteTemplate(&buf, "workbench_new.html", v); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(buf.Bytes())
}

// handleWizardCreate validates the wizard submission and, when valid, creates a
// session carrying the non-secret setup metadata. On any validation error it
// re-renders the form with readable Italian messages and the submitted values,
// returning 400 — no session is created.
func (ws *workbenchServer) handleWizardCreate(w http.ResponseWriter, r *http.Request) {
	v := wizardView{
		CSRF:          ws.csrf,
		Name:          strings.TrimSpace(r.FormValue("name")),
		PrimaryDomain: strings.TrimSpace(r.FormValue("primary_domain")),
		Notes:         strings.TrimSpace(r.FormValue("notes")),
		Src: wizardEndpointForm{
			Host:    strings.TrimSpace(r.FormValue("src_host")),
			Port:    strings.TrimSpace(r.FormValue("src_port")),
			Account: strings.TrimSpace(r.FormValue("src_account")),
		},
		Dst: wizardEndpointForm{
			Host:    strings.TrimSpace(r.FormValue("dst_host")),
			Port:    strings.TrimSpace(r.FormValue("dst_port")),
			Account: strings.TrimSpace(r.FormValue("dst_account")),
		},
		Content: wizardContentForm{
			Files:       formBool(r, "content_files"),
			Databases:   formBool(r, "content_databases"),
			Email:       formBool(r, "content_email"),
			EmailConfig: formBool(r, "content_email_config"),
			Cron:        formBool(r, "content_cron"),
			DNS:         formBool(r, "content_dns"),
		},
	}

	var errs []string
	if v.Name == "" {
		errs = append(errs, "Il nome della migrazione è obbligatorio.")
	}
	if v.Src.Host == "" {
		errs = append(errs, "L'indirizzo del server sorgente è obbligatorio.")
	}
	if v.Src.Account == "" {
		errs = append(errs, "L'account cPanel sorgente è obbligatorio.")
	}
	if v.Dst.Host == "" {
		errs = append(errs, "L'indirizzo del server destinazione è obbligatorio.")
	}
	if v.Dst.Account == "" {
		errs = append(errs, "L'account cPanel destinazione è obbligatorio.")
	}
	srcPort, err := parseWizardPort(v.Src.Port)
	if err != nil {
		errs = append(errs, "Porta SSH sorgente non valida: "+err.Error())
	}
	dstPort, err := parseWizardPort(v.Dst.Port)
	if err != nil {
		errs = append(errs, "Porta SSH destinazione non valida: "+err.Error())
	}
	c := v.Content
	if !(c.Files || c.Databases || c.Email || c.EmailConfig || c.Cron || c.DNS) {
		errs = append(errs, "Seleziona almeno un contenuto da migrare.")
	}

	if len(errs) > 0 {
		v.Errors = errs
		ws.renderWizard(w, v, http.StatusBadRequest)
		return
	}

	setup := &workbench.SetupMeta{
		PrimaryDomain: v.PrimaryDomain,
		Notes:         v.Notes,
		Source:        workbench.Endpoint{Host: v.Src.Host, Port: srcPort, Account: v.Src.Account},
		Destination:   workbench.Endpoint{Host: v.Dst.Host, Port: dstPort, Account: v.Dst.Account},
		Content: workbench.ContentSelection{
			Files: c.Files, Databases: c.Databases, Email: c.Email,
			EmailConfig: c.EmailConfig, Cron: c.Cron, DNS: c.DNS,
		},
	}
	// Legacy profile fields keep the list/detail views meaningful: a compact,
	// non-secret "account@host" label derived from the wizard.
	srcProfile := endpointLabel(v.Src.Account, v.Src.Host)
	dstProfile := endpointLabel(v.Dst.Account, v.Dst.Host)

	sess, err := ws.store.CreateWithSetup(v.Name, srcProfile, dstProfile, setup, time.Now().UTC())
	if err != nil {
		http.Error(w, "create session: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/workbench/session/"+sess.ID, http.StatusSeeOther)
}

// parseWizardPort defaults an empty value to 22 and rejects out-of-range ports.
func parseWizardPort(s string) (int, error) {
	if s == "" {
		return 22, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, errPortNotNumber
	}
	if n <= 0 || n > 65535 {
		return 0, errPortRange
	}
	return n, nil
}

var (
	errPortNotNumber = &wizardErr{"deve essere un numero"}
	errPortRange     = &wizardErr{"fuori dall'intervallo 1–65535"}
)

type wizardErr struct{ msg string }

func (e *wizardErr) Error() string { return e.msg }

// endpointLabel builds a compact, non-secret "account@host" label; falls back
// to whichever part is present.
func endpointLabel(account, host string) string {
	switch {
	case account != "" && host != "":
		return account + "@" + host
	case host != "":
		return host
	default:
		return account
	}
}
