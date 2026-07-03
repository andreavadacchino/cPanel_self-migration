package accountinventory

import (
	"fmt"
	"sort"
	"strings"
)

// Email apply plan (PR 2B-1). BuildEmailPlan is fully offline: it consumes
// two normalized inventories and produces a reviewable plan of what
// `email apply` would write into the DESTINATION account's email
// configuration. It never connects anywhere and never generates delete
// ops for destination-only resources; the design and its safety rules
// live in docs/dev/PR2B_EMAIL_APPLY_DESIGN.md and PR2B_PRE_CAPTURES.md.
//
// 2B-1 computes actionable ops for forwarders and default (catch-all)
// addresses only — the two real Fase 0.2 blockers. Autoresponders, email
// filters and email routing are carried in the plan from day one so the
// checklist picture stays complete, but only as skip/manual: their
// writers land in 2B-2/2B-3.

// Plan op actions. EmailActionManual is terminal: "flagged but applied
// anyway" does not exist.
const (
	EmailActionCreate = "create" // forwarder pair missing on destination
	EmailActionSet    = "set"    // default address: dest still carries a fresh-account default
	EmailActionSkip   = "skip"   // already satisfied on the destination
	EmailActionManual = "manual" // the tool refuses to touch it (terminal)
)

// Plan sections, matching the inventory/diff/policy section names.
const (
	EmailSectionForwarders     = "forwarders"
	EmailSectionDefaultAddress = "default_address"
	EmailSectionAutoresponders = "autoresponders"
	EmailSectionFilters        = "email_filters"
	EmailSectionRouting        = "email_routing"
)

// emailPlanSectionOrder fixes the deterministic section order of the ops
// list (writable sections first).
var emailPlanSectionOrder = map[string]int{
	EmailSectionForwarders:     0,
	EmailSectionDefaultAddress: 1,
	EmailSectionAutoresponders: 2,
	EmailSectionFilters:        3,
	EmailSectionRouting:        4,
}

// freshDefaultAssumption is the documented safety assumption of the `set`
// classification; the plan Markdown carries it verbatim (design rule).
const freshDefaultAssumption = "DOCUMENTED ASSUMPTION: the `set` classification treats the literal " +
	"account username and the `:fail:`/`:blackhole:` system forms (prefix-matched — the " +
	"human-readable tail is locale-dependent) as fresh-account defaults. This is safe because " +
	"the campaign's destination accounts are created FRESH by us; `:fail:`/username are also " +
	"legitimate deliberate choices, so pointing `email apply` at a pre-existing, " +
	"human-configured destination account makes the `set` classification unsafe."

// EmailPlanOp is the decision for one email-config item.
type EmailPlanOp struct {
	Section string `json:"section"`
	Action  string `json:"action"`
	Domain  string `json:"domain"`
	// Key identifies the item inside its section: forwarders → the
	// canonical source address; default_address / email_routing → the
	// domain; autoresponders → the autoresponder address; email_filters →
	// "account/filter name".
	Key    string `json:"key"`
	Reason string `json:"reason,omitempty"`
	// create (forwarders): Email::add_forwarder parameters — email is the
	// LOCAL part, Forward the single target address (2B-pre contract).
	Email   string `json:"email,omitempty"`
	Forward string `json:"forward,omitempty"`
	// set (default_address): the desired value, verbatim from the source.
	Value string `json:"value,omitempty"`
	// Display context + apply-time preconditions.
	SourceValue      string `json:"source_value,omitempty"`
	DestinationValue string `json:"destination_value,omitempty"`
	// PlanTimeDestForwards records, for a forwarder create op, the
	// destination's forwarder targets for this source address at plan
	// time — the per-op precondition `email apply` re-checks against a
	// fresh re-list before writing (the email analogue of the DNS serial).
	PlanTimeDestForwards []string `json:"plan_time_dest_forwards,omitempty"`
	// Autoresponder carries the full content payload of an autoresponder
	// create op (PR 2B-2). Its plan-time precondition is implicit in the
	// action: the destination address had NO autoresponder (a differing
	// one is terminal manual — the writer never overwrites).
	Autoresponder *EmailAutoresponderContent `json:"autoresponder,omitempty"`
}

