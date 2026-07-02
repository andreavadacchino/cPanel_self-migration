package cpanel

import (
	"context"
	"sort"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// RedirectEntry is one redirect from Mime::list_redirects. The call
// harvests .htaccess RewriteRules, so CMS-generated rewrites dominate
// real accounts (PR7E_PRE_CAPTURES.md fact 4); classification is the
// policy layer's job, the entry keeps the raw facts.
type RedirectEntry struct {
	Domain      string    `json:"domain"`
	Source      string    `json:"sourceurl"`
	Destination string    `json:"destination"`
	Kind        string    `json:"kind"`       // "rewrite" | "redirect"
	Type        string    `json:"type"`       // "permanent" | "temporary"
	StatusCode  flexInt64 `json:"statuscode"` // null (→0) or quoted "301" on the live server
	Wildcard    flexInt64 `json:"wildcard"`
	MatchWWW    flexInt64 `json:"matchwww"`
}

// ListRedirects returns every redirect/rewrite cPanel reports for the
// account (read-only).
func ListRedirects(ctx context.Context, c Runner) ([]RedirectEntry, error) {
	data, err := RunUAPI[[]RedirectEntry](ctx, c, "Mime", "list_redirects", nil)
	if err != nil {
		return nil, err
	}
	// Full tie-break: the backend harvests .htaccess per docroot and its
	// array order is not proven stable across invocations, so entries
	// sharing Domain+Source must still order deterministically.
	sort.SliceStable(data, func(i, j int) bool {
		a, b := data[i], data[j]
		if a.Domain != b.Domain {
			return a.Domain < b.Domain
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Destination != b.Destination {
			return a.Destination < b.Destination
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.StatusCode < b.StatusCode
	})
	logx.Debug("ListRedirects: %d redirect(s)", len(data))
	return data, nil
}
