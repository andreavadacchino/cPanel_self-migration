// Package webui serves the LOCAL web workstation for the migration
// pipeline (UI phases 1-3: dashboard, connections+run, accept,
// apply/run monitor).
//
// Trust boundary, by construction:
//   - the UI process never opens SSH itself and never mutates servers: the
//     analysis run is a SUBPROCESS of the tool's own binary, so the CLI
//     stays the single authority for every step (and --apply stays
//     terminal-only);
//   - the only local writes are host.yaml (the same plaintext-credential,
//     0600 config file the CLI uses today) and the artifacts the spawned
//     pipeline itself produces;
//   - it never serves raw files: fixed routes, rendered pages only, so
//     there is no path-traversal surface;
//   - it binds to loopback only (ValidateLoopback), and every request
//     passes the anti-rebinding Host gate, the Origin check and — for
//     POSTs — the per-start CSRF token;
//   - no readiness logic is re-implemented here: the dashboard renders
//     decisions the offline pipeline computed.
package webui

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
	"gopkg.in/yaml.v3"
)

//go:embed templates/index.html templates/_theme.html
var templatesFS embed.FS

// knownArtifacts is the fixed set of pipeline artifacts the dashboard
// reports on (present/absent) — nothing outside this list is ever touched.
var knownArtifacts = []string{
	"inventory_source.json",
	"inventory_destination.json",
	"inventory_diff.json",
	"policy_report.json",
	"dns_import_plan.json",
	"migration_checklist.json",
	"acceptances.json",
	"report.json",
	"events.jsonl",
}

// ValidateLoopback rejects any listen address that would expose the UI
// beyond the local machine: the host must be "localhost" or a loopback IP.
// An empty host (":8080") binds every interface and is rejected too.
func ValidateLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("webui: invalid listen address %q: %w", addr, err)
	}
	if isLocalHostname(host) {
		return nil
	}
	return fmt.Errorf("webui: refusing to listen on %q — the ui binds to loopback only (127.0.0.1, ::1 or localhost)", addr)
}

