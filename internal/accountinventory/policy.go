package accountinventory

// Policy engine v0: a deterministic rule table over an InventoryDiff. It
// classifies every difference as blocker / review / warning / info and
// rolls them up into an overall migration-readiness status. It NEVER
// decides what to do about a difference, never connects anywhere, and
// contains no heuristics — same diff in, same report out.

import (
	"fmt"
	"sort"
	"strings"
)

// Severity levels, weakest to strongest.
const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityReview  = "review"
	SeverityBlocker = "blocker"
)

// Overall / per-finding statuses.
const (
	StatusReady          = "ready"
	StatusReviewRequired = "review_required"
	StatusBlocked        = "blocked"
)

type PolicyFinding struct {
	ID             string `json:"id"`
	Section        string `json:"section"`
	Severity       string `json:"severity"`
	Status         string `json:"status"`
	Title          string `json:"title"`
	Detail         string `json:"detail"`
	Recommendation string `json:"recommendation"`
	SourceRef      string `json:"source_ref,omitempty"`
	DestinationRef string `json:"destination_ref,omitempty"`
}

type PolicySummary struct {
	Blockers int `json:"blockers"`
	Reviews  int `json:"reviews"`
	Warnings int `json:"warnings"`
	Info     int `json:"info"`
}

type PolicyReport struct {
	Mode        string `json:"mode"`
	GeneratedAt string `json:"generated_at"`
	InputDiff   string `json:"input_diff"`
	// InputDiffSHA256 hashes the raw bytes of the consumed diff file (set
	// by the CLI); the checklist verifies the provenance chain against it
	// (PR 7B). omitempty keeps older artifacts parseable.
	InputDiffSHA256 string          `json:"input_diff_sha256,omitempty"`
	OverallStatus   string          `json:"overall_status"`
	Summary         PolicySummary   `json:"summary"`
	Findings        []PolicyFinding `json:"findings"`
	Warnings        []string        `json:"warnings"`
}

func severityStatus(severity string) string {
	switch severity {
	case SeverityBlocker:
		return StatusBlocked
	case SeverityReview:
		return StatusReviewRequired
	default:
		return StatusReady
	}
}

// severityRank orders findings most-severe first.
func severityRank(severity string) int {
	switch severity {
	case SeverityBlocker:
		return 0
	case SeverityReview:
		return 1
	case SeverityWarning:
		return 2
	default:
		return 3
	}
}

