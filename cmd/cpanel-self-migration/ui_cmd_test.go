package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNewUIServerValidation pins the ui subcommand's safety gates: the
// artifact directory must exist and the listen address must be loopback.
func TestNewUIServerValidation(t *testing.T) {
	dir := t.TempDir()

	if _, err := newUIServer(dir, "0.0.0.0:0"); err == nil {
		t.Error("non-loopback listen address must be rejected")
	}
	if _, err := newUIServer(dir+"/missing", "127.0.0.1:0"); err == nil {
		t.Error("a missing artifact directory must be rejected")
	}

	srv, err := newUIServer(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("newUIServer: %v", err)
	}
	if srv.Addr != "127.0.0.1:0" {
		t.Errorf("srv.Addr = %q, want the requested listen address", srv.Addr)
	}
	if srv.ReadHeaderTimeout == 0 {
		t.Error("the server must set ReadHeaderTimeout (gosec G112, slowloris)")
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "127.0.0.1:0" // the rebinding gate rejects httptest's example.com default
	rr := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "migration_checklist.json") {
		t.Errorf("dashboard = %d, want 200 with the empty-state hint", rr.Code)
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
