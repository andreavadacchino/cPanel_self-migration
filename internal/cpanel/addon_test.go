package cpanel

import (
	"os/exec"
	"strings"
	"testing"
)

// TestAddonScriptCNExtraction: the CN sed must yield ONLY the certificate's CN —
// no trailing O=/OU= RDNs and no surrounding quotes — so the subsequent
// `--resolve "$CN:2083:127.0.0.1"` gets a clean hostname instead of failing and
// degrading to `curl -k`.
func TestAddonScriptCNExtraction(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("sed"); err != nil {
		t.Skip("sed not available")
	}
	// The production script must cut at the first comma; guard against a regression
	// that drops it (which would let trailing RDNs back into the CN).
	if !strings.Contains(addonScript, `s/,.*$//`) {
		t.Fatal("addonScript no longer cuts the CN at the first comma")
	}
	const sed = `sed -E 's/.*CN ?= ?//; s/,.*$//; s/^"//; s/"$//; s/[[:space:]]*$//'`
	cases := map[string]string{
		"subject=CN = host.example.com":             "host.example.com", // newer openssl, no RDNs
		"subject=CN = host.example.com, O = cPanel": "host.example.com", // trailing RDN must be cut
		"subject= /CN=host.example.com":             "host.example.com", // older openssl layout
		`subject=CN = "host.example.com"`:           "host.example.com", // quoted value
	}
	for in, want := range cases {
		cmd := exec.Command("bash", "-c", sed)
		cmd.Stdin = strings.NewReader(in)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("sed run on %q: %v", in, err)
		}
		if got := strings.TrimRight(string(out), "\n"); got != want {
			t.Errorf("CN(%q) = %q, want %q", in, got, want)
		}
	}
}
