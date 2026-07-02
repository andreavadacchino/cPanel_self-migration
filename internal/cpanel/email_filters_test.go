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
