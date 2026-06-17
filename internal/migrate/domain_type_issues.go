package migrate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/domainname"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
)

type DomainTypeIssue struct {
	Domain           string
	SourceType       model.DomainType
	ExpectedDestType model.DomainType
	DestinationName  string
	DestinationType  model.DomainType
	DestDocroot      string
	DestDocrootType  string
	ReasonText       string

	WarnMail      bool
	BlockWeb      bool
	BlockDBConfig bool
}

func (i DomainTypeIssue) Reason() string {
	if i.ReasonText != "" {
		return i.ReasonText
	}
	reason := fmt.Sprintf("destination domain type mismatch for %s: source %s expects destination %s, destination has %s %q",
		i.Domain, i.SourceType, i.ExpectedDestType, i.DestinationType, i.DestinationName)
	if i.DestDocrootType != "" {
		reason += fmt.Sprintf(" (docroot type %s", i.DestDocrootType)
		if i.DestDocroot != "" {
			reason += " at " + i.DestDocroot
		}
		reason += ")"
	}
	return reason
}

func updateDomainTypeIssuesForUses(pd *migrationData, uses []selectedDomainUse) {
	pd.DomainTypeIssues = nil
	if len(uses) == 0 || len(pd.DestDomains) == 0 {
		return
	}
	selected := selectedDomainUsesByDomain(uses)
	if len(selected) == 0 {
		return
	}

	var issues []DomainTypeIssue
	domains := make([]string, 0, len(selected))
	for domain := range selected {
		domains = append(domains, domain)
	}
	sort.Strings(domains)
	for _, key := range domains {
		raw := selected[key][0].Domain
		destMatches := destDomainEntryMatches(pd.DestDomains, raw)
		if len(destMatches) == 0 {
			continue
		}
		doc, hasDoc := uniqueDestDocrootEntry(pd.DestDocroots, raw)
		if len(destMatches) > 1 {
			issues = append(issues, canonicalDestDomainTypeCollisionIssue(raw, destMatches, doc, hasDoc))
			continue
		}
		src, ok := selectedSourceDomain(*pd, raw)
		if !ok {
			if selectedDomainHasWebOrDB(selected[key]) {
				issues = append(issues, unknownSourceDomainTypeIssue(raw, destMatches[0], doc, hasDoc))
			}
			continue
		}
		dest := destMatches[0]
		if issue, ok := classifyDomainTypeIssue(src, dest, doc, hasDoc); ok {
			issues = append(issues, issue)
		}
	}
	if len(issues) == 0 {
		return
	}
	sort.SliceStable(issues, func(i, j int) bool {
		return domainname.Key(issues[i].Domain) < domainname.Key(issues[j].Domain)
	})
	pd.DomainTypeIssues = make(map[string]DomainTypeIssue, len(issues))
	for _, issue := range issues {
		pd.DomainTypeIssues[domainname.Key(issue.Domain)] = issue
	}
}

func classifyDomainTypeIssue(src, dest model.Domain, doc cpanel.DomainDataEntry, hasDoc bool) (DomainTypeIssue, bool) {
	expected := model.ExpectedDestinationType(src.Type)
	mismatch := !model.CompatibleDestinationType(src.Type, dest.Type)
	issue := DomainTypeIssue{
		Domain:           src.Name,
		SourceType:       src.Type,
		ExpectedDestType: expected,
		DestinationName:  dest.Name,
		DestinationType:  dest.Type,
		WarnMail:         mismatch,
		BlockWeb:         mismatch,
		BlockDBConfig:    mismatch,
	}
	if hasDoc {
		issue.DestDocroot = doc.DocumentRoot
		issue.DestDocrootType = doc.Type
		if docType, ok := domainTypeFromDocrootType(doc.Type); !ok {
			issue.WarnMail = true
			issue.BlockWeb = true
			issue.BlockDBConfig = true
		} else if docType != dest.Type {
			issue.WarnMail = true
			issue.BlockWeb = true
			issue.BlockDBConfig = true
		} else if docType == model.Main || docType == model.Parked {
			issue.WarnMail = true
			issue.BlockWeb = true
			issue.BlockDBConfig = true
		}
	}
	if dest.Type == model.Main || dest.Type == model.Parked {
		issue.WarnMail = true
		issue.BlockWeb = true
		issue.BlockDBConfig = true
	}
	if !issue.WarnMail && !issue.BlockWeb && !issue.BlockDBConfig {
		return DomainTypeIssue{}, false
	}
	return issue, true
}

