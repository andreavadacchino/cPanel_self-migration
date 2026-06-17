package sshx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"golang.org/x/crypto/ssh"
)

// maxStreamLine caps a single scanned line in StreamLines, generous enough for
// long file paths (the default bufio.Scanner cap is 64 KiB).
const maxStreamLine = 1024 * 1024

// RunStream runs cmd on the client (with optional stdin), hands its stdout to
// consume, and tears the session down — centralizing the ctx-cancel abort and the
// scan/close error handling shared by every streaming reader. consume must read
// the stdout reader to EOF (e.g. a bufio.Scanner loop or a framed parser);
// RunStream then Close()s the session (waiting for the command) and reports, in
// priority order: a ctx cancellation, the consume error, then the command/stderr
// error. This is the single SSH-streaming chokepoint reused by the docroot gather,
// the copy file-listing, and the ~/mail scan.
func RunStream(ctx context.Context, c *Client, cmd string, stdin io.Reader, consume func(io.Reader) error) error {
	sr, err := c.StartReaderStdin(ctx, cmd, nil, stdin)
	if err != nil {
		return err
	}
	// Ctrl-C unblocks the in-flight Read at once.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			sr.Abort()
		case <-done:
		}
	}()

	cerr := consume(sr) // reads stdout to EOF (on success)
	// If consume returned early with an error it may NOT have drained stdout. The
	// remote command is then stuck writing into a full SSH window, so sr.Close()'s
	// Wait() would block forever. Abort() (immediate session teardown) instead and
	// surface the consume error. Only on a clean read do we Close()/Wait() for the
	// command's exit status + stderr.
	var closeErr error
	if cerr != nil {
		sr.Abort()
	} else {
		closeErr = sr.Close()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if cerr != nil {
		return cerr
	}
	return closeErr
}

// StreamLines runs cmd and invokes onLine for each stdout line (newline-stripped),
// over a buffer generous enough for long paths. A non-nil onLine error stops the
// scan and is returned. Built on RunStream, so it has the same ctx-abort and
// close-error semantics.
func StreamLines(ctx context.Context, c *Client, cmd string, stdin io.Reader, onLine func(string) error) error {
	return RunStream(ctx, c, cmd, stdin, func(r io.Reader) error {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), maxStreamLine)
		for sc.Scan() {
			if err := onLine(sc.Text()); err != nil {
				return err
			}
		}
		return sc.Err()
	})
}

// StreamNul runs cmd and invokes onRecord for each NUL-delimited record on
// stdout (the NUL stripped), over a buffer generous enough for long paths. It is
// the record-oriented twin of StreamLines, used for file lists that may contain
// spaces, newlines, or any byte except NUL (`find -printf '…\0'`): NUL is the
// only separator that cannot appear in a path. A non-nil onRecord error stops the
// scan and is returned. Built on RunStream, so it has the same ctx-abort and
// close-error semantics.
func StreamNul(ctx context.Context, c *Client, cmd string, stdin io.Reader, onRecord func(string) error) error {
	return RunStream(ctx, c, cmd, stdin, func(r io.Reader) error {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), maxStreamLine)
		sc.Split(scanNull)
		for sc.Scan() {
			if err := onRecord(sc.Text()); err != nil {
				return err
			}
		}
		return sc.Err()
	})
}

// scanNull is a bufio.SplitFunc that splits on NUL bytes. A trailing
// unterminated chunk at EOF is returned as a final token; the empty tail after a
// terminating NUL yields no token (so `a\0b\0` produces exactly "a","b").
func scanNull(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil // request more data
}

// StreamReader runs cmd on the client and exposes its stdout as a reader.
// Call Close to wait for the command and release the session. stderr is
// captured and surfaced via Close's error on failure.
type StreamReader struct {
	c      *Client
	sess   *ssh.Session
	stdout io.Reader
	stderr *capBuf
	cmd    string
	name   string
	once   sync.Once // guards a single session-close (Close or Abort)
	// feedDone, when non-nil (stdin was supplied, e.g. a tar --files-from=- list),
	// receives the result of READING that source list: non-nil only if the read
	// failed, which means tar got a TRUNCATED list and silently archived fewer
	// files. Close() surfaces it. Buffered, so the feeder never blocks on Abort.
	feedDone chan error
}

