package cpanel

import (
	goscanner "go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dnsWriteForbidden are the DNS write primitives no read-only source may
// mention: UAPI mass_edit_zone / swap_ip_in_zones, the API2 ZoneEdit
// record writers, and raw zone-file paths. PR 6D (dns apply — the only
// writer) will have to consciously amend this list with an explicit
// allowlist for its own files; that is the point of the test.
var dnsWriteForbidden = []string{
	"mass_edit_zone",
	"swap_ip_in_zones",
	"/var/named",
	"add_zone_record",
	"edit_zone_record",
	"remove_zone_record",
}

func TestNoDNSWriteFunctions(t *testing.T) {
	forbidden := dnsWriteForbidden

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		src := string(b)
		for _, pattern := range forbidden {
			if strings.Contains(src, pattern) {
				t.Errorf("%s contains forbidden DNS write pattern %q", f, pattern)
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
func TestNoDNSWritePatternsModuleWide(t *testing.T) {
	root := "../.." // module root from internal/cpanel
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
			for _, pattern := range dnsWriteForbidden {
				if strings.Contains(lit, pattern) {
					t.Errorf("%s: %s token %q contains forbidden DNS write pattern %q",
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
