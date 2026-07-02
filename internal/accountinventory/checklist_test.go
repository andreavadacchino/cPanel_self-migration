package accountinventory

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// chkNow is the injected reference time for certificate-validity checks:
// 2027-01-15T08:00:00Z as a fixed epoch, so tests never depend on the
// wall clock.
var chkNow = time.Unix(1_800_000_000, 0).UTC()

const (
	chkCertValidUntil   = int64(1_900_000_000) // after chkNow → valid
	chkCertExpiredUntil = int64(1_700_000_000) // before chkNow → expired
)

// chkInventory builds a rich, fully-available inventory. Both sides are
// built independently with IDENTICAL data by default, so the base diff is
// clean; tests mutate one side explicitly.
func chkInventory(side, host, user string) NormalizedInventory {
	inv := NewEmptyInventory(user, host, side)
	inv.Account.CollectedAt = "2026-07-02T00:00:00Z" // fixed: determinism tests compare full structs
	inv.Domains = []DomainEntry{{Name: "main.example", Type: "main", DocumentRoot: "/home/acct/public_html"}}
	inv.Mailboxes = []MailboxEntry{{Email: "info@main.example", Domain: "main.example", User: "info"}}
	inv.Databases = []DatabaseEntry{{Name: "acct_db1", Users: []string{"acct_dbu"}}}
	inv.Forwarders = []ForwarderEntry{{Source: "fwd@main.example", Destination: "info@main.example", Domain: "main.example"}}
	inv.FTP.Available = true
	inv.SSL.Available = true
	inv.SSL.Items = []SSLEntry{{
		Domains: "main.example", Issuer: "R3", ValidFrom: 1_690_000_000,
		ValidUntil: chkCertValidUntil, ValidationType: "dv",
	}}
	inv.PHP.Available = true
	inv.PHP.Items = []PHPEntry{{Domain: "main.example", Version: "ea-php81"}}
	inv.DNS.Available = true
	inv.DNS.Zones = []DNSZoneResult{planZone("main.example",
		aRec("main.example.", "1.2.3.4", 300),
		mxRec("main.example.", "main.example.", 0, 300),
	)}
	inv.Cron.Available = true
	inv.Cron.Jobs = []CronJobEntry{{
		Type: "standard", Minute: "5", Hour: "1", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		CommandRedacted: "php /home/acct/cronjob.php --token=****",
		CommandSHA256:   "sha256:aaa", RawLineSHA256: "sha256:bbb", Enabled: true, LineNumber: 1,
		Warnings: []string{},
	}}
	return inv
}

// chkEmptyInventory is a degenerate, fully-available inventory with no
// data at all: it pins the READY_TO_CUTOVER rollup logic.
func chkEmptyInventory(side, host, user string) NormalizedInventory {
	inv := NewEmptyInventory(user, host, side)
	inv.Account.CollectedAt = "2026-07-02T00:00:00Z"
	inv.FTP.Available = true
	inv.SSL.Available = true
	inv.PHP.Available = true
	inv.DNS.Available = true
	inv.Cron.Available = true
	return inv
}

func chkApplyReport() *MigrationReportInfo {
	return &MigrationReportInfo{
		RunID: "run-1", Mode: "apply",
		Scope:      MigrationReportScope{Mail: true, Files: true, Databases: true},
		ExitStatus: "success",
	}
}

// chkInput wires the real pipeline: diff and policy are computed from the
// inventories, never hand-built.
func chkInput(src, dest NormalizedInventory, plan *DNSPlan, rep *MigrationReportInfo) ChecklistInput {
	d := DiffInventories(src, dest)
	p := EvaluatePolicy(d)
	return ChecklistInput{
		Source: src, Destination: dest, Diff: d, Policy: p,
		DNSPlan: plan, MigrationReport: rep, Now: chkNow,
	}
}

func chkSection(t *testing.T, c MigrationChecklist, name string) ChecklistSection {
	t.Helper()
	for _, s := range c.Sections {
		if s.Section == name {
			return s
		}
	}
	t.Fatalf("section %q not found (sections: %d)", name, len(c.Sections))
	return ChecklistSection{}
}

