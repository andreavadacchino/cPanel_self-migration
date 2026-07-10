package events

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Writer struct {
	mu   sync.Mutex
	f    *os.File
	enc  *json.Encoder
	path string
}

func NewWriter(path string) (*Writer, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("events: create directory %s: %w", dir, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("events: open %s: %w", path, err)
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	return &Writer{f: f, enc: enc, path: path}, nil
}

func (w *Writer) Write(ev Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	safe := ev
	safe.FormatVersion = CurrentFormatVersion
	safe.Data = redactData(safe.Data)
	if err := w.enc.Encode(safe); err != nil {
		return fmt.Errorf("events: write: %w", err)
	}
	return nil
}

// redactData routes ANY Data payload through the key-based redaction net,
// not just map[string]any: a typed struct payload (e.g. the apply phase
// events) is marshaled to its JSON object form and redacted as a map, so a
// future payload with a sensitive field name cannot silently bypass
// RedactMap. Non-object payloads (arrays, scalars) and payloads that fail
// to marshal pass through unchanged — redaction is KEY-based, so there is
// no key to match on them (a marshal failure surfaces identically at
// Encode time anyway).
func redactData(v any) any {
	switch d := v.(type) {
	case nil:
		return nil
	case map[string]any:
		return RedactMap(d)
	}
	b, err := jsonMarshal(v)
	if err != nil {
		return v
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return v
	}
	return RedactMap(m)
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

func (w *Writer) Path() string {
	return w.path
}
