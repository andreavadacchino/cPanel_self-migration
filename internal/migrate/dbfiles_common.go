package migrate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/dbmig"
	"github.com/tis24dev/cPanel_self-migration/internal/domainname"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
	"github.com/tis24dev/cPanel_self-migration/internal/validate"
)

// domainFailed reports whether a domain's creation failed during apply (so all
// its mail/files/databases must be skipped). Safe on a nil map.
func domainFailed(pd migrationData, domain string) bool {
	return pd.FailedDomains[domainname.Key(domain)]
}

func domainBlocked(pd migrationData, domain string) (string, bool) {
	reason, ok := pd.BlockedDomains[domainname.Key(domain)]
	return reason, ok
}

func domainUnavailable(pd migrationData, domain string) bool {
	if domainFailed(pd, domain) {
		return true
	}
	_, blocked := domainBlocked(pd, domain)
	return blocked
}

func domainUnavailableReason(pd migrationData, domain string) string {
	if domainFailed(pd, domain) {
		return "domain '" + domain + "' creation failed"
	}
	if reason, blocked := domainBlocked(pd, domain); blocked {
		return reason
	}
	return ""
}

func destDomainNameFor(pd migrationData, domain string) (string, bool) {
	matches := uniqueDestDomainMatches(pd.DestDomains, domain)
	switch len(matches) {
	case 0:
		if domainname.Has(pd.DestDomainSet, domain) {
			return domain, true
		}
		return "", false
	case 1:
		// The matched name comes from the UNFILTERED destination domain list
		// (cPanel-reported, never run through validate.Domain). It is used to build
		// $HOME/mail/<destDom>/<user> and the wp-config rewrite path, so a malformed
		// name (e.g. "..", or one containing "/") would escape the intended tree.
		// Reject it here — the caller's "not resolved" branch (FAIL/UNVERIFIED) then
		// handles it instead of letting it reach a destructive path. Validate the
		// CANONICAL form (domainname.Key strips a legitimate FQDN trailing dot, which
		// validate.Domain would otherwise reject) but return the raw matched name.
		if err := validate.Domain(domainname.Key(matches[0])); err != nil {
			logx.Debug("destDomainNameFor %s: matched destination name %q rejected as malformed: %v", domain, matches[0], err)
			return "", false
		}
		return matches[0], true
	default:
		return "", false
	}
}

func destDomainResolutionIssue(pd migrationData, domain string) string {
	matches := uniqueDestDomainMatches(pd.DestDomains, domain)
	if len(matches) > 1 {
		sort.Strings(matches)
		return fmt.Sprintf("destination canonical domain collision for %q: %s", domain, strings.Join(matches, "; "))
	}
	return fmt.Sprintf("destination domain %q not configured", domain)
}

func uniqueDestDomainMatches(domains []model.Domain, domain string) []string {
	seen := map[string]bool{}
	var matches []string
	for _, d := range domains {
		if !domainname.Equal(d.Name, domain) || seen[d.Name] {
			continue
		}
		seen[d.Name] = true
		matches = append(matches, d.Name)
	}
	return matches
}

// dbAllDomainsFailed reports whether EVERY domain that references a database is
// unavailable because domain creation failed or selected-domain inventory
// coverage blocked it — in which case the database has no working destination
// site and is skipped. It returns false when the database has no referencing
// config (an orphan: keep migrating it) or when at least one referencing domain
// is healthy (a shared DB still needed by that domain). The domain of each config
// is resolved from the source docroot that contains it.
func dbAllDomainsFailed(pd migrationData, it dbmig.DBPlanItem) bool {
	if (len(pd.FailedDomains) == 0 && len(pd.BlockedDomains) == 0) || len(it.Configs) == 0 {
		return false
	}
	anyResolved := false
	for _, cfg := range it.Configs {
		entry, ok := srcDocrootContaining(pd, cfg.ConfigPath)
		if !ok {
			continue
		}
		anyResolved = true
		if !domainUnavailable(pd, entry.Domain) {
			return false // a healthy domain still needs this DB
		}
	}
	// True only if we resolved at least one domain and they were ALL failed.
	return anyResolved
}