// EvaluatePolicy applies the v0 rule table to a diff. Pure and
// deterministic: GeneratedAt/InputDiff are the caller's concern.
func EvaluatePolicy(d InventoryDiff) PolicyReport {
	r := PolicyReport{
		Mode:     "inventory-policy",
		Findings: []PolicyFinding{},
		Warnings: []string{},
	}
	emit := func(f PolicyFinding) {
		f.Status = severityStatus(f.Severity)
		r.Findings = append(r.Findings, f)
	}

	for _, name := range diffSectionNames {
		sec, ok := d.Sections[name]
		if !ok {
			continue
		}
		switch name {
		case "domains":
			evalDomains(sec, emit)
		case "mailboxes":
			evalSimple(sec, emit, "mailboxes", "POL-MAILBOX",
				"Mailbox", SeverityBlocker, "Recreate the mailbox on the destination before cutover.")
		case "databases":
			evalDatabases(sec, emit)
		case "forwarders":
			evalSimple(sec, emit, "forwarders", "POL-FORWARDER",
				"Forwarder", SeverityReview, "Recreate the forwarder on the destination or confirm it is obsolete.")
		case "autoresponders":
			evalSimple(sec, emit, "autoresponders", "POL-AUTORESPONDER",
				"Autoresponder", SeverityReview, "Recreate the autoresponder on the destination or confirm it is obsolete.")
		case "ftp":
			evalSimple(sec, emit, "ftp", "POL-FTP",
				"FTP account", SeverityReview, "Recreate the FTP account on the destination or confirm it is obsolete.")
		case "ssl":
			evalSSL(sec, d, emit)
		case "php":
			evalPHP(sec, emit)
		case "dns":
			evalDNS(sec, emit)
		case "cron":
			evalCron(sec, emit)
		case "email_routing":
			evalSimple(sec, emit, "email_routing", "POL-MAILROUTE",
				"Mail routing", SeverityReview, "Confirm the destination mail routing (local/remote) matches where this domain's mail is really hosted; a wrong value silently breaks delivery.")
		case "default_address":
			evalSimple(sec, emit, "default_address", "POL-DEFAULTADDR",
				"Default address", SeverityReview, "Recreate the default (catch-all) address on the destination or confirm the difference is intended; a lost catch-all silently drops mail.")
		case "email_filters":
			evalSimple(sec, emit, "email_filters", "POL-EMAILFILTER",
				"Email filter", SeverityReview, "Recreate the filter on the destination or confirm it is obsolete.")
		case "redirects":
			evalRedirects(sec, emit)
		}
		evalSectionWarnings(name, sec, emit)
	}

	for i := range r.Findings {
		switch r.Findings[i].Severity {
		case SeverityBlocker:
			r.Summary.Blockers++
		case SeverityReview:
			r.Summary.Reviews++
		case SeverityWarning:
			r.Summary.Warnings++
		default:
			r.Summary.Info++
		}
	}
	switch {
	case r.Summary.Blockers > 0:
		r.OverallStatus = StatusBlocked
	case r.Summary.Reviews > 0:
		r.OverallStatus = StatusReviewRequired
	default:
		r.OverallStatus = StatusReady
	}

	sort.Slice(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if sa, sb := severityRank(a.Severity), severityRank(b.Severity); sa != sb {
			return sa < sb
		}
		if a.Section != b.Section {
			return a.Section < b.Section
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		if a.Detail != b.Detail {
			return a.Detail < b.Detail
		}
		// Refs complete the ordering: several sections emit findings with
		// an empty Detail (e.g. two removed mailboxes) that differ only
		// by the item they reference.
		if a.SourceRef != b.SourceRef {
			return a.SourceRef < b.SourceRef
		}
		return a.DestinationRef < b.DestinationRef
	})
	return r
}

// evalSectionWarnings turns the diff's structured signals into findings:
// a skipped comparison (structured Skipped field, never matched by prose)
// means incomplete data, which can never be "ready"; free-text diff
// warnings surface as non-gating warnings.
func evalSectionWarnings(section string, sec SectionDiff, emit func(PolicyFinding)) {
	for _, s := range sec.Skipped {
		emit(PolicyFinding{
			ID: "POL-SECTION-UNAVAILABLE", Section: section, Severity: SeverityReview,
			Title:          fmt.Sprintf("%s comparison incomplete", section),
			Detail:         s,
			Recommendation: "Re-run the inventory so both sides expose this section, then diff again.",
		})
	}
	for _, w := range sec.Warnings {
		emit(PolicyFinding{
			ID: "POL-DIFF-WARNING", Section: section, Severity: SeverityWarning,
			Title:          fmt.Sprintf("%s diff warning", section),
			Detail:         w,
			Recommendation: "Inspect the diff warning; it may hide data-quality issues.",
		})
	}
}

// evalSimple covers sections whose policy is uniform: removals get
// removedSeverity, changes get review, additions are informational.
func evalSimple(sec SectionDiff, emit func(PolicyFinding), section, idPrefix, noun, removedSeverity, removedRecommendation string) {
	for _, e := range sec.Removed {
		emit(PolicyFinding{
			ID: idPrefix + "-REMOVED", Section: section, Severity: removedSeverity,
			Title:          noun + " missing on destination",
			Detail:         e.Detail,
			Recommendation: removedRecommendation,
			SourceRef:      e.Key,
		})
	}
	for _, c := range sec.Changed {
		emit(PolicyFinding{
			ID: idPrefix + "-CHANGED", Section: section, Severity: SeverityReview,
			Title:          fmt.Sprintf("%s %s differs", noun, c.Field),
			Detail:         fmt.Sprintf("%s: %s → %s", c.Field, c.Source, c.Destination),
			Recommendation: "Confirm the destination value is intended.",
			SourceRef:      c.Key, DestinationRef: c.Key,
		})
	}
	for _, e := range sec.Added {
		emit(PolicyFinding{
			ID: idPrefix + "-ADDED", Section: section, Severity: SeverityInfo,
			Title:          noun + " present only on destination",
			Detail:         e.Detail,
			Recommendation: "No action needed unless unexpected.",
			DestinationRef: e.Key,
		})
	}
}

