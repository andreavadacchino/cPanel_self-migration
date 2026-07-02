package accountinventory

import (
	"strings"
	"testing"
)

// --- fixtures ---------------------------------------------------------------

// epInventory builds a minimal inventory for email-plan tests: one main
// domain, available default-address / routing / filter sections.
func epInventory(side, user, domain string) NormalizedInventory {
	inv := NewEmptyInventory(user, "192.0.2.1", side)
	inv.Domains = []DomainEntry{{Name: domain, Type: "main"}}
	inv.DefaultAddresses.Available = true
	inv.DefaultAddresses.Items = []DefaultAddressEntry{{Domain: domain, DefaultAddress: user}}
	inv.EmailRouting.Available = true
	inv.EmailRouting.Items = []EmailRoutingEntry{{Domain: domain, Routing: "local"}}
	inv.EmailFilters.Available = true
	return inv
}

func opsBySection(p EmailApplyPlan, section string) []EmailPlanOp {
	var out []EmailPlanOp
	for _, op := range p.Ops {
		if op.Section == section {
			out = append(out, op)
		}
	}
	return out
}

func findEmailOp(t *testing.T, p EmailApplyPlan, section, key string) EmailPlanOp {
	t.Helper()
	for _, op := range p.Ops {
		if op.Section == section && op.Key == key {
			return op
		}
	}
	t.Fatalf("no op for section %q key %q in %+v", section, key, p.Ops)
	return EmailPlanOp{}
}

// --- forwarders -------------------------------------------------------------

func TestEmailPlanForwarderCreate(t *testing.T) {
	src := epInventory("source", "acct", "example.com")
	src.Forwarders = []ForwarderEntry{
		{Source: "info@example.com", Destination: "someone@gmail.com", Domain: "example.com"},
	}
	dest := epInventory("destination", "acct", "example.com")

	p := BuildEmailPlan(src, dest, nil)

	op := findEmailOp(t, p, "forwarders", "info@example.com")
	if op.Action != EmailActionCreate {
		t.Fatalf("action = %q, want create (reason %q)", op.Action, op.Reason)
	}
	if op.Domain != "example.com" || op.Email != "info" || op.Forward != "someone@gmail.com" {
		t.Errorf("write fields = domain %q email %q forward %q", op.Domain, op.Email, op.Forward)
	}
	if len(op.PlanTimeDestForwards) != 0 {
		t.Errorf("plan-time dest forwards = %v, want empty (fresh dest)", op.PlanTimeDestForwards)
	}
	if p.Summary.Create != 1 {
		t.Errorf("summary.create = %d, want 1", p.Summary.Create)
	}
}

func TestEmailPlanForwarderSkipWhenPairPresent(t *testing.T) {
	src := epInventory("source", "acct", "example.com")
	src.Forwarders = []ForwarderEntry{
		{Source: "info@example.com", Destination: "someone@gmail.com", Domain: "example.com"},
	}
	dest := epInventory("destination", "acct", "example.com")
	// Same pair, different spelling: canonical comparison must match.
	dest.Forwarders = []ForwarderEntry{
		{Source: "INFO@Example.COM", Destination: "Someone@Gmail.com", Domain: "example.com"},
	}

	p := BuildEmailPlan(src, dest, nil)

	op := findEmailOp(t, p, "forwarders", "info@example.com")
	if op.Action != EmailActionSkip {
		t.Fatalf("action = %q, want skip (reason %q)", op.Action, op.Reason)
	}
	if p.Summary.Skip == 0 {
		t.Errorf("summary.skip = %d, want > 0", p.Summary.Skip)
	}
}

// Non-single-address forward targets are terminal manual: comma-joined
// multi-target, pipes, file paths, :fail:/:blackhole:, deliver-to-account.
func TestEmailPlanForwarderManualForms(t *testing.T) {
	cases := []struct {
		name   string
		target string
	}{
		{"multi-target comma", "sales@company.com, backup@company.com"},
		{"pipe to script", "|/home/acct/script.sh"},
		{"deliver to file", "/home/acct/mail/archive"},
		{"system fail", ":fail: No Such User Here"},
		{"system blackhole", ":blackhole:"},
		{"deliver to account", "otheracct"},
		{"two at signs", "a@b@example.com"},
		{"domain without dot", "user@localhost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := epInventory("source", "acct", "example.com")
			src.Forwarders = []ForwarderEntry{
				{Source: "info@example.com", Destination: tc.target, Domain: "example.com"},
			}
			dest := epInventory("destination", "acct", "example.com")

			p := BuildEmailPlan(src, dest, nil)
			op := findEmailOp(t, p, "forwarders", "info@example.com")
			if op.Action != EmailActionManual {
				t.Fatalf("target %q: action = %q, want manual", tc.target, op.Action)
			}
			if !strings.Contains(op.Reason, tc.target) && !strings.Contains(op.Reason, strings.TrimSpace(tc.target)) {
				t.Errorf("reason %q does not carry the raw value %q", op.Reason, tc.target)
			}
		})
	}
}

