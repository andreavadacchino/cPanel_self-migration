package cpanel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoCronWritePatterns asserts the source never edits, removes, installs
// or pipes into a crontab: the ONLY allowed invocation is `crontab -l`.
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

	for _, dir := range []string{".", "../accountinventory"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
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
					t.Errorf("%s contains forbidden cron write pattern %q", f, pattern)
				}
			}
		}
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