// errCapReader wraps a reader and records its first non-EOF read error. It lets
// the file-list feeder tell a SOURCE-list read failure (dangerous: tar gets a
// truncated list) apart from a destination-side pipe close (benign: tar finished
// and stopped reading), which a bare io.Copy error cannot distinguish.
type errCapReader struct {
	r   io.Reader
	err error
}

func (e *errCapReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && err != io.EOF {
		e.err = err
	}
	return n, err
}

// StartReader starts cmd and returns a reader over its stdout. Used for the
// SOURCE side `tar -c` (read-only): the tar archive streams out of stdout. ctx
// bounds a transparent reconnect if the connection dropped (see newSession).
func (c *Client) StartReader(ctx context.Context, cmd string) (*StreamReader, error) {
	return c.StartReaderStdin(ctx, cmd, nil, nil)
}

// StartReaderStdin is StartReader with an optional stdin source fed to the
// command. Used to pass a large `--files-from=-` list to the source tar via
// STDIN instead of embedding it in the exec command string (a multi-hundred-KB
// command string overflows the SSH channel and gets reset by the server).
func (c *Client) StartReaderStdin(ctx context.Context, cmd string, env map[string]string, stdin io.Reader) (*StreamReader, error) {
	sess, err := c.newSession(ctx, "reader")
	if err != nil {
		return nil, fmt.Errorf("%s: new session: %w", c.name, err)
	}
	// Deliver env via the SSH channel (Setenv) so a secret value (e.g. MYSQL_PWD on
	// the DB import bridge) never enters the command STRING — and so never reaches a
	// log line or the shell's argv. If the server rejects Setenv (AcceptEnv), re-open
	// a clean session: NON-secret keys are inlined into the command we RUN (still
	// safe in argv), while SECRET keys are delivered through the command's STDIN with
	// a `read`+`export` prologue, so the secret value lands in the remote process's
	// ENVIRON (owner-only) and NEVER in argv. We log/store only the BARE command.
	runCmd := cmd
	stdinFeed := stdin
	if len(env) > 0 && !c.trySetenv(sess, env) {
		c.closeSession(sess, "reader/setenv-fallback")
		if sess, err = c.newSession(ctx, "reader"); err != nil {
			return nil, fmt.Errorf("%s: new session: %w", c.name, err)
		}
		public, secret := splitSecretEnv(env)
		runCmd = WithEnv(cmd, public)
		if len(secret) > 0 {
			keys := sortedEnvKeys(secret)
			secretBytes, serr := secretStdinBytes(secret, keys)
			if serr != nil {
				c.closeSession(sess, "reader/secret-encode")
				return nil, fmt.Errorf("%s: %w", c.name, serr)
			}
			runCmd = secretStdinPrologue(runCmd, keys)
			stdinFeed = prependSecretReader(secretBytes, stdin)
		}
	}
	out, err := sess.StdoutPipe()
	if err != nil {
		c.closeSession(sess, "reader/pipe-err")
		return nil, fmt.Errorf("%s: stdout pipe: %w", c.name, err)
	}
	var feedDone chan error
	if stdinFeed != nil {
		in, err := sess.StdinPipe()
		if err != nil {
			c.closeSession(sess, "reader/stdin-err")
			return nil, fmt.Errorf("%s: stdin pipe: %w", c.name, err)
		}
		ecr := &errCapReader{r: stdinFeed}
		feedDone = make(chan error, 1)
		go func() {
			_, _ = io.Copy(in, ecr)
			_ = in.Close()      // EOF so tar stops reading the file list
			feedDone <- ecr.err // non-nil ONLY if reading the source list failed
		}()
	}
	errBuf := newCapBuf(4096)
	sess.Stderr = errBuf
	logx.Debug("%s: reader starting %q (stdin=%v)", c.name, cmd, stdinFeed != nil)
	if err := sess.Start(runCmd); err != nil {
		c.closeSession(sess, "reader/start-err")
		return nil, fmt.Errorf("%s: start %q: %w", c.name, cmd, err)
	}
	return &StreamReader{c: c, sess: sess, stdout: out, stderr: errBuf, cmd: cmd, name: c.name, feedDone: feedDone}, nil
}

