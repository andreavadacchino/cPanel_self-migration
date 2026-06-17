package cpanel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseUAPINoRawLeak guards the fix where the generic UAPI parse error embedded
// the raw response — which for Tokens::create_full_access leaks the API token.
func TestParseUAPINoRawLeak(t *testing.T) {
	secret := "SECRETTOKEN1234567890"
	bad := []byte(`{"result":{"data":{"token":"` + secret + `"} BROKEN`)
	_, err := parseUAPI[map[string]any]("Tokens", "create_full_access", bad)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error leaks the raw response (would expose the token): %q", err.Error())
	}
	if !strings.Contains(err.Error(), "bytes") {
		t.Errorf("error should report the safe length, got: %q", err.Error())
	}
}

// TestAddonScriptNoFFlag guards the fix where the first (TLS-verified) addon curl
// used -f, so an HTTP API error fell into the insecure -k retry that re-issued the
// request.
func TestAddonScriptNoFFlag(t *testing.T) {
	if !strings.Contains(addonScript, " -sS ") {
		t.Error("addonScript's first curl should use -sS")
	}
	if strings.Contains(addonScript, "curl -fsS") {
		t.Error("addonScript's first curl must NOT use -f")
	}
	if strings.Contains(addonScript, `-H "$AUTH"`) || strings.Contains(addonScript, "AUTH=") {
		t.Error("addonScript must not put the Authorization header in curl argv")
	}
	if !strings.Contains(addonScript, "--config -") {
		t.Error("addonScript should feed the Authorization header through curl config stdin")
	}
}

func TestAddonScriptFeedsAuthThroughCurlConfig(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "curl.argv")
	stdinPath := filepath.Join(dir, "curl.stdin")
	if err := os.WriteFile(filepath.Join(dir, "openssl"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	curl := `#!/bin/sh
printf '%s\n' "$@" > "$CURL_ARGV"
cat > "$CURL_STDIN"
printf '{"cpanelresult":{"data":[{"result":"1","reason":"ok"}],"event":{"result":"1"}}}\n'
`
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(curl), 0o755); err != nil {
		t.Fatal(err)
	}

	secret := "TOK_SHOULD_ONLY_BE_IN_CONFIG_STDIN"
	cmd := exec.Command("bash", "-c", addonScript)
	cmd.Env = append(os.Environ(),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"CPUSER=destacct",
		"TOKEN="+secret,
		"APIURL=https://127.0.0.1:2083/json-api/cpanel?newdomain=site.example",
		"CURL_ARGV="+argvPath,
		"CURL_STDIN="+stdinPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("addonScript failed: %v\n%s", err, out)
	}
	argvRaw, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatal(err)
	}
	stdinRaw, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	argv := string(argvRaw)
	config := string(stdinRaw)
	if strings.Contains(argv, secret) {
		t.Fatalf("curl argv leaked token secret:\n%s", argv)
	}
	if strings.Contains(string(out), secret) {
		t.Fatalf("addonScript output leaked token secret:\n%s", out)
	}
	if !strings.Contains(argv, "--config\n-\n") {
		t.Fatalf("curl argv should use --config -:\n%s", argv)
	}
	wantHeader := `header = "Authorization: cpanel destacct:` + secret + `"`
	if !strings.Contains(config, wantHeader) {
		t.Fatalf("curl config stdin missing Authorization header %q:\n%s", wantHeader, config)
	}
}

func TestTemporaryTokenCommentsDoNotClaimNoExpiry(t *testing.T) {
	files := []string{"token.go", filepath.Join("..", "migrate", "apply_domains.go")}
	banned := []string{"no expiry argument", "cannot scope/expire", "cannot scope or time-limit", "cannot be time-limited"}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		text := string(raw)
		for _, phrase := range banned {
			if strings.Contains(text, phrase) {
				t.Fatalf("%s still contains stale token-expiry claim %q", file, phrase)
			}
		}
	}
}