func evalDomains(sec SectionDiff, emit func(PolicyFinding)) {
	for _, e := range sec.Removed {
		if e.Detail == "main" {
			emit(PolicyFinding{
				ID: "POL-DOMAIN-MAIN-REMOVED", Section: "domains", Severity: SeverityBlocker,
				Title:          "Main domain missing on destination",
				Detail:         "type: main",
				Recommendation: "The destination account must serve the main domain before any cutover.",
				SourceRef:      e.Key,
			})
			continue
		}
		emit(PolicyFinding{
			ID: "POL-DOMAIN-REMOVED", Section: "domains", Severity: SeverityReview,
			Title:          "Domain missing on destination",
			Detail:         "type: " + e.Detail,
			Recommendation: "Create the domain on the destination or confirm it is being dropped.",
			SourceRef:      e.Key,
		})
	}
	for _, c := range sec.Changed {
		if c.Field == "document_root" {
			// Docroot layouts legitimately differ between hosts.
			emit(PolicyFinding{
				ID: "POL-DOMAIN-DOCROOT-CHANGED", Section: "domains", Severity: SeverityInfo,
				Title:          "Document root differs",
				Detail:         fmt.Sprintf("%s → %s", c.Source, c.Destination),
				Recommendation: "Expected across hosts; verify only if the layout matters to the site.",
				SourceRef:      c.Key, DestinationRef: c.Key,
			})
			continue
		}
		emit(PolicyFinding{
			ID: "POL-DOMAIN-TYPE-CHANGED", Section: "domains", Severity: SeverityReview,
			Title:          "Domain type differs",
			Detail:         fmt.Sprintf("%s: %s → %s", c.Field, c.Source, c.Destination),
			Recommendation: "A domain served with a different type may behave differently; verify.",
			SourceRef:      c.Key, DestinationRef: c.Key,
		})
	}
	for _, e := range sec.Added {
		emit(PolicyFinding{
			ID: "POL-DOMAIN-ADDED", Section: "domains", Severity: SeverityInfo,
			Title:          "Domain present only on destination",
			Detail:         "type: " + e.Detail,
			Recommendation: "No action needed unless unexpected.",
			DestinationRef: e.Key,
		})
	}
}

func evalDatabases(sec SectionDiff, emit func(PolicyFinding)) {
	for _, e := range sec.Removed {
		emit(PolicyFinding{
			ID: "POL-DB-REMOVED", Section: "databases", Severity: SeverityBlocker,
			Title:          "Database missing on destination",
			Detail:         e.Detail,
			Recommendation: "Migrate the database before cutover.",
			SourceRef:      e.Key,
		})
	}
	for _, c := range sec.Changed {
		emit(PolicyFinding{
			ID: "POL-DB-USERS-CHANGED", Section: "databases", Severity: SeverityReview,
			Title:          "Database grants differ",
			Detail:         fmt.Sprintf("%s: %s → %s", c.Field, c.Source, c.Destination),
			Recommendation: "Verify the application's DB user still has the grants it needs.",
			SourceRef:      c.Key, DestinationRef: c.Key,
		})
	}
	for _, e := range sec.Added {
		emit(PolicyFinding{
			ID: "POL-DB-ADDED", Section: "databases", Severity: SeverityInfo,
			Title:          "Database present only on destination",
			Detail:         e.Detail,
			Recommendation: "No action needed unless unexpected.",
			DestinationRef: e.Key,
		})
	}
}

