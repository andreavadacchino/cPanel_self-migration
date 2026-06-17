package migrate

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/dbmig"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// destCred is the credential of a per-database destination user, captured during
// apply so the verify step (which has no other way to log into the destination
// MySQL) can reuse it.
type destCred struct{ user, pass string }

// applyDBs is the database migration step (apply only). For each source
// database: resolve the destination user's password (reuse the one from
// wp-config, or generate one for an orphan), create the destination user +
// database + grant (idempotent — "already exists" is tolerated because a
// migration overwrites), stream the data via the mysqldump|mysql bridge, then
// rewrite every referencing wp-config.php on the destination with the new
// prefixed name/user/password. Runs after applyWebFiles so the destination
// docroots (and their wp-config files) already exist. SOURCE stays read-only;
// all writes target the destination.
//
// It returns, keyed by destination database name, the per-database credentials
// it used, so verifyDBs can authenticate to the destination MySQL (the account
// user there has no direct login); the number of databases that FAILED to migrate;
// and the number that migrated but whose site config could NOT be rewritten (the site
// still points at the OLD database — an incomplete cutover that must make the run
// non-zero).
func applyDBs(ctx context.Context, pool *sshx.Pool, pd migrationData, log *logx.Logger, rep *report.Reporter, srcUser, srcPass string, overrides map[string]dbmig.Override, deep bool) (map[string]destCred, int, int, int, error) {
	plan := dbPlan(pd, overrides)
	creds := map[string]destCred{}

	// Preflight: refuse any plan collision that --apply would turn into data loss
	// (two source databases mapped to one destination database -> the second import
	// overwrites the first; a destination user shared with diverging passwords ->
	// configs left behind a wrong password). Validate ONLY the databases that will
	// actually be applied: a collision wholly among failed-domain-skipped databases
	// never touches the destination, so it must not fail a healthy partner. The
	// colliding items are failed below BEFORE any provision/empty/import.
	var live []dbmig.DBPlanItem
	for _, it := range plan {
		if skip, _ := dbAllDomainsUnavailableForApply(pd, it); !skip {
			live = append(live, it)
		}
	}
	conflicted := map[string]string{} // SrcDB -> reason
	for _, c := range dbmig.ValidatePlan(live) {
		log.Warn("unsafe DB plan: %s", c.Detail)
		for _, src := range c.SrcDBs {
			conflicted[src] = c.Detail
		}
	}
	invalidNames := map[string]string{} // SrcDB -> reason
	for _, v := range dbPlanNameViolations(pd, live) {
		log.Warn("unsafe DB name plan: %s", v.Detail)
		if invalidNames[v.SrcDB] == "" {
			invalidNames[v.SrcDB] = v.Detail
		} else {
			invalidNames[v.SrcDB] += "; " + v.Detail
		}
	}

	// Resolve the source dump command ONCE: add --set-gtid-purged=OFF only when the
	// SOURCE mysqldump supports it (MySQL). A GTID-enabled MySQL source otherwise emits
	// a SUPER-requiring `SET @@GLOBAL.GTID_PURGED` that the non-SUPER destination import
	// cannot run; MariaDB's mysqldump rejects the flag outright. The source is constant
	// for the run, so probe once — but only when at least one database will actually be
	// dumped (a live item that is neither a plan collision nor an invalid name; those
	// are failed below without ever dumping). A probe failure aborts here: guessing is
	// unsafe in both directions, and an unprobeable source cannot be dumped reliably.
	dumpCommand := dbmig.BuildDumpCmd(false)
	willDump := false
	for _, it := range live {
		if conflicted[it.SrcDB] == "" && invalidNames[it.SrcDB] == "" {
			willDump = true
			break
		}
	}
	if willDump {
		supportsGtidOff, err := dbmig.SrcSupportsGtidPurged(ctx, pool.Src)
		if err != nil {
			return creds, len(live), 0, 0, fmt.Errorf("probe source mysqldump capabilities: %w", err)
		}
		dumpCommand = dbmig.BuildDumpCmd(supportsGtidOff)
	}

	log.Step("Migrating databases (%d) ...", len(plan))
	rep.FileOnlyf("")
	rep.FileOnlyf("%s", report.DBHeaderLine())

	transfer := dbmig.Transfer{
		Src: pool.Src, Dest: pool.Dest,
		SrcUser: srcUser, SrcPass: srcPass,
		Timeout: sshx.DefaultStallTimeout, // per-attempt stall watchdog, matching the mail/web bridges
		DumpCmd: dumpCommand,
	}

	total := len(plan)
	// configUnrewritten counts databases whose DATA migrated but at least one
	// referencing site config could NOT be rewritten — the site still points at the
	// OLD database, so the cutover is incomplete. It is returned separately so the
	// final outcome is non-zero (a partial cutover must not read as a clean success).
	// configNotVerified counts configs the rewrite wrote and re-read but whose cutover
	// could not be INDEPENDENTLY verified (structurally ambiguous define — V35). At the
	// default tier this is a soft note (the data migrated; value/host checks passed), so
	// it is surfaced but NOT folded into the non-zero outcome; under --deep the ambiguous
	// configs are routed to configUnrewritten instead (a hard failure).
	var done, failed, skipped, configUnrewritten, configNotVerified int
	for i := range plan {
		it := plan[i]
		if ctx.Err() != nil {
			log.Warn("interrupted — %d of %d databases processed; stopping", i, total)
			rep.FileOnlyf("INTERRUPTED after %d/%d databases.", i, total)
			return creds, failed, configUnrewritten, 0, ctx.Err()
		}

		// Skip a database whose every referencing site lives on a domain that
		// failed creation or was blocked by a domain-creation preflight:
		// there is no working destination site to point it at.
		// A database shared with at least one healthy domain — and an orphan with
		// no referencing config at all — is still migrated.
		if skip, reason := dbAllDomainsUnavailableForApply(pd, it); skip {
			// Trace WHICH configs/domains drove the skip: a DB wrongly skipped (e.g. a
			// config path that didn't resolve to a docroot) otherwise leaves only a
			// one-line yellow note and no way to see why it was judged unreferenced.
			var cfgPaths []string
			for _, cfg := range it.Configs {
				cfgPaths = append(cfgPaths, cfg.ConfigPath)
			}
			logx.Debug("applyDBs %s: skipped — %s; referencing config(s): %v", it.SrcDB, reason, cfgPaths)
			if dbUnavailableReasonFailsDB(reason) {
				rep.LogScreenFile(itemStr(log, "✗", it.SrcDB, "%s", log.Red("FAIL — "+reason)),
					report.DBFailLine(it.SrcDB, reason))
				failed++
			} else {
				rep.LogScreenFile(itemStr(log, "→", it.SrcDB, "%s", log.Yellow("skip — "+reason)),
					report.DBSkipLine(it.SrcDB, reason))
				skipped++
			}
			continue
		}

		// Refuse a database that collides with another (same destination database, or a
		// shared destination user with diverging passwords) BEFORE any destructive op,
		// so we never overwrite one database with another or mis-provision a user. Both
		// sides of the collision are failed; the operator resolves the inventory and
		// re-runs.
		if reason, bad := conflicted[it.SrcDB]; bad {
			rep.LogScreenFile(itemStr(log, "✗", it.SrcDB, "%s", log.Red("FAIL — unsafe plan: "+reason)),
				report.DBFailLine(it.SrcDB, "unsafe plan collision: "+reason))
			failed++
			continue
		}
		if reason, bad := invalidNames[it.SrcDB]; bad {
			rep.LogScreenFile(itemStr(log, "✗", it.SrcDB, "%s", log.Red("FAIL — unsafe destination MySQL name: "+reason)),
				report.DBFailLine(it.SrcDB, "unsafe destination MySQL name: "+reason))
			failed++
			continue
		}

		// Resolve the destination user's password: reuse wp-config's, or generate
		// one for an orphan (no wp-config to read it from).
		password := it.Password
		if password == "" {
			gen, err := dbmig.GeneratePassword(dbmig.DefaultPasswordLen)
			if err != nil {
				rep.LogScreenFile(itemStr(log, "✗", it.SrcDB, "%s", log.Red("FAIL — generate password: "+err.Error())),
					report.DBFailLine(it.SrcDB, "generate password: "+err.Error()))
				failed++
				continue
			}
			password = gen
			logx.Debug("applyDBs %s: generated password for orphan (no wp-config)", it.SrcDB)
		}

		// 1) Create destination user + database + grant (idempotent).
		if err := provisionDest(ctx, pool.Dest, it, password); err != nil {
			if stopOnInterruptDuring(ctx, log, rep, it.SrcDB, "databases", i, total) {
				return creds, failed, configUnrewritten, 0, ctx.Err()
			}
			rep.LogScreenFile(itemStr(log, "✗", it.SrcDB, "%s", log.Red("FAIL — "+err.Error())),
				report.DBFailLine(it.SrcDB, err.Error()))
			failed++
			continue
		}
		if err := dbmig.EmptyDatabase(ctx, pool.Dest, it.DestDB, it.DestUser, password); err != nil {
			if stopOnInterruptDuring(ctx, log, rep, it.SrcDB, "databases", i, total) {
				return creds, failed, configUnrewritten, 0, ctx.Err()
			}
			rep.LogScreenFile(itemStr(log, "✗", it.SrcDB, "%s", log.Red("FAIL — "+err.Error())),
				report.DBFailLine(it.SrcDB, err.Error()))
			failed++
			continue
		}

		// 2) Stream the data (mysqldump | mysql). The destination import
		// authenticates as the per-database user just created (the destination
		// cPanel account user has no MySQL login), so pass it.DestUser + password.
		// The dump size is unknown up front (streamed mysqldump|mysql), so show a
		// live byte counter on the right of the row (not a percentage bar).
		prog := log.NewInlineBytesProgress(itemPrefix(log, "→", it.SrcDB))
		res, err := transfer.CopyDatabase(ctx, it, it.DestUser, password, prog.Add)
		if err != nil {
			if stopOnInterruptDuring(ctx, log, rep, it.SrcDB, "databases", i, total) {
				prog.Replace(itemStr(log, "→", it.SrcDB, "%s", log.Yellow("interrupted")))
				return creds, failed, configUnrewritten, 0, ctx.Err()
			}
			prog.Replace(itemStr(log, "✗", it.SrcDB, "%s", log.Red("FAIL — "+err.Error())))
			rep.FileOnlyf("%s", report.DBFailLine(it.SrcDB, err.Error()))
			failed++
			continue
		}

		// Record the credential for the verify step (which has no other way into the
		// destination MySQL) ONLY now that the data import has SUCCEEDED. Recording it
		// earlier left a credential behind for a database that failed to provision,
		// empty, or import — so verifyDBs would then try to verify a database that never
		// migrated and print contradictory output instead of a clean failed state.
		creds[it.DestDB] = destCred{user: it.DestUser, pass: password}

		// 2b) Normalize the destination DB's DEFAULT charset/collation to the source's,
		// so the verify's database-default check matches and a future table inherits the
		// source default. Best-effort and non-fatal: it changes only the schema default
		// (existing tables/data are untouched), and a cross-version collation the dest
		// server lacks is skipped (the verify then soft-classifies the residual
		// default-only diff). The source default is read read-only from the source.
		if srcCS, err := dbmig.GetCharsets(ctx, pool.Src, it.SrcDB, srcUser, srcPass); err != nil {
			logx.Debug("applyDBs %s: could not read source charset to normalize dest default (verify will report any diff): %v", it.SrcDB, err)
		} else if applied, reason, nerr := dbmig.NormalizeDBDefault(ctx, pool.Dest, it.DestDB, it.DestUser, password, srcCS.DBCharset, srcCS.DBCollation); nerr != nil {
			logx.Debug("applyDBs %s: normalize dest DB default failed (non-fatal; verify will report any diff): %v", it.DestDB, nerr)
		} else if applied {
			logx.Debug("applyDBs %s: normalized dest DB default to %s/%s", it.DestDB, srcCS.DBCharset, srcCS.DBCollation)
		} else {
			logx.Debug("applyDBs %s: dest DB default left as-is — %s", it.DestDB, reason)
		}

		// 3) Rewrite every referencing wp-config on the destination.
		nRewritten, nUnrewritten, nNotVerified := rewriteDestConfigs(ctx, pool.Dest, pd, it, password, deep, log, rep)

		// Count tables on the destination for the result line (best-effort),
		// using the per-database user's credentials.
		tables, errCount := dbmig.CountTables(ctx, pool.Dest, it.DestDB, it.DestUser, password)
		tableStr := fmt.Sprintf("%d tables", tables)
		if errCount != nil {
			// Don't print a misleading "0 tables" on a count failure — that is the
			// exact symptom of "my DB didn't migrate". Say so, and trace it; the
			// verify step re-checks authoritatively.
			tableStr = "table count unavailable"
			logx.Debug("applyDBs %s: post-import table count failed (verify will re-check): %v", it.DestDB, errCount)
		}
		// The data import succeeded (✓), but if a referencing config could not be
		// rewritten the site is NOT fully cut over — say so in yellow with the count,
		// never a clean green success, so a partial rewrite is not mistaken for done.
		verb := log.Green("migrated")
		extra := ""
		switch {
		case nUnrewritten > 0:
			verb = log.Yellow("migrated")
			extra = fmt.Sprintf(", %d NOT rewritten — see report", nUnrewritten)
		case nNotVerified > 0:
			// Data migrated and the value/host checks passed, but a config's cutover could
			// not be independently verified (structurally ambiguous define). Not a clean
			// green, not a hard failure at the default tier — yellow with the count.
			verb = log.Yellow("migrated")
			extra = fmt.Sprintf(", %d config(s) NOT independently verified — see report", nNotVerified)
		}
		prog.Replace(itemStr(log, "✓", it.SrcDB, "%s -> %s (%s, %s; %d config(s) rewritten%s)",
			verb, it.DestDB, tableStr, report.HumanBytes(res.BytesSent), nRewritten, extra))
		line, partial := dbResultLine(it.DestDB, tables, nUnrewritten, res.BytesSent, errCount == nil)
		rep.FileOnlyf("%s", line)
		if partial {
			// Data migrated, but the site still points at the OLD database: NOT a clean
			// success. Count it so the run ends non-zero (see applyOutcome).
			configUnrewritten++
		}
		configNotVerified += nNotVerified
		done++
	}

	rep.FileOnlyf("")
	rep.FileOnlyf("Database migration summary: %d migrated, %d failed, %d skipped.", done, failed, skipped)
	if configUnrewritten > 0 {
		rep.FileOnlyf("  %d database(s) migrated but their site config was NOT rewritten — the site still points at the OLD database.", configUnrewritten)
		log.Warn("%d database(s) migrated but their site config was NOT rewritten — the site still points at the OLD database; finish the cutover manually", configUnrewritten)
	}
	if configNotVerified > 0 {
		rep.FileOnlyf("  %d site config(s) were rewritten but their DB cutover could NOT be independently verified (structurally ambiguous) — run --deep-verify or confirm by hand.", configNotVerified)
		log.Warn("%d site config(s) rewritten but their DB cutover could NOT be independently verified (structurally ambiguous define) — run --deep-verify or confirm by hand", configNotVerified)
	}
	// Surface DB-config formats this tool does not discover/rewrite at all (Magento 1
	// local.xml, PrestaShop 1.7 parameters.php, Symfony DATABASE_URL, SilverStripe). Their
	// data may migrate, but the site keeps the SOURCE DB credentials, so they must never
	// read as a clean cutover: each is a MANUAL action returned in its OWN count (it is a
	// coverage gap, not a "migrated DB whose config wasn't rewritten") that the caller folds
	// into the non-zero outcome with an accurate message.
	unmigrated := reportUnmigratedDBConfigs(ctx, pool.Src, pd, log, rep)
	log.OK("database step done: %d migrated, %d failed, %d skipped", done, failed, skipped)
	return creds, failed, configUnrewritten, unmigrated, nil
}

