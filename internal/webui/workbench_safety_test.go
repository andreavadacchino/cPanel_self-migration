package webui_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWorkbenchUINoForbiddenImports ensures the webui package (which now
// includes workbench routes) never imports sshx or cpanel directly.
// The UI must remain offline — SSH connectivity is the CLI's domain.
func TestWorkbenchUINoForbiddenImports(t *testing.T) {
	forbidden := []string{
		"github.com/tis24dev/cPanel_self-migration/internal/sshx",
		"github.com/tis24dev/cPanel_self-migration/internal/cpanel",
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
					t.Errorf("%s imports forbidden package %q — UI must be offline", e.Name(), fb)
				}
			}
		}
	}
}

// TestWorkbenchUINoApplyVerbs ensures the workbench.go file (the new
// workbench handler) never contains SSH/cPanel write verbs or subprocess
// invocations with --apply/--cutover arguments.
func TestWorkbenchUINoApplyVerbs(t *testing.T) {
	forbidden := []string{
		"\"--apply\"",
		"exec.Command",
		"RunUAPI",
		"RunAPI2",
		"DialBoth",
		"DialDest",
		"mass_edit_zone",
	}

	content, err := os.ReadFile("workbench.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(content)
	for _, verb := range forbidden {
		if strings.Contains(src, verb) {
			t.Errorf("workbench.go contains forbidden verb %q — invariant: UI never executes apply", verb)
		}
	}
}