func evalSSL(sec SectionDiff, d InventoryDiff, emit func(PolicyFinding)) {
	// A certificate that disappeared together with ALL of its domains is
	// coherent; one whose domain still exists is a blocker.
	removedDomains := map[string]bool{}
	if domains, ok := d.Sections["domains"]; ok {
		for _, e := range domains.Removed {
			removedDomains[e.Key] = true
		}
	}
	for _, e := range sec.Removed {
		if e.Key == "" {
			// A certificate entry without a domain list cannot be
			// cross-checked: fail closed with an explicit note instead of
			// an empty, unactionable Item column.
			emit(PolicyFinding{
				ID: "POL-SSL-REMOVED", Section: "ssl", Severity: SeverityBlocker,
				Title:          "Certificate missing for a domain still present",
				Detail:         "certificate entry carries no domain list — verify manually",
				Recommendation: "Issue or install a certificate on the destination before cutover.",
				SourceRef:      "(no domain list)",
			})
			continue
		}
		allGone := true
		for _, dom := range strings.Split(e.Key, ",") {
			if !removedDomains[strings.TrimSpace(dom)] {
				allGone = false
				break
			}
		}
		if allGone {
			emit(PolicyFinding{
				ID: "POL-SSL-REMOVED-WITH-DOMAIN", Section: "ssl", Severity: SeverityInfo,
				Title:          "Certificate removed together with its domains",
				Detail:         e.Detail,
				Recommendation: "Coherent with the domain removal; no separate action.",
				SourceRef:      e.Key,
			})
			continue
		}
		emit(PolicyFinding{
			ID: "POL-SSL-REMOVED", Section: "ssl", Severity: SeverityBlocker,
			Title:          "Certificate missing for a domain still present",
			Detail:         e.Detail,
			Recommendation: "Issue or install a certificate on the destination before cutover.",
			SourceRef:      e.Key,
		})
	}
	for _, c := range sec.Changed {
		emit(PolicyFinding{
			ID: "POL-SSL-CHANGED", Section: "ssl", Severity: SeverityReview,
			Title:          fmt.Sprintf("Certificate %s differs", c.Field),
			Detail:         fmt.Sprintf("%s: %s → %s", c.Field, c.Source, c.Destination),
			Recommendation: "Verify the destination certificate covers the cutover window.",
			SourceRef:      c.Key, DestinationRef: c.Key,
		})
	}
	for _, e := range sec.Added {
		emit(PolicyFinding{
			ID: "POL-SSL-ADDED", Section: "ssl", Severity: SeverityInfo,
			Title:          "Certificate present only on destination",
			Detail:         e.Detail,
			Recommendation: "No action needed unless unexpected.",
			DestinationRef: e.Key,
		})
	}
}

func evalPHP(sec SectionDiff, emit func(PolicyFinding)) {
	for _, e := range sec.Removed {
		emit(PolicyFinding{
			ID: "POL-PHP-REMOVED", Section: "php", Severity: SeverityReview,
			Title:          "PHP vhost configuration missing on destination",
			Detail:         e.Detail,
			Recommendation: "Confirm the destination serves this vhost with an intended PHP version.",
			SourceRef:      e.Key,
		})
	}
	for _, c := range sec.Changed {
		emit(PolicyFinding{
			ID: "POL-PHP-CHANGED", Section: "php", Severity: SeverityReview,
			Title:          "PHP version differs",
			Detail:         fmt.Sprintf("%s → %s", c.Source, c.Destination),
			Recommendation: "Test the site against the destination PHP version before cutover.",
			SourceRef:      c.Key, DestinationRef: c.Key,
		})
	}
	for _, e := range sec.Added {
		emit(PolicyFinding{
			ID: "POL-PHP-ADDED", Section: "php", Severity: SeverityInfo,
			Title:          "PHP vhost present only on destination",
			Detail:         e.Detail,
			Recommendation: "No action needed unless unexpected.",
			DestinationRef: e.Key,
		})
	}
}

// dnsKeyType extracts the record type from a DNS diff key
// ("zone <zone> <TYPE> <name>"); zone-level keys ("zone <zone>") return "".
// A 3-token key (record whose owner name decoded empty) is still a
// RECORD-level key: classifying it as a whole zone would produce an
// alarming, wrong "zone missing" blocker for one malformed record.
func dnsKeyType(key string) string {
	fields := strings.Fields(key)
	if len(fields) >= 3 && fields[0] == "zone" {
		return fields[2]
	}
	return ""
}

func isMailRoutingType(t string) bool { return t == "MX" || t == "NS" }

