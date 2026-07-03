package accountinventory

import (
	"context"
	"testing"
)

// The 7E sections (email routing, default address, email filters,
// redirects) follow the ConfigSection contract: available:true with
// items on success, available:false + method:"unavailable" + warning on
// failure, never fatal. Fixtures are the byte-verified real captures
// (PR7E_PRE_CAPTURES.md); filters use the docs-derived synthetic one.

func TestCollectEmailRouting(t *testing.T) {
	runner := &fakeRunner{responses: map[string][]byte{
		"Email list_mxs": loadFixture(t, "email_list_mxs_realserver.json"),
	}}
	sec := collectEmailRouting(context.Background(), runner)
	if !sec.Available || sec.Method != "uapi" || sec.SourceFunction != "Email::list_mxs" {
		t.Fatalf("section meta = %+v", sec.ConfigSection)
	}
	if len(sec.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(sec.Items))
	}
	local := sec.Items[0]
	if local.Domain != "doctorbike.it" || local.Routing != "local" || !local.AlwaysAccept {
		t.Errorf("[0] = %+v, want doctorbike.it/local/always_accept", local)
	}
	if len(local.MXRecords) != 1 || local.MXRecords[0].Priority != 0 || local.MXRecords[0].Exchange != "doctorbike.it" {
		t.Errorf("[0] mx_records = %+v", local.MXRecords)
	}
	remote := sec.Items[1]
	if remote.Domain != "italplant.com" || remote.Routing != "remote" || remote.AlwaysAccept {
		t.Errorf("[1] = %+v, want italplant.com/remote/no-always-accept", remote)
	}
}

func TestCollectEmailRoutingUnavailable(t *testing.T) {
	sec := collectEmailRouting(context.Background(), &fakeRunner{responses: map[string][]byte{}})
	if sec.Available || sec.Method != "unavailable" || len(sec.Warnings) == 0 {
		t.Fatalf("section = %+v, want unavailable with warning", sec.ConfigSection)
	}
	if sec.Items == nil {
		t.Fatal("Items must stay non-nil")
	}
}

func TestCollectDefaultAddresses(t *testing.T) {
	runner := &fakeRunner{responses: map[string][]byte{
		"Email list_default_address": loadFixture(t, "email_default_address_realserver.json"),
	}}
	sec := collectDefaultAddresses(context.Background(), runner)
	if !sec.Available || sec.SourceFunction != "Email::list_default_address" {
		t.Fatalf("section meta = %+v", sec.ConfigSection)
	}
	if len(sec.Items) != 7 {
		t.Fatalf("got %d items, want 7 (subdomains included)", len(sec.Items))
	}
	// Sorted by domain (cz.italplant.com first); the opaque value keeps
	// its embedded quotes.
	if sec.Items[0].Domain != "cz.italplant.com" {
		t.Errorf("[0] domain = %q, want cz.italplant.com (sorted)", sec.Items[0].Domain)
	}
	byDomain := map[string]string{}
	for _, e := range sec.Items {
		byDomain[e.Domain] = e.DefaultAddress
	}
	if got := byDomain["doctorbike.it"]; got != `":fail: No Such User Here"` {
		t.Errorf("doctorbike.it default = %q, want embedded-quotes value", got)
	}
}

