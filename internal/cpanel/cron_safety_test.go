package cpanel

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cronWritePatternsForbidden are crontab write patterns. `crontab -r`
// is UNCONDITIONALLY forbidden (no allowlist). The pipe patterns
// (`| crontab`, `crontab <`) are allowed ONLY in the writer file
// (cron_apply.go) — the per-file allowlist was added consciously in 2A.
var cronWritePatternsForbidden = []string{
	"crontab -e",
	"crontab -i",
	"crontab <",
	"| crontab",
	"> crontab",
	"crontab /",
	"crontab $",
}

var cronWritePatternsNoAllowlist = []string{
	"crontab -r",
}

// cronWritePatternsAllowlist: ONLY files that may contain crontab write
// patterns. Amending this list is a conscious, reviewed act. Each entry
// must correspond to an existing file (a dangling entry would silently
// open a hole if someone later creates a file with that name).
var cronWritePatternsAllowlist = map[string]bool{
	"internal/cpanel/cron_apply.go": true,
}

// TestNoCronWritePatterns asserts crontab write patterns are absent from
// all source except the allowlisted writer files. `crontab -r` is
// forbidden EVERYWHERE (no allowlist — the tool never removes a crontab).
func TestNoCronWritePatterns(t *testing.T) {
	root := "../.."
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
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(b)
		for _, pattern := range cronWritePatternsNoAllowlist {
			if strings.Contains(src, pattern) {
				t.Errorf("%s contains unconditionally forbidden cron pattern %q", filepath.ToSlash(rel), pattern)
			}
		}
		if cronWritePatternsAllowlist[filepath.ToSlash(rel)] {
			return nil
		}
		for _, pattern := range cronWritePatternsForbidden {
			if strings.Contains(src, pattern) {
				t.Errorf("%s contains forbidden cron write pattern %q (allowlist: cron_apply.go only)",
					filepath.ToSlash(rel), pattern)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestCrontabScriptIsReadOnly pins the fetch script to the single read-only
// invocation: exactly one `crontab` occurrence, and it is `crontab -l`.
func TestCrontabScriptIsReadOnly(t *testing.T) {
	if got := strings.Count(crontabScript, "crontab"); got != 1 {
		t.Fatalf("crontabScript invokes crontab %d times, want exactly 1", got)
	}
	if !strings.Contains(crontabScript, "crontab -l") {
		t.Fatal("crontabScript must invoke `crontab -l`")
	}
}
