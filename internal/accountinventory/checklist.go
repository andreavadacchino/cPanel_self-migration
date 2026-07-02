package accountinventory

// BuildChecklist composes the migration checklist from already-produced
// artifacts (inventories, diff, policy report, optional DNS plan, optional
// migration report). Pure and deterministic: same input, same output —
// GeneratedAt and the input file references are the caller's concern.
//
// Honesty rules, in order of importance:
//   - migrated_by_tool is NEVER true without evidence. A missing migration
//     report means "unknown", even when both sides look identical.
//   - a DNS plan proves a difference is expected ONLY when the destination
//     already matches the desired translation (action=skip). Pending plan
//     work (add/replace) is still work, not an expected difference.
//   - areas the inventory cannot see are reported as their own sections
//     (not_inventoried / not_accessible_without_root), never silently ok.

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
)

// checklistSectionOrder fixes the section order in the output: the 10
// inventoried sections (diff order, with the synthetic web_files after
// domains), then the not-inventoried areas, then the root-only ones.
var checklistSectionOrder = []string{
	"domains", "web_files", "mailboxes", "databases", "forwarders",
	"autoresponders", "ftp", "ssl", "php", "dns", "cron",
	"email_routing", "default_address", "email_filters", "redirects",
	"quota_package", "server_level_config",
}

type checklistBuilder struct {
	in       ChecklistInput
	warnings []string

	// evidence/migrated per section, from the migration report.
	evidence map[string]string
	migrated map[string]bool

	// findings grouped by section (order preserved from the policy report,
	// which is already deterministically sorted).
	findings map[string][]PolicyFinding

	// planOps indexes the DNS plan ops by zone/type/canonical-name.
	planOps map[string]PlanOp

	actions        []ManualAction
	sectionActions map[string][]int // section -> indexes into actions

	// Operator acceptances (PR 7D), indexed by stable action key.
	// acceptMatched tracks which keys actually matched an action, so the
	// unmatched ones can warn (stale acceptances self-invalidate loudly).
	acceptByKey   map[string]OperatorAcceptance
	acceptMatched map[string]bool

	// chainMismatch is set when the provenance verification finds a
	// PROVEN hash mismatch (not a mere absence): the composition is
	// inconsistent and a READY_* verdict must be capped.
	chainMismatch bool
}

// BuildChecklist is the engine entry point.
func BuildChecklist(in ChecklistInput) MigrationChecklist {
	b := &checklistBuilder{
		in:             in,
		warnings:       []string{},
		evidence:       map[string]string{},
		migrated:       map[string]bool{},
		findings:       map[string][]PolicyFinding{},
		planOps:        map[string]PlanOp{},
		sectionActions: map[string][]int{},
		acceptByKey:    map[string]OperatorAcceptance{},
		acceptMatched:  map[string]bool{},
	}
	for _, acc := range in.Acceptances {
		if _, dup := b.acceptByKey[acc.ActionKey]; dup {
			b.warnings = append(b.warnings, fmt.Sprintf(
				"acceptances: duplicate entry for action key %s — first entry wins", acc.ActionKey))
			continue
		}
		b.acceptByKey[acc.ActionKey] = acc
	}
	for _, f := range in.Policy.Findings {
		b.findings[f.Section] = append(b.findings[f.Section], f)
	}
	if in.DNSPlan != nil {
		for _, z := range in.DNSPlan.Zones {
			for _, op := range z.Ops {
				b.planOps[planOpKey(z.Zone, op.Type, op.Name)] = op
			}
		}
	}
	b.computeEvidence()

	chainVerified, chainWarnings, chainMismatch := verifyProvenanceChain(in)
	b.warnings = append(b.warnings, chainWarnings...)
	b.chainMismatch = chainMismatch

	c := MigrationChecklist{
		Mode:             "migration-checklist",
		FormatVersion:    1,
		Account:          in.Source.Account.User,
		Inputs:           in.InputRefs,
		ChainVerified:    chainVerified,
		Sections:         []ChecklistSection{},
		ManualActions:    []ManualAction{},
		Warnings:         []string{},
		CoverageManifest: CoverageAreas(),
	}
	for _, name := range checklistSectionOrder {
		c.Sections = append(c.Sections, b.buildSection(name))
	}

	// Assign stable IDs in generation order (section order is fixed and
	// per-section generation follows already-sorted findings/diff entries),
	// then cross-reference them into the owning sections.
	for i := range b.actions {
		b.actions[i].ID = fmt.Sprintf("MA-%03d", i+1)
	}
	c.ManualActions = append(c.ManualActions, b.actions...)
	for i := range c.Sections {
		refs := []string{}
		accepted := []string{}
		for _, idx := range b.sectionActions[c.Sections[i].Section] {
			refs = append(refs, b.actions[idx].ID)
			if b.actions[idx].Accepted {
				accepted = append(accepted, b.actions[idx].ID)
			}
		}
		c.Sections[i].ManualActionRefs = refs
		c.Sections[i].AcceptedByOperator = accepted
	}
	// Every acceptance must have found its action: an unmatched key means
	// the underlying fact changed (or never existed) since the operator
	// reviewed it — say so instead of silently dropping the acceptance.
	unmatched := []string{}
	for key := range b.acceptByKey {
		if !b.acceptMatched[key] {
			unmatched = append(unmatched, key)
		}
	}
	sort.Strings(unmatched)
	for _, key := range unmatched {
		b.warnings = append(b.warnings, fmt.Sprintf(
			"acceptances: no current action matches key %s — the underlying finding changed since it was reviewed; re-review", key))
	}

	c.Warnings = append(c.Warnings, b.warnings...)
	b.summarize(&c)
	return c
}

// ---------------------------------------------------------------------------
// Migration evidence
// ---------------------------------------------------------------------------

