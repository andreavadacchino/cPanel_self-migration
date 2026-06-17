package migrate

import (
	"context"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
	"github.com/tis24dev/cPanel_self-migration/internal/webfiles"
)

// compareWebFiles logs the SOURCE<->DESTINATION web-file plan: for each source
// domain, where its files live on each side and how much will be copied, plus
// warnings for docroots that will be skipped. Read-only; used in dry-run.
func compareWebFiles(ctx context.Context, pool *sshx.Pool, pd migrationData, log *logx.Logger) {
	items := webPlan(pd)
	items, err := webfiles.Gather(ctx, pool.Src, items)
	if err != nil {
		log.Warn("could not compare web files: %v", err)
		return
	}

	var willCopy, willSkip int
	var copyBytes int64
	for _, it := range items {
		if reason, blocked := domainBlocked(pd, it.Domain); blocked {
			willSkip++
			item(log, "!", it.Domain, "%s", log.Red("BLOCKED — "+reason))
			continue
		}
		if issue, blocked := domainTypeIssue(pd, it.Domain); blocked && issue.BlockWeb {
			willSkip++
			item(log, "!", it.Domain, "%s", log.Red(issue.Reason()))
			continue
		}
		switch {
		case it.Skip && hasNote(it.Notes, "canonical domain collision"):
			willSkip++
			item(log, "!", it.Domain, "%s", log.Red(skipReason(it.Notes)))
		case it.DestDocroot == "":
			willSkip++
			item(log, "+", it.Domain, "%s", log.Yellow("destination domain missing — create it first"))
		case it.Skip && hasNote(it.Notes, "unreadable"):
			willSkip++
			item(log, "!", it.Domain, "%s", log.Red(skipReason(it.Notes)))
		case it.Skip:
			willSkip++
			item(log, "·", it.Domain, "%s", skipReason(it.Notes))
		default:
			willCopy++
			copyBytes += it.SrcBytes
			item(log, "→", it.Domain, "%s -> %s  %s", it.SrcDocroot, it.DestDocroot,
				log.Green(report.HumanBytes(it.SrcBytes)))
		}
	}
	// Persistent completion line (✓ + count), consistent with the other docroot
	// steps.
	log.OK("compared %d docroot(s): %d to copy (%s), %d skipped",
		willCopy+willSkip, willCopy, report.HumanBytes(copyBytes), willSkip)
}

// skipReason returns the first note, or a generic message.
func skipReason(notes []string) string {
	if len(notes) > 0 {
		return notes[0]
	}
	return "skipped"
}
