package migrate

import (
	"context"
	"io"
	"sort"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
	"github.com/tis24dev/cPanel_self-migration/internal/webfiles"
)

// toDocrootEntries adapts cpanel.DomainDataEntry values into the webfiles
// planner's local DocrootEntry type (so webfiles need not import cpanel).
func toDocrootEntries(in []cpanel.DomainDataEntry) []webfiles.DocrootEntry {
	out := make([]webfiles.DocrootEntry, len(in))
	for i, e := range in {
		out[i] = webfiles.DocrootEntry{
			Domain:       e.Domain,
			DocumentRoot: e.DocumentRoot,
			Type:         e.Type,
		}
	}
	return out
}

// webPlan builds the web-file plan from the gathered docroots (pure join). The
// caller fills sizes via webfiles.Gather where needed.
func webPlan(pd migrationData) []webfiles.WebPlanItem {
	return webfiles.BuildPlan(toDocrootEntries(pd.SrcDocroots), toDocrootEntries(pd.DestDocroots))
}

// sourceOnlyWebPlan builds docroot analysis items when there is no destination
// account configured. Every source docroot is probed; DestDocroot stays empty and
// the report renders that as "destination not configured" instead of "missing".
func sourceOnlyWebPlan(pd migrationData) []webfiles.WebPlanItem {
	src := toDocrootEntries(pd.SrcDocroots)
	items := make([]webfiles.WebPlanItem, 0, len(src))
	for _, s := range src {
		items = append(items, webfiles.WebPlanItem{
			Domain:     s.Domain,
			Type:       s.Type,
			SrcDocroot: s.DocumentRoot,
		})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Domain < items[j].Domain })
	return items
}

// probePairs turns the not-yet-skipped plan items into (domain, docroot) pairs for
// the one-shot streaming gather, preserving plan order. useDest selects the
// destination docroot (verify step) instead of the source docroot (analyze step);
// for the destination, an item with no destination docroot yet is skipped.
func probePairs(items []webfiles.WebPlanItem, useDest bool) []webfiles.GatherPair {
	pairs := make([]webfiles.GatherPair, 0, len(items))
	for _, it := range items {
		if it.Skip {
			continue
		}
		path := it.SrcDocroot
		if useDest {
			if it.DestDocroot == "" {
				continue
			}
			path = it.DestDocroot
		}
		pairs = append(pairs, webfiles.GatherPair{Domain: it.Domain, Path: path})
	}
	return pairs
}

// gatherStream runs the one-shot streaming gather against client c over the given
// docroot pairs, rendering live per-docroot + per-file progress via hooks. It is
// the shared SSH plumbing for the analyze (source) and verify (destination)
// steps: one session, the framed output parsed by webfiles.ParseGatherStream.
// Read-only — only `find` runs remotely.
//
// On ctx cancellation the in-flight Read is unblocked immediately (sr.Abort), so
// a Ctrl-C stops at once. A truncated stream is non-fatal: the partial result map
// is returned together with the error, so the caller can warn and proceed.
func gatherStream(ctx context.Context, c *sshx.Client, pairs []webfiles.GatherPair, hooks webfiles.GatherHooks) (map[string]webfiles.GatherResult, error) {
	if len(pairs) == 0 {
		return map[string]webfiles.GatherResult{}, nil
	}
	results := map[string]webfiles.GatherResult{}
	// RunStream owns the session/ctx-abort/close; ParseGatherStream consumes the
	// framed stdout (and fills `results`, partial on a truncated stream).
	err := sshx.RunStream(ctx, c, webfiles.GatherAllCommand(pairs),
		strings.NewReader(webfiles.GatherAllScriptBody()),
		func(r io.Reader) error {
			var perr error
			results, perr = webfiles.ParseGatherStream(r, len(pairs), hooks)
			return perr
		})
	return results, err
}

// instantRow is a pre-decided plan item with no streamed work (e.g. a domain with
// no destination docroot yet): its already-formatted line is printed as-is, in
// domain order, interleaved with the streamed rows.
type instantRow struct {
	Domain string
	Line   string // a full itemStr(...) line
}