func chkActionsOf(c MigrationChecklist, section, typ string) []ManualAction {
	var out []ManualAction
	for _, a := range c.ManualActions {
		if a.Section == section && a.Type == typ {
			out = append(out, a)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Overall status rollup
// ---------------------------------------------------------------------------

func TestBuildChecklistOverallBlocked(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.Mailboxes = []MailboxEntry{} // mailbox lost → policy blocker

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	if c.OverallStatus != OverallBlocked {
		t.Fatalf("overall = %q, want %q", c.OverallStatus, OverallBlocked)
	}
	mb := chkSection(t, c, "mailboxes")
	if mb.Status != SectionBlocked {
		t.Errorf("mailboxes status = %q, want %q", mb.Status, SectionBlocked)
	}
	found := false
	for _, b := range mb.Blockers {
		if strings.Contains(b, "POL-MAILBOX-REMOVED") {
			found = true
		}
	}
	if !found {
		t.Errorf("mailboxes blockers = %v, want POL-MAILBOX-REMOVED", mb.Blockers)
	}
	if acts := chkActionsOf(c, "mailboxes", MActionCreateOnDestination); len(acts) != 1 || !acts[0].BlockingCutover {
		t.Errorf("mailbox CREATE_ON_DESTINATION blocking action = %+v, want exactly 1 blocking", acts)
	}
}

func TestBuildChecklistManualActionRequiredFromSyntheticEmailRouting(t *testing.T) {
	// Identical inventories, full apply evidence: nothing is blocked, but
	// the account HAS mailboxes and email routing is not inventoried —
	// the operator must confirm it by hand before cutover.
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	if c.OverallStatus != OverallManualActionRequired {
		t.Fatalf("overall = %q, want %q", c.OverallStatus, OverallManualActionRequired)
	}
	er := chkSection(t, c, "email_routing")
	if er.Status != SectionNotInventoried {
		t.Errorf("email_routing status = %q, want %q", er.Status, SectionNotInventoried)
	}
	if acts := chkActionsOf(c, "email_routing", MActionConfirmEmailRouting); len(acts) != 1 || !acts[0].BlockingCutover {
		t.Errorf("CONFIRM_EMAIL_ROUTING = %+v, want exactly 1 blocking action", acts)
	}
}

func TestBuildChecklistNotReadyWithoutMigrationEvidence(t *testing.T) {
	// No mailboxes/forwarders (no blocking synthetics), but domains and a
	// database exist on the source and there is NO migration report: core
	// areas have zero migration evidence → NOT_READY.
	src := chkInventory("source", "1.2.3.4", "srcacct")
	src.Mailboxes = []MailboxEntry{}
	src.Forwarders = []ForwarderEntry{}
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.Mailboxes = []MailboxEntry{}
	dest.Forwarders = []ForwarderEntry{}

	c := BuildChecklist(chkInput(src, dest, nil, nil))

	if c.OverallStatus != OverallNotReady {
		t.Fatalf("overall = %q, want %q", c.OverallStatus, OverallNotReady)
	}
	wf := chkSection(t, c, "web_files")
	if wf.Status != SectionNotMigratedByTool {
		t.Errorf("web_files status = %q, want %q", wf.Status, SectionNotMigratedByTool)
	}
	if wf.MigratedByTool || wf.MigrationEvidence != EvidenceNone {
		t.Errorf("web_files evidence = (%v, %q), want (false, none)", wf.MigratedByTool, wf.MigrationEvidence)
	}
}

func TestBuildChecklistReadyWithManualNotes(t *testing.T) {
	// Same no-mail account WITH full apply evidence: only the non-blocking
	// redirects note remains.
	src := chkInventory("source", "1.2.3.4", "srcacct")
	src.Mailboxes = []MailboxEntry{}
	src.Forwarders = []ForwarderEntry{}
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.Mailboxes = []MailboxEntry{}
	dest.Forwarders = []ForwarderEntry{}

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	if c.OverallStatus != OverallReadyWithManualNotes {
		t.Fatalf("overall = %q, want %q", c.OverallStatus, OverallReadyWithManualNotes)
	}
	rd := chkSection(t, c, "redirects")
	if rd.Status != SectionNotInventoried {
		t.Errorf("redirects status = %q, want %q", rd.Status, SectionNotInventoried)
	}
	for _, a := range c.ManualActions {
		if a.BlockingCutover {
			t.Errorf("unexpected blocking action %s/%s", a.Section, a.Type)
		}
	}
}

func TestBuildChecklistReadyToCutoverOnEmptyAccount(t *testing.T) {
	// Degenerate empty account: no core data → no evidence needed, no
	// synthetic notes apply. Pins the READY_TO_CUTOVER branch.
	src := chkEmptyInventory("source", "1.2.3.4", "srcacct")
	dest := chkEmptyInventory("destination", "5.6.7.8", "srcacct")

	c := BuildChecklist(chkInput(src, dest, nil, nil))

	if c.OverallStatus != OverallReadyToCutover {
		t.Fatalf("overall = %q, want %q", c.OverallStatus, OverallReadyToCutover)
	}
	for _, name := range []string{"email_routing", "default_address", "email_filters", "redirects"} {
		if s := chkSection(t, c, name); s.Status != SectionNotApplicable {
			t.Errorf("%s status = %q, want %q", name, s.Status, SectionNotApplicable)
		}
	}
	// Root-only sections are always reported but never gate.
	for _, name := range []string{"quota_package", "server_level_config"} {
		if s := chkSection(t, c, name); s.Status != SectionNotAccessibleWithoutRoot {
			t.Errorf("%s status = %q, want %q", name, s.Status, SectionNotAccessibleWithoutRoot)
		}
	}
}

// ---------------------------------------------------------------------------
// migrated_by_tool honesty
// ---------------------------------------------------------------------------

func TestBuildChecklistEvidenceHonesty(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")

	tests := []struct {
		name         string
		report       *MigrationReportInfo
		wantEvidence string
		wantMigrated bool
		wantWarning  string
	}{
		{name: "no report", report: nil, wantEvidence: EvidenceNone, wantMigrated: false},
		{
			name:         "dry-run report is not evidence",
			report:       &MigrationReportInfo{RunID: "r", Mode: "dry-run", Scope: MigrationReportScope{Mail: true, Files: true, Databases: true}, ExitStatus: "success"},
			wantEvidence: EvidenceNone, wantMigrated: false, wantWarning: "not an apply run",
		},
		{
			name:         "failed apply is not evidence",
			report:       &MigrationReportInfo{RunID: "r", Mode: "apply", Scope: MigrationReportScope{Mail: true, Files: true, Databases: true}, ExitStatus: "failed"},
			wantEvidence: EvidenceNone, wantMigrated: false, wantWarning: "did not succeed",
		},
		{name: "successful apply", report: chkApplyReport(), wantEvidence: EvidenceRunLevel, wantMigrated: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := BuildChecklist(chkInput(src, dest, nil, tt.report))
			for _, name := range []string{"mailboxes", "databases", "web_files"} {
				s := chkSection(t, c, name)
				if s.MigratedByTool != tt.wantMigrated || s.MigrationEvidence != tt.wantEvidence {
					t.Errorf("%s evidence = (%v, %q), want (%v, %q)",
						name, s.MigratedByTool, s.MigrationEvidence, tt.wantMigrated, tt.wantEvidence)
				}
			}
			if tt.wantWarning != "" {
				found := false
				for _, w := range c.Warnings {
					if strings.Contains(w, tt.wantWarning) {
						found = true
					}
				}
				if !found {
					t.Errorf("warnings = %v, want one containing %q", c.Warnings, tt.wantWarning)
				}
			}
		})
	}
}

func TestBuildChecklistEvidenceRespectsScope(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	rep := &MigrationReportInfo{
		RunID: "r", Mode: "apply",
		Scope:      MigrationReportScope{Mail: true}, // mail only
		ExitStatus: "success",
	}

	c := BuildChecklist(chkInput(src, dest, nil, rep))

	if s := chkSection(t, c, "mailboxes"); !s.MigratedByTool || s.MigrationEvidence != EvidenceRunLevel {
		t.Errorf("mailboxes = (%v, %q), want (true, run_level)", s.MigratedByTool, s.MigrationEvidence)
	}
	for _, name := range []string{"databases", "web_files"} {
		if s := chkSection(t, c, name); s.MigratedByTool || s.MigrationEvidence != EvidenceNone {
			t.Errorf("%s = (%v, %q), want (false, none): out of the run's scope", name, s.MigratedByTool, s.MigrationEvidence)
		}
	}
	// Sections the tool has no importer for must NEVER claim migration.
	for _, name := range []string{"cron", "dns", "ssl", "ftp", "forwarders", "php"} {
		if s := chkSection(t, c, name); s.MigratedByTool || s.MigrationEvidence != EvidenceNone {
			t.Errorf("%s = (%v, %q), want (false, none): no importer exists", name, s.MigratedByTool, s.MigrationEvidence)
		}
	}
}

// ---------------------------------------------------------------------------
// Expected differences
// ---------------------------------------------------------------------------

func TestBuildChecklistDocrootDifferenceIsExpected(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.Domains = []DomainEntry{{Name: "main.example", Type: "main", DocumentRoot: "/home/other/public_html"}}

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	dom := chkSection(t, c, "domains")
	if dom.Status != SectionExpectedDifference {
		t.Errorf("domains status = %q, want %q", dom.Status, SectionExpectedDifference)
	}
	if len(dom.ExpectedDifferences) != 1 {
		t.Fatalf("domains expected differences = %d, want 1 (%+v)", len(dom.ExpectedDifferences), dom.ExpectedDifferences)
	}
}

func TestBuildChecklistSOADifferenceIsExpected(t *testing.T) {
	soa := func(serial string) DNSRecordEntry {
		return DNSRecordEntry{Type: "SOA", Name: "main.example.", TTL: 86400, Value: "ns1.host. root.host. " + serial}
	}
	src := chkInventory("source", "1.2.3.4", "srcacct")
	src.DNS.Zones = []DNSZoneResult{planZone("main.example", soa("2026010101"), aRec("main.example.", "1.2.3.4", 300))}
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.DNS.Zones = []DNSZoneResult{planZone("main.example", soa("2026070201"), aRec("main.example.", "1.2.3.4", 300))}

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	dns := chkSection(t, c, "dns")
	if dns.Status != SectionExpectedDifference {
		t.Errorf("dns status = %q, want %q", dns.Status, SectionExpectedDifference)
	}
	if len(dns.ExpectedDifferences) != 1 || !strings.Contains(dns.ExpectedDifferences[0].Key, "SOA") {
		t.Errorf("dns expected differences = %+v, want the SOA change", dns.ExpectedDifferences)
	}
}

func TestBuildChecklistARecordCoveredByPlanSkipIsExpected(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	// Destination apex A already points at the NEW address.
	dest.DNS.Zones = []DNSZoneResult{planZone("main.example",
		aRec("main.example.", "5.6.7.8", 300),
		mxRec("main.example.", "main.example.", 0, 300),
	)}

	plan, err := BuildDNSPlan(src, dest, nil, map[string]string{"1.2.3.4": "5.6.7.8"})
	if err != nil {
		t.Fatal(err)
	}

	// Without the plan the A change is an ordinary review.
	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))
	if dns := chkSection(t, c, "dns"); dns.Status != SectionReviewRequired {
		t.Errorf("dns status without plan = %q, want %q", dns.Status, SectionReviewRequired)
	}

	// With the plan proving the destination ALREADY matches the desired
	// translation (action=skip), the change is expected.
	c = BuildChecklist(chkInput(src, dest, &plan, chkApplyReport()))
	dns := chkSection(t, c, "dns")
	if dns.Status != SectionExpectedDifference {
		t.Errorf("dns status with plan = %q, want %q", dns.Status, SectionExpectedDifference)
	}
	if len(dns.ExpectedDifferences) == 0 {
		t.Error("dns expected differences empty, want the A rrset covered by plan skip")
	}
}