func evalDNS(sec SectionDiff, emit func(PolicyFinding)) {
	for _, e := range sec.Removed {
		typ := dnsKeyType(e.Key)
		switch {
		case typ == "": // whole zone gone — every record, MX included
			emit(PolicyFinding{
				ID: "POL-DNS-ZONE-REMOVED", Section: "dns", Severity: SeverityBlocker,
				Title:          "DNS zone missing on destination",
				Detail:         e.Detail,
				Recommendation: "The destination must serve this zone before any cutover.",
				SourceRef:      e.Key,
			})
		case isMailRoutingType(typ):
			emit(PolicyFinding{
				ID: "POL-DNS-" + typ + "-REMOVED", Section: "dns", Severity: SeverityBlocker,
				Title:          typ + " record missing on destination",
				Detail:         e.Detail,
				Recommendation: "Mail/delegation routing would break; recreate the record before cutover.",
				SourceRef:      e.Key,
			})
		default:
			emit(PolicyFinding{
				ID: "POL-DNS-RECORD-REMOVED", Section: "dns", Severity: SeverityReview,
				Title:          typ + " record missing on destination",
				Detail:         e.Detail,
				Recommendation: "Recreate the record or confirm it is obsolete.",
				SourceRef:      e.Key,
			})
		}
	}
	for _, c := range sec.Changed {
		typ := dnsKeyType(c.Key)
		switch {
		case isMailRoutingType(typ):
			emit(PolicyFinding{
				ID: "POL-DNS-" + typ + "-CHANGED", Section: "dns", Severity: SeverityBlocker,
				Title:          typ + " record differs",
				Detail:         fmt.Sprintf("%s → %s", c.Source, c.Destination),
				Recommendation: "Review mail/delegation routing before cutover.",
				SourceRef:      c.Key, DestinationRef: c.Key,
			})
		case typ == "SOA":
			// SOA serial/timers differ on virtually every regenerated
			// zone: review-severity here would only cause finding fatigue.
			emit(PolicyFinding{
				ID: "POL-DNS-SOA-CHANGED", Section: "dns", Severity: SeverityInfo,
				Title:          "SOA record differs",
				Detail:         fmt.Sprintf("%s → %s", c.Source, c.Destination),
				Recommendation: "Expected when a zone is regenerated on a new host; no action needed.",
				SourceRef:      c.Key, DestinationRef: c.Key,
			})
		default:
			emit(PolicyFinding{
				ID: "POL-DNS-RECORD-CHANGED", Section: "dns", Severity: SeverityReview,
				Title:          typ + " record differs",
				Detail:         fmt.Sprintf("%s → %s", c.Source, c.Destination),
				Recommendation: "Verify the destination value (SPF/DKIM/DMARC and app records especially).",
				SourceRef:      c.Key, DestinationRef: c.Key,
			})
		}
	}
	for _, e := range sec.Added {
		typ := dnsKeyType(e.Key)
		switch {
		case typ == "":
			emit(PolicyFinding{
				ID: "POL-DNS-ZONE-ADDED", Section: "dns", Severity: SeverityInfo,
				Title:          "DNS zone present only on destination",
				Detail:         e.Detail,
				Recommendation: "No action needed unless unexpected.",
				DestinationRef: e.Key,
			})
		case isMailRoutingType(typ):
			emit(PolicyFinding{
				ID: "POL-DNS-MAIL-RECORD-ADDED", Section: "dns", Severity: SeverityReview,
				Title:          typ + " record present only on destination",
				Detail:         e.Detail,
				Recommendation: "New mail/delegation routing appears on the destination; confirm it is intended.",
				DestinationRef: e.Key,
			})
		default:
			emit(PolicyFinding{
				ID: "POL-DNS-RECORD-ADDED", Section: "dns", Severity: SeverityInfo,
				Title:          typ + " record present only on destination",
				Detail:         e.Detail,
				Recommendation: "No action needed unless unexpected.",
				DestinationRef: e.Key,
			})
		}
	}
}

