// Package webui serves the LOCAL, read-only artifact browser (UI phase 1).
//
// Trust boundary, by construction:
//   - it renders decisions ALREADY computed by the offline pipeline — no
//     readiness logic is re-implemented here;
//   - it never opens SSH connections and never mutates anything;
//   - it never serves raw files: the single route is the rendered
//     dashboard, so there is no path-traversal surface;
//   - it binds to loopback only (ValidateLoopback).
package webui

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
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
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("webui: refusing to listen on %q — the ui binds to loopback only (127.0.0.1, ::1 or localhost)", addr)
	}
	return nil
}

type handler struct {
	dir string
	tpl *template.Template
}

// NewHandler returns the dashboard handler rooted at the given artifact
// directory. The directory must exist.
func NewHandler(dir string) (http.Handler, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("webui: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("webui: %s is not a directory", dir)
	}
	tpl, err := template.ParseFS(templatesFS, "templates/index.html")
	if err != nil {
		return nil, fmt.Errorf("webui: parse templates: %w", err)
	}
	h := &handler{dir: dir, tpl: tpl}
	// No ServeMux on purpose: with a single route, the mux's path
	// canonicalization would answer traversal-looking requests with a 307
	// redirect instead of a plain 404. The handler serves "/" and nothing
	// else — no redirects, no file serving.
	return http.HandlerFunc(h.index), nil
}

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

// page is the template model. Everything in it is derived from artifacts on
// disk at request time — a refresh always reflects the current state.
type page struct {
	Dir          string
	Checklist    *accountinventory.MigrationChecklist
	ChecklistErr string
	Stale        []staleEntry
	Artifacts    []artifactEntry
}

func (h *handler) index(w http.ResponseWriter, r *http.Request) {
	// Anything but the exact dashboard path is a 404 — this handler never
	// serves files; anything but a read is a 405 — it never mutates.
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := h.buildPage()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tpl.Execute(w, p); err != nil {
		// Headers are already gone; surface the failure both in-page and
		// on stderr so a template regression is debuggable.
		fmt.Fprintf(w, "\n<!-- template error: %v -->", err)
		fmt.Fprintf(os.Stderr, "webui: template error: %v\n", err)
	}
}

func (h *handler) buildPage() page {
	p := page{Dir: h.dir}
	for _, name := range knownArtifacts {
		e := artifactEntry{Name: name}
		if info, err := os.Stat(filepath.Join(h.dir, name)); err == nil && info.Mode().IsRegular() {
			e.Present = true
			e.ModTime = info.ModTime().UTC().Format("2006-01-02 15:04:05 UTC")
			e.Size = info.Size()
		}
		p.Artifacts = append(p.Artifacts, e)
	}

	path := filepath.Join(h.dir, "migration_checklist.json")
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
	p.Stale = h.staleInputs(c.Inputs)
	return p
}

// staleInputs re-hashes every input file the checklist records and reports
// the ones that no longer match — the dominant "do not trust this page"
// signal. A relative recorded path resolves against the artifact dir.
func (h *handler) staleInputs(in accountinventory.ChecklistInputs) []staleEntry {
	var out []staleEntry
	check := func(name string, ref accountinventory.ChecklistInputRef) {
		if !ref.Present || ref.File == "" || ref.SHA256 == "" {
			return
		}
		path := ref.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(h.dir, filepath.Base(ref.File))
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
