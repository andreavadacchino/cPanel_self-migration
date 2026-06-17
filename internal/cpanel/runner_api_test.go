package cpanel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
)

// fakeRunner is a canned-response Runner: it records the script/env it was asked
// to run and returns a fixed output/error — so every Runner-based call can be
// exercised without SSH or a real cPanel (this is the seam the Runner interface
// exists for).
type fakeRunner struct {
	out    []byte
	err    error
	script string
	env    map[string]string
}

func (f *fakeRunner) RunScript(_ context.Context, script string, env map[string]string) ([]byte, error) {
	f.script, f.env = script, env
	return f.out, f.err
}

type runCall struct {
	script string
	env    map[string]string
}

type runResult struct {
	out []byte
	err error
}

type sequenceRunner struct {
	results []runResult
	calls   []runCall
}

func (s *sequenceRunner) RunScript(_ context.Context, script string, env map[string]string) ([]byte, error) {
	envCopy := map[string]string{}
	for k, v := range env {
		envCopy[k] = v
	}
	s.calls = append(s.calls, runCall{script: script, env: envCopy})
	i := len(s.calls) - 1
	if i >= len(s.results) {
		return nil, fmt.Errorf("unexpected RunScript call %d: %s", i, script)
	}
	return s.results[i].out, s.results[i].err
}

// uapiOK / uapiFail build a UAPI envelope with the given data / failure.
func uapiOK(data string) []byte  { return []byte(`{"result":{"status":1,"data":` + data + `}}`) }
func uapiFail(msg string) []byte { return []byte(`{"result":{"status":0,"errors":["` + msg + `"]}}`) }

var bg = context.Background()

// --- api.go: RunUAPI command-building + parse + error propagation ---

func TestRunUAPISuccessBuildsCommandAndParses(t *testing.T) {
	f := &fakeRunner{out: uapiOK(`{"token":"SECRET","name":"n"}`)}
	got, err := RunUAPI[CreateTokenData](bg, f, "Tokens", "create_full_access", map[string]string{"name": "cpsm_x"})
	if err != nil {
		t.Fatalf("RunUAPI: %v", err)
	}
	if got.Token != "SECRET" {
		t.Errorf("data.Token = %q, want SECRET", got.Token)
	}
	if !strings.Contains(f.script, "uapi --output=json Tokens create_full_access") {
		t.Errorf("script = %q, want the uapi invocation", f.script)
	}
	if !strings.Contains(f.script, `name="$ARG_0"`) || f.env["ARG_0"] != "cpsm_x" {
		t.Errorf("arg not passed via env: script=%q env=%v", f.script, f.env)
	}
}

func TestRunUAPIPropagatesSSHError(t *testing.T) {
	f := &fakeRunner{err: context.DeadlineExceeded}
	if _, err := RunUAPI[json.RawMessage](bg, f, "Mod", "fn", nil); err == nil {
		t.Error("RunUAPI must propagate the RunScript error")
	}
}

func TestParseUAPIStatusAndJSONErrors(t *testing.T) {
	// status != 1 -> error including the reported messages.
	if _, err := parseUAPI[json.RawMessage]("M", "f", uapiFail("boom")); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("parseUAPI(status 0) = %v, want an error mentioning 'boom'", err)
	}
	// unparseable JSON -> error.
	if _, err := parseUAPI[json.RawMessage]("M", "f", []byte("not json")); err == nil {
		t.Error("parseUAPI(bad json) must error")
	}
}

// --- domains.go ---

