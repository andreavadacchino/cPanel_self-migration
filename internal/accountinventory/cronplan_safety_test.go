package accountinventory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCronPlanSourcesAreOfflineByConstruction pins the read-only,
// offline invariant of the cron plan-builder (PR 2A, mirror of
// emailplan_safety_test.go): no cronplan source may reference a cron
// write primitive or import a connecting package.
func TestCronPlanSourcesAreOfflineByConstruction(t *testing.T) {
	writeCalls := []string{
		"crontab -",
		"crontab -r",
		"InstallCrontab",
	}
	forbiddenImports := []string{
		"internal/sshx", "internal/cpanel", "internal/migrate",
		"golang.org/x/crypto/ssh", "\"net\"", "\"net/http\"",
	}
	files, err := filepath.Glob("cronplan*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no cronplan*.go files found — glob broken?")
	}
	for _, f := range files {
		if f == "cronplan_safety_test.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			code, _, _ := strings.Cut(strings.TrimSpace(line), "//")
			for _, verb := range writeCalls {
				if strings.Contains(code, verb) {
					t.Errorf("%s:%d references cron write call %q in code — the plan is offline",
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
