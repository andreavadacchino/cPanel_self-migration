package cpanel

import "testing"

// email_list_filters.json is SYNTHETIC (docs-derived): no reachable
// production account has filters (PR7E_PRE_CAPTURES.md fact 3), so the
// non-empty item shape is unproven. The decoder therefore keeps rules
// and actions as raw JSON — only their COUNTS are consumed — and the
// test pins that a shape surprise inside a rule body cannot break the
// decode. `enabled` exercises flexInt64 (1 and quoted "0").
func TestParseListEmailFiltersSynthetic(t *testing.T) {
	data, err := parseUAPI[[]EmailFilterEntry]("Email", "list_filters", fixture(t, "email_list_filters.json"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 2 {
		t.Fatalf("got %d filters, want 2", len(data))
	}
	if data[0].FilterName != "spam-to-junk" || data[0].Enabled != 1 {
		t.Errorf("[0] = %q enabled=%d, want spam-to-junk enabled=1", data[0].FilterName, data[0].Enabled)
	}
	if len(data[0].Rules) != 1 || len(data[0].Actions) != 1 {
		t.Errorf("[0] rules/actions = %d/%d, want 1/1", len(data[0].Rules), len(data[0].Actions))
	}
	if data[1].FilterName != "legacy-disabled" || data[1].Enabled != 0 {
		t.Errorf("[1] = %q enabled=%d, want legacy-disabled enabled=0 (from quoted \"0\")",
			data[1].FilterName, data[1].Enabled)
	}
	if len(data[1].Rules) != 2 || len(data[1].Actions) != 2 {
		t.Errorf("[1] rules/actions = %d/%d, want 2/2", len(data[1].Rules), len(data[1].Actions))
	}
}

// Byte-verified live shape: every reachable account returns data:[].
func TestParseListEmailFiltersEmpty(t *testing.T) {
	empty := []byte(`{"result":{"errors":null,"data":[],"warnings":null,"messages":null,"status":1,"metadata":{"transformed":1}}}`)
	data, err := parseUAPI[[]EmailFilterEntry]("Email", "list_filters", empty)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("got %d, want 0", len(data))
	}
}

// A rule whose body is an OBJECT with unexpected fields (or any other
// JSON value) must still count — raw retention, no inner binding.
func TestParseListEmailFiltersUnknownRuleShape(t *testing.T) {
	odd := []byte(`{"result":{"data":[{"filtername":"odd","enabled":"1","rules":[{"totally":"unknown","nested":{"x":1}},"even-a-string"],"actions":[]}],"errors":null,"messages":null,"status":1}}`)
	data, err := parseUAPI[[]EmailFilterEntry]("Email", "list_filters", odd)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data) != 1 || len(data[0].Rules) != 2 || len(data[0].Actions) != 0 {
		t.Errorf("got %+v, want 1 filter with 2 raw rules and 0 actions", data)
	}
	if data[0].Enabled != 1 {
		t.Errorf("enabled = %d, want 1 (from quoted \"1\")", data[0].Enabled)
	}
}

// 2B-3: get_filter with a real filter returns typed rule/action data.
func TestParseGetEmailFilterReal(t *testing.T) {
	data, err := parseUAPI[GetEmailFilterResult]("Email", "get_filter", fixture(t, "email_get_filter_spam-to-junk.json"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if data.FilterName != "spam-to-junk" {
		t.Errorf("filtername = %q, want spam-to-junk", data.FilterName)
	}
	if len(data.Rules) != 1 || len(data.Actions) != 1 {
		t.Fatalf("rules/actions = %d/%d, want 1/1", len(data.Rules), len(data.Actions))
	}
}

// 2B-3-pre fact 4: get_filter on a non-existent filter returns a
// template with filtername="Rule 1" — status:1, NOT an error.
func TestParseGetEmailFilterNonExistent(t *testing.T) {
	resp := []byte(`{"result":{"data":{"filtername":"Rule 1","rules":[{"number":1}],"actions":[{"number":1}]},"errors":null,"warnings":null,"status":1,"messages":null,"metadata":{}}}`)
	data, err := parseUAPI[GetEmailFilterResult]("Email", "get_filter", resp)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if data.FilterName != "Rule 1" {
		t.Errorf("filtername = %q, want template 'Rule 1'", data.FilterName)
	}
}

// Tie-break regression lock (round-2 reviewer): duplicate filter names
// must order deterministically regardless of the input order.
func TestListEmailFiltersTieBreakOrderIndependent(t *testing.T) {
	oneRule := `{"filtername":"dup","enabled":1,"rules":[{}],"actions":[{}]}`
	twoRules := `{"filtername":"dup","enabled":1,"rules":[{},{}],"actions":[{}]}`
	for name, payload := range map[string]string{
		"one-first": oneRule + "," + twoRules,
		"two-first": twoRules + "," + oneRule,
	} {
		out := []byte(`{"result":{"data":[` + payload + `],"errors":null,"messages":null,"status":1}}`)
		data, err := ListEmailFilters(t.Context(), &fakeRunner{out: out}, "")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(data) != 2 || len(data[0].Rules) != 1 {
			t.Errorf("%s: order not deterministic, got %+v", name, data)
		}
	}
}