func TestEmailPlanForwarderManualWhenSourceAddressMalformed(t *testing.T) {
	src := epInventory("source", "acct", "example.com")
	src.Forwarders = []ForwarderEntry{
		{Source: "not-an-address", Destination: "x@gmail.com", Domain: "example.com"},
	}
	dest := epInventory("destination", "acct", "example.com")

	p := BuildEmailPlan(src, dest, nil)
	op := findEmailOp(t, p, "forwarders", "not-an-address")
	if op.Action != EmailActionManual {
		t.Fatalf("action = %q, want manual", op.Action)
	}
}

func TestEmailPlanForwarderManualWhenDomainMissingOnDest(t *testing.T) {
	src := epInventory("source", "acct", "example.com")
	src.Forwarders = []ForwarderEntry{
		{Source: "info@other.com", Destination: "x@gmail.com", Domain: "other.com"},
	}
	dest := epInventory("destination", "acct", "example.com")

	p := BuildEmailPlan(src, dest, nil)
	op := findEmailOp(t, p, "forwarders", "info@other.com")
	if op.Action != EmailActionManual {
		t.Fatalf("action = %q, want manual (domain missing on dest)", op.Action)
	}
	if !strings.Contains(op.Reason, "missing on destination") {
		t.Errorf("reason = %q", op.Reason)
	}
}

// A create op for an address the destination already forwards ELSEWHERE
// still plans (additive posture, dest-only pair stays informational), but
// records the plan-time dest targets as its precondition.
func TestEmailPlanForwarderCreateRecordsPlanTimeDestState(t *testing.T) {
	src := epInventory("source", "acct", "example.com")
	src.Forwarders = []ForwarderEntry{
		{Source: "info@example.com", Destination: "new@gmail.com", Domain: "example.com"},
	}
	dest := epInventory("destination", "acct", "example.com")
	dest.Forwarders = []ForwarderEntry{
		{Source: "info@example.com", Destination: "old@gmail.com", Domain: "example.com"},
	}

	p := BuildEmailPlan(src, dest, nil)
	op := findEmailOp(t, p, "forwarders", "info@example.com")
	if op.Action != EmailActionCreate {
		t.Fatalf("action = %q, want create", op.Action)
	}
	if len(op.PlanTimeDestForwards) != 1 || op.PlanTimeDestForwards[0] != "old@gmail.com" {
		t.Errorf("plan-time dest forwards = %v, want [old@gmail.com]", op.PlanTimeDestForwards)
	}
	// The dest-only pair is informational, never deleted.
	if len(p.Informational) != 1 || p.Informational[0].Section != "forwarders" {
		t.Errorf("informational = %+v, want the dest-only pair", p.Informational)
	}
}

func TestEmailPlanDestOnlyForwardersAreInformational(t *testing.T) {
	src := epInventory("source", "acct", "example.com")
	dest := epInventory("destination", "acct", "example.com")
	dest.Forwarders = []ForwarderEntry{
		{Source: "sales@example.com", Destination: "x@gmail.com", Domain: "example.com"},
	}

	p := BuildEmailPlan(src, dest, nil)
	if len(opsBySection(p, "forwarders")) != 0 {
		t.Errorf("no forwarder ops expected, got %+v", p.Ops)
	}
	if len(p.Informational) != 1 || p.Informational[0].Key != "sales@example.com" {
		t.Errorf("informational = %+v", p.Informational)
	}
	if p.Summary.Informational != 1 {
		t.Errorf("summary.informational = %d, want 1", p.Summary.Informational)
	}
}

// Duplicate source rows for the same canonical pair collapse into one op.
func TestEmailPlanForwarderDeduplicatesIdenticalPairs(t *testing.T) {
	src := epInventory("source", "acct", "example.com")
	src.Forwarders = []ForwarderEntry{
		{Source: "info@example.com", Destination: "x@gmail.com", Domain: "example.com"},
		{Source: "INFO@example.com", Destination: "X@GMAIL.com", Domain: "example.com"},
	}
	dest := epInventory("destination", "acct", "example.com")

	p := BuildEmailPlan(src, dest, nil)
	if got := len(opsBySection(p, "forwarders")); got != 1 {
		t.Errorf("forwarder ops = %d, want 1 (deduplicated)", got)
	}
}

