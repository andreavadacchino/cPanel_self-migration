package cpanel

import (
	"strings"
	"testing"
)

// --- write primitives (2B-pre byte-verified contract) -----------------------

func TestAddForwarderBuildsVerifiedCall(t *testing.T) {
	f := &fakeRunner{out: uapiOK(`{"forward":"someone@gmail.com","domain":"example.com","email":"info@example.com"}`)}
	if err := AddForwarder(bg, f, "example.com", "info", "someone@gmail.com"); err != nil {
		t.Fatalf("AddForwarder: %v", err)
	}
	if !strings.Contains(f.script, "uapi --output=json Email add_forwarder") {
		t.Errorf("script = %q", f.script)
	}
	// 2B-pre contract: domain= email=<LOCAL part> fwdopt=fwd fwdemail=;
	// values travel via env, never spliced into the script.
	for _, k := range []string{"domain", "email", "fwdopt", "fwdemail"} {
		if !strings.Contains(f.script, k+`="$ARG_`) {
			t.Errorf("script missing env-backed %s= arg: %q", k, f.script)
		}
	}
	wantEnv := map[string]string{"domain": "example.com", "email": "info", "fwdopt": "fwd", "fwdemail": "someone@gmail.com"}
	envByKey := envArgsByKey(t, f.script, f.env)
	for k, v := range wantEnv {
		if envByKey[k] != v {
			t.Errorf("arg %s = %q, want %q (env %v)", k, envByKey[k], v, f.env)
		}
	}
}

func TestDeleteForwarderBuildsVerifiedCall(t *testing.T) {
	f := &fakeRunner{out: uapiOK(`null`)}
	if err := DeleteForwarder(bg, f, "info@example.com", "someone@gmail.com"); err != nil {
		t.Fatalf("DeleteForwarder: %v", err)
	}
	if !strings.Contains(f.script, "uapi --output=json Email delete_forwarder") {
		t.Errorf("script = %q", f.script)
	}
	envByKey := envArgsByKey(t, f.script, f.env)
	if envByKey["address"] != "info@example.com" || envByKey["forwarder"] != "someone@gmail.com" {
		t.Errorf("args = %v", envByKey)
	}
}

func TestSetDefaultAddressForms(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    map[string]string
		wantAbs []string // keys that must NOT be present
	}{
		{
			name:  "plain address",
			value: "someone@gmail.com",
			want:  map[string]string{"domain": "example.com", "fwdopt": "fwd", "fwdemail": "someone@gmail.com"},
		},
		{
			name:    "fail system form",
			value:   ":fail: No Such User Here",
			want:    map[string]string{"domain": "example.com", "fwdopt": "fail", "failmsgs": "No Such User Here"},
			wantAbs: []string{"fwdemail"},
		},
		{
			name:    "blackhole system form",
			value:   ":blackhole:",
			want:    map[string]string{"domain": "example.com", "fwdopt": "blackhole"},
			wantAbs: []string{"fwdemail", "failmsgs"},
		},
		{
			name:  "bare username (deliver to account)",
			value: "acctuser",
			want:  map[string]string{"domain": "example.com", "fwdopt": "fwd", "fwdemail": "acctuser"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{out: uapiOK(`[{"dest":"x","domain":"example.com"}]`)}
			if err := SetDefaultAddress(bg, f, "example.com", tc.value); err != nil {
				t.Fatalf("SetDefaultAddress: %v", err)
			}
			if !strings.Contains(f.script, "uapi --output=json Email set_default_address") {
				t.Errorf("script = %q", f.script)
			}
			envByKey := envArgsByKey(t, f.script, f.env)
			for k, v := range tc.want {
				if envByKey[k] != v {
					t.Errorf("arg %s = %q, want %q", k, envByKey[k], v)
				}
			}
			for _, k := range tc.wantAbs {
				if _, ok := envByKey[k]; ok {
					t.Errorf("arg %s must be absent, got %q", k, envByKey[k])
				}
			}
		})
	}
}

func TestEmailWritePrimitivesSurfaceUAPIErrors(t *testing.T) {
	f := &fakeRunner{out: uapiFail("denied")}
	if err := AddForwarder(bg, f, "d.com", "i", "x@y.com"); err == nil {
		t.Error("AddForwarder must surface a UAPI failure")
	}
	if err := DeleteForwarder(bg, f, "i@d.com", "x@y.com"); err == nil {
		t.Error("DeleteForwarder must surface a UAPI failure")
	}
	if err := SetDefaultAddress(bg, f, "d.com", "x@y.com"); err == nil {
		t.Error("SetDefaultAddress must surface a UAPI failure")
	}
}

// --- fresh re-list primitives (raw + normalized, for the apply backup) ------

func TestListForwardersWithRaw(t *testing.T) {
	raw := uapiOK(`[{"dest":"info@d.com","forward":"x@y.com"}]`)
	f := &fakeRunner{out: raw}
	entries, rawOut, err := ListForwardersWithRaw(bg, f, "d.com")
	if err != nil {
		t.Fatalf("ListForwardersWithRaw: %v", err)
	}
	if len(entries) != 1 || entries[0].Dest != "info@d.com" {
		t.Errorf("entries = %+v", entries)
	}
	if string(rawOut) != string(raw) {
		t.Errorf("raw = %q, want the verbatim response", rawOut)
	}
}

func TestListDefaultAddressesWithRaw(t *testing.T) {
	raw := uapiOK(`[{"domain":"d.com","defaultaddress":"acct"}]`)
	f := &fakeRunner{out: raw}
	entries, rawOut, err := ListDefaultAddressesWithRaw(bg, f)
	if err != nil {
		t.Fatalf("ListDefaultAddressesWithRaw: %v", err)
	}
	if len(entries) != 1 || entries[0].DefaultAddress != "acct" {
		t.Errorf("entries = %+v", entries)
	}
	if string(rawOut) != string(raw) {
		t.Errorf("raw = %q, want the verbatim response", rawOut)
	}
}

// envArgsByKey maps each `key="$ARG_i"` in the script back to its env
// value, so tests assert on the actual uapi arguments.
func envArgsByKey(t *testing.T, script string, env map[string]string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, tok := range strings.Fields(script) {
		k, v, ok := strings.Cut(tok, "=")
		if !ok || !strings.HasPrefix(v, `"$ARG_`) {
			continue
		}
		ev := strings.TrimSuffix(strings.TrimPrefix(v, `"$`), `"`)
		out[k] = env[ev]
	}
	return out
}
