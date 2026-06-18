package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateScopeFilters(t *testing.T) {
	cases := []struct {
		name                    string
		onlyDomain, onlyMailbox string
		mail, file, db          bool
		wantErr                 string // substring; "" means no error
	}{
		// Valid combinations.
		{"no filters", "", "", false, false, false, ""},
		{"domain bare", "tissolution.it", "", false, false, false, ""},
		{"domain + mail", "tissolution.it", "", true, false, false, ""},
		{"domain + file", "tissolution.it", "", false, true, false, ""},
		{"mailbox", "", "info@tissolution.it", false, false, false, ""},
		{"mailbox + mail (redundant)", "", "info@tissolution.it", true, false, false, ""},

		// Illegal combinations.
		{"mailbox + domain", "tissolution.it", "info@tissolution.it", false, false, false, "mutually exclusive"},
		{"mailbox + file", "", "info@tissolution.it", false, true, false, "mail-only"},
		{"mailbox + db", "", "info@tissolution.it", false, false, true, "mail-only"},
		{"domain + db", "tissolution.it", "", false, false, true, "does not support databases"},

		// Malformed values.
		{"mailbox no at", "", "noat", false, false, false, "must be local@domain"},
		{"mailbox empty domain", "", "info@", false, false, false, "must be local@domain"},
		{"mailbox empty local", "", "@tissolution.it", false, false, false, "must be local@domain"},
		{"mailbox traversal local", "", "..@tissolution.it", false, false, false, "invalid --mailbox"},
		{"mailbox bad domain", "", "info@bad/domain", false, false, false, "invalid --mailbox"},
		{"domain bad char", "bad/domain", "", false, false, false, "invalid --domain"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateScopeFilters(c.onlyDomain, c.onlyMailbox, c.mail, c.file, c.db)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("got error %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("got error %v, want substring %q", err, c.wantErr)
			}
		})
	}
}

// TestResolveConfigPathExplicit: an explicit --config is returned verbatim with no
// discovery and no alternates.
func TestResolveConfigPathExplicit(t *testing.T) {
	p, alt, err := resolveConfigPath("/some/explicit.yaml")
	if err != nil || p != "/some/explicit.yaml" || alt != nil {
		t.Fatalf("explicit: got (%q, %v, %v), want (/some/explicit.yaml, nil, nil)", p, alt, err)
	}
}

// TestResolveConfigPathNotFound: discovery with no host.yaml anywhere is an error.
func TestResolveConfigPathNotFound(t *testing.T) {
	t.Chdir(t.TempDir())
	if p, alt, err := resolveConfigPath(""); err == nil {
		t.Fatalf("expected an error when no host.yaml exists, got (%q, %v)", p, alt)
	}
}

// TestResolveConfigPathReportsAlternates (M9): when more than one distinct host.yaml
// is discoverable, the first is chosen and the rest are returned so the caller can
// warn (a stale config silently shadowing the intended one is a foot-gun). Within a
// base, configs/host.yaml is searched before the bare host.yaml.
func TestResolveConfigPathReportsAlternates(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "configs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "configs", "host.yaml"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	p, alt, err := resolveConfigPath("")
	if err != nil {
		t.Fatalf("resolveConfigPath: %v", err)
	}
	if filepath.Base(filepath.Dir(p)) != "configs" {
		t.Errorf("chosen path = %q, want configs/host.yaml (searched first within a base)", p)
	}
	if len(alt) != 1 || filepath.Base(alt[0]) != "host.yaml" || filepath.Dir(alt[0]) != "." {
		t.Errorf("alternates = %v, want [host.yaml]", alt)
	}
}

// TestResolveConfigPathSingleNoAlternates: exactly one discoverable host.yaml yields
// no alternates (no spurious ambiguity warning).
func TestResolveConfigPathSingleNoAlternates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "host.yaml"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	p, alt, err := resolveConfigPath("")
	if err != nil {
		t.Fatalf("resolveConfigPath: %v", err)
	}
	if filepath.Base(p) != "host.yaml" {
		t.Errorf("chosen path = %q, want host.yaml", p)
	}
	if len(alt) != 0 {
		t.Errorf("alternates = %v, want none", alt)
	}
}
