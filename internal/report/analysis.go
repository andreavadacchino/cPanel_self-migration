// Package report renders the human-facing analysis log and migration report in
// a stable, line-oriented format (golden-tested for regressions).
package report

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// AnalysisMailbox is one mailbox row in the source analysis.
type AnalysisMailbox struct {
	User   string
	Active bool   // ACTIVE vs ORPHAN
	Scheme string // password scheme string, e.g. "SHA-512", "no-shadow", "not-listed"
}

// AnalysisDomain is one domain found under ~/mail, with its mailboxes.
type AnalysisDomain struct {
	Name      string
	Mailboxes []AnalysisMailbox
}

// AnalysisReport is the full source-side ~/mail scan.
type AnalysisReport struct {
	HostRef string // "user@ip:port"
	Date    string // pre-formatted "%Y-%m-%d %H:%M:%S %z"
	Domains []AnalysisDomain
}

// Totals returns the summary counters (domains, mailboxes, active, orphan)
// shown at the bottom of the analysis. Exported so callers can log them.
func (r AnalysisReport) Totals() (domains, mailboxes, active, orphan int) {
	domains = len(r.Domains)
	for _, d := range r.Domains {
		for _, m := range d.Mailboxes {
			mailboxes++
			if m.Active {
				active++
			} else {
				orphan++
			}
		}
	}
	return
}

// WriteAnalysis renders the analysis to w in the stable mail_analysis.log
// format. Domains are emitted in the order given; mailboxes within a domain are
// sorted by name (lexical).
func WriteAnalysis(w io.Writer, r AnalysisReport) error {
	b := &strings.Builder{}

	// Header.
	b.WriteString("# Mail analysis\n")
	fmt.Fprintf(b, "# Host    : %s\n", r.HostRef)
	fmt.Fprintf(b, "# Date    : %s\n", r.Date)
	b.WriteString("# Source  : ~/mail + ~/etc (cPanel)\n")
	b.WriteString("================================================\n")
	b.WriteString("\n")

	for _, d := range r.Domains {
		mbs := make([]AnalysisMailbox, len(d.Mailboxes))
		copy(mbs, d.Mailboxes)
		sort.SliceStable(mbs, func(i, j int) bool { return mbs[i].User < mbs[j].User })

		fmt.Fprintf(b, "DOMAIN: %s  (%d mailbox)\n", d.Name, len(mbs))
		for _, m := range mbs {
			status := "ACTIVE"
			if !m.Active {
				status = "ORPHAN"
			}
			email := m.User + "@" + d.Name
			// printf '    - %-32s [%-6s] [password: %s]\n'
			fmt.Fprintf(b, "    - %-32s [%-6s] [password: %s]\n", email, status, m.Scheme)
		}
		b.WriteString("\n")
	}

	nd, nm, na, no := r.Totals()
	b.WriteString("================================================\n")
	fmt.Fprintf(b, "TOTAL DOMAINS   : %d\n", nd)
	fmt.Fprintf(b, "TOTAL MAILBOXES : %d\n", nm)
	fmt.Fprintf(b, "  - ACTIVE      : %d\n", na)
	fmt.Fprintf(b, "  - ORPHAN      : %d  (mail dir on disk, no account)\n", no)

	_, err := io.WriteString(w, b.String())
	return err
}

// WebAnalysisStatus classifies a docroot in the web-file analysis.
type WebAnalysisStatus int

const (
	// WebReady: the source docroot has content and a destination to copy into.
	WebReady WebAnalysisStatus = iota
	// WebEmpty: the source docroot exists but is empty (will be skipped).
	WebEmpty
	// WebAbsent: the source docroot does not exist on disk (will be skipped).
	WebAbsent
	// WebNoDest: no matching destination domain yet (must be created first).
	WebNoDest
	// WebUnreadable: the source docroot exists but is not readable (permissions);
	// skipped, and the operator must fix permissions — it is NOT empty/absent.
	WebUnreadable
)

// WebAnalysisDomain is one domain's row in the web-file (docroot) analysis.
type WebAnalysisDomain struct {
	Domain      string
	Type        string // source-side type (main_domain/addon_domain/sub_domain/...)
	SrcDocroot  string
	DestDocroot string
	Files       int
	Bytes       int64
	Status      WebAnalysisStatus
}

// WebAnalysisReport is the full source-side docroot scan (the web-file
// counterpart of AnalysisReport). It backs web_analysis.log.
type WebAnalysisReport struct {
	HostRef    string // "user@ip:port"
	Date       string // pre-formatted "%Y-%m-%d %H:%M:%S %z"
	SourceOnly bool   // true when no destination account is configured
	Domains    []WebAnalysisDomain
}

