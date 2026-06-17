package cpanel

import (
	"context"
	"sort"
)

// ListDocroots returns every domain on a host with its authoritative document
// root, from DomainInfo::domains_data. This is the modern UAPI v3 way to
// discover docroots (no filesystem guessing). Read-only.
//
// The main/addon/sub/parked sections are flattened into a single slice, sorted
// alphabetically by domain name for a STABLE, deterministic order (same rationale
// as ListDomains).
func ListDocroots(ctx context.Context, c Runner) ([]DomainDataEntry, error) {
	data, err := RunUAPI[DomainsData](ctx, c, "DomainInfo", "domains_data", nil)
	if err != nil {
		return nil, err
	}
	return flattenDomainsData(data), nil
}

// flattenDomainsData is the pure mapping from the API data to a flat, sorted
// docroot slice (testable without SSH). The main domain is included only if it
// carries a name (it always does on a real account).
func flattenDomainsData(d DomainsData) []DomainDataEntry {
	var out []DomainDataEntry
	if d.MainDomain.Domain != "" {
		out = append(out, d.MainDomain)
	}
	out = append(out, d.AddonDomains...)
	out = append(out, d.SubDomains...)
	out = append(out, d.ParkedDomains...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out
}
