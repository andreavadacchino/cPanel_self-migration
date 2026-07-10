// Package sshtest provides an in-process SSH "exec" server for integration tests:
// it accepts any password and runs each exec request via `bash -c` in a temp HOME,
// so a test can drive the REAL SSH transport (sshx) and the remote bash/tar/find/
// mysql commands end-to-end without a real SSH daemon. Two servers (one per HOME)
// stand in for source and destination.
//
// It lives in a non-_test package so the maildir/webfiles/dbmig/migrate test
// packages can share it. (sshx's own tests keep their in-package harness — importing
// this, which imports sshx, would cycle.)
package sshtest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
	"golang.org/x/crypto/ssh"
)

// NewExecServer starts a localhost SSH server (any password) that runs each exec
// request via `bash -c` with HOME set to home and any SSH env applied. It returns
// the listen address; teardown is via t.Cleanup.
func NewExecServer(t *testing.T, home string) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil },
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveExec(conn, cfg, home)
		}
	}()
	return ln.Addr().String()
}

func serveExec(conn net.Conn, cfg *ssh.ServerConfig, home string) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "only session")
			continue
		}
		ch, creqs, err := newCh.Accept()
		if err != nil {
			return
		}
		go func() {
			env := []string{"HOME=" + home}
			for req := range creqs {
				switch req.Type {
				case "env":
					var e struct{ Name, Value string }
					_ = ssh.Unmarshal(req.Payload, &e)
					env = append(env, e.Name+"="+e.Value)
					if req.WantReply {
						_ = req.Reply(true, nil)
					}
				case "exec":
					var m struct{ Command string }
					_ = ssh.Unmarshal(req.Payload, &m)
					if req.WantReply {
						_ = req.Reply(true, nil)
					}
					runExec(ch, m.Command, env)
					return
				default:
					if req.WantReply {
						_ = req.Reply(false, nil)
					}
				}
			}
		}()
	}
}

// runExec runs command via bash -c with stdin/stdout/stderr wired to the SSH
// channel and the given environment, then reports the exit status.
func runExec(ch ssh.Channel, command string, env []string) {
	cmd := exec.Command("bash", "-c", command) // #nosec G204 -- test-only in-process SSH exec server; the command is the test's own exec request
	cmd.Env = append(os.Environ(), env...)     // later entries (our HOME, REL, …) win
	cmd.Stdin = ch
	cmd.Stdout = ch
	cmd.Stderr = ch.Stderr()
	var code uint32
	if err := cmd.Run(); err != nil {
		code = 1
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() >= 0 {
			code = uint32(ee.ExitCode()) // #nosec G115 -- exit codes are 0-255 (guarded >= 0 above), no overflow
		}
	}
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{code}))
	_ = ch.Close()
}

// DialExec dials an *sshx.Client to an exec server started by NewExecServer.
func DialExec(t *testing.T, addr string) *sshx.Client {
	t.Helper()
	c, err := sshx.Dial(context.Background(), "test", addr, "u", sshx.PasswordAuth("p"), 5*time.Second, 0, ssh.InsecureIgnoreHostKey()) // #nosec G106 -- test-only dialer to an in-process localhost server with an ephemeral key
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	return c
}

// RequireTools skips the test unless every named external tool is on PATH.
func RequireTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, b := range tools {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s not available", b)
		}
	}
}
