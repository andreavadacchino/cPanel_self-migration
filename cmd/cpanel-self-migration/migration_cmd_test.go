package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/workbench"
)

// runMigrationChild runs the binary with migration subcommand in an isolated
// home directory and returns exit code, stdout, and stderr.
func runMigrationChild(t *testing.T, homeDir string, args ...string) (int, string, string) {
	t.Helper()
	fullArgs := append([]string{"migration"}, args...)
	cmd := exec.Command(os.Args[0], fullArgs...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"CPSM_DISPATCH_TEST_CHILD=1",
		"CPANEL_MIGRATION_HOME="+homeDir,
	)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), stdout.String(), stderr.String()
	}
	t.Fatalf("exec error: %v", err)
	return -1, "", ""
}

func TestDispatchMigrationRefusesUnknownAndBare(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		name string
		args []string
	}{
		{"bare migration", nil},
		{"unknown subcommand", []string{"bogus"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runMigrationChild(t, home, tc.args...)
			if code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr:\n%s", code, stderr)
			}
			if !strings.Contains(stderr, "usage: cpanel-self-migration migration") {
				t.Errorf("stderr missing usage line:\n%s", stderr)
			}
		})
	}
}

func TestMigrationInitAndShow(t *testing.T) {
	home := t.TempDir()

	// Init
	code, stdout, stderr := runMigrationChild(t, home, "init",
		"--name", "testmig",
		"--source-profile", "old193",
		"--destination-profile", "new78",
		"--json")
	if code != 0 {
		t.Fatalf("init exit %d; stderr:\n%s", code, stderr)
	}

	var sess workbench.Session
	if err := json.Unmarshal([]byte(stdout), &sess); err != nil {
		t.Fatalf("parse init output: %v\nstdout: %s", err, stdout)
	}
	if sess.Name != "testmig" {
		t.Errorf("name = %q", sess.Name)
	}
	if sess.Status != workbench.StatusDraft {
		t.Errorf("status = %q", sess.Status)
	}

	// Show
	code, stdout, stderr = runMigrationChild(t, home, "show", sess.ID, "--json")
	if code != 0 {
		t.Fatalf("show exit %d; stderr:\n%s", code, stderr)
	}
	var shown workbench.Session
	if err := json.Unmarshal([]byte(stdout), &shown); err != nil {
		t.Fatalf("parse show output: %v", err)
	}
	if shown.ID != sess.ID {
		t.Error("show ID mismatch")
	}
}

func TestMigrationList(t *testing.T) {
	home := t.TempDir()

	// Create two sessions
	runMigrationChild(t, home, "init", "--name", "a", "--source-profile", "s", "--destination-profile", "d")
	runMigrationChild(t, home, "init", "--name", "b", "--source-profile", "s", "--destination-profile", "d")

	code, stdout, stderr := runMigrationChild(t, home, "list", "--json")
	if code != 0 {
		t.Fatalf("list exit %d; stderr:\n%s", code, stderr)
	}

	var sessions []workbench.Session
	if err := json.Unmarshal([]byte(stdout), &sessions); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("len = %d, want 2", len(sessions))
	}
}

func TestMigrationSetStatus(t *testing.T) {
	home := t.TempDir()

	code, stdout, _ := runMigrationChild(t, home, "init", "--name", "x",
		"--source-profile", "s", "--destination-profile", "d", "--json")
	if code != 0 {
		t.Fatal("init failed")
	}
	var sess workbench.Session
	json.Unmarshal([]byte(stdout), &sess)

	// Valid transition: draft → preflight_required
	code, _, stderr := runMigrationChild(t, home, "set-status", sess.ID, "--status", "preflight_required")
	if code != 0 {
		t.Fatalf("set-status exit %d; stderr:\n%s", code, stderr)
	}

	// Invalid status
	code, _, _ = runMigrationChild(t, home, "set-status", sess.ID, "--status", "bogus")
	if code != 2 {
		t.Errorf("invalid status: exit %d, want 2", code)
	}
}

func TestMigrationSetStatusIllegalTransition(t *testing.T) {
	home := t.TempDir()

	code, stdout, _ := runMigrationChild(t, home, "init", "--name", "x",
		"--source-profile", "s", "--destination-profile", "d", "--json")
	if code != 0 {
		t.Fatal("init failed")
	}
	var sess workbench.Session
	json.Unmarshal([]byte(stdout), &sess)

	// draft → cutover_done is illegal
	code, _, stderr := runMigrationChild(t, home, "set-status", sess.ID, "--status", "cutover_done")
	if code != 1 {
		t.Fatalf("illegal transition: exit %d, want 1; stderr:\n%s", code, stderr)
	}
}

func TestMigrationAttachArtifact(t *testing.T) {
	home := t.TempDir()

	code, stdout, _ := runMigrationChild(t, home, "init", "--name", "x",
		"--source-profile", "s", "--destination-profile", "d", "--json")
	if code != 0 {
		t.Fatal("init failed")
	}
	var sess workbench.Session
	json.Unmarshal([]byte(stdout), &sess)

	// Create a temp artifact
	artFile := filepath.Join(t.TempDir(), "plan.json")
	os.WriteFile(artFile, []byte(`{"plan":"ok"}`), 0644)

	code, _, stderr := runMigrationChild(t, home, "attach-artifact", sess.ID,
		"--kind", "dns_plan", "--path", artFile)
	if code != 0 {
		t.Fatalf("attach exit %d; stderr:\n%s", code, stderr)
	}

	// Unknown kind → exit 2
	code, _, _ = runMigrationChild(t, home, "attach-artifact", sess.ID,
		"--kind", "host_yaml", "--path", artFile)
	if code != 2 {
		t.Errorf("unknown kind: exit %d, want 2", code)
	}
}

func TestMigrationArchive(t *testing.T) {
	home := t.TempDir()

	code, stdout, _ := runMigrationChild(t, home, "init", "--name", "x",
		"--source-profile", "s", "--destination-profile", "d", "--json")
	if code != 0 {
		t.Fatal("init failed")
	}
	var sess workbench.Session
	json.Unmarshal([]byte(stdout), &sess)

	// Draft cannot directly archive (terminal only from cutover_done/blocked/failed)
	code, _, _ = runMigrationChild(t, home, "archive", sess.ID)
	if code != 1 {
		t.Errorf("archive from draft: exit %d, want 1", code)
	}

	// Force to blocked, then archive
	runMigrationChild(t, home, "set-status", sess.ID,
		"--status", "blocked", "--force", "--reason", "external dependency blocks progress")
	code, _, stderr := runMigrationChild(t, home, "archive", sess.ID)
	if code != 0 {
		t.Fatalf("archive from blocked: exit %d; stderr:\n%s", code, stderr)
	}
}

func TestMigrationShowMissing(t *testing.T) {
	home := t.TempDir()
	code, _, _ := runMigrationChild(t, home, "show", "nonexistent")
	if code != 1 {
		t.Errorf("missing session: exit %d, want 1", code)
	}
}

func TestMigrationInitMissingFlags(t *testing.T) {
	home := t.TempDir()
	code, _, stderr := runMigrationChild(t, home, "init", "--name", "x")
	if code != 2 {
		t.Fatalf("missing flags: exit %d, want 2; stderr:\n%s", code, stderr)
	}
}
