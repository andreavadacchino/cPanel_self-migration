package migrate

import (
	"context"
	"fmt"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/dbmig"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
	"github.com/tis24dev/cPanel_self-migration/internal/validate"
)

// hostRef formats a host as "user@ip:port" for log lines and the report header.
type hostRef struct {
	User string
	IP   string
	Port int
}

func (h hostRef) String() string { return fmt.Sprintf("%s@%s:%d", h.User, h.IP, h.Port) }

// migrationData holds the read-only facts gathered up front (domains on both
// sides, active source mailboxes with hashes, and — when web files are in scope
// — the per-domain document roots on both sides), reused by the compare, domain,
// mailbox, web-file, and verify phases.
type migrationData struct {
	SrcDomains    []model.Domain
	DestDomains   []model.Domain
	DestDomainSet map[string]bool
	Mailboxes     []model.Mailbox

	// FailedDomains holds domains whose creation FAILED during apply. Everything
	// tied to such a domain — its mailboxes, its web files, its databases — is
	// skipped (the destination domain/account does not exist, so proceeding would
	// be pointless or land on a wrong/missing path), and the run ends non-zero.
	// Keyed by domain name for O(1) lookup.
	FailedDomains map[string]bool

	// BlockedDomains holds selected apply domains that Step 8 cannot create
	// because they are referenced by selected mail/file/DB data but are absent
	// from BOTH the source domain inventory and the refreshed destination domain
	// list. Everything tied to such a domain is skipped with this reason, and the
	// run ends non-zero without blocking unrelated healthy domains.
	BlockedDomains map[string]string

	// DomainTypeIssues holds selected domains that exist on the destination but
	// with domain/docroot semantics that do not match the migration's expected
	// destination binding. Mail can usually still be attempted, but web copy and
	// DB config rewrite may be blocked/manual to avoid touching the wrong docroot.
	DomainTypeIssues map[string]DomainTypeIssue

	// Populated when web files OR databases are requested (opts.DoFile ||
	// opts.DoDB). The document roots come from DomainInfo::domains_data on each
	// side and are joined by domain name (paths are never guessed). The database
	// flow needs them too, to locate wp-config.php files on the source and to map
	// their destination paths for the rewrite.
	SrcDocroots  []cpanel.DomainDataEntry
	DestDocroots []cpanel.DomainDataEntry

	// Populated only when databases are requested (opts.DoDB). Databases/DBUsers
	// are the authoritative source inventory; SiteCreds are the wp-config
	// credentials discovered under the source docroots (read-only). DestDatabases
	// is the destination's existing database list (UAPI Mysql::list_databases on
	// the destination) — used by the compare step to report which databases are
	// already there, since the destination cPanel user has no direct MySQL login.
	Databases     []cpanel.DatabaseEntry
	DBUsers       []cpanel.DBUserEntry
	SiteCreds     []dbmig.SiteCreds
	DestDatabases []cpanel.DatabaseEntry

	// MySQL restrictions are the authoritative cPanel naming contract. Prefix is
	// nil when database prefixing is disabled; do not derive a destination prefix
	// from the SSH user.
	SrcMySQLRestrictions  cpanel.MySQLRestrictions
	DestMySQLRestrictions cpanel.MySQLRestrictions
}