// EmailAutoresponderContent is the round-trippable autoresponder content
// (get_auto_responder → add_auto_responder, 2B-2-pre facts 3/5): exactly
// the fields the writer sends and the verify paths compare.
type EmailAutoresponderContent struct {
	From     string `json:"from"`
	Subject  string `json:"subject"`
	Body     string `json:"body"`
	IsHTML   int    `json:"is_html"`
	Interval int    `json:"interval"`
	Start    int64  `json:"start,omitempty"`
	Stop     int64  `json:"stop,omitempty"`
	Charset  string `json:"charset,omitempty"`
}

// EmailPlanInfo describes a destination-only item: listed so a human sees
// it, never deleted (additive posture).
type EmailPlanInfo struct {
	Section string `json:"section"`
	Domain  string `json:"domain"`
	Key     string `json:"key"`
	Value   string `json:"value"`
}

// EmailManualSection is a section the plan refuses to compute ops for
// (unavailable on either side — fail-safe, mirrors ManualZone).
type EmailManualSection struct {
	Section string `json:"section"`
	Reason  string `json:"reason"`
}

type EmailPlanSummary struct {
	Create        int `json:"create"`
	Set           int `json:"set"`
	Skip          int `json:"skip"`
	Manual        int `json:"manual"`
	Informational int `json:"informational"`
}

type EmailApplyPlan struct {
	Mode              string               `json:"mode"`
	FormatVersion     int                  `json:"format_version"`
	GeneratedAt       string               `json:"generated_at"`
	SourceFile        string               `json:"source_file,omitempty"`
	SourceSHA256      string               `json:"source_sha256,omitempty"`
	DestinationFile   string               `json:"destination_file,omitempty"`
	DestinationSHA256 string               `json:"destination_sha256,omitempty"`
	PolicyFile        string               `json:"policy_file,omitempty"`
	SourceUser        string               `json:"source_user"`
	DestinationUser   string               `json:"destination_user"`
	Ops               []EmailPlanOp        `json:"ops"`
	Informational     []EmailPlanInfo      `json:"informational,omitempty"`
	ManualSections    []EmailManualSection `json:"manual_sections,omitempty"`
	PolicyFindings    []string             `json:"policy_findings,omitempty"`
	NonEmailBlockers  []string             `json:"non_email_blockers,omitempty"`
	Summary           EmailPlanSummary     `json:"summary"`
}

// canonEmailAddr canonicalizes an email address (or address-like value)
// for comparison: trimmed and lowercased. cPanel stores mailbox/forwarder
// addresses lowercase; the inventory keeps them verbatim.
func canonEmailAddr(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// splitEmailAddr splits local@domain, requiring exactly one "@" with
// non-empty halves. Anything else cannot be expressed as add_forwarder
// parameters and fails safe into manual.
func splitEmailAddr(s string) (local, domain string, ok bool) {
	s = strings.TrimSpace(s)
	if strings.Count(s, "@") != 1 {
		return "", "", false
	}
	local, domain, _ = strings.Cut(s, "@")
	if local == "" || domain == "" {
		return "", "", false
	}
	return local, domain, true
}

// isSimpleForwardTarget reports whether a cPanel `forward` value is a
// single plain email address — the ONLY form add_forwarder
// fwdopt=fwd/fwdemail= can round-trip. Multi-target comma-joined values,
// pipes, deliver-to-file paths, :fail:/:blackhole: system forms and
// deliver-to-account bare usernames all fail this check (terminal manual,
// per the 2B design).
func isSimpleForwardTarget(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" || strings.ContainsAny(t, ", \t|") {
		return false
	}
	if strings.HasPrefix(t, ":") || strings.HasPrefix(t, "/") {
		return false
	}
	local, domain, ok := splitEmailAddr(t)
	if !ok || local == "" {
		return false
	}
	// A target domain without a dot (user@localhost) is suspicious enough
	// to review by hand — fail-safe.
	return strings.Contains(domain, ".")
}

