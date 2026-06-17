package dbmig

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// These tests pin the pipefail contract of the DB import pipeline. importCmd is
// `bash -c 'set -o pipefail; sed ... | mysql ...'`; without pipefail the pipeline's
// status is mysql's (the rightmost stage), so a `sed` that died mid-stream while
// mysql read the truncated stream to a statement boundary and exited 0 would report
// a PARTIAL import as success. They drive the REAL CopyDatabase against the real
// bash exec server (sshtest), so `set -o pipefail` is actually executed.

// writeFake creates an executable script named name in dir.
func writeFake(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil { // #nosec G306 -- test-only fake binary must be executable
		t.Fatal(err)
	}
}

// installFakeDBBinaries puts fake mysqldump/sed/mysql on a temp dir prepended to
// PATH (so they shadow any real tools the exec server would otherwise find) and
// exports SENTINEL_DIR so each fake can drop a marker proving it actually ran. The
// fake mysqldump (source) emits a few bytes and exits 0; the fake mysql (dest, last
// stage) drains stdin and exits 0; the sed body is the caller's, selecting the
// first-stage behavior under test. Returns the sentinel dir. t.Setenv pins the test
// to run serially, so the prepended PATH never bleeds into a parallel test.
func installFakeDBBinaries(t *testing.T, sedBody string) string {
	t.Helper()
	bin := t.TempDir()
	sentinel := t.TempDir()

	writeFake(t, bin, "mysqldump", `#!/bin/sh
echo ran > "$SENTINEL_DIR/dump.ran"
printf '/*!40000 ALTER TABLE t DISABLE KEYS */;\nINSERT INTO t VALUES (1);\n'
exit 0
`)
	writeFake(t, bin, "sed", sedBody)
	writeFake(t, bin, "mysql", `#!/bin/sh
echo ran > "$SENTINEL_DIR/mysql.ran"
cat >/dev/null
exit 0
`)

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SENTINEL_DIR", sentinel)
	return sentinel
}

// sedFail: the first pipeline stage consumes a little stdin then exits non-zero —
// the masking scenario (a killed/OOM sed) the fix must surface.
const sedFail = `#!/bin/sh
echo ran > "$SENTINEL_DIR/sed.ran"
head -c 16 >/dev/null 2>&1
exit 1
`

// sedPass: the first stage forwards the stream unchanged and exits 0 — a healthy
// import that must still succeed under pipefail.
const sedPass = `#!/bin/sh
echo ran > "$SENTINEL_DIR/sed.ran"
cat
exit 0
`

// TestImportPipelineSurfacesSedFailure is the decisive regression test: the source
// dump streams fine, the dest sed (first stage) exits non-zero, the dest mysql (last
// stage) drains and exits 0. WITHOUT pipefail the pipeline status is mysql's 0 and
// CopyDatabase falsely reports success; WITH the fix the sed failure surfaces as an
// import error. This FAILS on reverted code, proving it is non-vacuous.
func TestImportPipelineSurfacesSedFailure(t *testing.T) {
	sshtest.RequireTools(t, "bash", "head", "cat")
	sentinel := installFakeDBBinaries(t, sedFail)

	origBackoff := sshx.RetryBackoffBase
	sshx.RetryBackoffBase = 0 // CopyDatabase retries the bridge; keep the failure path fast
	t.Cleanup(func() { sshx.RetryBackoffBase = origBackoff })

	src := sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))
	defer dst.Close()

	tr := Transfer{Src: src, Dest: dst, Timeout: 10 * time.Second}
	_, err := tr.CopyDatabase(bg, DBPlanItem{SrcDB: "db", DestDB: "vh_db"}, "du", "dp", nil)

	// Prove the fakes actually ran on BOTH sides, so an error (or success) below is
	// the pipefail outcome and not a command-not-found masquerade.
	if _, e := os.Stat(filepath.Join(sentinel, "dump.ran")); e != nil {
		t.Fatalf("fake mysqldump never ran (PATH not honored on source): %v", e)
	}
	if _, e := os.Stat(filepath.Join(sentinel, "sed.ran")); e != nil {
		t.Fatalf("fake sed never ran (PATH not honored on dest): %v", e)
	}
	if err == nil {
		t.Fatal("CopyDatabase reported SUCCESS while sed (first pipeline stage) exited non-zero — " +
			"the import is not running under `set -o pipefail` (a partial import would pass silently)")
	}
	if se := sshx.SideError(err, sshx.SideDest); se == nil {
		t.Errorf("a dest-side import failure must be tagged SideDest, got %v", err)
	}
}

// TestImportPipelineSucceedsOnHealthyStream guards against the fix making good
// imports fail: sed forwards the stream and exits 0, mysql drains and exits 0, so
// CopyDatabase must succeed and report bytes streamed.
func TestImportPipelineSucceedsOnHealthyStream(t *testing.T) {
	sshtest.RequireTools(t, "bash", "cat")
	sentinel := installFakeDBBinaries(t, sedPass)

	src := sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))
	defer src.Close()
	dst := sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))
	defer dst.Close()

	tr := Transfer{Src: src, Dest: dst, Timeout: 10 * time.Second}
	res, err := tr.CopyDatabase(bg, DBPlanItem{SrcDB: "db", DestDB: "vh_db"}, "du", "dp", nil)
	if err != nil {
		t.Fatalf("healthy import must succeed under pipefail, got: %v", err)
	}
	if _, e := os.Stat(filepath.Join(sentinel, "mysql.ran")); e != nil {
		t.Fatalf("fake mysql never ran: %v", e)
	}
	if res.BytesSent <= 0 {
		t.Errorf("expected bytes streamed through the bridge, got %d", res.BytesSent)
	}
}
