package webui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

// writeFixtureChecklist writes a minimal but real-shaped checklist into dir,
// with one input ref pointing at a real file (correct sha256 unless stale).
func writeFixtureChecklist(t *testing.T, dir string, stale bool) string {
	t.Helper()
	policyPath := filepath.Join(dir, "policy_report.json")
	if err := os.WriteFile(policyPath, []byte(`{"mode":"policy-report"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(`{"mode":"policy-report"}`))
	sha := hex.EncodeToString(sum[:])
	if stale {
		sha = strings.Repeat("0", 64)
	}
	c := accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1,
		Account: "srcacct", GeneratedAt: "2026-07-02T10:00:00Z",
		ChainVerified: true,
		OverallStatus: accountinventory.OverallManualActionRequired,
		Inputs: accountinventory.ChecklistInputs{
			Policy: accountinventory.ChecklistInputRef{File: "policy_report.json", SHA256: sha, Present: true},
		},
		Sections: []accountinventory.ChecklistSection{
			{Section: "mailboxes", Status: accountinventory.SectionOK, MigratedByTool: true, MigrationEvidence: "per_item"},
			{Section: "email_routing", Status: accountinventory.SectionNotInventoried},
		},
		ManualActions: []accountinventory.ManualAction{
			{ID: "MA-001", Key: "AK-650e9068dc67", Type: "CONFIRM_EMAIL_ROUTING", Section: "email_routing",
				BlockingCutover: true, Acceptable: true, Title: "Confirm the email routing setting on both servers",
				OperatorAction: "Compare cPanel Email Routing between source and destination."},
			{ID: "MA-002", Key: "AK-abcabcabcabc", Type: "MANUAL_CHECK_REQUIRED", Section: "redirects",
				BlockingCutover: false, Acceptable: true, Accepted: true, AcceptedBy: "andrea",
				AcceptedAt: "2026-07-02T10:00:00Z", AcceptedReason: "reviewed",
				Title: "Check cPanel redirects", OperatorAction: "Review redirects."},
		},
		Warnings: []string{"synthetic warning for the ui"},
		Summary:  accountinventory.ChecklistSummary{ManualActions: 2, Accepted: 1, NotInventoried: 1},
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "migration_checklist.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func getIndex(t *testing.T, dir string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	h, err := NewHandler(dir)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = testHost // httptest defaults to example.com, which the rebinding gate rejects
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr, rr.Body.String()
}

func TestHandlerRendersChecklist(t *testing.T) {
	dir := t.TempDir()
	writeFixtureChecklist(t, dir, false)

	rr, body := getIndex(t, dir)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	for _, want := range []string{
		"MANUAL_ACTION_REQUIRED",       // verdict
		"srcacct",                      // account
		"mailboxes",                    // section
		"email_routing",                // section
		"AK-650e9068dc67",              // stable action key (the acceptance handle)
		"synthetic warning for the ui", // warnings surfaced
		"per_item",                     // evidence level visible
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// The accepted action is visibly accepted, attributed to its author.
	if !strings.Contains(body, "andrea") {
		t.Error("page missing the acceptance author")
	}
	// Fresh inputs: no stale banner.
	if strings.Contains(body, "OBSOLETO") {
		t.Error("fresh inputs must not render the STALE banner")
	}
}

func TestHandlerStaleInputBannerDominates(t *testing.T) {
	dir := t.TempDir()
	writeFixtureChecklist(t, dir, true)

	rr, body := getIndex(t, dir)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(body, "OBSOLETO") {
		t.Error("mismatched input hash must render the STALE banner")
	}
	if !strings.Contains(body, "policy_report.json") {
		t.Error("the stale banner must name the mismatched file")
	}
}

func TestHandlerMissingChecklistIsGraceful(t *testing.T) {
	dir := t.TempDir()

	rr, body := getIndex(t, dir)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (graceful empty state)", rr.Code)
	}
	if !strings.Contains(body, "migration_checklist.json") {
		t.Error("empty state must tell the operator which file is expected")
	}
}

func TestHandlerServesNoArbitraryFiles(t *testing.T) {
	dir := t.TempDir()
	writeFixtureChecklist(t, dir, false)
	secret := filepath.Join(dir, "inventory_source.json")
	if err := os.WriteFile(secret, []byte(`{"secret":"data"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/inventory_source.json",
		"/migration_checklist.json",
		"/../../../etc/passwd",
		"/static/../inventory_source.json",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = testHost
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d (body %q), want 404", path, rr.Code, rr.Body.String()[:min(80, rr.Body.Len())])
		}
	}
}

func TestValidateLoopback(t *testing.T) {
	cases := []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:8422", true},
		{"localhost:0", true},
		{"[::1]:9000", true},
		{"0.0.0.0:8080", false},
		{"192.168.1.10:8080", false},
		{":8080", false}, // empty host binds every interface
		{"example.com:80", false},
		{"nonsense", false},
		{"LOCALHOST:80", true},         // case variants of the literal are still loopback
		{"[::1%en0]:80", false},        // IPv6 zone identifiers are not parseable IPs
		{"2130706433:80", false},       // decimal-obfuscated 127.0.0.1
		{"0177.0.0.1:80", false},       // octal-looking IPv4
		{"127.1:80", false},            // short-form IPv4
		{"[0:0:0:0:0:0:0:1]:80", true}, // expanded ::1 is genuinely loopback
	}
	for _, tc := range cases {
		err := ValidateLoopback(tc.addr)
		if tc.ok && err != nil {
			t.Errorf("ValidateLoopback(%q) = %v, want nil", tc.addr, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("ValidateLoopback(%q) = nil, want error", tc.addr)
		}
	}
}

// TestHandlerListsPresentArtifacts: the dashboard tells the operator which
// pipeline artifacts exist in the directory.
func TestHandlerListsPresentArtifacts(t *testing.T) {
	dir := t.TempDir()
	writeFixtureChecklist(t, dir, false)
	if err := os.WriteFile(filepath.Join(dir, "acceptances.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, body := getIndex(t, dir)
	if !strings.Contains(body, "acceptances.json") {
		t.Error("artifact list missing acceptances.json")
	}
	if !strings.Contains(body, "dns_import_plan.json") {
		t.Error("artifact list must show known artifacts even when absent (present/absent state)")
	}
}

// TestHandlerMutatingMethodsRejected: the dashboard is read-only — anything
// but GET/HEAD is a 405 with the Allow header.
func TestHandlerMutatingMethodsRejected(t *testing.T) {
	dir := t.TempDir()
	writeFixtureChecklist(t, dir, false)
	h, err := NewHandler(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/", nil)
		req.Host = testHost
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s / = %d, want 405", method, rr.Code)
		}
		if allow := rr.Header().Get("Allow"); !strings.Contains(allow, "GET") {
			t.Errorf("%s Allow header = %q, want GET, HEAD", method, allow)
		}
	}
}

// TestHandlerAbsoluteInputRefNeverLeaksContent (reviewer §1b): a tampered
// checklist can point an input ref at an arbitrary absolute path; the
// handler re-hashes it, but the file CONTENT must never reach the page —
// only the recorded path string and the match/mismatch verdict.
func TestHandlerAbsoluteInputRefNeverLeaksContent(t *testing.T) {
	dir := t.TempDir()
	secretDir := t.TempDir()
	secretPath := filepath.Join(secretDir, "secret.txt")
	const marker = "TOP-SECRET-CONTENT-MARKER"
	if err := os.WriteFile(secretPath, []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	c := accountinventory.MigrationChecklist{
		Mode: "migration-checklist", FormatVersion: 1, Account: "srcacct",
		OverallStatus: accountinventory.OverallBlocked,
		Inputs: accountinventory.ChecklistInputs{
			Policy: accountinventory.ChecklistInputRef{File: secretPath, SHA256: strings.Repeat("0", 64), Present: true},
		},
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "migration_checklist.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	rr, body := getIndex(t, dir)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if strings.Contains(body, marker) {
		t.Fatal("SECURITY: file content from an absolute input ref reached the rendered page")
	}
	if !strings.Contains(body, "OBSOLETO") || !strings.Contains(body, "secret.txt") {
		t.Error("the mismatch must still be reported as a stale entry (path + verdict only)")
	}
}