// gatherData collects the read-only facts the selected phases need. Domains
// (both sides) are always collected — they drive the domain-creation step that
// mail, web files and databases all depend on. Mailboxes are collected only when
// mail is in scope (doMail). Document roots are collected when web files OR
// databases are in scope (both need them). Database inventory + wp-config
// credentials are collected only when databases are in scope (doDB). Nothing is
// ever written.
func gatherData(ctx context.Context, p *sshx.Pool, log *logx.Logger, doMail, doFile, doDB bool) (md migrationData, err error) {
	// One inline row for the read phase: the CURRENT operation on the left, a
	// "[bar] %  N/M reads" counter on the right, so this phase shows WHAT it is
	// reading (the cursor never just blinks). On the last read the row is REPLACED
	// in place by a permanent "✓ inventory  read — ..." line (the per-scope counts
	// then follow via logDataSummary); on a read error the half-drawn row is
	// cleared instead, so it never lingers under the error.
	prog := log.NewInlineCountProgress(itemPrefix(log, "→", "reading inventory"),
		gatherOpCount(doMail, doFile, doDB), "reads")
	prog.Draw()
	op := func(label string) { prog.SetPrefix(itemPrefix(log, "→", label)); prog.Draw() }
	defer func() {
		if err != nil {
			prog.Finish()
			return
		}
		prog.Replace(itemStr(log, "✓", "inventory", "%s",
			log.Green("read — domains"+gatherWhat(doMail, doFile, doDB))))
	}()

	op("reading source domains")
	srcDomains, err := cpanel.ListDomains(ctx, p.Src)
	if err != nil {
		return migrationData{}, fmt.Errorf("source domain list: %w", err)
	}
	prog.Add(1)

	op("reading dest domains")
	destDomains, err := cpanel.ListDomains(ctx, p.Dest)
	if err != nil {
		return migrationData{}, fmt.Errorf("destination domain list: %w", err)
	}
	prog.Add(1)
	// Defense-in-depth: drop any source domain whose name fails a permissive
	// sanity check (shell injection is already prevented by env-passing; this
	// catches obviously-malformed names early and visibly). Destination domains
	// are not filtered (we only read them to know what already exists).
	srcDomains = filterValid(log, "domain", srcDomains, func(d model.Domain) string { return d.Name }, validate.Domain)
	md = migrationData{
		SrcDomains:    srcDomains,
		DestDomains:   destDomains,
		DestDomainSet: cpanel.DomainNameSet(destDomains),
	}

	if doMail {
		op("scanning source mailboxes")
		mailboxes, err := collectMailboxes(ctx, p.Src)
		if err != nil {
			return migrationData{}, err
		}
		prog.Add(1)
		// Validate both the domain and the local part of each mailbox; skip any
		// that fail, with a warning, instead of aborting the whole run.
		md.Mailboxes = filterMailboxes(log, mailboxes)
	}

	// Document roots feed both the web-file flow and the database flow (the latter
	// uses them to find wp-config.php on the source and map its destination path).
	if doFile || doDB {
		op("reading source docroots")
		srcDocroots, err := cpanel.ListDocroots(ctx, p.Src)
		if err != nil {
			return migrationData{}, fmt.Errorf("source docroots: %w", err)
		}
		prog.Add(1)

		op("reading dest docroots")
		destDocroots, err := cpanel.ListDocroots(ctx, p.Dest)
		if err != nil {
			return migrationData{}, fmt.Errorf("destination docroots: %w", err)
		}
		prog.Add(1)
		// Validate the SOURCE docroots' domain names (skip malformed ones). The
		// docroot PATHS themselves come from cPanel and are passed via env; they
		// are not relative paths, so RelPath does not apply here.
		md.SrcDocroots = filterValid(log, "docroot domain", srcDocroots,
			func(e cpanel.DomainDataEntry) string { return e.Domain }, validate.Domain)
		md.DestDocroots = destDocroots
	}

	if doDB {
		op("reading source MySQL restrictions")
		srcMySQLRestrictions, err := cpanel.GetMySQLRestrictions(ctx, p.Src)
		if err != nil {
			return migrationData{}, fmt.Errorf("source MySQL restrictions: %w", err)
		}
		prog.Add(1)

		op("reading source databases")
		databases, err := cpanel.ListDatabases(ctx, p.Src)
		if err != nil {
			return migrationData{}, fmt.Errorf("source database list: %w", err)
		}
		prog.Add(1)

		op("reading source db users")
		dbUsers, err := cpanel.ListDBUsers(ctx, p.Src)
		if err != nil {
			return migrationData{}, fmt.Errorf("source database user list: %w", err)
		}
		prog.Add(1)

		// Discover DB credentials read-only, layered: Softaculous registry ->
		// per-CMS config files (WordPress/Joomla/Drupal/…) -> name-search for any
		// database still missing a password. Driven by the authoritative DB list
		// so even a database no config mentions gets a recovery attempt.
		op("discovering db credentials")
		creds, err := dbmig.DiscoverAllCreds(ctx, p.Src, srcDocrootPaths(md.SrcDocroots), dbNames(databases))
		if err != nil {
			return migrationData{}, fmt.Errorf("discover database credentials: %w", err)
		}
		prog.Add(1)

		op("reading dest MySQL restrictions")
		destMySQLRestrictions, err := cpanel.GetMySQLRestrictions(ctx, p.Dest)
		if err != nil {
			return migrationData{}, fmt.Errorf("destination MySQL restrictions: %w", err)
		}
		prog.Add(1)

		// Destination's existing databases (read-only) — the destination cPanel
		// user cannot log into MySQL directly, but UAPI list_databases works, so
		// this is how the compare step learns what already exists there.
		op("reading dest databases")
		destDatabases, err := cpanel.ListDatabases(ctx, p.Dest)
		if err != nil {
			return migrationData{}, fmt.Errorf("destination database list: %w", err)
		}
		prog.Add(1)
		// Validate source database names; skip malformed ones with a warning.
		md.Databases = filterValid(log, "database", databases,
			func(d cpanel.DatabaseEntry) string { return d.Database }, validate.DBName)
		md.DBUsers = dbUsers
		md.SiteCreds = creds
		md.DestDatabases = destDatabases
		md.SrcMySQLRestrictions = srcMySQLRestrictions
		md.DestMySQLRestrictions = destMySQLRestrictions
	}

	return md, nil
}