// reportUnmigratedDBConfigs scans the source docroots (read-only) for recognized
// DB-config formats this tool does NOT discover/rewrite, and surfaces each as a loud
// MANUAL line + warn + a summary line. A docroot that already yielded a handled DB config
// is skipped (containment-aware, so a healthy install with a stray marker is not flagged).
// Returns the count, which the caller folds into the non-zero outcome under its own
// (accurate) bucket — a detected-but-unmigrated config is a worse omission than the
// recognized-but-unsupported MANUAL case (the config was never even discovered). A probe
// error is swallowed to 0 (debug-logged): detection is best-effort and must never abort the
// migration.
func reportUnmigratedDBConfigs(ctx context.Context, src *sshx.Client, pd migrationData, log *logx.Logger, rep *report.Reporter) int {
	if src == nil {
		return 0
	}
	// Build the handled set ONLY from real, on-disk configs that are rewrite targets —
	// exactly the ones BuildPlanWithMapping keeps (it skips FromRegistry). A Softaculous
	// registry entry carries a real softpath Docroot and a DBName but is a credential
	// fallback, never a rewrite target, so it must not mark its docroot "handled": a
	// coexisting Magento 1 / PrestaShop 1.7 / Symfony / SilverStripe config in that docroot
	// would otherwise be silently suppressed while still pointing at the OLD database.
	var handled []string
	for _, sc := range pd.SiteCreds {
		if sc.FromRegistry {
			continue
		}
		if sc.Docroot != "" && sc.DBName != "" {
			handled = append(handled, sc.Docroot)
		}
	}
	apps, err := dbmig.DetectUnmigratedConfigs(ctx, src, srcDocrootPaths(pd.SrcDocroots), handled)
	if err != nil {
		logx.Debug("reportUnmigratedDBConfigs: %v", err)
		return 0
	}
	for _, a := range apps {
		log.Warn("MANUAL: %s detected at %s but its DB config (%s) is NOT migrated/verified — review the DB credentials by hand (the site still points at the OLD database)", a.App, a.Docroot, a.Marker)
		rep.FileOnlyf("  [db config MANUAL] %s — %s detected (%s); its DB config is NOT migrated/verified, set the destination DB by hand", a.Docroot, a.App, a.Marker)
	}
	if len(apps) > 0 {
		rep.FileOnlyf("  %d site(s) use a DB-config format this tool does not migrate/verify (Magento 1 / PrestaShop 1.7 / Symfony / SilverStripe) — set their destination DB by hand.", len(apps))
	}
	return len(apps)
}

