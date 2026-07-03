package accountinventory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmailPlanSourcesAreOfflineByConstruction pins the read-only,
// offline invariant of the email plan-builder line of work (PR 2B-1,
// mirror of dnsplan_safety_test.go): no emailplan source may reference an
// email write primitive in code (comments may name them for
// documentation) or import a connecting package. Only the allowlisted
// writer files may name the verbs — see TestNoEmailWritePatternsModuleWide
// in internal/cpanel/email_apply_safety_test.go.
func TestEmailPlanSourcesAreOfflineByConstruction(t *testing.T) {
	writeCalls := []string{
		"add_forwarder",
		"set_default_address",
		"delete_forwarder",
		"add_auto_responder",
		"delete_auto_responder",
		"store_filter",
		"delete_filter",
		"setmxcheck",
	}
	forbiddenImports := []string{
		"internal/sshx", "internal/cpanel", "internal/migrate",
		"golang.org/x/crypto/ssh", "\"net\"", "\"net/http\"",
	}
	files, err := filepath.Glob("emailplan*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no emailplan*.go files found — glob broken?")
	}
	for _, f := range files {
		if f == "emailplan_safety_test.go" {
			continue // this file names the verbs on purpose
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			code, _, _ := strings.Cut(strings.TrimSpace(line), "//")
			for _, verb := range writeCalls {
				if strings.Contains(code, verb) {
					t.Errorf("%s:%d references email write call %q in code — the plan line of work is read-only",
						f, i+1, verb)
				}
			}
			for _, imp := range forbiddenImports {
				if strings.Contains(code, imp) {
					t.Errorf("%s:%d imports/references %q — the plan builder is offline by construction",
						f, i+1, imp)
				}
			}
		}
	}
}
