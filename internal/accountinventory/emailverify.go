package accountinventory

import (
	"fmt"
	"sort"
	"strings"
)

// Email verify (PR 2B-1). VerifyEmailPlan is fully offline: it consumes
// an email apply plan and the live DESTINATION state (re-fetched by the
// `email verify` command) and reports, per planned op, whether the
// destination matches the plan. It reuses the plan's own comparison
// machinery (canonEmailAddr, defaultsEquivalent) so verify can never
// disagree with the plan about what "equal" means. Mirror of dnsverify.go.

// Verify op statuses (dnsverify vocabulary; `pending` = the destination
// still matches the plan-time state — legitimate before an apply, a
// failure after one; it gates either way).
const (
	EmailVerifyApplied      = "applied"
	EmailVerifyUnchanged    = "unchanged"
	EmailVerifyPending      = "pending"
	EmailVerifyDrift        = "drift"
	EmailVerifyManualReview = "manual_review" // manual op: reported for the human, never gates
	EmailVerifyNotChecked   = "not_checked"   // 2B-2/2B-3 sections: verify does not re-list them yet
	EmailVerifyUnavailable  = "unavailable"   // the fresh re-list failed — cannot verify ⇒ not verified
)

// EmailVerifyOpResult is the verdict for one planned op.
type EmailVerifyOpResult struct {
	Section  string `json:"section"`
	Action   string `json:"action"`
	Domain   string `json:"domain"`
	Key      string `json:"key"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
	Expected string `json:"expected,omitempty"`
	Observed string `json:"observed,omitempty"`
}

type EmailVerifySummary struct {
	Applied        int `json:"applied"`
	Unchanged      int `json:"unchanged"`
	Pending        int `json:"pending"`
	Drift          int `json:"drift"`
	ManualReview   int `json:"manual_review"`
	NotChecked     int `json:"not_checked"`
	Unavailable    int `json:"unavailable"`
	Untracked      int `json:"untracked"`
	ManualSections int `json:"manual_sections"`
}

type EmailVerifyReport struct {
	Mode          string                `json:"mode"`
	FormatVersion int                   `json:"format_version"`
	GeneratedAt   string                `json:"generated_at,omitempty"`
	PlanFile      string                `json:"plan_file,omitempty"`
	PlanSHA256    string                `json:"plan_sha256,omitempty"`
	Ops           []EmailVerifyOpResult `json:"ops"`
	// Untracked lists live 2B-1-section items that appear in neither the
	// plan's ops nor its informational set: they postdate the plan.
	// Informational, never gating (additive posture). Scope note: verify
	// re-lists only the domains the plan knows about — items on domains
	// the plan never saw are out of scope (dns verify has the same
	// plan-scoped coverage).
	Untracked []EmailPlanInfo `json:"untracked,omitempty"`
	// ManualSections is the plan's list, passed through: the plan
	// computed no ops for them, so their state is unknown — they gate.
	ManualSections []EmailManualSection `json:"manual_sections,omitempty"`
	Summary        EmailVerifySummary   `json:"summary"`
	// Clean is the gate predicate: no pending, no drift, no unavailable
	// op, no manual section. Manual OPS and untracked items never gate
	// (the 6A/6C precedent: gating on manual would deadlock every real
	// migration).
	Clean bool `json:"clean"`
}

// VerifyEmailPlan compares the live destination state against the plan.
func VerifyEmailPlan(plan EmailApplyPlan, live EmailLiveState) EmailVerifyReport {
	rep := EmailVerifyReport{
		Mode:          "email-verify",
		FormatVersion: 1,
		Ops:           []EmailVerifyOpResult{},
	}

	for _, op := range plan.Ops {
		res := verifyEmailOp(op, live, plan.DestinationUser)
		rep.Ops = append(rep.Ops, res)
		switch res.Status {
		case EmailVerifyApplied:
			rep.Summary.Applied++
		case EmailVerifyUnchanged:
			rep.Summary.Unchanged++
		case EmailVerifyPending:
			rep.Summary.Pending++
		case EmailVerifyDrift:
			rep.Summary.Drift++
		case EmailVerifyManualReview:
			rep.Summary.ManualReview++
		case EmailVerifyNotChecked:
			rep.Summary.NotChecked++
		case EmailVerifyUnavailable:
			rep.Summary.Unavailable++
		}
	}

	rep.Untracked = emailUntracked(plan, live)
	rep.Summary.Untracked = len(rep.Untracked)

	rep.ManualSections = append(rep.ManualSections, plan.ManualSections...)
	rep.Summary.ManualSections = len(rep.ManualSections)

	rep.Clean = rep.Summary.Pending == 0 &&
		rep.Summary.Drift == 0 &&
		rep.Summary.Unavailable == 0 &&
		rep.Summary.ManualSections == 0
	return rep
}

// verifyEmailOp applies the status table to one planned op. Every
// unexpected shape degrades to drift with a reason — fail-safe: a
// malformed or hand-edited plan can never verify clean.
func verifyEmailOp(op EmailPlanOp, live EmailLiveState, destUser string) EmailVerifyOpResult {
	res := EmailVerifyOpResult{Section: op.Section, Action: op.Action, Domain: op.Domain, Key: op.Key}

	if op.Action == EmailActionManual {
		res.Status, res.Reason = EmailVerifyManualReview, op.Reason
		return res
	}
	// 2B-2/2B-3 sections: verify re-lists only the 2B-1 write surface.
	if op.Section != EmailSectionForwarders && op.Section != EmailSectionDefaultAddress {
		res.Status = EmailVerifyNotChecked
		res.Reason = "section is not re-listed by email verify until its writer lands"
		return res
	}

	switch {
	case op.Section == EmailSectionForwarders:
		if msg, failed := live.ForwarderListErrors[op.Domain]; failed {
			res.Status, res.Reason = EmailVerifyUnavailable, "fresh forwarder re-list failed: "+msg
			return res
		}
		pair := op.Forward
		if op.Action == EmailActionSkip {
			// A skip means the pair existed on both sides at plan time;
			// the comparable target is the source value.
			pair = op.SourceValue
		}
		res.Expected = pair
		observed := live.destForwardTargets(op.Domain, op.Key)
		res.Observed = strings.Join(observed, ", ")
		present := live.forwardPairPresent(op.Domain, op.Key, pair)
		switch op.Action {
		case EmailActionSkip:
			if present {
				res.Status = EmailVerifyUnchanged
			} else {
				res.Status = EmailVerifyDrift
				res.Reason = "the plan-time destination pair is no longer live"
			}
		case EmailActionCreate:
			planTime := make([]string, 0, len(op.PlanTimeDestForwards))
			for _, t := range op.PlanTimeDestForwards {
				planTime = append(planTime, canonEmailAddr(t))
			}
			sort.Strings(planTime)
			switch {
			case present:
				res.Status = EmailVerifyApplied
			case stringSlicesEqual(planTime, observed):
				res.Status = EmailVerifyPending
				res.Reason = "pair still missing on the destination (plan-time state)"
			default:
				res.Status = EmailVerifyDrift
				res.Reason = "destination forwarders match neither the desired nor the plan-time state"
			}
		default:
			res.Status = EmailVerifyDrift
			res.Reason = fmt.Sprintf("unexpected forwarder action %q — malformed or hand-edited plan", op.Action)
		}
		return res

	default: // default_address
		if !live.DefaultsListed {
			res.Status, res.Reason = EmailVerifyUnavailable, "fresh default-address re-list failed: "+live.DefaultsError
			return res
		}
		cur, ok := live.defaultFor(op.Domain)
		res.Observed = cur
		if !ok {
			res.Status, res.Reason = EmailVerifyDrift, "domain no longer appears in the destination default-address list"
			return res
		}
		switch op.Action {
		case EmailActionSkip:
			res.Expected = op.DestinationValue
			if defaultsEquivalent(op.DestinationValue, cur, destUser, destUser) {
				res.Status = EmailVerifyUnchanged
			} else {
				res.Status = EmailVerifyDrift
				res.Reason = "live default no longer matches the plan-time destination state"
			}
		case EmailActionSet:
			res.Expected = op.Value
			switch {
			case defaultsEquivalent(op.Value, cur, destUser, destUser):
				res.Status = EmailVerifyApplied
			case defaultsEquivalent(op.DestinationValue, cur, destUser, destUser):
				res.Status = EmailVerifyPending
				res.Reason = "default still carries the plan-time destination value"
			default:
				res.Status = EmailVerifyDrift
				res.Reason = "live default matches neither the desired nor the plan-time state"
			}
		default:
			res.Status = EmailVerifyDrift
			res.Reason = fmt.Sprintf("unexpected default_address action %q — malformed or hand-edited plan", op.Action)
		}
		return res
	}
}

// emailUntracked lists live 2B-1-section items covered by neither the
// plan's ops nor its informational set.
func emailUntracked(plan EmailApplyPlan, live EmailLiveState) []EmailPlanInfo {
	knownPairs := map[forwarderPair]bool{}
	knownDefaults := map[string]bool{}
	for _, op := range plan.Ops {
		switch op.Section {
		case EmailSectionForwarders:
			target := op.Forward
			if target == "" {
				target = op.SourceValue
			}
			knownPairs[forwarderPair{Addr: canonEmailAddr(op.Key), Target: canonEmailAddr(target)}] = true
		case EmailSectionDefaultAddress:
			knownDefaults[op.Domain] = true
		}
	}
	for _, info := range plan.Informational {
		switch info.Section {
		case EmailSectionForwarders:
			knownPairs[forwarderPair{Addr: canonEmailAddr(info.Key), Target: canonEmailAddr(info.Value)}] = true
		case EmailSectionDefaultAddress:
			knownDefaults[info.Domain] = true
		}
	}

	var out []EmailPlanInfo
	domains := make([]string, 0, len(live.ForwardersByDomain))
	for d := range live.ForwardersByDomain {
		domains = append(domains, d)
	}
	sort.Strings(domains)
	for _, d := range domains {
		for _, f := range live.ForwardersByDomain[d] {
			pair := forwarderPair{Addr: canonEmailAddr(f.Source), Target: canonEmailAddr(f.Destination)}
			if knownPairs[pair] {
				continue
			}
			out = append(out, EmailPlanInfo{
				Section: EmailSectionForwarders, Domain: d,
				Key: canonEmailAddr(f.Source), Value: strings.TrimSpace(f.Destination),
			})
		}
	}
	if live.DefaultsListed {
		for _, e := range live.Defaults {
			domain := strings.ToLower(strings.TrimSpace(e.Domain))
			if knownDefaults[domain] {
				continue
			}
			out = append(out, EmailPlanInfo{
				Section: EmailSectionDefaultAddress, Domain: domain,
				Key: domain, Value: e.DefaultAddress,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Section != b.Section {
			return emailPlanSectionOrder[a.Section] < emailPlanSectionOrder[b.Section]
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.Value < b.Value
	})
	return out
}