// gatherSourceOnlyData collects only the SOURCE-side facts needed for source-only
// analysis when the destination block is absent. It intentionally does not read
// destination domains/docroots/databases, and it does not collect active
// mailboxes: the source-only mail analysis uses collectAnalysis directly because
// it needs the active/orphan mailbox report, not the apply-time hash inventory.
func gatherSourceOnlyData(ctx context.Context, p *sshx.Pool, log *logx.Logger, doFile, doDB bool) (md migrationData, err error) {
	total := sourceOnlyGatherOpCount(doFile, doDB)
	if total == 0 {
		return migrationData{}, nil
	}
	prog := log.NewInlineCountProgress(itemPrefix(log, "→", "reading source inventory"), total, "reads")
	prog.Draw()
	op := func(label string) { prog.SetPrefix(itemPrefix(log, "→", label)); prog.Draw() }
	defer func() {
		if err != nil {
			prog.Finish()
			return
		}
		prog.Replace(itemStr(log, "✓", "source inventory", "%s",
			log.Green("read"+sourceOnlyGatherWhat(doFile, doDB))))
	}()

	if doFile || doDB {
		op("reading source docroots")
		srcDocroots, err := cpanel.ListDocroots(ctx, p.Src)
		if err != nil {
			return migrationData{}, fmt.Errorf("source docroots: %w", err)
		}
		prog.Add(1)
		md.SrcDocroots = filterValid(log, "docroot domain", srcDocroots,
			func(e cpanel.DomainDataEntry) string { return e.Domain }, validate.Domain)
	}

	if doDB {
		op("reading source MySQL restrictions")
		srcMySQLRestrictions, err := cpanel.GetMySQLRestrictions(ctx, p.Src)
		if err != nil {
			return migrationData{}, fmt.Errorf("source MySQL restrictions: %w", err)
		}
		prog.Add(1)

		op("reading source databases")
		databases, err := cpanel.ListDatabases(ctx, p.Src)
		if err != nil {
			return migrationData{}, fmt.Errorf("source database list: %w", err)
		}
		prog.Add(1)

		op("reading source db users")
		dbUsers, err := cpanel.ListDBUsers(ctx, p.Src)
		if err != nil {
			return migrationData{}, fmt.Errorf("source database user list: %w", err)
		}
		prog.Add(1)

		op("discovering db credentials")
		creds, err := dbmig.DiscoverAllCreds(ctx, p.Src, srcDocrootPaths(md.SrcDocroots), dbNames(databases))
		if err != nil {
			return migrationData{}, fmt.Errorf("discover database credentials: %w", err)
		}
		prog.Add(1)

		md.Databases = filterValid(log, "database", databases,
			func(d cpanel.DatabaseEntry) string { return d.Database }, validate.DBName)
		md.DBUsers = dbUsers
		md.SiteCreds = creds
		md.SrcMySQLRestrictions = srcMySQLRestrictions
	}

	return md, nil
}

func sourceOnlyGatherOpCount(doFile, doDB bool) int {
	n := 0
	if doFile || doDB {
		n++ // source docroots
	}
	if doDB {
		n += 4 // source MySQL restrictions, source databases, source db users, credential discovery
	}
	return n
}

