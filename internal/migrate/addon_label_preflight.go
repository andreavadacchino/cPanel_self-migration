package migrate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/domainname"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
)

type addonLabelReservation struct {
	Domain string
	Type   model.DomainType
}

func preflightAddonLabelCollisions(pd *migrationData, addons, subs []string) []string {
	issues := addonLabelCollisionIssues(*pd, addons, subs)
	if len(issues) == 0 {
		return addons
	}
	blocked := map[string]bool{}
	for _, issue := range issues {
		blockDomain(pd, issue.Domain, issue.Reason)
		blocked[domainname.Key(issue.Domain)] = true
	}
	filtered := addons[:0]
	for _, domain := range addons {
		if blocked[domainname.Key(domain)] {
			continue
		}
		filtered = append(filtered, domain)
	}
	return filtered
}

type addonLabelCollisionIssue struct {
	Domain string
	Reason string
}

type addonLabelCandidate struct {
	Domain     string
	DomainKey  string
	LabelKey   string
	InternalFQ string
}

func addonLabelCollisionIssues(pd migrationData, addons, subs []string) []addonLabelCollisionIssue {
	if len(addons) == 0 {
		return nil
	}
	destMain := destinationMainDomain(pd)
	var candidates []addonLabelCandidate
	for _, domain := range addons {
		label := cpanel.AddonLabel(domain)
		labelKey := strings.ToLower(label)
		candidates = append(candidates, addonLabelCandidate{
			Domain:     domain,
			DomainKey:  domainname.Key(domain),
			LabelKey:   labelKey,
			InternalFQ: addonInternalSubdomainFQDN(labelKey, destMain),
		})
	}

	reasons := map[string]string{}
	byLabel := map[string][]addonLabelCandidate{}
	for _, c := range candidates {
		if c.LabelKey == "" {
			reasons[c.DomainKey] = fmt.Sprintf("addon label collision: cPanel would use an empty internal addon subdomain label for %s; Step 8 did not create this domain. Create it manually with a unique addon subdomain label or rename it, then re-run.", c.Domain)
			continue
		}
		byLabel[c.LabelKey] = append(byLabel[c.LabelKey], c)
	}

	for labelKey, group := range byLabel {
		keys := map[string]bool{}
		for _, c := range group {
			keys[c.DomainKey] = true
		}
		if len(keys) <= 1 {
			continue
		}
		domains := addonCandidateDomains(group)
		reason := fmt.Sprintf("addon label collision: cPanel would use internal addon subdomain label %q for %s; Step 8 did not create any domain in this collision group. Create them manually with unique addon subdomain labels or rename/remove one, then re-run.",
			labelKey, strings.Join(domains, ", "))
		for key := range keys {
			reasons[key] = reason
		}
	}

	reservations := addonInternalSubdomainReservations(pd, subs)
	for _, c := range candidates {
		if reasons[c.DomainKey] != "" || c.LabelKey == "" {
			continue
		}
		var conflicts []string
		for _, r := range reservations[domainname.Key(c.InternalFQ)] {
			if domainname.Key(r.Domain) == c.DomainKey {
				continue
			}
			conflicts = append(conflicts, fmt.Sprintf("%s (%s)", r.Domain, r.Type))
		}
		if len(conflicts) == 0 {
			continue
		}
		sort.Strings(conflicts)
		reasons[c.DomainKey] = fmt.Sprintf("addon label collision: cPanel internal addon subdomain label %q for %s would reserve %q, which is already reserved by destination/planned domain(s): %s; Step 8 did not create this domain. Create it manually with a unique addon subdomain label or rename/remove the reservation, then re-run.",
			c.LabelKey, c.Domain, c.InternalFQ, strings.Join(conflicts, ", "))
	}

	if len(reasons) == 0 {
		return nil
	}
	var issues []addonLabelCollisionIssue
	seen := map[string]bool{}
	for _, c := range candidates {
		if seen[c.DomainKey] {
			continue
		}
		reason := reasons[c.DomainKey]
		if reason == "" {
			continue
		}
		issues = append(issues, addonLabelCollisionIssue{Domain: c.Domain, Reason: reason})
		seen[c.DomainKey] = true
	}
	sort.SliceStable(issues, func(i, j int) bool {
		return domainname.Key(issues[i].Domain) < domainname.Key(issues[j].Domain)
	})
	return issues
}

func addonCandidateDomains(group []addonLabelCandidate) []string {
	seen := map[string]bool{}
	var domains []string
	for _, c := range group {
		if seen[c.Domain] {
			continue
		}
		seen[c.Domain] = true
		domains = append(domains, c.Domain)
	}
	sort.Strings(domains)
	return domains
}

func addonInternalSubdomainReservations(pd migrationData, plannedSubs []string) map[string][]addonLabelReservation {
	out := map[string][]addonLabelReservation{}
	add := func(fqdn, domain string, typ model.DomainType) {
		if fqdn == "" {
			return
		}
		out[domainname.Key(fqdn)] = append(out[domainname.Key(fqdn)], addonLabelReservation{Domain: domain, Type: typ})
	}

	for _, d := range pd.DestDomains {
		if d.Type == model.Sub {
			add(d.Name, d.Name, d.Type)
		}
	}
	for _, d := range pd.DestDocroots {
		switch d.Type {
		case "addon_domain":
			add(d.ServerName, d.Domain, model.Addon)
		case "sub_domain":
			add(d.Domain, d.Domain, model.Sub)
			add(d.ServerName, d.Domain, model.Sub)
		}
	}
	for _, domain := range plannedSubs {
		add(domain, domain, model.Sub)
	}
	return out
}

func destinationMainDomain(pd migrationData) string {
	for _, d := range pd.DestDomains {
		if d.Type == model.Main {
			return domainname.Key(d.Name)
		}
	}
	for _, d := range pd.DestDocroots {
		if d.Type == "main_domain" && d.Domain != "" {
			return domainname.Key(d.Domain)
		}
	}
	return ""
}

func addonInternalSubdomainFQDN(labelKey, destMain string) string {
	if labelKey == "" {
		return ""
	}
	if destMain == "" {
		return labelKey
	}
	return labelKey + "." + destMain
}

func blockDomain(pd *migrationData, domain, reason string) {
	if pd.BlockedDomains == nil {
		pd.BlockedDomains = map[string]string{}
	}
	key := domainname.Key(domain)
	if existing := pd.BlockedDomains[key]; existing != "" {
		if strings.Contains(existing, reason) {
			return
		}
		pd.BlockedDomains[key] = existing + "; " + reason
		return
	}
	pd.BlockedDomains[key] = reason
}
