package accountinventory

import (
	"strings"
	"testing"
)

// emptyDiff returns a diff with all 10 sections present and clean.
func emptyDiff() InventoryDiff {
	return DiffInventories(baseInventory(), baseInventory())
}

// diffWith returns an empty diff with one section replaced.
func diffWith(section string, sec SectionDiff) InventoryDiff {
	d := emptyDiff()
	d.Sections[section] = sec
	// Keep the summary coherent with the mutation.
	d.Summary.Added += len(sec.Added)
	d.Summary.Removed += len(sec.Removed)
	d.Summary.Changed += len(sec.Changed)
	d.Summary.Warnings += len(sec.Warnings)
	return d
}

func added(entries ...DiffEntry) SectionDiff {
	s := newSectionDiff()
	s.Added = entries
	return s
}

func removed(entries ...DiffEntry) SectionDiff {
	s := newSectionDiff()
	s.Removed = entries
	return s
}

func changed(entries ...DiffFieldChange) SectionDiff {
	s := newSectionDiff()
	s.Changed = entries
	return s
}

func findingByID(t *testing.T, r PolicyReport, id string) PolicyFinding {
	t.Helper()
	for _, f := range r.Findings {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("finding %s not found in %+v", id, r.Findings)
	return PolicyFinding{}
}

func assertNoFindingID(t *testing.T, r PolicyReport, id string) {
	t.Helper()
	for _, f := range r.Findings {
		if f.ID == id {
			t.Fatalf("unexpected finding %s: %+v", id, f)
		}
	}
}

// ---------------------------------------------------------------------------
// Overall status
// ---------------------------------------------------------------------------

func TestPolicyEmptyDiffReady(t *testing.T) {
	r := EvaluatePolicy(emptyDiff())
	if r.Mode != "inventory-policy" {
		t.Errorf("mode = %q", r.Mode)
	}
	if r.OverallStatus != "ready" {
		t.Errorf("status = %q, want ready", r.OverallStatus)
	}
	if len(r.Findings) != 0 {
		t.Errorf("findings = %+v, want none", r.Findings)
	}
}

func TestPolicyBlockerWinsOverall(t *testing.T) {
	d := diffWith("mailboxes", removed(DiffEntry{Key: "info@main.example"}))
	d.Sections["php"] = changed(DiffFieldChange{Key: "main.example", Field: "version", Source: "ea-php74", Destination: "ea-php81"})
	r := EvaluatePolicy(d)
	if r.OverallStatus != "blocked" {
		t.Errorf("status = %q, want blocked", r.OverallStatus)
	}
	if r.Summary.Blockers == 0 || r.Summary.Reviews == 0 {
		t.Errorf("summary = %+v", r.Summary)
	}
}

func TestPolicyOnlyReviewRequired(t *testing.T) {
	d := diffWith("php", changed(DiffFieldChange{Key: "main.example", Field: "version", Source: "a", Destination: "b"}))
	r := EvaluatePolicy(d)
	if r.OverallStatus != "review_required" {
		t.Errorf("status = %q, want review_required", r.OverallStatus)
	}
}

func TestPolicyOnlyInfoIsReady(t *testing.T) {
	d := diffWith("mailboxes", added(DiffEntry{Key: "new@main.example"}))
	r := EvaluatePolicy(d)
	if r.OverallStatus != "ready" {
		t.Errorf("status = %q, want ready (info only)", r.OverallStatus)
	}
	if r.Summary.Info == 0 {
		t.Errorf("summary = %+v, want info counted", r.Summary)
	}
}

// ---------------------------------------------------------------------------
// Mail / database / domains
// ---------------------------------------------------------------------------

func TestPolicyMailboxRemovedBlocker(t *testing.T) {
	r := EvaluatePolicy(diffWith("mailboxes", removed(DiffEntry{Key: "info@main.example"})))
	f := findingByID(t, r, "POL-MAILBOX-REMOVED")
	if f.Severity != "blocker" || f.Status != "blocked" {
		t.Errorf("finding = %+v", f)
	}
}

func TestPolicyDatabaseRemovedBlocker(t *testing.T) {
	r := EvaluatePolicy(diffWith("databases", removed(DiffEntry{Key: "u_wp"})))
	if findingByID(t, r, "POL-DB-REMOVED").Severity != "blocker" {
		t.Error("database removed must be blocker")
	}
}

func TestPolicyAddedMailboxAndDBAreInfo(t *testing.T) {
	d := diffWith("mailboxes", added(DiffEntry{Key: "new@x"}))
	d.Sections["databases"] = added(DiffEntry{Key: "u_new"})
	r := EvaluatePolicy(d)
	if findingByID(t, r, "POL-MAILBOX-ADDED").Severity != "info" {
		t.Error("mailbox added must be info")
	}
	if findingByID(t, r, "POL-DB-ADDED").Severity != "info" {
		t.Error("database added must be info")
	}
	if r.OverallStatus != "ready" {
		t.Errorf("status = %q", r.OverallStatus)
	}
}

func TestPolicyMainDomainRemovedBlocker(t *testing.T) {
	r := EvaluatePolicy(diffWith("domains", removed(DiffEntry{Key: "main.example", Detail: "main"})))
	if findingByID(t, r, "POL-DOMAIN-MAIN-REMOVED").Severity != "blocker" {
		t.Error("main domain removed must be blocker")
	}
}

func TestPolicyAddonDomainRemovedReview(t *testing.T) {
	r := EvaluatePolicy(diffWith("domains", removed(DiffEntry{Key: "addon.example", Detail: "addon"})))
	if findingByID(t, r, "POL-DOMAIN-REMOVED").Severity != "review" {
		t.Error("non-main domain removed must be review")
	}
}

func TestPolicyDocrootChangeIsInfo(t *testing.T) {
	// Docroots legitimately differ across hosts: informational only.
	r := EvaluatePolicy(diffWith("domains", changed(DiffFieldChange{
		Key: "main.example", Field: "document_root", Source: "/home/a/x", Destination: "/home/b/x",
	})))
	if findingByID(t, r, "POL-DOMAIN-DOCROOT-CHANGED").Severity != "info" {
		t.Error("docroot change must be info")
	}
	if r.OverallStatus != "ready" {
		t.Errorf("status = %q", r.OverallStatus)
	}
}

// ---------------------------------------------------------------------------
// DNS
// ---------------------------------------------------------------------------

func TestPolicyDNSMXChangedBlocker(t *testing.T) {
	r := EvaluatePolicy(diffWith("dns", changed(DiffFieldChange{
		Key: "zone main.example MX main.example.", Field: "records",
		Source: "prio=10 a. ttl=1", Destination: "prio=10 b. ttl=1",
	})))
	if findingByID(t, r, "POL-DNS-MX-CHANGED").Severity != "blocker" {
		t.Error("MX changed must be blocker")
	}
}

func TestPolicyDNSMXRemovedBlocker(t *testing.T) {
	r := EvaluatePolicy(diffWith("dns", removed(DiffEntry{Key: "zone main.example MX main.example.", Detail: "prio=10 m. ttl=1"})))
	if findingByID(t, r, "POL-DNS-MX-REMOVED").Severity != "blocker" {
		t.Error("MX removed must be blocker")
	}
}

func TestPolicyDNSNSChangedBlocker(t *testing.T) {
	r := EvaluatePolicy(diffWith("dns", changed(DiffFieldChange{
		Key: "zone main.example NS main.example.", Field: "records", Source: "a", Destination: "b",
	})))
	if findingByID(t, r, "POL-DNS-NS-CHANGED").Severity != "blocker" {
		t.Error("NS changed must be blocker")
	}
}

func TestPolicyDNSTXTChangedReview(t *testing.T) {
	r := EvaluatePolicy(diffWith("dns", changed(DiffFieldChange{
		Key: "zone main.example TXT dkim._domainkey.main.example.", Field: "records",
		Source: "v=DKIM1... ttl=1", Destination: "v=DKIM1;other ttl=1",
	})))
	f := findingByID(t, r, "POL-DNS-RECORD-CHANGED")
	if f.Severity != "review" {
		t.Errorf("TXT changed = %q, want review", f.Severity)
	}
}

func TestPolicyDNSAChangedReview(t *testing.T) {
	r := EvaluatePolicy(diffWith("dns", changed(DiffFieldChange{
		Key: "zone main.example A main.example.", Field: "records", Source: "1.1.1.1 ttl=1", Destination: "2.2.2.2 ttl=1",
	})))
	if findingByID(t, r, "POL-DNS-RECORD-CHANGED").Severity != "review" {
		t.Error("A changed must be review")
	}
}

func TestPolicyDNSAddedByType(t *testing.T) {
	d := diffWith("dns", added(
		DiffEntry{Key: "zone main.example A extra.main.example.", Detail: "9.9.9.9 ttl=1"},
		DiffEntry{Key: "zone main.example MX main.example.", Detail: "prio=20 backup. ttl=1"},
	))
	r := EvaluatePolicy(d)
	if findingByID(t, r, "POL-DNS-RECORD-ADDED").Severity != "info" {
		t.Error("A added must be info")
	}
	if findingByID(t, r, "POL-DNS-MAIL-RECORD-ADDED").Severity != "review" {
		t.Error("MX added must be review")
	}
}

func TestPolicyDNSZoneRemovedBlocker(t *testing.T) {
	r := EvaluatePolicy(diffWith("dns", removed(DiffEntry{Key: "zone gone.example", Detail: "12 record(s)"})))
	if findingByID(t, r, "POL-DNS-ZONE-REMOVED").Severity != "blocker" {
		t.Error("whole zone removed must be blocker")
	}
}

// ---------------------------------------------------------------------------
// Cron
// ---------------------------------------------------------------------------

func TestPolicyCronEnabledRemovedBlocker(t *testing.T) {
	r := EvaluatePolicy(diffWith("cron", removed(DiffEntry{
		Key: "/bin/backup --password=[REDACTED]", Detail: "0 3 * * * enabled=true",
	})))
	if findingByID(t, r, "POL-CRON-ENABLED-REMOVED").Severity != "blocker" {
		t.Error("enabled cron removed must be blocker")
	}
}

func TestPolicyCronDisabledRemovedInfo(t *testing.T) {
	r := EvaluatePolicy(diffWith("cron", removed(DiffEntry{
		Key: "/bin/old.sh", Detail: "30 2 * * 0 enabled=false",
	})))
	f := findingByID(t, r, "POL-CRON-DISABLED-REMOVED")
	if f.Severity != "info" {
		t.Errorf("disabled cron removed = %q, want info", f.Severity)
	}
	assertNoFindingID(t, r, "POL-CRON-ENABLED-REMOVED")
}

func TestPolicyCronScheduleChangedReview(t *testing.T) {
	r := EvaluatePolicy(diffWith("cron", changed(DiffFieldChange{
		Key: "/bin/backup --password=[REDACTED]", Field: "schedule", Source: "0 3 * * *", Destination: "0 5 * * *",
	})))
	if findingByID(t, r, "POL-CRON-SCHEDULE-CHANGED").Severity != "review" {
		t.Error("cron schedule changed must be review")
	}
}

// ---------------------------------------------------------------------------
// SSL / PHP / sections
// ---------------------------------------------------------------------------

func TestPolicySSLRemovedBlocker(t *testing.T) {
	r := EvaluatePolicy(diffWith("ssl", removed(DiffEntry{Key: "main.example,www.main.example", Detail: "R3"})))
	if findingByID(t, r, "POL-SSL-REMOVED").Severity != "blocker" {
		t.Error("SSL removed for a present domain must be blocker")
	}
}

func TestPolicySSLRemovedWithDomainIsInfo(t *testing.T) {
	// Certificate gone together with its (removed) domains: coherent.
	d := diffWith("ssl", removed(DiffEntry{Key: "gone.example,www.gone.example", Detail: "R3"}))
	d.Sections["domains"] = removed(
		DiffEntry{Key: "gone.example", Detail: "addon"},
		DiffEntry{Key: "www.gone.example", Detail: "sub"},
	)
	r := EvaluatePolicy(d)
	f := findingByID(t, r, "POL-SSL-REMOVED-WITH-DOMAIN")
	if f.Severity != "info" {
		t.Errorf("ssl removed with its domains = %q, want info", f.Severity)
	}
	assertNoFindingID(t, r, "POL-SSL-REMOVED")
}

func TestPolicySSLChangedReview(t *testing.T) {
	r := EvaluatePolicy(diffWith("ssl", changed(DiffFieldChange{
		Key: "main.example", Field: "valid_until", Source: "1", Destination: "2",
	})))
	if findingByID(t, r, "POL-SSL-CHANGED").Severity != "review" {
		t.Error("SSL changed must be review")
	}
}

func TestPolicyPHPChangedReview(t *testing.T) {
	r := EvaluatePolicy(diffWith("php", changed(DiffFieldChange{
		Key: "main.example", Field: "version", Source: "ea-php74", Destination: "ea-php81",
	})))
	if findingByID(t, r, "POL-PHP-CHANGED").Severity != "review" {
		t.Error("PHP version changed must be review")
	}
}

func TestPolicyFTPRemovedReview(t *testing.T) {
	r := EvaluatePolicy(diffWith("ftp", removed(DiffEntry{Key: "up@main.example"})))
	if findingByID(t, r, "POL-FTP-REMOVED").Severity != "review" {
		t.Error("FTP removed must be review")
	}
}

func TestPolicyForwarderRemovedReviewAddedInfo(t *testing.T) {
	d := diffWith("forwarders", removed(DiffEntry{Key: "main.example | a@b -> c@d"}))
	d.Sections["autoresponders"] = added(DiffEntry{Key: "main.example | new@main.example"})
	r := EvaluatePolicy(d)
	if findingByID(t, r, "POL-FORWARDER-REMOVED").Severity != "review" {
		t.Error("forwarder removed must be review")
	}
	if findingByID(t, r, "POL-AUTORESPONDER-ADDED").Severity != "info" {
		t.Error("autoresponder added must be info")
	}
}

func TestPolicySectionUnavailableReview(t *testing.T) {
	// Gating happens on the STRUCTURED Skipped field, not on warning
	// prose: rewording a message can never turn incomplete data "ready".
	sec := newSectionDiff()
	sec.Skipped = append(sec.Skipped, "cron unavailable on destination")
	r := EvaluatePolicy(diffWith("cron", sec))
	f := findingByID(t, r, "POL-SECTION-UNAVAILABLE")
	if f.Severity != "review" {
		t.Errorf("unavailable section = %q, want review", f.Severity)
	}
	if r.OverallStatus != "review_required" {
		t.Errorf("status = %q — incomplete data can never be ready", r.OverallStatus)
	}
}

func TestPolicyGenericWarningDoesNotGate(t *testing.T) {
	sec := newSectionDiff()
	sec.Warnings = append(sec.Warnings, `duplicate key "x" in source — last occurrence wins`)
	r := EvaluatePolicy(diffWith("domains", sec))
	if findingByID(t, r, "POL-DIFF-WARNING").Severity != "warning" {
		t.Error("generic diff warning must be severity warning")
	}
	if r.OverallStatus != "ready" {
		t.Errorf("status = %q — plain warnings must not gate", r.OverallStatus)
	}
}

func TestPolicyDNSSOAChangedIsInfo(t *testing.T) {
	// SOA serial/timers differ on virtually every regenerated zone:
	// review-severity here would only train operators to skim findings.
	r := EvaluatePolicy(diffWith("dns", changed(DiffFieldChange{
		Key: "zone main.example SOA main.example.", Field: "records",
		Source: "ns1. admin. 2026010101 ttl=1", Destination: "ns2. admin. 2026070101 ttl=1",
	})))
	f := findingByID(t, r, "POL-DNS-SOA-CHANGED")
	if f.Severity != "info" {
		t.Errorf("SOA changed = %q, want info", f.Severity)
	}
	if r.OverallStatus != "ready" {
		t.Errorf("status = %q", r.OverallStatus)
	}
}

func TestPolicyDNSKeyEmptyNameNotZone(t *testing.T) {
	// A malformed record-level key with an empty owner name must NOT be
	// classified as a whole missing zone (blocker with a wrong headline).
	r := EvaluatePolicy(diffWith("dns", removed(DiffEntry{Key: "zone main.example TXT", Detail: "x ttl=1"})))
	assertNoFindingID(t, r, "POL-DNS-ZONE-REMOVED")
	if findingByID(t, r, "POL-DNS-RECORD-REMOVED").Severity != "review" {
		t.Error("record with empty name must classify as record-level")
	}
}

func TestPolicySortTiebreakerOnRefs(t *testing.T) {
	// Two removed mailboxes: same severity/section/id and empty Detail —
	// the refs must order them deterministically.
	d := diffWith("mailboxes", removed(DiffEntry{Key: "b@x"}, DiffEntry{Key: "a@x"}))
	r := EvaluatePolicy(d)
	if len(r.Findings) != 2 {
		t.Fatalf("findings = %d", len(r.Findings))
	}
	if r.Findings[0].SourceRef != "a@x" || r.Findings[1].SourceRef != "b@x" {
		t.Errorf("findings not ordered by ref: %+v", r.Findings)
	}
}

// ---------------------------------------------------------------------------
// Determinism and hygiene
// ---------------------------------------------------------------------------

func TestPolicyDeterministicOrder(t *testing.T) {
	d := diffWith("mailboxes", removed(DiffEntry{Key: "b@x"}, DiffEntry{Key: "a@x"}))
	d.Sections["php"] = changed(DiffFieldChange{Key: "x", Field: "version", Source: "1", Destination: "2"})
	r1 := EvaluatePolicy(d)
	r2 := EvaluatePolicy(d)
	if len(r1.Findings) != len(r2.Findings) {
		t.Fatal("finding count differs between runs")
	}
	for i := range r1.Findings {
		if r1.Findings[i] != r2.Findings[i] {
			t.Errorf("non-deterministic finding order at %d", i)
		}
	}
	// Blockers first.
	if r1.Findings[0].Severity != "blocker" {
		t.Errorf("first finding = %+v, want blocker first", r1.Findings[0])
	}
}

func TestPolicyNoNilSlices(t *testing.T) {
	r := EvaluatePolicy(emptyDiff())
	if r.Findings == nil || r.Warnings == nil {
		t.Errorf("nil slices in report: %+v", r)
	}
}

func TestPolicyRedactedContentPreserved(t *testing.T) {
	// The policy must carry the redacted command through, never expand it.
	r := EvaluatePolicy(diffWith("cron", removed(DiffEntry{
		Key: "/bin/backup --password=[REDACTED] \\| gzip", Detail: "0 3 * * * enabled=true",
	})))
	f := findingByID(t, r, "POL-CRON-ENABLED-REMOVED")
	if !strings.Contains(f.SourceRef, "[REDACTED]") && !strings.Contains(f.Detail, "[REDACTED]") {
		t.Errorf("redacted command lost: %+v", f)
	}
}