func TestBuildChecklistPendingPlanWorkIsNotExpected(t *testing.T) {
	// Destination zone exists but has no apex A record: the plan will say
	// "add" — that is PENDING work, not an expected difference.
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.DNS.Zones = []DNSZoneResult{planZone("main.example",
		mxRec("main.example.", "main.example.", 0, 300),
	)}

	plan, err := BuildDNSPlan(src, dest, nil, map[string]string{"1.2.3.4": "5.6.7.8"})
	if err != nil {
		t.Fatal(err)
	}
	c := BuildChecklist(chkInput(src, dest, &plan, chkApplyReport()))

	dns := chkSection(t, c, "dns")
	if dns.Status == SectionExpectedDifference || dns.Status == SectionOK {
		t.Errorf("dns status = %q: a pending plan add must not read as expected/ok", dns.Status)
	}
}

func TestBuildChecklistSSLChangedButValidIsExpected(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.SSL.Items = []SSLEntry{{
		Domains: "main.example", Issuer: "E1 (reissued)", ValidFrom: 1_790_000_000,
		ValidUntil: chkCertValidUntil, ValidationType: "dv",
	}}

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	ssl := chkSection(t, c, "ssl")
	if ssl.Status != SectionExpectedDifference {
		t.Errorf("ssl status = %q, want %q", ssl.Status, SectionExpectedDifference)
	}
	if len(ssl.ExpectedDifferences) == 0 {
		t.Error("ssl expected differences empty, want the reissued-but-valid certificate")
	}
	if acts := chkActionsOf(c, "ssl", MActionAcceptExpectedDiff); len(acts) == 0 {
		t.Error("want a non-blocking ACCEPT_EXPECTED_DIFFERENCE acknowledgment for the reissued certificate")
	} else {
		for _, a := range acts {
			if a.BlockingCutover {
				t.Error("ACCEPT_EXPECTED_DIFFERENCE must never block the cutover")
			}
		}
	}
}