func TestListDomainsSortedWithTypes(t *testing.T) {
	f := &fakeRunner{out: uapiOK(`{"main_domain":"main.it","addon_domains":["b.it","a.it"],"sub_domains":["s.main.it"],"parked_domains":["p.it"]}`)}
	got, err := ListDomains(bg, f)
	if err != nil {
		t.Fatalf("ListDomains: %v", err)
	}
	// Sorted alphabetically: a.it, b.it, main.it, p.it, s.main.it.
	wantNames := []string{"a.it", "b.it", "main.it", "p.it", "s.main.it"}
	if len(got) != len(wantNames) {
		t.Fatalf("got %d domains, want %d: %+v", len(got), len(wantNames), got)
	}
	for i, n := range wantNames {
		if got[i].Name != n {
			t.Errorf("domain[%d] = %q, want %q", i, got[i].Name, n)
		}
	}
	// Type carried through for the main domain.
	for _, d := range got {
		if d.Name == "main.it" && d.Type != model.Main {
			t.Errorf("main.it type = %v, want Main", d.Type)
		}
		if d.Name == "a.it" && d.Type != model.Addon {
			t.Errorf("a.it type = %v, want Addon", d.Type)
		}
	}
}

func TestListDomainsError(t *testing.T) {
	f := &fakeRunner{out: uapiFail("nope")}
	if _, err := ListDomains(bg, f); err == nil {
		t.Error("ListDomains must surface a UAPI failure")
	}
}

func TestDomainNameSet(t *testing.T) {
	set := DomainNameSet([]model.Domain{{Name: "a.it"}, {Name: "B.IT"}, {Name: "shop.example.com."}, {Name: "XN--MNCHEN-3YA.DE"}})
	if !set["a.it"] || !set["b.it"] || !set["shop.example.com"] || !set["xn--mnchen-3ya.de"] || set["c.it"] {
		t.Errorf("DomainNameSet = %v", set)
	}
}

// --- domains_data.go ---

func TestListDocrootsFlattenedSorted(t *testing.T) {
	f := &fakeRunner{out: uapiOK(`{
		"main_domain":{"domain":"main.it","documentroot":"/home/u/public_html","type":"main_domain"},
		"addon_domains":[{"domain":"b.it","documentroot":"/home/u/b.it","type":"addon_domain"}],
		"sub_domains":[{"domain":"a.sub.it","documentroot":"/home/u/sub","type":"sub_domain"}],
		"parked_domains":[]}`)}
	got, err := ListDocroots(bg, f)
	if err != nil {
		t.Fatalf("ListDocroots: %v", err)
	}
	want := []string{"a.sub.it", "b.it", "main.it"} // sorted by domain
	if len(got) != 3 {
		t.Fatalf("got %d docroots, want 3: %+v", len(got), got)
	}
	for i, n := range want {
		if got[i].Domain != n {
			t.Errorf("docroot[%d] = %q, want %q", i, got[i].Domain, n)
		}
	}
	if got[2].DocumentRoot != "/home/u/public_html" {
		t.Errorf("main docroot = %q", got[2].DocumentRoot)
	}
}

// --- mysql.go ---

func TestListDatabasesSortedFlexDiskUsage(t *testing.T) {
	// disk_usage arrives as a STRING for one and a NUMBER for the other — flexInt64
	// must decode both without failing the whole response.
	f := &fakeRunner{out: uapiOK(`[{"database":"u_z","disk_usage":"2048","users":["u1"]},{"database":"u_a","disk_usage":1024,"users":[]}]`)}
	got, err := ListDatabases(bg, f)
	if err != nil {
		t.Fatalf("ListDatabases: %v", err)
	}
	if len(got) != 2 || got[0].Database != "u_a" || got[1].Database != "u_z" {
		t.Fatalf("databases not sorted by name: %+v", got)
	}
	if got[0].DiskUsage != 1024 || got[1].DiskUsage != 2048 {
		t.Errorf("disk usage = %d/%d, want 1024/2048", got[0].DiskUsage, got[1].DiskUsage)
	}
}

func TestListDBUsersSorted(t *testing.T) {
	f := &fakeRunner{out: uapiOK(`[{"user":"u_z","shortuser":"z","databases":[]},{"user":"u_a","shortuser":"a","databases":["u_x"]}]`)}
	got, err := ListDBUsers(bg, f)
	if err != nil {
		t.Fatalf("ListDBUsers: %v", err)
	}
	if len(got) != 2 || got[0].User != "u_a" || got[1].User != "u_z" {
		t.Errorf("DB users not sorted: %+v", got)
	}
}