// computeEvidence maps the optional migration report to per-section
// evidence. Only a SUCCESSFUL apply run counts; everything else is "none"
// plus a warning explaining why the report was ignored.
func (b *checklistBuilder) computeEvidence() {
	for _, name := range checklistSectionOrder {
		b.evidence[name] = EvidenceNone
	}
	rep := b.in.MigrationReport
	if rep == nil {
		return
	}
	if rep.Mode != "apply" {
		b.warnings = append(b.warnings, fmt.Sprintf(
			"migration report %q is not an apply run (mode %q) — ignored as migration evidence", rep.RunID, rep.Mode))
		return
	}
	if rep.ExitStatus != "success" {
		b.warnings = append(b.warnings, fmt.Sprintf(
			"migration report %q did not succeed (exit_status %q) — ignored as migration evidence", rep.RunID, rep.ExitStatus))
		return
	}
	// PR 7C upgrade: when the (successful) report's phases_completed proves
	// BOTH the migrate and the verify phase of a section's flow completed,
	// evidence rises to per_item — the verify phases are per-item integrity
	// passes whose failures make the run non-success, so "success + both
	// phases" proves each item was individually processed and verified.
	// Domains have no verify phase: creation is itself per-item and its
	// failures gate the exit status the same way.
	completed := map[string]bool{}
	for _, p := range rep.PhasesCompleted {
		completed[p] = true
	}
	perItem := map[string]bool{
		"mailboxes": completed["migrate_mail"] && completed["verify_mail"],
		"web_files": completed["copy_files"] && completed["verify_files"],
		"databases": completed["migrate_db"] && completed["verify_db"],
		"domains":   completed["create_domains"],
	}
	mark := func(section string) {
		b.evidence[section] = EvidenceRunLevel
		if perItem[section] {
			b.evidence[section] = EvidencePerItem
		}
		b.migrated[section] = true
	}
	if rep.Scope.Mail {
		mark("mailboxes")
	}
	if rep.Scope.Files {
		mark("web_files")
	}
	if rep.Scope.Databases {
		mark("databases")
	}
	// Domain creation runs as part of every apply flow.
	if rep.Scope.Mail || rep.Scope.Files || rep.Scope.Databases {
		mark("domains")
	}
}

// ---------------------------------------------------------------------------
// Section builders
// ---------------------------------------------------------------------------

func (b *checklistBuilder) buildSection(name string) ChecklistSection {
	switch name {
	case "web_files":
		return b.buildWebFilesSection()
	case "quota_package", "server_level_config":
		return b.buildRootOnlySection(name)
	default:
		return b.buildInventoriedSection(name)
	}
}

func newChecklistSection(name string) ChecklistSection {
	return ChecklistSection{
		Section:             name,
		MigrationEvidence:   EvidenceNone,
		ExpectedDifferences: []ExpectedDifference{},
		ManualActionRefs:    []string{},
		Blockers:            []string{},
		PolicyFindingRefs:   []string{},
		AcceptedByOperator:  []string{},
		PostCutoverChecks:   []string{},
		Evidence:            []ChecklistEvidence{},
	}
}

func (b *checklistBuilder) buildInventoriedSection(name string) ChecklistSection {
	sec := newChecklistSection(name)
	sec.SourceCount = inventorySectionCount(b.in.Source, name)
	sec.DestinationCount = inventorySectionCount(b.in.Destination, name)
	sec.SourcePresent = sec.SourceCount > 0
	sec.DestinationPresent = sec.DestinationCount > 0
	sec.MigrationEvidence = b.evidence[name]
	sec.MigratedByTool = b.migrated[name]

	findings := b.findings[name]
	for _, f := range findings {
		sec.PolicyFindingRefs = appendUnique(sec.PolicyFindingRefs, f.ID)
	}
	sec.Evidence = diffEvidence(b.in.Diff, name)

	// Section-specific expected-difference recognition and action
	// generation. downgraded marks findings whose severity no longer
	// gates the section.
	downgraded := make([]bool, len(findings))
	switch name {
	case "domains":
		b.evalDomainsSection(&sec, findings)
	case "mailboxes", "databases", "forwarders", "autoresponders", "ftp":
		b.evalRecreateSection(&sec, name, findings)
	case "ssl":
		b.evalSSLSection(&sec, findings, downgraded)
	case "php":
		b.evalPHPSection(&sec, findings)
	case "dns":
		b.evalDNSSection(&sec, findings, downgraded)
	case "cron":
		b.evalCronSection(&sec, findings)
	case "email_routing":
		b.evalEmailRoutingSection(&sec, findings)
	case "default_address":
		b.evalDefaultAddressSection(&sec, findings)
	case "email_filters":
		b.evalEmailFiltersSection(&sec, findings)
	case "redirects":
		b.evalRedirectsSection(&sec, findings)
	}

	blockers, reviews := 0, 0
	for i, f := range findings {
		if downgraded[i] {
			continue
		}
		switch f.Severity {
		case SeverityBlocker:
			blockers++
			ref := f.SourceRef
			if ref == "" {
				ref = f.DestinationRef
			}
			label := f.ID
			if ref != "" {
				label = fmt.Sprintf("%s (%s)", f.ID, ref)
			}
			sec.Blockers = append(sec.Blockers, label)
		case SeverityReview:
			reviews++
		}
	}

	sec.Status = b.resolveStatus(name, &sec, blockers, reviews, len(findings))
	b.addPostCutoverChecks(&sec)
	return sec
}