func TestCollectEmailFilters(t *testing.T) {
	// One fixture serves both the account-level and the per-mailbox
	// call (the collector labels entries via Account). The pseudo
	// mailbox (no "@") must be skipped. The get_filter fixture serves
	// all names (the fakeRunner cannot distinguish per-filtername calls).
	runner := &fakeRunner{responses: map[string][]byte{
		"Email list_filters": loadFixture(t, "email_list_filters.json"),
		"Email get_filter":   loadFixture(t, "email_get_filter_spam-to-junk.json"),
	}}
	mailboxes := []MailboxEntry{
		{Email: "info@doctorbike.it"},
		{Email: "doctorbike"}, // Main Account pseudo-entry
	}
	sec := collectEmailFilters(context.Background(), runner, mailboxes, false)
	if !sec.Available || sec.SourceFunction != "Email::list_filters" {
		t.Fatalf("section meta = %+v", sec.ConfigSection)
	}
	if len(sec.Items) != 4 {
		t.Fatalf("got %d items, want 4 (2 account-level + 2 for info@)", len(sec.Items))
	}
	// Sorted by account ("" first) then filter name; counts only.
	first := sec.Items[0]
	if first.Account != "" || first.FilterName != "legacy-disabled" || first.Enabled {
		t.Errorf("[0] = %+v, want account-level legacy-disabled disabled", first)
	}
	if first.RuleCount != 2 || first.ActionCount != 2 {
		t.Errorf("[0] counts = %d/%d, want 2/2", first.RuleCount, first.ActionCount)
	}
	if sec.Items[2].Account != "info@doctorbike.it" {
		t.Errorf("[2] account = %q, want info@doctorbike.it", sec.Items[2].Account)
	}
}

func TestCollectEmailFiltersUnavailable(t *testing.T) {
	sec := collectEmailFilters(context.Background(), &fakeRunner{responses: map[string][]byte{}}, nil, false)
	if sec.Available || sec.Method != "unavailable" || len(sec.Warnings) == 0 {
		t.Fatalf("section = %+v, want unavailable with warning", sec.ConfigSection)
	}
}

// The interleaving the review flagged: the mailbox list itself failed
// but the account-level filter call succeeds. The section must stay
// available AND record the narrowed scope — never a silent coverage
// loss.
func TestCollectEmailFiltersMailboxListUnavailable(t *testing.T) {
	runner := &fakeRunner{responses: map[string][]byte{
		"Email list_filters": loadFixture(t, "email_list_filters.json"),
		"Email get_filter":   loadFixture(t, "email_get_filter_spam-to-junk.json"),
	}}
	sec := collectEmailFilters(context.Background(), runner, nil, true)
	if !sec.Available {
		t.Fatalf("section = %+v, want available (account-level succeeded)", sec.ConfigSection)
	}
	if len(sec.Items) != 2 {
		t.Errorf("got %d items, want 2 account-level ones", len(sec.Items))
	}
	found := false
	for _, w := range sec.Warnings {
		if contains(w, "account-level") {
			found = true
		}
	}
	if !found {
		t.Errorf("missing narrowed-scope warning, got: %v", sec.Warnings)
	}
}

// 2B-3: the enriched collector populates Rules, Actions and
// RulesCollected from get_filter. When get_filter fails, the entry
// degrades gracefully (rules_collected=false, warning, never lost).
func TestCollectEmailFiltersRulesCollected(t *testing.T) {
	runner := &fakeRunner{responses: map[string][]byte{
		"Email list_filters": loadFixture(t, "email_list_filters.json"),
		"Email get_filter":   loadFixture(t, "email_get_filter_spam-to-junk.json"),
	}}
	sec := collectEmailFilters(context.Background(), runner, nil, false)
	if len(sec.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(sec.Items))
	}
	for _, item := range sec.Items {
		if !item.RulesCollected {
			t.Errorf("filter %q: rules_collected=false, want true", item.FilterName)
		}
		if len(item.Rules) == 0 {
			t.Errorf("filter %q: no rules populated", item.FilterName)
		}
		if len(item.Actions) == 0 {
			t.Errorf("filter %q: no actions populated", item.FilterName)
		}
	}
	// Verify first entry has the expected rule content from the fixture.
	// Note: fakeRunner returns the same get_filter fixture for all calls,
	// so both entries get the spam-to-junk content.
	first := sec.Items[0]
	if first.Rules[0].Part != "$header_subject:" || first.Rules[0].Match != "contains" || first.Rules[0].Val != "[SPAM]" {
		t.Errorf("first filter rule = %+v, want $header_subject: contains [SPAM]", first.Rules[0])
	}
	if first.Actions[0].Action != "save" {
		t.Errorf("first filter action = %+v, want save", first.Actions[0])
	}
}