func TestGetMySQLRestrictionsParsesPrefixAndLimits(t *testing.T) {
	f := &fakeRunner{out: uapiOK(`{"max_database_name_length":64,"max_username_length":16,"prefix":"destina_"}`)}
	got, err := GetMySQLRestrictions(bg, f)
	if err != nil {
		t.Fatalf("GetMySQLRestrictions: %v", err)
	}
	if got.Prefix == nil || *got.Prefix != "destina_" {
		t.Fatalf("Prefix = %v, want destina_", got.Prefix)
	}
	if got.MaxDatabaseNameLength != 64 || got.MaxUsernameLength != 16 {
		t.Errorf("limits = db:%d user:%d, want 64/16", got.MaxDatabaseNameLength, got.MaxUsernameLength)
	}
	if !strings.Contains(f.script, "uapi --output=json Mysql get_restrictions") {
		t.Errorf("script = %q, want Mysql get_restrictions", f.script)
	}
}

func TestGetMySQLRestrictionsAllowsDisabledPrefix(t *testing.T) {
	f := &fakeRunner{out: uapiOK(`{"max_database_name_length":64,"max_username_length":16,"prefix":null}`)}
	got, err := GetMySQLRestrictions(bg, f)
	if err != nil {
		t.Fatalf("GetMySQLRestrictions: %v", err)
	}
	if got.Prefix != nil {
		t.Fatalf("Prefix = %q, want nil for disabled prefixing", *got.Prefix)
	}
}

func TestMysqlWriteOpsPassArgsAndSucceed(t *testing.T) {
	// Each write op must succeed on status:1 and carry its args via env.
	f := &fakeRunner{out: uapiOK(`null`)}
	if err := CreateDatabase(bg, f, "vh_db"); err != nil {
		t.Errorf("CreateDatabase: %v", err)
	}
	if f.env["ARG_0"] != "vh_db" {
		t.Errorf("CreateDatabase arg = %v, want vh_db", f.env)
	}
	if err := CreateDBUser(bg, f, "vh_user", "pw"); err != nil {
		t.Errorf("CreateDBUser: %v", err)
	}
	if err := SetPrivilegesOnDatabase(bg, f, "vh_user", "vh_db"); err != nil {
		t.Errorf("SetPrivilegesOnDatabase: %v", err)
	}
	if !strings.Contains(f.script, "set_privileges_on_database") {
		t.Errorf("script = %q", f.script)
	}
	if err := SetDBUserPassword(bg, f, "vh_user", "newpw"); err != nil {
		t.Errorf("SetDBUserPassword: %v", err)
	}
}

func TestMysqlWriteOpFailure(t *testing.T) {
	f := &fakeRunner{out: uapiFail("exists")}
	if err := CreateDatabase(bg, f, "vh_db"); err == nil {
		t.Error("CreateDatabase must surface a UAPI failure")
	}
}

// --- token.go ---

func TestCreateFullAccessToken(t *testing.T) {
	expiryUnix := time.Now().Add(time.Hour).Unix()
	expiry := time.Unix(expiryUnix, 0)
	f := &fakeRunner{out: uapiOK(fmt.Sprintf(`{"name":"cpsm_x","token":"TOK","expires_at":%d,"create_time":0}`, expiryUnix))}
	tok, err := CreateFullAccessToken(bg, f, "cpsm_x", expiry)
	if err != nil || tok.Secret != "TOK" || tok.Name != "cpsm_x" || tok.ExpiresAt != expiryUnix {
		t.Fatalf("CreateFullAccessToken = %q, %v; want TOK", tok, err)
	}
	if !strings.Contains(f.script, `expires_at="$ARG_0"`) || !strings.Contains(f.script, `name="$ARG_1"`) {
		t.Fatalf("CreateFullAccessToken script missing env-backed expires_at/name args: %q", f.script)
	}
	if f.env["ARG_0"] != fmt.Sprint(expiryUnix) || f.env["ARG_1"] != "cpsm_x" {
		t.Fatalf("CreateFullAccessToken env = %+v, want expires_at/name args", f.env)
	}
	if strings.Contains(f.script, fmt.Sprint(expiryUnix)) {
		t.Fatalf("expires_at value must not be interpolated into script: %q", f.script)
	}
}

