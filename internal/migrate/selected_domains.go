package migrate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/dbmig"
	"github.com/tis24dev/cPanel_self-migration/internal/domainname"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

type selectedDomainUse struct {
	Domain string
	Flow   string
	Item   string
}

type selectedDomainCoverageIssue struct {
	Domain string
	Uses   []selectedDomainUse
}

func updateSelectedDomainCoverage(pd *migrationData, opts Options, overrides map[string]dbmig.Override) []selectedDomainUse {
	uses := selectedApplyDomainUses(*pd, opts, overrides)
	pd.BlockedDomains = nil
	srcSet := sourceDomainSet(*pd)
	bad := map[string][]selectedDomainUse{}
	for _, use := range uses {
		if use.Domain == "" || domainname.Has(srcSet, use.Domain) || domainname.Has(pd.DestDomainSet, use.Domain) {
			continue
		}
		bad[domainname.Key(use.Domain)] = append(bad[domainname.Key(use.Domain)], use)
	}
	if len(bad) == 0 {
		return uses
	}

	domains := make([]string, 0, len(bad))
	for domain := range bad {
		domains = append(domains, domain)
	}
	sort.Strings(domains)

	issues := make([]selectedDomainCoverageIssue, 0, len(domains))
	for _, domain := range domains {
		uses := bad[domain]
		sort.SliceStable(uses, func(i, j int) bool {
			if uses[i].Flow != uses[j].Flow {
				return uses[i].Flow < uses[j].Flow
			}
			return uses[i].Item < uses[j].Item
		})
		issues = append(issues, selectedDomainCoverageIssue{Domain: domain, Uses: uses})
	}
	pd.BlockedDomains = blockedDomainsFromCoverageIssues(issues)
	return uses
}

func selectedDomainSet(uses []selectedDomainUse) map[string]bool {
	set := make(map[string]bool, len(uses))
	for _, use := range uses {
		if use.Domain != "" {
			set[domainname.Key(use.Domain)] = true
		}
	}
	return set
}

func blockedDomainsFromCoverageIssues(issues []selectedDomainCoverageIssue) map[string]string {
	if len(issues) == 0 {
		return nil
	}
	out := make(map[string]string, len(issues))
	for _, issue := range issues {
		uses := make([]string, 0, len(issue.Uses))
		for _, use := range issue.Uses {
			uses = append(uses, use.Flow+": "+use.Item)
		}
		sort.Strings(uses)
		out[issue.Domain] = fmt.Sprintf("domain absent from source domain inventory and destination; Step 8 cannot create it (referenced by %s)", strings.Join(uses, "; "))
	}
	return out
}

func selectedApplyDomainUses(pd migrationData, opts Options, overrides map[string]dbmig.Override) []selectedDomainUse {
	var uses []selectedDomainUse
	seen := map[string]bool{}
	add := func(domain, flow, item string) {
		key := domain + "\x00" + flow + "\x00" + item
		if seen[key] {
			return
		}
		seen[key] = true
		uses = append(uses, selectedDomainUse{Domain: domain, Flow: flow, Item: item})
	}

	if opts.DoMail {
		for _, m := range pd.Mailboxes {
			add(m.Domain, "mail", m.Email())
		}
	}
	if opts.DoFile {
		for _, e := range pd.SrcDocroots {
			add(e.Domain, "web", e.DocumentRoot)
		}
	}
	if opts.DoDB {
		for _, it := range dbPlan(pd, overrides) {
			for _, cfg := range it.Configs {
				entry, ok := srcDocrootContaining(pd, cfg.ConfigPath)
				if !ok {
					logx.Debug("selectedApplyDomainUses: db %s config %s did not resolve to a source docroot — not counted toward domain coverage", it.SrcDB, cfg.ConfigPath)
					continue
				}
				add(entry.Domain, "db", fmt.Sprintf("%s for %s", cfg.ConfigPath, it.SrcDB))
			}
		}
	}
	return uses
}

func sourceDomainSet(pd migrationData) map[string]bool {
	set := make(map[string]bool, len(pd.SrcDomains))
	for _, d := range pd.SrcDomains {
		set[domainname.Key(d.Name)] = true
	}
	return set
}
