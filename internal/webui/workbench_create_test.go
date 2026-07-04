package webui

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

func TestCreateSessionFromUI(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := workbench.NewStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	h, err := New(Options{Dir: dir, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	csrf := fetchCSRF(t, h)

	// Valid creation
	form := url.Values{
		"csrf":                {csrf},
		"name":                {"testaccount"},
		"source_profile":      {"source193"},
		"destination_profile": {"dest78"},
	}
	rr := doReq(h, http.MethodPost, "/workbench/create", form)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("create: got %d, want 303; body: %s", rr.Code, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "/workbench/session/mig_") {
		t.Errorf("redirect location = %q, want /workbench/session/mig_...", loc)
	}

	// Verify session exists
	sessions, _, _ := store.List()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].Name != "testaccount" {
		t.Errorf("session name = %q, want testaccount", sessions[0].Name)
	}
}

func TestCreateSessionMissingFields(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := workbench.NewStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	h, err := New(Options{Dir: dir, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}
	csrf := fetchCSRF(t, h)

	// Missing name
	form := url.Values{
		"csrf":                {csrf},
		"name":                {""},
		"source_profile":      {"src"},
		"destination_profile": {"dst"},
	}
	rr := doReq(h, http.MethodPost, "/workbench/create", form)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("missing name: got %d, want 400", rr.Code)
	}
}

func TestCreateSessionWithoutCSRF(t *testing.T) {
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "migrations")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := workbench.NewStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	h, err := New(Options{Dir: dir, SessionStore: store})
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"name":                {"test"},
		"source_profile":      {"src"},
		"destination_profile": {"dst"},
	}
	rr := doReq(h, http.MethodPost, "/workbench/create", form)
	if rr.Code != http.StatusForbidden {
		t.Errorf("no csrf: got %d, want 403", rr.Code)
	}
}
