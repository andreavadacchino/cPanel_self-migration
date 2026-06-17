package sshx

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// erroringReader yields afterN one-byte reads, then fails with err.
type erroringReader struct {
	err    error
	afterN int
	n      int
}

func (e *erroringReader) Read(p []byte) (int, error) {
	if e.n >= e.afterN {
		return 0, e.err
	}
	e.n++
	p[0] = 'x'
	return 1, nil
}

func TestErrCapReaderRecordsReadError(t *testing.T) {
	want := errors.New("source list read error")
	ecr := &errCapReader{r: &erroringReader{err: want, afterN: 3}}
	_, _ = io.Copy(io.Discard, ecr)
	// The whole point: a truncated source-list read is recorded, so Close() can
	// fail the transfer instead of letting tar silently archive fewer files.
	if !errors.Is(ecr.err, want) {
		t.Errorf("ecr.err = %v, want %v", ecr.err, want)
	}
}

func TestErrCapReaderIgnoresEOF(t *testing.T) {
	ecr := &errCapReader{r: strings.NewReader("path/one\npath/two\n")}
	_, _ = io.Copy(io.Discard, ecr)
	if ecr.err != nil {
		t.Errorf("a clean EOF must not be recorded as an error, got %v", ecr.err)
	}
}