// dbResultLine builds the per-database result report line and reports whether the
// cutover is INCOMPLETE: when nUnrewritten > 0 the DATA migrated but at least one
// referencing site config could NOT be rewritten, so the site still points at the OLD
// database — a [db PARTIAL] line and partial=true (which the caller turns into a
// non-zero outcome), never a clean [db ok]. Pure; unit-tested.
func dbResultLine(destDB string, tables, nUnrewritten int, bytes int64, tablesKnown bool) (line string, partial bool) {
	if nUnrewritten > 0 {
		return report.DBPartialLine(destDB, tables, nUnrewritten, bytes, tablesKnown), true
	}
	return report.DBOKLine(destDB, tables, bytes, tablesKnown), false
}

// provisionDest ensures the destination user, database, and grant exist for one
// plan item, then leaves the data import (which overwrites the tables) to the
// caller. A migration OVERWRITES an existing destination, so re-running it must
// be idempotent.
//
// Crucially, it does NOT depend on the text of cPanel's "already exists" error,
// which is LOCALIZED (a destination cPanel localized in Polish answers "… już
// istnieje", not "already exists") — keying off that string is fragile and was
// the cause of a re-run failing on databases that already existed. Instead:
//
//   - create_user / create_database are treated as best-effort "ensure exists":
//     their failure is NOT fatal (the object most likely already exists). The
//     error is logged — at debug level when it looks like an "already exists"
//     condition (in any recognized language), at warn otherwise — and we proceed.
//   - set_password and set_privileges_on_database operate on the (new OR pre-
//     existing) object and DO carry the real outcome, so their failure is fatal.
//   - set_password additionally guarantees the (possibly pre-existing) user's
//     password matches what we write into the rewritten wp-config.
//
// If create_* failed for a REAL reason (not "exists"), the subsequent
// set_password / set_privileges — and ultimately the data import — fail and ARE
// surfaced, so a genuine problem is never silently swallowed.
// dest is taken as the cpanel.Runner interface (satisfied by *sshx.Client) so
// the provisioning sequence can be exercised in tests with a fake that returns
// canned UAPI JSON — in particular an "already exists" error in a locale NOT in
// the recognized list, to prove the migration proceeds regardless of language.
func provisionDest(ctx context.Context, dest cpanel.Runner, it dbmig.DBPlanItem, password string) error {
	if err := cpanel.CreateDBUser(ctx, dest, it.DestUser, password); err != nil {
		logProvisionStep("create user "+it.DestUser, err)
	}
	// Ensure the password matches the one we will write into wp-config, whether
	// the user was just created or already existed. This MUST succeed.
	if err := cpanel.SetDBUserPassword(ctx, dest, it.DestUser, password); err != nil {
		return err
	}
	if err := cpanel.CreateDatabase(ctx, dest, it.DestDB); err != nil {
		logProvisionStep("create database "+it.DestDB, err)
	}
	// The grant MUST succeed (it binds the user to the database, new or existing).
	if err := cpanel.SetPrivilegesOnDatabase(ctx, dest, it.DestUser, it.DestDB); err != nil {
		return err
	}
	return nil
}

// logProvisionStep records a non-fatal create_user/create_database outcome: an
// "already exists" condition (recognized across locales) is normal on a re-run
// and only traced; anything else is a warning (but still non-fatal — the import
// that follows decides the real success).
func logProvisionStep(what string, err error) {
	if isAlreadyExists(err) {
		logx.Debug("provisionDest: %s — already exists, continuing: %v", what, err)
		return
	}
	logx.Debug("provisionDest: %s — non-fatal error, continuing (the import will surface a real failure): %v", what, err)
}