func dbAllDomainsUnavailableForApply(pd migrationData, it dbmig.DBPlanItem) (bool, string) {
	if len(it.Configs) == 0 {
		return false, ""
	}
	anyResolved := false
	anyUnresolved := false
	var unavailable, typeBlocked bool
	var unavailableReasons []string
	for _, cfg := range it.Configs {
		entry, ok := srcDocrootContaining(pd, cfg.ConfigPath)
		if !ok {
			// This referencing config could not be mapped to a source docroot, so we
			// cannot tell which (possibly healthy) domain needs the DB. Record it: we
			// must NOT then conclude "all referencing domains are unavailable".
			anyUnresolved = true
			continue
		}
		anyResolved = true
		if domainUnavailable(pd, entry.Domain) {
			unavailable = true
			if reason := domainUnavailableReason(pd, entry.Domain); reason != "" {
				unavailableReasons = append(unavailableReasons, reason)
			}
			continue
		}
		if issue, blocked := domainTypeIssue(pd, entry.Domain); blocked && issue.BlockDBConfig {
			typeBlocked = true
			continue
		}
		return false, "" // at least one safe referencing site still needs this DB
	}
	if !anyResolved {
		return false, ""
	}
	if anyUnresolved {
		// At least one referencing config did not resolve to a source docroot. Skipping
		// the DB here is only safe when EVERY referencing site is provably unavailable;
		// an unresolved reference breaks that proof, so migrate the DB rather than skip
		// it. Skipping a still-needed database loses its data; migrating a possibly
		// unneeded one is harmless (it lands as an orphan DB on the destination).
		logx.Debug("applyDBs %s: a referencing config did not resolve to a source docroot — migrating instead of skipping (cannot prove all sites unavailable)", it.SrcDB)
		return false, ""
	}
	switch {
	case unavailable && typeBlocked:
		return true, "all referencing domains failed creation, are blocked by domain creation preflight, or have unsafe destination domain type bindings" + formatUnavailableReasons(unavailableReasons)
	case typeBlocked:
		return true, "all referencing site configs are blocked by destination domain type compatibility; DB data not imported to avoid a database cutover with no safe destination config"
	default:
		return true, "all referencing domains failed creation or are blocked by domain creation preflight" + formatUnavailableReasons(unavailableReasons)
	}
}

func formatUnavailableReasons(reasons []string) string {
	if len(reasons) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var unique []string
	for _, reason := range reasons {
		if reason == "" || seen[reason] {
			continue
		}
		seen[reason] = true
		unique = append(unique, reason)
	}
	if len(unique) == 0 {
		return ""
	}
	sort.Strings(unique)
	return ": " + strings.Join(unique, "; ")
}