// resolveStatus applies the fixed precedence: blocked > has-real-actions
// (manual_required, or not_migrated_by_tool for a non-migratable area whose
// destination is empty) > review_required > expected_difference >
// not_applicable > ok. ACCEPT_EXPECTED_DIFFERENCE acknowledgments never
// change a section's status, and neither do operator-ACCEPTED actions
// (PR 7D): a formally accepted action stops counting as real work.
func (b *checklistBuilder) resolveStatus(name string, sec *ChecklistSection, blockers, reviews, findingsCount int) string {
	realActions := 0
	for _, idx := range b.sectionActions[name] {
		if b.actions[idx].Type != MActionAcceptExpectedDiff && !b.actions[idx].Accepted {
			realActions++
		}
	}
	switch {
	case blockers > 0:
		return SectionBlocked
	case realActions > 0:
		if !checklistMigratable[name] && sec.SourceCount > 0 && sec.DestinationCount == 0 {
			return SectionNotMigratedByTool
		}
		return SectionManualRequired
	case reviews > 0:
		return SectionReviewRequired
	case len(sec.ExpectedDifferences) > 0:
		return SectionExpectedDifference
	case sec.SourceCount == 0 && sec.DestinationCount == 0 && findingsCount == 0:
		return SectionNotApplicable
	default:
		return SectionOK
	}
}

// checklistMigratable marks the sections the legacy apply flow can migrate.
var checklistMigratable = map[string]bool{
	"domains": true, "mailboxes": true, "databases": true, "web_files": true,
}

// evalDomainsSection needs no downgraded slice: the only expected
// difference here (docroot) is already info-severity at the policy layer.
func (b *checklistBuilder) evalDomainsSection(sec *ChecklistSection, findings []PolicyFinding) {
	for _, f := range findings {
		switch f.ID {
		case "POL-DOMAIN-DOCROOT-CHANGED":
			sec.ExpectedDifferences = append(sec.ExpectedDifferences, ExpectedDifference{
				Key: f.SourceRef, Reason: "document root layouts legitimately differ across hosts",
			})
		case "POL-DOMAIN-MAIN-REMOVED":
			b.addAction(sec.Section, MActionCreateOnDestination, true, f,
				"Create the main domain on the destination",
				"Create the domain (or transfer the account) so the destination serves it before cutover.")
		case "POL-DOMAIN-REMOVED":
			b.addAction(sec.Section, MActionCreateOnDestination, false, f,
				"Create the missing domain on the destination",
				"Create the addon/sub/parked domain on the destination or confirm it is being dropped.")
		}
	}
}

// evalRecreateSection covers the uniform "recreate it by hand" sections.
func (b *checklistBuilder) evalRecreateSection(sec *ChecklistSection, name string, findings []PolicyFinding) {
	noun := map[string]string{
		"mailboxes": "mailbox", "databases": "database", "forwarders": "forwarder",
		"autoresponders": "autoresponder", "ftp": "FTP account",
	}[name]
	// Mail flow breaks silently when a mailbox or forwarder is missing;
	// a lost database blocks the application outright.
	blocking := map[string]bool{"mailboxes": true, "databases": true, "forwarders": true}[name]
	for _, f := range findings {
		if !strings.HasSuffix(f.ID, "-REMOVED") {
			continue
		}
		b.addAction(sec.Section, MActionCreateOnDestination, blocking, f,
			fmt.Sprintf("Recreate %s %s on the destination", noun, f.SourceRef),
			fmt.Sprintf("Recreate the %s on the destination or confirm it is obsolete.", noun))
	}
}

// --- PR 7E section evaluators ----------------------------------------------

// evalEmailRoutingSection: a routing difference (or a domain whose
// routing entry disappeared) silently breaks mail delivery — blocking
// per-domain confirmation, replacing the old blanket not_inventoried
// check.
func (b *checklistBuilder) evalEmailRoutingSection(sec *ChecklistSection, findings []PolicyFinding) {
	// The routing diff compares the routing MODE only; the exchangers
	// live in the dns section. When the dns comparison relevant to a
	// routing domain was skipped, the operator must know the MX rrsets
	// behind this routing were never verified — a generic "dns
	// incomplete" note is not enough. Scoped to the routing domains'
	// own zones: an unrelated zone hiccup must not cry wolf.
	if dnsSec, ok := b.in.Diff.Sections["dns"]; ok && sec.SourceCount > 0 &&
		dnsSkipTouchesRouting(dnsSec.Skipped, b.in.Source.EmailRouting.Items) {
		b.warnings = append(b.warnings,
			"dns comparison was skipped — the MX exchangers behind email routing were not verified; compare them manually before cutover")
	}
	for _, f := range findings {
		switch f.ID {
		case "POL-MAILROUTE-REMOVED":
			b.addAction(sec.Section, MActionConfirmEmailRouting, true, f,
				"Confirm mail routing for "+f.SourceRef,
				"The domain has no routing entry on the destination; set cPanel Email Routing (local/remote) before cutover.")
		case "POL-MAILROUTE-CHANGED":
			b.addAction(sec.Section, MActionConfirmEmailRouting, true, f,
				"Confirm mail routing for "+f.SourceRef,
				"Email Routing differs between source and destination; a wrong local/remote value silently breaks delivery.")
		}
	}
}

// dnsSkipTouchesRouting reports whether a dns Skipped entry affects a
// mail-routing domain. A whole-section skip carries no "zone " prefix
// and leaves every exchanger unverified; a per-zone skip ("zone <name>
// unavailable on one side — records not compared") matters only when
// that zone hosts one of the routing domains.
func dnsSkipTouchesRouting(skipped []string, items []EmailRoutingEntry) bool {
	for _, s := range skipped {
		rest, perZone := strings.CutPrefix(s, "zone ")
		if !perZone {
			return true
		}
		zone := rest
		if i := strings.IndexByte(zone, ' '); i >= 0 {
			zone = zone[:i]
		}
		for _, it := range items {
			if it.Domain == zone || strings.HasSuffix(it.Domain, "."+zone) {
				return true
			}
		}
	}
	return false
}

