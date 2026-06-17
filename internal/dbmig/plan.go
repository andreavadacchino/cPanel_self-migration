// Package dbmig plans and executes the MySQL-database half of the cPanel
// migration: it joins the source databases/users with the credentials found in
// each site's wp-config.php, maps every name from the source account prefix to
// the destination account prefix, and (on apply) recreates the database + user
// on the destination and streams the data via a mysqldump|mysql bridge.
//
// The SOURCE is only ever read from (mysqldump --single-transaction, list APIs,
// wp-config reads). All writes (create database/user, data import, wp-config
// rewrite) target the DESTINATION exclusively.
package dbmig

import (
	"sort"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/wpconfig"
)

// SiteCreds is a database credential set discovered for one site, tagged with
// the docroot it came from and the config file that declared it (used both for
// logging and for the later wp-config rewrite on the destination).
//
// FromRegistry marks credentials that came from the Softaculous registry rather
// than a real on-disk site config. Such entries supply credentials (and may even
// be the ONLY source for a database whose files were removed) but their
// ConfigPath is the registry file, NOT a config to rewrite — so they must not
// contribute to the per-site rewrite list.
type SiteCreds struct {
	Docroot      string // absolute docroot path on the source
	ConfigPath   string // absolute path to the config file (or the registry, if FromRegistry)
	Kind         Kind   // which CMS recognized this config (drives the destination rewrite)
	FromRegistry bool   // true => from Softaculous, not a rewritable site config
	wpconfig.Creds
}

// DBPlanItem is one database to migrate, fully resolved: its source name/owner,
// the destination-prefixed name/owner it will become, the password to reuse
// (from wp-config) or generate, and every docroot whose wp-config references it
// (so each can be rewritten on the destination). A database with no referencing
// wp-config is an ORPHAN (Configs empty): it is still migrated, but its user
// password is unknown and must be generated.
type DBPlanItem struct {
	SrcDB     string // e.g. srcacct_wp694
	SrcUser   string // primary MySQL user that owns/uses it, e.g. srcacct_u1
	DestDB    string // e.g. destacct_wp694
	DestUser  string // e.g. destacct_u1
	Password  string // reused from wp-config when known; empty => generate on dest
	DiskUsage int64  // bytes on disk, from list_databases (informational)

	// Configs lists every source docroot whose wp-config.php points at SrcDB.
	// Usually one; a shared database (e.g. two WordPress installs) has several,
	// and ALL of them get rewritten on the destination. Empty for an orphan DB.
	Configs []DBConfigRef

	Orphan bool // true when no wp-config references this database
}

// DBConfigRef points at one wp-config.php that uses a database, with the table
// prefix it declared (preserved unchanged; informational for logging).
type DBConfigRef struct {
	Docroot     string
	ConfigPath  string
	TablePrefix string
	Kind        Kind // which CMS this config is, so the destination rewrite can dispatch
}

// NamePrefix describes cPanel MySQL prefixing for one side of the migration.
// Enabled=false means Mysql::get_restrictions returned prefix:null, so names are
// not account-prefixed on that side.
type NamePrefix struct {
	Enabled bool
	Value   string
}

// NameMapping is the explicit source->destination MySQL naming policy. It is
// driven by Mysql::get_restrictions, not by SSH usernames.
type NameMapping struct {
	Source      NamePrefix
	Destination NamePrefix
}

// prefixOf returns the cPanel account prefix of a database/user name: the text
// up to and including the first underscore (e.g. "srcacct_wp694" -> "srcacct_").
// Names without an underscore yield "" (no prefix), and are mapped unchanged.
func prefixOf(name string) string {
	i := strings.IndexByte(name, '_')
	if i < 0 {
		return ""
	}
	return name[:i+1]
}

// remap rewrites a name's source prefix to the destination prefix. If the name
// does not start with srcPrefix (or srcPrefix is empty), it returns the name
// unchanged — defensive against unexpected names rather than corrupting them.
func remap(name, srcPrefix, destPrefix string) string {
	if srcPrefix == "" || !strings.HasPrefix(name, srcPrefix) {
		return name
	}
	return destPrefix + strings.TrimPrefix(name, srcPrefix)
}

