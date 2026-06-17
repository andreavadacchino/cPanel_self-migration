package cpanel

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/model"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func TestParseListDomains(t *testing.T) {
	data, err := parseUAPI[ListDomainsData]("DomainInfo", "list_domains", fixture(t, "domaininfo_list.json"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	domains := domainsFromList(data)

	// Sorted alphabetically by name (deterministic order).
	want := []model.Domain{
		{Name: "addon1.example", Type: model.Addon},
		{Name: "domain3.example", Type: model.Addon},
		{Name: "domain4.example", Type: model.Addon},
		{Name: "main.example", Type: model.Main},
		{Name: "site2.example", Type: model.Addon},
		{Name: "sub1.example", Type: model.Sub},
	}
	if len(domains) != len(want) {
		t.Fatalf("got %d domains, want %d: %+v", len(domains), len(want), domains)
	}
	for i := range want {
		if domains[i] != want[i] {
			t.Errorf("domain[%d] = %+v, want %+v", i, domains[i], want[i])
		}
	}
}

func TestParseAddPopOK(t *testing.T) {
	// add_pop has empty data; success is purely status==1.
	if _, err := parseUAPI[struct{}]("Email", "add_pop", fixture(t, "add_pop_ok.json")); err != nil {
		t.Errorf("add_pop_ok should parse as success: %v", err)
	}
}

func TestParseAddPopDup(t *testing.T) {
	_, err := parseUAPI[struct{}]("Email", "add_pop", fixture(t, "add_pop_dup.json"))
	if err == nil {
		t.Fatal("add_pop_dup should be an error (status 0)")
	}
	if want := "already exists"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q should mention %q", err.Error(), want)
	}
}

