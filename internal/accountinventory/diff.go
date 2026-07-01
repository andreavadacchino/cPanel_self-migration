package accountinventory

// Deterministic comparison of two NormalizedInventory documents. The diff
// only states WHAT differs (source → destination); it never judges whether
// a difference is safe or dangerous — that is a later, separate concern.
//
// Direction: "removed" = present only in the source, "added" = present only
// in the destination.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// DiffEntry is one added or removed item, identified by its section
// matching key. Detail carries an already-safe human hint (for cron it is
// the REDACTED command — the raw command does not exist in the inventory).
type DiffEntry struct {
	Key    string `json:"key"`
	Detail string `json:"detail,omitempty"`
}

// DiffFieldChange is one field whose value differs on an item present on
// both sides.
type DiffFieldChange struct {
	Key         string `json:"key"`
	Field       string `json:"field"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

type SectionDiff struct {
	Added    []DiffEntry       `json:"added"`
	Removed  []DiffEntry       `json:"removed"`
	Changed  []DiffFieldChange `json:"changed"`
	Warnings []string          `json:"warnings"`
}

func newSectionDiff() SectionDiff {
	return SectionDiff{
		Added:    []DiffEntry{},
		Removed:  []DiffEntry{},
		Changed:  []DiffFieldChange{},
		Warnings: []string{},
	}
}

type DiffSummary struct {
	SectionsCompared int `json:"sections_compared"`
	Added            int `json:"added"`
	Removed          int `json:"removed"`
	Changed          int `json:"changed"`
	Warnings         int `json:"warnings"`
}

type InventoryDiff struct {
	Mode            string                 `json:"mode"`
	SourceFile      string                 `json:"source_file"`
	DestinationFile string                 `json:"destination_file"`
	GeneratedAt     string                 `json:"generated_at"`
	Summary         DiffSummary            `json:"summary"`
	Sections        map[string]SectionDiff `json:"sections"`
	// Warnings holds cross-section warnings. Currently always empty —
	// per-section warnings live in each SectionDiff — but it is part of
	// the diff schema consumers can already rely on.
	Warnings []string `json:"warnings"`
}

// diffSectionNames fixes the section order for reports and iteration.
var diffSectionNames = []string{
	"domains", "mailboxes", "databases", "forwarders", "autoresponders",
	"ftp", "ssl", "php", "dns", "cron",
}

// DiffInventories compares two inventories section by section. It is pure
// and deterministic: file names and timestamp are the caller's concern.
func DiffInventories(src, dest NormalizedInventory) InventoryDiff {
	d := InventoryDiff{
		Mode:     "inventory-diff",
		Sections: map[string]SectionDiff{},
		Warnings: []string{},
	}

	d.Sections["domains"] = diffKeyed(domainItems(src.Domains), domainItems(dest.Domains))
	d.Sections["mailboxes"] = diffKeyed(mailboxItems(src.Mailboxes), mailboxItems(dest.Mailboxes))
	d.Sections["databases"] = diffKeyed(databaseItems(src.Databases), databaseItems(dest.Databases))
	d.Sections["forwarders"] = diffKeyed(forwarderItems(src.Forwarders), forwarderItems(dest.Forwarders))
	d.Sections["autoresponders"] = diffKeyed(autoresponderItems(src.Autoresponders), autoresponderItems(dest.Autoresponders))
	d.Sections["ftp"] = diffConfigKeyed("ftp", src.FTP.ConfigSection, dest.FTP.ConfigSection,
		ftpItems(src.FTP.Items), ftpItems(dest.FTP.Items))
	d.Sections["ssl"] = diffConfigKeyed("ssl", src.SSL.ConfigSection, dest.SSL.ConfigSection,
		sslItems(src.SSL.Items), sslItems(dest.SSL.Items))
	d.Sections["php"] = diffConfigKeyed("php", src.PHP.ConfigSection, dest.PHP.ConfigSection,
		phpItems(src.PHP.Items), phpItems(dest.PHP.Items))
	d.Sections["dns"] = diffDNS(src.DNS, dest.DNS)
	d.Sections["cron"] = diffCron(src.Cron, dest.Cron)

	d.Summary.SectionsCompared = len(d.Sections)
	for _, sec := range d.Sections {
		d.Summary.Added += len(sec.Added)
		d.Summary.Removed += len(sec.Removed)
		d.Summary.Changed += len(sec.Changed)
		d.Summary.Warnings += len(sec.Warnings)
	}
	d.Summary.Warnings += len(d.Warnings)
	return d
}

// ---------------------------------------------------------------------------
// Generic keyed comparison
// ---------------------------------------------------------------------------

// keyedItem is one comparable item: an identity key, an optional
// human-safe detail, and the named fields compared for "changed".
type keyedItem struct {
	key    string
	detail string
	fields map[string]string
}

func diffKeyed(srcItems, destItems []keyedItem) SectionDiff {
	sec := newSectionDiff()

	srcMap := map[string]keyedItem{}
	for _, it := range srcItems {
		if _, dup := srcMap[it.key]; dup {
			sec.Warnings = append(sec.Warnings, fmt.Sprintf("duplicate key %q in source — last occurrence wins", it.key))
		}
		srcMap[it.key] = it
	}
	destMap := map[string]keyedItem{}
	for _, it := range destItems {
		if _, dup := destMap[it.key]; dup {
			sec.Warnings = append(sec.Warnings, fmt.Sprintf("duplicate key %q in destination — last occurrence wins", it.key))
		}
		destMap[it.key] = it
	}

	for key, it := range destMap {
		if _, ok := srcMap[key]; !ok {
			sec.Added = append(sec.Added, DiffEntry{Key: key, Detail: it.detail})
		}
	}
	for key, it := range srcMap {
		if _, ok := destMap[key]; !ok {
			sec.Removed = append(sec.Removed, DiffEntry{Key: key, Detail: it.detail})
			continue
		}
		other := destMap[key]
		fields := make([]string, 0, len(it.fields))
		for f := range it.fields {
			fields = append(fields, f)
		}
		sort.Strings(fields)
		for _, f := range fields {
			if it.fields[f] != other.fields[f] {
				sec.Changed = append(sec.Changed, DiffFieldChange{
					Key: key, Field: f, Source: it.fields[f], Destination: other.fields[f],
				})
			}
		}
	}

	sortSectionDiff(&sec)
	return sec
}

// diffConfigKeyed wraps diffKeyed for ConfigSection-based sections: when
// either side is unavailable the comparison is skipped with a warning —
// an unavailable section is NOT an empty one, and its absent items must
// not read as removals.
func diffConfigKeyed(name string, srcCfg, destCfg ConfigSection, srcItems, destItems []keyedItem) SectionDiff {
	if sec, skipped := skipUnavailable(name, srcCfg.Available, destCfg.Available); skipped {
		return sec
	}
	return diffKeyed(srcItems, destItems)
}

func skipUnavailable(name string, srcAvail, destAvail bool) (SectionDiff, bool) {
	sec := newSectionDiff()
	if !srcAvail || !destAvail {
		side := "source"
		if srcAvail {
			side = "destination"
		} else if !destAvail {
			side = "source and destination"
		}
		sec.Warnings = append(sec.Warnings,
			fmt.Sprintf("%s unavailable on %s — comparison skipped", name, side))
		return sec, true
	}
	return sec, false
}

func sortSectionDiff(sec *SectionDiff) {
	// Detail is the secondary key: entries can legitimately share a Key
	// (e.g. one cron command scheduled several times), and without a full
	// order the map-iteration order of their producer would leak into the
	// output, breaking the determinism contract.
	byKeyDetail := func(entries []DiffEntry) func(i, j int) bool {
		return func(i, j int) bool {
			if entries[i].Key != entries[j].Key {
				return entries[i].Key < entries[j].Key
			}
			return entries[i].Detail < entries[j].Detail
		}
	}
	sort.Slice(sec.Added, byKeyDetail(sec.Added))
	sort.Slice(sec.Removed, byKeyDetail(sec.Removed))
	sort.Slice(sec.Changed, func(i, j int) bool {
		if sec.Changed[i].Key != sec.Changed[j].Key {
			return sec.Changed[i].Key < sec.Changed[j].Key
		}
		return sec.Changed[i].Field < sec.Changed[j].Field
	})
	sort.Strings(sec.Warnings)
}

// ---------------------------------------------------------------------------
// Per-section item adapters
// ---------------------------------------------------------------------------

func domainItems(in []DomainEntry) []keyedItem {
	out := make([]keyedItem, 0, len(in))
	for _, e := range in {
		out = append(out, keyedItem{
			key:    e.Name,
			detail: e.Type,
			fields: map[string]string{"type": e.Type, "document_root": e.DocumentRoot},
		})
	}
	return out
}

// mailboxItems compares existence only: disk usage is volatile noise for a
// migration diff.
func mailboxItems(in []MailboxEntry) []keyedItem {
	out := make([]keyedItem, 0, len(in))
	for _, e := range in {
		out = append(out, keyedItem{key: e.Email})
	}
	return out
}

func databaseItems(in []DatabaseEntry) []keyedItem {
	out := make([]keyedItem, 0, len(in))
	for _, e := range in {
		users := append([]string(nil), e.Users...)
		sort.Strings(users)
		out = append(out, keyedItem{
			key:    e.Name,
			fields: map[string]string{"users": strings.Join(users, ",")},
		})
	}
	return out
}

// forwarderItems: the key IS the whole content, so any change is
// added+removed.
func forwarderItems(in []ForwarderEntry) []keyedItem {
	out := make([]keyedItem, 0, len(in))
	for _, e := range in {
		out = append(out, keyedItem{key: e.Domain + " | " + e.Source + " -> " + e.Destination})
	}
	return out
}

func autoresponderItems(in []AutoresponderEntry) []keyedItem {
	out := make([]keyedItem, 0, len(in))
	for _, e := range in {
		out = append(out, keyedItem{
			key: e.Domain + " | " + e.Email,
			fields: map[string]string{
				"subject":  e.Subject,
				"interval": strconv.Itoa(e.Interval),
			},
		})
	}
	return out
}

func ftpItems(in []FTPEntry) []keyedItem {
	out := make([]keyedItem, 0, len(in))
	for _, e := range in {
		out = append(out, keyedItem{
			key:    e.Login,
			fields: map[string]string{"type": e.Type, "dir": e.Dir},
		})
	}
	return out
}

func sslItems(in []SSLEntry) []keyedItem {
	out := make([]keyedItem, 0, len(in))
	for _, e := range in {
		out = append(out, keyedItem{
			key:    e.Domains,
			detail: e.Issuer,
			fields: map[string]string{
				"issuer":          e.Issuer,
				"validation_type": e.ValidationType,
				"is_self_signed":  strconv.FormatBool(e.IsSelfSigned),
				"valid_until":     strconv.FormatInt(e.ValidUntil, 10),
			},
		})
	}
	return out
}

func phpItems(in []PHPEntry) []keyedItem {
	out := make([]keyedItem, 0, len(in))
	for _, e := range in {
		out = append(out, keyedItem{
			key:    e.Domain,
			detail: e.Version,
			fields: map[string]string{"version": e.Version},
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// DNS: zone-aware, order-insensitive comparison
// ---------------------------------------------------------------------------

// dnsValue carries the two renderings of one record's comparable value:
// compare is unambiguous (NUL-framed fields — free-text TXT values that
// happen to contain "prio=…"/"ttl=…" cannot collide with a genuinely
// prioritized record), display is the human form used in reports.
type dnsValue struct {
	compare string
	display string
}

func canonicalDNSValue(r DNSRecordEntry) dnsValue {
	display := r.Value
	if r.Priority != 0 {
		display = fmt.Sprintf("prio=%d %s", r.Priority, display)
	}
	display = fmt.Sprintf("%s ttl=%d", display, r.TTL)
	return dnsValue{
		compare: fmt.Sprintf("%d\x00%d\x00%s", r.Priority, r.TTL, r.Value),
		display: display,
	}
}

func diffDNS(src, dest DNSSection) SectionDiff {
	if sec, skipped := skipUnavailable("dns", src.Available, dest.Available); skipped {
		return sec
	}
	sec := newSectionDiff()

	srcZones := map[string]DNSZoneResult{}
	for _, z := range src.Zones {
		srcZones[z.Zone] = z
	}
	destZones := map[string]DNSZoneResult{}
	for _, z := range dest.Zones {
		destZones[z.Zone] = z
	}

	for name, z := range destZones {
		if _, ok := srcZones[name]; !ok {
			sec.Added = append(sec.Added, DiffEntry{
				Key: "zone " + name, Detail: fmt.Sprintf("%d record(s)", len(z.Records)),
			})
		}
	}
	for name, sz := range srcZones {
		dz, ok := destZones[name]
		if !ok {
			sec.Removed = append(sec.Removed, DiffEntry{
				Key: "zone " + name, Detail: fmt.Sprintf("%d record(s)", len(sz.Records)),
			})
			continue
		}
		if !sz.Available || !dz.Available {
			sec.Warnings = append(sec.Warnings,
				fmt.Sprintf("zone %s unavailable on one side — records not compared", name))
			continue
		}
		diffZoneRecords(&sec, name, sz.Records, dz.Records)
	}

	sortSectionDiff(&sec)
	return sec
}

// diffZoneRecords groups records by (type, name) and compares the sorted
// canonical value-sets: same group with different values is a "changed"
// entry, a group present on one side only is added/removed. Equality is
// decided on the unambiguous compare form; reports show the display form.
func diffZoneRecords(sec *SectionDiff, zone string, src, dest []DNSRecordEntry) {
	group := func(records []DNSRecordEntry) map[string][]dnsValue {
		g := map[string][]dnsValue{}
		for _, r := range records {
			k := r.Type + " " + r.Name
			g[k] = append(g[k], canonicalDNSValue(r))
		}
		for k := range g {
			sort.Slice(g[k], func(i, j int) bool { return g[k][i].compare < g[k][j].compare })
		}
		return g
	}
	compareSet := func(values []dnsValue) string {
		parts := make([]string, 0, len(values))
		for _, v := range values {
			parts = append(parts, v.compare)
		}
		return strings.Join(parts, "\x01")
	}
	displaySet := func(values []dnsValue) string {
		parts := make([]string, 0, len(values))
		for _, v := range values {
			parts = append(parts, v.display)
		}
		return strings.Join(parts, "; ")
	}
	sg, dg := group(src), group(dest)

	for k, values := range dg {
		if _, ok := sg[k]; !ok {
			sec.Added = append(sec.Added, DiffEntry{
				Key: "zone " + zone + " " + k, Detail: displaySet(values),
			})
		}
	}
	for k, srcValues := range sg {
		destValues, ok := dg[k]
		if !ok {
			sec.Removed = append(sec.Removed, DiffEntry{
				Key: "zone " + zone + " " + k, Detail: displaySet(srcValues),
			})
			continue
		}
		if compareSet(srcValues) != compareSet(destValues) {
			sec.Changed = append(sec.Changed, DiffFieldChange{
				Key:         "zone " + zone + " " + k,
				Field:       "records",
				Source:      displaySet(srcValues),
				Destination: displaySet(destValues),
			})
		}
	}
}

// ---------------------------------------------------------------------------
// Cron: compared by command_sha256, never by (nonexistent) raw command
// ---------------------------------------------------------------------------

func cronSchedule(j CronJobEntry) string {
	if j.Type == "macro" {
		return j.Macro
	}
	return strings.Join([]string{j.Minute, j.Hour, j.DayOfMonth, j.Month, j.DayOfWeek}, " ")
}

func diffCron(src, dest CronSection) SectionDiff {
	if sec, skipped := skipUnavailable("cron", src.Available, dest.Available); skipped {
		return sec
	}
	sec := newSectionDiff()

	group := func(jobs []CronJobEntry) map[string][]CronJobEntry {
		g := map[string][]CronJobEntry{}
		for _, j := range jobs {
			g[j.CommandSHA256] = append(g[j.CommandSHA256], j)
		}
		return g
	}
	sg, dg := group(src.Jobs), group(dest.Jobs)

	// The command hash is computed over the redacted command, so hash and
	// redacted text identify each other 1:1 — the readable redacted form
	// is used as the diff key.
	keyOf := func(jobs []CronJobEntry) string { return jobs[0].CommandRedacted }

	for sha, jobs := range dg {
		if _, ok := sg[sha]; !ok {
			for _, j := range jobs {
				sec.Added = append(sec.Added, DiffEntry{Key: keyOf(jobs), Detail: cronSchedule(j)})
			}
		}
	}
	for sha, srcJobs := range sg {
		destJobs, ok := dg[sha]
		if !ok {
			for _, j := range srcJobs {
				sec.Removed = append(sec.Removed, DiffEntry{Key: keyOf(srcJobs), Detail: cronSchedule(j)})
			}
			continue
		}
		key := keyOf(srcJobs)
		if len(srcJobs) == 1 && len(destJobs) == 1 {
			if s, d := cronSchedule(srcJobs[0]), cronSchedule(destJobs[0]); s != d {
				sec.Changed = append(sec.Changed, DiffFieldChange{
					Key: key, Field: "schedule", Source: s, Destination: d,
				})
			}
			if srcJobs[0].Enabled != destJobs[0].Enabled {
				sec.Changed = append(sec.Changed, DiffFieldChange{
					Key: key, Field: "enabled",
					Source:      strconv.FormatBool(srcJobs[0].Enabled),
					Destination: strconv.FormatBool(destJobs[0].Enabled),
				})
			}
			continue
		}
		// Same command scheduled multiple times: compare the multiset of
		// schedule|enabled slots.
		slot := func(j CronJobEntry) string {
			return cronSchedule(j) + " enabled=" + strconv.FormatBool(j.Enabled)
		}
		srcSlots := map[string]int{}
		for _, j := range srcJobs {
			srcSlots[slot(j)]++
		}
		destSlots := map[string]int{}
		for _, j := range destJobs {
			destSlots[slot(j)]++
		}
		for s, n := range destSlots {
			for i := srcSlots[s]; i < n; i++ {
				sec.Added = append(sec.Added, DiffEntry{Key: key, Detail: s})
			}
		}
		for s, n := range srcSlots {
			for i := destSlots[s]; i < n; i++ {
				sec.Removed = append(sec.Removed, DiffEntry{Key: key, Detail: s})
			}
		}
	}

	sortSectionDiff(&sec)
	return sec
}