func TestCreateFullAccessTokenInvalidResponsesRevoke(t *testing.T) {
	expiryUnix := time.Now().Add(time.Hour).Unix()
	expiry := time.Unix(expiryUnix, 0)
	revokeOK := []byte(`{"result":{"status":1,"data":1}}`)
	cases := []struct {
		name       string
		data       string
		wantRevoke string
	}{
		{
			name:       "empty token",
			data:       fmt.Sprintf(`{"name":"cpsm_returned","token":"","expires_at":%d}`, expiryUnix),
			wantRevoke: "cpsm_returned",
		},
		{
			name:       "past expiry",
			data:       fmt.Sprintf(`{"name":"cpsm_x","token":"TOK","expires_at":%d}`, time.Now().Add(-time.Minute).Unix()),
			wantRevoke: "cpsm_x",
		},
		{
			name:       "excessive expiry",
			data:       fmt.Sprintf(`{"name":"cpsm_x","token":"TOK","expires_at":%d}`, expiry.Add(2*time.Minute).Unix()),
			wantRevoke: "cpsm_x",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &sequenceRunner{results: []runResult{
				{out: uapiOK(tc.data)},
				{out: revokeOK},
			}}
			if _, err := CreateFullAccessToken(bg, r, "cpsm_x", expiry); err == nil {
				t.Fatal("CreateFullAccessToken must fail closed on invalid token response")
			}
			requireRevokeCall(t, r, tc.wantRevoke)
		})
	}
}

// When the host returns a valid token but ignores the requested expiry
// (expires_at == 0), CreateFullAccessToken WARNS and proceeds with the token
// rather than failing — and does NOT revoke it itself (the caller revokes it
// immediately after use). This keeps addon-domain creation working on cPanel
// builds that do not support user-token expiry. See docs/DEBUGGING.md §3.
func TestCreateFullAccessTokenIgnoredExpiryWarnsAndProceeds(t *testing.T) {
	var buf bytes.Buffer
	restore := logx.SwapDebugOutput(&buf)
	defer restore()
	logx.SetDebug(true)        // the ignored-expiry condition is now a DEBUG trace (the operator
	defer logx.SetDebug(false) // warning + overwrite moved to the caller, which sees ExpiresAt==0)

	r := &sequenceRunner{results: []runResult{
		{out: uapiOK(`{"name":"cpsm_x","token":"TOK","expires_at":0}`)},
	}}
	tok, err := CreateFullAccessToken(bg, r, "cpsm_x", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ignored expiry must NOT fail (warn-and-proceed): %v", err)
	}
	// ExpiresAt==0 is the signal the caller uses to show its overwritable caveat.
	if tok.Secret != "TOK" || tok.ExpiresAt != 0 {
		t.Fatalf("token = %+v, want Secret=TOK ExpiresAt=0", tok)
	}
	if len(r.calls) != 1 {
		t.Fatalf("RunScript calls = %d, want 1 (create only, NO revoke); calls=%+v", len(r.calls), r.calls)
	}
	if w := buf.String(); !strings.Contains(w, "did not apply a token expiry") {
		t.Fatalf("expected a debug trace about the ignored expiry, got: %q", w)
	}
}

