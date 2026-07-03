package main

// Dispatch contract: `inventory` with a missing or unknown subcommand must
// be refused with exit 2 BEFORE the migration flag parsing ever runs.
// Before this guard existed, `inventory polcy` (typo) and a bare
// `inventory` fell through to the migration flow and, with a resolvable
// host.yaml, started a full migration dry-run — the same footgun class the
// `dns` namespace already refuses with exit 2 (PR 6C).
//
// These tests exercise the REAL main() dispatch: TestMain re-execs this
// test binary with CPSM_DISPATCH_TEST_CHILD=1 so every os.Exit happens in
// a child process. The child runs in an empty working directory, so no
// host.yaml is ever resolvable even if the fall-through regressed.

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("CPSM_DISPATCH_TEST_CHILD") == "1" {
		main() // every dispatch path below ends in os.Exit
		return
	}
	os.Exit(m.Run())
}

// runDispatchChild re-execs this test binary as `cpanel-self-migration
// <args…>` in a fresh temp dir and returns its exit code and stderr.
//
// The child inherits os.Environ(): safe today because no test in this
// package uses t.Parallel(), so the PATH-stubbing tests (stub uapi/cpapi2
// via t.Setenv) can never interleave with a re-exec. If t.Parallel() is
// ever introduced here, pin PATH explicitly in cmd.Env.
func runDispatchChild(t *testing.T, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "CPSM_DISPATCH_TEST_CHILD=1")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return 0, stderr.String()
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("re-exec %v: %v", args, err)
	}
	return exitErr.ExitCode(), stderr.String()
}

func TestDispatchInventoryRefusesUnknownAndBare(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"typo subcommand", []string{"inventory", "polcy"}},
		{"bare inventory", []string{"inventory"}},
		{"help is not a subcommand", []string{"inventory", "--help"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stderr := runDispatchChild(t, tc.args...)
			if code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr:\n%s", code, stderr)
			}
			if !strings.Contains(stderr, "usage: cpanel-self-migration inventory") {
				t.Errorf("stderr missing inventory usage line:\n%s", stderr)
			}
			// Reaching the migration flow would surface its config
			// resolution error instead of the dispatch usage.
			if strings.Contains(stderr, "host.yaml") || strings.Contains(stderr, "--config") {
				t.Errorf("stderr suggests the migration flow ran:\n%s", stderr)
			}
		})
	}
}

// Known subcommands must still route: `inventory diff` without flags is
// the subcommand's own input error (exit 1 + its message), NOT the
// dispatch usage (exit 2) and NOT the migration flow.
func TestDispatchInventoryKnownSubcommandStillRoutes(t *testing.T) {
	code, stderr := runDispatchChild(t, "inventory", "diff")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (diff's own input error); stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "--source and --destination are required") {
		t.Errorf("stderr is not the diff subcommand's error:\n%s", stderr)
	}
}

// Regression lock for the PR 6C behavior this fix mirrors: an unknown
// `dns` subcommand exits 2 with the dns usage.
func TestDispatchDNSRefusesUnknown(t *testing.T) {
	code, stderr := runDispatchChild(t, "dns", "bogus")
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "usage: cpanel-self-migration dns") {
		t.Errorf("stderr missing dns usage line:\n%s", stderr)
	}
}

// The `cron` namespace mirrors `dns` and `email`: an unknown or missing
// subcommand exits 2 with the cron usage, never falling through to the
// migration flow.
func TestDispatchCronRefusesUnknown(t *testing.T) {
	for _, args := range [][]string{{"cron", "bogus"}, {"cron"}} {
		code, stderr := runDispatchChild(t, args...)
		if code != 2 {
			t.Fatalf("%v: exit code = %d, want 2; stderr:\n%s", args, code, stderr)
		}
		if !strings.Contains(stderr, "usage: cpanel-self-migration cron <apply|verify>") {
			t.Errorf("%v: stderr missing cron usage line:\n%s", args, stderr)
		}
	}
}

// The `email` namespace (PR 2B-1) has the same contract: an unknown or
// missing subcommand exits 2 with the email usage, never falling through
// to the migration flow.
func TestDispatchEmailRefusesUnknown(t *testing.T) {
	for _, args := range [][]string{{"email", "bogus"}, {"email"}} {
		code, stderr := runDispatchChild(t, args...)
		if code != 2 {
			t.Fatalf("%v: exit code = %d, want 2; stderr:\n%s", args, code, stderr)
		}
		if !strings.Contains(stderr, "usage: cpanel-self-migration email <apply|verify>") {
			t.Errorf("%v: stderr missing email usage line:\n%s", args, stderr)
		}
	}
}
