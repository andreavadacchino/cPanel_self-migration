package workbench

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestSetupMetaRoundTrip pins the wizard metadata schema: a session with a
// non-nil Setup marshals and unmarshals byte-stably (no secret fields, DNS
// carried as its own selection flag).
func TestSetupMetaRoundTrip(t *testing.T) {
	in := &SetupMeta{
		PrimaryDomain: "giorginisposi.it",
		Notes:         "prima migrazione di prova",
		Source:        Endpoint{Host: "192.168.1.193", Port: 22, Account: "giorginisposi"},
		Destination:   Endpoint{Host: "192.168.1.78", Port: 2222, Account: "giorginisposi"},
		Content: ContentSelection{
			Files: true, Databases: true, Email: true, EmailConfig: false, Cron: true, DNS: false,
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out SetupMeta
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(*in, out) {
		t.Errorf("round-trip mismatch:\n in=%+v\nout=%+v", *in, out)
	}
}

// TestEndpointHasNoSecretField is the structural anti-leak guard: an Endpoint
// must never gain a password/token/secret field. The session is persisted and
// can be bundled into reports — a secret here would leak by construction.
func TestEndpointHasNoSecretField(t *testing.T) {
	tp := reflect.TypeOf(Endpoint{})
	for i := 0; i < tp.NumField(); i++ {
		name := strings.ToLower(tp.Field(i).Name)
		for _, bad := range []string{"pass", "secret", "token", "key", "cred"} {
			if strings.Contains(name, bad) {
				t.Errorf("Endpoint field %q looks secret-bearing (contains %q); credentials must stay in host.yaml only", tp.Field(i).Name, bad)
			}
		}
	}
}

// TestSessionSetupOptional proves an OLD session JSON (no "setup" key) still
// parses — Setup is a pointer and stays nil, so existing sessions keep working.
func TestSessionSetupOptional(t *testing.T) {
	old := `{
		"id": "mig_20260101_deadbeefcafe",
		"name": "legacy",
		"source_profile": "src",
		"destination_profile": "dst",
		"status": "draft",
		"current_step": "setup",
		"artifacts": [],
		"timeline": [],
		"tool_version": "test"
	}`
	var s Session
	if err := json.Unmarshal([]byte(old), &s); err != nil {
		t.Fatalf("legacy session must still parse: %v", err)
	}
	if s.Setup != nil {
		t.Errorf("legacy session Setup = %+v, want nil", s.Setup)
	}
	if s.Name != "legacy" {
		t.Errorf("Name = %q, want legacy", s.Name)
	}
}

// TestSetupOmittedWhenNil ensures a session without a wizard setup does not
// emit an empty "setup" object (omitempty on the pointer keeps old sessions
// byte-clean).
func TestSetupOmittedWhenNil(t *testing.T) {
	s := Session{ID: "mig_x", Name: "n", Status: StatusDraft}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "\"setup\"") {
		t.Errorf("nil Setup must be omitted from JSON, got: %s", b)
	}
}