// An expiry returned under an UNRECOGNIZED field name (expires_at == 0 but e.g.
// "expires" is set) must NOT be masked as "ignored"/warn-and-proceed: it is a
// parsing mismatch that must fail closed (revoke) so the bug surfaces.
func TestCreateFullAccessTokenUnrecognizedExpiryFailsClosed(t *testing.T) {
	future := time.Now().Add(time.Hour).Unix()
	for _, field := range []string{"expires", "expiry"} {
		t.Run(field, func(t *testing.T) {
			data := fmt.Sprintf(`{"name":"cpsm_x","token":"TOK","expires_at":0,%q:%d}`, field, future)
			r := &sequenceRunner{results: []runResult{
				{out: uapiOK(data)},
				{out: []byte(`{"result":{"status":1,"data":1}}`)}, // revoke OK
			}}
			_, err := CreateFullAccessToken(bg, r, "cpsm_x", time.Now().Add(time.Hour))
			if err == nil {
				t.Fatalf("%s: an unbound expiry must fail closed, not warn-and-proceed", field)
			}
			if !errors.Is(err, errTokenExpiryUnrecognized) {
				t.Fatalf("%s: want errTokenExpiryUnrecognized, got %v", field, err)
			}
			requireRevokeCall(t, r, "cpsm_x")
		})
	}
}

// The three expiry-classification sentinels must stay mutually distinct under
// errors.Is. errString compares by VALUE, so if any two ever shared the same
// text, errTokenExpiryUnrecognized/Invalid could be misrouted into the
// warn-and-proceed branch (which keys on errors.Is(err, errTokenExpiryIgnored)),
// re-opening the masking hole the unrecognized-field guard closes.
func TestTokenExpiryErrorsAreDistinct(t *testing.T) {
	errs := map[string]error{
		"ignored":      errTokenExpiryIgnored,
		"invalid":      errTokenExpiryInvalid,
		"unrecognized": errTokenExpiryUnrecognized,
	}
	for an, a := range errs {
		for bn, b := range errs {
			if an != bn && errors.Is(a, b) {
				t.Errorf("errors.Is(%s, %s) = true; expiry sentinels must be distinct", an, bn)
			}
		}
	}
}

func TestCreateFullAccessTokenCreateErrorRevokesPossibleToken(t *testing.T) {
	r := &sequenceRunner{results: []runResult{
		{out: []byte(`{"result":{"status":1,"data":`)},
		{out: []byte(`{"result":{"status":1,"data":1}}`)},
	}}
	if _, err := CreateFullAccessToken(bg, r, "cpsm_x", time.Now().Add(time.Hour)); err == nil {
		t.Fatal("CreateFullAccessToken must return the create error")
	}
	requireRevokeCall(t, r, "cpsm_x")
}

func TestCreateFullAccessTokenRevokeFailureVisible(t *testing.T) {
	// A past expiry is an INVALID (not merely ignored) expiry, so it fails closed
	// and revokes; here the revoke itself fails, which must surface in the error.
	pastExpiry := fmt.Sprintf(`{"name":"cpsm_x","token":"TOK","expires_at":%d}`, time.Now().Add(-time.Minute).Unix())
	r := &sequenceRunner{results: []runResult{
		{out: uapiOK(pastExpiry)},
		{out: uapiFail("denied")},
	}}
	_, err := CreateFullAccessToken(bg, r, "cpsm_x", time.Now().Add(time.Hour))
	if err == nil {
		t.Fatal("CreateFullAccessToken must fail")
	}
	for _, want := range []string{"revoke", "Manage API Tokens", "cpsm_x", "denied"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error should include %q for manual cleanup guidance: %v", want, err)
		}
	}
}

func requireRevokeCall(t *testing.T, r *sequenceRunner, name string) {
	t.Helper()
	if len(r.calls) != 2 {
		t.Fatalf("RunScript calls = %d, want create + revoke; calls=%+v", len(r.calls), r.calls)
	}
	call := r.calls[1]
	if !strings.Contains(call.script, "uapi --output=json Tokens revoke") || call.env["ARG_0"] != name {
		t.Fatalf("second call should revoke %q, got script=%q env=%+v", name, call.script, call.env)
	}
}

func TestListTokenNames(t *testing.T) {
	f := &fakeRunner{out: uapiOK(`[{"name":"cpsm_a"},{"name":"other"}]`)}
	got, err := ListTokenNames(bg, f)
	if err != nil {
		t.Fatalf("ListTokenNames: %v", err)
	}
	if len(got) != 2 || got[0] != "cpsm_a" || got[1] != "other" {
		t.Errorf("token names = %v", got)
	}
}