func evalCron(sec SectionDiff, emit func(PolicyFinding)) {
	for _, e := range sec.Removed {
		if strings.Contains(e.Detail, "enabled=false") {
			emit(PolicyFinding{
				ID: "POL-CRON-DISABLED-REMOVED", Section: "cron", Severity: SeverityInfo,
				Title:          "Disabled cron job missing on destination",
				Detail:         e.Detail,
				Recommendation: "The job was disabled; recreate only if you plan to re-enable it.",
				SourceRef:      e.Key,
			})
			continue
		}
		emit(PolicyFinding{
			ID: "POL-CRON-ENABLED-REMOVED", Section: "cron", Severity: SeverityBlocker,
			Title:          "Active cron job missing on destination",
			Detail:         e.Detail,
			Recommendation: "Recreate the job on the destination before cutover.",
			SourceRef:      e.Key,
		})
	}
	for _, c := range sec.Changed {
		id := "POL-CRON-SCHEDULE-CHANGED"
		title := "Cron schedule differs"
		if c.Field == "enabled" {
			id, title = "POL-CRON-ENABLED-CHANGED", "Cron enabled state differs"
		}
		emit(PolicyFinding{
			ID: id, Section: "cron", Severity: SeverityReview,
			Title:          title,
			Detail:         fmt.Sprintf("%s: %s → %s", c.Field, c.Source, c.Destination),
			Recommendation: "Confirm the destination scheduling is intended.",
			SourceRef:      c.Key, DestinationRef: c.Key,
		})
	}
	for _, e := range sec.Added {
		emit(PolicyFinding{
			ID: "POL-CRON-ADDED", Section: "cron", Severity: SeverityInfo,
			Title:          "Cron job present only on destination",
			Detail:         e.Detail,
			Recommendation: "No action needed unless unexpected.",
			DestinationRef: e.Key,
		})
	}
}

// cmsRewriteDetailPrefix marks a redirect whose diff detail identifies
// it as a CMS-generated .htaccess RewriteRule (kind=rewrite,
// type=temporary, no status code — PR7E_PRE_CAPTURES.md fact 4). Those
// rules travel with the web files, so their absence on a not-yet-synced
// destination is expected, not operator work.
const cmsRewriteDetailPrefix = "rewrite/temporary/- "

func isCMSRewriteDetail(detail string) bool {
	return strings.HasPrefix(detail, cmsRewriteDetailPrefix)
}

// evalRedirects: CMS-generated rewrites are informational (they live in
// .htaccess and migrate with the web files); genuine redirects follow
// the uniform removed→review / changed→review / added→info policy.
func evalRedirects(sec SectionDiff, emit func(PolicyFinding)) {
	for _, e := range sec.Removed {
		if isCMSRewriteDetail(e.Detail) {
			emit(PolicyFinding{
				ID: "POL-REDIRECT-CMS-REMOVED", Section: "redirects", Severity: SeverityInfo,
				Title:          "CMS rewrite not on destination yet",
				Detail:         e.Detail,
				Recommendation: "No action: .htaccess RewriteRules travel with the web files migration.",
				SourceRef:      e.Key,
			})
			continue
		}
		emit(PolicyFinding{
			ID: "POL-REDIRECT-REMOVED", Section: "redirects", Severity: SeverityReview,
			Title:          "Redirect missing on destination",
			Detail:         e.Detail,
			Recommendation: "Recreate the redirect on the destination (or confirm the web files migration carries its .htaccess rule).",
			SourceRef:      e.Key,
		})
	}
	for _, c := range sec.Changed {
		emit(PolicyFinding{
			ID: "POL-REDIRECT-CHANGED", Section: "redirects", Severity: SeverityReview,
			Title:          fmt.Sprintf("Redirect %s differs", c.Field),
			Detail:         fmt.Sprintf("%s: %s → %s", c.Field, c.Source, c.Destination),
			Recommendation: "Confirm the destination redirect is intended.",
			SourceRef:      c.Key, DestinationRef: c.Key,
		})
	}
	for _, e := range sec.Added {
		emit(PolicyFinding{
			ID: "POL-REDIRECT-ADDED", Section: "redirects", Severity: SeverityInfo,
			Title:          "Redirect present only on destination",
			Detail:         e.Detail,
			Recommendation: "No action needed unless unexpected.",
			DestinationRef: e.Key,
		})
	}
}
