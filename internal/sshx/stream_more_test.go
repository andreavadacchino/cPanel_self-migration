package sshx

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStartReaderReadsStdout(t *testing.T) {
	addr := newCmdServer(t, true, func(_ string, _ map[string]string, _ io.Reader, stdout, _ io.Writer) uint32 {
		_, _ = io.WriteString(stdout, "tar-bytes")
		return 0
	})
	c := dialTest(t, addr)
	defer c.Close()

	sr, err := c.StartReader(context.Background(), "tar -c")
	if err != nil {
		t.Fatalf("StartReader: %v", err)
	}
	out, err := io.ReadAll(sr)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(out) != "tar-bytes" {
		t.Errorf("StartReader stdout = %q", out)
	}
	if err := sr.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestStreamLinesCollectsLines(t *testing.T) {
	addr := newCmdServer(t, true, func(_ string, _ map[string]string, _ io.Reader, stdout, _ io.Writer) uint32 {
		_, _ = io.WriteString(stdout, "alpha\nbeta\ngamma\n")
		return 0
	})
	c := dialTest(t, addr)
	defer c.Close()

	var got []string
	if err := StreamLines(context.Background(), c, "list", nil, func(s string) error {
		got = append(got, s)
		return nil
	}); err != nil {
		t.Fatalf("StreamLines: %v", err)
	}
	if len(got) != 3 || got[0] != "alpha" || got[2] != "gamma" {
		t.Errorf("lines = %v", got)
	}
}

func TestStreamNulCollectsRecords(t *testing.T) {
	addr := newCmdServer(t, true, func(_ string, _ map[string]string, _ io.Reader, stdout, _ io.Writer) uint32 {
		_, _ = io.WriteString(stdout, "a b\x00c\nd\x00") // spaces/newlines kept inside records
		return 0
	})
	c := dialTest(t, addr)
	defer c.Close()

	var got []string
	if err := StreamNul(context.Background(), c, "list0", nil, func(s string) error {
		got = append(got, s)
		return nil
	}); err != nil {
		t.Fatalf("StreamNul: %v", err)
	}
	if len(got) != 2 || got[0] != "a b" || got[1] != "c\nd" {
		t.Errorf("records = %q", got)
	}
}

// Feeding a stdin reader exercises StartReaderStdin's stdin-copy path: the
// command echoes its stdin to stdout, which StreamLines then reads back.
func TestStreamLinesWithStdin(t *testing.T) {
	addr := newCmdServer(t, true, func(_ string, _ map[string]string, stdin io.Reader, stdout, _ io.Writer) uint32 {
		_, _ = io.Copy(stdout, stdin)
		return 0
	})
	c := dialTest(t, addr)
	defer c.Close()

	var got []string
	if err := StreamLines(context.Background(), c, "cat", strings.NewReader("one\ntwo\n"), func(s string) error {
		got = append(got, s)
		return nil
	}); err != nil {
		t.Fatalf("StreamLines: %v", err)
	}
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Errorf("lines = %v", got)
	}
}

// Cancelling the context mid-bridge must abort BOTH sessions and return the ctx
// error promptly (covers the ctx.Done branch and StreamWriter.Abort). The dest
// never drains, so the relay blocks on a full window until the cancel.
func TestBridgeProgressContextCancel(t *testing.T) {
	block := make(chan struct{})
	addr := newCmdServer(t, true, func(cmd string, _ map[string]string, _ io.Reader, stdout, _ io.Writer) uint32 {
		switch cmd {
		case "src":
			buf := make([]byte, 32*1024)
			for i := 0; i < 4096; i++ { // bounded; blocks once the window fills
				if _, err := stdout.Write(buf); err != nil {
					return 0
				}
			}
		case "dst":
			<-block // never reads its stdin -> the relay blocks
		}
		return 0
	})
	t.Cleanup(func() { close(block) })
	src := dialTest(t, addr)
	defer src.Close()
	dst := dialTest(t, addr)
	defer dst.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	err := withTimeout(t, deadlockTimeout, func() error {
		return BridgeProgress(ctx, src, "src", nil, nil, dst, "dst", nil, nil)
	})
	if err == nil {
		t.Fatal("BridgeProgress must return an error when the context is cancelled")
	}
}

// Bridge must pipe the source command's stdout into the destination command's
// stdin across two clients.
func TestBridgeCopiesSourceToDest(t *testing.T) {
	var mu sync.Mutex
	var received []byte
	addr := newCmdServer(t, true, func(cmd string, _ map[string]string, stdin io.Reader, stdout, _ io.Writer) uint32 {
		switch cmd {
		case "src":
			_, _ = io.WriteString(stdout, "PAYLOAD-DATA")
		case "dst":
			b, _ := io.ReadAll(stdin)
			mu.Lock()
			received = b
			mu.Unlock()
		}
		return 0
	})
	src := dialTest(t, addr)
	defer src.Close()
	dst := dialTest(t, addr)
	defer dst.Close()

	if err := Bridge(context.Background(), src, "src", nil, dst, "dst", nil); err != nil {
		t.Fatalf("Bridge: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if string(received) != "PAYLOAD-DATA" {
		t.Errorf("dest received %q, want PAYLOAD-DATA", received)
	}
}

// BridgeProgress's onBytes callback must report the exact number of bytes piped.
func TestBridgeProgressReportsBytes(t *testing.T) {
	addr := newCmdServer(t, true, func(cmd string, _ map[string]string, stdin io.Reader, stdout, _ io.Writer) uint32 {
		if cmd == "src" {
			_, _ = io.WriteString(stdout, "0123456789") // 10 bytes
		} else {
			_, _ = io.Copy(io.Discard, stdin)
		}
		return 0
	})
	src := dialTest(t, addr)
	defer src.Close()
	dst := dialTest(t, addr)
	defer dst.Close()

	var total int64
	if err := BridgeProgress(context.Background(), src, "src", nil, nil, dst, "dst", nil, func(n int64) {
		total += n
	}); err != nil {
		t.Fatalf("BridgeProgress: %v", err)
	}
	if total != 10 {
		t.Errorf("onBytes total = %d, want 10", total)
	}
}