// --- default address --------------------------------------------------------

func TestEmailPlanDefaultAddressSetOnFreshDest(t *testing.T) {
	src := epInventory("source", "acct", "example.com")
	src.DefaultAddresses.Items = []DefaultAddressEntry{
		{Domain: "example.com", DefaultAddress: "someone@gmail.com"},
	}
	dest := epInventory("destination", "acct", "example.com") // fresh default = username "acct"

	p := BuildEmailPlan(src, dest, nil)
	op := findEmailOp(t, p, "default_address", "example.com")
	if op.Action != EmailActionSet {
		t.Fatalf("action = %q, want set (reason %q)", op.Action, op.Reason)
	}
	if op.Value != "someone@gmail.com" || op.DestinationValue != "acct" {
		t.Errorf("value = %q, destination_value = %q", op.Value, op.DestinationValue)
	}
	if p.Summary.Set != 1 {
		t.Errorf("summary.set = %d, want 1", p.Summary.Set)
	}
}

func TestEmailPlanDefaultAddressSetOnFreshFailDest(t *testing.T) {
	src := epInventory("source", "acct", "example.com")
	src.DefaultAddresses.Items = []DefaultAddressEntry{
		{Domain: "example.com", DefaultAddress: "someone@gmail.com"},
	}
	dest := epInventory("destination", "acct", "example.com")
	dest.DefaultAddresses.Items = []DefaultAddressEntry{
		{Domain: "example.com", DefaultAddress: ":fail: No Such User Here"},
	}

	p := BuildEmailPlan(src, dest, nil)
	op := findEmailOp(t, p, "default_address", "example.com")
	if op.Action != EmailActionSet {
		t.Fatalf("action = %q, want set (:fail: prefix is a fresh-account form)", op.Action)
	}
}

func TestEmailPlanDefaultAddressSkipCases(t *testing.T) {
	cases := []struct {
		name     string
		srcUser  string
		srcVal   string
		destUser string
		destVal  string
	}{
		{"identical address", "acct", "someone@gmail.com", "acct", "someone@gmail.com"},
		{"both account defaults", "srcacct", "srcacct", "destacct", "destacct"},
		{"both fail forms, locale-different tails", "acct", ":fail: No Such User Here", "acct", ":fail: no such address here"},
		{"both blackhole forms", "acct", ":blackhole:", "acct", ":blackhole: discarded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := epInventory("source", tc.srcUser, "example.com")
			src.DefaultAddresses.Items = []DefaultAddressEntry{{Domain: "example.com", DefaultAddress: tc.srcVal}}
			dest := epInventory("destination", tc.destUser, "example.com")
			dest.DefaultAddresses.Items = []DefaultAddressEntry{{Domain: "example.com", DefaultAddress: tc.destVal}}

			p := BuildEmailPlan(src, dest, nil)
			op := findEmailOp(t, p, "default_address", "example.com")
			if op.Action != EmailActionSkip {
				t.Fatalf("action = %q, want skip (reason %q)", op.Action, op.Reason)
			}
		})
	}
}

// A destination default that is neither identical nor a fresh-account form
// is somebody's decision: terminal manual, never overwritten.
func TestEmailPlanDefaultAddressManualWhenDestCustomized(t *testing.T) {
	src := epInventory("source", "acct", "example.com")
	src.DefaultAddresses.Items = []DefaultAddressEntry{
		{Domain: "example.com", DefaultAddress: "someone@gmail.com"},
	}
	dest := epInventory("destination", "acct", "example.com")
	dest.DefaultAddresses.Items = []DefaultAddressEntry{
		{Domain: "example.com", DefaultAddress: "other@custom.example"},
	}

	p := BuildEmailPlan(src, dest, nil)
	op := findEmailOp(t, p, "default_address", "example.com")
	if op.Action != EmailActionManual {
		t.Fatalf("action = %q, want manual", op.Action)
	}
}

func TestEmailPlanDefaultAddressManualWhenDomainMissingOnDest(t *testing.T) {
	src := epInventory("source", "acct", "example.com")
	src.DefaultAddresses.Items = []DefaultAddressEntry{
		{Domain: "example.com", DefaultAddress: "someone@gmail.com"},
		{Domain: "other.com", DefaultAddress: "someone@gmail.com"},
	}
	dest := epInventory("destination", "acct", "example.com")

	p := BuildEmailPlan(src, dest, nil)
	op := findEmailOp(t, p, "default_address", "other.com")
	if op.Action != EmailActionManual {
		t.Fatalf("action = %q, want manual", op.Action)
	}
}

