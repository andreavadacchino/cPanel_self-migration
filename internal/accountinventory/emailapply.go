package accountinventory

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Email apply engine (PR 2B-1) — the OFFLINE half of `email apply`: op
// evaluation against a fresh destination re-list (the per-op freshness
// guard, the email analogue of the DNS serial), the backup/report
// artifact types with their bidirectional pairing, and the report-driven
// rollback computation. Everything here is pure data logic: the SSH
// orchestration lives in the `email apply` command, the write primitives
// in internal/cpanel/email_apply.go (the only allowlisted verb files).

// Apply op statuses.
const (
	EmailOpApplied = "applied"         // written and verified present after the write
	EmailOpAlready = "already_present" // the op's outcome was already on the destination
	EmailOpRefused = "refused_precondition"
	EmailOpFailed  = "failed"
	EmailOpSkipped = "skipped" // plan action skip: nothing to do
	EmailOpManual  = "manual"  // plan action manual: terminal, never applied
	EmailOpPlanned = "planned" // dry-run only: would write
)

// Apply-time decisions for an actionable (create/set) op.
const (
	EmailDecisionWrite   = "write"
	EmailDecisionAlready = "already_present"
	EmailDecisionRefused = "refused_precondition"
)

// EmailLiveState is a fresh re-list of the sections the plan touches on
// the DESTINATION, in normalized inventory shape. A section (or a
// domain's forwarder list) that failed to list carries its error so every
// op depending on it refuses fail-closed instead of guessing.
type EmailLiveState struct {
	// ForwardersByDomain holds the fresh forwarder list per touched
	// domain (normalized entries; Source is local@domain).
	ForwardersByDomain map[string][]ForwarderEntry
	// ForwarderListErrors records domains whose re-list failed.
	ForwarderListErrors map[string]string
	// Defaults is the fresh default-address list (all domains, one call).
	Defaults []DefaultAddressEntry
	// DefaultsListed is true when the default-address re-list succeeded;
	// DefaultsError carries the failure otherwise.
	DefaultsListed bool
	DefaultsError  string
	// AutorespondersByDomain holds the fresh autoresponder list per touched
	// domain (PR 2B-2), with bodies fetched per address; an entry whose
	// per-address body read failed carries BodyCollected=false and every op
	// depending on it refuses fail-closed.
	AutorespondersByDomain map[string][]AutoresponderEntry
	// AutoresponderListErrors records domains whose re-list failed.
	AutoresponderListErrors map[string]string
	// FiltersByAccount holds the fresh filter list per account scope
	// ("" = account-level). Each entry's RulesCollected is true when
	// get_filter succeeded for it (PR 2B-3).
	FiltersByAccount map[string][]EmailFilterEntry
	// FilterListErrors records account scopes whose re-list failed.
	FilterListErrors map[string]string
	// RoutingEntries holds the fresh routing list (all domains).
	RoutingEntries []EmailRoutingEntry
	// RoutingListed is true when the routing re-list succeeded.
	RoutingListed bool
	RoutingError  string
}

// destForwardTargets returns the canonical target set for one source
// address in the live state.
func (l EmailLiveState) destForwardTargets(domain, address string) []string {
	var out []string
	for _, f := range l.ForwardersByDomain[domain] {
		if canonEmailAddr(f.Source) == canonEmailAddr(address) {
			out = append(out, canonEmailAddr(f.Destination))
		}
	}
	sort.Strings(out)
	return out
}

// forwardPairPresent reports whether the exact pair is live.
func (l EmailLiveState) forwardPairPresent(domain, address, target string) bool {
	for _, f := range l.ForwardersByDomain[domain] {
		if canonEmailAddr(f.Source) == canonEmailAddr(address) &&
			canonEmailAddr(f.Destination) == canonEmailAddr(target) {
			return true
		}
	}
	return false
}

