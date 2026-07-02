package cpanel

import (
	"context"
	"sort"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// MXEntry is one MX record row inside a MailRoutingEntry.
type MXEntry struct {
	Domain   string    `json:"domain"`
	MX       string    `json:"mx"`
	Priority flexInt64 `json:"priority"` // quoted string "0" on the live server
}

// MailRoutingEntry is one domain's mail-routing configuration from
// Email::list_mxs. Only mail-routing domains are returned — subdomains
// do not appear even when they carry a default address.
type MailRoutingEntry struct {
	Domain   string `json:"domain"`
	MXCheck  string `json:"mxcheck"`  // configured: local | remote | auto | secondary
	Detected string `json:"detected"` // what cPanel detects from the MX records
	// Bare ints on the capture server; flexInt64 as defensive hardening
	// per the established quoted-string/float lesson.
	Local        flexInt64 `json:"local"`
	Remote       flexInt64 `json:"remote"`
	Secondary    flexInt64 `json:"secondary"`
	AlwaysAccept flexInt64 `json:"alwaysaccept"`
	Entries      []MXEntry `json:"entries"`
}

// ListMXs returns the mail-routing configuration of every mail-routing
// domain on the account (read-only).
func ListMXs(ctx context.Context, c Runner) ([]MailRoutingEntry, error) {
	data, err := RunUAPI[[]MailRoutingEntry](ctx, c, "Email", "list_mxs", nil)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(data, func(i, j int) bool { return data[i].Domain < data[j].Domain })
	for i := range data {
		e := data[i].Entries
		sort.SliceStable(e, func(a, b int) bool {
			if e[a].Priority != e[b].Priority {
				return e[a].Priority < e[b].Priority
			}
			return e[a].MX < e[b].MX
		})
	}
	logx.Debug("ListMXs: %d domain(s)", len(data))
	return data, nil
}
