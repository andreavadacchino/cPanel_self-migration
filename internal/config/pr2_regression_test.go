package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDestIntendedPortTimeout guards the fix where a dest block with only port/timeout
// set (connection fields forgotten) was silently treated as source-only.
func TestDestIntendedPortTimeout(t *testing.T) {
	if !(Config{Dest: HostConfig{Port: 22}}).destIntended() {
		t.Error("dest with only port set must be intended")
	}
	if !(Config{Dest: HostConfig{Timeout: time.Second}}).destIntended() {
		t.Error("dest with only timeout set must be intended")
	}
	if (Config{}).destIntended() {
		t.Error("a fully blank dest must NOT be intended (source-only)")
	}

	dir := t.TempDir()
	src := "src:\n  ip: \"1.2.3.4\"\n  port: 22\n  ssh_user: u\n  ssh_pass: p\n  timeout: 10s\n"
	p := filepath.Join(dir, "only-port.yaml")
	if err := os.WriteFile(p, []byte(src+"dest:\n  port: 22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Error("dest with only port set must error, not be silently source-only")
	}
	p2 := filepath.Join(dir, "blank-dest.yaml")
	if err := os.WriteFile(p2, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg, err := Load(p2); err != nil || cfg.DestConfigured() {
		t.Errorf("blank dest should load source-only: err=%v configured=%v", err, cfg.DestConfigured())
	}
}