func (b *checklistBuilder) evalDefaultAddressSection(sec *ChecklistSection, findings []PolicyFinding) {
	for _, f := range findings {
		switch f.ID {
		case "POL-DEFAULTADDR-REMOVED", "POL-DEFAULTADDR-CHANGED":
			ref := f.SourceRef
			if ref == "" {
				ref = f.DestinationRef
			}
			b.addAction(sec.Section, MActionManualCheckRequired, true, f,
				"Check the default (catch-all) address for "+ref,
				"The default address differs or is missing on the destination; a lost catch-all silently drops mail.")
		}
	}
}

func (b *checklistBuilder) evalEmailFiltersSection(sec *ChecklistSection, findings []PolicyFinding) {
	for _, f := range findings {
		if f.ID != "POL-EMAILFILTER-REMOVED" {
			continue
		}
		b.addAction(sec.Section, MActionRecreateEmailFilters, true, f,
			"Recreate email filter "+f.SourceRef,
			"The filter exists only on the source; recreate it on the destination or confirm it is obsolete — filters change mail handling silently.")
	}
}

// evalRedirectsSection: CMS rewrites travel with the web files, so
// their absence on a not-yet-synced destination is an expected
// difference; only genuine redirects get an operator action
// (non-blocking — the .htaccess rule still migrates with webfiles).
func (b *checklistBuilder) evalRedirectsSection(sec *ChecklistSection, findings []PolicyFinding) {
	for _, f := range findings {
		switch f.ID {
		case "POL-REDIRECT-CMS-REMOVED":
			sec.ExpectedDifferences = append(sec.ExpectedDifferences, ExpectedDifference{
				Key: f.SourceRef, Reason: "CMS-generated .htaccess rewrite — travels with the web files migration",
			})
		case "POL-REDIRECT-REMOVED", "POL-REDIRECT-CHANGED":
			ref := f.SourceRef
			if ref == "" {
				ref = f.DestinationRef
			}
			b.addAction(sec.Section, MActionConfirmRedirect, false, f,
				"Confirm redirect "+ref,
				"A genuine redirect differs or is missing on the destination; verify it after the web files migration (its .htaccess rule travels with the files).")
		}
	}
}

func (b *checklistBuilder) evalSSLSection(sec *ChecklistSection, findings []PolicyFinding, downgraded []bool) {
	now := b.in.Now.Unix()
	validDest := func(e SSLEntry) bool {
		return e.ValidUntil > now && (e.ValidFrom == 0 || e.ValidFrom <= now)
	}
	domainCovered := func(dom string) bool {
		for _, e := range b.in.Destination.SSL.Items {
			if !validDest(e) {
				continue
			}
			for _, d := range strings.Split(e.Domains, ",") {
				if certDomainCovers(strings.TrimSpace(d), dom) {
					return true
				}
			}
		}
		return false
	}
	// sourceGroupExpired reports whether the source inventory holds at least
	// one certificate under this diff key and ALL of them are provably
	// expired at Now. Unknown expiry (ValidUntil <= 0) is never proof of
	// expiry, and one still-valid generation keeps the whole group live —
	// both fail-safe: when in doubt the removal keeps gating.
	sourceGroupExpired := func(key string) bool {
		found := false
		for _, e := range b.in.Source.SSL.Items {
			if e.Domains != key {
				continue
			}
			if e.ValidUntil <= 0 || e.ValidUntil > now {
				return false
			}
			found = true
		}
		return found
	}
	certByKey := func(key string) (SSLEntry, bool) {
		for _, e := range b.in.Destination.SSL.Items {
			if e.Domains == key {
				return e, true
			}
		}
		return SSLEntry{}, false
	}

	expectedKeys := map[string]bool{}
	reissueKeys := map[string]bool{}
	for i, f := range findings {
		key := f.SourceRef
		switch f.ID {
		case "POL-SSL-CHANGED":
			if e, ok := certByKey(key); ok && validDest(e) {
				downgraded[i] = true
				if !expectedKeys[key] {
					expectedKeys[key] = true
					sec.ExpectedDifferences = append(sec.ExpectedDifferences, ExpectedDifference{
						Key: key, Reason: "destination presents a different but currently valid certificate for the same domains",
					})
					b.addAction(sec.Section, MActionAcceptExpectedDiff, false, f,
						"Acknowledge the reissued certificate for "+key,
						"The destination certificate differs from the source but is currently valid; acknowledge or investigate.")
				}
				continue
			}
			if !reissueKeys[key] {
				reissueKeys[key] = true
				b.addAction(sec.Section, MActionReissueSSL, false, f,
					"Verify or reissue the certificate for "+key,
					"The destination certificate differs and its validity could not be confirmed; verify it, reissue via AutoSSL if needed.")
			}
		case "POL-SSL-REMOVED":
			allCovered := key != "" && key != "(no domain list)"
			if allCovered {
				for _, d := range strings.Split(key, ",") {
					if !domainCovered(strings.TrimSpace(d)) {
						allCovered = false
						break
					}
				}
			}
			if allCovered {
				downgraded[i] = true
				if !expectedKeys[key] {
					expectedKeys[key] = true
					sec.ExpectedDifferences = append(sec.ExpectedDifferences, ExpectedDifference{
						Key: key, Reason: "certificate regrouped on the destination — every domain is still covered by a valid certificate",
					})
					b.addAction(sec.Section, MActionAcceptExpectedDiff, false, f,
						"Acknowledge the regrouped certificate for "+key,
						"The source certificate no longer exists as-is, but a valid destination certificate covers all of its domains.")
				}
				continue
			}
			// A group that was ALREADY expired on the source carries nothing
			// valid to migrate: its absence on the destination is expected,
			// not a cutover gate (real-smoke finding 2 — old wildcard
			// generations kept blocking forever).
			if sourceGroupExpired(key) {
				downgraded[i] = true
				if !expectedKeys[key] {
					expectedKeys[key] = true
					sec.ExpectedDifferences = append(sec.ExpectedDifferences, ExpectedDifference{
						Key: key, Reason: "every source certificate for these domains was already expired — nothing valid to migrate",
					})
					b.addAction(sec.Section, MActionAcceptExpectedDiff, false, f,
						"Acknowledge the expired source certificate for "+key,
						"All source certificates for these domains were already expired before the migration; issue a destination certificate only if the domains must serve HTTPS.")
				}
				continue
			}
			if !reissueKeys[key] {
				reissueKeys[key] = true
				b.addAction(sec.Section, MActionReissueSSL, true, f,
					"Issue a certificate for "+displayOr(key, "(no domain list)"),
					"Issue or install a certificate on the destination (AutoSSL or manual) before cutover.")
			}
		}
	}
}

