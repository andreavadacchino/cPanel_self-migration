package cpanel

import (
	"context"
	"sort"

	"github.com/tis24dev/cPanel_self-migration/internal/domainname"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
)

// ListDomains returns the authoritative domain list of a host with each
// domain's type, using DomainInfo::list_domains as the source of truth (it
// includes configured domains with no mailbox).
//
// Domains are sorted alphabetically by name for a STABLE, deterministic order
// (cPanel's raw JSON key order is not stable between calls — sorting makes every
// run produce the same plan/report).
func ListDomains(ctx context.Context, c Runner) ([]model.Domain, error) {
	data, err := RunUAPI[ListDomainsData](ctx, c, "DomainInfo", "list_domains", nil)
	if err != nil {
		return nil, err
	}
	return domainsFromList(data), nil
}

// domainsFromList is the pure mapping from the API data to the domain slice,
// sorted alphabetically by name (testable without SSH).
func domainsFromList(d ListDomainsData) []model.Domain {
	var out []model.Domain
	if d.MainDomain != "" {
		out = append(out, model.Domain{Name: d.MainDomain, Type: model.Main})
	}
	for _, n := range d.AddonDomains {
		out = append(out, model.Domain{Name: n, Type: model.Addon})
	}
	for _, n := range d.SubDomains {
		out = append(out, model.Domain{Name: n, Type: model.Sub})
	}
	for _, n := range d.ParkedDomains {
		out = append(out, model.Domain{Name: n, Type: model.Parked})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DomainNameSet returns the set of all domain names on a host (any type),
// used to check presence on the destination.
func DomainNameSet(domains []model.Domain) map[string]bool {
	set := make(map[string]bool, len(domains))
	for _, d := range domains {
		set[domainname.Key(d.Name)] = true
	}
	return set
}