func TestBuildChecklistSSLChangedAndExpiredStaysReview(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.SSL.Items = []SSLEntry{{
		Domains: "main.example", Issuer: "E1", ValidFrom: 1_600_000_000,
		ValidUntil: chkCertExpiredUntil, ValidationType: "dv",
	}}

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	ssl := chkSection(t, c, "ssl")
	if ssl.Status == SectionExpectedDifference || ssl.Status == SectionOK {
		t.Errorf("ssl status = %q: an EXPIRED destination certificate is never an expected difference", ssl.Status)
	}
	if acts := chkActionsOf(c, "ssl", MActionReissueSSL); len(acts) == 0 {
		t.Error("want a REISSUE_SSL action for the expired destination certificate")
	}
}

func TestBuildChecklistSSLRemovedButCoveredIsExpected(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	// Destination regrouped the SANs: the source cert key disappears, but
	// every domain it covered is still covered by a valid cert.
	dest.SSL.Items = []SSLEntry{{
		Domains: "main.example,www.main.example", Issuer: "E1", ValidFrom: 1_790_000_000,
		ValidUntil: chkCertValidUntil, ValidationType: "dv",
	}}

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	ssl := chkSection(t, c, "ssl")
	if ssl.Status == SectionBlocked {
		t.Errorf("ssl status = %q: removed cert fully covered by a valid destination cert must not block", ssl.Status)
	}
	if len(ssl.ExpectedDifferences) == 0 {
		t.Error("ssl expected differences empty, want the regrouped-but-covered certificate")
	}
}

