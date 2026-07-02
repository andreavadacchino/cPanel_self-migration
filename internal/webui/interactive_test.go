package webui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
)

const testHost = "127.0.0.1:8422"

// fakeRunner records every step invocation and can be scripted to block or
// fail; it stands in for the subprocess execution of the tool's own binary.
type fakeRunner struct {
	mu    sync.Mutex
	calls []recordedStep
	fail  string        // step name that should fail
	gate  chan struct{} // when non-nil, block each step until closed
}

type recordedStep struct {
	name string
	argv []string
}

func (f *fakeRunner) run(ctx context.Context, out io.Writer, name string, argv []string) error {
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, recordedStep{name: name, argv: append([]string{}, argv...)})
	f.mu.Unlock()
	fmt.Fprintf(out, "step %s ok\n", name)
	if name == f.fail {
		return fmt.Errorf("scripted failure in %s", name)
	}
	return nil
}

func (f *fakeRunner) recorded() []recordedStep {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedStep{}, f.calls...)
}

func newTestHandler(t *testing.T, dir string, r StepRunner) http.Handler {
	t.Helper()
	h, err := New(Options{Dir: dir, Runner: r})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

// doReq performs a request with a well-formed local Host header.
func doReq(h http.Handler, method, target string, form url.Values) *httptest.ResponseRecorder {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	req.Host = testHost
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([a-f0-9]+)"`)

func fetchCSRF(t *testing.T, h http.Handler) string {
	t.Helper()
	rr := doReq(h, http.MethodGet, "/", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rr.Code)
	}
	m := csrfRe.FindStringSubmatch(rr.Body.String())
	if m == nil {
		t.Fatal("no csrf token in the dashboard form")
	}
	return m[1]
}

func validConfigForm(csrf string) url.Values {
	return url.Values{
		"csrf":      {csrf},
		"src_ip":    {"194.76.118.193"},
		"src_port":  {"22"},
		"src_user":  {"demoacct"},
		"src_pass":  {"src-secret"},
		"dest_ip":   {"38.224.109.78"},
		"dest_port": {"22"},
		"dest_user": {"demoacct"},
		"dest_pass": {"dest-secret"},
	}
}

// waitJob polls the dashboard until it contains marker (or times out).
func waitJob(t *testing.T, h http.Handler, marker string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		body := doReq(h, http.MethodGet, "/", nil).Body.String()
		if strings.Contains(body, marker) {
			return body
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("dashboard never showed %q", marker)
	return ""
}

// ---------------------------------------------------------------------------
// Security middleware
// ---------------------------------------------------------------------------

// TestSecurityHostHeaderGate: a request whose Host header is not local is
// rejected — the DNS-rebinding defense (evil.com resolving to 127.0.0.1).
func TestSecurityHostHeaderGate(t *testing.T) {
	h := newTestHandler(t, t.TempDir(), nil)
	for host, want := range map[string]int{
		"127.0.0.1:8422": http.StatusOK,
		"localhost:8422": http.StatusOK,
		"[::1]:8422":     http.StatusOK,
		"evil.com":       http.StatusForbidden,
		"evil.com:8422":  http.StatusForbidden,
		"10.0.0.5:8422":  http.StatusForbidden,
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Errorf("Host %q = %d, want %d", host, rr.Code, want)
		}
	}
}

// TestSecurityCSRFAndOrigin: POSTs need the per-start CSRF token, and a
// cross-site Origin is rejected even with a valid token.
func TestSecurityCSRFAndOrigin(t *testing.T) {
	h := newTestHandler(t, t.TempDir(), nil)
	csrf := fetchCSRF(t, h)

	if rr := doReq(h, http.MethodPost, "/config", validConfigForm("")); rr.Code != http.StatusForbidden {
		t.Errorf("missing csrf = %d, want 403", rr.Code)
	}
	if rr := doReq(h, http.MethodPost, "/config", validConfigForm(strings.Repeat("0", 64))); rr.Code != http.StatusForbidden {
		t.Errorf("wrong csrf = %d, want 403", rr.Code)
	}

	form := validConfigForm(csrf)
	req := httptest.NewRequest(http.MethodPost, "/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	req.Host = testHost
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("cross-site Origin = %d, want 403", rr.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+testHost)
	req.Host = testHost
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Errorf("same-origin valid post = %d, want 303", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Config form
// ---------------------------------------------------------------------------

func TestConfigFormWritesValidHostYAML(t *testing.T) {
	dir := t.TempDir()
	h := newTestHandler(t, dir, nil)
	csrf := fetchCSRF(t, h)

	rr := doReq(h, http.MethodPost, "/config", validConfigForm(csrf))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("POST /config = %d (%s), want 303", rr.Code, rr.Body.String())
	}
	path := filepath.Join(dir, "host.yaml")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("host.yaml not written: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("host.yaml perms = %o, want 600 (it holds credentials)", info.Mode().Perm())
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("the AUTHORITY (config.Load) rejects the written file: %v", err)
	}
	if cfg.Src.IP != "194.76.118.193" || cfg.Src.SSHUser != "demoacct" || cfg.Src.SSHPass != "src-secret" || cfg.Src.Port != 22 {
		t.Errorf("src = %+v, want the posted values", cfg.Src)
	}
	if cfg.Dest.IP != "38.224.109.78" || cfg.Dest.SSHPass != "dest-secret" {
		t.Errorf("dest = %+v, want the posted values", cfg.Dest)
	}

	// Passwords are NEVER echoed back to the page.
	body := doReq(h, http.MethodGet, "/", nil).Body.String()
	if strings.Contains(body, "src-secret") || strings.Contains(body, "dest-secret") {
		t.Fatal("SECURITY: a stored password reached the rendered page")
	}
	// But the non-secret values are shown so the operator sees what's set.
	if !strings.Contains(body, "194.76.118.193") || !strings.Contains(body, "38.224.109.78") {
		t.Error("saved connection endpoints must be visible")
	}
}

func TestConfigFormKeepsPasswordWhenBlank(t *testing.T) {
	dir := t.TempDir()
	h := newTestHandler(t, dir, nil)
	csrf := fetchCSRF(t, h)
	if rr := doReq(h, http.MethodPost, "/config", validConfigForm(csrf)); rr.Code != http.StatusSeeOther {
		t.Fatalf("first save = %d", rr.Code)
	}

	form := validConfigForm(csrf)
	form.Set("src_ip", "194.76.116.41") // change one field...
	form.Set("src_pass", "")            // ...and leave the passwords blank
	form.Set("dest_pass", "")
	if rr := doReq(h, http.MethodPost, "/config", form); rr.Code != http.StatusSeeOther {
		t.Fatalf("second save = %d", rr.Code)
	}
	cfg, err := config.Load(filepath.Join(dir, "host.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Src.IP != "194.76.116.41" {
		t.Errorf("src ip = %q, want the updated value", cfg.Src.IP)
	}
	if cfg.Src.SSHPass != "src-secret" || cfg.Dest.SSHPass != "dest-secret" {
		t.Error("blank password fields must keep the stored passwords")
	}
}

func TestConfigFormInvalidRejectedWithoutWrite(t *testing.T) {
	dir := t.TempDir()
	h := newTestHandler(t, dir, nil)
	csrf := fetchCSRF(t, h)
	form := validConfigForm(csrf)
	form.Set("src_ip", "") // the authority requires src.ip

	rr := doReq(h, http.MethodPost, "/config", form)
	if rr.Code < 400 || rr.Code >= 500 {
		t.Fatalf("invalid config = %d, want a 4xx", rr.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "host.yaml")); !os.IsNotExist(err) {
		t.Error("an invalid config must not be written")
	}
}

// ---------------------------------------------------------------------------
// Run job
// ---------------------------------------------------------------------------

func saveValidConfig(t *testing.T, h http.Handler) {
	t.Helper()
	csrf := fetchCSRF(t, h)
	if rr := doReq(h, http.MethodPost, "/config", validConfigForm(csrf)); rr.Code != http.StatusSeeOther {
		t.Fatalf("config save = %d", rr.Code)
	}
}

func TestRunPipelineHappyPath(t *testing.T) {
	dir := t.TempDir()
	fr := &fakeRunner{}
	h := newTestHandler(t, dir, fr.run)
	saveValidConfig(t, h)
	csrf := fetchCSRF(t, h)

	rr := doReq(h, http.MethodPost, "/run", url.Values{"csrf": {csrf}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("POST /run = %d (%s), want 303", rr.Code, rr.Body.String())
	}
	waitJob(t, h, "Run completed")

	calls := fr.recorded()
	if len(calls) != 4 {
		t.Fatalf("steps = %d (%v), want 4 (inventory, diff, policy, checklist)", len(calls), calls)
	}
	joined := make([]string, len(calls))
	for i, c := range calls {
		joined[i] = strings.Join(c.argv, " ")
	}
	if !strings.Contains(joined[0], "--account-inventory") || !strings.Contains(joined[0], "--output-dir "+dir) ||
		!strings.Contains(joined[0], filepath.Join(dir, "host.yaml")) {
		t.Errorf("step 1 argv = %q, want the account inventory into the run dir", joined[0])
	}
	if !strings.Contains(joined[1], "inventory diff") || !strings.Contains(joined[2], "inventory policy") {
		t.Errorf("steps 2-3 argv = %q / %q", joined[1], joined[2])
	}
	if !strings.Contains(joined[3], "inventory checklist") || !strings.Contains(joined[3], filepath.Join(dir, "policy_report.json")) {
		t.Errorf("step 4 argv = %q, want the checklist composition", joined[3])
	}
}

func TestRunConflictWhileRunning(t *testing.T) {
	dir := t.TempDir()
	fr := &fakeRunner{gate: make(chan struct{})}
	h := newTestHandler(t, dir, fr.run)
	saveValidConfig(t, h)
	csrf := fetchCSRF(t, h)

	if rr := doReq(h, http.MethodPost, "/run", url.Values{"csrf": {csrf}}); rr.Code != http.StatusSeeOther {
		t.Fatalf("first run = %d", rr.Code)
	}
	if rr := doReq(h, http.MethodPost, "/run", url.Values{"csrf": {csrf}}); rr.Code != http.StatusConflict {
		t.Errorf("second run while busy = %d, want 409", rr.Code)
	}
	close(fr.gate)
	waitJob(t, h, "Run completed")
}

func TestRunFailureRecorded(t *testing.T) {
	dir := t.TempDir()
	fr := &fakeRunner{fail: "inventory diff"}
	h := newTestHandler(t, dir, fr.run)
	saveValidConfig(t, h)
	csrf := fetchCSRF(t, h)

	if rr := doReq(h, http.MethodPost, "/run", url.Values{"csrf": {csrf}}); rr.Code != http.StatusSeeOther {
		t.Fatalf("run = %d", rr.Code)
	}
	body := waitJob(t, h, "Run failed")
	if !strings.Contains(body, "inventory diff") {
		t.Error("the failed step must be named on the dashboard")
	}
	if calls := fr.recorded(); len(calls) != 2 {
		t.Errorf("steps executed = %d, want 2 (stop at the failing step)", len(calls))
	}
}

func TestRunRequiresConfig(t *testing.T) {
	dir := t.TempDir()
	fr := &fakeRunner{}
	h := newTestHandler(t, dir, fr.run)
	csrf := fetchCSRF(t, h)

	rr := doReq(h, http.MethodPost, "/run", url.Values{"csrf": {csrf}})
	if rr.Code < 400 || rr.Code >= 500 {
		t.Errorf("run without host.yaml = %d, want 4xx", rr.Code)
	}
	if len(fr.recorded()) != 0 {
		t.Error("no step may run without a configuration")
	}
}
