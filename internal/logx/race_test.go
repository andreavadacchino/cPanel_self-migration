package logx

import (
	"bytes"
	"sync"
	"testing"
)

// TestConcurrentLoggingIsRaceFree drives ONE Logger — and the Progress lines it
// creates — from many goroutines at once, writing to a (non-concurrent)
// bytes.Buffer. Under `go test -race` this FAILS on a Logger whose writes are not
// serialized: the pre-fix code took no lock in the Logger's write methods (so the
// shared writer and the cur counter raced) and gave each Progress its OWN mutex
// (which never coordinated with the Logger's writes). With the single shared
// Logger mutex it is race-free.
func TestConcurrentLoggingIsRaceFree(t *testing.T) {
	var buf bytes.Buffer
	l := NewTo(&buf, 1000)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(4)
		go func() { defer wg.Done(); l.Step("step") }()           // writes buf AND cur++
		go func() { defer wg.Done(); l.Warn("warn") }()           // writes buf
		go func() { defer wg.Done(); l.Detail("detail %d", 1) }() // writes buf
		go func() {
			defer wg.Done()
			p := l.NewInlineProgress("item", 100)
			p.Add(10)
			p.SetSuffix("x")
			p.Replace("result") // writes buf via the SAME (shared) mutex
		}()
	}
	wg.Wait()

	if buf.Len() == 0 {
		t.Fatal("expected some logged output")
	}
}
