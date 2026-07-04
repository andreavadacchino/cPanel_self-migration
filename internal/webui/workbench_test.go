package webui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

func newTestWorkbenchHandler(t *testing.T) (http.Handler, *workbench.Store, string) {
	t.Helper()
	dir := t.TempDir()
	artDir := filepath.Join(dir, "artifacts")
	os.MkdirAll(artDir, 0755)
	os.WriteFile(filepath.Join(artDir, "migration_checklist.json"), []byte("{}"), 0644)

	storeDir := filepath.Join(dir, "migrations")
	store, err := workbench.NewStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	h, err := New(Options{Dir: dir, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	return h, store, dir
}

func extractCSRF(t *testing.T, h http.Handler, sessionID string) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/workbench/session/"+sessionID, nil)
	req.Host = "127.0.0.1:8422"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	body := w.Body.String()
	idx := strings.Index(body, `name="csrf" value="`)
	if idx < 0 {
		t.Fatalf("no csrf token in detail page for %s", sessionID)
	}
	start := idx + len(`name="csrf" value="`)
	end := strings.Index(body[start:], `"`)
	return body[start : start+end]
}

func TestWorkbenchListEmpty(t *testing.T) {
	h, _, _ := newTestWorkbenchHandler(t)
	req := httptest.NewRequest("GET", "/workbench", nil)
	req.Host = "127.0.0.1:8422"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "No migration sessions") {
		t.Error("expected empty state message")
	}
}

func TestWorkbenchListWithSessions(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	store.Create("testmig", "src", "dst", time.Now().UTC())

	req := httptest.NewRequest("GET", "/workbench", nil)
	req.Host = "127.0.0.1:8422"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "testmig") {
		t.Error("session name not in output")
	}
}

func TestWorkbenchDetailNotFound(t *testing.T) {
	h, _, _ := newTestWorkbenchHandler(t)
	req := httptest.NewRequest("GET", "/workbench/session/nonexistent", nil)
	req.Host = "127.0.0.1:8422"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestWorkbenchDetailFound(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	sess, _ := store.Create("testmig", "src", "dst", time.Now().UTC())

	req := httptest.NewRequest("GET", "/workbench/session/"+sess.ID, nil)
	req.Host = "127.0.0.1:8422"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "testmig") {
		t.Error("session name not in detail page")
	}
}

func TestWorkbenchSetStatusValid(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	sess, _ := store.Create("test", "s", "d", time.Now().UTC())

	csrf := extractCSRF(t, h, sess.ID)
	form := url.Values{
		"csrf":   {csrf},
		"status": {"preflight_required"},
	}
	req := httptest.NewRequest("POST", "/workbench/session/"+sess.ID+"/status",
		strings.NewReader(form.Encode()))
	req.Host = "127.0.0.1:8422"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 303 {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}

	got, _ := store.Get(sess.ID)
	if got.Status != workbench.StatusPreflightRequired {
		t.Errorf("session status = %q", got.Status)
	}
}

func TestWorkbenchSetStatusInvalid(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	sess, _ := store.Create("test", "s", "d", time.Now().UTC())

	csrf := extractCSRF(t, h, sess.ID)
	form := url.Values{
		"csrf":   {csrf},
		"status": {"bogus"},
	}
	req := httptest.NewRequest("POST", "/workbench/session/"+sess.ID+"/status",
		strings.NewReader(form.Encode()))
	req.Host = "127.0.0.1:8422"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestWorkbenchSetStatusIllegalTransition(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	sess, _ := store.Create("test", "s", "d", time.Now().UTC())

	csrf := extractCSRF(t, h, sess.ID)
	form := url.Values{
		"csrf":   {csrf},
		"status": {"cutover_done"},
	}
	req := httptest.NewRequest("POST", "/workbench/session/"+sess.ID+"/status",
		strings.NewReader(form.Encode()))
	req.Host = "127.0.0.1:8422"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 422 {
		t.Fatalf("status = %d, want 422; body: %s", w.Code, w.Body.String())
	}
}

func TestWorkbenchAttachArtifact(t *testing.T) {
	h, store, dir := newTestWorkbenchHandler(t)
	sess, _ := store.Create("test", "s", "d", time.Now().UTC())

	artFile := filepath.Join(dir, "artifacts", "migration_checklist.json")

	csrf := extractCSRF(t, h, sess.ID)
	form := url.Values{
		"csrf": {csrf},
		"kind": {"migration_checklist"},
		"path": {artFile},
	}
	req := httptest.NewRequest("POST", "/workbench/session/"+sess.ID+"/attach",
		strings.NewReader(form.Encode()))
	req.Host = "127.0.0.1:8422"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 303 {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}

	got, _ := store.Get(sess.ID)
	if len(got.Artifacts) != 1 {
		t.Fatalf("artifacts = %d, want 1", len(got.Artifacts))
	}
}

func TestWorkbenchAttachUnknownKind(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	sess, _ := store.Create("test", "s", "d", time.Now().UTC())

	csrf := extractCSRF(t, h, sess.ID)
	form := url.Values{
		"csrf": {csrf},
		"kind": {"host_yaml"},
		"path": {"/tmp/x"},
	}
	req := httptest.NewRequest("POST", "/workbench/session/"+sess.ID+"/attach",
		strings.NewReader(form.Encode()))
	req.Host = "127.0.0.1:8422"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestWorkbenchCSRFRequired(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	sess, _ := store.Create("test", "s", "d", time.Now().UTC())

	form := url.Values{"status": {"preflight_required"}}
	req := httptest.NewRequest("POST", "/workbench/session/"+sess.ID+"/status",
		strings.NewReader(form.Encode()))
	req.Host = "127.0.0.1:8422"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 403 {
		t.Fatalf("status = %d, want 403 (missing CSRF)", w.Code)
	}
}

func TestWorkbenchEscapingXSS(t *testing.T) {
	h, store, _ := newTestWorkbenchHandler(t)
	store.Create(`<script>alert("xss")</script>`, "src", "dst", time.Now().UTC())

	req := httptest.NewRequest("GET", "/workbench", nil)
	req.Host = "127.0.0.1:8422"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	body := w.Body.String()
	if strings.Contains(body, `<script>alert`) {
		t.Error("XSS: unescaped <script> tag in output")
	}
	if !strings.Contains(body, `&lt;script&gt;`) {
		t.Error("expected HTML-escaped script tag")
	}
}

func TestWorkbenchPathTraversalInSessionID(t *testing.T) {
	h, _, _ := newTestWorkbenchHandler(t)

	req := httptest.NewRequest("GET", "/workbench/session/../../../etc/passwd", nil)
	req.Host = "127.0.0.1:8422"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Fatalf("status = %d, want 404 for traversal attempt", w.Code)
	}
}
