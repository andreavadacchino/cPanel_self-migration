package events

import (
	"strings"
	"testing"
)

func TestRedactMap(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		contains []string
		excludes []string
	}{
		{
			name:     "password redacted",
			input:    map[string]any{"password": "secret123", "user": "admin"},
			contains: []string{"<redacted>", "admin"},
			excludes: []string{"secret123"},
		},
		{
			name:     "token redacted",
			input:    map[string]any{"api_token": "tok_abc", "ip": "1.2.3.4"},
			contains: []string{"<redacted>", "1.2.3.4"},
			excludes: []string{"tok_abc"},
		},
		{
			name:     "nested password",
			input:    map[string]any{"config": map[string]any{"db_password": "pw123"}},
			contains: []string{"<redacted>"},
			excludes: []string{"pw123"},
		},
		{
			name:     "key containing secret",
			input:    map[string]any{"client_secret": "s3cr3t"},
			contains: []string{"<redacted>"},
			excludes: []string{"s3cr3t"},
		},
		{
			name:     "auth header",
			input:    map[string]any{"authorization": "Bearer xyz"},
			contains: []string{"<redacted>"},
			excludes: []string{"Bearer xyz"},
		},
		{
			name:     "credential key",
			input:    map[string]any{"credentials": "user:pass"},
			contains: []string{"<redacted>"},
			excludes: []string{"user:pass"},
		},
		{
			name:     "safe keys unchanged",
			input:    map[string]any{"ip": "1.2.3.4", "user": "admin", "domain": "example.com"},
			contains: []string{"1.2.3.4", "admin", "example.com"},
		},
		{
			name:     "empty password preserved",
			input:    map[string]any{"password": ""},
			contains: []string{`"password":""`},
		},
		{
			name:     "nil password preserved",
			input:    map[string]any{"password": nil},
			contains: []string{`"password":null`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactMap(tt.input)
			s := mapToString(got)
			for _, want := range tt.contains {
				if !strings.Contains(s, want) {
					t.Errorf("result missing %q: %s", want, s)
				}
			}
			for _, bad := range tt.excludes {
				if strings.Contains(s, bad) {
					t.Errorf("result contains secret %q: %s", bad, s)
				}
			}
		})
	}
}

func TestIsSensitiveKey(t *testing.T) {
	sensitive := []string{
		"password", "Password", "DB_PASSWORD",
		"token", "api_token", "cpanel_token",
		"secret", "client_secret",
		"ssh_pass", "passphrase",
		"auth", "authorization",
		"key", "api_key", "private_key",
		"credential", "credentials",
		"cookie", "session_cookie",
		"session", "session_id",
		"bearer",
	}
	for _, k := range sensitive {
		if !isSensitiveKey(k) {
			t.Errorf("isSensitiveKey(%q) = false, want true", k)
		}
	}

	safe := []string{
		"ip", "user", "domain", "port", "host",
		"mailbox", "database", "docroot", "path",
		"count", "size", "status", "version",
	}
	for _, k := range safe {
		if isSensitiveKey(k) {
			t.Errorf("isSensitiveKey(%q) = true, want false", k)
		}
	}
}

func mapToString(m map[string]any) string {
	b, _ := jsonMarshal(m)
	return string(b)
}