func TestBuildChecklistSSLRemovedUncoveredStaysBlocked(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.SSL.Items = []SSLEntry{}

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	ssl := chkSection(t, c, "ssl")
	if ssl.Status != SectionBlocked {
		t.Errorf("ssl status = %q, want %q", ssl.Status, SectionBlocked)
	}
	if acts := chkActionsOf(c, "ssl", MActionReissueSSL); len(acts) == 0 {
		t.Error("want a blocking REISSUE_SSL action for the missing certificate")
	}
	if c.OverallStatus != OverallBlocked {
		t.Errorf("overall = %q, want %q", c.OverallStatus, OverallBlocked)
	}
}

// ---------------------------------------------------------------------------
// Manual actions taxonomy
// ---------------------------------------------------------------------------

func TestBuildChecklistCronActions(t *testing.T) {
	tests := []struct {
		name         string
		job          CronJobEntry
		wantType     string
		wantBlocking bool
	}{
		{
			name: "enabled job with home path",
			job: CronJobEntry{Type: "standard", Minute: "5", Hour: "1", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
				CommandRedacted: "php /home/acct/cronjob.php", CommandSHA256: "sha256:c1", RawLineSHA256: "sha256:r1",
				Enabled: true, LineNumber: 1, Warnings: []string{}},
			wantType: MActionAdaptCronPath, wantBlocking: true,
		},
		{
			name: "enabled job without home path",
			job: CronJobEntry{Type: "standard", Minute: "5", Hour: "1", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
				CommandRedacted: "curl -fsS https://example.com/ping", CommandSHA256: "sha256:c2", RawLineSHA256: "sha256:r2",
				Enabled: true, LineNumber: 1, Warnings: []string{}},
			wantType: MActionRecreateCron, wantBlocking: true,
		},
		{
			name: "disabled job",
			job: CronJobEntry{Type: "standard", Minute: "5", Hour: "1", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
				CommandRedacted: "php /home/acct/old.php", CommandSHA256: "sha256:c3", RawLineSHA256: "sha256:r3",
				Enabled: false, LineNumber: 1, Warnings: []string{}},
			wantType: MActionRecreateCron, wantBlocking: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := chkInventory("source", "1.2.3.4", "srcacct")
			src.Cron.Jobs = []CronJobEntry{tt.job}
			dest := chkInventory("destination", "5.6.7.8", "srcacct")
			dest.Cron.Jobs = []CronJobEntry{}

			c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

			acts := chkActionsOf(c, "cron", tt.wantType)
			if len(acts) != 1 {
				t.Fatalf("cron %s actions = %d, want 1 (all: %+v)", tt.wantType, len(acts), c.ManualActions)
			}
			if acts[0].BlockingCutover != tt.wantBlocking {
				t.Errorf("blocking = %v, want %v", acts[0].BlockingCutover, tt.wantBlocking)
			}
		})
	}
}