// rewriteDestConfigs rewrites, on the destination, each wp-config.php that the
// source plan item referenced — mapping the source config path to its destination
// path via the matched docroots. It returns how many configs were rewritten and
// how many were NOT (could not be resolved, unsupported, or errored). The data
// import is independent, so a config that cannot be rewritten does not abort the
// DB — but it MUST be surfaced (loud warn + report line), never left silent, or the
// site stays pointed at the old database while the run reports a clean success.
func rewriteDestConfigs(ctx context.Context, dest *sshx.Client, pd migrationData, it dbmig.DBPlanItem, password string, deep bool, log *logx.Logger, rep *report.Reporter) (rewritten, notRewritten, notVerified int) {
	// Configs in THIS DB's own rewrite plan are each independently rewritten + certified,
	// so a stale source name the containment scan finds in a SIBLING (e.g. a docroot's
	// wp-config.php plus a test/wp-config.php both on this DB) is transient ordering, not
	// an un-acted-on live value. Resolve their destination paths up front so the scan
	// ignores them (a sibling that genuinely could not be rewritten is already counted by
	// its own iteration).
	var plannedDestConfigs []string
	for _, c := range it.Configs {
		if se, ok := srcDocrootContaining(pd, c.ConfigPath); ok {
			if dd, issue := destDocrootForChecked(pd, se.Domain); issue == "" && dd != "" {
				plannedDestConfigs = append(plannedDestConfigs, dbmig.MapConfigPath(c.ConfigPath, se.DocumentRoot, dd))
			}
		}
	}
	for _, cfg := range it.Configs {
		// Find the source docroot that contains this config, then its destination
		// docroot, to build the destination config path. If either cannot be
		// resolved the config still references THIS database, so the site would be
		// left on the old DB — surface it as a MANUAL step instead of dropping it.
		srcEntry, ok := srcDocrootContaining(pd, cfg.ConfigPath)
		if !ok {
			log.Warn("MANUAL: config %s (db %s) NOT rewritten — no source docroot contains it; point it at db=%s user=%s by hand",
				cfg.ConfigPath, it.SrcDB, it.DestDB, it.DestUser)
			rep.FileOnlyf("  [db config MANUAL] %s — could not locate its docroot; set DB to %s / user %s by hand (was %s)",
				cfg.ConfigPath, it.DestDB, it.DestUser, it.SrcDB)
			notRewritten++
			continue
		}
		if reason, blocked := domainBlocked(pd, srcEntry.Domain); blocked {
			log.Warn("MANUAL: config %s (db %s) NOT rewritten — %s; point it at db=%s user=%s by hand",
				cfg.ConfigPath, it.SrcDB, reason, it.DestDB, it.DestUser)
			rep.FileOnlyf("  [db config MANUAL] %s — %s; set DB to %s / user %s by hand (was %s)",
				cfg.ConfigPath, reason, it.DestDB, it.DestUser, it.SrcDB)
			notRewritten++
			continue
		}
		if issue, blocked := domainTypeIssue(pd, srcEntry.Domain); blocked && issue.BlockDBConfig {
			log.Warn("MANUAL: config %s (db %s) NOT rewritten — %s; point it at db=%s user=%s by hand",
				cfg.ConfigPath, it.SrcDB, issue.Reason(), it.DestDB, it.DestUser)
			rep.FileOnlyf("  [db config MANUAL] %s — %s; set DB to %s / user %s by hand (was %s)",
				cfg.ConfigPath, issue.Reason(), it.DestDB, it.DestUser, it.SrcDB)
			notRewritten++
			continue
		}
		destDocroot, docrootIssue := destDocrootForChecked(pd, srcEntry.Domain)
		if docrootIssue != "" {
			log.Warn("MANUAL: config %s (db %s) NOT rewritten — %s; point it at db=%s user=%s by hand",
				cfg.ConfigPath, it.SrcDB, docrootIssue, it.DestDB, it.DestUser)
			rep.FileOnlyf("  [db config MANUAL] %s — %s; set DB to %s / user %s by hand (was %s)",
				cfg.ConfigPath, docrootIssue, it.DestDB, it.DestUser, it.SrcDB)
			notRewritten++
			continue
		}
		if destDocroot == "" {
			log.Warn("MANUAL: config %s (db %s) NOT rewritten — domain %s has no destination docroot; point it at db=%s user=%s by hand",
				cfg.ConfigPath, it.SrcDB, srcEntry.Domain, it.DestDB, it.DestUser)
			rep.FileOnlyf("  [db config MANUAL] %s — domain %s has no destination docroot; set DB to %s / user %s by hand (was %s)",
				cfg.ConfigPath, srcEntry.Domain, it.DestDB, it.DestUser, it.SrcDB)
			notRewritten++
			continue
		}
		destPath := dbmig.MapConfigPath(cfg.ConfigPath, srcEntry.DocumentRoot, destDocroot)
		if err := dbmig.RewriteSiteConfig(ctx, dest, destPath, cfg.Kind, it.DestDB, it.DestUser, password); err != nil {
			var ue *dbmig.UnsupportedRewriteError
			if errors.As(err, &ue) {
				// The data migrated, but this CMS's config rewriter is not implemented
				// yet: make it a loud, actionable MANUAL step, not a buried warning, so
				// the site is not silently left pointing at the old database.
				log.Warn("MANUAL: %s config %s NOT rewritten — point it at db=%s user=%s by hand (site still uses the old %s)",
					ue.Kind, destPath, it.DestDB, it.DestUser, it.SrcDB)
				rep.FileOnlyf("  [db config MANUAL] %s (%s) — set DB to %s / user %s by hand (was %s)",
					destPath, ue.Kind, it.DestDB, it.DestUser, it.SrcDB)
				notRewritten++
				continue
			}
			log.Warn("could not rewrite %s: %v", destPath, err)
			rep.FileOnlyf("  [db config WARN] %s — %v", destPath, err)
			notRewritten++
			continue
		}
		// Re-read the just-written destination config and confirm the site actually
		// points at the destination database (planned name/user/password AND a LOCAL DB
		// host). DB_HOST is never rewritten, so a source config that used a remote DB
		// host is still pointed there — name/user/password correct, but the site cannot
		// reach the destination MySQL. Demote such a config to NOT rewritten so the run
		// ends non-zero instead of a green [db ok].
		ok, reason, unverified, rerr := dbmig.VerifyDestConfig(ctx, dest, destPath, cfg.Kind, it.DestDB, it.DestUser, password)
		if rerr != nil {
			log.Warn("MANUAL: %s rewritten but could not be re-read to verify the cutover: %v", destPath, rerr)
			rep.FileOnlyf("  [db config WARN] %s — rewritten but re-read to verify the cutover failed: %v", destPath, rerr)
			notRewritten++
			continue
		}
		// Layer the source-cred containment scan on a config that otherwise verified:
		// if the SOURCE db name/user is still reachable in the destination docroot, the
		// value PHP actually uses may not be where the rewrite acted (a split config via
		// include()/require(), a Laravel config:cache shadow, a Drupal settings.local.php
		// override, or a second un-rewritten definition — the V35 include/runtime
		// residual). Demote to UNVERIFIED via the same tier policy below; it is
		// evidence-based and demote-only, so it can never certify a cutover green. A scan
		// error is best-effort: keep the existing verdict rather than abort.
		if ok {
			// Ignore the OTHER planned sibling configs on this DB, but NOT destPath itself:
			// a stale source name found in the file we just rewrote is a genuine
			// second-definition residual in THAT file and must still demote; a hit in a
			// not-yet-rewritten sibling is transient ordering, already certified on its turn.
			ignoreSiblings := make([]string, 0, len(plannedDestConfigs))
			for _, p := range plannedDestConfigs {
				if p != destPath {
					ignoreSiblings = append(ignoreSiblings, p)
				}
			}
			if leak, lreason, lerr := dbmig.SourceCredsStillReachable(ctx, dest, destDocroot, it.SrcDB, it.DestDB, it.SrcUser, it.DestUser, ignoreSiblings); lerr != nil {
				// Best-effort: a scan error/timeout does NOT fail the cutover (the value/host
				// check already passed and the scan is an additive net) — but surface it so a
				// non-completing scan is not silent, rather than hiding it at debug level.
				log.Detail("source-cred containment scan of %s did not complete (%v) — cutover not re-scanned for stale source creds", destDocroot, lerr)
				rep.FileOnlyf("  [db config note] %s — source-cred containment scan did not complete (%v); cutover not re-scanned for stale source creds", destDocroot, lerr)
			} else if leak {
				ok, unverified, reason = false, true, lreason
			}
		}
		if !ok && unverified {
			// The value/host checks passed, but a structurally-different second opinion
			// cannot PROVE the rewrite acted on the value PHP resolves (the constant is
			// defined more than once, the live definition is a non-literal expression, or a
			// heredoc/string decoy define was edited instead of the live one — finding V35).
			// Tier policy: a SOFT "not independently verified" note at the default tier (the
			// data migrated and the value/host checks held), a HARD failure under
			// --deep-verify (which must not green-light an unproven cutover).
			if deep {
				log.Warn("MANUAL: %s rewritten but its DB cutover could NOT be independently verified (%s) — confirm it by hand (--deep-verify)", destPath, reason)
				rep.FileOnlyf("  [db config UNVERIFIED] %s — %s; cutover not provable (--deep-verify)", destPath, reason)
				notRewritten++
				continue
			}
			log.Detail("%s rewritten; DB cutover NOT independently verified (%s) — run --deep-verify to certify", destPath, reason)
			rep.FileOnlyf("%s", report.DBConfigUnverifiedLine(it.DestDB, destPath, reason))
			notVerified++
			continue
		}
		if !ok {
			log.Warn("MANUAL: %s does NOT point at the destination database after rewrite (%s) — fix it by hand", destPath, reason)
			rep.FileOnlyf("  [db config MANUAL] %s — does not point at the destination DB after rewrite: %s", destPath, reason)
			notRewritten++
			continue
		}
		rep.FileOnlyf("%s", report.DBConfigLine(it.DestDB, destPath))
		rewritten++
	}
	return rewritten, notRewritten, notVerified
}

