package cpanel

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoCronWritePatterns asserts the source never edits, removes, installs
// or pipes into a crontab: the ONLY allowed invocation is `crontab -l`.
// It walks the whole module so future packages are covered too.
func TestNoCronWritePatterns(t *testing.T) {
	forbidden := []string{
		"crontab -e",
		"crontab -r",
		"crontab -i",
		"crontab <",
		"| crontab",
		"> crontab",
		"crontab /",
		"crontab $",
	}

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
		src := string(b)
		for _, pattern := range forbidden {
			if strings.Contains(src, pattern) {
				t.Errorf("%s contains forbidden cron write pattern %q", path, pattern)
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