// 2B-3: when get_filter fails, the entry must carry
// rules_collected=false and a warning, but the entry itself survives.
func TestCollectEmailFiltersGetFilterFails(t *testing.T) {
	// No get_filter response → every get_filter call fails
	runner := &fakeRunner{responses: map[string][]byte{
		"Email list_filters": loadFixture(t, "email_list_filters.json"),
	}}
	sec := collectEmailFilters(context.Background(), runner, nil, false)
	if !sec.Available {
		t.Fatalf("section should still be available")
	}
	if len(sec.Items) != 2 {
		t.Fatalf("got %d items, want 2 (entries must survive get_filter failure)", len(sec.Items))
	}
	for _, item := range sec.Items {
		if item.RulesCollected {
			t.Errorf("filter %q: rules_collected=true, want false (get_filter failed)", item.FilterName)
		}
		if len(item.Rules) != 0 {
			t.Errorf("filter %q: has %d rules, want 0 (get_filter failed)", item.FilterName, len(item.Rules))
		}
	}
	if len(sec.Warnings) != 2 {
		t.Errorf("expected 2 get_filter warnings, got %d: %v", len(sec.Warnings), sec.Warnings)
	}
}

func TestCollectRedirects(t *testing.T) {
	runner := &fakeRunner{responses: map[string][]byte{
		"Mime list_redirects": loadFixture(t, "mime_redirects_realserver.json"),
	}}
	sec := collectRedirects(context.Background(), runner)
	if !sec.Available || sec.SourceFunction != "Mime::list_redirects" {
		t.Fatalf("section meta = %+v", sec.ConfigSection)
	}
	if len(sec.Items) != 3 {
		t.Fatalf("got %d items, want 3", len(sec.Items))
	}
	// Sorted by domain then source: the two CMS rewrites (noleggio.*)
	// precede the genuine 301 (wilco-uk.*).
	last := sec.Items[2]
	if last.Domain != "wilco-uk.italplant.com" || last.StatusCode != 301 || last.Type != "permanent" {
		t.Errorf("[2] = %+v, want the genuine 301", last)
	}
	if !last.Wildcard || !last.MatchWWW {
		t.Errorf("[2] wildcard/matchwww = %v/%v, want true/true", last.Wildcard, last.MatchWWW)
	}
	if cms := sec.Items[0]; cms.StatusCode != 0 || cms.Kind != "rewrite" || cms.Type != "temporary" {
		t.Errorf("[0] = %+v, want CMS rewrite with no status code", cms)
	}
}

func TestCollectRedirectsUnavailable(t *testing.T) {
	sec := collectRedirects(context.Background(), &fakeRunner{responses: map[string][]byte{}})
	if sec.Available || sec.Method != "unavailable" || len(sec.Warnings) == 0 {
		t.Fatalf("section = %+v, want unavailable with warning", sec.ConfigSection)
	}
}

// A legitimately mailbox-less account (list succeeded, zero entries)
// must NOT get the narrowed-scope warning.
func TestCollectEmailFiltersNoWarningWhenNoMailboxes(t *testing.T) {
	// get_filter returns a single-filter response; the fakeRunner cannot
	// distinguish per-filtername calls (values are in env vars, not the
	// script text), so we supply a generic get_filter fixture. The collector
	// tolerates shape mismatches between list and get gracefully.
	runner := &fakeRunner{responses: map[string][]byte{
		"Email list_filters": loadFixture(t, "email_list_filters.json"),
		"Email get_filter":   loadFixture(t, "email_get_filter_spam-to-junk.json"),
	}}
	sec := collectEmailFilters(context.Background(), runner, nil, false)
	if !sec.Available {
		t.Fatalf("section = %+v, want available", sec.ConfigSection)
	}
	if len(sec.Warnings) != 0 {
		t.Errorf("no warnings expected for a mailbox-less account, got: %v", sec.Warnings)
	}
	// 2B-3: every entry must have rules_collected=true when get_filter succeeds.
	for _, item := range sec.Items {
		if !item.RulesCollected {
			t.Errorf("filter %q: rules_collected=false, want true", item.FilterName)
		}
	}
}