// WebTotals returns the summary counters (docroots, ready, empty, absent,
// unreadable, no-dest, total files, total bytes of the ready docroots).
func (r WebAnalysisReport) WebTotals() (docroots, ready, empty, absent, unreadable, noDest, files int, bytes int64) {
	docroots = len(r.Domains)
	for _, d := range r.Domains {
		switch d.Status {
		case WebReady:
			ready++
			files += d.Files
			bytes += d.Bytes
		case WebEmpty:
			empty++
		case WebAbsent:
			absent++
		case WebUnreadable:
			unreadable++
		case WebNoDest:
			noDest++
		}
	}
	return
}

func webStatusLabel(s WebAnalysisStatus) string {
	switch s {
	case WebReady:
		return "READY"
	case WebEmpty:
		return "EMPTY"
	case WebAbsent:
		return "ABSENT"
	case WebUnreadable:
		return "UNREADABLE"
	case WebNoDest:
		return "NO-DEST"
	default:
		return "?"
	}
}

// WriteWebAnalysis renders the web-file (docroot) analysis to w, mirroring the
// shape of WriteAnalysis/mail_analysis.log. Domains are sorted by name for a
// deterministic file. This is the website-files analogue of mail_analysis.log.
func WriteWebAnalysis(w io.Writer, r WebAnalysisReport) error {
	b := &strings.Builder{}

	b.WriteString("# Web files analysis\n")
	fmt.Fprintf(b, "# Host    : %s\n", r.HostRef)
	fmt.Fprintf(b, "# Date    : %s\n", r.Date)
	b.WriteString("# Source  : document roots (DomainInfo::domains_data), read-only\n")
	if r.SourceOnly {
		b.WriteString("# Dest    : not configured (source-only analysis)\n")
	}
	b.WriteString("# Excludes: cgi-bin, .ftpquota (.well-known is user content)\n")
	b.WriteString("================================================\n")
	b.WriteString("\n")

	doms := make([]WebAnalysisDomain, len(r.Domains))
	copy(doms, r.Domains)
	sort.SliceStable(doms, func(i, j int) bool { return doms[i].Domain < doms[j].Domain })

	for _, d := range doms {
		fmt.Fprintf(b, "DOMAIN: %s  [%s]\n", d.Domain, webStatusLabel(d.Status))
		fmt.Fprintf(b, "    - src docroot : %s\n", dflt(d.SrcDocroot))
		fmt.Fprintf(b, "    - dest docroot: %s\n", dfltDest(d.DestDocroot, r.SourceOnly))
		if d.Status == WebReady {
			fmt.Fprintf(b, "    - content     : %d files, %s\n", d.Files, HumanBytes(d.Bytes))
		}
		b.WriteString("\n")
	}

	nd, ready, empty, absent, unreadable, noDest, files, bytes := r.WebTotals()
	b.WriteString("================================================\n")
	fmt.Fprintf(b, "TOTAL DOCROOTS  : %d\n", nd)
	fmt.Fprintf(b, "  - READY       : %d  (%d files, %s)\n", ready, files, HumanBytes(bytes))
	fmt.Fprintf(b, "  - EMPTY       : %d  (skipped, destination left untouched)\n", empty)
	fmt.Fprintf(b, "  - ABSENT      : %d  (no docroot on disk)\n", absent)
	fmt.Fprintf(b, "  - UNREADABLE  : %d  (docroot exists but permission denied — fix permissions; NOT migrated)\n", unreadable)
	if r.SourceOnly {
		fmt.Fprintf(b, "  - NO-DEST     : %d  (destination not configured)\n", noDest)
	} else {
		fmt.Fprintf(b, "  - NO-DEST     : %d  (destination domain missing)\n", noDest)
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// dfltDest renders a missing destination docroot explicitly.
func dfltDest(s string, sourceOnly bool) string {
	if s == "" {
		if sourceOnly {
			return "(not configured — source-only analysis)"
		}
		return "(none — destination domain missing)"
	}
	return s
}

// DBAnalysisStatus classifies a database in the database analysis.
type DBAnalysisStatus int

const (
	// DBLinked: the database is referenced by at least one site's wp-config.
	DBLinked DBAnalysisStatus = iota
	// DBShared: referenced by more than one wp-config (e.g. two WP installs).
	DBShared
	// DBOrphan: no wp-config references it (still migrated; password may be recovered).
	DBOrphan
)

// DBAnalysisDomain is one database's row in the database analysis.
type DBAnalysisDomain struct {
	SrcDB     string
	SrcUser   string
	DestDB    string
	DestUser  string
	DiskUsage int64
	Configs   []string // docroots (or wp-config paths) that reference this DB
	HasPass   bool     // whether a password is known (reused) vs to-be-generated
	Status    DBAnalysisStatus
}

// DBAnalysisReport is the full source-side database scan (the database
// counterpart of AnalysisReport / WebAnalysisReport). It backs db_analysis.log.
type DBAnalysisReport struct {
	HostRef    string // "user@ip:port"
	Date       string // pre-formatted "%Y-%m-%d %H:%M:%S %z"
	SrcPrefix  string // e.g. "srcacct_"
	DestPrefix string // e.g. "destacct_"
	SourceOnly bool   // true when no destination account is configured
	Databases  []DBAnalysisDomain
	Warnings   []string
}

// DBTotals returns the summary counters (databases, linked, shared, orphan,
// total disk usage).
func (r DBAnalysisReport) DBTotals() (dbs, linked, shared, orphan int, disk int64) {
	dbs = len(r.Databases)
	for _, d := range r.Databases {
		if d.DiskUsage > 0 { // a negative/unknown cPanel disk figure must not shrink the total (matches analyzeDBs)
			disk += d.DiskUsage
		}
		switch d.Status {
		case DBShared:
			shared++
			linked++ // a shared DB is also linked
		case DBLinked:
			linked++
		case DBOrphan:
			orphan++
		}
	}
	return
}

func dbStatusLabel(s DBAnalysisStatus) string {
	switch s {
	case DBLinked:
		return "LINKED"
	case DBShared:
		return "SHARED"
	case DBOrphan:
		return "ORPHAN"
	default:
		return "?"
	}
}

// WriteDBAnalysis renders the database analysis to w, mirroring the shape of
// WriteWebAnalysis/web_analysis.log. Databases are sorted by source name for a
// deterministic file. The password value is NEVER written — only whether one is
// known. This is the database analogue of mail_analysis.log / web_analysis.log.
func WriteDBAnalysis(w io.Writer, r DBAnalysisReport) error {
	b := &strings.Builder{}

	b.WriteString("# Database analysis\n")
	fmt.Fprintf(b, "# Host    : %s\n", r.HostRef)
	fmt.Fprintf(b, "# Date    : %s\n", r.Date)
	b.WriteString("# Source  : Mysql::list_databases / list_users (read-only)\n")
	if r.SourceOnly {
		b.WriteString("# Dest    : not configured (source-only analysis)\n")
		fmt.Fprintf(b, "# Prefix  : %s -> (not configured)\n", dbPrefixLabel(r.SrcPrefix))
	} else {
		fmt.Fprintf(b, "# Prefix  : %s -> %s\n", dbPrefixLabel(r.SrcPrefix), dbPrefixLabel(r.DestPrefix))
	}
	b.WriteString("================================================\n")
	b.WriteString("\n")

	for _, w := range r.Warnings {
		fmt.Fprintf(b, "WARNING: %s\n", w)
	}
	if len(r.Warnings) > 0 {
		b.WriteString("\n")
	}

	dbs := make([]DBAnalysisDomain, len(r.Databases))
	copy(dbs, r.Databases)
	sort.SliceStable(dbs, func(i, j int) bool { return dbs[i].SrcDB < dbs[j].SrcDB })

	for _, d := range dbs {
		fmt.Fprintf(b, "DATABASE: %s  [%s]\n", d.SrcDB, dbStatusLabel(d.Status))
		fmt.Fprintf(b, "    - source     : %s (user %s, %s)\n", d.SrcDB, d.SrcUser, HumanBytes(d.DiskUsage))
		if r.SourceOnly {
			b.WriteString("    - destination: (not configured — source-only analysis)\n")
		} else {
			fmt.Fprintf(b, "    - destination: %s (user %s)\n", d.DestDB, d.DestUser)
		}
		provenance := "to be generated"
		if d.HasPass {
			// A known password with no wp-config (an orphan DB) was recovered
			// elsewhere — the Softaculous registry or a databases: override — not from
			// a wp-config, so don't claim one.
			if len(d.Configs) == 0 {
				provenance = "reused (recovered without a wp-config)"
			} else {
				provenance = "reused from wp-config"
			}
		}
		fmt.Fprintf(b, "    - password   : %s\n", provenance)
		if len(d.Configs) == 0 {
			b.WriteString("    - wp-config  : (none — orphan database)\n")
		} else {
			for _, c := range d.Configs {
				fmt.Fprintf(b, "    - wp-config  : %s\n", c)
			}
		}
		b.WriteString("\n")
	}

	nd, linked, shared, orphan, disk := r.DBTotals()
	b.WriteString("================================================\n")
	fmt.Fprintf(b, "TOTAL DATABASES : %d  (%s on disk)\n", nd, HumanBytes(disk))
	fmt.Fprintf(b, "  - LINKED      : %d  (referenced by a site)\n", linked)
	fmt.Fprintf(b, "  - SHARED      : %d  (referenced by >1 install)\n", shared)
	fmt.Fprintf(b, "  - ORPHAN      : %d  (no wp-config; password may be recovered)\n", orphan)

	_, err := io.WriteString(w, b.String())
	return err
}

func dbPrefixLabel(s string) string {
	if s == "" {
		return "(prefixing disabled)"
	}
	return s
}