// autoresponderFor returns the live autoresponder for one address.
func (l EmailLiveState) autoresponderFor(domain, address string) (AutoresponderEntry, bool) {
	for _, a := range l.AutorespondersByDomain[domain] {
		if canonEmailAddr(a.Email) == canonEmailAddr(address) {
			return a, true
		}
	}
	return AutoresponderEntry{}, false
}

// autoresponderContentEquivalent compares a plan payload against a live
// entry using the same field set and body normalization as the plan's
// autorespondersEquivalent (verify can never disagree with the plan).
func autoresponderContentEquivalent(c *EmailAutoresponderContent, e AutoresponderEntry) bool {
	if c == nil || !e.BodyCollected {
		return false
	}
	return autorespondersEquivalent(AutoresponderEntry{
		Subject: c.Subject, From: c.From, Body: c.Body,
		IsHTML: c.IsHTML, Interval: c.Interval, Start: c.Start, Stop: c.Stop,
		Charset: c.Charset, BodyCollected: true,
	}, e)
}

// splitFilterKey inverts filterKey: "account/filtername" → account, filtername.
// "(account-level)/name" → "", "name".
func splitFilterKey(key string) (account, filtername string) {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) != 2 {
		return "", key
	}
	account = parts[0]
	if account == "(account-level)" {
		account = ""
	}
	return account, parts[1]
}

// filterFor returns the live filter entry for an account+name key.
func (l EmailLiveState) filterFor(account, filtername string) (EmailFilterEntry, bool) {
	for _, f := range l.FiltersByAccount[account] {
		if f.FilterName == filtername {
			return f, true
		}
	}
	return EmailFilterEntry{}, false
}

// filterContentEquivalent compares a plan payload against a live entry
// using the same field comparisons as filtersEquivalent.
func filterContentEquivalent(c *EmailFilterContent, e EmailFilterEntry) bool {
	if c == nil || !e.RulesCollected {
		return false
	}
	return filtersEquivalent(EmailFilterEntry{
		Rules:          c.Rules,
		Actions:        c.Actions,
		RulesCollected: true,
	}, e)
}

// routingFor returns the live routing value for a domain.
func (l EmailLiveState) routingFor(domain string) (string, bool) {
	for _, r := range l.RoutingEntries {
		if strings.ToLower(strings.TrimSpace(r.Domain)) == strings.ToLower(strings.TrimSpace(domain)) {
			return r.Routing, true
		}
	}
	return "", false
}

// defaultFor returns the live default address for a domain.
func (l EmailLiveState) defaultFor(domain string) (string, bool) {
	for _, d := range l.Defaults {
		if strings.ToLower(strings.TrimSpace(d.Domain)) == strings.ToLower(strings.TrimSpace(domain)) {
			return d.DefaultAddress, true
		}
	}
	return "", false
}

// EmailOutcomePresent reports whether an actionable op's OUTCOME is
// observably present in the live state — the check that makes re-running
// a partially applied plan converge without duplicates, and the
// unconditional per-op verify-after predicate.
func EmailOutcomePresent(op EmailPlanOp, live EmailLiveState, destUser string) bool {
	switch {
	case op.Section == EmailSectionForwarders && op.Action == EmailActionCreate:
		return live.forwardPairPresent(op.Domain, op.Key, op.Forward)
	case op.Section == EmailSectionDefaultAddress && op.Action == EmailActionSet:
		cur, ok := live.defaultFor(op.Domain)
		if !ok {
			return false
		}
		// Class-aware equality: a :fail: value round-trips with a
		// locale-dependent tail, so exact comparison would false-negative.
		return defaultsEquivalent(op.Value, cur, destUser, destUser)
	case op.Section == EmailSectionAutoresponders && op.Action == EmailActionCreate:
		e, ok := live.autoresponderFor(op.Domain, op.Key)
		return ok && autoresponderContentEquivalent(op.Autoresponder, e)
	case op.Section == EmailSectionFilters && op.Action == EmailActionCreate:
		account, filtername := splitFilterKey(op.Key)
		e, ok := live.filterFor(account, filtername)
		return ok && filterContentEquivalent(op.Filter, e)
	case op.Section == EmailSectionRouting && op.Action == EmailActionSet:
		cur, ok := live.routingFor(op.Domain)
		return ok && cur == op.Value
	}
	return false
}