// certDomainCovers reports whether one certificate domain entry covers dom:
// exact match, or RFC 6125-style wildcard matching — "*.base" covers exactly
// one extra non-empty label ("shop.base" yes; "base" itself and "a.b.base"
// no). A wildcard query is only ever covered by the identical literal
// wildcard entry, never synthesized from per-host coverage. Matching is
// case-sensitive like the rest of the pipeline: cPanel normalizes domains
// to lowercase on both sides.
func certDomainCovers(certDom, dom string) bool {
	if certDom == dom {
		return true
	}
	base, isWild := strings.CutPrefix(certDom, "*.")
	if !isWild || base == "" || strings.Contains(dom, "*") {
		return false
	}
	label, matched := strings.CutSuffix(dom, "."+base)
	return matched && label != "" && !strings.Contains(label, ".")
}

func (b *checklistBuilder) evalPHPSection(sec *ChecklistSection, findings []PolicyFinding) {
	for _, f := range findings {
		if f.ID != "POL-PHP-CHANGED" && f.ID != "POL-PHP-REMOVED" {
			continue
		}
		b.addAction(sec.Section, MActionCheckPHPCompat, false, f,
			"Check PHP compatibility for "+f.SourceRef,
			"Test the site against the destination PHP configuration before cutover.")
	}
}

func (b *checklistBuilder) evalDNSSection(sec *ChecklistSection, findings []PolicyFinding, downgraded []bool) {
	hasPlan := b.in.DNSPlan != nil
	sawDNSChange := false

	for i, f := range findings {
		ref := f.SourceRef
		if ref == "" {
			ref = f.DestinationRef
		}
		switch {
		case f.ID == "POL-DNS-SOA-CHANGED":
			sec.ExpectedDifferences = append(sec.ExpectedDifferences, ExpectedDifference{
				Key: ref, Reason: "SOA serial/timers change whenever a zone is regenerated on a new host",
			})
		case f.ID == "POL-DNS-RECORD-CHANGED" || f.ID == "POL-DNS-RECORD-REMOVED":
			sawDNSChange = true
			op, planned := b.planOpForFindingRef(ref)
			switch {
			case planned && op.Action == ActionSkip:
				// The destination ALREADY matches the plan's desired
				// translation: the difference is the intended one.
				downgraded[i] = true
				sec.ExpectedDifferences = append(sec.ExpectedDifferences, ExpectedDifference{
					Key: ref, Reason: "destination already matches the DNS plan translation (plan action: skip)",
				})
			case planned && op.Action == ActionReplace && f.ID == "POL-DNS-RECORD-CHANGED" &&
				strings.Contains(op.Name, "._domainkey"):
				// 7A smoke finding 3: a regenerated DKIM key produces a
				// pending plan replace and a policy review, but the
				// old-key-vs-regenerated-key choice is a human decision —
				// surface it instead of staying silent. Non-blocking: the
				// replace itself is already tracked as plan work.
				b.addAction(sec.Section, MActionConfirmDNSRecord, false, f,
					"Confirm the regenerated DKIM key "+ref,
					"The destination regenerated this DKIM TXT (plan: replace). Decide which key is authoritative: keep the destination's regenerated key (and update any external DNS copies) or restore the source key via the plan.")
			case !hasPlan && f.ID == "POL-DNS-RECORD-CHANGED" && dnsKeyType(f.SourceRef) == "TXT":
				b.addAction(sec.Section, MActionVerifyExternalSvc, false, f,
					"Verify the changed TXT record "+f.SourceRef,
					"TXT records often bind external services (SPF/DKIM/verification); confirm the destination value is intended.")
			}
		case f.ID == "POL-DNS-MX-REMOVED" || f.ID == "POL-DNS-MX-CHANGED":
			b.addAction(sec.Section, MActionConfirmMXExternal, true, f,
				"Confirm mail routing (MX) for "+ref,
				"MX records differ between source and destination; confirm external mail (e.g. Microsoft 365 / Google Workspace) keeps working before cutover.")
		case f.ID == "POL-DNS-NS-REMOVED" || f.ID == "POL-DNS-NS-CHANGED":
			b.addAction(sec.Section, MActionConfirmDNSRecord, true, f,
				"Confirm delegation (NS) for "+ref,
				"NS records differ; confirm the intended delegation at the registrar/WHM level.")
		case f.ID == "POL-DNS-ZONE-REMOVED":
			b.addAction(sec.Section, MActionCreateOnDestination, true, f,
				"Create the missing DNS zone "+ref,
				"The destination does not serve this zone; create it via WHM/park, then re-run the inventory.")
		}
	}

	if b.in.DNSPlan != nil {
		for _, z := range b.in.DNSPlan.Zones {
			for _, op := range z.Ops {
				if op.Action != ActionManual {
					continue
				}
				b.addPlanManualAction(sec.Section, z.Zone, op)
			}
		}
		for _, mz := range b.in.DNSPlan.ManualZones {
			sec.Evidence = append(sec.Evidence, ChecklistEvidence{
				Kind: "plan_manual_zone", Key: mz.Zone, Detail: mz.Reason,
			})
		}
	} else if sawDNSChange {
		b.warnings = append(b.warnings,
			"no DNS plan provided — only SOA differences can be recognized as expected; run `inventory dns-plan` and pass it via --dns-plan")
	}
}

