package accountinventory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDNSPlanSourcesContainNoWriteCalls pins the read-only invariant of
// the plan-builder line of work (PR 6B/6C): no dnsplan source may
// invoke a DNS write primitive in code (comments may name them for
// documentation). Only the future 6D apply files may. The cron safety
// test scans cron-specific literals and does NOT cover these — this is
// the DNS equivalent, per PR6A_DNS_IMPORT_DESIGN.md.
func TestDNSPlanSourcesContainNoWriteCalls(t *testing.T) {
	writeCalls := []string{
		"mass_edit_zone",
		"add_zone_record",
		"edit_zone_record",
		"remove_zone_record",
		"resetzone",
	}
	files, err := filepath.Glob("dnsplan*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no dnsplan*.go files found — glob broken?")
	}
	for _, f := range files {
		if f == "dnsplan_safety_test.go" {
			continue // this file names the verbs on purpose
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			code, _, _ := strings.Cut(strings.TrimSpace(line), "//")
			for _, verb := range writeCalls {
				if strings.Contains(code, verb) {
					t.Errorf("%s:%d references DNS write call %q in code — the plan line of work is read-only",
						f, i+1, verb)
				}
			}
		}
	}
}
