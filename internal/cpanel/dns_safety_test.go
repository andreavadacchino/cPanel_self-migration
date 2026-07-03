package cpanel

import (
	"fmt"
	"go/ast"
	"go/parser"
	goscanner "go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dnsWriteForbidden are the DNS write primitives no source may mention
// outside the allowlisted writer files: UAPI mass_edit_zone /
// swap_ip_in_zones, the API2 ZoneEdit record writers, and raw zone-file
// paths. PR 6D consciously adds the first per-file allowlist.
var dnsWriteForbidden = []string{
	"mass_edit_zone",
	"swap_ip_in_zones",
	"/var/named",
	"add_zone_record",
	"edit_zone_record",
	"remove_zone_record",
}

// dnsWriteAllowlist names the ONLY files that may reference the DNS
// write verbs — the writer primitives file. When the `dns apply`
// command file is created, it must be added here in the same PR.
// Amending this list is a conscious, reviewed act.
var dnsWriteAllowlist = map[string]bool{
	"internal/cpanel/dns_apply.go":               true,
	"cmd/cpanel-self-migration/dns_apply_cmd.go": true,
}

func TestNoDNSWriteFunctions(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		if dnsWriteAllowlist["internal/cpanel/"+f] {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		src := string(b)
		for _, pattern := range dnsWriteForbidden {
			if strings.Contains(src, pattern) {
				t.Errorf("%s contains forbidden DNS write pattern %q (allowlist: dns_apply.go only)", f, pattern)
			}
		}
	}
}

// TestNoDNSWritePatternsModuleWide extends the scan to the whole module
// (cron_safety_test.go pattern): the dns-plan and dns verify sources live
// in internal/accountinventory and cmd/, outside this package's glob.
// Required by the PR 6A design ("dns-plan and dns verify sources contain
// no write calls").
//
// Comments are exempt: design references legitimately NAME the write API
// (dnsplan.go documents the mass-edit shape 6D will target). The guarded
// property is that no code can CALL it, and a call requires the name in a
// string literal (RunUAPI/cpapi2 argv) or an identifier — so the scan
// tokenizes each file and checks only those tokens.
//
// Known limit: a name built at runtime ("mass_"+"edit_zone") defeats a
// lexical scan by construction. TestDNSAPICallsUseLiteralNames below
// closes that hole for the cPanel API entry points by REQUIRING literal
// module/function arguments — the concatenation itself becomes a failure.
func TestNoDNSWritePatternsModuleWide(t *testing.T) {
	root := "../.." // module root from internal/cpanel
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(rootAbs, abs)
		if err != nil {
			return err
		}
		if dnsWriteAllowlist[filepath.ToSlash(rel)] {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		f := fset.AddFile(path, fset.Base(), len(b))
		var s goscanner.Scanner
		s.Init(f, b, nil, 0)
		for {
			pos, tok, lit := s.Scan()
			if tok == token.EOF {
				break
			}
			if tok != token.STRING && tok != token.CHAR && tok != token.IDENT {
				continue
			}
			for _, pattern := range dnsWriteForbidden {
				if strings.Contains(lit, pattern) {
					t.Errorf("%s: %s token %q contains forbidden DNS write pattern %q (allowlist: dns_apply.go only)",
						fset.Position(pos), tok, lit, pattern)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestDNSAPICallsUseLiteralNames is the structural companion of the
// lexical scans (go-reviewer finding on PR 6C): every RunUAPI/RunAPI2
// call in the module must pass its module and function names as PLAIN
// STRING LITERALS, so the forbidden-pattern scan above is guaranteed to
// see them. A dynamically built name (concatenation, Sprintf, variable)
// fails here regardless of its value — the bypass itself is the error.
//
// Residual limit, accepted: a writer that avoids RunUAPI/RunAPI2 entirely
// (a hand-built `uapi …` script through Runner.RunScript) is not caught
// by this test; new RunScript call sites remain a human-review surface.
func TestDNSAPICallsUseLiteralNames(t *testing.T) {
	root := "../.." // module root from internal/cpanel
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := calleeBaseName(call.Fun)
			// RunUAPIRaw (PR 2B-1) is the same executor returning also the
			// raw bytes — same literal-name contract.
			if name != "RunUAPI" && name != "RunAPI2" && name != "RunUAPIRaw" {
				return true
			}
			if len(call.Args) < 4 {
				return true // not the (ctx, runner, module, fn, args) shape
			}
			for i, arg := range call.Args[2:4] {
				if lit, ok := arg.(*ast.BasicLit); !ok || lit.Kind != token.STRING {
					t.Errorf("%s: %s call builds its %s name dynamically — the DNS write scan cannot vet it, use a string literal",
						fset.Position(call.Pos()), name, []string{"module", "function"}[i])
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestDNSWriteAllowlistFilesExist pins the allowlist against silent rot:
// an allowlisted path that no longer exists means the writer moved and
// the scan is guarding the wrong file (mirror of email test).
func TestDNSWriteAllowlistFilesExist(t *testing.T) {
	for rel := range dnsWriteAllowlist {
		p := filepath.Join("../..", filepath.FromSlash(rel))
		if _, err := os.Stat(p); err != nil {
			t.Errorf("allowlisted file %s does not exist: %v", rel, err)
		}
	}
}

// calleeBaseName unwraps generic instantiation (RunUAPI[T]) and package
// selectors (cpanel.RunUAPI) down to the called identifier.
func calleeBaseName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	case *ast.IndexExpr:
		return calleeBaseName(v.X)
	case *ast.IndexListExpr:
		return calleeBaseName(v.X)
	}
	return ""
}