func remapName(name string, mapping NameMapping) string {
	suffix := name
	if mapping.Source.Enabled {
		if !strings.HasPrefix(name, mapping.Source.Value) {
			return name
		}
		suffix = strings.TrimPrefix(name, mapping.Source.Value)
	}
	if mapping.Destination.Enabled {
		return mapping.Destination.Value + suffix
	}
	return suffix
}

// BuildPlan joins the authoritative source database list with the credentials
// discovered from wp-config files and produces one resolved DBPlanItem per
// source database, mapped to the destination prefix.
//
//   - dbs / users: from cpanel.ListDatabases / ListDBUsers on the SOURCE.
//   - creds: parsed wp-config credentials (one per docroot that has a wp-config),
//     used to attach owner/password and the rewrite targets. A database may be
//     referenced by several configs (shared DB) or none (orphan).
//   - srcPrefix / destPrefix: e.g. "srcacct_" and "destacct_". When empty,
//     BuildPlan derives srcPrefix from the first prefixed database name.
//   - overrides: optional credential overrides keyed by SOURCE database name
//     (from host.yaml); they win over wp-config for owner/password and can
//     supply a password for an orphan database.
//
// Pure and deterministic (sorted by source DB name); fully unit-tested.
func BuildPlan(dbs []cpanel.DatabaseEntry, users []cpanel.DBUserEntry, creds []SiteCreds, srcPrefix, destPrefix string, overrides map[string]Override) []DBPlanItem {
	if srcPrefix == "" {
		srcPrefix = deriveSrcPrefix(dbs)
	}
	mapping := NameMapping{
		Source:      NamePrefix{Enabled: srcPrefix != "", Value: srcPrefix},
		Destination: NamePrefix{Enabled: destPrefix != "", Value: destPrefix},
	}
	return BuildPlanWithMapping(dbs, users, creds, mapping, overrides)
}