func selectedDomainUsesByDomain(uses []selectedDomainUse) map[string][]selectedDomainUse {
	out := map[string][]selectedDomainUse{}
	for _, use := range uses {
		if use.Domain == "" {
			continue
		}
		key := domainname.Key(use.Domain)
		out[key] = append(out[key], use)
	}
	return out
}

func selectedDomainHasWebOrDB(uses []selectedDomainUse) bool {
	for _, use := range uses {
		if use.Flow == "web" || use.Flow == "db" {
			return true
		}
	}
	return false
}

func selectedSourceDomain(pd migrationData, domain string) (model.Domain, bool) {
	if src, ok := uniqueSourceDomainEntry(pd.SrcDomains, domain); ok {
		return src, true
	}
	doc, ok := uniqueSourceDocrootEntry(pd.SrcDocroots, domain)
	if !ok {
		return model.Domain{}, false
	}
	t, ok := domainTypeFromDocrootType(doc.Type)
	if !ok {
		return model.Domain{}, false
	}
	return model.Domain{Name: doc.Domain, Type: t}, true
}

func canonicalDestDomainTypeCollisionIssue(domain string, matches []model.Domain, doc cpanel.DomainDataEntry, hasDoc bool) DomainTypeIssue {
	parts := make([]string, 0, len(matches))
	for _, d := range matches {
		parts = append(parts, fmt.Sprintf("%s (%s)", d.Name, d.Type))
	}
	sort.Strings(parts)
	issue := DomainTypeIssue{
		Domain:        domain,
		ReasonText:    fmt.Sprintf("destination canonical domain collision for %q: %s", domain, strings.Join(parts, "; ")),
		WarnMail:      true,
		BlockWeb:      true,
		BlockDBConfig: true,
	}
	if hasDoc {
		issue.DestDocroot = doc.DocumentRoot
		issue.DestDocrootType = doc.Type
	}
	return issue
}

func unknownSourceDomainTypeIssue(domain string, dest model.Domain, doc cpanel.DomainDataEntry, hasDoc bool) DomainTypeIssue {
	issue := DomainTypeIssue{
		Domain:          domain,
		DestinationName: dest.Name,
		DestinationType: dest.Type,
		ReasonText:      fmt.Sprintf("destination domain type cannot be validated for %q: selected web/DB domain is absent from source domain inventory and source docroot type is unknown; refusing web copy and DB config rewrite", domain),
		WarnMail:        true,
		BlockWeb:        true,
		BlockDBConfig:   true,
	}
	if hasDoc {
		issue.DestDocroot = doc.DocumentRoot
		issue.DestDocrootType = doc.Type
	}
	return issue
}

func domainTypeIssue(pd migrationData, domain string) (DomainTypeIssue, bool) {
	issue, ok := pd.DomainTypeIssues[domainname.Key(domain)]
	return issue, ok
}

func uniqueSourceDomainEntry(domains []model.Domain, domain string) (model.Domain, bool) {
	var out model.Domain
	found := false
	for _, d := range domains {
		if !domainname.Equal(d.Name, domain) {
			continue
		}
		if found {
			return model.Domain{}, false
		}
		out = d
		found = true
	}
	return out, found
}

func destDomainEntryMatches(domains []model.Domain, domain string) []model.Domain {
	var matches []model.Domain
	for _, d := range domains {
		if domainname.Equal(d.Name, domain) {
			matches = append(matches, d)
		}
	}
	return matches
}

func uniqueDestDocrootEntry(docroots []cpanel.DomainDataEntry, domain string) (cpanel.DomainDataEntry, bool) {
	var out cpanel.DomainDataEntry
	found := false
	for _, d := range docroots {
		if !domainname.Equal(d.Domain, domain) {
			continue
		}
		if found {
			return cpanel.DomainDataEntry{}, false
		}
		out = d
		found = true
	}
	return out, found
}

func uniqueSourceDocrootEntry(docroots []cpanel.DomainDataEntry, domain string) (cpanel.DomainDataEntry, bool) {
	var out cpanel.DomainDataEntry
	found := false
	for _, d := range docroots {
		if !domainname.Equal(d.Domain, domain) {
			continue
		}
		if found {
			return cpanel.DomainDataEntry{}, false
		}
		out = d
		found = true
	}
	return out, found
}

func domainTypeFromDocrootType(raw string) (model.DomainType, bool) {
	switch raw {
	case "main_domain":
		return model.Main, true
	case "addon_domain":
		return model.Addon, true
	case "sub_domain":
		return model.Sub, true
	case "parked_domain":
		return model.Parked, true
	default:
		return model.Main, false
	}
}
