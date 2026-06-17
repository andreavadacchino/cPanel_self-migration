package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"golang.org/x/crypto/ssh"
)

// fakeExec emulates a remote command: it sees the command string, the env the
// client managed to set over SSH, and the command's stdin; it writes
// stdout/stderr and returns the exit code the server then reports.
type fakeExec func(cmd string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer) uint32

// newCmdServer starts a localhost SSH server (accepts any password). Per session
// it collects "env" requests (granted only when acceptEnv) and dispatches the
// "exec" request to h, then reports h's exit code. Richer than newTestSSHServer:
// it supports env negotiation, stdin, stderr and non-zero exits, so it can drive
// Run / RunScript / the inline-env fallback. Teardown is via t.Cleanup.
func newCmdServer(t *testing.T, acceptEnv bool, h fakeExec) string {
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
			go serveCmdConn(conn, cfg, acceptEnv, h)
		}
	}()
	return ln.Addr().String()
}

func serveCmdConn(conn net.Conn, cfg *ssh.ServerConfig, acceptEnv bool, h fakeExec) {
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
			env := map[string]string{}
			for req := range creqs {
				switch req.Type {
				case "env":
					var e struct{ Name, Value string }
					_ = ssh.Unmarshal(req.Payload, &e)
					if acceptEnv {
						env[e.Name] = e.Value
					}
					if req.WantReply {
						_ = req.Reply(acceptEnv, nil)
					}
				case "exec":
					var m struct{ Command string }
					_ = ssh.Unmarshal(req.Payload, &m)
					if req.WantReply {
						_ = req.Reply(true, nil)
					}
					code := h(m.Command, env, ch, ch, ch.Stderr())
					_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{code}))
					_ = ch.Close()
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

// okHandler is a no-op command that exits 0 (for connection-only tests).
func okHandler(string, map[string]string, io.Reader, io.Writer, io.Writer) uint32 { return 0 }

// cfgToHost builds a config.HostConfig pointing at a test-server "host:port".
func cfgToHost(t *testing.T, addr string) config.HostConfig {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return config.HostConfig{IP: host, Port: p, SSHUser: "u", SSHPass: "p", Timeout: 5 * time.Second}
}

// refusedPort returns a TCP port with no listener (a freshly closed one), so a
// connection there is refused promptly — used to drive Dial failure paths.
func refusedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// testPubKey generates a throwaway ed25519 SSH public key.
func testPubKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return pk
}

// testAddr is a stand-in remote address for host-key callback tests.
var testAddr = &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 22}