// addPlanManualAction maps one manual plan op to the operator action it
// requires. Blocking is decided by the record's nature, not the reason
// text, except for the SPF case which the plan states explicitly.
func (b *checklistBuilder) addPlanManualAction(section, zone string, op PlanOp) {
	ev := []ChecklistEvidence{{
		Kind: "plan_manual_op", Key: fmt.Sprintf("zone %s %s %s", zone, op.Type, op.Name), Detail: op.Reason,
	}}
	derived := []string{fmt.Sprintf("dns-plan:%s:%s:%s", zone, op.Type, op.Name)}
	switch {
	case op.Type == "TXT" && strings.Contains(op.Reason, "SPF"):
		b.addActionRaw(section, MActionUpdateSPF, true, derived,
			"Rewrite the SPF TXT record "+op.Name,
			op.Reason, "Rewrite the SPF value by hand replacing the old server address, then create it on the destination.", ev)
	case op.Type == "MX":
		b.addActionRaw(section, MActionConfirmMXExternal, true, derived,
			"Resolve the MX record "+op.Name+" by hand",
			op.Reason, "The plan refuses to touch this MX rrset; confirm mail routing manually.", ev)
	case op.Type == "NS":
		b.addActionRaw(section, MActionConfirmDNSRecord, false, derived,
			"Review delegation (NS) for "+op.Name,
			op.Reason, "NS/delegation is registrar/WHM territory; review it manually.", ev)
	case op.Type == "A" || op.Type == "AAAA" || op.Type == "CNAME":
		b.addActionRaw(section, MActionConfirmDNSRecord, true, derived,
			fmt.Sprintf("Resolve the %s record %s by hand", op.Type, op.Name),
			op.Reason, "The plan cannot translate this record; without it the destination will not serve it — resolve before cutover.", ev)
	default:
		b.addActionRaw(section, MActionConfirmDNSRecord, false, derived,
			fmt.Sprintf("Review the %s record %s by hand", op.Type, op.Name),
			op.Reason, "The plan does not support this record type; recreate it manually if still needed.", ev)
	}
}

func (b *checklistBuilder) evalCronSection(sec *ChecklistSection, findings []PolicyFinding) {
	for _, f := range findings {
		switch f.ID {
		case "POL-CRON-ENABLED-REMOVED":
			typ := MActionRecreateCron
			operator := "Recreate this cron job on the destination before cutover."
			if strings.Contains(f.SourceRef, "/home/") {
				typ = MActionAdaptCronPath
				operator = "Recreate this cron job on the destination adapting the /home/<user> paths to the new account."
			}
			b.addAction(sec.Section, typ, true, f,
				"Recreate active cron job", operator)
		case "POL-CRON-DISABLED-REMOVED":
			b.addAction(sec.Section, MActionRecreateCron, false, f,
				"Recreate disabled cron job (only if still needed)",
				"The job was disabled on the source; recreate it only if you plan to re-enable it.")
		}
	}
}

func (b *checklistBuilder) buildWebFilesSection() ChecklistSection {
	sec := newChecklistSection("web_files")
	sec.SourceCount = len(b.in.Source.Domains)
	sec.DestinationCount = len(b.in.Destination.Domains)
	sec.SourcePresent = sec.SourceCount > 0
	sec.DestinationPresent = sec.DestinationCount > 0
	sec.MigrationEvidence = b.evidence["web_files"]
	sec.MigratedByTool = b.migrated["web_files"]
	// The inventory carries NO file listing: this section is knowable only
	// through migration evidence.
	sec.Evidence = append(sec.Evidence, ChecklistEvidence{
		Kind: "note", Detail: "the inventory has no file listing — web files are assessed from migration evidence only",
	})
	switch {
	case sec.SourceCount == 0:
		sec.Status = SectionNotApplicable
	case sec.MigratedByTool:
		sec.Status = SectionOK
	default:
		sec.Status = SectionNotMigratedByTool
	}
	b.addPostCutoverChecks(&sec)
	return sec
}

// buildRootOnlySection reports what account-level access cannot see at
// all. These sections are informational and never gate the rollup: the
// operator cannot fix them from cPanel.
func (b *checklistBuilder) buildRootOnlySection(name string) ChecklistSection {
	sec := newChecklistSection(name)
	sec.Status = SectionNotAccessibleWithoutRoot
	detail := map[string]string{
		"quota_package":       "package assignment and account quotas/limits are WHM territory — compare them from WHM if you have access",
		"server_level_config": "server-level configuration (PHP handlers, Apache/LiteSpeed, firewall, system crons) is not visible with account-level access",
	}[name]
	sec.Evidence = append(sec.Evidence, ChecklistEvidence{Kind: "note", Detail: detail})
	return sec
}

// ---------------------------------------------------------------------------
// Post-cutover checks (fixed, deterministic strings)
// ---------------------------------------------------------------------------

func (b *checklistBuilder) addPostCutoverChecks(sec *ChecklistSection) {
	if sec.SourceCount == 0 {
		return
	}
	switch sec.Section {
	case "mailboxes":
		sec.PostCutoverChecks = append(sec.PostCutoverChecks,
			"Send and receive a test message for at least one mailbox per domain.")
	case "dns":
		sec.PostCutoverChecks = append(sec.PostCutoverChecks,
			"Verify public DNS resolves every domain to the destination server once TTLs expire.")
	case "ssl":
		sec.PostCutoverChecks = append(sec.PostCutoverChecks,
			"Run AutoSSL on the destination and confirm every domain serves a valid certificate.")
	case "web_files":
		sec.PostCutoverChecks = append(sec.PostCutoverChecks,
			"Load every site over HTTPS and confirm the homepage renders from the destination.")
	case "cron":
		sec.PostCutoverChecks = append(sec.PostCutoverChecks,
			"Confirm recreated cron jobs actually ran (check their output/log once).")
	}
}