// trySetenv applies env to the session via SSH Setenv. It returns false on the
// FIRST rejection (many sshd configs limit AcceptEnv) so the caller can fall back:
// non-secret keys are inlined into the command string, secret keys (MYSQL_PWD) are
// delivered via stdin — keeping the secret value out of logs and the shell argv.
func (c *Client) trySetenv(sess *ssh.Session, env map[string]string) bool {
	for k, v := range env {
		if err := sess.Setenv(k, v); err != nil {
			logx.Debug("%s: Setenv rejected, falling back (non-secret env inlined, secrets via stdin)", c.name)
			return false
		}
	}
	return true
}

// Read implements io.Reader over the command's stdout.
func (r *StreamReader) Read(p []byte) (int, error) { return r.stdout.Read(p) }

// Abort closes the session IMMEDIATELY without waiting for the command to
// finish. This unblocks any in-flight Read/io.Copy (used on ctx cancellation,
// e.g. Ctrl-C, so a large transfer stops at once instead of after the batch).
func (r *StreamReader) Abort() { r.once.Do(func() { r.c.closeSession(r.sess, "reader/abort") }) }

// Close waits for the command to finish and returns its error (with stderr).
func (r *StreamReader) Close() error {
	err := r.sess.Wait()
	// Collect the file-list feed result (if any). A read failure here means tar
	// received a TRUNCATED --files-from list and silently archived fewer files —
	// a partial transfer that would otherwise report success. Only wait for this
	// feeder after a clean command exit: when the command failed, that error
	// dominates and waiting on a blocked stdin feeder can hang Close().
	var feedErr error
	if err == nil && r.feedDone != nil {
		feedErr = <-r.feedDone
	}
	if err == nil {
		// A clean exit can still carry warnings on stderr — GNU tar writes "file
		// changed as we read it" / "Removing leading '/'" while exiting 0. Those
		// are the breadcrumb for a later byte/count mismatch at verify time, so
		// surface them at debug instead of dropping them on the success path.
		if s := r.stderr.String(); s != "" {
			logx.Debug("%s: reader %q exited 0 with stderr: %s", r.name, r.cmd, s)
		}
		logx.Debug("%s: reader %q closed successfully", r.name, r.cmd)
	}
	r.once.Do(func() { r.c.closeSession(r.sess, "reader/close") })
	if err != nil {
		return fmt.Errorf("%s: %q failed: %w (stderr: %s)", r.name, r.cmd, err, r.stderr.String())
	}
	if feedErr != nil {
		logx.Warn("%s: reading the file list for %q failed (%v) — the transfer is INCOMPLETE: tar received a truncated list", r.name, r.cmd, feedErr)
		return fmt.Errorf("%s: file-list feed failed (transfer incomplete): %w", r.name, feedErr)
	}
	return nil
}

// StreamWriter runs cmd on the client with an attached stdin writer. Used for
// the DEST side `tar -x`: the archive is fed into stdin.
type StreamWriter struct {
	c      *Client
	sess   *ssh.Session
	stdin  io.WriteCloser
	stderr *capBuf
	cmd    string
	name   string
	once   sync.Once // guards a single session-close (Wait or Abort)
}