func TestBuildChecklistMXChangeYieldsConfirmMXExternal(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.DNS.Zones = []DNSZoneResult{planZone("main.example",
		aRec("main.example.", "1.2.3.4", 300),
		mxRec("main.example.", "other-host.example.", 10, 300),
	)}

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	dns := chkSection(t, c, "dns")
	if dns.Status != SectionBlocked {
		t.Errorf("dns status = %q, want %q (MX changed is a policy blocker)", dns.Status, SectionBlocked)
	}
	if acts := chkActionsOf(c, "dns", MActionConfirmMXExternal); len(acts) != 1 || !acts[0].BlockingCutover {
		t.Errorf("CONFIRM_MX_EXTERNAL = %+v, want exactly 1 blocking", acts)
	}
	if c.OverallStatus != OverallBlocked {
		t.Errorf("overall = %q, want %q", c.OverallStatus, OverallBlocked)
	}
}

func TestBuildChecklistDNSPlanManualOps(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	src.DNS.Zones = []DNSZoneResult{planZone("main.example",
		aRec("main.example.", "1.2.3.4", 300),
		txtRec("main.example.", "v=spf1 +a +mx +ip4:1.2.3.4 ~all", 300),
	)}
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.DNS.Zones = []DNSZoneResult{planZone("main.example",
		aRec("main.example.", "5.6.7.8", 300),
	)}

	plan, err := BuildDNSPlan(src, dest, nil, map[string]string{"1.2.3.4": "5.6.7.8"})
	if err != nil {
		t.Fatal(err)
	}
	c := BuildChecklist(chkInput(src, dest, &plan, chkApplyReport()))

	if acts := chkActionsOf(c, "dns", MActionUpdateSPF); len(acts) != 1 || !acts[0].BlockingCutover {
		t.Errorf("UPDATE_SPF = %+v, want exactly 1 blocking (SPF carries the mapped source address)", acts)
	}
}

func TestBuildChecklistForwarderRemovedBlocksViaAction(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.Forwarders = []ForwarderEntry{}

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	fw := chkSection(t, c, "forwarders")
	if fw.Status != SectionNotMigratedByTool {
		t.Errorf("forwarders status = %q, want %q (source-only data, no importer)", fw.Status, SectionNotMigratedByTool)
	}
	if acts := chkActionsOf(c, "forwarders", MActionCreateOnDestination); len(acts) != 1 || !acts[0].BlockingCutover {
		t.Errorf("forwarder CREATE_ON_DESTINATION = %+v, want exactly 1 blocking (mail flow)", acts)
	}
	if c.OverallStatus != OverallManualActionRequired {
		t.Errorf("overall = %q, want %q", c.OverallStatus, OverallManualActionRequired)
	}
}

func TestBuildChecklistPHPChangeYieldsCompatCheck(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.PHP.Items = []PHPEntry{{Domain: "main.example", Version: "ea-php82"}}

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	if acts := chkActionsOf(c, "php", MActionCheckPHPCompat); len(acts) != 1 || acts[0].BlockingCutover {
		t.Errorf("CHECK_PHP_COMPATIBILITY = %+v, want exactly 1 non-blocking", acts)
	}
}

// ---------------------------------------------------------------------------
// Synthetic sections, structure, determinism
// ---------------------------------------------------------------------------