// Default-address value classes. list_default_address values are kept
// verbatim since 7E; the :fail:/:blackhole: system forms are matched by
// PREFIX because the human-readable tail is locale-dependent.
const (
	defaultClassFail           = "fail"
	defaultClassBlackhole      = "blackhole"
	defaultClassAccountDefault = "account_default"
	defaultClassAddress        = "address"
	defaultClassOther          = "other"
)

func classifyDefaultValue(v, accountUser string) string {
	t := strings.TrimSpace(v)
	switch {
	case strings.HasPrefix(t, ":fail:"):
		return defaultClassFail
	case strings.HasPrefix(t, ":blackhole:"):
		return defaultClassBlackhole
	case t == accountUser:
		return defaultClassAccountDefault
	case isSimpleForwardTarget(t):
		return defaultClassAddress
	default:
		return defaultClassOther
	}
}

// defaultsEquivalent reports whether the source and destination default
// addresses are behaviorally the same: exact match, same system-form
// class (locale-independent), or both carrying their own account's
// fresh default (deliver to the account owner on either side).
func defaultsEquivalent(srcVal, destVal, srcUser, destUser string) bool {
	if strings.TrimSpace(srcVal) == strings.TrimSpace(destVal) {
		return true
	}
	sc := classifyDefaultValue(srcVal, srcUser)
	dc := classifyDefaultValue(destVal, destUser)
	if sc == dc && (sc == defaultClassFail || sc == defaultClassBlackhole || sc == defaultClassAccountDefault) {
		return true
	}
	return false
}

// isFreshDefault reports whether a DESTINATION default address still
// carries a fresh-account form (never customized by a human): the literal
// account username, or a :fail:/:blackhole: system form.
func isFreshDefault(v, destUser string) bool {
	switch classifyDefaultValue(v, destUser) {
	case defaultClassFail, defaultClassBlackhole, defaultClassAccountDefault:
		return true
	}
	return false
}