// StartWriter starts cmd and returns a writer to its stdin. ctx bounds a
// transparent reconnect if the connection dropped (see newSession).
func (c *Client) StartWriter(ctx context.Context, cmd string, env map[string]string) (*StreamWriter, error) {
	sess, err := c.newSession(ctx, "writer")
	if err != nil {
		return nil, fmt.Errorf("%s: new session: %w", c.name, err)
	}
	// Setenv-first (keeps a secret env value out of the command string / logs / argv),
	// with an inline fallback when the server rejects it — see StartReaderStdin. A
	// secret key (MYSQL_PWD) is NOT inlined: it is written as the FIRST line(s) of
	// stdin and picked up by a `read`+`export` prologue, so it stays out of argv. The
	// import pipeline then reads the rest of stdin (the SQL stream) on the same fd.
	runCmd := cmd
	var secretBytes []byte
	if len(env) > 0 && !c.trySetenv(sess, env) {
		c.closeSession(sess, "writer/setenv-fallback")
		if sess, err = c.newSession(ctx, "writer"); err != nil {
			return nil, fmt.Errorf("%s: new session: %w", c.name, err)
		}
		public, secret := splitSecretEnv(env)
		runCmd = WithEnv(cmd, public)
		if len(secret) > 0 {
			keys := sortedEnvKeys(secret)
			sb, serr := secretStdinBytes(secret, keys)
			if serr != nil {
				c.closeSession(sess, "writer/secret-encode")
				return nil, fmt.Errorf("%s: %w", c.name, serr)
			}
			runCmd = secretStdinPrologue(runCmd, keys)
			secretBytes = sb
		}
	}
	in, err := sess.StdinPipe()
	if err != nil {
		c.closeSession(sess, "writer/pipe-err")
		return nil, fmt.Errorf("%s: stdin pipe: %w", c.name, err)
	}
	errBuf := newCapBuf(4096)
	sess.Stderr = errBuf
	logx.Debug("%s: writer starting %q", c.name, cmd)
	if err := sess.Start(runCmd); err != nil {
		c.closeSession(sess, "writer/start-err")
		return nil, fmt.Errorf("%s: start %q: %w", c.name, cmd, err)
	}
	// Feed the secret env line(s) BEFORE the caller streams the data payload, so the
	// remote prologue's `read` consumes them and the payload that follows reaches the
	// import command unchanged. The lines are tiny (one short line per secret), well
	// under the SSH window, so this write does not block.
	if len(secretBytes) > 0 {
		if _, werr := in.Write(secretBytes); werr != nil {
			c.closeSession(sess, "writer/secret-write")
			return nil, fmt.Errorf("%s: writing secret env to stdin: %w", c.name, werr)
		}
	}
	return &StreamWriter{c: c, sess: sess, stdin: in, stderr: errBuf, cmd: cmd, name: c.name}, nil
}

// Write implements io.Writer to the command's stdin.
func (w *StreamWriter) Write(p []byte) (int, error) { return w.stdin.Write(p) }

// CloseStdin signals EOF on stdin so the remote command can finish.
func (w *StreamWriter) CloseStdin() error { return w.stdin.Close() }

// Abort closes the session IMMEDIATELY without waiting. Unblocks any in-flight
// Write/io.Copy on ctx cancellation.
func (w *StreamWriter) Abort() { w.once.Do(func() { w.c.closeSession(w.sess, "writer/abort") }) }

// Wait closes stdin (if not already) and waits for the command, returning its
// error (with stderr).
func (w *StreamWriter) Wait() error {
	_ = w.stdin.Close()
	err := w.sess.Wait()
	if err == nil {
		// Even on a clean exit the dest command may have warned on stderr; surface
		// it at debug rather than discarding it on the success path (see the reader
		// Close above for the tar-warning rationale).
		if s := w.stderr.String(); s != "" {
			logx.Debug("%s: writer %q exited 0 with stderr: %s", w.name, w.cmd, s)
		}
		logx.Debug("%s: writer %q closed successfully", w.name, w.cmd)
	}
	w.once.Do(func() { w.c.closeSession(w.sess, "writer/wait") })
	if err != nil {
		return fmt.Errorf("%s: %q failed: %w (stderr: %s)", w.name, w.cmd, err, w.stderr.String())
	}
	return nil
}

// BridgeSide identifies which end of a tar bridge produced an error. Both tars can
// print the SAME text (e.g. "No such file or directory" — a vanished source member
// on the read side, or a missing path on the extract side), so a caller that must
// tell them apart (the maildir copy re-scans only on a SOURCE-side vanished file)
// needs the structural tag, not a fragile stderr-text guess.
type BridgeSide string

const (
	SideSource BridgeSide = "source"
	SideDest   BridgeSide = "dest"
)

