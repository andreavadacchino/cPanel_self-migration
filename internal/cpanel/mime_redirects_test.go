package cpanel

import "testing"

// Real capture facts (PR7E_PRE_CAPTURES.md fact 4): Mime::list_redirects
// harvests .htaccess. The fixture pairs the one genuine operator 301
// (statuscode as QUOTED STRING "301", wildcard:1, matchwww:1) with two
// CMS RewriteRules (statuscode:null → 0, type:"temporary").
func TestParseListRedirectsRealServer(t *testing.T) {
	data, err := parseUAPI[[]RedirectEntry]("Mime", "list_redirects", fixture(t, "mime_redirects_realserver.json"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 3 {
		t.Fatalf("got %d redirects, want 3", len(data))
	}
	real301 := data[0]
	if real301.Domain != "wilco-uk.italplant.com" {
		t.Errorf("[0] domain = %q", real301.Domain)
	}
	if real301.Destination != "https://wilco.italplant.com/" {
		t.Errorf("[0] destination = %q", real301.Destination)
	}
	if real301.Type != "permanent" || real301.StatusCode != 301 {
		t.Errorf("[0] type/statuscode = %q/%d, want permanent/301 (from quoted \"301\")",
			real301.Type, real301.StatusCode)
	}
	if real301.Wildcard != 1 || real301.MatchWWW != 1 {
		t.Errorf("[0] wildcard/matchwww = %d/%d, want 1/1", real301.Wildcard, real301.MatchWWW)
	}
	for i, cms := range data[1:] {
		if cms.Kind != "rewrite" || cms.Type != "temporary" || cms.StatusCode != 0 {
			t.Errorf("[%d] kind/type/statuscode = %q/%q/%d, want rewrite/temporary/0 (from null)",
				i+1, cms.Kind, cms.Type, cms.StatusCode)
		}
		if cms.Source == "" || cms.Destination == "" {
			t.Errorf("[%d] source/destination empty: %+v", i+1, cms)
		}
	}
}

func TestParseListRedirectsEmpty(t *testing.T) {
	empty := []byte(`{"result":{"data":[],"errors":null,"messages":null,"status":1}}`)
	data, err := parseUAPI[[]RedirectEntry]("Mime", "list_redirects", empty)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("got %d, want 0", len(data))
	}
}

// Tie-break regression lock (round-2 reviewer): two entries sharing
// Domain+Source must come out in the same order regardless of the
// input (API) order, which is not proven stable across invocations.
func TestListRedirectsTieBreakOrderIndependent(t *testing.T) {
	entryA := `{"domain":"d.test","sourceurl":"/old","destination":"https://a.test/","kind":"rewrite","type":"permanent","statuscode":"301","wildcard":0,"matchwww":0}`
	entryB := `{"domain":"d.test","sourceurl":"/old","destination":"https://b.test/","kind":"rewrite","type":"permanent","statuscode":"301","wildcard":0,"matchwww":0}`
	for name, payload := range map[string]string{
		"a-first": entryA + "," + entryB,
		"b-first": entryB + "," + entryA,
	} {
		out := []byte(`{"result":{"data":[` + payload + `],"errors":null,"messages":null,"status":1}}`)
		data, err := ListRedirects(t.Context(), &fakeRunner{out: out})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(data) != 2 || data[0].Destination != "https://a.test/" {
			t.Errorf("%s: order not deterministic, got %+v", name, data)
		}
	}
}
