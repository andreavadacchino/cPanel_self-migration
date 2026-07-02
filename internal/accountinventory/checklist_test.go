package accountinventory

import (
	"encoding/json"
	"reflect"
	"regexp"
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
	inv.EmailRouting.Available = true
	inv.EmailRouting.Items = []EmailRoutingEntry{{
		Domain: "main.example", Routing: "local", Detected: "local", AlwaysAccept: true,
		MXRecords: []MXRecordEntry{{Priority: 0, Exchange: "main.example"}},
	}}
	inv.DefaultAddresses.Available = true
	inv.DefaultAddresses.Items = []DefaultAddressEntry{{
		Domain: "main.example", DefaultAddress: `":fail: No Such User Here"`,
	}}
	inv.EmailFilters.Available = true
	inv.EmailFilters.Items = []EmailFilterEntry{}
	inv.Redirects.Available = true
	inv.Redirects.Items = []RedirectEntry{}
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
	inv.EmailRouting.Available = true
	inv.DefaultAddresses.Available = true
	inv.EmailFilters.Available = true
	inv.Redirects.Available = true
	return inv
}

func chkApplyReport() *MigrationReportInfo {
	return &MigrationReportInfo{
		RunID: "run-1", Mode: "apply",
		Scope:      MigrationReportScope{Mail: true, Files: true, Databases: true},
		ExitStatus: "success",
	}
}

// chkApplyReportWithPhases is chkApplyReport plus a phases_completed list,
// as a PR 7C apply run records it in report.json.
func chkApplyReportWithPhases(phases ...string) *MigrationReportInfo {
	r := chkApplyReport()
	r.PhasesCompleted = phases
	return r
}