// isLocalHostname reports whether host is the literal localhost (any case)
// or a loopback IP literal. No DNS resolution happens — a name that merely
// RESOLVES to loopback is rejected.
func isLocalHostname(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// Options configures the workstation handler.
type Options struct {
	// Dir is the artifact/run directory (must exist).
	Dir string
	// Runner overrides the pipeline step execution — tests only. Nil uses
	// the production subprocess runner.
	Runner StepRunner
	// BaseContext is the parent of every analysis run's context. Cancel it
	// (e.g. from a signal handler) to stop an in-flight run and kill its
	// subprocess. Nil defaults to context.Background().
	BaseContext context.Context
	// SessionStore is the workbench session store. If non-nil, enables
	// the /workbench/* routes for migration governance.
	SessionStore *workbench.Store
}

type server struct {
	dir       string
	tpl       *template.Template
	csrf      string
	job       *jobManager
	cfgMu     sync.Mutex // serializes config writes (shared host.yaml target)
	workbench *workbenchServer
	wbExec    *workbenchExecServer
	platform  *platformServer
}

// New returns the workstation handler for the given options.
func New(o Options) (http.Handler, error) {
	info, err := os.Stat(o.Dir)
	if err != nil {
		return nil, fmt.Errorf("webui: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("webui: %s is not a directory", o.Dir)
	}
	// .Funcs must be attached before ParseFS, so the template is built with
	// New(...).Funcs(...).ParseFS(...). The explicit name "index.html" keeps
	// the root template name equal to the parsed file so s.tpl.Execute still
	// renders it (New("") would leave an empty root → blank page). Funcs
	// registers the Italian localisers used by the manual-actions table.
	tpl, err := template.New("index.html").Funcs(template.FuncMap{
		"manualTitleIT":  manualTitleIT,
		"manualActionIT": manualActionIT,
	}).ParseFS(templatesFS, "templates/index.html", "templates/_theme.html")
	if err != nil {
		return nil, fmt.Errorf("webui: parse templates: %w", err)
	}
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		return nil, fmt.Errorf("webui: csrf token: %w", err)
	}
	// Sweep any leftover credential temp files from a previous crash
	// (host.yaml.*.tmp are 0600 but should not linger).
	if matches, _ := filepath.Glob(filepath.Join(o.Dir, "host.yaml.*.tmp")); matches != nil {
		for _, m := range matches {
			_ = os.Remove(m)
		}
	}
	s := &server{
		dir:  o.Dir,
		tpl:  tpl,
		csrf: hex.EncodeToString(tok),
		job:  newJobManager(o.Dir, o.Runner, o.BaseContext),
	}
	// Crash recovery: a job journal still marked running at startup is the
	// residue of a ui killed mid-exec — the in-memory slot is free by
	// construction now, so its subprocess died with the old process. Persist it
	// as interrupted so the first page reflects reality (roadmap §6).
	recoverJobJournal(o.Dir, time.Now().UTC())
	if o.SessionStore != nil {
		ws, err := newWorkbenchServer(o.SessionStore, o.Dir, s.csrf)
		if err != nil {
			return nil, err
		}
		// The per-session screens reconcile the job journal against the LIVE
		// slot: a running record with a free slot is presented as interrupted.
		ws.jobBusy = s.job.running
		s.workbench = ws
		base := o.BaseContext
		if base == nil {
			base = context.Background()
		}
		s.wbExec = &workbenchExecServer{
			store:  o.SessionStore,
			csrf:   s.csrf,
			runner: s.job.runner,
			base:   base,
			job:    s.job,
			dir:    o.Dir,
		}
		// Platform UI V2: the operator-first product shell (/platform/*). It is a
		// read-only presentation layer over the same store + shared artifact dir;
		// mutating actions delegate to the workbench POST handlers above. The old
		// workbench (/workbench/*) remains the expert/fallback surface.
		ps, err := newPlatformServer(o.SessionStore, o.Dir, s.csrf, s.job.running)
		if err != nil {
			return nil, err
		}
		s.platform = ps
	}
	// No ServeMux on purpose: its path canonicalization would answer
	// traversal-looking requests with a 307 redirect instead of a plain
	// 404. route() serves fixed paths and nothing else — no redirects
	// besides the post-action 303, no file serving.
	return http.HandlerFunc(s.route), nil
}

// NewHandler is the read-only phase-1 constructor, kept as a convenience
// wrapper: same handler, default (subprocess) runner.
func NewHandler(dir string) (http.Handler, error) {
	return New(Options{Dir: dir})
}

// route applies the request-level security gates, then dispatches the
// fixed routes.
func (s *server) route(w http.ResponseWriter, r *http.Request) {
	// Framing + sniffing + caching hardening on EVERY response: the page
	// carries the live CSRF token and the stored endpoints, and a framed
	// same-origin request would otherwise pass every gate (clickjacking).
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if !s.requestIsLocal(r) {
		http.Error(w, "forbidden: this ui only answers local requests", http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.index(w, r)
	case "/config":
		s.post(w, r, s.saveConfig)
	case "/run":
		s.post(w, r, s.startRun)
	case "/accept":
		s.post(w, r, s.saveAccept)
	default:
		if s.platform != nil && strings.HasPrefix(r.URL.Path, "/platform") && s.routePlatform(w, r) {
			return
		}
		if s.workbench != nil && s.routeWorkbench(w, r) {
			return
		}
		http.NotFound(w, r)
	}
}

// requestIsLocal is the anti-DNS-rebinding gate: the Host header the
// BROWSER sent must itself be local (a rebinding attack reaches 127.0.0.1
// with Host: evil.com), and any Origin must be a local origin too.
func (s *server) requestIsLocal(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if !isLocalHostname(host) {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		oh := u.Host
		if h, _, err := net.SplitHostPort(oh); err == nil {
			oh = h
		}
		if !isLocalHostname(oh) {
			return false
		}
	}
	return true
}

// post gates a mutating route: POST only, valid CSRF token required.
func (s *server) post(w http.ResponseWriter, r *http.Request, fn func(http.ResponseWriter, *http.Request)) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16) // 64 KiB — a config/run form is tiny
	tok := r.FormValue("csrf")
	if subtle.ConstantTimeCompare([]byte(tok), []byte(s.csrf)) != 1 {
		http.Error(w, "forbidden: missing or invalid csrf token — reload the dashboard and retry", http.StatusForbidden)
		return
	}
	fn(w, r)
}