// verifyDBs re-counts base tables on the DESTINATION (read-only) and compares
// against the source for each migrated database, reporting a match verdict. It
// mirrors the mailbox and web-file integrity checks. destCreds carries the
// per-database destination credentials captured during apply (the destination
// account user cannot log into MySQL directly); a database absent from the map
// (e.g. its apply failed) is reported as unverifiable.
// It returns the number of REAL verify divergences (databases that have a
// captured credential but whose table/object counts differ) so runApply can fail
// the run. The UNVERIFIED case (no credential — the DB's migration did not
// complete) is NOT counted here: it is already reflected in the database/domain
// failure tallies, so counting it again would double-report the same problem.
func verifyDBs(ctx context.Context, pool *sshx.Pool, pd migrationData, log *logx.Logger, rep *report.Reporter, srcUser, srcPass string, overrides map[string]dbmig.Override, destCreds map[string]destCred, deep bool) (int, error) {
	plan := dbPlan(pd, overrides)

	title := "tables + objects + encoding + row counts + same-version content checksum + object definitions"
	if deep {
		title = "deep: tables + objects + encoding + row counts + content checksum + object definitions (fail-closed if unprovable)"
	}
	log.Step("Verifying databases (%s) ...", title)
	rep.FileOnlyf("")
	rep.FileOnlyf("=== --apply: verifying databases (%s) ===", title)

	var ok, diff, realDiff, eventsOnDest int
	for _, it := range plan {
		if ctx.Err() != nil {
			log.Warn("interrupted — database verification stopped")
			return realDiff, ctx.Err()
		}
		// A database whose every referencing domain is unavailable was SKIPPED (or
		// FAILED) during apply and has no destination to verify; mirror that
		// classification here so it is reported as a SKIP — its root cause is already
		// counted under apply (skipped/failed/FailedDomains) — instead of being
		// mislabeled UNVERIFIED with generic "migration did not complete" re-run advice.
		if skip, reason := dbAllDomainsUnavailableForApply(pd, it); skip {
			rep.LogScreenFile(
				itemStr(log, "→", it.DestDB, "%s — %s", log.Yellow("SKIP"), reason),
				report.DBSkipLine(it.DestDB, reason+"; counted under apply"))
			continue
		}
		dc, haveCred := destCreds[it.DestDB]
		if !haveCred {
			rep.LogScreenFile(
				itemStr(log, "~", it.DestDB, "%s (migration did not complete)", log.Yellow("UNVERIFIED")),
				report.DBVerifyLine(it.DestDB, report.DBVerifyUnverified, 0, 0, "", ""))
			diff++ // shown in the summary, but already counted by dbFailed/FailedDomains
			continue
		}
		// Per-database verify runs several phases (schema, encoding, row counts +
		// checksums); show a live row so a DB with many/large tables shows WHICH phase
		// is running instead of an idle wait. Cleared (Finish) before the verdict line,
		// so the result branches below are untouched; inert on a non-TTY / --log-level
		// debug run (liveProgress off), like the copy step's bars.
		prog := inlineRow(log, "→", it.DestDB, 0, "")
		prog.SetSuffix("reading schema")
		srcSchema, errS := dbmig.GetSchemaFingerprint(ctx, pool.Src, it.SrcDB, srcUser, srcPass)
		destSchema, errD := dbmig.GetSchemaFingerprint(ctx, pool.Dest, it.DestDB, dc.user, dc.pass)
		var srcTables, destTables int
		var srcObj, destObj dbmig.ObjectCounts
		var schemaDelta dbSchemaDiff
		if errS == nil {
			srcTables = len(srcSchema.Tables)
			srcObj = srcSchema.ObjectCounts()
		}
		if errD == nil {
			destTables = len(destSchema.Tables)
			destObj = destSchema.ObjectCounts()
			eventsOnDest += destObj.Events
		}
		if errS == nil && errD == nil {
			schemaDelta = diffSchema(srcSchema, destSchema)
		}

		// Charset/collation fingerprint: catches a wrong-encoding import (mojibake)
		// that the table/object counts are blind to. Encoding is part of the default
		// verification contract ("tables + objects + encoding"), so an unreadable
		// charset on either side is NOT a clean pass: it is reported UNVERIFIED (fail
		// closed) below rather than silently folded into an OK verdict.
		prog.SetSuffix("reading encoding")
		srcCS, errSC := dbmig.GetCharsets(ctx, pool.Src, it.SrcDB, srcUser, srcPass)
		destCS, errDC := dbmig.GetCharsets(ctx, pool.Dest, it.DestDB, dc.user, dc.pass)
		csComparable := errSC == nil && errDC == nil
		csUnverified := !csComparable
		var csDBDiff bool
		var csTableDiffs []string
		if csComparable {
			csDBDiff, csTableDiffs = diffCharsets(srcCS, destCS)
		}
		// csOK = clean encoding match. csDBDefaultOnly = only the schema DEFAULT
		// charset/collation differs while every table's collation matches — a SOFT
		// advisory, not a mojibake DIFF (the default governs only future tables; it
		// touches no migrated byte). The migration already tries to normalize the dest
		// default at apply time; this covers the residual cross-version case where the
		// source collation does not exist on the destination server. A real wrong-encoding
		// import surfaces as a table-collation/row-count/checksum divergence, which keep
		// match=false below. See csVerdict.
		csOK, csDBDefaultOnly := csVerdict(csComparable, csDBDiff, len(csTableDiffs))

		match, readErr := dbVerdict(srcTables, destTables, srcObj, destObj, schemaDelta, errS, errD)
		match = match && (csOK || csDBDefaultOnly)

		// Per-table ROW COUNTS + table set + same-version CONTENT CHECKSUM are part of the
		// DEFAULT verify: a partial/empty import (rows missing) or a same-size content
		// corruption must fail closed. Exact COUNT(*) is the engine-independent content-loss
		// signal; the per-table CHECKSUM TABLE (run only for equal-row tables on an IDENTICAL
		// server version) catches a same-count byte divergence. Missing/extra tables,
		// row-count drift, a content-checksum mismatch, or an unreadable row-count read all
		// fail in BOTH tiers. Content that CANNOT be byte-proven (server versions differ, or a
		// NULL/failed checksum) is ContentUnchecked: a soft note at the default tier (row
		// counts already matched), escalated to a fail (UNVERIFIED) only under --deep-verify.
		// AUTO_INCREMENT drift stays informational. Like the mailbox verify, a row shortfall on
		// a LIVE source (writes after the dump snapshot) is a real divergence the operator
		// resolves with a re-run / final sync — it errs toward flagging, never toward a false OK.
		var deepRes deepDBResult
		deepDone := false
		deepUnverified := false
		if !readErr {
			prog.SetSuffix("row counts + checksums")
			deepRes, deepDone = deepDB(ctx, pool, it, srcUser, srcPass, dc)
			deepUnverified = !deepDone || (deep && deepRes.ContentUnchecked)
		}
		if !readErr && (!deepDone || deepRes.HardDiff() || (deep && deepRes.ContentUnchecked)) {
			match = false
		}

		// Mirror the mailbox verify (verify.go): trace the counts AND every read
		// error at debug, so an operator can tell "the databases genuinely differ"
		// from "I couldn't read one side" — the two otherwise render identically as
		// SRC(0)/DEST(0) with no clue which it was.
		logx.Debug("verify db %s -> %s: src tables=%d dest tables=%d (errS=%v errD=%v); srcObj=%v destObj=%v schemaDiff=%s; csComparable=%v dbCharsetDiff=%v tableCollDiffs=%d",
			it.SrcDB, it.DestDB, srcTables, destTables, errS, errD, srcObj, destObj, schemaDelta.Headline(), csComparable, csDBDiff, len(csTableDiffs))

		// Only render object detail when there is something to show (some object
		// exists on either side); the common no-objects DB keeps the plain line.
		srcObjStr, destObjStr := "", ""
		if errS == nil && errD == nil && (srcObj.Total() > 0 || destObj.Total() > 0) {
			srcObjStr, destObjStr = objCountsStr(srcObj), objCountsStr(destObj)
		}

		prog.Finish() // clear the progress row; the verdict line below prints in its place
		if readErr {
			// A count could not be read on at least one side. Report UNREADABLE
			// distinctly instead of a bogus "DIFF — SRC(tables=0) DEST(tables=0)"
			// that looks like both databases are genuinely empty. Still counted as a
			// real divergence (an unverifiable DB is not a pass), matching how the
			// mailbox verify treats its UNREADABLE verdict.
			rep.LogScreenFile(
				itemStr(log, "~", it.DestDB, "%s — could not read %s table count",
					log.Red("UNREADABLE"), unreadableSide(errS, errD)),
				report.DBVerifyLine(it.DestDB, report.DBVerifyUnreadable, srcTables, destTables, srcObjStr, destObjStr))
			diff++
			realDiff++
			continue
		}

		if match {
			screen := itemStr(log, "✓", it.DestDB, "%s (tables=%d)", log.Green("OK"), srcTables)
			if srcObjStr != "" {
				screen = itemStr(log, "✓", it.DestDB, "%s (tables=%d, %s)", log.Green("OK"), srcTables, srcObjStr)
			}
			rep.LogScreenFile(screen, report.DBVerifyLine(it.DestDB, report.DBVerifyOK, srcTables, destTables, srcObjStr, destObjStr))
			if csDBDefaultOnly {
				// Surface the cosmetic default-collation difference (the migration could not
				// normalize it — the source collation does not exist on the destination
				// server) WITHOUT failing the verdict: existing tables/data are unaffected.
				log.Detail("%s: DB default collation differs (%s) — cosmetic: affects only new tables created without an explicit COLLATE; existing tables/data unaffected",
					it.DestDB, charsetHeadline(srcCS, destCS, csDBDiff, len(csTableDiffs)))
				rep.FileOnlyf("      encoding (soft): %s", charsetDetail(srcCS, destCS, csDBDiff, csTableDiffs))
			}
			ok++
		} else {
			// Distinguish WHAT diverged. When tables/objects match, the divergence is
			// lost rows (deep) or a re-encoding (mojibake) — "SRC(tables=N) DEST(tables=N)"
			// would read as a contradiction, so name the real cause.
			schemaOK := schemaDelta.Empty()
			var screen string
			// The persisted status mirrors the on-screen verdict: a genuine divergence
			// is DIFF, but the deep-content and charset legs that could not be PROVEN are
			// UNVERIFIED (set in their cases below), so the report file no longer collapses
			// them all to DIFF.
			status := report.DBVerifyDiff
			switch {
			case !schemaOK:
				screen = itemStr(log, "~", it.DestDB, "%s — schema differs: %s",
					log.Red("DIFF"), schemaDelta.Headline())
			case srcTables != destTables:
				screen = itemStr(log, "~", it.DestDB, "%s — SRC(tables=%d) DEST(tables=%d)", log.Red("DIFF"), srcTables, destTables)
			case len(deepRes.MissingTables) > 0 || len(deepRes.ExtraTables) > 0:
				screen = itemStr(log, "~", it.DestDB, "%s — table set differs: %s",
					log.Red("DIFF"), deepRes.TableSetHeadline())
			case len(deepRes.RowDiffs) > 0: // tables/objects equal — rows were lost
				screen = itemStr(log, "~", it.DestDB, "%s — row counts differ: %s",
					log.Red("DIFF"), strings.Join(firstN(deepRes.RowDiffs, 3), ", "))
			case len(deepRes.ChecksumDiffs) > 0:
				screen = itemStr(log, "~", it.DestDB, "%s — content checksums differ: %s",
					log.Red("DIFF"), strings.Join(firstN(deepRes.ChecksumDiffs, 3), ", "))
			case len(deepRes.ObjectDiffs) > 0:
				screen = itemStr(log, "~", it.DestDB, "%s — object definition differs: %s",
					log.Red("DIFF"), strings.Join(firstN(deepRes.ObjectDiffs, 3), ", "))
			case deepUnverified:
				status = report.DBVerifyUnverified
				screen = itemStr(log, "~", it.DestDB, "%s — row-count/content check incomplete: %s",
					log.Red("UNVERIFIED"), deepRes.UnverifiedReason())
			case csUnverified:
				status = report.DBVerifyUnverified
				// Charset metadata could not be read on at least one side — the encoding
				// leg of the verdict is unproven. Report UNVERIFIED, never a mojibake DIFF
				// (we observed no divergence) and never a clean OK.
				screen = itemStr(log, "~", it.DestDB, "%s — encoding could not be read on %s",
					log.Red("UNVERIFIED"), unreadableSide(errSC, errDC))
			default: // tables/objects equal — the divergence is the encoding
				screen = itemStr(log, "~", it.DestDB, "%s — encoding differs (mojibake risk): %s",
					log.Red("DIFF"), charsetHeadline(srcCS, destCS, csDBDiff, len(csTableDiffs)))
			}
			rep.LogScreenFile(screen, report.DBVerifyLine(it.DestDB, status, srcTables, destTables, srcObjStr, destObjStr))
			if !schemaOK {
				rep.FileOnlyf("      schema: %s", schemaDelta.Detail())
			}
			if csUnverified {
				rep.FileOnlyf("      encoding UNVERIFIED: could not read charsets on %s", unreadableSide(errSC, errDC))
			} else if csDBDiff || len(csTableDiffs) > 0 {
				rep.FileOnlyf("      encoding: %s", charsetDetail(srcCS, destCS, csDBDiff, csTableDiffs))
			}
			diff++
			realDiff++ // a genuine post-migration mismatch (credential was captured)
		}

		// Row-count + deep detail, for both OK and DIFF: table-set divergences, row-count
		// divergences, hard same-version checksum differences (deep), content-unverified
		// downgrades (deep), and informational AUTO_INCREMENT drift.
		if deepDone {
			if len(deepRes.MissingTables) > 0 {
				rep.FileOnlyf("      missing tables: %s", strings.Join(firstN(deepRes.MissingTables, 8), ", "))
			}
			if len(deepRes.ExtraTables) > 0 {
				rep.FileOnlyf("      extra tables: %s", strings.Join(firstN(deepRes.ExtraTables, 8), ", "))
			}
			for _, rd := range deepRes.RowDiffs {
				rep.FileOnlyf("      rows: %s", rd)
			}
			if len(deepRes.RowDiffs) > 0 {
				rep.FileOnlyf("      NOTE: fewer rows on the destination means a partial import OR a source still receiving writes after the dump snapshot — re-run --apply --db to re-copy (as the final sync shortly before cutover if the source is live).")
			}
			if len(deepRes.ChecksumDiffs) > 0 {
				log.Detail("%s: %d table(s) with equal row count but different content checksum (same server version)",
					it.DestDB, len(deepRes.ChecksumDiffs))
				rep.FileOnlyf("      content: %d table(s) checksum differs (same version): %s",
					len(deepRes.ChecksumDiffs), strings.Join(firstN(deepRes.ChecksumDiffs, 8), ", "))
			}
			if len(deepRes.ObjectDiffs) > 0 {
				rep.FileOnlyf("      object definitions differ (same version): %s",
					strings.Join(firstN(deepRes.ObjectDiffs, 8), ", "))
			}
			if deepRes.ContentUnchecked {
				if deep {
					rep.FileOnlyf("      content UNVERIFIED: %s", deepRes.UnverifiedReason())
				} else {
					rep.FileOnlyf("      content not byte-verified (%s); row counts match — run --deep-verify to certify", deepRes.UnverifiedReason())
				}
			}
			if len(deepRes.AutoIncrDiffs) > 0 {
				rep.FileOnlyf("      AUTO_INCREMENT (informational): %s", strings.Join(firstN(deepRes.AutoIncrDiffs, 8), ", "))
			}
		} else if !readErr {
			rep.FileOnlyf("      UNVERIFIED: row-count fingerprint could not be read")
		}
	}

	rep.FileOnlyf("")
	rep.FileOnlyf("Database integrity check: %d consistent, %d divergent.", ok, diff)
	if diff == 0 {
		log.OK("database integrity check passed: %d database(s) consistent", ok)
	} else {
		log.Warn("%d database(s) differ — re-run --apply --db to re-copy", diff)
		rep.FileOnlyf("  re-run --apply --db to re-copy the divergent databases.")
	}
	// Events are imported but only RUN when the destination MySQL has
	// event_scheduler=ON — a global a non-root cPanel user cannot set. Flag it so
	// the operator (or host admin) enables it; this is operational, not a failure.
	if eventsOnDest > 0 {
		log.Detail("%d event(s) migrated — ensure event_scheduler=ON on the destination MySQL for them to run", eventsOnDest)
		rep.FileOnlyf("  NOTE: %d event(s) migrated — the destination MySQL needs event_scheduler=ON for them to run.", eventsOnDest)
	}
	return realDiff, nil
}