// BridgeSideError tags a bridge error with the side that produced it. Unwrap exposes
// the cause, so errors.Is/As still reach it and the message is unchanged.
type BridgeSideError struct {
	Side BridgeSide
	Err  error
}

func (e *BridgeSideError) Error() string { return e.Err.Error() }
func (e *BridgeSideError) Unwrap() error { return e.Err }

// SideError returns the first error in err's tree tagged with side, descending into
// errors.Join branches and Unwrap chains, or nil if none. It finds a side-tagged
// error even when the OTHER side also failed (e.g. a source tar that aborts on a
// vanished member also makes the dest tar fail on the truncated archive — both are
// present, and SideError(err, SideSource) still recovers the source one).
func SideError(err error, side BridgeSide) *BridgeSideError {
	switch e := err.(type) {
	case nil:
		return nil
	case *BridgeSideError:
		if e.Side == side {
			return e
		}
		return SideError(e.Err, side)
	case interface{ Unwrap() []error }: // errors.Join
		for _, sub := range e.Unwrap() {
			if found := SideError(sub, side); found != nil {
				return found
			}
		}
		return nil
	default:
		return SideError(errors.Unwrap(err), side)
	}
}

// Bridge pipes the SOURCE reader command into the DEST writer command, i.e.
// `srcCmd | destCmd` across two hosts, with this process as the relay. Nothing
// is spilled to local disk. ctx cancels the copy.
//
// This is the tar streaming bridge: srcCmd is `tar -c ...` on the read-only
// source, destCmd is `tar -x ...` on the destination. srcEnv/destEnv carry each
// side's environment via SSH Setenv (so a secret value like MYSQL_PWD stays out of
// the command string, logs, and argv); pass nil for no env. On a server that
// rejects Setenv, non-secret keys fall back to an inline WithEnv export while
// secret keys (MYSQL_PWD) are delivered through the command's stdin — never argv.
func Bridge(ctx context.Context, src *Client, srcCmd string, srcEnv map[string]string, dst *Client, destCmd string, destEnv map[string]string) error {
	return BridgeProgress(ctx, src, srcCmd, srcEnv, nil, dst, destCmd, destEnv, nil)
}

// BridgeProgress is Bridge with an optional source stdin (e.g. a large
// `--files-from=-` list) and an optional onBytes callback for live progress.
// srcStdin/onBytes/srcEnv/destEnv may be nil.
func BridgeProgress(ctx context.Context, src *Client, srcCmd string, srcEnv map[string]string, srcStdin io.Reader, dst *Client, destCmd string, destEnv map[string]string, onBytes func(int64)) error {
	logx.Debug("bridge: starting source %q -> dest %q", srcCmd, destCmd)
	reader, err := src.StartReaderStdin(ctx, srcCmd, srcEnv, srcStdin)
	if err != nil {
		return err
	}
	writer, err := dst.StartWriter(ctx, destCmd, destEnv)
	if err != nil {
		// The source reader already started and is producing output; with no writer
		// to drain it, reader.Close()'s Wait() would block on the source tar stuck
		// writing into a full window. Abort() (immediate teardown) instead.
		reader.Abort()
		return err
	}

	var dstW io.Writer = writer
	if onBytes != nil {
		dstW = &countingWriter{w: writer, onBytes: onBytes}
	}

	copyErr := make(chan error, 1)
	go func() {
		_, err := io.Copy(dstW, reader)
		// Signal EOF to the destination tar so it can finish extracting.
		_ = writer.CloseStdin()
		copyErr <- err
	}()

	select {
	case <-ctx.Done():
		// Cancellation (e.g. Ctrl-C): close both SSH sessions IMMEDIATELY so the
		// blocked io.Copy returns at once, instead of waiting for the remote tar
		// to finish the current batch. Then drain the copy goroutine (now
		// unblocked) with a short safety timeout so we never hang on shutdown.
		reader.Abort()
		writer.Abort()
		select {
		case <-copyErr:
		case <-time.After(5 * time.Second):
		}
		return ctx.Err()
	case cerr := <-copyErr:
		if cerr != nil {
			// The relay failed. A DEST-side write error leaves the SOURCE tar still
			// producing output that nobody will read, so reader.Close()'s Wait()
			// would block on it forever. Wait() the writer first — the dest command
			// has already exited (that is why the write failed), so this returns
			// promptly with its stderr for diagnostics — then Abort() the reader
			// instead of waiting on it.
			wErr := writer.Wait()
			reader.Abort()
			if wErr != nil {
				// The dest command exited (that is why the relay write failed), so the
				// dest error is the root cause; the source was aborted unread, so this is
				// NOT a source-vanished case. Tag it dest-side and keep the relay error.
				return &BridgeSideError{Side: SideDest, Err: fmt.Errorf("bridge copy: %w (relay: %v)", wErr, cerr)}
			}
			return fmt.Errorf("bridge copy: %w", cerr)
		}
		// Clean relay (source reached EOF): drain both sides. Each tar can exit
		// non-zero INDEPENDENTLY (e.g. the dest hits disk-full/permission-denied while
		// the source only warns "file changed as we read it"; or the source aborts on a
		// vanished member and the dest then fails on the truncated archive), so surface
		// BOTH rather than dropping one — dest first, since for a migration the
		// destination failure is the actionable root cause. Each side is tagged so a
		// caller can tell a source-vanished file from a dest failure without parsing
		// stderr; each error already names its host + stderr.
		rErr := reader.Close()
		wErr := writer.Wait()
		var sideErrs []error
		if wErr != nil {
			sideErrs = append(sideErrs, &BridgeSideError{Side: SideDest, Err: wErr})
		}
		if rErr != nil {
			sideErrs = append(sideErrs, &BridgeSideError{Side: SideSource, Err: rErr})
		}
		if len(sideErrs) > 0 {
			return errors.Join(sideErrs...)
		}
		logx.Debug("bridge: completed successfully")
		return nil
	}
}