func TestRevokeToken(t *testing.T) {
	f := &fakeRunner{out: []byte(`{"result":{"status":1,"data":1}}`)} // data is a number
	if err := RevokeToken(bg, f, "cpsm_x"); err != nil {
		t.Errorf("RevokeToken: %v", err)
	}
	fe := &fakeRunner{out: uapiFail("no such token")}
	if err := RevokeToken(bg, fe, "cpsm_x"); err == nil {
		t.Error("RevokeToken must surface a failure")
	}
}

func TestErrStringError(t *testing.T) {
	if errEmptyToken.Error() == "" {
		t.Error("errEmptyToken.Error() must not be empty")
	}
	if errString("boom").Error() != "boom" {
		t.Errorf("errString.Error() = %q, want boom", errString("boom").Error())
	}
}

// --- addon.go ---

func TestAddAddonDomainSuccessAndArgs(t *testing.T) {
	f := &fakeRunner{out: []byte(`{"cpanelresult":{"data":[{"result":"1","reason":"ok"}],"event":{"result":"1"}}}`)}
	secret := "SECRETVALUE123"
	if err := AddAddonDomain(bg, f, "destacct", APIToken{Name: "cpsm_x", Secret: secret}, "new.it"); err != nil {
		t.Fatalf("AddAddonDomain: %v", err)
	}
	if f.env["CPUSER"] != "destacct" || f.env["TOKEN"] != secret || !strings.Contains(f.env["APIURL"], "newdomain=new.it") {
		t.Errorf("addon env wrong: %v", f.env)
	}
	if strings.Contains(f.script, secret) || strings.Contains(f.env["APIURL"], secret) {
		t.Fatalf("addon token must not be embedded in script or API URL: script=%q env=%v", f.script, f.env)
	}
}

func TestAddAddonDomainAPIFailure(t *testing.T) {
	f := &fakeRunner{out: []byte(`{"cpanelresult":{"data":[{"result":"0","reason":"already exists"}]}}`)}
	if err := AddAddonDomain(bg, f, "u", APIToken{Name: "n", Secret: "t"}, "dup.it"); err == nil {
		t.Error("AddAddonDomain must error when api2 result != 1")
	}
}

func TestAddAddonDomainSSHError(t *testing.T) {
	f := &fakeRunner{err: context.Canceled}
	if err := AddAddonDomain(bg, f, "u", APIToken{Name: "n", Secret: "t"}, "d.it"); err == nil {
		t.Error("AddAddonDomain must propagate the RunScript error")
	}
}

func TestAddSubdomainUAPIError(t *testing.T) {
	f := &fakeRunner{out: uapiFail("exists")} // valid domain, but the API call fails
	if err := AddSubdomain(bg, f, "blog.example.it"); err == nil {
		t.Error("AddSubdomain must surface a UAPI failure")
	}
}

func TestAddSubdomain(t *testing.T) {
	f := &fakeRunner{out: uapiOK(`null`)}
	if err := AddSubdomain(bg, f, "blog.example.it"); err != nil {
		t.Fatalf("AddSubdomain: %v", err)
	}
	// label + rootdomain split passed as args.
	if f.env["ARG_0"] == "" {
		t.Errorf("AddSubdomain args missing: %v", f.env)
	}
	// A domain with no dot cannot be split -> error WITHOUT calling the server.
	f2 := &fakeRunner{out: uapiOK(`null`)}
	if err := AddSubdomain(bg, f2, "nodot"); err == nil {
		t.Error("AddSubdomain must reject an unsplittable domain")
	}
	if f2.script != "" {
		t.Error("AddSubdomain must not call the server for an invalid domain")
	}
}

// --- email.go ---