func sourceOnlyGatherWhat(doFile, doDB bool) string {
	var parts []string
	if doFile || doDB {
		parts = append(parts, "document roots")
	}
	if doDB {
		parts = append(parts, "databases")
	}
	if len(parts) == 0 {
		return ""
	}
	return " — " + strings.Join(parts, ", ")
}

// gatherOpCount is the number of read operations gatherData performs for the
// given scope, used as the progress-bar total: source+destination domains
// always; +1 mailbox scan (mail); +2 docroot reads (files or databases); +6
// database reads — source/destination MySQL restrictions, list, users,
// credential discovery, destination list (db).
func gatherOpCount(doMail, doFile, doDB bool) int {
	n := 2
	if doMail {
		n++
	}
	if doFile || doDB {
		n += 2
	}
	if doDB {
		n += 6
	}
	return n
}

// refreshDocroots re-reads the document roots from BOTH sides (read-only) and
// updates pd in place. It is called right after domain creation so the web-file
// and database steps see a domain that was just created (its destination docroot
// did not exist during the initial analysis). It is a NO-OP when docroots were
// never gathered (i.e. neither web files nor databases are in scope: pd has nil
// docroot slices), so a mail-only apply does not do useless reads. The same
// validation policy as gatherData is applied to the SOURCE domain names.
// refreshDocroots re-reads the source and destination docroots after domain
// creation so the web/db phases see newly-created destination docroots. onlyDomain
// (the --domain filter, "" when unset) is re-applied to the freshly-read SOURCE
// docroots: a full re-read would otherwise silently UNDO the early scope filter
// and widen the web phase (which empties the dest docroot) back to every domain.
// DestDocroots stays full (collision detection needs the whole picture).
func refreshDocroots(ctx context.Context, p *sshx.Pool, pd *migrationData, log *logx.Logger, onlyDomain string) error {
	if pd.SrcDocroots == nil && pd.DestDocroots == nil {
		return nil // docroots not in scope (mail-only) — nothing to refresh
	}
	srcDocroots, err := cpanel.ListDocroots(ctx, p.Src)
	if err != nil {
		return fmt.Errorf("re-read source docroots: %w", err)
	}
	destDocroots, err := cpanel.ListDocroots(ctx, p.Dest)
	if err != nil {
		return fmt.Errorf("re-read destination docroots: %w", err)
	}
	pd.SrcDocroots = filterValid(log, "docroot domain", srcDocroots,
		func(e cpanel.DomainDataEntry) string { return e.Domain }, validate.Domain)
	if onlyDomain != "" {
		pd.SrcDocroots = filterDocrootsToDomain(pd.SrcDocroots, onlyDomain)
	}
	pd.DestDocroots = destDocroots
	return nil
}

// filterValid returns the input entries whose key (extracted by keyOf) passes
// the validator, logging a warning for each one skipped. It is the shared
// "skip-with-warning" policy for the gathered data: a single malformed
// identifier never aborts the whole migration, but it is always reported.
func filterValid[T any](log *logx.Logger, kind string, in []T, keyOf func(T) string, valid func(string) error) []T {
	out := in[:0:0] // new backing array; do not alias the input
	for _, e := range in {
		key := keyOf(e)
		if err := valid(key); err != nil {
			log.Warn("skipping %s %q: %v", kind, key, err)
			continue
		}
		out = append(out, e)
	}
	return out
}

// filterMailboxes keeps only mailboxes whose domain AND local part are valid,
// warning on each skipped one.
func filterMailboxes(log *logx.Logger, in []model.Mailbox) []model.Mailbox {
	out := in[:0:0]
	for _, m := range in {
		if err := validate.Domain(m.Domain); err != nil {
			log.Warn("skipping mailbox %q: %v", m.Email(), err)
			continue
		}
		if err := validate.MailboxUser(m.User); err != nil {
			log.Warn("skipping mailbox %q: %v", m.Email(), err)
			continue
		}
		out = append(out, m)
	}
	return out
}

// srcDocrootPaths extracts the document-root paths from the source docroot
// entries, for the wp-config discovery scan.
func srcDocrootPaths(entries []cpanel.DomainDataEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.DocumentRoot != "" {
			out = append(out, e.DocumentRoot)
		}
	}
	return out
}

// dbNames extracts the database names from the inventory, for the name-search
// credential fallback.
func dbNames(entries []cpanel.DatabaseEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Database)
	}
	return out
}
