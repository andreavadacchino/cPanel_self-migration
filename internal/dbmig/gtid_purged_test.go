package dbmig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/sshtest"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// These tests pin the version-gated --set-gtid-purged handling. On a GTID-enabled
// MySQL source, mysqldump's default emits a SUPER-requiring `SET @@GLOBAL.GTID_PURGED`
// that the non-SUPER destination import cannot run; `--set-gtid-purged=OFF` suppresses
// it, but that flag is MySQL-only and MariaDB's mysqldump errors on it. So the probe
// must detect support, and the dump command must carry the flag only when supported.
// They run against the real-bash exec server (sshtest), with a fake mysqldump on PATH.

// installGtidFakes puts a caller-supplied fake mysqldump plus a passthrough sed and a
// CAPTURING mysql (writes its stdin to $SENTINEL_DIR/import.sql) on a temp dir
// prepended to PATH, and exports SENTINEL_DIR. Returns the sentinel dir so a test can
// confirm the fakes ran and inspect what reached the dest. t.Setenv pins the test
// serial so the prepended PATH never bleeds into a parallel test.
func installGtidFakes(t *testing.T, mysqldumpBody string) string {
	t.Helper()
	bin := t.TempDir()
	sentinel := t.TempDir()
	writeFake(t, bin, "mysqldump", mysqldumpBody)
	writeFake(t, bin, "sed", sedPass) // passthrough; DEFINER strip is irrelevant here
	writeFake(t, bin, "mysql", `#!/bin/sh
echo ran > "$SENTINEL_DIR/mysql.ran"
cat > "$SENTINEL_DIR/import.sql"
exit 0
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SENTINEL_DIR", sentinel)
	return sentinel
}

// fakeMysqldumpMySQL: `--help` advertises --set-gtid-purged (probe => true); the real
// dump emits a clean stream + a proof marker WHEN given the flag, and leaks a
// GTID_PURGED line WITHOUT it.
const fakeMysqldumpMySQL = `#!/bin/sh
echo ran > "$SENTINEL_DIR/dump.ran"
case "$*" in *--help*) printf 'Usage: mysqldump\n  --set-gtid-purged[=name]  Add SET @@GLOBAL.GTID_PURGED\n'; exit 0 ;; esac
case "$*" in
  *--set-gtid-purged*) printf -- '-- gtid-suppressed\nINSERT INTO t VALUES (1);\n' ;;
  *) printf "SET @@GLOBAL.GTID_PURGED='abc-123';\nINSERT INTO t VALUES (1);\n" ;;
