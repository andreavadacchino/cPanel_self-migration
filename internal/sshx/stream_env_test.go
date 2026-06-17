package sshx

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests pin the streaming-bridge secret-handling contract: an env value
// (e.g. MYSQL_PWD on the DB import bridge) is delivered over the SSH channel via
// Setenv, so it NEVER enters the exec command string — and therefore never a log
// line nor the wrapping shell's argv (/proc/PID/cmdline). When the server rejects
// Setenv (AcceptEnv), the bridge does NOT put the secret in argv: non-secret keys
// are inlined via WithEnv, while a secret key (MYSQL_PWD) is delivered through the
// command's STDIN and a `read`+`export` prologue, so the value lands only in the
// remote process ENVIRON (owner-only) — never the world-readable argv.

// StartReaderStdin (the SOURCE `mysqldump` side) must deliver env via Setenv and
// keep the secret out of the exec command string the server sees.
func TestStartReaderStdinEnvViaSetenvNotInCommand(t *testing.T) {
	const secret = "SRC_PWD_MUST_NOT_APPEAR_IN_ARGV"
	var gotCmd string
	var gotEnv map[string]string
	addr := newCmdServer(t, true, func(cmd string, env map[string]string, stdin io.Reader, stdout, _ io.Writer) uint32 {
		gotCmd, gotEnv = cmd, env
		_, _ = io.Copy(io.Discard, stdin)
		_, _ = io.WriteString(stdout, "ok")
		return 0
	})
	c := dialTest(t, addr)
	defer c.Close()

	sr, err := c.StartReaderStdin(context.Background(), "mysqldump thedb", map[string]string{"MYSQL_PWD": secret}, nil)
	if err != nil {
		t.Fatalf("StartReaderStdin: %v", err)
	}
	if _, err := io.Copy(io.Discard, sr); err != nil {
		t.Fatalf("drain reader: %v", err)
	}
	if err := sr.Close(); err != nil {
		t.Fatalf("reader Close: %v", err)
	}
	if gotCmd != "mysqldump thedb" {
		t.Errorf("exec command = %q, want the bare command (env must not be inlined)", gotCmd)
	}
	if strings.Contains(gotCmd, secret) {
		t.Fatalf("secret leaked into exec command %q", gotCmd)
	}
	if gotEnv["MYSQL_PWD"] != secret {
		t.Fatalf("secret not delivered via Setenv: env = %v", gotEnv)
	}
}

// When the server rejects Setenv, StartReaderStdin must keep the SECRET (MYSQL_PWD)
// out of the exec command (argv): it is delivered through stdin and a `read`+`export`
// prologue, while the NON-secret keys (DB_NAME/DB_USER) are inlined as before.
func TestStartReaderStdinEnvSecretViaStdinFallback(t *testing.T) {
	const secret = "FALLBACK_SRC_PWD"
	var gotCmd, gotStdin string
	addr := newCmdServer(t, false, func(cmd string, _ map[string]string, stdin io.Reader, stdout, _ io.Writer) uint32 {
		gotCmd = cmd
		b, _ := io.ReadAll(stdin)
		gotStdin = string(b)
		_, _ = io.WriteString(stdout, "ok")
		return 0
	})
	c := dialTest(t, addr)
	defer c.Close()

	sr, err := c.StartReaderStdin(context.Background(), "mysqldump thedb",
		map[string]string{"MYSQL_PWD": secret, "DB_NAME": "thedb", "DB_USER": "u"}, nil)
	if err != nil {
		t.Fatalf("StartReaderStdin: %v", err)
	}
	out, err := io.ReadAll(sr)
	if err != nil {
		t.Fatalf("drain reader: %v", err)
	}
	if err := sr.Close(); err != nil {
		t.Fatalf("reader Close: %v", err)
	}
	if string(out) != "ok" {
		t.Errorf("reader stdout = %q, want ok (fallback command must still run)", out)
	}
	if strings.Contains(gotCmd, secret) {
		t.Fatalf("SECRET leaked into the exec command (argv): %q", gotCmd)
	}
	if !strings.Contains(gotCmd, "IFS= read -r MYSQL_PWD") || !strings.Contains(gotCmd, "export MYSQL_PWD") {
		t.Fatalf("fallback missing the stdin read prologue for MYSQL_PWD: %q", gotCmd)
	}
	if !strings.Contains(gotCmd, "export DB_NAME='thedb'") || !strings.Contains(gotCmd, "export DB_USER='u'") {
		t.Fatalf("non-secret keys must still be inlined: %q", gotCmd)
	}
	if !strings.Contains(gotCmd, "mysqldump thedb") {
		t.Fatalf("fallback dropped the original command: %q", gotCmd)
	}
	if gotStdin != secret+"\n" {
		t.Fatalf("secret not delivered as the leading stdin line: stdin = %q", gotStdin)
	}
}

