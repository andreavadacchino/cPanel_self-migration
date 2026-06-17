// Package webfiles copies website document roots (public_html content) from a
// read-only SOURCE cPanel account to a DESTINATION, over the same tar-stream
// bridge used for mail (SRC `tar -c` -> Go pipe -> DEST `tar -x`).
//
// MIGRATION semantics, not sync: the destination docroot is emptied (within a
// hard safety guard) before the copy so it becomes an exact mirror of the
// source. Only files are copied — no databases. The SOURCE is only ever read.
//
// Docroots are NEVER guessed: they come from DomainInfo::domains_data on each
// side and are joined by domain name, because the two cPanel accounts can lay
// docroots out differently (e.g. addons in dedicated HOME dirs on the source
// vs under public_html/ on the destination).
package webfiles

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/domainname"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// DocrootEntry is the subset of cpanel.DomainDataEntry the planner needs. Kept
// package-local so webfiles does not import cpanel (the caller adapts).
type DocrootEntry struct {
	Domain       string
	DocumentRoot string
	Type         string // main_domain | addon_domain | sub_domain | parked_domain
}

// WebPlanItem is one domain's web-file migration plan: where its files live on
// each side, how big the source side is, and whether it should be skipped.
type WebPlanItem struct {
	Domain      string
	Type        string // source-side type
	SrcDocroot  string
	DestDocroot string

	// Filled later by Gather (a read-only du/find on the source); zero here.
	SrcBytes     int64
	SrcFileCount int

	Notes []string // human warnings (no dest match, empty/absent docroot, ...)
	Skip  bool     // true => do not transfer this domain
}

// BuildPlan joins the source and destination docroots by domain name. It
// iterates the SOURCE domains (the things we want to migrate); for each it
// finds the destination docroot of the SAME domain name and emits one item
// using each side's own document root.
//
// Rules:
//   - A source domain with no destination match => Skip=true with a note
//     (the domain must be created on the destination first).
//   - A destination-only domain (e.g. the destination's real main domain,
//     which has no source counterpart) never appears: we only iterate sources.
//
// Pure and deterministic: output is sorted by domain name. Sizes are 0 here;
// emptiness/absence is decided later by Gather against the live source.
func BuildPlan(src, dest []DocrootEntry) []WebPlanItem {
	destByName := make(map[string]DocrootEntry, len(dest))
	collisions := map[string][]DocrootEntry{}
	for _, d := range dest {
		key := domainname.Key(d.Domain)
		if prev, ok := destByName[key]; ok {
			if len(collisions[key]) == 0 {
				collisions[key] = append(collisions[key], prev)
			}
			collisions[key] = append(collisions[key], d)
			continue
		}
		destByName[key] = d
	}

	out := make([]WebPlanItem, 0, len(src))
	for _, s := range src {
		key := domainname.Key(s.Domain)
		item := WebPlanItem{
			Domain:     s.Domain,
			Type:       s.Type,
			SrcDocroot: s.DocumentRoot,
		}
		if dup := collisions[key]; len(dup) > 0 {
			item.Skip = true
			item.Notes = append(item.Notes, canonicalDocrootCollisionNote(s.Domain, dup))
			logx.Debug("webfiles plan: %s has a destination canonical-domain collision — will skip", s.Domain)
			out = append(out, item)
			continue
		}
		d, ok := destByName[key]
		if !ok || d.DocumentRoot == "" {
			item.Skip = true
			item.Notes = append(item.Notes,
				"no destination domain '"+s.Domain+"' — create it first")
			logx.Debug("webfiles plan: %s has no dest docroot match — will skip", s.Domain)
		} else {
			item.DestDocroot = d.DocumentRoot
		}
		out = append(out, item)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	skipped := countSkipped(out)
	logx.Debug("webfiles plan: %d source domain(s) processed (%d to transfer, %d skipped)", len(out), len(out)-skipped, skipped)
	return out
}

func canonicalDocrootCollisionNote(sourceDomain string, dest []DocrootEntry) string {
	parts := make([]string, 0, len(dest))
	for _, d := range dest {
		parts = append(parts, fmt.Sprintf("%s -> %s", d.Domain, d.DocumentRoot))
	}
	sort.Strings(parts)
	return fmt.Sprintf("destination canonical domain collision for %q: %s", sourceDomain, strings.Join(parts, "; "))
}

// countSkipped returns how many plan items are marked Skip.
func countSkipped(items []WebPlanItem) int {
	n := 0
	for _, it := range items {
		if it.Skip {
			n++
		}
	}
	return n
}