// ---------------------------------------------------------------------------
// Rollup
// ---------------------------------------------------------------------------

func (b *checklistBuilder) summarize(c *MigrationChecklist) {
	for _, s := range c.Sections {
		switch s.Status {
		case SectionOK:
			c.Summary.OK++
		case SectionReviewRequired:
			c.Summary.ReviewRequired++
		case SectionBlocked:
			c.Summary.Blocked++
		case SectionNotMigratedByTool:
			c.Summary.NotMigratedByTool++
		case SectionNotInventoried:
			c.Summary.NotInventoried++
		case SectionNotAccessibleWithoutRoot:
			c.Summary.NotAccessibleWithoutRoot++
		}
		c.Summary.ExpectedDifferences += len(s.ExpectedDifferences)
	}
	c.Summary.ManualActions = len(c.ManualActions)

	blockingActions := false
	for _, a := range c.ManualActions {
		if a.Accepted {
			c.Summary.Accepted++
			continue // a formally accepted action no longer gates
		}
		if a.BlockingCutover {
			blockingActions = true
		}
	}
	notes := len(c.ManualActions) > 0
	for _, s := range c.Sections {
		switch s.Status {
		case SectionReviewRequired, SectionManualRequired, SectionNotMigratedByTool,
			SectionNotInventoried, SectionExpectedDifference:
			notes = true
		}
	}

	switch {
	case c.Summary.Blocked > 0:
		c.OverallStatus = OverallBlocked
	case blockingActions:
		c.OverallStatus = OverallManualActionRequired
	case b.coreEvidenceMissing():
		c.OverallStatus = OverallNotReady
	case notes:
		c.OverallStatus = OverallReadyWithManualNotes
	default:
		c.OverallStatus = OverallReadyToCutover
	}

	// A PROVEN provenance mismatch means the artifacts were not derived
	// from these inventories: any READY_* verdict is unreliable and is
	// capped to NOT_READY. Worse verdicts stand — the cap never improves.
	if b.chainMismatch &&
		(c.OverallStatus == OverallReadyToCutover || c.OverallStatus == OverallReadyWithManualNotes) {
		c.OverallStatus = OverallNotReady
	}
}

// verifyProvenanceChain compares the hashes the CALLER computed for the
// raw input files (InputRefs) against the hashes each artifact records
// about its OWN inputs. It never hashes anything itself. Missing hashes
// (artifacts produced before PR 7B, or programmatic use without refs)
// leave the chain unverified without gating; a mismatch is evidence the
// composition is inconsistent and is reported for the overall cap.
func verifyProvenanceChain(in ChecklistInput) (verified bool, warnings []string, mismatch bool) {
	refs := in.InputRefs
	var emptyRefs []string
	if refs.SourceInventory.SHA256 == "" {
		emptyRefs = append(emptyRefs, "source inventory")
	}
	if refs.DestinationInventory.SHA256 == "" {
		emptyRefs = append(emptyRefs, "destination inventory")
	}
	if refs.Diff.SHA256 == "" {
		emptyRefs = append(emptyRefs, "diff")
	}
	if len(emptyRefs) == 3 {
		return false, nil, false // fully programmatic use: nothing to verify against
	}

	var missing []string
	// check compares one recorded-vs-expected link; an empty expected ref
	// means the caller could not provide it — the link is skipped here and
	// reported once via emptyRefs (partial refs must never be silent).
	check := func(artifact, input, recorded, expected string) {
		switch {
		case expected == "":
			// reported via emptyRefs
		case recorded == "":
			missing = append(missing, fmt.Sprintf("%s does not record the hash of its %s", artifact, input))
		case recorded != expected:
			mismatch = true
			warnings = append(warnings, fmt.Sprintf(
				"provenance chain mismatch: %s was generated from a DIFFERENT %s (hash mismatch) — regenerate the pipeline from fresh inventories",
				artifact, input))
		}
	}
	check("diff", "source inventory", in.Diff.SourceSHA256, refs.SourceInventory.SHA256)
	check("diff", "destination inventory", in.Diff.DestinationSHA256, refs.DestinationInventory.SHA256)
	check("policy report", "diff", in.Policy.InputDiffSHA256, refs.Diff.SHA256)
	if in.DNSPlan != nil {
		check("dns plan", "source inventory", in.DNSPlan.SourceSHA256, refs.SourceInventory.SHA256)
		check("dns plan", "destination inventory", in.DNSPlan.DestinationSHA256, refs.DestinationInventory.SHA256)
	}
	if len(emptyRefs) > 0 {
		missing = append(missing, "the caller provided no reference hash for: "+strings.Join(emptyRefs, ", "))
	}
	if len(missing) > 0 {
		warnings = append(warnings, "provenance chain not verifiable: "+
			strings.Join(missing, "; ")+" (artifact generated before PR 7B?)")
	}
	return !mismatch && len(missing) == 0, warnings, mismatch
}