// BuildPlanWithMapping is the production planner: it maps names with the
// explicit MySQL prefix policy read from cPanel on both sides. Unlike BuildPlan's
// legacy string-prefix API, it can represent prefixing disabled on source and/or
// destination.
func BuildPlanWithMapping(dbs []cpanel.DatabaseEntry, users []cpanel.DBUserEntry, creds []SiteCreds, mapping NameMapping, overrides map[string]Override) []DBPlanItem {
	// Index wp-config credentials by the database name they reference, so a
	// shared database collects all its (real, on-disk) configs. Registry-sourced
	// credentials (FromRegistry) supply owner/password but are NOT added to the
	// rewrite list — their ConfigPath is the Softaculous file, not a site config.
	// Real configs are preferred for owner/password; the registry fills any gap.
	configsByDB := map[string][]DBConfigRef{}
	ownerByDB := map[string]string{}        // db -> user, real config preferred
	passwordByDB := map[string]string{}     // db -> password, real config preferred
	ownerRegistry := map[string]string{}    // db -> user from registry (fallback)
	passwordRegistry := map[string]string{} // db -> password from registry (fallback)
	for _, sc := range creds {
		if sc.DBName == "" {
			continue
		}
		if sc.FromRegistry {
			if _, ok := ownerRegistry[sc.DBName]; !ok && sc.DBUser != "" {
				ownerRegistry[sc.DBName] = sc.DBUser
			}
			if _, ok := passwordRegistry[sc.DBName]; !ok && sc.DBPassword != "" {
				passwordRegistry[sc.DBName] = sc.DBPassword
			}
			continue // do NOT add the registry file to the rewrite list
		}
		configsByDB[sc.DBName] = append(configsByDB[sc.DBName], DBConfigRef{
			Docroot:     sc.Docroot,
			ConfigPath:  sc.ConfigPath,
			TablePrefix: sc.TablePrefix,
			Kind:        sc.Kind,
		})
		// First real config wins for owner/password (they agree for a shared DB).
		if _, ok := ownerByDB[sc.DBName]; !ok && sc.DBUser != "" {
			ownerByDB[sc.DBName] = sc.DBUser
		}
		if _, ok := passwordByDB[sc.DBName]; !ok && sc.DBPassword != "" {
			passwordByDB[sc.DBName] = sc.DBPassword
		}
	}
	// Fold registry credentials in as a fallback where no real config supplied them.
	for db, u := range ownerRegistry {
		if ownerByDB[db] == "" {
			ownerByDB[db] = u
		}
	}
	for db, p := range passwordRegistry {
		if passwordByDB[db] == "" {
			passwordByDB[db] = p
		}
	}

	// Fallback owner from the authoritative user list: the first user granted on
	// the database (used when no wp-config names a DB_USER, e.g. orphan).
	apiOwner := map[string]string{}
	for _, db := range dbs {
		if len(db.Users) > 0 {
			apiOwner[db.Database] = db.Users[0]
		}
	}
	// Some accounts report the mapping on the user side instead; merge it in.
	for _, u := range users {
		for _, d := range u.Databases {
			if _, ok := apiOwner[d]; !ok {
				apiOwner[d] = u.User
			}
		}
	}

	logx.Debug("BuildPlan: starting with %d source database(s), %d credential set(s) (real+registry), %d override(s)", len(dbs), len(creds), len(overrides))
	var plan []DBPlanItem
	for _, db := range dbs {
		owner := ownerByDB[db.Database]
		if owner == "" {
			owner = apiOwner[db.Database]
			if owner != "" {
				logx.Debug("BuildPlan %s: no config named a DB user; using the first API grantee %q as owner", db.Database, owner)
			}
		}
		if owner == "" {
			owner = db.Database // last resort: same-named user (cPanel default)
			// Warn (not Debug): this is a GUESS for the DB owner; if it is wrong the
			// grant step fails later with an error that does not mention the guess, so
			// surface the high-risk fallback at info level for correlation.
			logx.Warn("database %s: owner unknown — guessing the same-named user (high risk; the grant may fail if this is wrong)", db.Database)
		}
		password := passwordByDB[db.Database]

		// host.yaml override wins for owner/password.
		if ov, ok := overrides[db.Database]; ok {
			if ov.User != "" {
				owner = ov.User
			}
			if ov.Password != "" {
				password = ov.Password
			}
		}

		cfgs := configsByDB[db.Database]
		logx.Debug("BuildPlan %s: src_user=%s dest_user=%s, %d config(s) to rewrite, orphan=%v",
			db.Database, owner, remapName(owner, mapping), len(cfgs), len(cfgs) == 0)
		plan = append(plan, DBPlanItem{
			SrcDB:     db.Database,
			SrcUser:   owner,
			DestDB:    remapName(db.Database, mapping),
			DestUser:  remapName(owner, mapping),
			Password:  password,
			DiskUsage: int64(db.DiskUsage),
			Configs:   cfgs,
			Orphan:    len(cfgs) == 0,
		})
	}

	sort.SliceStable(plan, func(i, j int) bool { return plan[i].SrcDB < plan[j].SrcDB })
	orphanCount := 0
	for _, item := range plan {
		if item.Orphan {
			orphanCount++
		}
	}
	logx.Debug("BuildPlan complete: %d total database(s) (%d orphan, %d referenced by a config)", len(plan), orphanCount, len(plan)-orphanCount)
	return plan
}

// Override is a host.yaml credential override/fallback for one source database.
type Override struct {
	User     string
	Password string
}

// deriveSrcPrefix picks the account prefix shared by the source databases. It
// returns the prefix of the first database that has one ("" if none do).
func deriveSrcPrefix(dbs []cpanel.DatabaseEntry) string {
	for _, db := range dbs {
		if p := prefixOf(db.Database); p != "" {
			return p
		}
	}
	return ""
}
