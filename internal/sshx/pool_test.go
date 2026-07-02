package sshx

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
)

// fastDial disables the inter-attempt backoff for the duration of a test, so the
// now-retried connect-failure paths stay quick. Restored on cleanup.
func fastDial(t *testing.T) {
	t.Helper()
	orig := RetryBackoffBase
	RetryBackoffBase = 0
	t.Cleanup(func() { RetryBackoffBase = orig })
}

func TestDialBothSourceOnly(t *testing.T) {
	addr := newCmdServer(t, true, okHandler)
	cfg := config.Config{Src: cfgToHost(t, addr)} // dest blank -> source-only
	kh := filepath.Join(t.TempDir(), "known_hosts")

	pool, err := DialBoth(context.Background(), cfg, kh)
	if err != nil {
		t.Fatalf("DialBoth: %v", err)
	}
	defer pool.Close()
	if pool.Src == nil {
		t.Error("Src must be connected")
	}
	if pool.Dest != nil {
		t.Error("Dest must be nil when not configured")
	}
}

func TestDialBothSourceAndDest(t *testing.T) {
	addr := newCmdServer(t, true, okHandler)
	host := cfgToHost(t, addr)
	cfg := config.Config{Src: host, Dest: host} // both point at the test server
	kh := filepath.Join(t.TempDir(), "known_hosts")

	pool, err := DialBoth(context.Background(), cfg, kh)
	if err != nil {
		t.Fatalf("DialBoth: %v", err)
	}
	if pool.Src == nil || pool.Dest == nil {
		t.Fatal("both Src and Dest must be connected")
	}
	if err := pool.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// An empty knownHostsPath defaults to ~/.ssh/known_hosts; HOME is redirected to a
// temp dir so the test never touches the real one.
func TestDialBothDefaultKnownHostsUsesHome(t *testing.T) {
	addr := newCmdServer(t, true, okHandler)
	cfg := config.Config{Src: cfgToHost(t, addr)}
	t.Setenv("HOME", t.TempDir())

	pool, err := DialBoth(context.Background(), cfg, "")
	if err != nil {
		t.Fatalf("DialBoth: %v", err)
	}
	defer pool.Close()
	if pool.Src == nil {
		t.Error("Src must be connected")
	}
}

func TestDialBothSourceUnreachable(t *testing.T) {
	fastDial(t)
	cfg := config.Config{Src: config.HostConfig{
		IP: "127.0.0.1", Port: refusedPort(t), SSHUser: "u", SSHPass: "p", Timeout: time.Second,
	}}
	kh := filepath.Join(t.TempDir(), "known_hosts")
	if _, err := DialBoth(context.Background(), cfg, kh); err == nil {
		t.Error("DialBoth must fail when the source is unreachable")
	}
}

// When the source connects but the dest does not, DialBoth must close the source
// and return the error (no leaked connection).
func TestDialBothDestUnreachable(t *testing.T) {
	fastDial(t)
	addr := newCmdServer(t, true, okHandler)
	cfg := config.Config{
		Src:  cfgToHost(t, addr),
		Dest: config.HostConfig{IP: "127.0.0.1", Port: refusedPort(t), SSHUser: "u", SSHPass: "p", Timeout: time.Second},
	}
	kh := filepath.Join(t.TempDir(), "known_hosts")
	if _, err := DialBoth(context.Background(), cfg, kh); err == nil {
		t.Error("DialBoth must fail when the dest is unreachable")
	}
}

// S1-02 leg 2: a pre-cancelled context must make DialBoth fail BEFORE any
// filesystem/TOFU side effect — no known_hosts dir or file is created.
func TestDialBothPreCancelNoSideEffects(t *testing.T) {
	addr := newCmdServer(t, true, okHandler)
	kh := filepath.Join(t.TempDir(), "ssh", "known_hosts")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DialBoth(ctx, config.Config{Src: cfgToHost(t, addr)}, kh); err == nil {
		t.Fatal("DialBoth with a pre-cancelled context must fail")
	}
	if _, err := os.Stat(filepath.Dir(kh)); !os.IsNotExist(err) {
		t.Errorf("pre-cancelled DialBoth must not create the known_hosts dir (stat err=%v)", err)
	}
	if _, err := os.Stat(kh); !os.IsNotExist(err) {
		t.Errorf("pre-cancelled DialBoth must not create the known_hosts file (stat err=%v)", err)
	}
}

// DialDest (PR 6C) opens the destination connection ONLY: `dns verify`
// re-fetches destination zones after a migration, when the source server
// may already be decommissioned — DialBoth would dial it first and fail.
func TestDialDestConnectsWithoutSource(t *testing.T) {
	addr := newCmdServer(t, true, okHandler)
	cfg := config.Config{Dest: cfgToHost(t, addr)} // src BLANK on purpose
	kh := filepath.Join(t.TempDir(), "known_hosts")

	c, err := DialDest(context.Background(), cfg, kh)
	if err != nil {
		t.Fatalf("DialDest: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestDialDestNotConfigured(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	if _, err := DialDest(context.Background(), config.Config{}, kh); err == nil {
		t.Fatal("DialDest with a blank destination must fail")
	}
}

// A pre-cancelled context must fail before any filesystem/TOFU side effect
// (same guard as DialBoth).
func TestDialDestPreCancelledContext(t *testing.T) {
	addr := newCmdServer(t, true, okHandler)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	khDir := filepath.Join(t.TempDir(), "fresh")
	kh := filepath.Join(khDir, "known_hosts")

	if _, err := DialDest(ctx, config.Config{Dest: cfgToHost(t, addr)}, kh); err == nil {
		t.Fatal("pre-cancelled context must fail")
	}
	if _, statErr := os.Stat(khDir); !os.IsNotExist(statErr) {
		t.Error("a cancelled dial must not create known_hosts side effects")
	}
}