// StartWriter (the DEST import side) on a Setenv-rejecting server must keep MYSQL_PWD
// off argv too: the secret is the FIRST line of stdin and the data payload follows on
// the same fd, so the prologue's `read` consumes the secret and the import reads the
// rest. This pins the wire order: secret line, then payload.
func TestStartWriterEnvSecretViaStdinFallback(t *testing.T) {
	const secret = "FALLBACK_DST_PWD"
	const importCmd = "sed 's/x/y/' | mysql thedb"
	var gotCmd, gotStdin string
	addr := newCmdServer(t, false, func(cmd string, _ map[string]string, stdin io.Reader, _, _ io.Writer) uint32 {
		gotCmd = cmd
		b, _ := io.ReadAll(stdin)
		gotStdin = string(b)
		return 0
	})
	c := dialTest(t, addr)
	defer c.Close()

	w, err := c.StartWriter(context.Background(), importCmd,
		map[string]string{"MYSQL_PWD": secret, "DB_NAME": "thedb", "DB_USER": "u"})
	if err != nil {
		t.Fatalf("StartWriter: %v", err)
	}
	if _, err := io.WriteString(w, "PAYLOAD"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Wait(); err != nil {
		t.Fatalf("writer Wait: %v", err)
	}
	if strings.Contains(gotCmd, secret) {
		t.Fatalf("SECRET leaked into the exec command (argv): %q", gotCmd)
	}
	if !strings.Contains(gotCmd, "IFS= read -r MYSQL_PWD") || !strings.Contains(gotCmd, importCmd) {
		t.Fatalf("fallback missing the read prologue or original pipeline: %q", gotCmd)
	}
	if gotStdin != secret+"\nPAYLOAD" {
		t.Fatalf("stdin order wrong: want secret line then payload, got %q", gotStdin)
	}
}

// End-to-end over the Bridge dbmig uses, on a Setenv-rejecting server: NEITHER
// password may appear in EITHER exec command; both arrive via their side's stdin.
func TestBridgeEnvSecretViaStdinFallback(t *testing.T) {
	const (
		dumpCmd   = "DUMP mysqldump thedb"
		importCmd = "IMPORT sed 's/x/y/' | mysql thedb"
		srcSecret = "BRIDGE_SRC_PWD_SENTINEL"
		dstSecret = "BRIDGE_DST_PWD_SENTINEL"
	)
	var srcCmdSeen, dstCmdSeen, srcStdin, dstStdin string
	addr := newCmdServer(t, false, func(cmd string, _ map[string]string, stdin io.Reader, stdout, _ io.Writer) uint32 {
		if strings.Contains(cmd, "DUMP mysqldump") {
			srcCmdSeen = cmd
			b, _ := io.ReadAll(stdin)
			srcStdin = string(b)
			_, _ = io.WriteString(stdout, "ARCHIVE-BYTES")
			return 0
		}
		dstCmdSeen = cmd
		b, _ := io.ReadAll(stdin)
		dstStdin = string(b)
		return 0
	})
	src := dialTest(t, addr)
	defer src.Close()
	dst := dialTest(t, addr)
	defer dst.Close()

	err := BridgeProgress(context.Background(),
		src, dumpCmd, map[string]string{"MYSQL_PWD": srcSecret}, nil,
		dst, importCmd, map[string]string{"MYSQL_PWD": dstSecret}, nil)
	if err != nil {
		t.Fatalf("BridgeProgress: %v", err)
	}
	if strings.Contains(srcCmdSeen, srcSecret) || strings.Contains(srcCmdSeen, dstSecret) ||
		strings.Contains(dstCmdSeen, srcSecret) || strings.Contains(dstCmdSeen, dstSecret) {
		t.Fatalf("a password leaked into an exec command: src=%q dst=%q", srcCmdSeen, dstCmdSeen)
	}
	if srcStdin != srcSecret+"\n" {
		t.Errorf("source secret not the leading stdin line: %q", srcStdin)
	}
	// The dest stdin is the secret line followed by the relayed archive bytes.
	if dstStdin != dstSecret+"\nARCHIVE-BYTES" {
		t.Errorf("dest stdin order wrong: %q", dstStdin)
	}
}

// A secret value containing a newline cannot be delivered as one stdin line, so the
// fallback must FAIL CLOSED (return an error, never start the command) rather than
// risk splitting the secret and corrupting the data stream. Both sides must do this.
func TestSecretWithNewlineFailsClosed(t *testing.T) {
	const bad = "line1\nDROP TABLE users"
	var started bool
	addr := newCmdServer(t, false, func(string, map[string]string, io.Reader, io.Writer, io.Writer) uint32 {
		started = true
		return 0
	})
	c := dialTest(t, addr)
	defer c.Close()

	if _, err := c.StartWriter(context.Background(), "mysql thedb", map[string]string{"MYSQL_PWD": bad}); err == nil {
		t.Fatal("StartWriter must reject a newline-bearing secret, got nil error")
	}
	if _, err := c.StartReaderStdin(context.Background(), "mysqldump thedb", map[string]string{"MYSQL_PWD": bad}, nil); err == nil {
		t.Fatal("StartReaderStdin must reject a newline-bearing secret, got nil error")
	}
	if started {
		t.Fatal("the remote command must NEVER start when the secret is rejected")
	}
}

// An EMPTY password is valid: the fallback feeds an empty leading line, the prologue
// reads it, and the data payload that follows still reaches the command intact.
func TestEmptySecretViaStdinFallback(t *testing.T) {
	var gotCmd, gotStdin string
	addr := newCmdServer(t, false, func(cmd string, _ map[string]string, stdin io.Reader, _, _ io.Writer) uint32 {
		gotCmd = cmd
		b, _ := io.ReadAll(stdin)
		gotStdin = string(b)
		return 0
	})
	c := dialTest(t, addr)
	defer c.Close()

	w, err := c.StartWriter(context.Background(), "mysql thedb", map[string]string{"MYSQL_PWD": ""})
	if err != nil {
		t.Fatalf("StartWriter: %v", err)
	}
	if _, err := io.WriteString(w, "PAYLOAD"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Wait(); err != nil {
		t.Fatalf("writer Wait: %v", err)
	}
	if !strings.Contains(gotCmd, "IFS= read -r MYSQL_PWD") {
		t.Fatalf("empty secret must still use the read prologue: %q", gotCmd)
	}
	if gotStdin != "\nPAYLOAD" {
		t.Fatalf("empty secret: stdin = %q, want an empty leading line then payload", gotStdin)
	}
}

// TestSecretStdinFallbackRealShell drives the fallback path through a REAL shell to
// prove the prologue's semantics: `IFS= read -r MYSQL_PWD` consumes exactly the
// secret line (exporting it into the env the command sees), and the bytes AFTER that
// line reach the command unchanged on the same fd 0 (the import's data stream).
func TestSecretStdinFallbackRealShell(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	// Leading/trailing spaces (would be trimmed without IFS=) + a backslash (would be
	// eaten without -r) + shell-special chars: only a verbatim `IFS= read -r` survives.
	const secret = `  p@ss\w0rd'"$x  `
	dir := t.TempDir()
	pwdFile := filepath.Join(dir, "pwd")
	dataFile := filepath.Join(dir, "data")
	// The command exports nothing itself; it relies on the prologue having read+exported
	// MYSQL_PWD from stdin, then writes the env value and the remaining stdin to files.
	cmd := fmt.Sprintf(`printf '%%s' "$MYSQL_PWD" > %s; cat > %s`, pwdFile, dataFile)

	var gotCmd string
	addr := newCmdServer(t, false, func(c string, _ map[string]string, stdin io.Reader, _, stderr io.Writer) uint32 {
		gotCmd = c
		rc := exec.Command("bash", "-c", c) // #nosec G204 -- test drives the fallback command through a real shell
		rc.Stdin = stdin
		rc.Stderr = stderr
		if err := rc.Run(); err != nil {
			return 1
		}
		return 0
	})
	cl := dialTest(t, addr)
	defer cl.Close()

	w, err := cl.StartWriter(context.Background(), cmd,
		map[string]string{"MYSQL_PWD": secret, "DB_NAME": "thedb", "DB_USER": "u"})
	if err != nil {
		t.Fatalf("StartWriter: %v", err)
	}
	if _, err := io.WriteString(w, "THE-SQL-DUMP-PAYLOAD"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Wait(); err != nil {
		t.Fatalf("writer Wait: %v", err)
	}
	// The security property: the secret must never be in the exec command (argv). This
	// makes the test fail on the OLD inline-into-argv behavior, not just validate mechanics.
	if strings.Contains(gotCmd, secret) {
		t.Fatalf("SECRET leaked into the exec command (argv): %q", gotCmd)
	}
	gotPwd, err := os.ReadFile(pwdFile) //nolint:gosec // test-controlled temp path
	if err != nil {
		t.Fatalf("read pwd file: %v", err)
	}
	if string(gotPwd) != secret {
		t.Fatalf("MYSQL_PWD as seen by the command = %q, want %q (prologue must export it verbatim)", gotPwd, secret)
	}
	gotData, err := os.ReadFile(dataFile) //nolint:gosec // test-controlled temp path
	if err != nil {
		t.Fatalf("read data file: %v", err)
	}
	if string(gotData) != "THE-SQL-DUMP-PAYLOAD" {
		t.Fatalf("payload after the secret line = %q, want the full dump (read must not over-consume)", gotData)
	}
}

// StartWriter is the DEST import side — the `sed … | mysql …` PIPELINE that, when
// inlined, left MYSQL_PWD in the wrapping bash's argv (bash keeps argv for a
// pipeline; it does not exec-away the last command). Setenv delivery keeps it out.
func TestStartWriterEnvViaSetenvNotInCommand(t *testing.T) {
	const secret = "DST_PWD_MUST_NOT_APPEAR_IN_ARGV"
	const importCmd = "sed 's/x/y/' | mysql thedb"
	var gotCmd string
	var gotEnv map[string]string
	addr := newCmdServer(t, true, func(cmd string, env map[string]string, stdin io.Reader, _, _ io.Writer) uint32 {
		gotCmd, gotEnv = cmd, env
		_, _ = io.Copy(io.Discard, stdin)
		return 0
	})
	c := dialTest(t, addr)
	defer c.Close()

	w, err := c.StartWriter(context.Background(), importCmd, map[string]string{"MYSQL_PWD": secret})
	if err != nil {
		t.Fatalf("StartWriter: %v", err)
	}
	if _, err := io.WriteString(w, "payload"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Wait(); err != nil {
		t.Fatalf("writer Wait: %v", err)
	}
	if gotCmd != importCmd {
		t.Errorf("exec command = %q, want the bare pipeline (env must not be inlined)", gotCmd)
	}
	if strings.Contains(gotCmd, secret) {
		t.Fatalf("secret leaked into exec command %q", gotCmd)
	}
	if gotEnv["MYSQL_PWD"] != secret {
		t.Fatalf("secret not delivered via Setenv: env = %v", gotEnv)
	}
}

// End-to-end over the actual Bridge path dbmig uses: BOTH the source dump command
// and the dest import command must carry their password via Setenv only — neither
// secret may appear in either exec command string.
func TestBridgeEnvViaSetenvNotInCommands(t *testing.T) {
	const (
		dumpCmd   = "DUMP mysqldump thedb"
		importCmd = "IMPORT sed 's/x/y/' | mysql thedb"
		srcSecret = "BRIDGE_SRC_PWD_SENTINEL"
		dstSecret = "BRIDGE_DST_PWD_SENTINEL"
	)
	var srcCmdSeen, dstCmdSeen string
	var srcEnvSeen, dstEnvSeen map[string]string
	addr := newCmdServer(t, true, func(cmd string, env map[string]string, stdin io.Reader, stdout, _ io.Writer) uint32 {
		if strings.HasPrefix(cmd, "DUMP") {
			srcCmdSeen, srcEnvSeen = cmd, env
			_, _ = io.WriteString(stdout, "ARCHIVE-BYTES")
			return 0
		}
		dstCmdSeen, dstEnvSeen = cmd, env
		_, _ = io.Copy(io.Discard, stdin)
		return 0
	})
	src := dialTest(t, addr)
	defer src.Close()
	dst := dialTest(t, addr)
	defer dst.Close()

	err := BridgeProgress(context.Background(),
		src, dumpCmd, map[string]string{"MYSQL_PWD": srcSecret}, nil,
		dst, importCmd, map[string]string{"MYSQL_PWD": dstSecret}, nil)
	if err != nil {
		t.Fatalf("BridgeProgress: %v", err)
	}
	if srcCmdSeen != dumpCmd {
		t.Errorf("source exec command = %q, want bare %q", srcCmdSeen, dumpCmd)
	}
	if dstCmdSeen != importCmd {
		t.Errorf("dest exec command = %q, want bare %q", dstCmdSeen, importCmd)
	}
	if strings.Contains(srcCmdSeen, srcSecret) || strings.Contains(dstCmdSeen, srcSecret) ||
		strings.Contains(srcCmdSeen, dstSecret) || strings.Contains(dstCmdSeen, dstSecret) {
		t.Fatalf("a password leaked into an exec command: src=%q dst=%q", srcCmdSeen, dstCmdSeen)
	}
	if srcEnvSeen["MYSQL_PWD"] != srcSecret {
		t.Errorf("source password not delivered via Setenv: %v", srcEnvSeen)
	}
	if dstEnvSeen["MYSQL_PWD"] != dstSecret {
		t.Errorf("dest password not delivered via Setenv: %v", dstEnvSeen)
	}
}
