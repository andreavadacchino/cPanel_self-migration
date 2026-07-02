// Package webui serves the LOCAL web workstation for the migration
// pipeline (UI phases 1+2a).
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

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"gopkg.in/yaml.v3"
)

//go:embed templates/index.html
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
}

type server struct {
	dir  string
	tpl  *template.Template
	csrf string
	job  *jobManager
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
	tpl, err := template.ParseFS(templatesFS, "templates/index.html")
	if err != nil {
		return nil, fmt.Errorf("webui: parse templates: %w", err)
	}
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return nil, fmt.Errorf("webui: csrf token: %w", err)
	}
	s := &server{
		dir:  o.Dir,
		tpl:  tpl,
		csrf: hex.EncodeToString(tok),
		job:  newJobManager(o.Dir, o.Runner),
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
	default:
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
	if origin := r.Header.Get("Origin"); origin != "" && origin != "null" {
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

// writeValidatedConfig writes the candidate to a temp file, lets
// config.Load (the CLI's own validator) accept or reject it, and only then
// renames it over host.yaml — the UI can never write a config the CLI
// would refuse.
func (s *server) writeValidatedConfig(c yamlConfig) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	tmp := s.hostYAMLPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if _, err := config.Load(tmp); err != nil {
		_ = os.Remove(tmp)
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
			http.Error(w, "a run is already in progress — wait for it to finish", http.StatusConflict)
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
