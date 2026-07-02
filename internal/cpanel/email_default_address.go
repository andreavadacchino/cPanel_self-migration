package cpanel

import (
	"context"
	"sort"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// DefaultAddressEntry is one domain's catch-all configuration. The
// value is kept verbatim (the cPanel default `":fail: No Such User
// Here"` embeds literal double quotes) and must be compared as an
// opaque string.
type DefaultAddressEntry struct {
	Domain         string `json:"domain"`
	DefaultAddress string `json:"defaultaddress"`
}

// ListDefaultAddresses returns the default (catch-all) address of every
// domain on the account, subdomains included, in a single call
// (read-only).
func ListDefaultAddresses(ctx context.Context, c Runner) ([]DefaultAddressEntry, error) {
	data, err := RunUAPI[[]DefaultAddressEntry](ctx, c, "Email", "list_default_address", nil)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(data, func(i, j int) bool { return data[i].Domain < data[j].Domain })
	logx.Debug("ListDefaultAddresses: %d domain(s)", len(data))
	return data, nil
}
