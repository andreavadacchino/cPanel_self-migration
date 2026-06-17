package cpanel

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// The raw-response debug must be OFF unless explicitly enabled, so a normal run
// never logs a body that could carry a token.
func TestRawResponseDebugDefaultOff(t *testing.T) {
	if rawDebugFromEnv() {
		t.Fatal("rawDebugFromEnv() is true with CPSM_DEBUG_RAW_UAPI unset in the test env")
	}
}

func TestRawDebugFromEnv(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "on", " on "} {
		t.Setenv(rawDebugEnvVar, v)
		if !rawDebugFromEnv() {
			t.Errorf("rawDebugFromEnv() = false for %q, want true", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "off", "nope"} {
		t.Setenv(rawDebugEnvVar, v)
		if rawDebugFromEnv() {
			t.Errorf("rawDebugFromEnv() = true for %q, want false", v)
		}
	}
}

func TestRedactJSONForDebug(t *testing.T) {
	const secret = "ABCDEF-SUPER-SECRET-TOKEN-0123456789"

	// A create_full_access-shaped body: the token must vanish, every other field
	// (notably expires_at) must survive so the shape stays inspectable.
	body := `{"result":{"status":1,"data":{"name":"cpsm_x","token":"` + secret + `","expires_at":1700000000,"create_time":1699999100}}}`
	got := redactJSONForDebug([]byte(body))
	if strings.Contains(got, secret) {
		t.Fatalf("redacted output still contains the token secret: %s", got)
	}
	if !strings.Contains(got, redactedPlaceholder) {
		t.Errorf("expected the redaction placeholder in %s", got)
	}
	for _, must := range []string{`"name":"cpsm_x"`, `"expires_at":1700000000`, `"create_time":1699999100`, `"status":1`} {
		if !strings.Contains(got, must) {
			t.Errorf("redaction dropped a non-secret field %q from %s", must, got)
		}
	}
}

// Substring matching must catch credential keys beyond the obvious ones, and a
// key with surrounding whitespace must not dodge the filter.
func TestRedactJSONForDebugSubstringKeysAndWhitespace(t *testing.T) {
	const secret = "leak-me-if-you-can"
	cases := []string{
		`{"access_token":"` + secret + `"}`,
		`{"auth_token":"` + secret + `"}`,
		`{"client_secret":"` + secret + `"}`,
		`{"apikey":"` + secret + `"}`,
		`{"x-csrf-token":"` + secret + `"}`,
		`{"Bearer":"` + secret + `"}`,
		`{"session_id":"` + secret + `"}`,
		`{"sessionCookie":"` + secret + `"}`,
		`{"token ":"` + secret + `"}`,     // trailing space in key
		`{"  PASSWORD":"` + secret + `"}`, // leading space + uppercase
	}
	for _, body := range cases {
		got := redactJSONForDebug([]byte(body))
		if strings.Contains(got, secret) {
			t.Fatalf("secret leaked for %s -> %s", body, got)
		}
		if !strings.Contains(got, redactedPlaceholder) {
			t.Errorf("expected redaction for %s -> %s", body, got)
		}
	}
}

// A bare top-level JSON scalar has no key to match on, so it must be withheld
// outright rather than echoed.
func TestRedactJSONForDebugWithholdsTopLevelScalar(t *testing.T) {
	for _, body := range []string{`"a-bare-secret-string"`, `12345`, `true`} {
		got := redactJSONForDebug([]byte(body))
		if strings.Contains(got, "secret") || strings.Contains(got, "12345") || strings.Contains(got, "true") {
			t.Fatalf("top-level scalar echoed: %s -> %s", body, got)
		}
		if !strings.Contains(got, "withheld") {
			t.Errorf("expected withheld for %s -> %s", body, got)
		}
	}
}

func TestRedactJSONForDebugNestedAndArrays(t *testing.T) {
	const secret = "nested-secret-value"
	body := `{"a":[{"password":"` + secret + `","keep":1},{"x":2}],"b":{"api_token":"` + secret + `"}}`
	got := redactJSONForDebug([]byte(body))
	if strings.Contains(got, secret) {
		t.Fatalf("nested/array secret leaked: %s", got)
	}
	if !strings.Contains(got, `"keep":1`) || !strings.Contains(got, `"x":2`) {
		t.Errorf("redaction dropped non-secret fields: %s", got)
	}
}

// An empty or null secret carries nothing to hide and is diagnostically useful
// (it shows the field exists but is unset), so it is left as-is.
func TestRedactJSONForDebugKeepsEmptyAndNull(t *testing.T) {
	got := redactJSONForDebug([]byte(`{"token":"","secret":null,"name":"n"}`))
	if !strings.Contains(got, `"token":""`) {
		t.Errorf("empty token should be preserved: %s", got)
	}
	if !strings.Contains(got, `"secret":null`) {
		t.Errorf("null secret should be preserved: %s", got)
	}
	if strings.Contains(got, redactedPlaceholder) {
		t.Errorf("nothing to redact, but got a placeholder: %s", got)
	}
}

// Malformed input must never echo raw bytes (they could contain a secret).
func TestRedactJSONForDebugWithholdsNonJSON(t *testing.T) {
	raw := []byte("not json but maybe-a-secret=" + "hunter2")
	got := redactJSONForDebug(raw)
	if strings.Contains(got, "hunter2") {
		t.Fatalf("non-JSON input leaked raw bytes: %s", got)
	}
	if !strings.Contains(got, "withheld") {
		t.Errorf("expected a withheld placeholder for non-JSON input, got %s", got)
	}
}

// End-to-end: with the toggle on, RunUAPI logs the redacted body — proving the
// shape is visible (expires_at present) while the live token never reaches the
// log sink.
func TestRunUAPIRawDebugLogsRedactedBody(t *testing.T) {
	defer func(prev bool) { rawResponseDebug = prev }(rawResponseDebug)
	rawResponseDebug = true

	var buf bytes.Buffer
	restore := logx.SwapDebugOutput(&buf)
	defer restore()
	logx.SetDebug(true)
	defer logx.SetDebug(false)

	const secret = "LIVE-TOKEN-do-not-log-me"
	f := &fakeRunner{out: uapiOK(`{"name":"cpsm_x","token":"` + secret + `","expires_at":1700000000}`)}
	if _, err := RunUAPI[CreateTokenData](bg, f, "Tokens", "create_full_access", map[string]string{"name": "cpsm_x"}); err != nil {
		t.Fatalf("RunUAPI: %v", err)
	}
	logged := buf.String()
	if strings.Contains(logged, secret) {
		t.Fatalf("the live token leaked into the debug log:\n%s", logged)
	}
	if !strings.Contains(logged, "raw response (secrets redacted)") {
		t.Fatalf("expected the redacted raw-response debug line, got:\n%s", logged)
	}
	if !strings.Contains(logged, `"expires_at":1700000000`) {
		t.Errorf("expected expires_at visible in the redacted body, got:\n%s", logged)
	}
}

// With the toggle OFF, RunUAPI must NOT emit the raw body at all.
func TestRunUAPIRawDebugOffEmitsNoBody(t *testing.T) {
	defer func(prev bool) { rawResponseDebug = prev }(rawResponseDebug)
	rawResponseDebug = false

	var buf bytes.Buffer
	restore := logx.SwapDebugOutput(&buf)
	defer restore()
	logx.SetDebug(true)
	defer logx.SetDebug(false)

	const secret = "LIVE-TOKEN-off-path"
	f := &fakeRunner{out: uapiOK(`{"name":"cpsm_x","token":"` + secret + `","expires_at":1700000000}`)}
	if _, err := RunUAPI[CreateTokenData](bg, f, "Tokens", "create_full_access", map[string]string{"name": "cpsm_x"}); err != nil {
		t.Fatalf("RunUAPI: %v", err)
	}
	logged := buf.String()
	if strings.Contains(logged, secret) || strings.Contains(logged, "raw response") {
		t.Fatalf("raw body emitted while toggle was off:\n%s", logged)
	}
}

// TestRunUAPIErrorPathRedactsEchoedSecret (M1): on a FAILURE response (status != 1)
// the debug log must REDACT the raw body — some cPanel builds echo request input back
// into the response, so a create_user/add_pop failure could carry the password/hash.
// The raw body is no longer dumped verbatim; the diagnostic shape stays.
func TestRunUAPIErrorPathRedactsEchoedSecret(t *testing.T) {
	var buf bytes.Buffer
	restore := logx.SwapDebugOutput(&buf)
	defer restore()
	logx.SetDebug(true)
	defer logx.SetDebug(false)

	const secret = "ECHOED-PASSWORD-do-not-log"
	body := `{"result":{"status":0,"errors":["create_user failed"],"data":{"password":"` + secret + `"}}}`
	f := &fakeRunner{out: []byte(body)}
	if _, err := RunUAPI[anyData](bg, f, "Mysql", "create_user", map[string]string{"name": "u", "password": secret}); err == nil {
		t.Fatal("expected an error for status != 1")
	}
	logged := buf.String()
	if strings.Contains(logged, secret) {
		t.Fatalf("echoed password leaked into the debug log:\n%s", logged)
	}
	if !strings.Contains(logged, "non-success") {
		t.Fatalf("expected the non-success debug line, got:\n%s", logged)
	}
	if !strings.Contains(logged, redactedPlaceholder) {
		t.Fatalf("expected the password value replaced by the redaction placeholder, got:\n%s", logged)
	}
}
