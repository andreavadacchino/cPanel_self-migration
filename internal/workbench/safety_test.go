package workbench_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkbenchNoForbiddenImports ensures the workbench package never imports
// sshx or cpanel — it must remain offline and credential-free.
func TestWorkbenchNoForbiddenImports(t *testing.T) {
	forbidden := []string{
		"github.com/tis24dev/cPanel_self-migration/internal/sshx",
		"github.com/tis24dev/cPanel_self-migration/internal/cpanel",
		"github.com/tis24dev/cPanel_self-migration/internal/config",
	}

	dir := "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imp := range f.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			for _, fb := range forbidden {
				if importPath == fb {
					t.Errorf("%s imports forbidden package %q — workbench must be offline", e.Name(), fb)
				}
			}
		}
	}
}

// TestWorkbenchNoWriteVerbs scans all non-test Go files in the workbench
// package for strings that suggest SSH/cPanel write operations.
func TestWorkbenchNoWriteVerbs(t *testing.T) {
	forbiddenStrings := []string{
		"RunUAPI",
		"RunAPI2",
		"DialBoth",
		"DialDest",
		"DialSource",
		"sshx.Dial",
		"mass_edit_zone",
		"add_forwarder",
		"delete_forwarder",
		"set_default_address",
		"crontab",
	}

	dir := "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, verb := range forbiddenStrings {
			if strings.Contains(string(content), verb) {
				t.Errorf("%s contains forbidden write verb %q", e.Name(), verb)
			}
		}
	}
}

// TestSessionJSONNoCredentialFields verifies that the Session type's JSON
// serialization never includes fields that could contain SECRETS.
//
// Since the New Migration Wizard (setup flow) the session legitimately stores
// the NON-secret connection COORDINATES of a migration — host, port and the
// cPanel account (== user-level SSH user) — under Session.Setup. Those are
// safe to persist and display; they are what makes a session self-describing.
// This guard therefore forbids only genuinely secret-bearing tags. Credentials
// (ssh_pass, tokens, keys) live ONLY in host.yaml (0600) and must never appear
// in a session json tag. A structural companion guard lives in setup_test.go
// (TestEndpointHasNoSecretField).
func TestSessionJSONNoCredentialFields(t *testing.T) {
	credentialFields := []string{
		"password", "passwd", "ssh_pass", "token", "secret",
		"ssh_key", "private_key", "apikey", "api_key", "credential",
	}

	dir := "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(content))
		for _, field := range credentialFields {
			// Look for json tags that would serialize credential data
			jsonTag := `json:"` + field
			if strings.Contains(lower, jsonTag) {
				t.Errorf("%s has json tag containing credential field %q", e.Name(), field)
			}
		}
	}
}
