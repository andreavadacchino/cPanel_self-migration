package accountinventory

import (
	"fmt"
	"sort"
	"strings"
)

// DNS verify (PR 6C). VerifyDNSPlan is fully offline: it consumes a DNS
// import plan (PR 6B) and the live DESTINATION zones (re-fetched by the
// `dns verify` command) and reports, per planned op, whether the zone
// matches the plan. It reuses the plan's own comparison machinery
// (groupRRSets, planValue, valuesEqual, canonDNSName) so verify can never
// disagree with the plan about what "equal" means; values only, TTL is
// never compared (plan rule). Design: docs/dev/PR6C_DNS_VERIFY_DESIGN.md.

// Verify op statuses. `pending` means the zone still matches the
// plan-time state (nothing happened yet): legitimate before an apply,
// a failure after one — it gates either way, the report counts it
// separately so both readings stay clear.
const (
	VerifyStatusApplied      = "applied"       // add/replace landed: live equals the desired values
	VerifyStatusUnchanged    = "unchanged"     // checkable skip: live still equals the plan-time state
	VerifyStatusPending      = "pending"       // live still equals the plan-time state (add missing / replace old)
	VerifyStatusDrift        = "drift"         // live matches neither desired nor plan-time state
	VerifyStatusManualReview = "manual_review" // manual op: reported for the human, never gates
	VerifyStatusNotChecked   = "not_checked"   // excluded skip (SOA, host-validation): no expectation
)

// VerifyOpResult is the verdict for one planned op.
type VerifyOpResult struct {
	Action         string   `json:"action"`
	Type           string   `json:"type"`
	Name           string   `json:"name"`
	Status         string   `json:"status"`
	Reason         string   `json:"reason,omitempty"`
	ExpectedValues []string `json:"expected_values,omitempty"`
	ObservedValues []string `json:"observed_values,omitempty"`
}

// VerifyZoneReport is the verdict for one plan zone.
type VerifyZoneReport struct {
	Zone       string           `json:"zone"`
	Available  bool             `json:"available"`
	Method     string           `json:"method,omitempty"`
	FetchError string           `json:"fetch_error,omitempty"`
	Ops        []VerifyOpResult `json:"ops"`
	// Untracked lists live rrsets of actionable types that appear in
	// neither the zone's ops nor its plan-time informational set: they
	// postdate the plan. Informational, never gating (additive posture).
	Untracked []PlanRRSetInfo `json:"untracked,omitempty"`
}

type VerifySummary struct {
	Applied          int `json:"applied"`
	Unchanged        int `json:"unchanged"`
	Pending          int `json:"pending"`
	Drift            int `json:"drift"`
	ManualReview     int `json:"manual_review"`
	NotChecked       int `json:"not_checked"`
	Untracked        int `json:"untracked"`
	UnavailableZones int `json:"unavailable_zones"`
	ManualZones      int `json:"manual_zones"`
}

type DNSVerifyReport struct {
	Mode          string             `json:"mode"`
	FormatVersion int                `json:"format_version"`
	GeneratedAt   string             `json:"generated_at,omitempty"`
	PlanFile      string             `json:"plan_file,omitempty"`
	PlanSHA256    string             `json:"plan_sha256,omitempty"`
	Zones         []VerifyZoneReport `json:"zones"`
	// ManualZones is the plan's list, passed through: the plan computed
	// no ops for them, so their migration state is unknown — they gate.
	ManualZones []ManualZone  `json:"manual_zones,omitempty"`
	Summary     VerifySummary `json:"summary"`
	// Clean is the gate predicate: no pending, no drift, no unavailable
	// zone, no manual zone. Manual OPS and untracked rrsets never gate
	// (gating on manual ops — NS differs in every real migration — would
	// deadlock --fail-on-drift, the exact mistake the 6A v2 redesign
	// removed from the plan gate).
	Clean bool `json:"clean"`
}

// VerifyDNSPlan compares the live destination zones against the plan.
// live is keyed by lowercase zone name; a plan zone missing from the map
// is treated as unavailable (fail-safe: cannot verify ⇒ not verified).
func VerifyDNSPlan(plan DNSPlan, live map[string]DNSZoneResult) DNSVerifyReport {
	rep := DNSVerifyReport{
		Mode:          "dns-verify",
		FormatVersion: 1,
		Zones:         []VerifyZoneReport{},
	}

	for _, pz := range plan.Zones {
		zr := VerifyZoneReport{Zone: pz.Zone, Ops: []VerifyOpResult{}}
		lz, ok := live[strings.ToLower(pz.Zone)]
		if !ok || !lz.Available {
			zr.Available = false
			zr.Method = "unavailable"
			zr.FetchError = zoneFetchError(lz, ok)
			rep.Summary.UnavailableZones++
			rep.Zones = append(rep.Zones, zr)
			continue
		}
		zr.Available = true
		zr.Method = lz.Method

		liveSets := groupRRSets(lz.Records, pz.Zone)
		known := map[rrsetKey]bool{}
		for _, op := range pz.Ops {
			known[rrsetKey{Type: op.Type, Name: op.Name}] = true
			res := verifyOp(op, liveSets, pz.Zone)
			zr.Ops = append(zr.Ops, res)
			switch res.Status {
			case VerifyStatusApplied:
				rep.Summary.Applied++
			case VerifyStatusUnchanged:
				rep.Summary.Unchanged++
			case VerifyStatusPending:
				rep.Summary.Pending++
			case VerifyStatusDrift:
				rep.Summary.Drift++
			case VerifyStatusManualReview:
				rep.Summary.ManualReview++
			case VerifyStatusNotChecked:
				rep.Summary.NotChecked++
			}
		}
		for _, info := range pz.Informational {
			known[rrsetKey{Type: info.Type, Name: info.Name}] = true
		}
		for k, records := range liveSets {
			if known[k] || !actionableTypes[k.Type] || isHostValidationName(k.Name) {
				continue
			}
			zr.Untracked = append(zr.Untracked, PlanRRSetInfo{
				Type: k.Type, Name: k.Name, Values: sortedValues(records, pz.Zone)})
		}
		sort.Slice(zr.Untracked, func(i, j int) bool {
			if zr.Untracked[i].Name != zr.Untracked[j].Name {
				return zr.Untracked[i].Name < zr.Untracked[j].Name
			}
			return zr.Untracked[i].Type < zr.Untracked[j].Type
		})
		rep.Summary.Untracked += len(zr.Untracked)
		rep.Zones = append(rep.Zones, zr)
	}

	rep.ManualZones = append(rep.ManualZones, plan.ManualZones...)
	rep.Summary.ManualZones = len(rep.ManualZones)

	rep.Clean = rep.Summary.Pending == 0 &&
		rep.Summary.Drift == 0 &&
		rep.Summary.UnavailableZones == 0 &&
		rep.Summary.ManualZones == 0
	return rep
}