// Every lister/creator must surface a UAPI failure rather than returning empty.
func TestRunnerOpsSurfaceUAPIErrors(t *testing.T) {
	f := &fakeRunner{out: uapiFail("denied")}
	if _, err := ListDocroots(bg, f); err == nil {
		t.Error("ListDocroots")
	}
	if _, err := ListDatabases(bg, f); err == nil {
		t.Error("ListDatabases")
	}
	if _, err := ListDBUsers(bg, f); err == nil {
		t.Error("ListDBUsers")
	}
	if _, err := GetMySQLRestrictions(bg, f); err == nil {
		t.Error("GetMySQLRestrictions")
	}
	if _, err := ListTokenNames(bg, f); err == nil {
		t.Error("ListTokenNames")
	}
	if _, err := CreateFullAccessToken(bg, f, "n", time.Now().Add(time.Minute)); err == nil {
		t.Error("CreateFullAccessToken")
	}
	if err := CreateDBUser(bg, f, "u", "p"); err == nil {
		t.Error("CreateDBUser")
	}
	if err := SetPrivilegesOnDatabase(bg, f, "u", "d"); err == nil {
		t.Error("SetPrivilegesOnDatabase")
	}
	if err := SetDBUserPassword(bg, f, "u", "p"); err == nil {
		t.Error("SetDBUserPassword")
	}
}

func TestErrStrings(t *testing.T) {
	if errStrings(nil) != nil {
		t.Error("nil raw -> nil")
	}
	if got := errStrings(json.RawMessage(`["a","b"]`)); len(got) != 2 || got[0] != "a" {
		t.Errorf("array -> %v", got)
	}
	if got := errStrings(json.RawMessage(`"solo"`)); len(got) != 1 || got[0] != "solo" {
		t.Errorf("single string -> %v", got)
	}
	if got := errStrings(json.RawMessage(`{"x":1}`)); got != nil {
		t.Errorf("object -> nil, got %v", got)
	}
	if got := errStrings(json.RawMessage(`""`)); got != nil {
		t.Errorf("empty string -> nil, got %v", got)
	}
}

func TestCheckAddonResponseEdges(t *testing.T) {
	// Leading noise before the JSON object is tolerated (indexByte skips to '{').
	if err := checkAddonResponse("d.it", []byte(`curl warning {"cpanelresult":{"data":[{"result":"1"}]}}`)); err != nil {
		t.Errorf("leading noise should be skipped: %v", err)
	}
	// Unparseable api2 body -> error.
	if err := checkAddonResponse("d.it", []byte("not json")); err == nil {
		t.Error("bad api2 JSON must error")
	}
	// An explicit cpanelresult.error -> error.
	if err := checkAddonResponse("d.it", []byte(`{"cpanelresult":{"error":"boom"}}`)); err == nil {
		t.Error("cpanelresult.error must surface")
	}
}

func TestEnsureAccount(t *testing.T) {
	f := &fakeRunner{out: []byte("CREATED\n")}
	res, err := EnsureAccount(bg, f, "d.it", "info", "$6$hash")
	if err != nil || res.State != AccountCreated {
		t.Fatalf("EnsureAccount = %+v, %v; want created", res, err)
	}
	if f.env["DOM"] != "d.it" || f.env["USER"] != "info" || f.env["HASH"] != "$6$hash" {
		t.Errorf("EnsureAccount env wrong: %v", f.env)
	}
	// Orphan-maildir backup line is surfaced.
	fb := &fakeRunner{out: []byte("BAKDIR info-bak.2\nUPDATED\n")}
	res, err = EnsureAccount(bg, fb, "d.it", "info", "h")
	if err != nil || res.State != AccountUpdated || res.BackedUpDir != "info-bak.2" {
		t.Errorf("EnsureAccount(bak) = %+v, %v", res, err)
	}
	// RunScript failure propagates.
	ferr := &fakeRunner{err: context.Canceled}
	if _, err := EnsureAccount(bg, ferr, "d.it", "info", "h"); err == nil {
		t.Error("EnsureAccount must propagate the RunScript error")
	}
}
