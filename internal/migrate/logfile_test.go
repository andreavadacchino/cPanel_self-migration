package migrate

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// TestOpenReportWarnsWhenReportCannotBeCommitted pins finding #13: the close func
// returned by openReport performs the atomic close+rename that COMMITS the temp
// file as logs/migration_report.log. If that rename fails, the report file is never
// produced — yet the run still tells the operator to read it. The close func must
// WARN, not silently swallow the error. Force the failure by pre-creating the final
// report name as a non-empty directory so the temp file cannot be renamed onto it.
func TestOpenReportWarnsWhenReportCannotBeCommitted(t *testing.T) {
	out := t.TempDir()
	occupied := filepath.Join(out, logsDir, "migration_report.log")
	if err := os.MkdirAll(filepath.Join(occupied, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	var logBuf bytes.Buffer
	log := logx.NewTo(&logBuf, 0)
	_, closeFn, err := openReport(Options{OutputDir: out}, log, "src", "dest", "2026-06-16")
	if err != nil {
		t.Fatalf("openReport: %v", err)
	}
	closeFn()
	if !strings.Contains(logBuf.String(), "could not be committed") {
		t.Errorf("a failed report commit must warn the operator, got log: %q", logBuf.String())
	}
}

func TestCreateLogFileCreatesPrivateRegularFile(t *testing.T) {
	out := t.TempDir()
	f, path, err := createLogFile(out, "mail_analysis.log")
	if err != nil {
		t.Fatalf("createLogFile: %v", err)
	}
	if _, err := f.WriteString("analysis\n"); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close log: %v", err)
	}

	dirInfo, err := os.Stat(filepath.Join(out, logsDir))
	if err != nil {
		t.Fatalf("stat logs dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("logs dir mode = %o, want 700", got)
	}
	fileInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat log: %v", err)
	}
	if fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() {
		t.Fatalf("log mode = %v, want regular file", fileInfo.Mode())
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("log mode = %o, want 600", got)
	}
}

func TestCreateLogFileReplacesSymlinkArtifact(t *testing.T) {
	out := t.TempDir()
	logDir := filepath.Join(out, logsDir)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(out, "target.txt")
	if err := os.WriteFile(target, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "mail_analysis.log")
	if err := os.Symlink(target, logPath); err != nil {
		t.Skipf("symlink not available: %v", err)
	}

	f, path, err := createLogFile(out, "mail_analysis.log")
	if err != nil {
		t.Fatalf("createLogFile: %v", err)
	}
	if path != logPath {
		t.Fatalf("path = %q, want %q", path, logPath)
	}
	if _, err := f.WriteString("analysis"); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close log: %v", err)
	}

	targetBytes, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(targetBytes) != "sentinel" {
		t.Fatalf("symlink target was overwritten with %q", targetBytes)
	}
	logInfo, err := os.Lstat(logPath)
	if err != nil {
		t.Fatalf("lstat log: %v", err)
	}
	if logInfo.Mode()&os.ModeSymlink != 0 || !logInfo.Mode().IsRegular() {
		t.Fatalf("log mode = %v, want regular file replacing symlink", logInfo.Mode())
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if string(logBytes) != "analysis" {
		t.Fatalf("log content = %q, want analysis", logBytes)
	}
}

func TestCreateLogFileCommitsOnlyOnClose(t *testing.T) {
	out := t.TempDir()
	logDir := filepath.Join(out, logsDir)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "mail_analysis.log")
	if err := os.WriteFile(logPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	f, path, err := createLogFile(out, "mail_analysis.log")
	if err != nil {
		t.Fatalf("createLogFile: %v", err)
	}
	if path != logPath {
		t.Fatalf("path = %q, want %q", path, logPath)
	}
	if _, err := f.WriteString("new"); err != nil {
		t.Fatalf("write log: %v", err)
	}
	before, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read existing log before close: %v", err)
	}
	if string(before) != "old" {
		t.Fatalf("log was replaced before close with %q", before)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close log: %v", err)
	}
	after, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log after close: %v", err)
	}
	if string(after) != "new" {
		t.Fatalf("log after close = %q, want new", after)
	}
}

func TestCreateLogFileAbortPreservesExistingArtifact(t *testing.T) {
	out := t.TempDir()
	logDir := filepath.Join(out, logsDir)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "mail_analysis.log")
	if err := os.WriteFile(logPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	f, _, err := createLogFile(out, "mail_analysis.log")
	if err != nil {
		t.Fatalf("createLogFile: %v", err)
	}
	if _, err := f.WriteString("new"); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := f.Abort(); err != nil {
		t.Fatalf("abort log: %v", err)
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read existing log after abort: %v", err)
	}
	if string(got) != "old" {
		t.Fatalf("abort replaced existing log with %q", got)
	}
}

func TestCreateLogFileRejectsSymlinkLogsDir(t *testing.T) {
	out := t.TempDir()
	target := filepath.Join(out, "elsewhere")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(out, logsDir)); err != nil {
		t.Skipf("symlink not available: %v", err)
	}

	f, _, err := createLogFile(out, "mail_analysis.log")
	if err == nil {
		_ = f.Close()
		t.Fatal("createLogFile succeeded with symlink logs dir")
	}
	if !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("error = %v, want not a real directory", err)
	}
}
