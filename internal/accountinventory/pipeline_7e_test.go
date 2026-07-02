package accountinventory

import (
	"strings"
	"testing"
)

// PR 7E-2: the four former not_inventoried areas flow through
// diff → policy → checklist. Scenarios mirror the byte-verified capture
// facts (PR7E_PRE_CAPTURES.md): the local/remote routing pair, the
// CMS-vs-genuine redirect split, count-only filters.

// --- diff adapters ---------------------------------------------------------

func TestDiff7ESectionsPresent(t *testing.T) {
	d := DiffInventories(baseInventory(), baseInventory())
	for _, name := range []string{"email_routing", "default_address", "email_filters", "redirects"} {
		if _, ok := d.Sections[name]; !ok {
			t.Errorf("section %q missing from diff", name)
		}
	}
	assertClean(t, d)
}

func TestDiffEmailRoutingComparesOnlyOperatorFacts(t *testing.T) {
	src, dest := baseInventory(), baseInventory()
	// detected + MX summary differ, but the operator-set facts are
	// identical: MX rrsets are the dns section's job, detected is
	// cPanel's runtime guess — no change may be reported.
	dest.EmailRouting.Items[0].Detected = "remote"
	dest.EmailRouting.Items[0].MXRecords = []MXRecordEntry{{Priority: 10, Exchange: "elsewhere.example"}}
	d := DiffInventories(src, dest)
	if got := d.Sections["email_routing"]; len(got.Changed) != 0 {
		t.Errorf("changed = %+v, want none (detected/mx are not compared fields)", got.Changed)
	}

	dest.EmailRouting.Items[0].Routing = "remote"
	dest.EmailRouting.Items[0].AlwaysAccept = false
	d = DiffInventories(src, dest)
	got := d.Sections["email_routing"]
	if len(got.Changed) != 2 {
		t.Fatalf("changed = %+v, want routing + always_accept", got.Changed)
	}
}

func TestDiffEmailFiltersKeyedPerAccount(t *testing.T) {
	src, dest := baseInventory(), baseInventory()
	// Same filter name in two scopes must NOT collide on one key.
	src.EmailFilters.Items = []EmailFilterEntry{
		{Account: "", FilterName: "spam", Enabled: true, RuleCount: 1, ActionCount: 1},
		{Account: "info@main.example", FilterName: "spam", Enabled: true, RuleCount: 1, ActionCount: 1},
	}
	dest.EmailFilters.Items = nil
	d := DiffInventories(src, dest)
	got := d.Sections["email_filters"]
	if len(got.Removed) != 2 || len(got.Warnings) != 0 {
		t.Fatalf("removed = %+v warnings = %v, want 2 removals and no duplicate-key warning", got.Removed, got.Warnings)
	}
}

func TestDiffRedirectDetailCarriesClassification(t *testing.T) {
	src, dest := baseInventory(), baseInventory()
	src.Redirects.Items = []RedirectEntry{{
		Domain: "shop.example", Source: "/([0-9])/.+.jpg", Destination: "%{ENV:REWRITEBASE}img/p/$1.jpg",
		Kind: "rewrite", Type: "temporary", StatusCode: 0,
	}}
	dest.Redirects.Items = nil
	d := DiffInventories(src, dest)
	got := d.Sections["redirects"]
	if len(got.Removed) != 1 || !strings.HasPrefix(got.Removed[0].Detail, "rewrite/temporary/- ") {
		t.Fatalf("removed = %+v, want the CMS classification prefix in the detail", got.Removed)
	}
}

// --- policy ----------------------------------------------------------------

func TestPolicy7ERoutingChangeIsReview(t *testing.T) {
	src, dest := baseInventory(), baseInventory()
	dest.EmailRouting.Items[0].Routing = "remote"
	p := EvaluatePolicy(DiffInventories(src, dest))
	found := false
	for _, f := range p.Findings {
		if f.ID == "POL-MAILROUTE-CHANGED" && f.Severity == SeverityReview {
			found = true
		}
	}
	if !found {
		t.Errorf("findings = %+v, want POL-MAILROUTE-CHANGED review", p.Findings)
	}
}