// zoneFetchError renders why a zone could not be verified.
func zoneFetchError(lz DNSZoneResult, fetched bool) string {
	if !fetched {
		return "zone was not fetched from the destination"
	}
	if len(lz.Warnings) > 0 {
		return strings.Join(lz.Warnings, "; ")
	}
	if len(lz.Errors) > 0 {
		return strings.Join(lz.Errors, "; ")
	}
	return "zone unavailable on the destination"
}

// verifyOp applies the status table of the 6C design to one planned op.
// Every unexpected shape degrades to drift with a reason — fail-safe: a
// malformed or hand-edited plan can never verify clean.
func verifyOp(op PlanOp, liveSets map[rrsetKey][]DNSRecordEntry, zone string) VerifyOpResult {
	res := VerifyOpResult{Action: op.Action, Type: op.Type, Name: op.Name}
	liveRecords, present := liveSets[rrsetKey{Type: op.Type, Name: op.Name}]
	var observed []string
	if present {
		observed = sortedValues(liveRecords, zone)
		res.ObservedValues = observed
	}

	switch op.Action {
	case ActionManual:
		res.Status = VerifyStatusManualReview
		res.Reason = op.Reason
		return res

	case ActionSkip:
		if op.Type == "SOA" || isHostValidationName(op.Name) {
			res.Status = VerifyStatusNotChecked
			return res
		}
		if len(op.DestinationValues) == 0 {
			// A checkable skip always has plan-time destination values by
			// construction (equality implies the rrset existed).
			res.Status = VerifyStatusDrift
			res.Reason = "skip op carries no plan-time destination values — malformed or hand-edited plan"
			return res
		}
		res.ExpectedValues = op.DestinationValues
		if present && valuesEqual(observed, op.DestinationValues) {
			res.Status = VerifyStatusUnchanged
			return res
		}
		res.Status = VerifyStatusDrift
		res.Reason = "live rrset no longer matches the plan-time destination state"
		return res

	case ActionAdd, ActionReplace:
		desired, err := desiredValues(op)
		if err != nil {
			res.Status = VerifyStatusDrift
			res.Reason = err.Error()
			return res
		}
		res.ExpectedValues = desired
		switch {
		case present && valuesEqual(observed, desired):
			res.Status = VerifyStatusApplied
		case op.Action == ActionAdd && !present:
			res.Status = VerifyStatusPending
			res.Reason = "rrset still missing on the destination (plan-time state)"
		case op.Action == ActionReplace && present && valuesEqual(observed, op.DestinationValues):
			res.Status = VerifyStatusPending
			res.Reason = "rrset still carries the plan-time destination values"
		default:
			res.Status = VerifyStatusDrift
			res.Reason = "live rrset matches neither the desired nor the plan-time state"
		}
		return res

	default:
		res.Status = VerifyStatusDrift
		res.Reason = fmt.Sprintf("unknown plan action %q — malformed or hand-edited plan", op.Action)
		return res
	}
}

// desiredValues derives the comparable values of an add/replace op from
// its write-shaped records — the exact inverse of translateRecords'
// encoding (A/AAAA/CNAME: single datum; MX: preference + exchange; TXT:
// RFC 1035 segments rejoined by plain concatenation).
func desiredValues(op PlanOp) ([]string, error) {
	if len(op.Records) == 0 {
		return nil, fmt.Errorf("%s op carries no desired records — malformed or hand-edited plan", op.Action)
	}
	vals := make([]string, 0, len(op.Records))
	for _, r := range op.Records {
		if len(r.Data) == 0 {
			return nil, fmt.Errorf("desired %s record has empty data — malformed or hand-edited plan", r.Type)
		}
		switch r.Type {
		case "MX":
			if len(r.Data) != 2 {
				return nil, fmt.Errorf("desired MX record has %d data fields, want 2 (preference, exchange)", len(r.Data))
			}
			vals = append(vals, r.Data[0]+"\x00"+r.Data[1])
		case "TXT":
			vals = append(vals, strings.Join(r.Data, ""))
		default: // A, AAAA, CNAME — one datum each
			if len(r.Data) != 1 {
				return nil, fmt.Errorf("desired %s record has %d data fields, want 1", r.Type, len(r.Data))
			}
			vals = append(vals, r.Data[0])
		}
	}
	sort.Strings(vals)
	return vals, nil
}