// dbSchemaDiff is the exact named-object drift between source and destination.
// It exists because counts can lie: {a,b} and {a,x} both have two tables.
type dbSchemaDiff struct {
	MissingTables, ExtraTables     []string
	MissingViews, ExtraViews       []string
	MissingTriggers, ExtraTriggers []string
	MissingRoutines, ExtraRoutines []string
	MissingEvents, ExtraEvents     []string
}

func diffSchema(src, dest dbmig.SchemaFingerprint) dbSchemaDiff {
	return dbSchemaDiff{
		MissingTables:   missingNames(src.Tables, dest.Tables),
		ExtraTables:     missingNames(dest.Tables, src.Tables),
		MissingViews:    missingNames(src.Views, dest.Views),
		ExtraViews:      missingNames(dest.Views, src.Views),
		MissingTriggers: missingNames(src.Triggers, dest.Triggers),
		ExtraTriggers:   missingNames(dest.Triggers, src.Triggers),
		MissingRoutines: missingNames(src.Routines, dest.Routines),
		ExtraRoutines:   missingNames(dest.Routines, src.Routines),
		MissingEvents:   missingNames(src.Events, dest.Events),
		ExtraEvents:     missingNames(dest.Events, src.Events),
	}
}

func missingNames(want, have map[string]struct{}) []string {
	var out []string
	for n := range want {
		if _, ok := have[n]; !ok {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (d dbSchemaDiff) Empty() bool {
	return len(d.MissingTables)+len(d.ExtraTables)+
		len(d.MissingViews)+len(d.ExtraViews)+
		len(d.MissingTriggers)+len(d.ExtraTriggers)+
		len(d.MissingRoutines)+len(d.ExtraRoutines)+
		len(d.MissingEvents)+len(d.ExtraEvents) == 0
}

func (d dbSchemaDiff) Headline() string {
	if d.Empty() {
		return "none"
	}
	for _, p := range d.parts(2) {
		return p
	}
	return "object sets differ"
}

func (d dbSchemaDiff) Detail() string {
	if d.Empty() {
		return "none"
	}
	return strings.Join(d.parts(8), "; ")
}

func (d dbSchemaDiff) parts(limit int) []string {
	var parts []string
	add := func(label string, names []string) {
		if len(names) == 0 {
			return
		}
		suffix := ""
		if len(names) > limit {
			suffix = fmt.Sprintf(" (+%d more)", len(names)-limit)
		}
		parts = append(parts, fmt.Sprintf("%s: %s%s", label, strings.Join(firstN(names, limit), ", "), suffix))
	}
	add("missing tables", d.MissingTables)
	add("extra tables", d.ExtraTables)
	add("missing views", d.MissingViews)
	add("extra views", d.ExtraViews)
	add("missing triggers", d.MissingTriggers)
	add("extra triggers", d.ExtraTriggers)
	add("missing routines", d.MissingRoutines)
	add("extra routines", d.ExtraRoutines)
	add("missing events", d.MissingEvents)
	add("extra events", d.ExtraEvents)
	return parts
}

// dbVerdict decides a database verify outcome from exact schema-object sets and
// read errors. Pure, so it is unit-tested. readErr is true when the schema could
// not be read on either side — reported distinctly as UNREADABLE rather than a
// misleading 0/0 "divergence". Counts are retained as report summaries.
func dbVerdict(srcTables, destTables int, srcObj, destObj dbmig.ObjectCounts, schemaDelta dbSchemaDiff, errS, errD error) (match, readErr bool) {
	if errS != nil || errD != nil {
		return false, true
	}
	match = srcTables == destTables && srcObj == destObj && schemaDelta.Empty()
	return match, false
}

// csVerdict classifies a charset comparison. csOK is a clean match (no diff).
// csDBDefaultOnly is the SOFT case: ONLY the schema DEFAULT charset/collation
// differs while every common table's collation matches (nTableDiffs==0) — the
// default governs only future tables created without an explicit COLLATE, so it
// touches no migrated byte and is an advisory, not a failure. A table-collation
// diff (nTableDiffs>0) or an unreadable side (!comparable) yields neither, so it
// stays a hard DIFF / UNVERIFIED. Pure; unit-tested.
func csVerdict(comparable, dbDiff bool, nTableDiffs int) (csOK, csDBDefaultOnly bool) {
	csOK = comparable && !dbDiff && nTableDiffs == 0
	csDBDefaultOnly = comparable && dbDiff && nTableDiffs == 0
	return
}

// diffCharsets compares two database charset fingerprints. dbDiff is set when the
// schema default charset or collation differs; tableDiffs names each base table
// (present on both sides) whose collation differs, as "name (src->dest)", sorted.
// A table missing on one side is NOT reported here — the table-count verdict
// already catches that. Pure, so it is unit-tested.
func diffCharsets(src, dest dbmig.CharsetInfo) (dbDiff bool, tableDiffs []string) {
	dbDiff = src.DBCharset != dest.DBCharset || src.DBCollation != dest.DBCollation
	for name, sc := range src.Tables {
		if dc, ok := dest.Tables[name]; ok && sc != dc {
			tableDiffs = append(tableDiffs, fmt.Sprintf("%s (%s->%s)", name, sc, dc))
		}
	}
	sort.Strings(tableDiffs)
	return dbDiff, tableDiffs
}

// charsetHeadline renders the one-line on-screen encoding-divergence cause: the
// schema default change if any, else the count of re-collated tables. Pure.
func charsetHeadline(src, dest dbmig.CharsetInfo, dbDiff bool, nTables int) string {
	if dbDiff {
		return fmt.Sprintf("db %s/%s -> %s/%s", src.DBCharset, src.DBCollation, dest.DBCharset, dest.DBCollation)
	}
	return fmt.Sprintf("%d table(s) re-collated", nTables)
}

// charsetDetail renders the full encoding divergence for the report file: the
// schema default change (if any) and the per-table collation changes. Pure.
func charsetDetail(src, dest dbmig.CharsetInfo, dbDiff bool, tableDiffs []string) string {
	var parts []string
	if dbDiff {
		parts = append(parts, fmt.Sprintf("db %s/%s -> %s/%s", src.DBCharset, src.DBCollation, dest.DBCharset, dest.DBCollation))
	}
	if len(tableDiffs) > 0 {
		parts = append(parts, "tables: "+strings.Join(tableDiffs, ", "))
	}
	return strings.Join(parts, "; ")
}

// deepDBResult holds the --deep-verify findings for one database. RowDiffs are HARD
// (lost/partial rows, engine-independent); ChecksumDiffs are SOFT (equal row count
// but a different content checksum, only meaningful when both sides ran the same
// server version); AutoIncrDiffs are informational (a reload legitimately changes
// AUTO_INCREMENT).
type deepDBResult struct {
	MissingTables    []string // present on source, absent on destination
	ExtraTables      []string // present on destination, absent on source
	RowDiffs         []string // "table (srcRows->destRows)"
	ChecksumDiffs    []string // table name
	ObjectDiffs      []string // "view foo"/"procedure bar" — same-version body fingerprint differs
	ContentUnchecked bool     // checksum/content comparison could not prove equality
	UncheckedReason  string
	AutoIncrDiffs    []string // "table (src->dest)"
}

func (r deepDBResult) HardDiff() bool {
	return len(r.MissingTables)+len(r.ExtraTables)+len(r.RowDiffs)+len(r.ChecksumDiffs)+len(r.ObjectDiffs) > 0
}

func (r deepDBResult) TableSetHeadline() string {
	switch {
	case len(r.MissingTables) > 0:
		return "missing tables: " + strings.Join(firstN(r.MissingTables, 3), ", ")
	case len(r.ExtraTables) > 0:
		return "extra tables: " + strings.Join(firstN(r.ExtraTables, 3), ", ")
	default:
		return "none"
	}
}

func (r deepDBResult) UnverifiedReason() string {
	if r.UncheckedReason != "" {
		return r.UncheckedReason
	}
	return "row-count fingerprint could not be read"
}

// deepDB fetches the deep fingerprint of one database on both sides and diffs it. It
// returns ok=false if either side's per-table row-count fingerprint could not be read
// (the caller fails closed in BOTH tiers). The per-table CHECKSUM TABLE content hash is
// fetched for the equal-row common tables (a row-count mismatch is already a hard diff,
// so scanning it for content would be wasted) and only when both sides report the
// IDENTICAL server version (CHECKSUM is not comparable across versions/engines); a
// cross-version or NULL/failed checksum is marked ContentUnchecked. deepDB is
// tier-agnostic: it computes the same facts in both tiers, and verifyDBs decides whether
// ContentUnchecked is a soft note (default) or a fail (UNVERIFIED, under --deep-verify).
func deepDB(ctx context.Context, pool *sshx.Pool, it dbmig.DBPlanItem, srcUser, srcPass string, dc destCred) (deepDBResult, bool) {
	srcInfo, errS := dbmig.DeepTables(ctx, pool.Src, it.SrcDB, srcUser, srcPass)
	destInfo, errD := dbmig.DeepTables(ctx, pool.Dest, it.DestDB, dc.user, dc.pass)
	if errS != nil || errD != nil {
		// Fail closed: the per-table row-count fingerprint is part of the DEFAULT verify,
		// so a read failure for this DB is reported UNVERIFIED and fails the run — it must
		// NOT silently certify the metadata-only (table/object count + charset) verdict.
		logx.Warn("row-count verify could not read %s -> %s (src=%v dest=%v); reporting UNVERIFIED for this database", it.SrcDB, it.DestDB, errS, errD)
		return deepDBResult{}, false
	}
	// Only the common tables whose row counts already MATCH are worth a content hash: a
	// row-count mismatch is already reported as a hard RowDiff, so checksumming it (a full
	// table scan) would be wasted work, and the ContentUnchecked note must describe only
	// the equal-row tables whose content genuinely could not be proven.
	var checkable []string
	for _, n := range commonTableNames(srcInfo.Tables, destInfo.Tables) {
		if srcInfo.Tables[n].Rows == destInfo.Tables[n].Rows {
			checkable = append(checkable, n)
		}
	}
	var srcCk, destCk map[string]string
	var checksumUnchecked string
	// CHECKSUM TABLE runs in BOTH tiers (the user opted same-version content checking into
	// the default verify), but only when both sides report the IDENTICAL server version (it
	// is not comparable across versions/engines). A cross-version pair, or a checksum the
	// server could not produce, is ContentUnchecked — which verifyDBs softens to a note at
	// the default tier and escalates to a fail only under --deep-verify.
	if len(checkable) > 0 && srcInfo.Version != "" && srcInfo.Version == destInfo.Version {
		var e1, e2 error
		srcCk, e1 = dbmig.ChecksumTables(ctx, pool.Src, it.SrcDB, srcUser, srcPass, checkable)
		destCk, e2 = dbmig.ChecksumTables(ctx, pool.Dest, it.DestDB, dc.user, dc.pass, checkable)
		if e1 != nil || e2 != nil {
			srcCk, destCk = nil, nil
			checksumUnchecked = fmt.Sprintf("checksum read failed (src=%v dest=%v)", e1, e2)
		}
	} else if len(checkable) > 0 {
		// Cross-version (or unknown version): CHECKSUM TABLE is not comparable, but an
		// engine-independent per-row content fingerprint IS. If it certifies EVERY
		// equal-row table, the content is proven despite the engine difference; otherwise
		// (a read error, a missing table, or a mismatch — possibly just a cross-engine
		// FLOAT/JSON formatting artifact) fall back to the existing "content unchecked"
		// verdict. It can only UPGRADE to certified, never invent a false hard diff.
		if crossEngineContentMatches(ctx, pool, it, srcUser, srcPass, dc, checkable) {
			logx.Debug("deep db %s: cross-engine content fingerprint matched for %d equal-row table(s) — content certified despite version diff (src=%q dest=%q)", it.DestDB, len(checkable), srcInfo.Version, destInfo.Version)
		} else {
			checksumUnchecked = fmt.Sprintf("server versions differ or are unknown (src=%q dest=%q)", srcInfo.Version, destInfo.Version)
			logx.Debug("deep db %s: server versions differ (src=%q dest=%q) — checksum skipped; engine-independent fingerprint did not certify", it.DestDB, srcInfo.Version, destInfo.Version)
		}
	}
	res := diffDeepTables(srcInfo, destInfo, srcCk, destCk)
	if checksumUnchecked != "" {
		res.ContentUnchecked = true
		res.UncheckedReason = checksumUnchecked
	}

	// Non-table object BODIES (view/trigger/routine/event DEFINITIONS) are content too,
	// and the name-set diff (diffSchema) is blind to a same-name object whose body
	// changed — e.g. a botched DEFINER-strip on import corrupting a definition (V12).
	// Fingerprint each object's DEFINER-independent body and compare. Like CHECKSUM
	// TABLE, definitions are reliably comparable only at an IDENTICAL server version
	// (the server canonicalizes them), so a cross-version pair is marked ContentUnchecked
	// (soft note at default, fail under --deep) rather than byte-diffed.
	srcBodies, e3 := dbmig.ObjectBodies(ctx, pool.Src, it.SrcDB, srcUser, srcPass)
	destBodies, e4 := dbmig.ObjectBodies(ctx, pool.Dest, it.DestDB, dc.user, dc.pass)
	switch {
	case e3 != nil || e4 != nil:
		res.ContentUnchecked = true
		if res.UncheckedReason == "" {
			res.UncheckedReason = fmt.Sprintf("object body read failed (src=%v dest=%v)", e3, e4)
		}
		logx.Warn("object-body verify could not read %s -> %s (src=%v dest=%v); reporting content UNVERIFIED", it.SrcDB, it.DestDB, e3, e4)
	case srcInfo.Version != "" && srcInfo.Version == destInfo.Version:
		res.ObjectDiffs = diffObjectBodies(srcBodies, destBodies)
	default:
		// Cross-version: object definitions canonicalize differently, so they cannot be
		// byte-compared. Only flag content-unchecked if there is actually a common object.
		if commonObjectCount(srcBodies, destBodies) > 0 {
			res.ContentUnchecked = true
			if res.UncheckedReason == "" {
				res.UncheckedReason = fmt.Sprintf("object definitions not byte-compared across server versions (src=%q dest=%q)", srcInfo.Version, destInfo.Version)
			}
		}
	}
	return res, true
}

// crossEngineContentMatches certifies, ENGINE-INDEPENDENTLY, that the given equal-row
// tables hold identical content on both sides — the cross-version answer to CHECKSUM
// TABLE, which MySQL<->MariaDB cannot compare. It hashes each table's rows with a
// portable BIT_XOR-of-MD5 fingerprint (dbmig.ContentFingerprints), using the SOURCE
// column lists for BOTH sides so the expression and column order are identical. It
// returns true ONLY when every requested table produced a fingerprint on BOTH sides and
// all match; any read error, any missing table, or any mismatch returns false. So it can
// only UPGRADE a verdict to certified, never invent a difference — a faithful cross-engine
// migration that merely formats a FLOAT/JSON column differently stays "unchecked", not a
// false fail.
func crossEngineContentMatches(ctx context.Context, pool *sshx.Pool, it dbmig.DBPlanItem, srcUser, srcPass string, dc destCred, tables []string) bool {
	if len(tables) == 0 {
		return false
	}
	srcCols, errS := dbmig.TableColumns(ctx, pool.Src, it.SrcDB, srcUser, srcPass)
	destCols, errD := dbmig.TableColumns(ctx, pool.Dest, it.DestDB, dc.user, dc.pass)
	if errS != nil || errD != nil {
		logx.Debug("cross-engine fingerprint %s: could not read columns (src=%v dest=%v) — not certifying", it.DestDB, errS, errD)
		return false
	}
	// Require the column NAME+ORDER to be IDENTICAL on both sides for every table before
	// fingerprinting. The fingerprint hashes the SOURCE column list on both sides, so a
	// DESTINATION-only extra column would otherwise never enter the hash (its data stays
	// invisible) and the schema verdict compares only table/object NAME SETS, never
	// columns — that asymmetry would falsely certify a dest carrying extra/divergent
	// column data. Identical column lists close it; any difference is schema drift, so we
	// do not certify (the verdict stays UNVERIFIED, never a false green).
	want := make(map[string][]string, len(tables))
	for _, t := range tables {
		sc, sok := srcCols[t]
		dcl, dok := destCols[t]
		if !sok || !dok || len(sc) == 0 || !sameColumns(sc, dcl) {
			logx.Debug("cross-engine fingerprint %s: column set differs/absent for table %q — not certifying", it.DestDB, t)
			return false
		}
		want[t] = sc
	}
	srcFP, e1 := dbmig.ContentFingerprints(ctx, pool.Src, it.SrcDB, srcUser, srcPass, want)
	destFP, e2 := dbmig.ContentFingerprints(ctx, pool.Dest, it.DestDB, dc.user, dc.pass, want)
	if e1 != nil || e2 != nil {
		logx.Debug("cross-engine fingerprint %s: read failed (src=%v dest=%v) — not certifying", it.DestDB, e1, e2)
		return false
	}
	for t := range want {
		s, sok := srcFP[t]
		d, dok := destFP[t]
		if !sok || !dok || s == "" || d == "" || s != d {
			logx.Debug("cross-engine fingerprint %s: table %q did not certify (src=%q dest=%q)", it.DestDB, t, s, d)
			return false
		}
	}
	return true
}

// sameColumns reports whether two column-name lists are identical in name AND order.
// Pure.
func sameColumns(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// diffObjectBodies returns the labels of objects present on BOTH sides whose body
// fingerprint differs. A missing/extra object is NOT reported here — it is already
// caught by the version-independent name-set diff (diffSchema). Pure.
func diffObjectBodies(src, dest map[string]string) []string {
	var diffs []string
	for label, srcHash := range src {
		if destHash, ok := dest[label]; ok && srcHash != destHash {
			diffs = append(diffs, label)
		}
	}
	sort.Strings(diffs)
	return diffs
}

// commonObjectCount counts the objects present on both sides (used only to decide
// whether a cross-version pair has any body to flag as unverifiable). Pure.
func commonObjectCount(src, dest map[string]string) int {
	n := 0
	for label := range src {
		if _, ok := dest[label]; ok {
			n++
		}
	}
	return n
}

// diffDeepTables compares two deep fingerprints. Missing/extra tables are HARD
// differences; row-count and same-version checksum mismatches are HARD content
// differences; AUTO_INCREMENT drift remains informational. Checksums are compared
// only when both maps are non-nil; for a common, equal-row table a missing or NULL
// checksum ("") on either side marks the DB content-UNVERIFIED rather than passing
// it on empty-string equality. Pure, so it is unit-tested.
func diffDeepTables(src, dest dbmig.DeepDBInfo, srcCk, destCk map[string]string) deepDBResult {
	var res deepDBResult
	var unproven []string // common, equal-row tables whose checksum could not be compared
	for _, n := range sortedNames(src.Tables) {
		s := src.Tables[n]
		d, ok := dest.Tables[n]
		if !ok {
			res.MissingTables = append(res.MissingTables, n)
			continue
		}
		switch {
		case s.Rows != d.Rows:
			res.RowDiffs = append(res.RowDiffs, fmt.Sprintf("%s (%d->%d)", n, s.Rows, d.Rows))
		case srcCk != nil && destCk != nil:
			// Checksums were attempted (same server version, read succeeded). For a
			// common table with equal row counts the checksum is the ONLY remaining
			// content proof, so a missing entry or NULL checksum (both stored as "")
			// on either side means content was never proven — that is UNVERIFIED, not
			// a pass. Empty-string equality ("" == "") must not read as "content matches".
			sc, dck := srcCk[n], destCk[n]
			switch {
			case sc == "" || dck == "":
				unproven = append(unproven, n)
			case sc != dck:
				res.ChecksumDiffs = append(res.ChecksumDiffs, n)
			}
		}
		if s.AutoIncr != d.AutoIncr {
			res.AutoIncrDiffs = append(res.AutoIncrDiffs, fmt.Sprintf("%s (%d->%d)", n, s.AutoIncr, d.AutoIncr))
		}
	}
	for _, n := range sortedNames(dest.Tables) {
		if _, ok := src.Tables[n]; !ok {
			res.ExtraTables = append(res.ExtraTables, n)
		}
	}
	if len(unproven) > 0 {
		res.ContentUnchecked = true
		res.UncheckedReason = fmt.Sprintf("no comparable content checksum for %d table(s) (CHECKSUM TABLE returned NULL or no row): %s",
			len(unproven), strings.Join(firstN(unproven, 8), ", "))
	}
	return res
}

func commonTableNames(src, dest map[string]dbmig.DeepTable) []string {
	names := make([]string, 0, len(src))
	for n := range src {
		if _, ok := dest[n]; ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// sortedNames returns the table names of a deep fingerprint in sorted order, so the
// diff and its examples are deterministic.
func sortedNames(tables map[string]dbmig.DeepTable) []string {
	names := make([]string, 0, len(tables))
	for n := range tables {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// firstN returns up to n elements of s (for bounded example lists in the report).
func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// unreadableSide names which side's table count could not be read, for the
// UNREADABLE verify line.
func unreadableSide(errS, errD error) string {
	switch {
	case errS != nil && errD != nil:
		return "either side's"
	case errS != nil:
		return "the source"
	default:
		return "the destination"
	}
}

// objCountsStr renders the non-table object counts compactly for a verify line,
// e.g. "routines=2 events=1 triggers=0 views=1".
func objCountsStr(o dbmig.ObjectCounts) string {
	return fmt.Sprintf("routines=%d events=%d triggers=%d views=%d", o.Routines, o.Events, o.Triggers, o.Views)
}

// isAlreadyExists / alreadyExistsMarkers moved to already_exists.go — shared with
// the domain-creation flow (apply_domains.go).
