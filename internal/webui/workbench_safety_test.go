package webui_test

import (
	"go/ast"
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

// TestWorkbenchUINoApplyVerbs ensures the workbench.go file (the governance
// handler) never contains SSH/cPanel write verbs or subprocess invocations.
// NOTE: workbench_exec.go is ALLOWED to contain these (PR59 emendament) but
// is guarded by TestAllApplyVerbsRequireStrongConfirmation below.
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
			t.Errorf("workbench.go contains forbidden verb %q — invariant: governance handler never executes apply", verb)
		}
	}
}

// TestAllApplyVerbsRequireStrongConfirmation is the PR59 emendament guard:
// ANY non-test .go file in webui/ that contains the literal "--yes-apply-writes"
// or constructs argv with "--apply" MUST also call validateStrongConfirmation
// (or validateDoubleConfirmation) in the same file. This prevents bypass via
// indirect helper files.
func TestAllApplyVerbsRequireStrongConfirmation(t *testing.T) {
	dangerousLiterals := []string{
		`"--yes-apply-writes"`,
		`"--apply"`,
	}
	confirmationFuncs := []string{
		"validateStrongConfirmation",
		"validateDoubleConfirmation",
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
		// workbench.go is covered by TestWorkbenchUINoApplyVerbs (must have NONE)
		if e.Name() == "workbench.go" {
			continue
		}

		path := filepath.Join(dir, e.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		src := string(content)

		hasDangerous := false
		for _, lit := range dangerousLiterals {
			if strings.Contains(src, lit) {
				hasDangerous = true
				break
			}
		}
		if !hasDangerous {
			continue
		}

		// This file contains apply verbs — it MUST also reference the confirmation gate
		hasConfirmation := false
		for _, fn := range confirmationFuncs {
			if strings.Contains(src, fn) {
				hasConfirmation = true
				break
			}
		}
		if !hasConfirmation {
			t.Errorf("%s contains apply verb literals but does NOT call validateStrongConfirmation — "+
				"PR59 emendament requires strong confirmation on the mandatory path", e.Name())
		}
	}
}

// TestExecLauncherConfirmationBeforeArgv verifies via AST that in
// workbench_exec.go, the handleExecRedirect function — the REAL exec launcher,
// which handleExec merely delegates to — calls validateStrongConfirmation (or
// validateDoubleConfirmation) BEFORE calling buildArgv. This pins the security
// gate to the actual execution path (not a decorative wrapper), so a code
// reordering can never launch a write subprocess before the confirmation gate.
func TestExecLauncherConfirmationBeforeArgv(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "workbench_exec.go", nil, 0)
	if err != nil {
		t.Fatalf("parse workbench_exec.go: %v", err)
	}

	// Find the handleExecRedirect function (the real launcher).
	var handleExecBody *ast.BlockStmt
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Name.Name == "handleExecRedirect" {
			handleExecBody = fn.Body
			return false
		}
		return true
	})
	if handleExecBody == nil {
		t.Fatal("handleExecRedirect function not found in workbench_exec.go")
	}

	// Walk the body statements and find positions of key calls
	confirmPos := -1
	buildArgvPos := -1
	for i, stmt := range handleExecBody.List {
		src := nodeSource(fset, stmt)
		if strings.Contains(src, "validateStrongConfirmation") || strings.Contains(src, "validateDoubleConfirmation") {
			if confirmPos == -1 {
				confirmPos = i
			}
		}
		if strings.Contains(src, "buildArgv") {
			if buildArgvPos == -1 {
				buildArgvPos = i
			}
		}
	}

	if confirmPos == -1 {
		t.Fatal("handleExecRedirect does not call validateStrongConfirmation or validateDoubleConfirmation")
	}
	if buildArgvPos == -1 {
		t.Fatal("handleExecRedirect does not call buildArgv")
	}
	if confirmPos >= buildArgvPos {
		t.Errorf("confirmation (stmt %d) must appear BEFORE buildArgv (stmt %d) — security gate ordering violated", confirmPos, buildArgvPos)
	}
}

// nodeSource returns a rough string representation of an AST node for
// substring matching. Uses the file set to get the source position range,
// then reads the file bytes. For simplicity, we use ast.Inspect to collect
// identifiers.
func nodeSource(fset *token.FileSet, node ast.Node) string {
	var idents []string
	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			idents = append(idents, x.Name)
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok {
				idents = append(idents, id.Name+"."+x.Sel.Name)
			}
		}
		return true
	})
	return strings.Join(idents, " ")
}
