package migrate

import (
	"github.com/tis24dev/cPanel_self-migration/internal/dbmig"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
)

// compareDBs logs the SOURCE<->DESTINATION database plan: for each source
// database, the name it will become on the destination, whether that database
// already exists there, and how much will be copied. Read-only; used in dry-run.
//
// Whether a destination database exists is read from the destination's UAPI
// database list (gathered up front), NOT a direct MySQL query — the destination
// cPanel user has no MySQL login, and UAPI list_databases is the supported
// read-only way to enumerate them.
func compareDBs(pd migrationData, log *logx.Logger, overrides map[string]dbmig.Override) {
	plan := dbPlan(pd, overrides)
	destSet := destDBSet(pd)
	logx.Debug("compareDBs: %d database(s) to evaluate against %d existing on dest", len(plan), len(destSet))

	// Surface any data-destroying plan collision up front, so the operator sees it in
	// the dry-run before --apply. (--apply refuses the colliding databases unless a
	// referenced domain fails creation first and removes one side from the run, which
	// is why this is worded as "would" rather than a categorical promise.)
	for _, c := range dbmig.ValidatePlan(plan) {
		log.Warn("unsafe DB plan: %s — --apply would refuse the colliding databases", c.Detail)
	}
	for _, v := range dbPlanNameViolations(pd, plan) {
		log.Warn("unsafe DB name plan: %s — --apply would refuse this database before provisioning", v.Detail)
	}

	var willCreate, alreadyThere, skipped int
	var copyBytes int64
	for _, it := range plan {
		if skip, reason := dbAllDomainsUnavailableForApply(pd, it); skip {
			skipped++
			item(log, "→", it.SrcDB, "%s", log.Yellow("skip — "+reason))
			continue
		}
		if it.DiskUsage > 0 { // a negative/unknown cPanel disk figure must not shrink the total
			copyBytes += it.DiskUsage
		}
		typeIssueReasons := dbConfigTypeIssueReasons(pd, it)
		if destSet[it.DestDB] {
			alreadyThere++
			item(log, "~", it.SrcDB, "%s -> %s  %s (will overwrite — migration)", log.Yellow("exists on dest"), it.DestDB, report.HumanBytes(it.DiskUsage))
		} else {
			willCreate++
			item(log, "→", it.SrcDB, "%s -> %s  %s", log.Green("will create"), it.DestDB, report.HumanBytes(it.DiskUsage))
		}
		for _, reason := range typeIssueReasons {
			log.Warn("%s config rewrite will require manual action: %s", it.SrcDB, reason)
		}
	}
	if skipped > 0 {
		log.Info("databases summary: %d to create, %d already on destination (will overwrite), %d skipped; %s to copy",
			willCreate, alreadyThere, skipped, report.HumanBytes(copyBytes))
	} else {
		log.Info("databases summary: %d to create, %d already on destination (will overwrite); %s to copy",
			willCreate, alreadyThere, report.HumanBytes(copyBytes))
	}
}

// destDBSet indexes the destination's existing database names for O(1) lookup.
func destDBSet(pd migrationData) map[string]bool {
	m := make(map[string]bool, len(pd.DestDatabases))
	for _, d := range pd.DestDatabases {
		m[d.Database] = true
	}
	return m
}
