package cpanel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoDNSWriteFunctions(t *testing.T) {
	forbidden := []string{
		"mass_edit_zone",
		"swap_ip_in_zones",
		"/var/named",
		"add_zone_record",
		"edit_zone_record",
		"remove_zone_record",
	}

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