// EvaluateEmailOp applies the per-op freshness guard to one actionable
// op against the fresh live state, per the 2B design order:
//  1. outcome already present → already_present (convergence);
//  2. the plan-time precondition still holds → write;
//  3. anything else → refused_precondition, fail-closed, continue.
func EvaluateEmailOp(op EmailPlanOp, live EmailLiveState, destUser string) (decision, reason string) {
	switch {
	case op.Section == EmailSectionForwarders && op.Action == EmailActionCreate:
		if msg, failed := live.ForwarderListErrors[op.Domain]; failed {
			return EmailDecisionRefused, fmt.Sprintf("fresh forwarder re-list failed for %s: %s", op.Domain, msg)
		}
		if live.forwardPairPresent(op.Domain, op.Key, op.Forward) {
			return EmailDecisionAlready, ""
		}
		want := make([]string, 0, len(op.PlanTimeDestForwards))
		for _, t := range op.PlanTimeDestForwards {
			want = append(want, canonEmailAddr(t))
		}
		sort.Strings(want)
		got := live.destForwardTargets(op.Domain, op.Key)
		if stringSlicesEqual(want, got) {
			return EmailDecisionWrite, ""
		}
		return EmailDecisionRefused, fmt.Sprintf(
			"destination forwarders for %s changed since the plan (plan-time %v, now %v) — re-plan and review",
			op.Key, want, got)

	case op.Section == EmailSectionDefaultAddress && op.Action == EmailActionSet:
		if !live.DefaultsListed {
			return EmailDecisionRefused, "fresh default-address re-list failed: " + live.DefaultsError
		}
		cur, ok := live.defaultFor(op.Domain)
		if !ok {
			return EmailDecisionRefused, fmt.Sprintf("domain %s no longer appears in the destination default-address list", op.Domain)
		}
		if defaultsEquivalent(op.Value, cur, destUser, destUser) {
			return EmailDecisionAlready, ""
		}
		if defaultsEquivalent(op.DestinationValue, cur, destUser, destUser) {
			return EmailDecisionWrite, ""
		}
		return EmailDecisionRefused, fmt.Sprintf(
			"destination default address changed since the plan (plan-time %q, now %q) — re-plan and review",
			op.DestinationValue, cur)

	case op.Section == EmailSectionAutoresponders && op.Action == EmailActionCreate:
		if op.Autoresponder == nil {
			return EmailDecisionRefused, fmt.Sprintf("autoresponder create op for %s carries no content payload — malformed or hand-edited plan", op.Key)
		}
		if msg, failed := live.AutoresponderListErrors[op.Domain]; failed {
			return EmailDecisionRefused, fmt.Sprintf("fresh autoresponder re-list failed for %s: %s", op.Domain, msg)
		}
		e, present := live.autoresponderFor(op.Domain, op.Key)
		if !present {
			// The plan-time precondition of an autoresponder create is
			// "address empty" (a differing dest is terminal manual at plan
			// time) — still empty means it still holds.
			return EmailDecisionWrite, ""
		}
		if !e.BodyCollected {
			return EmailDecisionRefused, fmt.Sprintf(
				"an autoresponder exists on %s but its body could not be read — cannot prove equality, refusing fail-closed", op.Key)
		}
		if autoresponderContentEquivalent(op.Autoresponder, e) {
			return EmailDecisionAlready, ""
		}
		return EmailDecisionRefused, fmt.Sprintf(
			"an autoresponder with different content appeared on %s since the plan — the writer never overwrites (the add call would destroy it); re-plan and review", op.Key)

	case op.Section == EmailSectionFilters && op.Action == EmailActionCreate:
		if op.Filter == nil {
			return EmailDecisionRefused, fmt.Sprintf("filter create op for %s carries no content payload — malformed or hand-edited plan", op.Key)
		}
		account, filtername := splitFilterKey(op.Key)
		if msg, failed := live.FilterListErrors[account]; failed {
			return EmailDecisionRefused, fmt.Sprintf("fresh filter re-list failed for account %q: %s", account, msg)
		}
		e, present := live.filterFor(account, filtername)
		if !present {
			return EmailDecisionWrite, ""
		}
		if !e.RulesCollected {
			return EmailDecisionRefused, fmt.Sprintf(
				"a filter %q exists on the destination but its rules could not be read — cannot prove equality, refusing fail-closed", filtername)
		}
		if filterContentEquivalent(op.Filter, e) {
			return EmailDecisionAlready, ""
		}
		return EmailDecisionRefused, fmt.Sprintf(
			"a filter %q with different content appeared on the destination since the plan — the write call upserts (never-overwrite); re-plan and review", filtername)

	case op.Section == EmailSectionRouting && op.Action == EmailActionSet:
		if !live.RoutingListed {
			return EmailDecisionRefused, "fresh routing re-list failed: " + live.RoutingError
		}
		cur, ok := live.routingFor(op.Domain)
		if !ok {
			return EmailDecisionRefused, fmt.Sprintf("domain %s no longer appears in the routing list", op.Domain)
		}
		if cur == op.Value {
			return EmailDecisionAlready, ""
		}
		if cur == op.DestinationValue {
			return EmailDecisionWrite, ""
		}
		return EmailDecisionRefused, fmt.Sprintf(
			"routing for %s changed since the plan (plan-time %q, now %q) — re-plan and review",
			op.Domain, op.DestinationValue, cur)
	}
	return EmailDecisionRefused, fmt.Sprintf("op %s/%s is not actionable (action %q)", op.Section, op.Key, op.Action)
}

