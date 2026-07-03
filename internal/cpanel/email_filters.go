package cpanel

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// EmailFilterEntry is one email filter from Email::list_filters. The
// non-empty item shape is docs-derived (no reachable production account
// has filters — PR7E_PRE_CAPTURES.md fact 3), so rules and actions are
// retained as raw JSON: consumers use their COUNTS only, and a shape
// surprise inside a rule body cannot break the decode. If `rules` or
// `actions` ever arrives as a non-array, the decode errors and the
// section degrades to unavailable+warning — fail-safe, never silent.
type EmailFilterEntry struct {
	FilterName string            `json:"filtername"`
	Enabled    flexInt64         `json:"enabled"` // observed as int and quoted string in docs examples
	Rules      []json.RawMessage `json:"rules"`
	Actions    []json.RawMessage `json:"actions"`
}

// FilterRuleDecoded is a get_filter rule entry with typed fields.
type FilterRuleDecoded struct {
	Part   string `json:"part"`
	Match  string `json:"match"`
	Opt    any    `json:"opt"`
	Val    string `json:"val"`
	Number int    `json:"number"`
}

// FilterActionDecoded is a get_filter action entry with typed fields.
type FilterActionDecoded struct {
	Action string  `json:"action"`
	Dest   *string `json:"dest"`
	Number int     `json:"number"`
}

// GetEmailFilterResult is the decoded get_filter response. The response
// shape includes a `number` field per rule/action (positional index) and
// `opt` (always null in all observed responses — 2B-3-pre fact 3).
// Rules and Actions are retained as raw JSON so consumers are not broken
// by shape surprises in the rule/action bodies.
type GetEmailFilterResult struct {
	FilterName string            `json:"filtername"`
	Rules      []json.RawMessage `json:"rules"`
	Actions    []json.RawMessage `json:"actions"`
}

// GetEmailFilter returns a single filter by name (read-only).
// ⚠️ On a NON-EXISTENT filter, cPanel returns status:1 with a TEMPLATE
// response (filtername="Rule 1", 1 empty rule, 1 empty action) — NOT an
// error (2B-3-pre fact 4). Callers must gate existence on list_filters.
func GetEmailFilter(ctx context.Context, c Runner, filtername, account string) (GetEmailFilterResult, error) {
	args := map[string]string{"filtername": filtername}
	if account != "" {
		args["account"] = account
	}
	data, err := RunUAPI[GetEmailFilterResult](ctx, c, "Email", "get_filter", args)
	if err != nil {
		return GetEmailFilterResult{}, err
	}
	logx.Debug("GetEmailFilter(%q, %q): rules=%d actions=%d", filtername, account, len(data.Rules), len(data.Actions))
	return data, nil
}

// ListEmailFilters returns the filters of one scope (read-only):
// account == "" is the account-level (all mail) filter set, otherwise
// the per-mailbox set of that email address.
func ListEmailFilters(ctx context.Context, c Runner, account string) ([]EmailFilterEntry, error) {
	var args map[string]string
	if account != "" {
		args = map[string]string{"account": account}
	}
	data, err := RunUAPI[[]EmailFilterEntry](ctx, c, "Email", "list_filters", args)
	if err != nil {
		return nil, err
	}
	// Tie-break beyond the name: duplicate filter names are possible in
	// a hand-edited filter file and the backend order is not proven
	// stable across invocations.
	sort.SliceStable(data, func(i, j int) bool {
		a, b := data[i], data[j]
		if a.FilterName != b.FilterName {
			return a.FilterName < b.FilterName
		}
		if a.Enabled != b.Enabled {
			return a.Enabled < b.Enabled
		}
		if len(a.Rules) != len(b.Rules) {
			return len(a.Rules) < len(b.Rules)
		}
		return len(a.Actions) < len(b.Actions)
	})
	logx.Debug("ListEmailFilters(%q): %d filter(s)", account, len(data))
	return data, nil
}