// coreEvidenceMissing: an area the tool is SUPPOSED to migrate has data on
// the source and no migration evidence at all.
func (b *checklistBuilder) coreEvidenceMissing() bool {
	if len(b.in.Source.Mailboxes) > 0 && b.evidence["mailboxes"] == EvidenceNone {
		return true
	}
	if len(b.in.Source.Databases) > 0 && b.evidence["databases"] == EvidenceNone {
		return true
	}
	if len(b.in.Source.Domains) > 0 && b.evidence["web_files"] == EvidenceNone {
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// addAction records one manual action derived from a policy finding.
func (b *checklistBuilder) addAction(section, typ string, blocking bool, f PolicyFinding, title, operator string) {
	ref := f.SourceRef
	if ref == "" {
		ref = f.DestinationRef
	}
	ev := []ChecklistEvidence{}
	if ref != "" || f.Detail != "" {
		ev = append(ev, ChecklistEvidence{Kind: "policy_finding", Key: ref, Detail: f.Detail})
	}
	b.addActionRaw(section, typ, blocking, []string{f.ID}, title, f.Detail, operator, ev)
}

func (b *checklistBuilder) addActionRaw(section, typ string, blocking bool, derivedFrom []string, title, detail, operator string, ev []ChecklistEvidence) {
	if ev == nil {
		ev = []ChecklistEvidence{}
	}
	// CONFIRM_MX_EXTERNAL and a blocking cron recreation (with or without
	// path adaptation) must be resolved, not waved through: they are
	// non-acceptable for the operator acceptance flow (PR 7D).
	acceptable := typ != MActionConfirmMXExternal &&
		!(blocking && (typ == MActionRecreateCron || typ == MActionAdaptCronPath))
	a := ManualAction{
		Key:  manualActionKey(typ, section, title, detail),
		Type: typ, Section: section, BlockingCutover: blocking,
		DerivedFrom: derivedFrom, Title: title, Detail: detail,
		Evidence: ev, OperatorAction: operator, Acceptable: acceptable,
	}
	if acc, ok := b.acceptByKey[a.Key]; ok {
		if !b.acceptMatched[a.Key] {
			b.acceptMatched[a.Key] = true
			if a.Acceptable {
				a.Accepted = true
				a.AcceptedBy = acc.AcceptedBy
				a.AcceptedAt = acc.AcceptedAt
				a.AcceptedReason = acc.Reason
			} else {
				b.warnings = append(b.warnings, fmt.Sprintf(
					"acceptances: action %s (%s) is not acceptable — it must be resolved, the acceptance was ignored", a.Key, typ))
			}
		} else {
			// Two structurally identical actions (e.g. the same cron job
			// scheduled twice, both lost) share the same content key. Only
			// the FIRST matching action was accepted — say so, or the
			// second would silently keep gating with no explanation.
			b.warnings = append(b.warnings, fmt.Sprintf(
				"acceptances: key %s matches more than one identical action — only the first was accepted, the other(s) still require attention", a.Key))
		}
	}
	b.actions = append(b.actions, a)
	b.sectionActions[section] = append(b.sectionActions[section], len(b.actions)-1)
}

// manualActionKey derives the STABLE acceptance handle of an action from
// its content: same fact → same key across regenerations; changed fact →
// changed key, so stale acceptances stop matching (fail-safe). NUL framing
// prevents field-boundary collisions.
func manualActionKey(typ, section, title, detail string) string {
	sum := sha256.Sum256([]byte(typ + "\x00" + section + "\x00" + title + "\x00" + detail))
	return fmt.Sprintf("AK-%x", sum[:6])
}

// planOpForFindingRef resolves a policy DNS finding ref ("zone <zone>
// <TYPE> <name>") to the plan op for that rrset, canonicalizing the owner
// name the same way the plan does.
func (b *checklistBuilder) planOpForFindingRef(ref string) (PlanOp, bool) {
	fields := strings.Fields(ref)
	if len(fields) < 3 || fields[0] != "zone" {
		return PlanOp{}, false
	}
	zone, typ, name := fields[1], fields[2], ""
	if len(fields) >= 4 {
		name = fields[3]
	}
	op, ok := b.planOps[planOpKey(zone, typ, canonDNSName(name, zone))]
	return op, ok
}

func planOpKey(zone, typ, canonicalName string) string {
	return strings.ToLower(zone) + "\x00" + strings.ToUpper(typ) + "\x00" + canonicalName
}

// diffEvidence converts one diff section into already-safe evidence
// pointers (diff keys and details are redacted/preview-safe upstream).
func diffEvidence(d InventoryDiff, name string) []ChecklistEvidence {
	out := []ChecklistEvidence{}
	sec, ok := d.Sections[name]
	if !ok {
		return out
	}
	for _, e := range sec.Removed {
		out = append(out, ChecklistEvidence{Kind: "missing_on_destination", Key: e.Key, Detail: e.Detail})
	}
	for _, e := range sec.Added {
		out = append(out, ChecklistEvidence{Kind: "destination_only", Key: e.Key, Detail: e.Detail})
	}
	for _, ch := range sec.Changed {
		out = append(out, ChecklistEvidence{
			Kind: "differs", Key: ch.Key,
			Detail: fmt.Sprintf("%s: %s → %s", ch.Field, ch.Source, ch.Destination),
		})
	}
	for _, s := range sec.Skipped {
		out = append(out, ChecklistEvidence{Kind: "comparison_skipped", Detail: s})
	}
	return out
}

func inventorySectionCount(inv NormalizedInventory, name string) int {
	switch name {
	case "domains":
		return len(inv.Domains)
	case "mailboxes":
		return len(inv.Mailboxes)
	case "databases":
		return len(inv.Databases)
	case "forwarders":
		return len(inv.Forwarders)
	case "autoresponders":
		return len(inv.Autoresponders)
	case "ftp":
		return len(inv.FTP.Items)
	case "ssl":
		return len(inv.SSL.Items)
	case "php":
		return len(inv.PHP.Items)
	case "dns":
		return len(inv.DNS.Zones)
	case "cron":
		return len(inv.Cron.Jobs)
	case "email_routing":
		return len(inv.EmailRouting.Items)
	case "default_address":
		return len(inv.DefaultAddresses.Items)
	case "email_filters":
		return len(inv.EmailFilters.Items)
	case "redirects":
		return len(inv.Redirects.Items)
	}
	return 0
}

func appendUnique(list []string, s string) []string {
	for _, v := range list {
		if v == s {
			return list
		}
	}
	list = append(list, s)
	sort.Strings(list)
	return list
}

func displayOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
