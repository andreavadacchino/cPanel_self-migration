package migrate

import (
	"context"
	"fmt"

	"github.com/tis24dev/cPanel_self-migration/internal/domainname"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/maildir"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// itemCol is the shared visible-column width for the "<marker> <name>" block of
// every per-item comparison/analysis line (domains, mailboxes, web docroots), so
// the SECOND column (status/size/path) starts at the same place across ALL the
// [n/N] groups. Wide enough for the longest email (e.g.
// "first.last@main.example") plus the marker and a space. The block is
// padded rune-aware via logx.PadCol (markers like → / · are multi-byte but one
// column wide).
const itemCol = 34

// item prints one per-item line (see itemStr) to the screen.
func item(log *logx.Logger, marker, name, format string, args ...any) {
	log.Plain("%s", itemStr(log, marker, name, format, args...))
}

// itemStr builds one per-item line as a STRING in the shared step-3 style: the
// "     • <marker> <name>" block padded to itemCol visible columns, then the
// rest. marker is a 1-column glyph ("=", "~", "+", "·", "→", "?", "!", "✓",
// "✗"). Returned (not printed) so callers can also tee a plain variant to the
// report file. Used by both the dry-run comparison and the apply phases so they
// render identically on screen.
func itemStr(log *logx.Logger, marker, name, format string, args ...any) string {
	body := fmt.Sprintf(format, args...)
	return log.ItemLine("%s %s", itemLeft(marker, name), body)
}

// itemLeft returns just the padded "<marker> <name>" left block (no indent, no
// trailing body), so a progress bar can be placed exactly where the result text
// will later go. The block is padded rune-aware to itemCol visible columns.
func itemLeft(marker, name string) string {
	return logx.PadCol(marker+" "+name, itemCol)
}

// itemPrefix returns the full indented + padded left block ("     • <marker>
// <name>") as a string, for use as a NewInlineProgress prefix (the bar follows
// it, then Replace swaps in the final itemStr result on the same line).
func itemPrefix(log *logx.Logger, marker, name string) string {
	return log.ItemLine("%s", itemLeft(marker, name))
}

// inlineRow starts one item's progress row in the shared "action-left, bar-right"
// layout: a padded "<marker> <name>" block, then the bar where the result will
// land. total<=0 + unit gives a live "N unit" counter; total>0 + unit gives
// "N/M unit" with a percentage. The caller ticks it (SetSuffix/Add) and finishes
// it with prog.Replace(itemStr(log, resultMarker, name, ...)). Drawn immediately
// so the row appears at once (no blank cursor) even before the first tick.
func inlineRow(log *logx.Logger, marker, name string, total int, unit string) *logx.Progress {
	p := log.NewInlineCountProgress(itemPrefix(log, marker, name), total, unit)
	p.Draw()
	return p
}

// comparator fetches mailbox stats from the source or destination. It exists so
// compareDryRun can be exercised in tests with a fake instead of real SSH.
type comparator struct {
	src, dest *sshx.Client
}

// stats returns a mailbox's stats from the source (fromSrc=true) or the
// destination. Read-only on both.
func (c *comparator) stats(ctx context.Context, fromSrc bool, dom, user string) (maildir.BoxStats, error) {
	client := c.dest
	var opts []maildir.ReadOption
	if fromSrc {
		client = c.src
	} else {
		opts = append(opts, maildir.GuardRoot()) // dest read: reject a symlinked root
	}
	return maildir.GetBoxStats(ctx, client, dom, user, opts...)
}

// boxStatReader fetches a mailbox's stats from the source or destination. A real
// *comparator talks to SSH; compareDryRun depends on this interface so the dry-run
// comparison can be exercised with a fake (no SSH) in tests.
type boxStatReader interface {
	stats(ctx context.Context, fromSrc bool, dom, user string) (maildir.BoxStats, error)
}

// boxStatus classifies a mailbox in the SRC<->DEST comparison.
type boxStatus int

const (
	// boxToMigrate: the mailbox does not exist (or is empty) on the destination.
	boxToMigrate boxStatus = iota
	// boxIdentical: same message count AND UIDVALIDITY on both sides.
	boxIdentical
	// boxDiffers: present on both, but count or UIDVALIDITY differ.
	boxDiffers
)

// classifyBox decides a mailbox's status from the SRC and DEST stats. Pure;
// unit-tested. A destination with zero messages is treated as "to migrate"
// (the account/maildir isn't populated yet).
func classifyBox(src, dest maildir.BoxStats) boxStatus {
	if dest.MsgCount == 0 {
		return boxToMigrate
	}
	if src.MsgCount == dest.MsgCount && src.UIDValidity == dest.UIDValidity {
		return boxIdentical
	}
	return boxDiffers
}

// compareDryRun reads, read-only, the per-domain presence and per-mailbox
// content state (message count + UIDVALIDITY) on both servers and logs a
// human-readable SRC<->DEST comparison. Used only in dry-run; it never writes.
func compareDryRun(ctx context.Context, c boxStatReader, pd migrationData, log *logx.Logger, mirror bool) {
	// ----- Domains -----
	log.Detail("domains:")
	var domPresent, domCreate, domBlocked int
	for _, d := range pd.SrcDomains {
		if reason, blocked := domainBlocked(pd, d.Name); blocked {
			item(log, "!", d.Name, "%s", log.Red("BLOCKED — "+reason))
			domBlocked++
			continue
		}
		if issue, ok := domainTypeIssue(pd, d.Name); ok {
			marker := "!"
			color := log.Yellow
			msg := issue.Reason()
			if issue.BlockWeb || issue.BlockDBConfig {
				color = log.Red
			}
			item(log, marker, d.Name, "%s", color(msg))
			domPresent++
			continue
		}
		switch model.ActionFor(d.Type, domainname.Has(pd.DestDomainSet, d.Name)) {
		case model.AlreadyPresent:
			item(log, "=", d.Name, "%s", log.Green("present on both"))
			domPresent++
		case model.CreateAddon:
			item(log, "+", d.Name, "%s", log.Yellow("MISSING on dest — will create (addon)"))
			domCreate++
		case model.CreateSub:
			item(log, "+", d.Name, "%s", log.Yellow("MISSING on dest — will create (subdomain)"))
			domCreate++
		}
	}

	// ----- Mailboxes -----
	// One inline row per mailbox: the action ("→ email") on the left with a
	// "reading ..." indicator on the right while the two read-only stat reads
	// (SRC + DEST) run, then Replace turns that same row into the verdict line.
	// Same layout as every other step.
	//
	// Under --apply-mirror the IDENTICAL/DIFFERS verdicts are not meaningful — every
	// mailbox is re-copied regardless — so the rows instead read "will mirror", and
	// the useful signal becomes the destructive preview: a mailbox whose dest holds
	// MORE messages than the source has at least (dest-src) dest-only messages that
	// the mirror will move aside to <user>-bak (recoverable).
	if mirror {
		log.Detail("mailboxes (--apply-mirror: each will be reset to an exact copy of the source):")
	} else {
		log.Detail("mailboxes (message count + UIDVALIDITY):")
	}
	var identical, differs, toMigrate, toMirror, destAhead, unknown, blockedMail int
	for _, m := range pd.Mailboxes {
		if ctx.Err() != nil {
			log.Warn("interrupted — mailbox comparison stopped")
			break
		}
		email := m.Email()
		if reason, blocked := domainBlocked(pd, m.Domain); blocked {
			item(log, "!", email, "%s", log.Red("BLOCKED — "+reason))
			blockedMail++
			continue
		}

		// If the destination domain isn't there yet, the mailbox is necessarily
		// to-migrate; skip the (pointless) destination read (instant row).
		if !domainname.Has(pd.DestDomainSet, m.Domain) {
			if mirror {
				if src, err := c.stats(ctx, true, m.Domain, m.User); err == nil {
					item(log, "+", email, "%s — src %d msg (dest domain missing)", log.Yellow("will mirror"), src.MsgCount)
				} else {
					logx.Debug("compareDryRun %s: --apply-mirror source stat read failed (dest domain missing): %v", email, err)
					item(log, "+", email, "%s (dest domain missing)", log.Yellow("will mirror"))
				}
				toMirror++
			} else {
				item(log, "+", email, "%s (destination domain missing)", log.Yellow("TO MIGRATE"))
				toMigrate++
			}
			continue
		}

		// Resolve the DESTINATION domain spelling before reading its stats: the
		// destination maildir path is $HOME/mail/<destDom>/<user>, and the
		// cPanel-reported destination name can differ from the source spelling
		// (case, a trailing FQDN dot) while still being canonically the same domain.
		// Reading it with the SOURCE spelling (m.Domain) can hit the wrong/absent
		// path and yield a misleading DIFFERS/TO MIGRATE/unreadable verdict. Apply
		// and verify already resolve via destDomainNameFor; the dry-run must match.
		destDomain, ok := destDomainNameFor(pd, m.Domain)
		if !ok {
			reason := destDomainResolutionIssue(pd, m.Domain)
			logx.Debug("compareDryRun %s: destination domain not resolved: %s", email, reason)
			item(log, "?", email, "could not compare (%s)", reason)
			unknown++
			continue
		}

		prog := inlineRow(log, "→", email, 0, "") // indeterminate "checking" row
		prog.SetSuffix("reading ...")
		src, errS := c.stats(ctx, true, m.Domain, m.User)
		dst, errD := c.stats(ctx, false, destDomain, m.User)
		if errS != nil || errD != nil {
			logx.Debug("compareDryRun %s: read error (src: %v, dest: %v)", email, errS, errD)
			prog.Replace(itemStr(log, "?", email, "could not compare (read error)"))
			unknown++
			continue
		}

		if mirror {
			if dst.MsgCount > src.MsgCount {
				prog.Replace(itemStr(log, "~", email, "%s — dest %d > src %d (≥ %d dest-only msg moved aside to -bak)",
					log.Yellow("will mirror"), dst.MsgCount, src.MsgCount, dst.MsgCount-src.MsgCount))
				destAhead++
			} else {
				prog.Replace(itemStr(log, "→", email, "%s — src %d msg", log.Green("will mirror"), src.MsgCount))
			}
			toMirror++
			continue
		}

		switch classifyBox(src, dst) {
		case boxIdentical:
			prog.Replace(itemStr(log, "=", email, "%s (%d msg, uid %s)", log.Green("IDENTICAL"), src.MsgCount, orQ(src.UIDValidity)))
			identical++
		case boxDiffers:
			logx.Debug("compareDryRun %s: DIFFERS (src %d msg uid %s, dest %d msg uid %s)", email, src.MsgCount, orQ(src.UIDValidity), dst.MsgCount, orQ(dst.UIDValidity))
			prog.Replace(itemStr(log, "~", email, "%s (src %d msg / dest %d msg)", log.Red("DIFFERS"), src.MsgCount, dst.MsgCount))
			differs++
		case boxToMigrate:
			prog.Replace(itemStr(log, "+", email, "%s (%d msg on source, absent on dest)", log.Yellow("TO MIGRATE"), src.MsgCount))
			toMigrate++
		}
	}

	// ----- Summary -----
	if domBlocked > 0 {
		log.Info("domain summary: %d present, %d to create, %d blocked", domPresent, domCreate, domBlocked)
	} else {
		log.Info("domain summary: %d present, %d to create", domPresent, domCreate)
	}
	switch {
	case mirror:
		msg := fmt.Sprintf("mailbox summary: %d to mirror", toMirror)
		if destAhead > 0 {
			msg += fmt.Sprintf(", %d with dest-only mail (moved aside to -bak)", destAhead)
		}
		if blockedMail > 0 {
			msg += fmt.Sprintf(", %d blocked", blockedMail)
		}
		if unknown > 0 {
			msg += fmt.Sprintf(", %d unreadable", unknown)
		}
		log.Info("%s", msg)
	case unknown > 0:
		log.Info("mailbox summary: %d identical, %d differ, %d to migrate, %d blocked, %d unreadable",
			identical, differs, toMigrate, blockedMail, unknown)
	case blockedMail > 0:
		log.Info("mailbox summary: %d identical, %d differ, %d to migrate, %d blocked",
			identical, differs, toMigrate, blockedMail)
	default:
		log.Info("mailbox summary: %d identical, %d differ, %d to migrate",
			identical, differs, toMigrate)
	}
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}
