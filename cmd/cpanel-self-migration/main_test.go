package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
