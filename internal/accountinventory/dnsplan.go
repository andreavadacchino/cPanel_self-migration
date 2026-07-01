package accountinventory

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// DNS import plan (PR 6B). BuildDNSPlan is fully offline: it consumes
// two normalized inventories and produces a reviewable plan of what a
// future apply (PR 6D) would write into the DESTINATION account's DNS
// zones. It never connects anywhere and never generates delete ops for
// destination records; the design and its safety rules live in
// docs/dev/PR6A_DNS_IMPORT_DESIGN.md and PR6B_PRE_CAPTURES.md.

const (
	ActionAdd     = "add"     // rrset missing on destination
	ActionReplace = "replace" // rrset present with different values
	ActionManual  = "manual"  // the tool refuses to touch it (terminal)
	ActionSkip    = "skip"    // identical after translation, or excluded
)

// planWriteTTLCap bounds the TTL written to the destination so a wrong
// record or a rollback cannot live in resolver caches for hours.
const planWriteTTLCap = 3600

// txtSegmentLen is the RFC 1035 character-string limit; TXT data is
// written as pre-split segments, mirroring what parse_zone returns.
const txtSegmentLen = 255

// hostValidationPrefixes are owner-name leftmost labels of transient,
// host-specific validation records (AutoSSL / DCV) that must never be
// migrated (real-server finding, PR6B_PRE_CAPTURES.md).
var hostValidationPrefixes = []string{"_acme-challenge", "_cpanel-dcv-test-record"}

// actionableTypes are the record types the tool knows how to round-trip.
var actionableTypes = map[string]bool{"A": true, "AAAA": true, "CNAME": true, "MX": true, "TXT": true}

// PlanRecord is one desired record in the shape DNS::mass_edit_zone
// accepts (dname/ttl/record_type/data). Names are stored canonical
// (absolute, lowercase, trailing dot); the 6D writer converts to the
// server's expected format.
type PlanRecord struct {
	Name string   `json:"name"`
	Type string   `json:"type"`
	TTL  int      `json:"ttl"`
	Data []string `json:"data"`
}

// PlanOp is the decision for one source rrset (zone, type, name).
type PlanOp struct {
	Action            string       `json:"action"`
	Type              string       `json:"type"`
	Name              string       `json:"name"`
	Reason            string       `json:"reason,omitempty"`
	Records           []PlanRecord `json:"records,omitempty"`
	SourceValues      []string     `json:"source_values,omitempty"`
	DestinationValues []string     `json:"destination_values,omitempty"`
	TTLCapped         bool         `json:"ttl_capped,omitempty"`
}

