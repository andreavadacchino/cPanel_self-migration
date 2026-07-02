package cpanel

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	goscanner "go/scanner"
	"go/token"
)

// emailWriteForbidden are the email-config write primitives no source may
// mention outside the allowlisted writer files: the 2B-1 UAPI writers plus
// the 2B-2/2B-3 primitives reserved for their own future PRs.
var emailWriteForbidden = []string{
	"add_forwarder",
	"set_default_address",
	"delete_forwarder",
	"add_auto_responder",
	"store_filter",
	"setmxcheck",
}

// emailWriteAllowlist names the ONLY files that may reference the email
// write verbs — the writer primitives and the `email apply` command that
// drives them (which also implements --rollback). This is the first
// per-file allowlist in a module-wide scan (the DNS scan has none yet;
// 6D will introduce its own the same way). Amending this list is a
// conscious, reviewed act: that is the point of the test.
var emailWriteAllowlist = map[string]bool{
	"internal/cpanel/email_apply.go":               true,
	"cmd/cpanel-self-migration/email_apply_cmd.go": true,
}

// TestNoEmailWritePatternsModuleWide extends the email write-verb scan to
// the whole module (mirror of TestNoDNSWritePatternsModuleWide): the plan
// builder, verify logic and every other source must never reference an
// email write primitive in code. Comments are exempt (design references
// legitimately NAME the API); the guarded property is that no code can
// CALL it, and a call requires the name in a string literal (RunUAPI
// argv) or an identifier — so the scan tokenizes each file and checks
// only those tokens.
//
// Known limit: a name built at runtime defeats a lexical scan by
// construction. TestDNSAPICallsUseLiteralNames closes that hole for the
// RunUAPI/RunAPI2/RunUAPIRaw entry points by REQUIRING literal
// module/function arguments — the concatenation itself becomes a failure.
func TestNoEmailWritePatternsModuleWide(t *testing.T) {
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
		if emailWriteAllowlist[filepath.ToSlash(rel)] {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		f := fset.AddFile(path, fset.Base(), len(b))
		var s goscanner.Scanner
		s.Init(f, b, nil, 0) // mode 0: comments are not returned
		for {
			pos, tok, lit := s.Scan()
			if tok == token.EOF {
				break
			}
			if tok != token.STRING && tok != token.CHAR && tok != token.IDENT {
				continue
			}
			for _, pattern := range emailWriteForbidden {
				if strings.Contains(lit, pattern) {
					t.Errorf("%s: %s token %q contains forbidden email write pattern %q (allowlist: email_apply.go + email_apply_cmd.go only)",
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

// TestEmailWriteAllowlistFilesExist pins the allowlist against silent rot:
// an allowlisted path that no longer exists means the writer moved and the
// scan is guarding the wrong file.
func TestEmailWriteAllowlistFilesExist(t *testing.T) {
	for rel := range emailWriteAllowlist {
		p := filepath.Join("../..", filepath.FromSlash(rel))
		if _, err := os.Stat(p); err != nil {
			t.Errorf("allowlisted file %s does not exist: %v", rel, err)
		}
	}
}