func TestPolicyRedirectCMSRemovedIsInfo(t *testing.T) {
	src, dest := baseInventory(), baseInventory()
	src.Redirects.Items = []RedirectEntry{
		{Domain: "shop.example", Source: "/([0-9])/.+.jpg", Destination: "%{ENV:REWRITEBASE}img/p/$1.jpg",
			Kind: "rewrite", Type: "temporary", StatusCode: 0},
		{Domain: "shop.example", Source: "/old", Destination: "https://a.example/",
			Kind: "rewrite", Type: "permanent", StatusCode: 301},
	}
	dest.Redirects.Items = nil
	p := EvaluatePolicy(DiffInventories(src, dest))
	var cms, genuine *PolicyFinding
	for i, f := range p.Findings {
		switch f.ID {
		case "POL-REDIRECT-CMS-REMOVED":
			cms = &p.Findings[i]
		case "POL-REDIRECT-REMOVED":
			genuine = &p.Findings[i]
		}
	}
	if cms == nil || cms.Severity != SeverityInfo {
		t.Errorf("CMS rewrite finding = %+v, want info", cms)
	}
	if genuine == nil || genuine.Severity != SeverityReview {
		t.Errorf("genuine redirect finding = %+v, want review", genuine)
	}
}

// --- checklist ---------------------------------------------------------------

func TestChecklistFilterRemovedYieldsBlockingRecreate(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	src.EmailFilters.Items = []EmailFilterEntry{{
		Account: "info@main.example", FilterName: "spam-to-junk", Enabled: true, RuleCount: 1, ActionCount: 1,
	}}

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	acts := chkActionsOf(c, "email_filters", MActionRecreateEmailFilters)
	if len(acts) != 1 || !acts[0].BlockingCutover {
		t.Fatalf("RECREATE_EMAIL_FILTERS = %+v, want exactly 1 blocking", acts)
	}
	if !acts[0].Acceptable {
		t.Errorf("RECREATE_EMAIL_FILTERS must be acceptable (operator can vouch it is obsolete)")
	}
	if c.OverallStatus != OverallManualActionRequired {
		t.Errorf("overall = %q, want %q", c.OverallStatus, OverallManualActionRequired)
	}
}

func TestChecklistCMSRewriteRemovedIsExpectedDifference(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	src.Redirects.Items = []RedirectEntry{{
		Domain: "main.example", Source: "/([0-9])/.+.jpg", Destination: "%{ENV:REWRITEBASE}img/p/$1.jpg",
		Kind: "rewrite", Type: "temporary", StatusCode: 0,
	}}

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	rd := chkSection(t, c, "redirects")
	if len(rd.ExpectedDifferences) != 1 || !strings.Contains(rd.ExpectedDifferences[0].Reason, "web files") {
		t.Errorf("expected differences = %+v, want the CMS travels-with-webfiles reason", rd.ExpectedDifferences)
	}
	if acts := chkActionsOf(c, "redirects", MActionConfirmRedirect); len(acts) != 0 {
		t.Errorf("CONFIRM_REDIRECT = %+v, want none for a CMS rewrite", acts)
	}
	if rd.Status != SectionExpectedDifference {
		t.Errorf("redirects status = %q, want %q", rd.Status, SectionExpectedDifference)
	}
}

// TestChecklistDKIMRegeneratedGetsConfirmAction pins 7A smoke finding 3:
// a dns-plan REPLACE op on a _domainkey TXT (destination regenerated the
// DKIM key) must surface a dedicated non-blocking CONFIRM_DNS_RECORD
// action instead of staying a silent review.
func TestChecklistDKIMRegeneratedGetsConfirmAction(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	src.DNS.Zones[0].Records = append(src.DNS.Zones[0].Records, DNSRecordEntry{
		Type: "TXT", Name: "default._domainkey", TTL: 14400,
		Value: "v=DKIM1; k=rsa; p=OLDKEY", TxtData: "v=DKIM1; k=rsa; p=OLDKEY",
	})
	dest.DNS.Zones[0].Records = append(dest.DNS.Zones[0].Records, DNSRecordEntry{
		Type: "TXT", Name: "default._domainkey", TTL: 14400,
		Value: "v=DKIM1; k=rsa; p=NEWKEY", TxtData: "v=DKIM1; k=rsa; p=NEWKEY",
	})
	// Plans emit canonical FQDN rrset names (6B canonicalization).
	plan := &DNSPlan{Zones: []PlanZone{{
		Zone: "main.example",
		Ops: []PlanOp{{
			Action: ActionReplace, Type: "TXT", Name: "default._domainkey.main.example.",
			Reason: "destination value differs from the translated source value",
		}},
	}}}

	c := BuildChecklist(chkInput(src, dest, plan, chkApplyReport()))

	var dkim []ManualAction
	for _, a := range chkActionsOf(c, "dns", MActionConfirmDNSRecord) {
		if strings.Contains(a.Title, "DKIM") {
			dkim = append(dkim, a)
		}
	}
	if len(dkim) != 1 || dkim[0].BlockingCutover {
		t.Fatalf("DKIM CONFIRM_DNS_RECORD = %+v, want exactly 1 non-blocking", dkim)
	}
}