func dbConfigTypeIssueReasons(pd migrationData, it dbmig.DBPlanItem) []string {
	seen := map[string]bool{}
	var reasons []string
	for _, cfg := range it.Configs {
		entry, ok := srcDocrootContaining(pd, cfg.ConfigPath)
		if !ok {
			continue
		}
		issue, blocked := domainTypeIssue(pd, entry.Domain)
		if !blocked || !issue.BlockDBConfig {
			continue
		}
		reason := issue.Reason()
		if seen[reason] {
			continue
		}
		seen[reason] = true
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	return reasons
}

func dbUnavailableReasonFailsDB(reason string) bool {
	return strings.Contains(reason, "destination domain type")
}

// dbPlan builds the database migration plan from the gathered inventory and the
// discovered wp-config credentials, mapping the source prefix to the destination
// prefix. overrides come from the optional host.yaml databases: section. The
// source and destination prefix policies come from Mysql::get_restrictions.
func dbPlan(pd migrationData, overrides map[string]dbmig.Override) []dbmig.DBPlanItem {
	return dbmig.BuildPlanWithMapping(
		pd.Databases, pd.DBUsers, pd.SiteCreds,
		dbNameMapping(pd), overrides,
	)
}

func dbNameMapping(pd migrationData) dbmig.NameMapping {
	legacy := legacySrcPrefix(pd.Databases)
	src := mysqlNamePrefix(pd.SrcMySQLRestrictions, legacy)
	dest := mysqlNamePrefix(pd.DestMySQLRestrictions, "")
	if !mysqlRestrictionsKnown(pd.SrcMySQLRestrictions) && legacy != "" {
		// The source's authoritative Mysql::get_restrictions was unavailable, so the
		// account DB prefix is GUESSED from the first database name — a wrong guess
		// produces wrong destination DB/user names. Surface it (always-visible Warn).
		logx.Warn("source Mysql::get_restrictions unavailable; deriving the account DB prefix %q from the first database name — verify the destination DB/user names", legacy)
	}
	logx.Debug("dbNameMapping: src prefix=%q (enabled=%v, restrictions known=%v), dest prefix=%q (enabled=%v)",
		src.Value, src.Enabled, mysqlRestrictionsKnown(pd.SrcMySQLRestrictions), dest.Value, dest.Enabled)
	return dbmig.NameMapping{Source: src, Destination: dest}
}

func mysqlNamePrefix(r cpanel.MySQLRestrictions, legacyPrefix string) dbmig.NamePrefix {
	if mysqlRestrictionsKnown(r) {
		if r.Prefix == nil {
			return dbmig.NamePrefix{}
		}
		return dbmig.NamePrefix{Enabled: true, Value: *r.Prefix}
	}
	return dbmig.NamePrefix{Enabled: legacyPrefix != "", Value: legacyPrefix}
}

func mysqlRestrictionsKnown(r cpanel.MySQLRestrictions) bool {
	return r.MaxDatabaseNameLength > 0 || r.MaxUsernameLength > 0 || r.Prefix != nil
}

func legacySrcPrefix(dbs []cpanel.DatabaseEntry) string {
	for _, db := range dbs {
		for i := 0; i < len(db.Database); i++ {
			if db.Database[i] == '_' {
				return db.Database[:i+1]
			}
		}
	}
	return ""
}

func dbSrcPrefix(pd migrationData) string {
	p := mysqlNamePrefix(pd.SrcMySQLRestrictions, legacySrcPrefix(pd.Databases))
	if !p.Enabled {
		return ""
	}
	return p.Value
}

func dbDestPrefix(pd migrationData) string {
	p := mysqlNamePrefix(pd.DestMySQLRestrictions, "")
	if !p.Enabled {
		return ""
	}
	return p.Value
}

func dbPlanNameViolations(pd migrationData, plan []dbmig.DBPlanItem) []dbmig.PlanNameViolation {
	if !mysqlRestrictionsKnown(pd.DestMySQLRestrictions) {
		return nil
	}
	mapping := dbNameMapping(pd)
	return dbmig.ValidateDestNameLimits(plan,
		pd.DestMySQLRestrictions.MaxDatabaseNameLength,
		pd.DestMySQLRestrictions.MaxUsernameLength,
		mapping.Destination)
}

func dbPlanNameError(violations []dbmig.PlanNameViolation) error {
	if len(violations) == 0 {
		return nil
	}
	details := make([]string, 0, len(violations))
	for _, v := range violations {
		details = append(details, v.Detail)
	}
	return fmt.Errorf("unsafe DB name plan: %s", strings.Join(details, "; "))
}

// destDocrootFor returns the destination document root for a domain name, or ""
// if the domain has no destination docroot yet. Used to map a source wp-config
// path to its destination path for the rewrite.
func destDocrootFor(pd migrationData, domain string) string {
	docroot, _ := destDocrootForChecked(pd, domain)
	return docroot
}

func destDocrootForChecked(pd migrationData, domain string) (string, string) {
	matches := uniqueDestDocrootMatches(pd.DestDocroots, domain)
	switch len(matches) {
	case 0:
		return "", ""
	case 1:
		return matches[0].DocumentRoot, ""
	default:
		return "", canonicalDestDocrootCollisionNote(domain, matches)
	}
}

func uniqueDestDocrootMatches(docroots []cpanel.DomainDataEntry, domain string) []cpanel.DomainDataEntry {
	seen := map[string]bool{}
	var matches []cpanel.DomainDataEntry
	for _, e := range docroots {
		if !domainname.Equal(e.Domain, domain) {
			continue
		}
		key := e.Domain + "\x00" + e.DocumentRoot
		if seen[key] {
			continue
		}
		seen[key] = true
		matches = append(matches, e)
	}
	return matches
}

func canonicalDestDocrootCollisionNote(domain string, matches []cpanel.DomainDataEntry) string {
	parts := make([]string, 0, len(matches))
	for _, e := range matches {
		parts = append(parts, fmt.Sprintf("%s -> %s", e.Domain, e.DocumentRoot))
	}
	sort.Strings(parts)
	return fmt.Sprintf("destination canonical domain collision for %q: %s", domain, strings.Join(parts, "; "))
}

func destinationDomainMissingDocroot(pd migrationData, domain string) bool {
	docroot, issue := destDocrootForChecked(pd, domain)
	return issue == "" && domainname.Has(pd.DestDomainSet, domain) && docroot == ""
}

// srcDocrootContaining returns the source docroot entry whose DocumentRoot is a
// prefix of the given path (i.e. the docroot that contains a wp-config), or
// false. When several match (nested docroots), the LONGEST (most specific) wins.
func srcDocrootContaining(pd migrationData, path string) (cpanel.DomainDataEntry, bool) {
	var best cpanel.DomainDataEntry
	found := false
	for _, e := range pd.SrcDocroots {
		if e.DocumentRoot == "" {
			continue
		}
		if hasPathPrefix(path, e.DocumentRoot) && len(e.DocumentRoot) > len(best.DocumentRoot) {
			best = e
			found = true
		}
	}
	return best, found
}

// hasPathPrefix reports whether path is the dir itself or lies under it (a
// boundary-aware prefix check, so "/a/bc" is NOT under "/a/b").
func hasPathPrefix(path, dir string) bool {
	// Tolerate a trailing slash on dir (e.g. a docroot given as ".../public_html/"):
	// strip trailing slashes so the boundary check below holds, but keep "/" as root.
	for len(dir) > 1 && dir[len(dir)-1] == '/' {
		dir = dir[:len(dir)-1]
	}
	if path == dir {
		return true
	}
	if len(path) <= len(dir) {
		return false
	}
	return path[:len(dir)] == dir && path[len(dir)] == '/'
}