// A source default the writer cannot round-trip onto a fresh dest
// (pipes, bare non-username values) is manual, not set.
func TestEmailPlanDefaultAddressManualUnroundtrippableSource(t *testing.T) {
	cases := []string{"|/home/acct/script.sh", "someotheruser", "a, b@c.com"}
	for _, srcVal := range cases {
		src := epInventory("source", "acct", "example.com")
		src.DefaultAddresses.Items = []DefaultAddressEntry{{Domain: "example.com", DefaultAddress: srcVal}}
		dest := epInventory("destination", "acct", "example.com")

		p := BuildEmailPlan(src, dest, nil)
		op := findEmailOp(t, p, "default_address", "example.com")
		if op.Action != EmailActionManual {
			t.Errorf("src %q: action = %q, want manual", srcVal, op.Action)
		}
	}
}

// Migrating a :fail:/:blackhole: source onto a fresh username dest is a
// real behavior change the writer can express: it plans as set.
func TestEmailPlanDefaultAddressSetSystemFormOntoFreshUsername(t *testing.T) {
	src := epInventory("source", "acct", "example.com")
	src.DefaultAddresses.Items = []DefaultAddressEntry{
		{Domain: "example.com", DefaultAddress: ":fail: No Such User Here"},
	}
	dest := epInventory("destination", "acct", "example.com") // fresh = "acct"

	p := BuildEmailPlan(src, dest, nil)
	op := findEmailOp(t, p, "default_address", "example.com")
	if op.Action != EmailActionSet {
		t.Fatalf("action = %q, want set (reason %q)", op.Action, op.Reason)
	}
	if op.Value != ":fail: No Such User Here" {
		t.Errorf("value = %q", op.Value)
	}
}

func TestEmailPlanDefaultAddressSectionUnavailable(t *testing.T) {
	src := epInventory("source", "acct", "example.com")
	src.DefaultAddresses.Items = []DefaultAddressEntry{
		{Domain: "example.com", DefaultAddress: "someone@gmail.com"},
	}
	dest := epInventory("destination", "acct", "example.com")
	dest.DefaultAddresses.Available = false
	dest.DefaultAddresses.Items = nil

	p := BuildEmailPlan(src, dest, nil)
	if len(opsBySection(p, "default_address")) != 0 {
		t.Errorf("no default_address ops expected when the section is unavailable, got %+v", p.Ops)
	}
	var found bool
	for _, ms := range p.ManualSections {
		if ms.Section == "default_address" {
			found = true
		}
	}
	if !found {
		t.Errorf("manual_sections = %+v, want default_address listed", p.ManualSections)
	}
}

// --- 2B-2/2B-3 sections carried as manual from day one ----------------------

func TestEmailPlanAutorespondersAlwaysManual(t *testing.T) {
	src := epInventory("source", "acct", "example.com")
	src.Autoresponders = []AutoresponderEntry{
		{Email: "info@example.com", Domain: "example.com", Subject: "Out of office", Interval: 3600},
	}
	dest := epInventory("destination", "acct", "example.com")
	// Even an apparently identical dest autoresponder stays manual: bodies
	// are not collected until 2B-2, equality cannot be proven.
	dest.Autoresponders = []AutoresponderEntry{
		{Email: "info@example.com", Domain: "example.com", Subject: "Out of office", Interval: 3600},
	}

	p := BuildEmailPlan(src, dest, nil)
	op := findEmailOp(t, p, "autoresponders", "info@example.com")
	if op.Action != EmailActionManual {
		t.Fatalf("action = %q, want manual", op.Action)
	}
	if !strings.Contains(op.Reason, "2B-2") {
		t.Errorf("reason %q should name the PR that will implement it", op.Reason)
	}
}

func TestEmailPlanFiltersAlwaysManual(t *testing.T) {
	src := epInventory("source", "acct", "example.com")
	src.EmailFilters.Items = []EmailFilterEntry{
		{Account: "", FilterName: "spam", Enabled: true, RuleCount: 2, ActionCount: 1},
	}
	dest := epInventory("destination", "acct", "example.com")

	p := BuildEmailPlan(src, dest, nil)
	op := findEmailOp(t, p, "email_filters", "(account-level)/spam")
	if op.Action != EmailActionManual {
		t.Fatalf("action = %q, want manual", op.Action)
	}
	if !strings.Contains(op.Reason, "2B-3") {
		t.Errorf("reason %q should name the PR that will implement it", op.Reason)
	}
}

