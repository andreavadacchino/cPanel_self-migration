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
	sort.SliceStable(data, func(i, j int) bool { return data[i].FilterName < data[j].FilterName })
	logx.Debug("ListEmailFilters(%q): %d filter(s)", account, len(data))
	return data, nil
}