// capBuf retains a bounded view of a stream for diagnostics, keeping the HEAD and
// the TAIL when the stream is larger than the budget. tar/mysql stderr puts the
// actionable lines at BOTH ends — the first error near the top, and the final
// "tar: Exiting with failure status due to previous errors" / "ERROR ... at line
// N" summary at the very bottom — so a head-only buffer dropped exactly the
// trailing line that usually names the cause, with no sign it had truncated.
// Keeping both ends (and recording how much was dropped) means a long, noisy
// stderr never hides the lines that matter, and the truncation is always visible.
type capBuf struct {
	head    []byte
	tail    []byte
	headMax int
	tailMax int
	total   int64 // total bytes ever written, to report how much was dropped
}

// newCapBuf returns a capBuf that keeps up to max bytes, split between head and
// tail.
func newCapBuf(max int) *capBuf {
	return &capBuf{headMax: max / 2, tailMax: max - max/2}
}

func (b *capBuf) Write(p []byte) (int, error) {
	n := len(p)
	b.total += int64(n)
	if len(b.head) < b.headMax { // fill the head first
		room := b.headMax - len(b.head)
		if room > len(p) {
			room = len(p)
		}
		b.head = append(b.head, p[:room]...)
		p = p[room:]
	}
	if len(p) > 0 { // the rest slides through the tail, keeping the last tailMax bytes
		b.tail = append(b.tail, p...)
		if len(b.tail) > b.tailMax {
			b.tail = b.tail[len(b.tail)-b.tailMax:]
		}
	}
	return n, nil
}

// String returns head, then (only if bytes were actually dropped) a truncation
// marker, then tail.
func (b *capBuf) String() string {
	if len(b.tail) == 0 {
		return string(b.head)
	}
	dropped := b.total - int64(len(b.head)) - int64(len(b.tail))
	if dropped <= 0 {
		return string(b.head) + string(b.tail) // everything fit; head+tail is contiguous
	}
	return fmt.Sprintf("%s\n...[%d byte(s) omitted]...\n%s", b.head, dropped, b.tail)
}

// countingWriter forwards writes to w and reports each chunk's size via onBytes.
type countingWriter struct {
	w       io.Writer
	onBytes func(int64)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		c.onBytes(int64(n))
	}
	return n, err
}
