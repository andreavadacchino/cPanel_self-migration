package migrate

import (
	"context"
	"fmt"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
	"github.com/tis24dev/cPanel_self-migration/internal/webfiles"
)

// analyzeWebFiles is the web-file analysis step: it reads the SOURCE docroots
// READ-ONLY (size + file count, applying the system exclusions), logs the
// per-domain structure to screen, AND writes web_analysis.log — the website-file
// counterpart of mail_analysis.log. It never writes to either server.
func analyzeWebFiles(ctx context.Context, pool *sshx.Pool, pd migrationData, log *logx.Logger, outputDir, srcRef, date string, sourceOnly bool) error {
	logx.Debug("analyzeWebFiles: building plan from %d src / %d dest docroots",
		len(pd.SrcDocroots), len(pd.DestDocroots))
	items := webPlan(pd)
	if sourceOnly {
		items = sourceOnlyWebPlan(pd)
	}

	// Scan each docroot in ONE streaming SSH session, rendered as ONE inline row
	// per docroot: the action ("→ domain") on the left with a live "N files"
	// counter on the right while its tree is walked, then Replace turns that same
	// row into the docroot's result (= ready / · empty / ! absent). Domains with no
	// destination yet have nothing to scan, so they appear as instant "?" rows
	// interleaved in alphabetical order.
	pairs := probePairs(items, false)
	var instant []instantRow
	for _, it := range items {
		if it.Skip { // BuildPlan-skip here means: no destination domain yet
			marker := "?"
			msg := log.Yellow("no destination domain yet")
			if reason, blocked := domainBlocked(pd, it.Domain); blocked {
				marker = "!"
				msg = log.Red("BLOCKED — " + reason)
			} else if hasNote(it.Notes, "canonical domain collision") {
				marker = "!"
				msg = log.Red(skipReason(it.Notes))
			}
			instant = append(instant, instantRow{
				Domain: it.Domain,
				Line:   itemStr(log, marker, it.Domain, "%s", msg),
			})
		}
	}
	results, gerr := gatherStreamRows(ctx, pool.Src, log, pairs, instant,
		func(domain string, res webfiles.GatherResult, prog *logx.Progress) {
			switch {
			case res.Unreadable:
				prog.Replace(itemStr(log, "!", domain, "%s", log.Red("source docroot UNREADABLE (permission denied) — fix permissions; NOT migrated")))
			case res.Absent:
				prog.Replace(itemStr(log, "!", domain, "%s", log.Yellow("source docroot absent")))
			case res.Count == 0:
				prog.Replace(itemStr(log, "·", domain, "source docroot empty — on apply: existing destination backed up to <docroot>-bak, then emptied"))
			default:
				prog.Replace(itemStr(log, "=", domain, "%s (%d files, %s)",
					log.Green("ready"), res.Count, report.HumanBytes(res.Bytes)))
			}
		},
		func(domain string) {
			item(log, "!", domain, "%s", log.Yellow("not probed (analysis incomplete)"))
		},
	)
	if gerr != nil {
		// Non-fatal: dry-run analysis should not abort. Keep going and report the
		// docroots that DID complete (applyGatherResults flags the unprobed ones).
		log.Warn("could not fully analyze source web files: %v", gerr)
	}
	applyGatherResults(items, results)

	// Build the report + summary from the folded-back items (the per-domain lines
	// were already printed live above).
	rep := report.WebAnalysisReport{HostRef: srcRef, Date: date, SourceOnly: sourceOnly}
	var withFiles, empty, absent, unreadable, noDest int
	var totalBytes int64
	var totalFiles int
	for _, it := range items {
		status := classifyWebAnalysis(it, sourceOnly)
		rep.Domains = append(rep.Domains, report.WebAnalysisDomain{
			Domain:      it.Domain,
			Type:        it.Type,
			SrcDocroot:  it.SrcDocroot,
			DestDocroot: it.DestDocroot,
			Files:       it.SrcFileCount,
			Bytes:       it.SrcBytes,
			Status:      status,
		})
		switch status {
		case report.WebNoDest:
			noDest++
		case report.WebAbsent:
			absent++
		case report.WebUnreadable:
			unreadable++
		case report.WebEmpty:
			empty++
		default: // WebReady
			withFiles++
			totalBytes += it.SrcBytes
			totalFiles += it.SrcFileCount
		}
	}
	// Persistent completion line for the scan (✓ + count), so the step ends with a
	// clear "scanned N docroots" summary, consistent with the other scan steps.
	log.OK("scanned %d docroot(s): %d with content (%d files, %s), %d empty, %d absent, %d unreadable, %d without dest",
		withFiles+empty+absent+unreadable+noDest, withFiles, totalFiles, report.HumanBytes(totalBytes), empty, absent, unreadable, noDest)

	// Write the analysis artifact (web_analysis.log), mirroring mail_analysis.log.
	f, path, err := createLogFile(outputDir, "web_analysis.log")
	if err != nil {
		return err
	}
	if err := report.WriteWebAnalysis(f, rep); err != nil {
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
	return nil
}

// classifyWebAnalysis maps a (gathered) plan item to its analysis status.
func classifyWebAnalysis(it webfiles.WebPlanItem, sourceOnly bool) report.WebAnalysisStatus {
	if sourceOnly {
		switch {
		case it.Skip && hasNote(it.Notes, "unreadable"):
			return report.WebUnreadable
		case it.Skip && hasNote(it.Notes, "absent"):
			return report.WebAbsent
		case it.Skip && hasNote(it.Notes, "empty"):
			return report.WebEmpty
		default:
			return report.WebReady
		}
	}
	switch {
	case it.DestDocroot == "":
		return report.WebNoDest
	case it.Skip && hasNote(it.Notes, "unreadable"):
		return report.WebUnreadable
	case it.Skip && hasNote(it.Notes, "absent"):
		return report.WebAbsent
	case it.Skip && hasNote(it.Notes, "empty"):
		return report.WebEmpty
	default:
		return report.WebReady
	}
}

// hasNote reports whether any note contains the given substring (case-insensitive).
func hasNote(notes []string, sub string) bool {
	for _, n := range notes {
		if containsFold(n, sub) {
			return true
		}
	}
	return false
}

// containsFold is a tiny case-insensitive ASCII substring check.
func containsFold(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	lower := func(b byte) byte {
		if b >= 'A' && b <= 'Z' {
			return b + 32
		}
		return b
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		ok := true
		for j := 0; j < len(sub); j++ {
			if lower(s[i+j]) != lower(sub[j]) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