func TestParseToken(t *testing.T) {
	data, err := parseUAPI[CreateTokenData]("Tokens", "create_full_access", fixture(t, "tokens_create.json"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if data.Token != "ABCDEF0123456789ABCDEF0123456789" {
		t.Errorf("token = %q", data.Token)
	}
	if data.Name != "cpsm_12345_apply" {
		t.Errorf("name = %q", data.Name)
	}
	inline := []byte(`{"result":{"status":1,"data":{"name":"cpsm_x","token":"TOK","create_time":1717500000,"expires_at":1717500900}}}`)
	nonzero, err := parseUAPI[CreateTokenData]("Tokens", "create_full_access", inline)
	if err != nil {
		t.Fatalf("parse inline token: %v", err)
	}
	if nonzero.ExpiresAt != 1717500900 {
		t.Errorf("expires_at = %d, want 1717500900", nonzero.ExpiresAt)
	}
}

func TestCheckAddonResponse(t *testing.T) {
	if err := checkAddonResponse("domain4.example", fixture(t, "addon_ok.json")); err != nil {
		t.Errorf("addon_ok should succeed: %v", err)
	}
	if err := checkAddonResponse("domain4.example", fixture(t, "addon_fail.json")); err == nil {
		t.Error("addon_fail should error")
	}
}

func TestAddonLabel(t *testing.T) {
	cases := map[string]string{
		"domain4.example": "domain4example",
		"addon1.example":  "addon1example",
		"domain3.example": "domain3example",
		"sub1.example":    "sub1example",
	}
	for in, want := range cases {
		if got := addonLabel(in); got != want {
			t.Errorf("addonLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAddonLabelCollisionExamples(t *testing.T) {
	cases := []struct {
		a, b string
		want string
	}{
		{"my-site.example", "mysite.example", "mysiteexample"},
		{"shop.example.com", "shop-example.com", "shopexamplecom"},
	}
	for _, c := range cases {
		if got := AddonLabel(c.a); got != c.want {
			t.Fatalf("AddonLabel(%q) = %q, want %q", c.a, got, c.want)
		}
		if got := AddonLabel(c.b); got != c.want {
			t.Fatalf("AddonLabel(%q) = %q, want %q", c.b, got, c.want)
		}
		if got := addonSubdomainParam(t, c.a); got != c.want {
			t.Fatalf("addonAPIURL(%q) subdomain = %q, want %q", c.a, got, c.want)
		}
		if got := addonSubdomainParam(t, c.b); got != c.want {
			t.Fatalf("addonAPIURL(%q) subdomain = %q, want %q", c.b, got, c.want)
		}
	}
}

func addonSubdomainParam(t *testing.T, domain string) string {
	t.Helper()
	u, err := url.Parse(addonAPIURL("user", domain))
	if err != nil {
		t.Fatalf("parse addonAPIURL(%q): %v", domain, err)
	}
	return u.Query().Get("subdomain")
}

func TestRandomTokenName(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		n, err := RandomTokenName()
		if err != nil {
			t.Fatalf("RandomTokenName: %v", err)
		}
		if !strings.Contains(n, TokenNamePrefix) {
			t.Errorf("name %q must carry the tool prefix %q", n, TokenNamePrefix)
		}
		// Random suffix must not be the old predictable cpsm_<pid>_apply shape.
		if strings.Contains(n, "_apply") {
			t.Errorf("name %q should not use the old predictable form", n)
		}
		if seen[n] {
			t.Errorf("duplicate token name generated: %q (must be unpredictable/unique)", n)
		}
		seen[n] = true
	}
}

func TestLeftoverToolTokens(t *testing.T) {
	in := []string{
		"cpsm_deadbeef",     // ours
		"cpsm_aabbccdd",     // ours
		"my-personal-token", // not ours — must be left alone
		"backup-key",        // not ours
		"cpsm_old_apply",    // ours (legacy shape, still prefixed)
	}
	got := LeftoverToolTokens(in)
	want := []string{"cpsm_deadbeef", "cpsm_aabbccdd", "cpsm_old_apply"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d]=%q, want %q", i, got[i], want[i])
		}
	}
	// A user's own tokens must NEVER be selected for revocation.
	for _, n := range got {
		if n == "my-personal-token" || n == "backup-key" {
			t.Errorf("non-tool token %q must not be flagged for revoke", n)
		}
	}
}

func TestAddonAPIURL(t *testing.T) {
	got := addonAPIURL("srcacct", "domain4.example")

	// Must be the local cpsrvd json-api endpoint.
	if !strings.Contains(got, "https://127.0.0.1:2083/json-api/cpanel?") {
		t.Errorf("unexpected base URL: %s", got)
	}
	// All required api2 parameters present and correctly carried.
	for _, want := range []string{
		"cpanel_jsonapi_user=srcacct",
		"cpanel_jsonapi_apiversion=2",
		"cpanel_jsonapi_module=AddonDomain",
		"cpanel_jsonapi_func=addaddondomain",
		"newdomain=domain4.example",
		"subdomain=domain4example",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("URL missing %q: %s", want, got)
		}
	}
	// The dir parameter must be URL-ENCODED ("/" -> %2F), not raw.
	if !strings.Contains(got, "dir=public_html%2Fdomain4.example") {
		t.Errorf("dir not URL-encoded (expected public_html%%2Fdomain4.example): %s", got)
	}
	if strings.Contains(got, "dir=public_html/domain4.example") {
		t.Errorf("dir must not contain a raw slash: %s", got)
	}
	// The token must NEVER appear in the URL (it goes in the Authorization header).
	if strings.Contains(got, "token") || strings.Contains(got, "Authorization") {
		t.Errorf("token/credentials must not be in the URL: %s", got)
	}
}

func TestSplitSub(t *testing.T) {
	sub, parent, ok := splitSub("sub1.example")
	if !ok || sub != "sub1" || parent != "example" {
		t.Errorf("splitSub = (%q,%q,%v)", sub, parent, ok)
	}
	if _, _, ok := splitSub("nodot"); ok {
		t.Error("splitSub(nodot) should fail")
	}
}

func TestUAPIArgsScriptStableOrder(t *testing.T) {
	script, env := uapiArgsScript("Email", "add_pop", map[string]string{
		"email": "info", "domain": "x.it", "password_hash": "$6$abc",
	})
	// keys sorted: domain, email, password_hash -> ARG_0, ARG_1, ARG_2
	if env["ARG_2"] != "$6$abc" {
		t.Errorf("password_hash should map to ARG_2, env=%v", env)
	}
	if !strings.Contains(script, `uapi --output=json Email add_pop`) {
		t.Errorf("script prefix wrong: %q", script)
	}
	// The hash must NOT appear inline in the script (only $ARG_2 reference).
	if strings.Contains(script, "$6$abc") {
		t.Errorf("hash leaked into script command line: %q", script)
	}
}