func TestEmailPlanRoutingSkipWhenIdenticalManualWhenNot(t *testing.T) {
	src := epInventory("source", "acct", "example.com")
	src.EmailRouting.Items = []EmailRoutingEntry{{Domain: "example.com", Routing: "remote"}}
	dest := epInventory("destination", "acct", "example.com")
	dest.EmailRouting.Items = []EmailRoutingEntry{{Domain: "example.com", Routing: "local"}}

	p := BuildEmailPlan(src, dest, nil)
	op := findEmailOp(t, p, "email_routing", "example.com")
	if op.Action != EmailActionManual {
		t.Fatalf("differing routing: action = %q, want manual (reason %q)", op.Action, op.Reason)
	}

	dest.EmailRouting.Items = []EmailRoutingEntry{{Domain: "example.com", Routing: "remote"}}
	p = BuildEmailPlan(src, dest, nil)
	op = findEmailOp(t, p, "email_routing", "example.com")
	if op.Action != EmailActionSkip {
		t.Fatalf("identical routing: action = %q, want skip", op.Action)
	}
}

// --- plan envelope ----------------------------------------------------------

func TestEmailPlanEnvelopeAndDeterminism(t *testing.T) {
	src := epInventory("source", "srcacct", "example.com")
	src.Forwarders = []ForwarderEntry{
		{Source: "zeta@example.com", Destination: "z@gmail.com", Domain: "example.com"},
		{Source: "alpha@example.com", Destination: "a@gmail.com", Domain: "example.com"},
	}
	dest := epInventory("destination", "destacct", "example.com")

	p1 := BuildEmailPlan(src, dest, nil)
	p2 := BuildEmailPlan(src, dest, nil)

	if p1.Mode != "email-apply-plan" || p1.FormatVersion != 1 {
		t.Errorf("mode = %q, format_version = %d", p1.Mode, p1.FormatVersion)
	}
	if p1.SourceUser != "srcacct" || p1.DestinationUser != "destacct" {
		t.Errorf("users = %q/%q", p1.SourceUser, p1.DestinationUser)
	}
	if len(p1.Ops) != len(p2.Ops) {
		t.Fatalf("non-deterministic op count")
	}
	for i := range p1.Ops {
		if p1.Ops[i].Section != p2.Ops[i].Section || p1.Ops[i].Key != p2.Ops[i].Key {
			t.Fatalf("non-deterministic op order at %d", i)
		}
	}
	fw := opsBySection(p1, "forwarders")
	if len(fw) != 2 || fw[0].Key > fw[1].Key {
		t.Errorf("forwarder ops not sorted by key: %+v", fw)
	}
	// Ops must be array-typed even when empty (diff.go convention).
	empty := BuildEmailPlan(epInventory("source", "a", "x.com"), epInventory("destination", "a", "x.com"), nil)
	if empty.Ops == nil {
		t.Errorf("Ops must be non-nil")
	}
}

func TestEmailPlanPolicyContext(t *testing.T) {
	src := epInventory("source", "acct", "example.com")
	src.Forwarders = []ForwarderEntry{
		{Source: "info@example.com", Destination: "x@gmail.com", Domain: "example.com"},
	}
	dest := epInventory("destination", "acct", "example.com")
	policy := &PolicyReport{
		Findings: []PolicyFinding{
			{ID: "POL-FORWARDER-REMOVED", Section: "forwarders", Severity: SeverityReview, SourceRef: "info@example.com -> x@gmail.com"},
			{ID: "POL-DB-REMOVED", Section: "databases", Severity: SeverityBlocker},
			{ID: "POL-PHP-CHANGED", Section: "php", Severity: SeverityReview},
		},
	}

	p := BuildEmailPlan(src, dest, policy)
	if len(p.PolicyFindings) != 1 || !strings.Contains(p.PolicyFindings[0], "POL-FORWARDER-REMOVED") {
		t.Errorf("policy_findings = %v", p.PolicyFindings)
	}
	if len(p.NonEmailBlockers) != 1 || !strings.Contains(p.NonEmailBlockers[0], "POL-DB-REMOVED") {
		t.Errorf("non_email_blockers = %v", p.NonEmailBlockers)
	}
	// Policy is context, never a gate: the create op is still there.
	if op := findEmailOp(t, p, "forwarders", "info@example.com"); op.Action != EmailActionCreate {
		t.Errorf("policy must never gate: action = %q", op.Action)
	}
}
