package sshx

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestRunReturnsStdoutAndName(t *testing.T) {
	addr := newCmdServer(t, true, func(cmd string, _ map[string]string, _ io.Reader, stdout, _ io.Writer) uint32 {
		_, _ = io.WriteString(stdout, "hello from "+cmd)
		return 0
	})
	c := dialTest(t, addr)
	defer c.Close()

	out, err := c.Run(context.Background(), "echo")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(out) != "hello from echo" {
		t.Errorf("Run stdout = %q", out)
	}
	if c.Name() != "test" {
		t.Errorf("Name() = %q, want test", c.Name())
	}
}

func TestRunNonZeroExitIncludesStderr(t *testing.T) {
	addr := newCmdServer(t, true, func(_ string, _ map[string]string, _ io.Reader, _, stderr io.Writer) uint32 {
		_, _ = io.WriteString(stderr, "boom happened")
		return 3
	})
	c := dialTest(t, addr)
	defer c.Close()

	if _, err := c.Run(context.Background(), "fail"); err == nil || !strings.Contains(err.Error(), "boom happened") {
		t.Errorf("Run on non-zero exit = %v, want error including stderr", err)
	}
}

// RunScript's params reach the command via SSH Setenv when the server allows it.
func TestRunScriptSetenvSuccess(t *testing.T) {
	addr := newCmdServer(t, true, func(_ string, env map[string]string, stdin io.Reader, stdout, _ io.Writer) uint32 {
		// Drain stdin (the script) like a real `bash -s` would, so the client's
		// stdin copy completes before the session closes — otherwise the server can
		// close the channel mid-write and the client's copy fails with EOF (a
		// harness-only race that -race scheduling exposes).
		_, _ = io.Copy(io.Discard, stdin)
		_, _ = io.WriteString(stdout, env["FOO"])
		return 0
	})
	c := dialTest(t, addr)
	defer c.Close()

	out, err := c.RunScript(context.Background(), "ignored", map[string]string{"FOO": "bar"})
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if string(out) != "bar" {
		t.Errorf("env FOO via Setenv = %q, want bar", out)
	}
}

// When the server rejects Setenv, RunScript must fall back to prepending
// `export KEY='value'` to the script (preserving "no secrets in argv").
func TestRunScriptInlineEnvFallback(t *testing.T) {
	addr := newCmdServer(t, false, func(_ string, _ map[string]string, stdin io.Reader, stdout, _ io.Writer) uint32 {
		b, _ := io.ReadAll(stdin) // echo the (now export-prefixed) script back
		_, _ = stdout.Write(b)
		return 0
	})
	c := dialTest(t, addr)
	defer c.Close()

	out, err := c.RunScript(context.Background(), "echo body\n", map[string]string{"FOO": "bar"})
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "export FOO='bar'") || !strings.Contains(got, "echo body") {
		t.Errorf("inline-env script = %q, want export FOO + original body", got)
	}
}

func TestRunScriptEnvValueNotInExecCommand(t *testing.T) {
	secret := "TOK_SHOULD_NOT_APPEAR_IN_ARGV"
	for _, acceptEnv := range []bool{true, false} {
		var gotCmd string
		addr := newCmdServer(t, acceptEnv, func(cmd string, _ map[string]string, stdin io.Reader, stdout, _ io.Writer) uint32 {
			gotCmd = cmd
			_, _ = io.Copy(io.Discard, stdin)
			_, _ = io.WriteString(stdout, "ok")
			return 0
		})
		c := dialTest(t, addr)
		out, err := c.RunScript(context.Background(), "echo ok\n", map[string]string{"TOKEN": secret})
		if err != nil {
			_ = c.Close()
			t.Fatalf("RunScript(acceptEnv=%v): %v", acceptEnv, err)
		}
		_ = c.Close()
		if string(out) != "ok" {
			t.Fatalf("RunScript(acceptEnv=%v) stdout = %q, want ok", acceptEnv, out)
		}
		if strings.Contains(gotCmd, secret) {
			t.Fatalf("RunScript(acceptEnv=%v) leaked env value into exec command %q", acceptEnv, gotCmd)
		}
	}
}

// run() with env but no stdin, against a server that rejects Setenv, cannot
// inline env into a non-script command and must say so.
func TestRunInlineEnvNonScriptErrors(t *testing.T) {
	addr := newCmdServer(t, false, okHandler)
	c := dialTest(t, addr)
	defer c.Close()

	if _, err := c.run(context.Background(), "echo", map[string]string{"K": "v"}, nil); err == nil || !strings.Contains(err.Error(), "not a script") {
		t.Errorf("run(env, nil stdin) = %v, want 'not a script' error", err)
	}
}

// A cancelled context must make Run return promptly even while the command is
// still running. The handler blocks until the test ends, so only the ctx timeout
// can unblock Run.
func TestRunContextCancelled(t *testing.T) {
	block := make(chan struct{})
	addr := newCmdServer(t, true, func(_ string, _ map[string]string, _ io.Reader, _, _ io.Writer) uint32 {
		<-block // never returns on its own during the test
		return 0
	})
	t.Cleanup(func() { close(block) })
	c := dialTest(t, addr)
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := withTimeout(t, deadlockTimeout, func() error {
		_, e := c.Run(ctx, "block")
		return e
	})
	if err == nil {
		t.Fatal("Run with a cancelled context must return an error")
	}
}

// keepaliveProbe must report probeOK (and return within its bound, not block) on a
// healthy connection — the bounded wait is what stops a black-holed connection from
// hanging the probe until the OS TCP timeout.
func TestKeepaliveProbeHealthy(t *testing.T) {
	addr := newCmdServer(t, true, okHandler)
	c := dialTest(t, addr)
	defer c.Close()
	if ok := withTimeout(t, deadlockTimeout, func() error {
		if res, err := c.keepaliveProbe(c.cli, 2*time.Second, c.stopKA); res != probeOK {
			t.Errorf("keepaliveProbe on a healthy connection should report probeOK, got %v (err=%v)", res, err)
		}
		return nil
	}); ok != nil {
		t.Fatalf("keepaliveProbe did not return promptly: %v", ok)
	}
}

// Dialing with a short keepalive exercises the keepalive loop (it ticks at least
// once), and Close stops it cleanly.
func TestKeepaliveLoopRunsAndStops(t *testing.T) {
	addr := newCmdServer(t, true, okHandler)
	c, err := Dial(context.Background(), "ka", addr, "u", "p", 5*time.Second, 20*time.Millisecond, ssh.InsecureIgnoreHostKey())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	time.Sleep(70 * time.Millisecond) // allow a few keepalive ticks
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
