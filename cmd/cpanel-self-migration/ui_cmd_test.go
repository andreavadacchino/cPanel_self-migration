package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNewUIServerValidation pins the ui subcommand's safety gates: the
// artifact directory must exist and the listen address must be loopback.
func TestNewUIServerValidation(t *testing.T) {
	dir := t.TempDir()

	if _, err := newUIServer(context.Background(), dir, "0.0.0.0:0"); err == nil {
		t.Error("non-loopback listen address must be rejected")
	}
	if _, err := newUIServer(context.Background(), dir+"/missing", "127.0.0.1:0"); err == nil {
		t.Error("a missing artifact directory must be rejected")
	}

	srv, err := newUIServer(context.Background(), dir, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("newUIServer: %v", err)
	}
	if srv.Addr != "127.0.0.1:0" {
		t.Errorf("srv.Addr = %q, want the requested listen address", srv.Addr)
	}
	if srv.ReadHeaderTimeout == 0 {
		t.Error("the server must set ReadHeaderTimeout (gosec G112, slowloris)")
	}
	// Operator-first landing: "/" redirects to the platform, whose empty state
	// guides the operator to create a first migration. (The old technical
	// dashboard hint — migration_checklist.json — was intentionally removed by
	// the operator-first reset; the guidance now lives on the platform landing.)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "127.0.0.1:0" // the rebinding gate rejects httptest's example.com default
	rr := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/platform/migrations" {
		t.Errorf("landing / = %d loc %q, want 303 -> /platform/migrations",
			rr.Code, rr.Header().Get("Location"))
	}
	req = httptest.NewRequest(http.MethodGet, "/platform/migrations", nil)
	req.Host = "127.0.0.1:0"
	rr = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, req)
	// Store-independent assertion: the platform landing always renders the
	// "Nuova migrazione" CTA (the operator-first analog of the old dashboard's
	// create hint), regardless of how many sessions the store holds.
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Nuova migrazione") {
		t.Errorf("platform landing = %d, want 200 with the 'Nuova migrazione' CTA", rr.Code)
	}
}

// TestUICmdFlagErrors: bad invocations exit 1/2 without starting a server.
func TestUICmdFlagErrors(t *testing.T) {
	if code := runUICmd([]string{"--listen", "0.0.0.0:80"}); code != 1 {
		t.Errorf("non-loopback exit = %d, want 1", code)
	}
	if code := runUICmd([]string{"--dir", t.TempDir() + "/nope"}); code != 1 {
		t.Errorf("missing dir exit = %d, want 1", code)
	}
	if code := runUICmd([]string{"--not-a-flag"}); code != 2 {
		t.Errorf("bad flag exit = %d, want 2", code)
	}
}
