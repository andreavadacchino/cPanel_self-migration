package migrate

import (
	"context"
	"fmt"

	"github.com/tis24dev/cPanel_self-migration/internal/dbmig"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
)

// analyzeDBs is the database analysis step: from the read-only source inventory
// (Mysql::list_databases / list_users) joined with the wp-config credentials
// discovered under the docroots, it logs each database's source->destination
// name mapping and its link state (linked / shared / orphan), AND writes
// db_analysis.log — the database counterpart of mail_analysis.log and
// web_analysis.log. It never writes to either server.
func analyzeDBs(ctx context.Context, pd migrationData, log *logx.Logger, outputDir, srcRef, date string, overrides map[string]dbmig.Override, sourceOnly bool) error {
	plan := dbPlan(pd, overrides)
	logx.Debug("analyzeDBs: %d database(s), %d wp-config cred(s) discovered", len(plan), len(pd.SiteCreds))
	var warnings []string
	var planErr error
	if !sourceOnly {
		violations := dbPlanNameViolations(pd, plan)
		for _, v := range violations {
			log.Warn("unsafe DB name plan: %s", v.Detail)
			warnings = append(warnings, "unsafe DB name plan: "+v.Detail)
		}
		planErr = dbPlanNameError(violations)
	}

	rep := report.DBAnalysisReport{
		HostRef:    srcRef,
		Date:       date,
		SrcPrefix:  dbSrcPrefix(pd),
		DestPrefix: dbDestPrefix(pd),
		SourceOnly: sourceOnly,
		Warnings:   warnings,
	}
	var linked, shared, orphan int
	var totalDisk int64
	for _, it := range plan {
		status := classifyDBAnalysis(it)
		destDBName, destUserName := it.DestDB, it.DestUser
		if sourceOnly {
			destDBName, destUserName = "", ""
		}
		rep.Databases = append(rep.Databases, report.DBAnalysisDomain{
			SrcDB:     it.SrcDB,
			SrcUser:   it.SrcUser,
			DestDB:    destDBName,
			DestUser:  destUserName,
			DiskUsage: it.DiskUsage,
			Configs:   configPaths(it),
			HasPass:   it.Password != "",
			Status:    status,
		})
		if it.DiskUsage > 0 { // a negative/unknown cPanel disk figure must not shrink the total
			totalDisk += it.DiskUsage
		}
		switch status {
		case report.DBOrphan:
			orphan++
			if sourceOnly {
				item(log, "?", it.SrcDB, "%s (user %s, %s)", log.Yellow("orphan"), it.SrcUser, report.HumanBytes(it.DiskUsage))
			} else {
				item(log, "?", it.SrcDB, "%s -> %s (user %s, %s)", log.Yellow("orphan"), it.DestDB, it.DestUser, report.HumanBytes(it.DiskUsage))
			}
		case report.DBShared:
			shared++
			linked++
			if sourceOnly {
				item(log, "=", it.SrcDB, "%s (%d installs, %s)", log.Green("shared"), len(it.Configs), report.HumanBytes(it.DiskUsage))
			} else {
				item(log, "=", it.SrcDB, "%s -> %s (%d installs, %s)", log.Green("shared"), it.DestDB, len(it.Configs), report.HumanBytes(it.DiskUsage))
			}
		default: // DBLinked
			linked++
			if sourceOnly {
				item(log, "=", it.SrcDB, "%s (user %s, %s)", log.Green("linked"), it.SrcUser, report.HumanBytes(it.DiskUsage))
			} else {
				item(log, "=", it.SrcDB, "%s -> %s (user %s, %s)", log.Green("linked"), it.DestDB, it.DestUser, report.HumanBytes(it.DiskUsage))
			}
		}
	}
	log.Info("databases on source: %d (%s); %d linked, %d shared, %d orphan",
		len(plan), report.HumanBytes(totalDisk), linked, shared, orphan)

	// Write the analysis artifact (db_analysis.log).
	f, path, err := createLogFile(outputDir, "db_analysis.log")
	if err != nil {
		return err
	}
	if err := report.WriteDBAnalysis(f, rep); err != nil {
		_ = f.Abort()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		// A Close error means a buffered write/flush failed (disk full/quota/NFS);
		// surface it rather than reporting "wrote ..." for a possibly-truncated
		// artifact — same handling as mail_analysis.log in runner.go.
		return fmt.Errorf("close %s: %w", path, err)
	}
	log.OK("wrote %s", path)
	return planErr
}

// classifyDBAnalysis maps a plan item to its analysis status.
func classifyDBAnalysis(it dbmig.DBPlanItem) report.DBAnalysisStatus {
	switch {
	case it.Orphan:
		return report.DBOrphan
	case len(it.Configs) > 1:
		return report.DBShared
	default:
		return report.DBLinked
	}
}

// configPaths extracts the wp-config paths a database item references.
func configPaths(it dbmig.DBPlanItem) []string {
	out := make([]string, 0, len(it.Configs))
	for _, c := range it.Configs {
		out = append(out, c.ConfigPath)
	}
	return out
}