// PlanRRSetInfo describes a destination-only rrset: listed so a human
// sees it, never deleted (additive posture).
type PlanRRSetInfo struct {
	Type   string   `json:"type"`
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

type PlanZone struct {
	Zone           string          `json:"zone"`
	Ops            []PlanOp        `json:"ops"`
	Informational  []PlanRRSetInfo `json:"informational,omitempty"`
	PolicyFindings []string        `json:"policy_findings,omitempty"`
}

// ManualZone is a zone the plan refuses to compute ops for.
type ManualZone struct {
	Zone   string `json:"zone"`
	Reason string `json:"reason"`
}

type PlanSummary struct {
	Add           int `json:"add"`
	Replace       int `json:"replace"`
	Manual        int `json:"manual"`
	Skip          int `json:"skip"`
	Informational int `json:"informational"`
}

type DNSPlan struct {
	Mode              string            `json:"mode"`
	FormatVersion     int               `json:"format_version"`
	GeneratedAt       string            `json:"generated_at"`
	SourceFile        string            `json:"source_file,omitempty"`
	SourceSHA256      string            `json:"source_sha256,omitempty"`
	DestinationFile   string            `json:"destination_file,omitempty"`
	DestinationSHA256 string            `json:"destination_sha256,omitempty"`
	PolicyFile        string            `json:"policy_file,omitempty"`
	IPMap             map[string]string `json:"ip_map"`
	Zones             []PlanZone        `json:"zones"`
	ManualZones       []ManualZone      `json:"manual_zones,omitempty"`
	NonDNSBlockers    []string          `json:"non_dns_blockers,omitempty"`
	Summary           PlanSummary       `json:"summary"`
}

// canonDNSName canonicalizes an owner name or target relative to a
// zone: lowercase, absolute FQDN with trailing dot. Real parse_zone
// data mixes absolute apex names with relative non-apex names
// (PR6B_PRE_CAPTURES.md); RFC semantics qualify relative names against
// the zone origin.
func canonDNSName(name, zone string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	z := strings.ToLower(strings.TrimSpace(zone))
	z = strings.TrimSuffix(z, ".") + "."
	switch n {
	case "", "@":
		return z
	}
	if strings.HasSuffix(n, ".") {
		return n
	}
	// A bare name equal to the zone is the apex, not a relative name:
	// real parse_zone emits the apex WITH the dot, but a degraded
	// upstream (hand-edited zone, different collector) must not turn
	// "example.com" into "example.com.example.com.".
	if n == strings.TrimSuffix(z, ".") {
		return z
	}
	return n + "." + z
}

// canonIPString returns the canonical textual form of an IP literal,
// or the lowercased input when it does not parse — failing safe: an
// unparsable address can never match an ip-map entry, so its rrset
// lands in manual.
func canonIPString(s string) string {
	if a, err := netip.ParseAddr(strings.TrimSpace(s)); err == nil {
		return a.String()
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// leftmostLabel returns the first DNS label of a canonical name.
func leftmostLabel(name string) string {
	if i := strings.IndexByte(name, '.'); i >= 0 {
		return name[:i]
	}
	return name
}

// splitTXTSegments splits TXT data into RFC 1035 character-string
// segments (≤255 bytes), the format both parse_zone and mass_edit_zone
// use. Cuts land on UTF-8 rune boundaries: a segment holding half a
// multi-byte rune would be invalid UTF-8 and encoding/json silently
// rewrites invalid bytes to U+FFFD when the plan is persisted — the
// rejoined value would no longer equal the source. Callers must reject
// non-UTF-8 input first (classify sends it to manual).
func splitTXTSegments(s string) []string {
	if s == "" {
		return []string{""}
	}
	var out []string
	for len(s) > txtSegmentLen {
		cut := txtSegmentLen
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 { // degenerate: no boundary in the window
			cut = txtSegmentLen
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return append(out, s)
}

// rrsetKey identifies an rrset inside one zone.
type rrsetKey struct {
	Type string
	Name string
}

// planValue renders one record's comparable value (TTL deliberately
// excluded: the plan acts on substance, TTL-only drift is a skip).
func planValue(r DNSRecordEntry, zone string) string {
	switch r.Type {
	case "A", "AAAA":
		return canonIPString(r.Address)
	case "CNAME", "NS":
		return canonDNSName(r.Target, zone)
	case "MX":
		return strconv.Itoa(r.Priority) + "\x00" + canonDNSName(r.Exchange, zone)
	case "TXT":
		return r.TxtData
	default:
		return r.Value
	}
}

func groupRRSets(records []DNSRecordEntry, zone string) map[rrsetKey][]DNSRecordEntry {
	g := map[rrsetKey][]DNSRecordEntry{}
	for _, r := range records {
		k := rrsetKey{Type: strings.ToUpper(r.Type), Name: canonDNSName(r.Name, zone)}
		g[k] = append(g[k], r)
	}
	return g
}

func sortedValues(records []DNSRecordEntry, zone string) []string {
	vals := make([]string, 0, len(records))
	for _, r := range records {
		vals = append(vals, planValue(r, zone))
	}
	sort.Strings(vals)
	return vals
}

func valuesEqual(a, b []string) bool {
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

// BuildDNSPlan computes the DNS import plan. policy is optional context
// (findings are cross-referenced, never gating — see the 6A design for
// why gating on the policy status would deadlock every real migration).
// ipMap maps source addresses to destination addresses; an A/AAAA rrset
// with any unmapped value becomes manual, without exception.
func BuildDNSPlan(src, dest NormalizedInventory, policy *PolicyReport, ipMap map[string]string) (DNSPlan, error) {
	if !src.DNS.Available {
		return DNSPlan{}, fmt.Errorf("source DNS section unavailable — re-run the inventory")
	}
	if !dest.DNS.Available {
		return DNSPlan{}, fmt.Errorf("destination DNS section unavailable — re-run the inventory")
	}
	// Both sides of every mapping go through netip canonicalization so
	// lookups are by address identity, not by spelling.
	canonMap := make(map[string]string, len(ipMap))
	for k, v := range ipMap {
		canonMap[canonIPString(k)] = canonIPString(v)
	}
	ipMap = canonMap

	// Zones is seeded non-nil so the JSON stays array-typed ("[]", not
	// null) even when every source zone lands in ManualZones — the
	// normal state of a fresh migration (diff.go convention).
	plan := DNSPlan{
		Mode:          "dns-import-plan",
		FormatVersion: 1,
		IPMap:         ipMap,
		Zones:         []PlanZone{},
	}

	destZones := map[string]DNSZoneResult{}
	for _, z := range dest.DNS.Zones {
		destZones[strings.ToLower(z.Zone)] = z
	}

	for _, sz := range src.DNS.Zones {
		zoneName := strings.ToLower(sz.Zone)
		if !sz.Available {
			plan.ManualZones = append(plan.ManualZones, ManualZone{
				Zone: zoneName, Reason: "zone unavailable on source — re-run the source inventory"})
			continue
		}
		dz, ok := destZones[zoneName]
		if !ok {
			plan.ManualZones = append(plan.ManualZones, ManualZone{
				Zone: zoneName, Reason: "zone missing on destination — create it via WHM/park first, then re-run"})
			continue
		}
		if !dz.Available {
			plan.ManualZones = append(plan.ManualZones, ManualZone{
				Zone: zoneName, Reason: "zone unavailable on destination — re-run the destination inventory"})
			continue
		}
		pz := buildZonePlan(zoneName, sz.Records, dz.Records, ipMap)
		pz.PolicyFindings = zonePolicyFindings(policy, zoneName)
		plan.Zones = append(plan.Zones, pz)
	}

	plan.NonDNSBlockers = nonDNSBlockers(policy)

	sort.Slice(plan.Zones, func(i, j int) bool { return plan.Zones[i].Zone < plan.Zones[j].Zone })
	sort.Slice(plan.ManualZones, func(i, j int) bool { return plan.ManualZones[i].Zone < plan.ManualZones[j].Zone })

	for _, z := range plan.Zones {
		for _, op := range z.Ops {
			switch op.Action {
			case ActionAdd:
				plan.Summary.Add++
			case ActionReplace:
				plan.Summary.Replace++
			case ActionManual:
				plan.Summary.Manual++
			case ActionSkip:
				plan.Summary.Skip++
			}
		}
		plan.Summary.Informational += len(z.Informational)
	}
	return plan, nil
}

func buildZonePlan(zone string, srcRecords, destRecords []DNSRecordEntry, ipMap map[string]string) PlanZone {
	pz := PlanZone{Zone: zone, Ops: []PlanOp{}}
	srcSets := groupRRSets(srcRecords, zone)
	destSets := groupRRSets(destRecords, zone)

	// Owner names that carry a CNAME on either side: nothing else may
	// coexist there (and the tool never deletes), so any cross-type op
	// at such a name is forced manual.
	cnameNames := map[string]bool{}
	typesAt := map[string]map[string]bool{}
	for _, sets := range []map[rrsetKey][]DNSRecordEntry{srcSets, destSets} {
		for k := range sets {
			if k.Type == "CNAME" {
				cnameNames[k.Name] = true
			}
			if typesAt[k.Name] == nil {
				typesAt[k.Name] = map[string]bool{}
			}
			typesAt[k.Name][k.Type] = true
		}
	}
	crossTypeConflict := func(k rrsetKey) bool {
		if !cnameNames[k.Name] {
			return false
		}
		for t := range typesAt[k.Name] {
			if t != k.Type {
				return true
			}
		}
		return false
	}

	for k, records := range srcSets {
		op := PlanOp{Type: k.Type, Name: k.Name, SourceValues: sortedValues(records, zone)}
		if dv, ok := destSets[k]; ok {
			op.DestinationValues = sortedValues(dv, zone)
		}
		classify(&op, k, records, destSets, ipMap, zone, crossTypeConflict)
		pz.Ops = append(pz.Ops, op)
	}

	for k, records := range destSets {
		if _, ok := srcSets[k]; ok {
			continue
		}
		pz.Informational = append(pz.Informational, PlanRRSetInfo{
			Type: k.Type, Name: k.Name, Values: sortedValues(records, zone)})
	}

	sort.Slice(pz.Ops, func(i, j int) bool {
		if pz.Ops[i].Name != pz.Ops[j].Name {
			return pz.Ops[i].Name < pz.Ops[j].Name
		}
		return pz.Ops[i].Type < pz.Ops[j].Type
	})
	sort.Slice(pz.Informational, func(i, j int) bool {
		if pz.Informational[i].Name != pz.Informational[j].Name {
			return pz.Informational[i].Name < pz.Informational[j].Name
		}
		return pz.Informational[i].Type < pz.Informational[j].Type
	})
	return pz
}

// classify applies the 6A rule table to one source rrset, in order:
// exclusions first (SOA, host-validation, NS, unsupported, CNAME
// conflicts), then translation gates (unmapped A/AAAA, TXT with mapped
// IPs), then the value comparison that yields skip/add/replace.
func classify(op *PlanOp, k rrsetKey, records []DNSRecordEntry, destSets map[rrsetKey][]DNSRecordEntry, ipMap map[string]string, zone string, crossTypeConflict func(rrsetKey) bool) {
	switch {
	case k.Type == "SOA":
		op.Action, op.Reason = ActionSkip, "SOA is server-managed — never compared or written"
		return
	case isHostValidationName(k.Name):
		op.Action, op.Reason = ActionSkip, "host-specific validation record (AutoSSL/DCV) — not migrated"
		return
	case k.Type == "NS":
		if dv, ok := destSets[k]; ok && valuesEqual(op.SourceValues, sortedValues(dv, zone)) {
			op.Action = ActionSkip
			return
		}
		op.Action, op.Reason = ActionManual, "NS/delegation is registrar/WHM territory — never written"
		return
	case !actionableTypes[k.Type]:
		op.Action, op.Reason = ActionManual, fmt.Sprintf("record type %s is not supported for import", k.Type)
		return
	case crossTypeConflict(k):
		op.Action, op.Reason = ActionManual, "CNAME cannot coexist with other types at the same name — resolve by hand"
		return
	}

	translated, unmapped := translateRecords(records, k.Type, ipMap, zone)
	if len(unmapped) > 0 {
		op.Action = ActionManual
		op.Reason = fmt.Sprintf("unmapped address(es) %s — add --ip-map entries (identity mapping to copy verbatim)",
			strings.Join(unmapped, ", "))
		return
	}
	if k.Type == "TXT" {
		for _, r := range records {
			if !utf8.ValidString(r.TxtData) {
				op.Action = ActionManual
				op.Reason = "TXT value is not valid UTF-8 — it cannot be represented faithfully in the plan file"
				return
			}
		}
		// Substring match is deliberately naive: a false positive only
		// costs a manual review (fail-safe), a miss would ship a stale
		// address inside SPF.
		matched := map[string]bool{}
		for _, r := range records {
			for from := range ipMap {
				if strings.Contains(r.TxtData, from) {
					matched[from] = true
				}
			}
		}
		if len(matched) > 0 {
			addrs := make([]string, 0, len(matched))
			for a := range matched {
				addrs = append(addrs, a)
			}
			sort.Strings(addrs)
			op.Action = ActionManual
			op.Reason = fmt.Sprintf("TXT value contains mapped source address(es) %s (SPF?) — rewrite by hand",
				strings.Join(addrs, ", "))
			return
		}
	}

	desiredValues := make([]string, 0, len(translated))
	for _, tr := range translated {
		desiredValues = append(desiredValues, tr.value)
	}
	sort.Strings(desiredValues)

	if dv, ok := destSets[k]; ok {
		if valuesEqual(desiredValues, sortedValues(dv, zone)) {
			op.Action = ActionSkip
			return
		}
		op.Action = ActionReplace
	} else {
		op.Action = ActionAdd
	}
	for _, tr := range translated {
		op.Records = append(op.Records, tr.record)
		if tr.capped {
			op.TTLCapped = true
		}
	}
	sort.Slice(op.Records, func(i, j int) bool {
		return strings.Join(op.Records[i].Data, "\x00") < strings.Join(op.Records[j].Data, "\x00")
	})
}

func isHostValidationName(canonical string) bool {
	label := leftmostLabel(canonical)
	for _, p := range hostValidationPrefixes {
		if label == p {
			return true
		}
	}
	return false
}

type translatedRecord struct {
	record PlanRecord
	value  string
	capped bool
}

// translateRecords builds the desired write-shaped records for one
// rrset, applying the ip-map to A/AAAA and the TTL cap to everything.
// It returns the sorted list of unmapped addresses when translation is
// impossible.
func translateRecords(records []DNSRecordEntry, typ string, ipMap map[string]string, zone string) ([]translatedRecord, []string) {
	var out []translatedRecord
	unmappedSet := map[string]bool{}
	for _, r := range records {
		ttl, capped := capTTL(r.TTL)
		rec := PlanRecord{Name: canonDNSName(r.Name, zone), Type: typ, TTL: ttl}
		var value string
		switch typ {
		case "A", "AAAA":
			// Lookups go through netip canonical form so textually
			// different spellings of the same address (IPv6 zero
			// compression, case) still match; an unparsable address
			// fails safe into manual.
			addr := canonIPString(r.Address)
			to, ok := ipMap[addr]
			if !ok {
				unmappedSet[addr] = true
				continue
			}
			to = canonIPString(to)
			rec.Data = []string{to}
			value = to
		case "CNAME":
			t := canonDNSName(r.Target, zone)
			rec.Data = []string{t}
			value = t
		case "MX":
			ex := canonDNSName(r.Exchange, zone)
			rec.Data = []string{strconv.Itoa(r.Priority), ex}
			value = strconv.Itoa(r.Priority) + "\x00" + ex
		case "TXT":
			rec.Data = splitTXTSegments(r.TxtData)
			value = r.TxtData
		}
		out = append(out, translatedRecord{record: rec, value: value, capped: capped})
	}
	var unmapped []string
	for a := range unmappedSet {
		unmapped = append(unmapped, a)
	}
	sort.Strings(unmapped)
	return out, unmapped
}

// capTTL implements the design's min(source TTL, 3600) with one
// documented deviation: TTL <= 0 is treated as MISSING collector data
// (parse_zone always carries a positive ttl on real records; a zero
// here means the field never decoded) and defaults to the cap rather
// than writing an accidental "do not cache". The capped flag surfaces
// both cases in the plan so the reviewer sees the rewrite.
func capTTL(ttl int) (int, bool) {
	if ttl <= 0 || ttl > planWriteTTLCap {
		return planWriteTTLCap, true
	}
	return ttl, false
}

// zonePolicyFindings extracts the POL-DNS-* findings that reference the
// zone, formatted for the plan (context for the reviewer, never a gate).
func zonePolicyFindings(policy *PolicyReport, zone string) []string {
	if policy == nil {
		return nil
	}
	var out []string
	prefix := "zone " + zone
	for _, f := range policy.Findings {
		if f.Section != "dns" {
			continue
		}
		ref := f.SourceRef
		if ref == "" {
			ref = f.DestinationRef
		}
		if ref == "" {
			ref = f.Detail
		}
		if ref != prefix && !strings.HasPrefix(ref, prefix+" ") {
			continue
		}
		out = append(out, fmt.Sprintf("%s [%s] %s", f.ID, f.Severity, ref))
	}
	sort.Strings(out)
	return out
}

// nonDNSBlockers lists blocker findings outside the dns section: they
// concern other migration steps and are surfaced as context only.
func nonDNSBlockers(policy *PolicyReport) []string {
	if policy == nil {
		return nil
	}
	var out []string
	for _, f := range policy.Findings {
		if f.Severity == SeverityBlocker && f.Section != "dns" {
			out = append(out, fmt.Sprintf("%s (%s)", f.ID, f.Section))
		}
	}
	sort.Strings(out)
	return out
}