// BuildEmailPlan computes the email apply plan. policy is optional
// context (findings are cross-referenced, never gating — 6A precedent).
// It cannot fail: every unprovable state degrades to manual ops or
// manual_sections instead of erroring (fail-safe by construction).
func BuildEmailPlan(src, dest NormalizedInventory, policy *PolicyReport) EmailApplyPlan {
	plan := EmailApplyPlan{
		Mode:            "email-apply-plan",
		FormatVersion:   1,
		SourceUser:      src.Account.User,
		DestinationUser: dest.Account.User,
		// Ops is seeded non-nil so the JSON stays array-typed (diff.go
		// convention).
		Ops: []EmailPlanOp{},
	}

	planForwarders(&plan, src, dest)
	planDefaultAddresses(&plan, src, dest)
	planAutoresponders(&plan, src, dest)
	planFilters(&plan, src)
	planRouting(&plan, src, dest)

	plan.PolicyFindings = emailPolicyFindings(policy)
	plan.NonEmailBlockers = nonEmailBlockers(policy)

	sort.Slice(plan.Ops, func(i, j int) bool { return emailOpLess(plan.Ops[i], plan.Ops[j]) })
	sort.Slice(plan.Informational, func(i, j int) bool {
		a, b := plan.Informational[i], plan.Informational[j]
		if a.Section != b.Section {
			return emailPlanSectionOrder[a.Section] < emailPlanSectionOrder[b.Section]
		}
		if a.Domain != b.Domain {
			return a.Domain < b.Domain
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.Value < b.Value
	})
	sort.Slice(plan.ManualSections, func(i, j int) bool {
		return emailPlanSectionOrder[plan.ManualSections[i].Section] < emailPlanSectionOrder[plan.ManualSections[j].Section]
	})

	for _, op := range plan.Ops {
		switch op.Action {
		case EmailActionCreate:
			plan.Summary.Create++
		case EmailActionSet:
			plan.Summary.Set++
		case EmailActionSkip:
			plan.Summary.Skip++
		case EmailActionManual:
			plan.Summary.Manual++
		}
	}
	plan.Summary.Informational = len(plan.Informational)
	return plan
}

func emailOpLess(a, b EmailPlanOp) bool {
	if a.Section != b.Section {
		return emailPlanSectionOrder[a.Section] < emailPlanSectionOrder[b.Section]
	}
	if a.Domain != b.Domain {
		return a.Domain < b.Domain
	}
	if a.Key != b.Key {
		return a.Key < b.Key
	}
	return a.Forward < b.Forward
}

// forwarderPair is the canonical identity of one forwarder.
type forwarderPair struct {
	Addr   string
	Target string
}

// planForwarders applies the 2B rule table to the forwarder sections.
// NOTE: the forwarders inventory section has no availability flag (a
// per-domain collection failure surfaces only as an inventory warning),
// so the plan cannot gate on it; the diff/policy layers share the same
// blindness. Dest-only pairs are informational, never deleted.
func planForwarders(plan *EmailApplyPlan, src, dest NormalizedInventory) {
	destDomains := map[string]bool{}
	for _, d := range dest.Domains {
		destDomains[strings.ToLower(d.Name)] = true
	}

	destPairs := map[forwarderPair]bool{}
	destTargets := map[string][]string{}
	for _, f := range dest.Forwarders {
		addr := canonEmailAddr(f.Source)
		pair := forwarderPair{Addr: addr, Target: canonEmailAddr(f.Destination)}
		if !destPairs[pair] {
			destPairs[pair] = true
			destTargets[addr] = append(destTargets[addr], strings.TrimSpace(f.Destination))
		}
	}
	for _, ts := range destTargets {
		sort.Strings(ts)
	}

	srcPairs := map[forwarderPair]bool{}
	seen := map[forwarderPair]bool{}
	for _, f := range src.Forwarders {
		addr := canonEmailAddr(f.Source)
		pair := forwarderPair{Addr: addr, Target: canonEmailAddr(f.Destination)}
		if seen[pair] {
			continue // duplicate source rows collapse into one op
		}
		seen[pair] = true
		srcPairs[pair] = true

		op := EmailPlanOp{
			Section:          EmailSectionForwarders,
			Key:              addr,
			SourceValue:      strings.TrimSpace(f.Destination),
			DestinationValue: strings.Join(destTargets[addr], ", "),
		}
		local, domain, ok := splitEmailAddr(f.Source)
		if ok {
			op.Domain = strings.ToLower(domain)
		} else {
			op.Domain = strings.ToLower(strings.TrimSpace(f.Domain))
		}

		target := strings.TrimSpace(f.Destination)
		switch {
		case !ok:
			op.Action = EmailActionManual
			op.Reason = fmt.Sprintf("source address %q does not split into local@domain — cannot be expressed as forwarder-write parameters", strings.TrimSpace(f.Source))
		case destPairs[pair]:
			op.Action = EmailActionSkip
		case !isSimpleForwardTarget(target):
			op.Action = EmailActionManual
			op.Reason = fmt.Sprintf("non-single-address forward (raw: %q) — the forwarder writer only round-trips a single plain address; recreate by hand", target)
		case !destDomains[op.Domain]:
			op.Action = EmailActionManual
			op.Reason = fmt.Sprintf("domain %s is missing on destination — create it first, then re-plan", op.Domain)
		default:
			op.Action = EmailActionCreate
			op.Email = strings.ToLower(local)
			op.Forward = target
			op.PlanTimeDestForwards = destTargets[addr]
		}
		plan.Ops = append(plan.Ops, op)
	}

	for _, f := range dest.Forwarders {
		pair := forwarderPair{Addr: canonEmailAddr(f.Source), Target: canonEmailAddr(f.Destination)}
		if srcPairs[pair] {
			continue
		}
		plan.Informational = append(plan.Informational, EmailPlanInfo{
			Section: EmailSectionForwarders,
			Domain:  strings.ToLower(strings.TrimSpace(f.Domain)),
			Key:     canonEmailAddr(f.Source),
			Value:   strings.TrimSpace(f.Destination),
		})
	}
}

// planDefaultAddresses applies the 2B rule table to the default (catch-all)
// address section: set only onto a fresh-account destination default,
// terminal manual otherwise (a customized dest default is somebody's
// decision).
func planDefaultAddresses(plan *EmailApplyPlan, src, dest NormalizedInventory) {
	if !src.DefaultAddresses.Available || !dest.DefaultAddresses.Available {
		side := "source"
		if src.DefaultAddresses.Available {
			side = "destination"
		}
		plan.ManualSections = append(plan.ManualSections, EmailManualSection{
			Section: EmailSectionDefaultAddress,
			Reason:  fmt.Sprintf("default_address section unavailable on %s — re-run that inventory", side),
		})
		return
	}

	destByDomain := map[string]string{}
	srcDomains := map[string]bool{}
	for _, d := range dest.DefaultAddresses.Items {
		destByDomain[strings.ToLower(d.Domain)] = d.DefaultAddress
	}

	for _, s := range src.DefaultAddresses.Items {
		domain := strings.ToLower(s.Domain)
		srcDomains[domain] = true
		op := EmailPlanOp{
			Section:     EmailSectionDefaultAddress,
			Domain:      domain,
			Key:         domain,
			SourceValue: s.DefaultAddress,
		}
		destVal, ok := destByDomain[domain]
		op.DestinationValue = destVal
		switch {
		case !ok:
			op.Action = EmailActionManual
			op.Reason = fmt.Sprintf("domain %s is missing on the destination default-address list — create it first, then re-plan", domain)
		case defaultsEquivalent(s.DefaultAddress, destVal, plan.SourceUser, plan.DestinationUser):
			op.Action = EmailActionSkip
		case !isFreshDefault(destVal, plan.DestinationUser):
			op.Action = EmailActionManual
			op.Reason = fmt.Sprintf("destination default address %q was customized on purpose — resolve by hand (terminal)", destVal)
		default:
			switch classifyDefaultValue(s.DefaultAddress, plan.SourceUser) {
			case defaultClassAddress, defaultClassFail, defaultClassBlackhole:
				op.Action = EmailActionSet
				op.Value = strings.TrimSpace(s.DefaultAddress)
			default:
				op.Action = EmailActionManual
				op.Reason = fmt.Sprintf("source default address %q cannot be round-tripped by the default-address writer — recreate by hand", s.DefaultAddress)
			}
		}
		plan.Ops = append(plan.Ops, op)
	}

	for _, d := range dest.DefaultAddresses.Items {
		domain := strings.ToLower(d.Domain)
		if srcDomains[domain] {
			continue
		}
		plan.Informational = append(plan.Informational, EmailPlanInfo{
			Section: EmailSectionDefaultAddress,
			Domain:  domain,
			Key:     domain,
			Value:   d.DefaultAddress,
		})
	}
}

// normalizeAutoresponderBody applies the byte-verified cPanel storage
// normalization (2B-2-pre fact 5): trailing "\n" runs collapse to exactly
// one. A get_auto_responder output is already in this form; normalizing
// both sides makes the equality immune to hand-edited artifacts.
func normalizeAutoresponderBody(b string) string {
	return strings.TrimRight(b, "\n") + "\n"
}

// autoresponderCharset defaults an empty charset to the cPanel default.
func autoresponderCharset(c string) string {
	if strings.TrimSpace(c) == "" {
		return "utf-8"
	}
	return strings.TrimSpace(c)
}

// autorespondersEquivalent reports whether two COLLECTED autoresponders
// are behaviorally identical: every round-trippable content field, with
// the body compared under the storage normalization.
func autorespondersEquivalent(a, b AutoresponderEntry) bool {
	return a.Subject == b.Subject &&
		a.From == b.From &&
		normalizeAutoresponderBody(a.Body) == normalizeAutoresponderBody(b.Body) &&
		a.IsHTML == b.IsHTML &&
		a.Interval == b.Interval &&
		a.Start == b.Start &&
		a.Stop == b.Stop &&
		autoresponderCharset(a.Charset) == autoresponderCharset(b.Charset)
}

// planAutoresponders applies the 2B rule table to the autoresponder
// section (PR 2B-2). Bodies are collected since 2B-2, so equality is
// provable when BOTH sides carry BodyCollected; anything unprovable
// degrades to terminal manual (fail-safe). ⚠️ add_auto_responder UPSERTS
// (2B-2-pre fact 7): a differing destination autoresponder is terminal
// manual — the writer never overwrites somebody's content. Dest-only
// autoresponders are informational, never deleted.
func planAutoresponders(plan *EmailApplyPlan, src, dest NormalizedInventory) {
	destDomains := map[string]bool{}
	for _, d := range dest.Domains {
		destDomains[strings.ToLower(d.Name)] = true
	}
	destByEmail := map[string]AutoresponderEntry{}
	destPresent := map[string]bool{}
	for _, a := range dest.Autoresponders {
		addr := canonEmailAddr(a.Email)
		if !destPresent[addr] {
			destPresent[addr] = true
			destByEmail[addr] = a
		}
	}

	srcSeen := map[string]bool{}
	for _, a := range src.Autoresponders {
		addr := canonEmailAddr(a.Email)
		if srcSeen[addr] {
			continue // duplicate source rows collapse into one op
		}
		srcSeen[addr] = true

		op := EmailPlanOp{
			Section:     EmailSectionAutoresponders,
			Domain:      strings.ToLower(strings.TrimSpace(a.Domain)),
			Key:         addr,
			SourceValue: a.Subject,
		}
		local, domain, ok := splitEmailAddr(a.Email)
		if ok {
			op.Domain = strings.ToLower(domain)
		}
		d, onDest := destByEmail[addr]
		if onDest {
			op.DestinationValue = d.Subject
		}

		switch {
		case !ok:
			op.Action = EmailActionManual
			op.Reason = fmt.Sprintf("autoresponder address %q does not split into local@domain — cannot be expressed as write parameters", strings.TrimSpace(a.Email))
		case !a.BodyCollected:
			op.Action = EmailActionManual
			op.Reason = "the source inventory carries no autoresponder body (pre-2B-2 artifact or a failed per-address read) — re-run --account-inventory on the source, then re-plan"
		case onDest && !d.BodyCollected:
			op.Action = EmailActionManual
			op.Reason = "an autoresponder exists on the destination but its inventory carries no body (pre-2B-2 artifact or a failed per-address read) — re-run the destination inventory, then re-plan"
		case onDest && autorespondersEquivalent(a, d):
			op.Action = EmailActionSkip
			// The agreed content travels with the skip op too: email verify
			// re-checks the destination still carries it (dnsverify
			// precedent: a skip proves plan-time equality, verify proves it
			// is still live).
			op.Autoresponder = &EmailAutoresponderContent{
				From:     a.From,
				Subject:  a.Subject,
				Body:     normalizeAutoresponderBody(a.Body),
				IsHTML:   a.IsHTML,
				Interval: a.Interval,
				Start:    a.Start,
				Stop:     a.Stop,
				Charset:  autoresponderCharset(a.Charset),
			}
		case onDest:
			op.Action = EmailActionManual
			op.Reason = "an autoresponder with DIFFERENT content already exists on the destination — the writer never overwrites (the add call would destroy it); resolve by hand (terminal)"
		case !destDomains[op.Domain]:
			op.Action = EmailActionManual
			op.Reason = fmt.Sprintf("domain %s is missing on destination — create it first, then re-plan", op.Domain)
		default:
			op.Action = EmailActionCreate
			op.Email = strings.ToLower(local)
			op.Autoresponder = &EmailAutoresponderContent{
				From:     a.From,
				Subject:  a.Subject,
				Body:     normalizeAutoresponderBody(a.Body),
				IsHTML:   a.IsHTML,
				Interval: a.Interval,
				Start:    a.Start,
				Stop:     a.Stop,
				Charset:  autoresponderCharset(a.Charset),
			}
		}
		plan.Ops = append(plan.Ops, op)
	}

	for _, a := range dest.Autoresponders {
		addr := canonEmailAddr(a.Email)
		if srcSeen[addr] {
			continue
		}
		plan.Informational = append(plan.Informational, EmailPlanInfo{
			Section: EmailSectionAutoresponders,
			Domain:  strings.ToLower(strings.TrimSpace(a.Domain)),
			Key:     addr,
			Value:   a.Subject,
		})
	}
}

// planFilters carries every source email filter as a terminal manual op:
// the collector stores counts only (redaction posture), so the rules
// cannot be round-tripped until 2B-3.
func planFilters(plan *EmailApplyPlan, src NormalizedInventory) {
	if !src.EmailFilters.Available {
		plan.ManualSections = append(plan.ManualSections, EmailManualSection{
			Section: EmailSectionFilters,
			Reason:  "email_filters section unavailable on source — re-run that inventory",
		})
		return
	}
	for _, f := range src.EmailFilters.Items {
		account := f.Account
		if account == "" {
			account = "(account-level)"
		}
		plan.Ops = append(plan.Ops, EmailPlanOp{
			Section:     EmailSectionFilters,
			Key:         account + "/" + f.FilterName,
			SourceValue: fmt.Sprintf("%d rule(s), %d action(s), enabled=%v", f.RuleCount, f.ActionCount, f.Enabled),
			Action:      EmailActionManual,
			Reason:      "email filter apply lands in 2B-3 (filter rule round-trip, pending the redaction decision) — recreate by hand or wait",
		})
	}
}

// planRouting compares the configured mail-routing mode per domain: the
// enum comparison is complete, so identical values honestly skip; any
// difference is manual until the 2B-3 setmxcheck writer lands.
func planRouting(plan *EmailApplyPlan, src, dest NormalizedInventory) {
	if !src.EmailRouting.Available || !dest.EmailRouting.Available {
		side := "source"
		if src.EmailRouting.Available {
			side = "destination"
		}
		plan.ManualSections = append(plan.ManualSections, EmailManualSection{
			Section: EmailSectionRouting,
			Reason:  fmt.Sprintf("email_routing section unavailable on %s — re-run that inventory", side),
		})
		return
	}
	destByDomain := map[string]string{}
	for _, r := range dest.EmailRouting.Items {
		destByDomain[strings.ToLower(r.Domain)] = r.Routing
	}
	for _, r := range src.EmailRouting.Items {
		domain := strings.ToLower(r.Domain)
		op := EmailPlanOp{
			Section:     EmailSectionRouting,
			Domain:      domain,
			Key:         domain,
			SourceValue: r.Routing,
		}
		destVal, ok := destByDomain[domain]
		op.DestinationValue = destVal
		if ok && destVal == r.Routing {
			op.Action = EmailActionSkip
		} else {
			op.Action = EmailActionManual
			op.Reason = "email routing apply (the API2 routing write) lands in 2B-3 — set by hand or wait"
		}
		plan.Ops = append(plan.Ops, op)
	}
}

// emailPlanSections is the section set whose policy findings are
// cross-referenced into the plan (context for the reviewer, never a gate).
var emailPlanSections = map[string]bool{
	EmailSectionForwarders:     true,
	EmailSectionDefaultAddress: true,
	EmailSectionAutoresponders: true,
	EmailSectionFilters:        true,
	EmailSectionRouting:        true,
}

func emailPolicyFindings(policy *PolicyReport) []string {
	if policy == nil {
		return nil
	}
	var out []string
	for _, f := range policy.Findings {
		if !emailPlanSections[f.Section] {
			continue
		}
		ref := f.SourceRef
		if ref == "" {
			ref = f.DestinationRef
		}
		if ref == "" {
			ref = f.Detail
		}
		out = append(out, fmt.Sprintf("%s [%s] %s", f.ID, f.Severity, ref))
	}
	sort.Strings(out)
	return out
}

// nonEmailBlockers lists blocker findings outside the email sections:
// they concern other migration steps and are surfaced as context only.
func nonEmailBlockers(policy *PolicyReport) []string {
	if policy == nil {
		return nil
	}
	var out []string
	for _, f := range policy.Findings {
		if f.Severity == SeverityBlocker && !emailPlanSections[f.Section] {
			out = append(out, fmt.Sprintf("%s (%s)", f.ID, f.Section))
		}
	}
	sort.Strings(out)
	return out
}
