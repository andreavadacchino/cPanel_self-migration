package cpanel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// AddonLabel derives the subdomain "label" cPanel requires for an addon
// domain: the domain with all non-alphanumerics stripped (what the UI does).
// e.g. domain4.example -> domain4example. Pure; unit-tested.
func AddonLabel(domain string) string {
	var b strings.Builder
	for _, r := range domain {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func addonLabel(domain string) string { return AddonLabel(domain) }

// AddAddonDomain creates a REAL addon domain (own docroot, mail-capable) via
// the legacy api2 AddonDomain::addaddondomain endpoint on the local cpsrvd
// (https://127.0.0.1:2083), authenticated with the temporary token. This runs
// on the destination host (the curl must originate from 127.0.0.1). Parameters
// travel as environment variables.
func AddAddonDomain(ctx context.Context, c Runner, user string, token APIToken, domain string) error {
	// Build the full request URL (with its query string) IN GO, so every
	// parameter is correctly percent-encoded by net/url — the shell never
	// assembles the query, which avoids both broken parameters and any chance of
	// a value bleeding into the URL structure (e.g. a stray '&' or '=' in dir).
	// The URL is then passed as one opaque env var ($APIURL); the token stays in
	// the Authorization header (not the URL).
	apiURL := addonAPIURL(user, domain)
	out, err := c.RunScript(ctx, addonScript, map[string]string{
		"CPUSER": user,         // header only (cpanel <user>:<token>)
		"TOKEN":  token.Secret, // header only
		"APIURL": apiURL,
	})
	if err != nil {
		return fmt.Errorf("addon %s: %w", domain, err)
	}
	logx.Debug("AddAddonDomain %s: api2 response received (%d bytes)", domain, len(out))
	return checkAddonResponse(domain, out)
}

// addonAPIURL builds the api2 addaddondomain request URL with a properly
// URL-encoded query string. Pure; unit-tested. The token is NOT part of the URL
// (it travels in the Authorization header).
func addonAPIURL(user, domain string) string {
	q := url.Values{}
	q.Set("cpanel_jsonapi_user", user)
	q.Set("cpanel_jsonapi_apiversion", "2")
	q.Set("cpanel_jsonapi_module", "AddonDomain")
	q.Set("cpanel_jsonapi_func", "addaddondomain")
	q.Set("newdomain", domain)
	q.Set("subdomain", AddonLabel(domain))
	q.Set("dir", "public_html/"+domain)
	return "https://127.0.0.1:2083/json-api/cpanel?" + q.Encode()
}

// checkAddonResponse parses the api2 JSON and confirms success. Pure; tested.
func checkAddonResponse(domain string, out []byte) error {
	// The remote snippet prints the raw api2 JSON. Find the JSON object (the
	// curl may emit nothing else, but be defensive about leading noise).
	body := out
	if i := indexByte(body, '{'); i > 0 {
		body = body[i:]
	}
	var r api2Response
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("addon %s: parse api2 response: %w (raw: %s)", domain, err, logx.Snippet(out, 200))
	}
	if r.CPanelResult.Error != "" {
		return fmt.Errorf("addon %s: %s", domain, r.CPanelResult.Error)
	}
	if len(r.CPanelResult.Data) > 0 && r.CPanelResult.Data[0].Result.String() == "1" {
		logx.Debug("checkAddonResponse %s: addon created successfully (result=1)", domain)
		return nil
	}
	reason := ""
	if len(r.CPanelResult.Data) > 0 {
		reason = r.CPanelResult.Data[0].Reason
	}
	return fmt.Errorf("addon %s: api2 result not 1 (reason: %s)", domain, reason)
}

// AddSubdomain creates a subdomain via uapi SubDomain::addsubdomain. The label
// is the leftmost component, rooted at the rest of the domain.
func AddSubdomain(ctx context.Context, c Runner, domain string) error {
	sub, parent, ok := splitSub(domain)
	if !ok {
		return fmt.Errorf("subdomain %s: cannot split into label.parent", domain)
	}
	// data shape varies; decode as RawMessage and rely on the UAPI status.
	_, err := RunUAPI[json.RawMessage](ctx, c, "SubDomain", "addsubdomain", map[string]string{
		"domain":     sub,
		"rootdomain": parent,
		"dir":        "public_html/" + domain,
	})
	if err != nil {
		return fmt.Errorf("subdomain %s: %w", domain, err)
	}
	logx.Debug("AddSubdomain %s: created successfully", domain)
	return nil
}

// splitSub splits "label.parent.tld" into ("label", "parent.tld").
func splitSub(domain string) (label, parent string, ok bool) {
	i := strings.IndexByte(domain, '.')
	if i <= 0 || i == len(domain)-1 {
		return "", "", false
	}
	return domain[:i], domain[i+1:], true
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// addonScript runs on the destination: a single api2 curl to local cpsrvd,
// authenticated with the temporary token. The request URL (already URL-encoded
// in Go) and the credentials are read from the env; the shell does NOT assemble
// the query string, so no value can break or alter the URL.
//
// TLS: cpsrvd on 127.0.0.1:2083 usually serves a certificate valid for the
// server HOSTNAME, not for 127.0.0.1 — so a plain `curl https://127.0.0.1:2083`
// fails on a hostname mismatch even though the cert is trusted. Rather than
// blanket-disabling verification with -k, we read the hostname FROM the
// presented certificate and re-issue the request to that hostname pinned to
// 127.0.0.1 (--resolve), which keeps full TLS verification (CA + hostname) while
// still talking only to the local daemon. If the TLS handshake itself fails
// (e.g. a self-signed cpsrvd cert on some hosts, or the CN couldn't be derived),
// we fall back to -k so the migration still works, and print a notice on stderr.
// An HTTP-level API error is NOT a TLS failure, so it is passed through instead of
// triggering the insecure retry. The endpoint is always
// loopback, so even the fallback never exposes traffic off-host.
const addonScript = `set -u
CP_USER=$CPUSER
API_TOKEN=$TOKEN
API_URL=$APIURL
unset CPUSER TOKEN APIURL

curl_with_auth() {
  url=$1
  shift
  {
    printf 'header = "Authorization: cpanel '
    printf '%s' "$CP_USER" | sed 's/[\\"]/\\&/g'
    printf ':'
    printf '%s' "$API_TOKEN" | sed 's/[\\"]/\\&/g'
    printf '"\n'
  } | curl -q "$@" --config - "$url"
}

# Derive the hostname cpsrvd's cert is issued for (CN), to verify TLS properly.
CN=$(echo | openssl s_client -connect 127.0.0.1:2083 2>/dev/null \
       | openssl x509 -noout -subject 2>/dev/null \
       | sed -E 's/.*CN ?= ?//; s/,.*$//; s/^"//; s/"$//; s/[[:space:]]*$//')
# No -f here on purpose: an HTTP 4xx/5xx is a real api2 response over verified TLS,
# so let it through for Go to parse rather than failing into the insecure -k retry
# (which would RE-ISSUE the request). The fallback is for TLS/connection failures
# only — curl exits non-zero on those regardless of -f.
if [ -n "$CN" ] && curl_with_auth "${API_URL/127.0.0.1/$CN}" -sS --max-time 25 --resolve "$CN:2083:127.0.0.1" 2>/dev/null; then
  : # got a response over full TLS verification (any HTTP status)
else
  echo "addon: TLS-verified call to cpsrvd failed; retrying with -k (loopback only)" >&2
  curl_with_auth "$API_URL" -sk --max-time 25
fi
`