esac
exit 0
`

// fakeMysqldumpMariaDB: `--help` omits the flag (probe => false); the real dump errors
// like MariaDB if it is ever given --set-gtid-purged, and dumps fine without it.
const fakeMysqldumpMariaDB = `#!/bin/sh
echo ran > "$SENTINEL_DIR/dump.ran"
case "$*" in *--help*) printf 'Usage: mariadb-dump\n  --single-transaction\n  --no-tablespaces\n'; exit 0 ;; esac
case "$*" in *--set-gtid-purged*) echo "mysqldump: unknown variable 'set-gtid-purged=OFF'" >&2; exit 7 ;; esac
printf 'INSERT INTO t VALUES (1);\n'
exit 0
`

// fakeMysqldumpBroken: `--help` (and everything) exits non-zero, simulating an
// absent/unrunnable mysqldump — the probe must surface an ERROR, never a silent false.
const fakeMysqldumpBroken = `#!/bin/sh
echo ran > "$SENTINEL_DIR/dump.ran"
echo "mysqldump: cannot run" >&2
exit 2
`

func dialSrc(t *testing.T) *sshx.Client {
	t.Helper()
	c := sshtest.DialExec(t, sshtest.NewExecServer(t, t.TempDir()))
	t.Cleanup(func() { c.Close() })
	return c
}

func TestSrcSupportsGtidPurgedMySQL(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	sentinel := installGtidFakes(t, fakeMysqldumpMySQL)
	ok, err := SrcSupportsGtidPurged(bg, dialSrc(t))
	if _, e := os.Stat(filepath.Join(sentinel, "dump.ran")); e != nil {
		t.Fatalf("fake mysqldump never ran (PATH not honored): %v", e)
	}
	if err != nil {
		t.Fatalf("probe errored on a supporting mysqldump: %v", err)
	}
	if !ok {
		t.Error("probe must report TRUE when `mysqldump --help` lists --set-gtid-purged (MySQL)")
	}
}

func TestSrcSupportsGtidPurgedMariaDB(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	sentinel := installGtidFakes(t, fakeMysqldumpMariaDB)
	ok, err := SrcSupportsGtidPurged(bg, dialSrc(t))
	if _, e := os.Stat(filepath.Join(sentinel, "dump.ran")); e != nil {
		t.Fatalf("fake mysqldump never ran: %v", e)
	}
	if err != nil {
		// `mysqldump --help` ran fine; the option is just absent. That is a clean
		// FALSE, never an error (the glob match must not conflate it with a failure).
		t.Fatalf("MariaDB probe must be (false, nil), got error: %v", err)
	}
	if ok {
		t.Error("probe must report FALSE when --help omits --set-gtid-purged (MariaDB)")
	}
}

func TestSrcSupportsGtidPurgedProbeErrors(t *testing.T) {
	sshtest.RequireTools(t, "bash")
	sentinel := installGtidFakes(t, fakeMysqldumpBroken)
	ok, err := SrcSupportsGtidPurged(bg, dialSrc(t))
	if _, e := os.Stat(filepath.Join(sentinel, "dump.ran")); e != nil {
		t.Fatalf("fake mysqldump never ran: %v", e)
	}
	if err == nil {
		t.Fatalf("probe must ERROR when mysqldump itself fails (got ok=%v) — a silent false "+
			"would omit the flag on a real MySQL source and break the import later", ok)
	}
}

// With the flag (BuildDumpCmd(true)) the source dump must NOT leak SET @@GLOBAL.GTID_PURGED
// to the destination. Without the flag the fake would leak it, so this pins the flag's
// effect end to end through the real CopyDatabase bridge.
func TestCopyDatabaseGtidOffSuppressesGtidPurged(t *testing.T) {
	sshtest.RequireTools(t, "bash", "cat")
	sentinel := installGtidFakes(t, fakeMysqldumpMySQL)

	src := dialSrc(t)
	dst := dialSrc(t)
	tr := Transfer{Src: src, Dest: dst, Timeout: 10 * time.Second, DumpCmd: BuildDumpCmd(true)}
	if _, err := tr.CopyDatabase(bg, DBPlanItem{SrcDB: "db", DestDB: "vh_db"}, "du", "dp", nil); err != nil {
		t.Fatalf("MySQL copy with --set-gtid-purged=OFF must succeed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(sentinel, "import.sql"))
	if err != nil {
		t.Fatalf("dest mysql never captured a stream: %v", err)
	}
	if strings.Contains(string(got), "GTID_PURGED") {
		t.Errorf("the stream that reached the dest still carries GTID_PURGED — the flag was not applied:\n%s", got)
	}
	if !strings.Contains(string(got), "gtid-suppressed") {
		t.Errorf("expected the flag-honored marker in the dest stream, got:\n%s", got)
	}
}

// On MariaDB the flag must be OMITTED: BuildDumpCmd(false) dumps fine, but adding the
// flag (BuildDumpCmd(true)) makes the MariaDB mysqldump die — which is exactly why the
// flag is version-gated.
func TestCopyDatabaseMariaDBFlagGating(t *testing.T) {
	sshtest.RequireTools(t, "bash", "cat")
	origBackoff := sshx.RetryBackoffBase
	sshx.RetryBackoffBase = 0 // the with-flag case fails and retries; keep it fast
	t.Cleanup(func() { sshx.RetryBackoffBase = origBackoff })

	// (a) No flag -> MariaDB dumps fine -> copy succeeds.
	sentinel := installGtidFakes(t, fakeMysqldumpMariaDB)
	tr := Transfer{Src: dialSrc(t), Dest: dialSrc(t), Timeout: 10 * time.Second, DumpCmd: BuildDumpCmd(false)}
	if _, err := tr.CopyDatabase(bg, DBPlanItem{SrcDB: "db", DestDB: "vh_db"}, "du", "dp", nil); err != nil {
		t.Fatalf("MariaDB copy WITHOUT the flag must succeed: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(sentinel, "import.sql")); !strings.Contains(string(got), "INSERT INTO t") {
		t.Errorf("expected the MariaDB dump body at the dest, got: %q", got)
	}

	// (b) Wrongly adding the flag -> MariaDB mysqldump errors -> copy fails (source side).
	installGtidFakes(t, fakeMysqldumpMariaDB)
	trBad := Transfer{Src: dialSrc(t), Dest: dialSrc(t), Timeout: 10 * time.Second, DumpCmd: BuildDumpCmd(true)}
	_, err := trBad.CopyDatabase(bg, DBPlanItem{SrcDB: "db", DestDB: "vh_db"}, "du", "dp", nil)
	if err == nil {
		t.Fatal("MariaDB copy WITH --set-gtid-purged must fail (unknown option) — proving the flag must be version-gated")
	}
	if se := sshx.SideError(err, sshx.SideSource); se == nil {
		t.Errorf("the MariaDB dump failure must be tagged SideSource, got: %v", err)
	}
}
