package events

import (
	"bytes"
	"encoding/json"
	"strings"
)

var sensitiveSubstrings = []string{
	"token", "secret", "pass", "key", "auth",
	"cred", "cookie", "session", "bearer",
}

const redactedPlaceholder = "<redacted>"

func isSensitiveKey(k string) bool {
	lower := strings.ToLower(strings.TrimSpace(k))
	for _, sub := range sensitiveSubstrings {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	return false
}

func RedactMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = redactValue(k, v)
	}
	return out
}

func redactValue(key string, v any) any {
	if isSensitiveKey(key) && !isEmptyValue(v) {
		return redactedPlaceholder
	}
	switch val := v.(type) {
	case map[string]any:
		return RedactMap(val)
	case []any:
		result := make([]any, len(val))
		for i, item := range val {
			result[i] = redactValue("", item)
		}
		return result
	}
	return v
}

func isEmptyValue(v any) bool {
	if v == nil {
		return true
	}
	if s, ok := v.(string); ok && s == "" {
		return true
	}
	return false
}

func jsonMarshal(v any) ([]byte, error) {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b, nil
}