// AutoresponderMatchesContent is the exported content-equality check the
// rollback pre-check uses: the live entry must still carry exactly the
// content the tool applied (same field set and body normalization as the
// plan). A live entry whose body could not be read never matches —
// fail-closed toward refusal.
func AutoresponderMatchesContent(c *EmailAutoresponderContent, e AutoresponderEntry) bool {
	return autoresponderContentEquivalent(c, e)
}

// DefaultValuesEquivalent is the exported, same-account form of the
// class-aware default-address equality (exact match, or same
// :fail:/:blackhole:/account-username class) used by the apply/rollback
// verify paths.
func DefaultValuesEquivalent(a, b, accountUser string) bool {
	return defaultsEquivalent(a, b, accountUser, accountUser)
}

func stringSlicesEqual(a, b []string) bool {
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

// --- apply report -----------------------------------------------------------

// EmailOpResult is one plan op with its apply outcome.
type EmailOpResult struct {
	EmailPlanOp
	Status       string `json:"status"`
	StatusReason string `json:"status_reason,omitempty"`
}

type EmailApplySummary struct {
	Applied        int `json:"applied"`
	AlreadyPresent int `json:"already_present"`
	Refused        int `json:"refused_precondition"`
	Failed         int `json:"failed"`
	Skipped        int `json:"skipped"`
	Manual         int `json:"manual"`
}

// EmailApplyReport records what one `email apply` (or rollback) run
// actually did. The backup records the path of its paired report; the
// report records the path AND sha256 of its backup — bidirectional
// pairing, because the rollback needs the report to know which ops were
// ACTUALLY performed (a create can resolve to already_present).
type EmailApplyReport struct {
	Mode            string `json:"mode"` // "email-apply-report"
	FormatVersion   int    `json:"format_version"`
	RunMode         string `json:"run_mode"` // "apply" | "rollback"
	GeneratedAt     string `json:"generated_at"`
	DestinationUser string `json:"destination_user"`
	PlanFile        string `json:"plan_file,omitempty"`
	PlanSHA256      string `json:"plan_sha256,omitempty"`
	BackupFile      string `json:"backup_file,omitempty"`
	BackupSHA256    string `json:"backup_sha256,omitempty"`
	// BackupNote documents WHY no backup exists when BackupFile is empty
	// (e.g. zero writes decided) — an empty path with no note is invalid.
	BackupNote string            `json:"backup_note,omitempty"`
	Results    []EmailOpResult   `json:"results"`
	Summary    EmailApplySummary `json:"summary"`
}

// SummarizeEmailResults recomputes the summary from the results.
func SummarizeEmailResults(results []EmailOpResult) EmailApplySummary {
	var s EmailApplySummary
	for _, r := range results {
		switch r.Status {
		case EmailOpApplied:
			s.Applied++
		case EmailOpAlready:
			s.AlreadyPresent++
		case EmailOpRefused:
			s.Refused++
		case EmailOpFailed:
			s.Failed++
		case EmailOpSkipped:
			s.Skipped++
		case EmailOpManual:
			s.Manual++
		}
	}
	return s
}

// --- backup -----------------------------------------------------------------

// EmailBackupSection archives one section's verbatim UAPI response plus
// its normalized entries (2B design: raw + normalized).
type EmailBackupSection struct {
	RawUAPIResponse json.RawMessage       `json:"raw_uapi_response"`
	Forwarders      []ForwarderEntry      `json:"forwarders,omitempty"`
	Defaults        []DefaultAddressEntry `json:"default_addresses,omitempty"`
	// Autoresponders (PR 2B-2): normalized entries with their bodies;
	// RawGetResponses archives the verbatim per-address body reads keyed
	// by address (the list raw alone carries no content).
	Autoresponders  []AutoresponderEntry       `json:"autoresponders,omitempty"`
	RawGetResponses map[string]json.RawMessage `json:"raw_get_responses,omitempty"`
	// Filters (PR 2B-3): normalized entries with collected rules.
	Filters []EmailFilterEntry `json:"filters,omitempty"`
	// Routing (PR 2B-3): routing entries.
	Routing []EmailRoutingEntry `json:"routing,omitempty"`
}

// EmailBackup is the pre-write state of every section the plan touches.
// No backup file ⇒ no write.
type EmailBackup struct {
	Mode            string `json:"mode"` // "email-apply-backup"
	FormatVersion   int    `json:"format_version"`
	GeneratedAt     string `json:"generated_at"`
	DestinationUser string `json:"destination_user"`
	PlanFile        string `json:"plan_file,omitempty"`
	PlanSHA256      string `json:"plan_sha256,omitempty"`
	// ReportFile is the path of the paired apply report (recorded at
	// backup time — the report path is known before the first write).
	ReportFile             string                        `json:"report_file"`
	ForwardersByDomain     map[string]EmailBackupSection `json:"forwarders_by_domain,omitempty"`
	DefaultAddresses       *EmailBackupSection           `json:"default_addresses,omitempty"`
	AutorespondersByDomain map[string]EmailBackupSection `json:"autoresponders_by_domain,omitempty"`
	FiltersByAccount       map[string]EmailBackupSection `json:"filters_by_account,omitempty"`
	Routing                *EmailBackupSection           `json:"routing,omitempty"`
}

// backupDefaultFor returns the backed-up default address for a domain.
func (b EmailBackup) backupDefaultFor(domain string) (string, bool) {
	if b.DefaultAddresses == nil {
		return "", false
	}
	for _, d := range b.DefaultAddresses.Defaults {
		if strings.EqualFold(strings.TrimSpace(d.Domain), strings.TrimSpace(domain)) {
			return d.DefaultAddress, true
		}
	}
	return "", false
}

// backupRoutingFor returns the backed-up routing value for a domain.
func (b EmailBackup) backupRoutingFor(domain string) (string, bool) {
	if b.Routing == nil {
		return "", false
	}
	for _, r := range b.Routing.Routing {
		if strings.EqualFold(strings.TrimSpace(r.Domain), strings.TrimSpace(domain)) {
			return r.Routing, true
		}
	}
	return "", false
}

// FilterMatchesContent is the exported content-equality check the
// rollback pre-check uses: the live entry must still carry exactly the
// content the tool applied.
func FilterMatchesContent(c *EmailFilterContent, e EmailFilterEntry) bool {
	return filterContentEquivalent(c, e)
}

// --- rollback ---------------------------------------------------------------

// Rollback op kinds (verb-free names: this file is covered by the
// module-wide email write scan, only the writer files may name the API
// functions).
const (
	EmailRollbackForwarderRemove     = "forwarder_remove"
	EmailRollbackDefaultRestore      = "default_restore"
	EmailRollbackAutoresponderRemove = "autoresponder_remove"
	EmailRollbackFilterRemove        = "filter_remove"
	EmailRollbackRoutingRestore      = "routing_restore"
)

// EmailRollbackOp is one inverse op, computed ONLY for ops the report
// records as applied. Each carries the post-apply state it expects to
// find: rollback refuses an item whose current state diverged (a human
// changed it since — explicit resolution required).
type EmailRollbackOp struct {
	Kind   string `json:"kind"`
	Domain string `json:"domain"`
	// forwarder_remove: the pair to delete (the tool's own create).
	Address   string `json:"address,omitempty"`
	Forwarder string `json:"forwarder,omitempty"`
	// default_restore: the backup value to restore.
	Value string `json:"value,omitempty"`
	// ExpectedCurrent documents the post-apply state the item must still
	// be in: the pair present (forwarder_remove) / the applied value
	// (default_restore, stored here).
	ExpectedCurrent string `json:"expected_current,omitempty"`
	// Autoresponder (autoresponder_remove): the content the tool applied —
	// the expected-current state; rollback refuses to delete an
	// autoresponder whose live content diverged from it (a human
	// customized it since).
	Autoresponder *EmailAutoresponderContent `json:"autoresponder,omitempty"`
	// Filter (filter_remove): the content the tool applied (2B-3).
	Filter *EmailFilterContent `json:"filter,omitempty"`
	// Account carries the per-mailbox account scope for filter ops
	// ("" = account-level).
	Account string `json:"account,omitempty"`
}

// ComputeEmailRollback derives the inverse ops from a report+backup pair:
// delete_forwarder-shaped inverses for the tool's own applied creates
// (the ONLY deletes the tool ever emits; already_present ops are NEVER
// inverted) and default-address restores back to the backup value for
// its own applied sets. It fails closed when the backup lacks a needed
// value.
func ComputeEmailRollback(report EmailApplyReport, backup EmailBackup) ([]EmailRollbackOp, error) {
	if report.RunMode != "apply" {
		return nil, fmt.Errorf("rollback needs an APPLY report, got run_mode %q (rolling back a rollback is not supported)", report.RunMode)
	}
	var out []EmailRollbackOp
	for _, r := range report.Results {
		if r.Status != EmailOpApplied {
			continue
		}
		switch {
		case r.Section == EmailSectionForwarders && r.Action == EmailActionCreate:
			out = append(out, EmailRollbackOp{
				Kind:      EmailRollbackForwarderRemove,
				Domain:    r.Domain,
				Address:   r.Key,
				Forwarder: r.Forward,
			})
		case r.Section == EmailSectionDefaultAddress && r.Action == EmailActionSet:
			backupVal, ok := backup.backupDefaultFor(r.Domain)
			if !ok {
				return nil, fmt.Errorf("backup carries no default address for domain %s — cannot compute its rollback (fail-closed)", r.Domain)
			}
			out = append(out, EmailRollbackOp{
				Kind:            EmailRollbackDefaultRestore,
				Domain:          r.Domain,
				Value:           backupVal,
				ExpectedCurrent: r.Value,
			})
		case r.Section == EmailSectionAutoresponders && r.Action == EmailActionCreate:
			if r.Autoresponder == nil {
				return nil, fmt.Errorf("applied autoresponder create for %s carries no content payload — cannot compute a guarded rollback (fail-closed)", r.Key)
			}
			out = append(out, EmailRollbackOp{
				Kind:          EmailRollbackAutoresponderRemove,
				Domain:        r.Domain,
				Address:       r.Key,
				Autoresponder: r.Autoresponder,
			})
		case r.Section == EmailSectionFilters && r.Action == EmailActionCreate:
			if r.Filter == nil {
				return nil, fmt.Errorf("applied filter create for %s carries no content payload — cannot compute a guarded rollback (fail-closed)", r.Key)
			}
			account, filtername := splitFilterKey(r.Key)
			out = append(out, EmailRollbackOp{
				Kind:    EmailRollbackFilterRemove,
				Address: filtername,
				Filter:  r.Filter,
				Account: account,
			})
		case r.Section == EmailSectionRouting && r.Action == EmailActionSet:
			backupVal, ok := backup.backupRoutingFor(r.Domain)
			if !ok {
				return nil, fmt.Errorf("backup carries no routing for domain %s — cannot compute its rollback (fail-closed)", r.Domain)
			}
			out = append(out, EmailRollbackOp{
				Kind:            EmailRollbackRoutingRestore,
				Domain:          r.Domain,
				Value:           backupVal,
				ExpectedCurrent: r.Value,
			})
		default:
			return nil, fmt.Errorf("applied op %s/%s has unexpected shape (section %s, action %s) — refusing to invert it",
				r.Section, r.Key, r.Section, r.Action)
		}
	}
	return out, nil
}

// ComputeEmailRollbackDegraded is the DOCUMENTED report-loss degradation:
// without the report, the set of ops the tool actually performed is
// unknowable, so forwarder rollback is MANUAL (deleting "present now but
// absent in backup" could destroy a forwarder a human added post-apply —
// never-delete wins) and only the default-address restores remain
// computable from the backup values alone. The returned notes list what
// the operator must resolve by hand.
func ComputeEmailRollbackDegraded(backup EmailBackup) (ops []EmailRollbackOp, manualNotes []string) {
	if backup.DefaultAddresses != nil {
		for _, d := range backup.DefaultAddresses.Defaults {
			ops = append(ops, EmailRollbackOp{
				Kind:   EmailRollbackDefaultRestore,
				Domain: strings.ToLower(strings.TrimSpace(d.Domain)),
				Value:  d.DefaultAddress,
			})
		}
	}
	domains := make([]string, 0, len(backup.ForwardersByDomain))
	for d := range backup.ForwardersByDomain {
		domains = append(domains, d)
	}
	sort.Strings(domains)
	for _, d := range domains {
		manualNotes = append(manualNotes, fmt.Sprintf(
			"forwarders for %s: rollback is MANUAL without the report — compare the live list against the backup and remove only forwarders you know the tool created", d))
	}
	arDomains := make([]string, 0, len(backup.AutorespondersByDomain))
	for d := range backup.AutorespondersByDomain {
		arDomains = append(arDomains, d)
	}
	sort.Strings(arDomains)
	for _, d := range arDomains {
		manualNotes = append(manualNotes, fmt.Sprintf(
			"autoresponders for %s: rollback is MANUAL without the report — compare the live list against the backup and remove only autoresponders you know the tool created", d))
	}
	filterAccounts := make([]string, 0, len(backup.FiltersByAccount))
	for a := range backup.FiltersByAccount {
		filterAccounts = append(filterAccounts, a)
	}
	sort.Strings(filterAccounts)
	for _, a := range filterAccounts {
		scope := a
		if scope == "" {
			scope = "(account-level)"
		}
		manualNotes = append(manualNotes, fmt.Sprintf(
			"filters for %s: rollback is MANUAL without the report — compare the live list against the backup and remove only filters you know the tool created", scope))
	}
	if backup.Routing != nil {
		for _, r := range backup.Routing.Routing {
			ops = append(ops, EmailRollbackOp{
				Kind:   EmailRollbackRoutingRestore,
				Domain: strings.ToLower(strings.TrimSpace(r.Domain)),
				Value:  r.Routing,
			})
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].Domain < ops[j].Domain })
	return ops, manualNotes
}