// chkAllApplyPhases is the full phase set of a whole-scope apply run.
var chkAllApplyPhases = []string{
	"create_domains", "migrate_mail", "verify_mail",
	"copy_files", "verify_files", "migrate_db", "verify_db",
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

func TestBuildChecklistManualActionRequiredFromEmailRoutingChange(t *testing.T) {
	// PR 7E: email routing is a real section now. A routing-mode change
	// (local → remote) silently breaks delivery, so it must produce a
	// blocking per-domain confirmation.
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.EmailRouting.Items[0].Routing = "remote"

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	if c.OverallStatus != OverallManualActionRequired {
		t.Fatalf("overall = %q, want %q", c.OverallStatus, OverallManualActionRequired)
	}
	er := chkSection(t, c, "email_routing")
	if er.Status != SectionManualRequired {
		t.Errorf("email_routing status = %q, want %q", er.Status, SectionManualRequired)
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
	// Same no-mail account WITH full apply evidence: only a non-blocking
	// redirect confirmation remains.
	src := chkInventory("source", "1.2.3.4", "srcacct")
	src.Mailboxes = []MailboxEntry{}
	src.Forwarders = []ForwarderEntry{}
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.Mailboxes = []MailboxEntry{}
	dest.Forwarders = []ForwarderEntry{}

	// A genuine (non-CMS) redirect whose destination differs: the only
	// remaining note is the non-blocking CONFIRM_REDIRECT.
	src.Redirects.Items = []RedirectEntry{{
		Domain: "main.example", Source: "/old", Destination: "https://a.example/",
		Kind: "redirect", Type: "permanent", StatusCode: 301,
	}}
	dest.Redirects.Items = []RedirectEntry{{
		Domain: "main.example", Source: "/old", Destination: "https://b.example/",
		Kind: "redirect", Type: "permanent", StatusCode: 301,
	}}

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	if c.OverallStatus != OverallReadyWithManualNotes {
		t.Fatalf("overall = %q, want %q", c.OverallStatus, OverallReadyWithManualNotes)
	}
	rd := chkSection(t, c, "redirects")
	if rd.Status != SectionManualRequired {
		t.Errorf("redirects status = %q, want %q", rd.Status, SectionManualRequired)
	}
	if acts := chkActionsOf(c, "redirects", MActionConfirmRedirect); len(acts) != 1 || acts[0].BlockingCutover {
		t.Errorf("CONFIRM_REDIRECT = %+v, want exactly 1 non-blocking", acts)
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

// TestBuildChecklistEvidencePerItemWithApplyPhases pins the PR 7C upgrade: a
// successful apply report whose phases_completed proves BOTH the migrate and
// the verify phase of a flow raises that section's evidence to per_item (the
// verify phases are per-item integrity passes whose failures gate the exit
// status). Domains have no verify phase; create_domains alone suffices.
func TestBuildChecklistEvidencePerItemWithApplyPhases(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReportWithPhases(chkAllApplyPhases...)))

	for _, name := range []string{"domains", "mailboxes", "web_files", "databases"} {
		s := chkSection(t, c, name)
		if !s.MigratedByTool || s.MigrationEvidence != EvidencePerItem {
			t.Errorf("%s = (%v, %q), want (true, per_item)", name, s.MigratedByTool, s.MigrationEvidence)
		}
	}
}

// TestBuildChecklistEvidencePartialPhasesStayRunLevel: a flow whose verify
// phase is missing from phases_completed keeps run_level — completing the
// migrate phase alone does not prove per-item verification.
func TestBuildChecklistEvidencePartialPhasesStayRunLevel(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")

	// verify_mail and verify_db are missing; the file flow is complete.
	c := BuildChecklist(chkInput(src, dest, nil,
		chkApplyReportWithPhases("create_domains", "migrate_mail", "copy_files", "verify_files", "migrate_db")))

	for name, want := range map[string]string{
		"domains":   EvidencePerItem,
		"mailboxes": EvidenceRunLevel,
		"web_files": EvidencePerItem,
		"databases": EvidenceRunLevel,
	} {
		s := chkSection(t, c, name)
		if !s.MigratedByTool || s.MigrationEvidence != want {
			t.Errorf("%s = (%v, %q), want (true, %q)", name, s.MigratedByTool, s.MigrationEvidence, want)
		}
	}
}

// TestBuildChecklistEvidenceLegacyReportStaysRunLevel: a pre-7C report.json
// has no phases_completed at all — evidence stays run_level, never per_item.
func TestBuildChecklistEvidenceLegacyReportStaysRunLevel(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	for _, name := range []string{"domains", "mailboxes", "web_files", "databases"} {
		s := chkSection(t, c, name)
		if !s.MigratedByTool || s.MigrationEvidence != EvidenceRunLevel {
			t.Errorf("%s = (%v, %q), want (true, run_level)", name, s.MigratedByTool, s.MigrationEvidence)
		}
	}
}

// TestBuildChecklistEvidencePhasesNeverOverrideFailedExit: phases_completed
// on a FAILED apply run must not produce any evidence at all — the exit
// status gate comes first.
func TestBuildChecklistEvidencePhasesNeverOverrideFailedExit(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	rep := chkApplyReportWithPhases(chkAllApplyPhases...)
	rep.ExitStatus = "failed"

	c := BuildChecklist(chkInput(src, dest, nil, rep))

	for _, name := range []string{"domains", "mailboxes", "web_files", "databases"} {
		s := chkSection(t, c, name)
		if s.MigratedByTool || s.MigrationEvidence != EvidenceNone {
			t.Errorf("%s = (%v, %q), want (false, none): failed run, phases are irrelevant", name, s.MigratedByTool, s.MigrationEvidence)
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

// Real-smoke finding 2 (PR7A_REAL_SMOKE.md): old wildcard certificate
// generations already EXPIRED on the source must not gate the cutover when
// their grouping is missing on the destination — there is nothing valid to
// migrate.
func TestBuildChecklistSSLRemovedExpiredOnSourceIsExpected(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	// Two expired wildcard generations share the same diff key; the
	// destination never regains a wildcard cert (AutoSSL per-vhost only).
	src.SSL.Items = append(src.SSL.Items,
		SSLEntry{Domains: "*.main.example,main.example", Issuer: "R3 gen1",
			ValidFrom: 1_500_000_000, ValidUntil: chkCertExpiredUntil, ValidationType: "dv"},
		SSLEntry{Domains: "*.main.example,main.example", Issuer: "R3 gen2",
			ValidFrom: 1_600_000_000, ValidUntil: chkCertExpiredUntil, ValidationType: "dv"},
	)

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	ssl := chkSection(t, c, "ssl")
	if ssl.Status == SectionBlocked {
		t.Errorf("ssl status = %q: a source-expired certificate group must not block the cutover", ssl.Status)
	}
	found := false
	for _, d := range ssl.ExpectedDifferences {
		if d.Key == "*.main.example,main.example" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected differences %v: want the expired source certificate group", ssl.ExpectedDifferences)
	}
	if acts := chkActionsOf(c, "ssl", MActionAcceptExpectedDiff); len(acts) == 0 {
		t.Error("want a non-blocking ACCEPT_EXPECTED_DIFFERENCE acknowledgment for the expired source certificate")
	} else {
		for _, a := range acts {
			if a.BlockingCutover {
				t.Error("ACCEPT_EXPECTED_DIFFERENCE must never block the cutover")
			}
		}
	}
	if acts := chkActionsOf(c, "ssl", MActionReissueSSL); len(acts) != 0 {
		t.Errorf("got %d REISSUE_SSL actions, want none: nothing valid was lost", len(acts))
	}
}

// Fail-safe: if ANY generation in the removed group is still valid on the
// source, the group is live and its loss keeps blocking.
func TestBuildChecklistSSLRemovedGroupWithValidGenerationStaysBlocked(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	src.SSL.Items = append(src.SSL.Items,
		SSLEntry{Domains: "*.main.example,main.example", Issuer: "R3 gen1",
			ValidFrom: 1_500_000_000, ValidUntil: chkCertExpiredUntil, ValidationType: "dv"},
		SSLEntry{Domains: "*.main.example,main.example", Issuer: "R3 gen2",
			ValidFrom: 1_700_000_000, ValidUntil: chkCertValidUntil, ValidationType: "dv"},
	)

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	ssl := chkSection(t, c, "ssl")
	if ssl.Status != SectionBlocked {
		t.Errorf("ssl status = %q, want %q: one generation is still valid on the source", ssl.Status, SectionBlocked)
	}
	if acts := chkActionsOf(c, "ssl", MActionReissueSSL); len(acts) == 0 {
		t.Error("want a blocking REISSUE_SSL action: a still-valid wildcard certificate was lost")
	}
}

// Fail-safe: an unknown expiry (ValidUntil == 0) is NOT proof of expiry.
func TestBuildChecklistSSLRemovedUnknownExpiryStaysBlocked(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	src.SSL.Items = append(src.SSL.Items,
		SSLEntry{Domains: "*.main.example,main.example", Issuer: "R3",
			ValidFrom: 1_500_000_000, ValidUntil: 0, ValidationType: "dv"},
	)

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	ssl := chkSection(t, c, "ssl")
	if ssl.Status != SectionBlocked {
		t.Errorf("ssl status = %q, want %q: unknown expiry must stay fail-safe", ssl.Status, SectionBlocked)
	}
}

// Fail-safe: a source certificate with NO domain list surfaces as the
// "(no domain list)" placeholder ref, which can never match a real Domains
// key — even when that certificate is itself expired, the removal must keep
// blocking (the tool cannot prove which domains it covered).
func TestBuildChecklistSSLNoDomainListExpiredStaysBlocked(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	src.SSL.Items = append(src.SSL.Items,
		SSLEntry{Domains: "", Issuer: "R3",
			ValidFrom: 1_500_000_000, ValidUntil: chkCertExpiredUntil, ValidationType: "dv"},
	)

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	ssl := chkSection(t, c, "ssl")
	if ssl.Status != SectionBlocked {
		t.Errorf("ssl status = %q, want %q: an unmatchable placeholder key must never downgrade", ssl.Status, SectionBlocked)
	}
	if acts := chkActionsOf(c, "ssl", MActionReissueSSL); len(acts) == 0 {
		t.Error("want a blocking REISSUE_SSL action for the certificate without a domain list")
	}
}

// Fail-safe: one expired generation followed by one with UNKNOWN expiry
// under the same key — the unknown entry must veto the downgrade even after
// an expired entry was already seen.
func TestBuildChecklistSSLRemovedExpiredThenUnknownStaysBlocked(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	src.SSL.Items = append(src.SSL.Items,
		SSLEntry{Domains: "*.main.example,main.example", Issuer: "R3 gen1",
			ValidFrom: 1_500_000_000, ValidUntil: chkCertExpiredUntil, ValidationType: "dv"},
		SSLEntry{Domains: "*.main.example,main.example", Issuer: "R3 gen2",
			ValidFrom: 1_600_000_000, ValidUntil: 0, ValidationType: "dv"},
	)

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	ssl := chkSection(t, c, "ssl")
	if ssl.Status != SectionBlocked {
		t.Errorf("ssl status = %q, want %q: an unknown-expiry generation must veto the expired-group downgrade", ssl.Status, SectionBlocked)
	}
}

func TestCertDomainCovers(t *testing.T) {
	cases := []struct {
		certDom, dom string
		want         bool
	}{
		{"main.example", "main.example", true},         // exact
		{"*.main.example", "shop.main.example", true},  // one extra label
		{"*.main.example", "main.example", false},      // never the base itself
		{"*.main.example", "a.b.main.example", false},  // never multi-label
		{"*.main.example", "*.main.example", true},     // literal wildcard match
		{"*.main.example", "*.other.example", false},   // wildcard query, other zone
		{"*.", "x", false},                             // bare wildcard, empty base
		{"*.a.b", "*.c.a.b", false},                    // wildcard query never synthesized
		{"shop.main.example", "*.main.example", false}, // per-host never covers a wildcard
		{"*.main.example", ".main.example", false},     // empty label
		{"", "", true}, // pre-existing exact-match semantics
		{"", "main.example", false},
	}
	for _, tc := range cases {
		if got := certDomainCovers(tc.certDom, tc.dom); got != tc.want {
			t.Errorf("certDomainCovers(%q, %q) = %v, want %v", tc.certDom, tc.dom, got, tc.want)
		}
	}
}

// Semantic wildcard coverage: a valid destination wildcard covers the
// single-label subdomain a removed per-host certificate used to serve.
func TestBuildChecklistSSLRemovedCoveredByWildcardIsExpected(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	src.SSL.Items = append(src.SSL.Items,
		SSLEntry{Domains: "shop.main.example", Issuer: "R3",
			ValidFrom: 1_700_000_000, ValidUntil: chkCertValidUntil, ValidationType: "dv"},
	)
	dest.SSL.Items = append(dest.SSL.Items,
		SSLEntry{Domains: "*.main.example", Issuer: "E1",
			ValidFrom: 1_790_000_000, ValidUntil: chkCertValidUntil, ValidationType: "dv"},
	)

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	ssl := chkSection(t, c, "ssl")
	if ssl.Status == SectionBlocked {
		t.Errorf("ssl status = %q: shop.main.example is covered by the valid destination wildcard", ssl.Status)
	}
	found := false
	for _, d := range ssl.ExpectedDifferences {
		if d.Key == "shop.main.example" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected differences %v: want the wildcard-covered certificate", ssl.ExpectedDifferences)
	}
}

// Wildcard coverage limits (fail-safe): *.base never covers the base domain
// itself, multi-label subdomains, or anything when the wildcard is expired;
// a lost wildcard is never covered by per-host certificates.
func TestBuildChecklistSSLWildcardCoverageLimits(t *testing.T) {
	cases := []struct {
		name    string
		srcCert SSLEntry
		dstCert SSLEntry
	}{
		{
			name: "base domain not covered by wildcard",
			srcCert: SSLEntry{Domains: "other.example", Issuer: "R3",
				ValidFrom: 1_700_000_000, ValidUntil: chkCertValidUntil, ValidationType: "dv"},
			dstCert: SSLEntry{Domains: "*.other.example", Issuer: "E1",
				ValidFrom: 1_790_000_000, ValidUntil: chkCertValidUntil, ValidationType: "dv"},
		},
		{
			name: "multi-label subdomain not covered by wildcard",
			srcCert: SSLEntry{Domains: "a.b.main.example", Issuer: "R3",
				ValidFrom: 1_700_000_000, ValidUntil: chkCertValidUntil, ValidationType: "dv"},
			dstCert: SSLEntry{Domains: "*.main.example", Issuer: "E1",
				ValidFrom: 1_790_000_000, ValidUntil: chkCertValidUntil, ValidationType: "dv"},
		},
		{
			name: "expired destination wildcard covers nothing",
			srcCert: SSLEntry{Domains: "shop.main.example", Issuer: "R3",
				ValidFrom: 1_700_000_000, ValidUntil: chkCertValidUntil, ValidationType: "dv"},
			dstCert: SSLEntry{Domains: "*.main.example", Issuer: "E1",
				ValidFrom: 1_500_000_000, ValidUntil: chkCertExpiredUntil, ValidationType: "dv"},
		},
		{
			name: "valid wildcard never covered by per-host certificates",
			srcCert: SSLEntry{Domains: "*.main.example", Issuer: "R3",
				ValidFrom: 1_700_000_000, ValidUntil: chkCertValidUntil, ValidationType: "dv"},
			dstCert: SSLEntry{Domains: "shop.main.example", Issuer: "E1",
				ValidFrom: 1_790_000_000, ValidUntil: chkCertValidUntil, ValidationType: "dv"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := chkInventory("source", "1.2.3.4", "srcacct")
			dest := chkInventory("destination", "5.6.7.8", "srcacct")
			src.SSL.Items = append(src.SSL.Items, tc.srcCert)
			dest.SSL.Items = append(dest.SSL.Items, tc.dstCert)

			c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

			ssl := chkSection(t, c, "ssl")
			if ssl.Status != SectionBlocked {
				t.Errorf("ssl status = %q, want %q", ssl.Status, SectionBlocked)
			}
			if acts := chkActionsOf(c, "ssl", MActionReissueSSL); len(acts) == 0 {
				t.Error("want a blocking REISSUE_SSL action for the uncovered certificate")
			}
		})
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

func TestBuildChecklistFormerSyntheticSectionsInventoried(t *testing.T) {
	// PR 7E: the four former not_inventoried areas are real sections.
	// With identical data on both sides they resolve like any other
	// section — NO blanket manual checks survive.
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")

	c := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	want := map[string]string{
		"email_routing":       SectionOK,
		"default_address":     SectionOK,
		"email_filters":       SectionNotApplicable, // zero filters on both sides
		"redirects":           SectionNotApplicable, // zero redirects on both sides
		"quota_package":       SectionNotAccessibleWithoutRoot,
		"server_level_config": SectionNotAccessibleWithoutRoot,
	}
	for name, status := range want {
		if s := chkSection(t, c, name); s.Status != status {
			t.Errorf("%s status = %q, want %q", name, s.Status, status)
		}
	}
	for _, sec := range []string{"email_routing", "default_address", "email_filters", "redirects"} {
		if acts := chkActionsOf(c, sec, MActionManualCheckRequired); len(acts) != 0 {
			t.Errorf("%s: blanket MANUAL_CHECK_REQUIRED survived: %+v", sec, acts)
		}
	}
	if c.Summary.NotInventoried != 0 {
		t.Errorf("summary.not_inventoried = %d, want 0", c.Summary.NotInventoried)
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
		t.Errorf("summary.accepted = %d, want 0 (no acceptance file was provided)", a.Summary.Accepted)
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

// ---------------------------------------------------------------------------
// Operator acceptances (PR 7D)
// ---------------------------------------------------------------------------

func chkAcceptance(key string) OperatorAcceptance {
	return OperatorAcceptance{
		ActionKey: key, Reason: "reviewed with the customer",
		AcceptedBy: "andrea", AcceptedAt: "2026-07-02T10:00:00Z",
	}
}

func chkInputWithAcceptances(src, dest NormalizedInventory, rep *MigrationReportInfo, accs []OperatorAcceptance) ChecklistInput {
	in := chkInput(src, dest, nil, rep)
	in.Acceptances = accs
	return in
}

// chkDivergeMailConfig makes the destination diverge on the three 7E
// mail areas, producing exactly three ACCEPTABLE blocking actions
// (CONFIRM_EMAIL_ROUTING, MANUAL_CHECK_REQUIRED on the default address,
// RECREATE_EMAIL_FILTERS) — the acceptance tests' baseline since the
// blanket not_inventoried actions no longer exist.
func chkDivergeMailConfig(src, dest *NormalizedInventory) {
	dest.EmailRouting.Items[0].Routing = "remote"
	dest.DefaultAddresses.Items[0].DefaultAddress = "info@main.example"
	src.EmailFilters.Items = []EmailFilterEntry{{
		Account: "", FilterName: "spam-to-junk", Enabled: true, RuleCount: 1, ActionCount: 1,
	}}
}

// TestManualActionKeysStableAndUnique pins the acceptance handle: every
// action carries a content-derived key (AK-<12 hex>) that is IDENTICAL
// across regenerations from the same inputs and unique per action, while
// the positional MA-nnn id may shift when findings change.
func TestManualActionKeysStableAndUnique(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	chkDivergeMailConfig(&src, &dest)

	c1 := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))
	c2 := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))

	if len(c1.ManualActions) == 0 {
		t.Fatal("want the three divergence-driven actions")
	}
	keyRe := regexp.MustCompile(`^AK-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for i, a := range c1.ManualActions {
		if !keyRe.MatchString(a.Key) {
			t.Errorf("action %s key = %q, want AK-<12 hex>", a.ID, a.Key)
		}
		if seen[a.Key] {
			t.Errorf("duplicate action key %q", a.Key)
		}
		seen[a.Key] = true
		if a.Key != c2.ManualActions[i].Key {
			t.Errorf("action %s key changed across identical regenerations: %q vs %q", a.ID, a.Key, c2.ManualActions[i].Key)
		}
	}
}

// TestBuildChecklistAcceptanceClearsManualGate: accepting every blocking
// (and acceptable) action moves the verdict from MANUAL_ACTION_REQUIRED to
// READY_WITH_MANUAL_NOTES, populates accepted_by_operator on the owning
// sections and summary.accepted — while the underlying policy reviews KEEP
// gating the section status.
func TestBuildChecklistAcceptanceClearsManualGate(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	chkDivergeMailConfig(&src, &dest)

	base := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))
	if base.OverallStatus != OverallManualActionRequired {
		t.Fatalf("baseline overall = %q, want %q", base.OverallStatus, OverallManualActionRequired)
	}
	var accs []OperatorAcceptance
	for _, a := range base.ManualActions {
		if a.BlockingCutover {
			if !a.Acceptable {
				t.Fatalf("baseline has a non-acceptable blocking action %s (%s) — scenario invalid", a.ID, a.Type)
			}
			accs = append(accs, chkAcceptance(a.Key))
		}
	}
	if len(accs) == 0 {
		t.Fatal("baseline has no blocking actions to accept — scenario invalid")
	}

	c := BuildChecklist(chkInputWithAcceptances(src, dest, chkApplyReport(), accs))

	if c.OverallStatus != OverallReadyWithManualNotes {
		t.Fatalf("overall = %q, want %q after accepting every blocking action", c.OverallStatus, OverallReadyWithManualNotes)
	}
	if c.Summary.Accepted != len(accs) {
		t.Errorf("summary.accepted = %d, want %d", c.Summary.Accepted, len(accs))
	}
	er := chkSection(t, c, "email_routing")
	if er.Status != SectionReviewRequired {
		t.Errorf("email_routing status = %q, want %q: acceptance clears the gate, not the underlying review", er.Status, SectionReviewRequired)
	}
	if len(er.AcceptedByOperator) != 1 {
		t.Errorf("email_routing accepted_by_operator = %v, want the accepted action id", er.AcceptedByOperator)
	}
	for _, a := range c.ManualActions {
		if a.BlockingCutover {
			if !a.Accepted || a.AcceptedBy != "andrea" || a.AcceptedReason == "" || a.AcceptedAt == "" {
				t.Errorf("action %s = %+v, want accepted with author/reason/date", a.ID, a)
			}
		}
	}
}

// TestBuildChecklistAcceptanceNonAcceptableIgnored: BOTH blocking cron
// action types (RECREATE_CRON and ADAPT_CRON_PATH) must be RESOLVED, not
// waved through — acceptances targeting them are ignored with a warning and
// the verdict stays blocked.
func TestBuildChecklistAcceptanceNonAcceptableIgnored(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	// A second enabled job WITHOUT /home/ paths → plain RECREATE_CRON; the
	// fixture job carries /home/acct → ADAPT_CRON_PATH.
	src.Cron.Jobs = append(src.Cron.Jobs, CronJobEntry{
		Type: "standard", Minute: "10", Hour: "2", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		CommandRedacted: "php -q cron.php --token=****",
		CommandSHA256:   "sha256:ccc", RawLineSHA256: "sha256:ddd", Enabled: true, LineNumber: 2,
		Warnings: []string{},
	})
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	dest.Cron.Jobs = []CronJobEntry{} // every source cron job lost

	base := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))
	keys := map[string]string{} // type -> key
	for _, a := range base.ManualActions {
		if (a.Type == MActionRecreateCron || a.Type == MActionAdaptCronPath) && a.BlockingCutover {
			if a.Acceptable {
				t.Fatalf("blocking %s must not be acceptable: %+v", a.Type, a)
			}
			keys[a.Type] = a.Key
		}
	}
	if keys[MActionRecreateCron] == "" || keys[MActionAdaptCronPath] == "" {
		t.Fatalf("baseline actions %v: want one blocking RECREATE_CRON and one blocking ADAPT_CRON_PATH", keys)
	}

	c := BuildChecklist(chkInputWithAcceptances(src, dest, chkApplyReport(),
		[]OperatorAcceptance{chkAcceptance(keys[MActionRecreateCron]), chkAcceptance(keys[MActionAdaptCronPath])}))

	if c.Summary.Accepted != 0 {
		t.Errorf("summary.accepted = %d, want 0: non-acceptable actions cannot be accepted", c.Summary.Accepted)
	}
	if c.OverallStatus != OverallBlocked {
		t.Errorf("overall = %q, want %q: the lost cron jobs still gate", c.OverallStatus, OverallBlocked)
	}
	for typ, key := range keys {
		found := false
		for _, w := range c.Warnings {
			if strings.Contains(w, "not acceptable") && strings.Contains(w, key) {
				found = true
			}
		}
		if !found {
			t.Errorf("warnings = %v, want one naming the non-acceptable %s key %s", c.Warnings, typ, key)
		}
	}
}

// TestBuildChecklistAcceptanceUnknownKeyWarns: an acceptance whose key
// matches nothing (the underlying fact changed since the operator reviewed
// it) is ignored with a warning — stale acceptances self-invalidate.
func TestBuildChecklistAcceptanceUnknownKeyWarns(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	chkDivergeMailConfig(&src, &dest)

	c := BuildChecklist(chkInputWithAcceptances(src, dest, chkApplyReport(),
		[]OperatorAcceptance{chkAcceptance("AK-000000000000")}))

	if c.Summary.Accepted != 0 {
		t.Errorf("summary.accepted = %d, want 0", c.Summary.Accepted)
	}
	if c.OverallStatus != OverallManualActionRequired {
		t.Errorf("overall = %q, want %q: nothing was actually accepted", c.OverallStatus, OverallManualActionRequired)
	}
	found := false
	for _, w := range c.Warnings {
		if strings.Contains(w, "AK-000000000000") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one naming the unmatched key", c.Warnings)
	}
}

// TestBuildChecklistAcceptanceDuplicateFirstWins: two entries for the same
// key — the first is applied, the duplicate warns.
func TestBuildChecklistAcceptanceDuplicateFirstWins(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	chkDivergeMailConfig(&src, &dest)

	base := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))
	var key string
	for _, a := range base.ManualActions {
		if a.Section == "email_routing" {
			key = a.Key
		}
	}
	first := chkAcceptance(key)
	second := chkAcceptance(key)
	second.AcceptedBy = "someone-else"

	c := BuildChecklist(chkInputWithAcceptances(src, dest, chkApplyReport(), []OperatorAcceptance{first, second}))

	if c.Summary.Accepted != 1 {
		t.Errorf("summary.accepted = %d, want 1", c.Summary.Accepted)
	}
	for _, a := range c.ManualActions {
		if a.Key == key && a.AcceptedBy != "andrea" {
			t.Errorf("accepted_by = %q, want the FIRST entry's author", a.AcceptedBy)
		}
	}
	found := false
	for _, w := range c.Warnings {
		if strings.Contains(w, "duplicate") && strings.Contains(w, key) {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want a duplicate-entry warning for %s", c.Warnings, key)
	}
}

// TestBuildChecklistAcceptanceDuplicateIdenticalActionsWarn (reviewer HIGH):
// two structurally identical actions (the same disabled cron job scheduled
// twice, both lost) share the same content key. Only the FIRST is accepted;
// the second must surface a warning instead of silently keeping the gate.
func TestBuildChecklistAcceptanceDuplicateIdenticalActionsWarn(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dup := CronJobEntry{
		Type: "standard", Minute: "5", Hour: "1", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		CommandRedacted: "php /usr/local/bin/report.php --token=****",
		CommandSHA256:   "sha256:eee", RawLineSHA256: "sha256:fff", Enabled: false, LineNumber: 3,
		Warnings: []string{},
	}
	dup2 := dup
	dup2.LineNumber = 4
	src.Cron.Jobs = append(src.Cron.Jobs, dup, dup2)
	dest := chkInventory("destination", "5.6.7.8", "srcacct")

	base := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))
	var keys []string
	for _, a := range base.ManualActions {
		if a.Type == MActionRecreateCron && !a.BlockingCutover {
			keys = append(keys, a.Key)
		}
	}
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("want 2 identical non-blocking RECREATE_CRON keys, got %v", keys)
	}

	c := BuildChecklist(chkInputWithAcceptances(src, dest, chkApplyReport(),
		[]OperatorAcceptance{chkAcceptance(keys[0])}))

	if c.Summary.Accepted != 1 {
		t.Errorf("summary.accepted = %d, want 1: only the first identical action is accepted", c.Summary.Accepted)
	}
	found := false
	for _, w := range c.Warnings {
		if strings.Contains(w, "more than one identical action") && strings.Contains(w, keys[0]) {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want the multi-match warning for key %s", c.Warnings, keys[0])
	}
}

// TestBuildChecklistAllAcceptedNeverReadyToCutover (reviewer coverage gap):
// accepting EVERY acceptable action can raise the verdict at most to
// READY_WITH_MANUAL_NOTES — an acceptance is a formal note, not an eraser.
func TestBuildChecklistAllAcceptedNeverReadyToCutover(t *testing.T) {
	src := chkInventory("source", "1.2.3.4", "srcacct")
	dest := chkInventory("destination", "5.6.7.8", "srcacct")
	chkDivergeMailConfig(&src, &dest)

	base := BuildChecklist(chkInput(src, dest, nil, chkApplyReport()))
	var accs []OperatorAcceptance
	for _, a := range base.ManualActions {
		if a.Acceptable {
			accs = append(accs, chkAcceptance(a.Key))
		}
	}
	if len(accs) == 0 {
		t.Fatal("no acceptable actions in the baseline — scenario invalid")
	}

	c := BuildChecklist(chkInputWithAcceptances(src, dest, chkApplyReport(), accs))

	if c.Summary.Accepted != len(accs) {
		t.Fatalf("summary.accepted = %d, want %d", c.Summary.Accepted, len(accs))
	}
	if c.OverallStatus != OverallReadyWithManualNotes {
		t.Errorf("overall = %q, want %q — never READY_TO_CUTOVER while accepted actions exist",
			c.OverallStatus, OverallReadyWithManualNotes)
	}
}