func TestBuildChecklistSyntheticSectionsAlwaysPresent(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	want := map[string]string{
		"email_routing":       SectionNotInventoried,
		"default_address":     SectionNotInventoried,
		"email_filters":       SectionNotInventoried,
		"redirects":           SectionNotInventoried,
		"quota_package":       SectionNotAccessibleWithoutRoot,
		"server_level_config": SectionNotAccessibleWithoutRoot,
	}
	for name, status := range want {
		if s := chkSection(t, c, name); s.Status != status {
			t.Errorf("%s status = %q, want %q", name, s.Status, status)
		}
	}
	if acts := chkActionsOf(c, "email_filters", MActionManualCheckRequired); len(acts) != 1 || !acts[0].BlockingCutover {
		t.Errorf("email_filters MANUAL_CHECK_REQUIRED = %+v, want exactly 1 blocking", acts)
	}
	if acts := chkActionsOf(c, "redirects", MActionManualCheckRequired); len(acts) != 1 || acts[0].BlockingCutover {
		t.Errorf("redirects MANUAL_CHECK_REQUIRED = %+v, want exactly 1 non-blocking", acts)
	}
}

func TestBuildChecklistAccountAndModeFields(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "destacct")

	c := BuildChecklist(chkInput(src, dest, nil, nil))

	if c.Mode != "migration-checklist" || c.FormatVersion != 1 {
		t.Errorf("mode/version = %q/%d, want migration-checklist/1", c.Mode, c.FormatVersion)
	}
	if c.Account != "srcacct" {
		t.Errorf("account = %q, want the SOURCE account user", c.Account)
	}
	if c.ChainVerified {
		t.Error("chain_verified must be false until diff/policy record their own input hashes (PR 7B)")
	}
}

func TestBuildChecklistDeterministicAndConsistent(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.Cron.Jobs = []CronJobEntry{}
	dest.PHP.Items = []PHPEntry{{Domain: "main.example", Version: "ea-php82"}}
	in := chkInput(src, dest, nil, chkApplyReport())

	a := BuildChecklist(in)
	b := BuildChecklist(in)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("BuildChecklist is not deterministic: two runs differ")
	}
	ja, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	jb, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(ja) != string(jb) {
		t.Fatal("JSON output differs between two identical runs")
	}

	// Action IDs are unique and cross-referenced both ways.
	ids := map[string]string{}
	for _, act := range a.ManualActions {
		if act.ID == "" {
			t.Errorf("action without ID: %+v", act)
		}
		if prev, dup := ids[act.ID]; dup {
			t.Errorf("duplicate action ID %s (%s and %s)", act.ID, prev, act.Section)
		}
		ids[act.ID] = act.Section
	}
	for _, s := range a.Sections {
		for _, ref := range s.ManualActionRefs {
			if ids[ref] != s.Section {
				t.Errorf("section %s references action %s owned by %q", s.Section, ref, ids[ref])
			}
		}
	}
	refd := map[string]bool{}
	for _, s := range a.Sections {
		for _, ref := range s.ManualActionRefs {
			refd[ref] = true
		}
	}
	for id := range ids {
		if !refd[id] {
			t.Errorf("action %s not referenced by any section", id)
		}
	}

	// Summary consistency.
	if a.Summary.ManualActions != len(a.ManualActions) {
		t.Errorf("summary.manual_actions = %d, want %d", a.Summary.ManualActions, len(a.ManualActions))
	}
	if a.Summary.Accepted != 0 {
		t.Errorf("summary.accepted = %d, want 0 (reserved for PR 7D)", a.Summary.Accepted)
	}
}

func TestBuildChecklistJSONHasNoNullArrays(t *testing.T) {
	src := chkEmptyInventory("source", "1.2.3.4", "srcacct")
	dest := chkEmptyInventory("destination", "5.6.7.8", "srcacct")

	c := BuildChecklist(chkInput(src, dest, nil, nil))
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), ": null") {
		t.Errorf("checklist JSON contains null arrays/objects:\n%s", b)
	}
}

func TestBuildChecklistSectionUnavailableGates(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	src.Cron = NewEmptyCronSection() // unavailable on source
	dest := chkInventory("destination", "5.6.7.8", "srcacct")

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	cron := chkSection(t, c, "cron")
	if cron.Status != SectionReviewRequired {
		t.Errorf("cron status = %q, want %q (skipped comparison can never be ok)", cron.Status, SectionReviewRequired)
	}
}