// gatherStreamRows streams the gather over `probed` (ONE SSH session) and renders
// ONE inline row per docroot — action-left, live "N files" counter on the right —
// in domain order, interleaving the pre-decided `instant` rows at their
// alphabetical position. For each streamed docroot it calls done(domain, res,
// prog); the callback MUST turn the row into its result via prog.Replace(...) (and
// may tee to a report). A probed docroot that never produced a result (truncated
// stream) is passed to missing(domain) for a final row. Returns the results map +
// gather error. Both `instant` and `probed` are domain-sorted (BuildPlan order),
// so a single merge pointer keeps the output alphabetical.
func gatherStreamRows(
	ctx context.Context, c *sshx.Client, log *logx.Logger,
	probed []webfiles.GatherPair, instant []instantRow,
	done func(domain string, res webfiles.GatherResult, prog *logx.Progress),
	missing func(domain string),
) (map[string]webfiles.GatherResult, error) {
	sort.Slice(instant, func(i, j int) bool { return instant[i].Domain < instant[j].Domain })
	ii := 0
	flushBefore := func(domain string) {
		for ii < len(instant) && instant[ii].Domain < domain {
			log.Plain("%s", instant[ii].Line)
			ii++
		}
	}

	var prog *logx.Progress
	results, gerr := gatherStream(ctx, c, probed, webfiles.GatherHooks{
		Start: func(_, _ int, domain string) {
			flushBefore(domain)
			prog = inlineRow(log, "→", domain, 0, "files")
		},
		Tick: func(_, _ int, domain string, files int) {
			if prog != nil {
				// The cumulative count lives in the bar's counter (cur), so the row
				// renders "→ domain  [bar]  N files" — not a phantom "0 files" from
				// the counter plus the real count duplicated in the suffix slot.
				prog.Set(int64(files))
			}
		},
		Done: func(_, _ int, domain string, res webfiles.GatherResult) {
			if prog == nil { // defensive: a Done with no preceding Start
				prog = inlineRow(log, "→", domain, 0, "files")
			}
			done(domain, res, prog)
			prog = nil
		},
	})

	// Any instant rows after the last streamed docroot.
	for ii < len(instant) {
		log.Plain("%s", instant[ii].Line)
		ii++
	}
	// Probed docroots with no result (truncated/aborted stream) get a final row.
	// Collect their names: per-docroot rows look like ordinary divergences, so name
	// the casualties of an incomplete stream together — otherwise an operator
	// seeing several docroots flip to "not probed" has no trace of where it broke.
	if missing != nil {
		var lost []string
		for _, p := range probed {
			if _, ok := results[p.Domain]; !ok {
				lost = append(lost, p.Domain)
				missing(p.Domain)
			}
		}
		if len(lost) > 0 {
			logx.Warn("gather: %d of %d probed docroot(s) produced no result and were not measured: %v", len(lost), len(probed), lost)
		}
	}
	return results, gerr
}

// applyGatherResults folds streamed results (keyed by domain) back into the plan,
// applying the SAME skip rules and note strings the per-docroot gather used, so
// the downstream classification (classifyWebAnalysis / hasNote, which match the
// substrings "absent"/"empty") is unchanged:
//
//   - docroot absent       -> Skip + "source docroot absent: <path>"
//   - docroot unreadable   -> Skip + "source docroot unreadable: <path>"
//   - docroot empty (0)    -> Skip + "source docroot empty — destination left untouched"
//   - otherwise            -> fill SrcBytes / SrcFileCount
//
// A probable item with NO result (a truncated stream stopped before it) is marked
// Skip with a "source not probed" note rather than left looking like a healthy
// "ready (0 files)" docroot.
func applyGatherResults(items []webfiles.WebPlanItem, results map[string]webfiles.GatherResult) {
	for i := range items {
		it := &items[i]
		if it.Skip {
			continue
		}
		res, ok := results[it.Domain]
		switch {
		case !ok:
			it.Skip = true
			it.Notes = append(it.Notes, "source not probed (analysis incomplete): "+it.SrcDocroot)
		case res.Absent:
			it.Skip = true
			it.Notes = append(it.Notes, "source docroot absent: "+it.SrcDocroot)
		case res.Unreadable:
			it.Skip = true
			it.Notes = append(it.Notes, "source docroot unreadable: "+it.SrcDocroot)
		case res.Count == 0:
			it.Skip = true
			it.Notes = append(it.Notes, "source docroot empty — destination left untouched")
		default:
			it.SrcBytes = res.Bytes
			it.SrcFileCount = res.Count
		}
	}
}
