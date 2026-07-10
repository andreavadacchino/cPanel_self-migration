package sshx

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// These tests exercise RunStream / BridgeProgress against a REAL in-process SSH
// server, and assert they never DEADLOCK when the consumer/relay stops reading
// while the remote command is still producing output (the bug: Close()/Wait()
// blocked forever on a remote process stuck writing into a full SSH window). The
// server's writer fills the channel window and blocks until the client tears the
// session down; with the bug the client would Wait() on it forever, so each test
// is guarded by a timeout that fails (rather than hangs) if the fix regresses.

// execHandler runs for each exec request; it writes to / reads from the channel
// to emulate a remote command, keyed by the command string.
type execHandler func(cmd string, ch ssh.Channel)

// newTestSSHServer starts a localhost SSH server that accepts any password and
// dispatches each exec request to handler. It returns the listen address; the
// listener and connections are torn down via t.Cleanup.
func newTestSSHServer(t *testing.T, handler execHandler) string {
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
			go serveTestConn(conn, cfg, handler)
		}
	}()
	return ln.Addr().String()
}

func serveTestConn(conn net.Conn, cfg *ssh.ServerConfig, handler execHandler) {
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
			for req := range creqs {
				if req.Type != "exec" {
					if req.WantReply {
						_ = req.Reply(false, nil)
					}
					continue
				}
				var m struct{ Command string }
				_ = ssh.Unmarshal(req.Payload, &m)
				_ = req.Reply(true, nil)
				handler(m.Command, ch)
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{0}))
				_ = ch.Close()
				return
			}
		}()
	}
}

// bigWriter writes until the channel errors (i.e. the client tore the session
// down). It blocks once the SSH window fills and the client stops reading —
// exactly the condition that made Close()/Wait() hang.
func bigWriter(_ string, ch ssh.Channel) {
	buf := make([]byte, 32*1024)
	for i := 0; i < 2048; i++ { // cap at 64 MB so it can never spin forever
		if _, err := ch.Write(buf); err != nil {
			return
		}
	}
}

func dialTest(t *testing.T, addr string) *Client {
	t.Helper()
	c, err := Dial(context.Background(), "test", addr, "u", PasswordAuth("p"), 5*time.Second, 0, ssh.InsecureIgnoreHostKey())
	if err != nil {
		t.Fatalf("dial test server: %v", err)
	}
	return c
}

// withTimeout runs fn and fails the test (instead of hanging the whole suite) if
// it does not return within d — turning a deadlock regression into a clear failure.
func withTimeout(t *testing.T, d time.Duration, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		t.Fatalf("operation did not return within %v — deadlock", d)
		return nil
	}
}

const deadlockTimeout = 10 * time.Second

// RunStream must not hang when consume returns an error before draining stdout
// (the remote command is still writing): it must Abort, not Wait.
func TestRunStreamConsumeErrorDoesNotHang(t *testing.T) {
	addr := newTestSSHServer(t, bigWriter)
	c := dialTest(t, addr)
	defer c.Close()

	wantErr := errors.New("consume bailed early")
	err := withTimeout(t, deadlockTimeout, func() error {
		return RunStream(context.Background(), c, "stream", nil, func(r io.Reader) error {
			_, _ = r.Read(make([]byte, 16)) // read a little, then bail without draining
			return wantErr
		})
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("RunStream = %v, want the consume error %v", err, wantErr)
	}
}

// BridgeProgress must not hang when the destination writer fails to start while
// the source reader is already producing output: it must Abort the reader.
func TestBridgeStartWriterFailDoesNotHang(t *testing.T) {
	addr := newTestSSHServer(t, bigWriter)
	src := dialTest(t, addr)
	defer src.Close()
	dst := dialTest(t, addr)
	dst.Close() // closed connection -> StartWriter (NewSession) fails

	err := withTimeout(t, deadlockTimeout, func() error {
		return BridgeProgress(context.Background(), src, "src", nil, nil, dst, "dst", nil, nil)
	})
	if err == nil {
		t.Error("BridgeProgress should return the StartWriter error, got nil")
	}
}

// BridgeProgress must not hang when the relay fails because the DESTINATION exits
// early (write error) while the SOURCE is still producing output: it must Wait the
// writer (for its stderr) and Abort the reader, not Close()/Wait() the reader.
func TestBridgeCopyErrorDoesNotHang(t *testing.T) {
	addr := newTestSSHServer(t, func(cmd string, ch ssh.Channel) {
		if cmd == "src" {
			bigWriter(cmd, ch)
		}
		// "dst": return immediately without reading -> the channel closes, so the
		// client's writes (io.Copy into the dest) fail with a write error.
	})
	src := dialTest(t, addr)
	defer src.Close()
	dst := dialTest(t, addr)
	defer dst.Close()

	err := withTimeout(t, deadlockTimeout, func() error {
		return BridgeProgress(context.Background(), src, "src", nil, nil, dst, "dst", nil, nil)
	})
	if err == nil {
		t.Error("BridgeProgress should return a copy error when the dest exits early, got nil")
	}
}