// ---------------------------------------------------------------------------
// Config form
// ---------------------------------------------------------------------------

// yamlHost mirrors config.HostConfig with a HUMAN timeout string, so the
// written host.yaml stays hand-editable ("15s", not nanoseconds).
type yamlHost struct {
	IP      string `yaml:"ip"`
	Port    int    `yaml:"port"`
	SSHUser string `yaml:"ssh_user"`
	SSHPass string `yaml:"ssh_pass"`
	Timeout string `yaml:"timeout"`
}

type yamlConfig struct {
	Src  yamlHost `yaml:"src"`
	Dest yamlHost `yaml:"dest"`
}

func (s *server) hostYAMLPath() string { return filepath.Join(s.dir, "host.yaml") }

// saveConfig writes host.yaml from the form. Validation is delegated to
// the AUTHORITY: the candidate is written to a temp file and accepted only
// if config.Load accepts it, then atomically renamed into place. Blank
// password fields inherit the stored ones, so editing an endpoint never
// requires re-typing (or re-exposing) a secret.
func (s *server) saveConfig(w http.ResponseWriter, r *http.Request) {
	existing, _ := config.Load(s.hostYAMLPath()) // best-effort: zero value when absent

	parseHost := func(prefix string, prev config.HostConfig) (yamlHost, error) {
		h := yamlHost{
			IP:      strings.TrimSpace(r.FormValue(prefix + "_ip")),
			SSHUser: strings.TrimSpace(r.FormValue(prefix + "_user")),
			SSHPass: r.FormValue(prefix + "_pass"),
			Timeout: "15s",
		}
		if prev.Timeout > 0 {
			h.Timeout = prev.Timeout.String()
		}
		if h.SSHPass == "" {
			h.SSHPass = prev.SSHPass
		}
		portStr := strings.TrimSpace(r.FormValue(prefix + "_port"))
		if portStr == "" {
			portStr = "22"
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return h, fmt.Errorf("%s port %q is not a number", prefix, portStr)
		}
		h.Port = port
		return h, nil
	}

	src, err := parseHost("src", existing.Src)
	if err == nil {
		var dest yamlHost
		dest, err = parseHost("dest", existing.Dest)
		if err == nil {
			err = s.writeValidatedConfig(yamlConfig{Src: src, Dest: dest})
		}
	}
	if err != nil {
		http.Error(w, "invalid configuration: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// writeValidatedConfig writes the candidate to a UNIQUE temp file, lets
// config.Load (the CLI's own validator) accept or reject it, and only then
// renames it over host.yaml — the UI can never write a config the CLI
// would refuse. The caller (saveConfig) holds cfgMu, which serializes the
// full read-modify-write; os.CreateTemp already creates each file 0600, so
// there is no permission-widening window and no shared-name race.
func (s *server) writeValidatedConfig(c yamlConfig) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, "host.yaml.*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // no-op after a successful rename
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if _, err := config.Load(tmp); err != nil {
		return err
	}
	return os.Rename(tmp, s.hostYAMLPath())
}

// ---------------------------------------------------------------------------
// Run
// ---------------------------------------------------------------------------

// startRun launches the read-only analysis pipeline for the run dir.
func (s *server) startRun(w http.ResponseWriter, r *http.Request) {
	if !fileExists(s.hostYAMLPath()) {
		http.Error(w, "no configuration yet: save the server connections first", http.StatusUnprocessableEntity)
		return
	}
	if err := s.job.start(); err != nil {
		if errors.Is(err, errBusy) {
			writeBusy409(w, s.dir, s.job)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// Dashboard
// ---------------------------------------------------------------------------

// artifactEntry is one row of the artifact presence table.
type artifactEntry struct {
	Name    string
	Present bool
	ModTime string
	Size    int64
}

// staleEntry names one checklist input whose file on disk no longer matches
// the sha256 the checklist was built from.
type staleEntry struct {
	Name   string
	File   string
	Reason string
}

// cfgView is the NON-SECRET projection of host.yaml for the form: the
// page shows endpoints and whether a password is stored, never the
// password itself.
type cfgView struct {
	Present                    bool
	SrcIP, SrcPort, SrcUser    string
	SrcHasPass                 bool
	DestIP, DestPort, DestUser string
	DestHasPass                bool
}

// page is the template model. Everything in it is derived from artifacts on
// disk at request time — a refresh always reflects the current state.
type page struct {
	Dir          string
	CSRF         string
	Cfg          cfgView
	Job          jobStatus
	JobRunning   bool
	Monitor      *runMonitor
	MonitorLive  bool
	Checklist    *accountinventory.MigrationChecklist
	ChecklistErr string
	Stale        []staleEntry
	Artifacts    []artifactEntry
}

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	p := s.buildPage()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tpl.Execute(w, p); err != nil {
		// Headers are already gone; surface the failure both in-page and
		// on stderr so a template regression is debuggable.
		fmt.Fprintf(w, "\n<!-- template error: %v -->", err)
		fmt.Fprintf(os.Stderr, "webui: template error: %v\n", err)
	}
}

func (s *server) buildPage() page {
	p := page{Dir: s.dir, CSRF: s.csrf}
	p.Job = s.job.snapshot()
	p.JobRunning = p.Job.State == "running"
	p.Monitor = loadRunMonitor(s.dir, time.Now())
	p.MonitorLive = p.Monitor != nil && p.Monitor.Live
	if cfg, err := config.Load(s.hostYAMLPath()); err == nil {
		p.Cfg = cfgView{
			Present: true,
			SrcIP:   cfg.Src.IP, SrcPort: strconv.Itoa(cfg.Src.Port), SrcUser: cfg.Src.SSHUser,
			SrcHasPass: cfg.Src.SSHPass != "",
			DestIP:     cfg.Dest.IP, DestPort: strconv.Itoa(cfg.Dest.Port), DestUser: cfg.Dest.SSHUser,
			DestHasPass: cfg.Dest.SSHPass != "",
		}
		if cfg.Dest.Port == 0 {
			p.Cfg.DestPort = ""
		}
	}

	for _, name := range knownArtifacts {
		e := artifactEntry{Name: name}
		if info, err := os.Stat(filepath.Join(s.dir, name)); err == nil && info.Mode().IsRegular() {
			e.Present = true
			e.ModTime = info.ModTime().UTC().Format("2006-01-02 15:04:05 UTC")
			e.Size = info.Size()
		}
		p.Artifacts = append(p.Artifacts, e)
	}

	path := filepath.Join(s.dir, "migration_checklist.json")
	b, err := os.ReadFile(path) // #nosec G304 -- fixed name inside the operator-chosen artifact dir
	if err != nil {
		if !os.IsNotExist(err) {
			p.ChecklistErr = fmt.Sprintf("cannot read %s: %v", path, err)
		}
		return p
	}
	var c accountinventory.MigrationChecklist
	if err := json.Unmarshal(b, &c); err != nil {
		p.ChecklistErr = fmt.Sprintf("cannot parse %s: %v", path, err)
		return p
	}
	if c.Mode != "migration-checklist" {
		p.ChecklistErr = fmt.Sprintf("%s is not a migration checklist (mode %q)", path, c.Mode)
		return p
	}
	if c.FormatVersion != 1 {
		p.ChecklistErr = fmt.Sprintf("%s uses checklist format_version %d — this build renders version 1 only; regenerate or upgrade", path, c.FormatVersion)
		return p
	}
	p.Checklist = &c
	p.Stale = s.staleInputs(c.Inputs)
	return p
}

// staleInputs re-hashes every input file the checklist records and reports
// the ones that no longer match — the dominant "do not trust this page"
// signal. A relative recorded path resolves against the artifact dir.
func (s *server) staleInputs(in accountinventory.ChecklistInputs) []staleEntry {
	var out []staleEntry
	check := func(name string, ref accountinventory.ChecklistInputRef) {
		if !ref.Present || ref.File == "" || ref.SHA256 == "" {
			return
		}
		path := ref.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(s.dir, filepath.Base(ref.File))
		}
		// #nosec G304 -- the path comes from the checklist ON DISK, which is
		// exactly the artifact this feature distrusts: the read is bounded
		// to an existence/hash-match oracle — file CONTENT never reaches
		// the rendered page, only the recorded path string and the verdict.
		b, err := os.ReadFile(path)
		if err != nil {
			out = append(out, staleEntry{Name: name, File: ref.File, Reason: "file missing or unreadable"})
			return
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != ref.SHA256 {
			out = append(out, staleEntry{Name: name, File: ref.File, Reason: "content changed since the checklist was generated (sha256 mismatch)"})
		}
	}
	check("source inventory", in.SourceInventory)
	check("destination inventory", in.DestinationInventory)
	check("diff", in.Diff)
	check("policy", in.Policy)
	check("dns plan", in.DNSPlan)
	check("migration report", in.MigrationReport)
	check("acceptances", in.Acceptances)
	return out
}

// routeWorkbench handles /workbench/* routes. Returns true if it handled
// the request (caller should return), false if the path is not a workbench route.
func (s *server) routeWorkbench(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path

	if path == "/workbench" || path == "/workbench/" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		s.workbench.handleList(w, r)
		return true
	}

	if path == "/workbench/create" {
		s.post(w, r, func(w http.ResponseWriter, r *http.Request) {
			s.workbench.handleCreate(w, r)
		})
		return true
	}

	// New Migration Wizard: GET renders the guided form, POST creates the
	// session with its non-secret setup metadata.
	if path == "/workbench/new" {
		switch r.Method {
		case http.MethodGet:
			s.workbench.handleNewForm(w, r)
		case http.MethodPost:
			s.post(w, r, s.workbench.handleWizardCreate)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return true
	}

	const sessionPrefix = "/workbench/session/"
	if !strings.HasPrefix(path, sessionPrefix) {
		return false
	}
	rest := path[len(sessionPrefix):]

	// Extract session ID (first path segment)
	var sessionID, action string
	if idx := strings.IndexByte(rest, '/'); idx >= 0 {
		sessionID = rest[:idx]
		action = rest[idx+1:]
	} else {
		sessionID = rest
	}
	if sessionID == "" {
		http.NotFound(w, r)
		return true
	}

	switch {
	case action == "" && r.Method == http.MethodGet:
		s.workbench.handleDetail(w, r, sessionID)
	// Guided-path sub-views (GET only): additive, non-breaking. A POST to any
	// of these falls through to default:404 rather than a GET handler.
	case isScreenSegment(action) && r.Method == http.MethodGet:
		s.workbench.handleScreen(w, r, sessionID, action)
	case action == "status":
		s.post(w, r, func(w http.ResponseWriter, r *http.Request) {
			s.workbench.handleSetStatus(w, r, sessionID)
		})
	case action == "attach":
		s.post(w, r, func(w http.ResponseWriter, r *http.Request) {
			s.workbench.handleAttach(w, r, sessionID)
		})
	case action == "exec":
		s.post(w, r, func(w http.ResponseWriter, r *http.Request) {
			s.wbExec.handleExec(w, r, sessionID)
		})
	case action == "start-migration":
		// Fase 3: one strong confirmation runs the automatic, in-scope, safe
		// phases in sequence (DNS excluded), stop-on-first-failure.
		s.post(w, r, func(w http.ResponseWriter, r *http.Request) {
			s.wbExec.handleStartMigration(w, r, sessionID, "/workbench/session/"+sessionID+"/"+screenMigrazione)
		})
	case action == "scope":
		// Fase 2: confirm/refine the migration scope after the preflight, then
		// return to the plan screen. Metadata mutation only, no migration write.
		s.post(w, r, func(w http.ResponseWriter, r *http.Request) {
			s.workbench.handleConfirmScope(w, r, sessionID)
		})
	case action == "accept":
		// Register an operator acceptance from the Conferme screen, then return
		// to that screen (the dashboard /accept still returns to "/").
		s.post(w, r, func(w http.ResponseWriter, r *http.Request) {
			s.saveAcceptTo(w, r, "/workbench/session/"+sessionID+"/"+screenConferme)
		})
	default:
		http.NotFound(w, r)
	}
	return true
}

// isScreenSegment reports whether seg is one of the guided-path sub-view
// segments (excludes the empty base/Panoramica segment).
func isScreenSegment(seg string) bool {
	switch seg {
	case screenPreflight, screenInventario, screenMigrazione,
		screenConferme, screenApplica, screenChiusura:
		return true
	}
	return false
}
