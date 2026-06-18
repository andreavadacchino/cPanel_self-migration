package migrate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/domainname"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
)

// SplitMailbox splits "local@domain" on the FINAL "@" (cPanel mailbox locals do
// not contain "@", but splitting on the last one is robust either way). ok is
// false when there is no "@", or when either side is empty.
func SplitMailbox(addr string) (local, domain string, ok bool) {
	i := strings.LastIndex(addr, "@")
	if i <= 0 || i == len(addr)-1 {
		return "", "", false
	}
	return addr[:i], addr[i+1:], true
}

// applyScopeFilter narrows pd in place to the single domain (opts.OnlyDomain) or
// single mailbox (opts.OnlyMailbox) named on the command line, then validates that
// the target exists in the freshly-scanned SOURCE inventory. It runs once, right
// after gatherData and before updateSelectedDomainCoverage, so every downstream
// phase — domain creation, mail (compare/apply/verify), web, and the summaries —
// sees only the target with no per-loop edits. Databases are NEVER touched here
// (--domain excludes them; --mailbox is mail-only), so the account-wide database
// inventory, prefix derivation, and collision detection are left intact. It is a
// no-op when neither filter is set, and returns a clear error when the target is
// absent (so the caller fails fast after the source scan, listing what IS there).
func applyScopeFilter(pd *migrationData, opts Options, doMail, doFile bool, log *logx.Logger) error {
	switch {
	case opts.OnlyMailbox != "":
		local, domain, ok := SplitMailbox(opts.OnlyMailbox)
		if !ok {
			return fmt.Errorf("invalid --mailbox %q: must be local@domain", opts.OnlyMailbox)
		}
		if !mailboxPresent(pd.Mailboxes, local, domain) {
			return fmt.Errorf("mailbox %q not found among active source mailboxes; %s",
				opts.OnlyMailbox, availableMailboxesHint(pd.Mailboxes, domain))
		}
		pd.Mailboxes = filterMailboxesToOne(pd.Mailboxes, local, domain)
		log.Info("scope narrowed to mailbox %s", opts.OnlyMailbox)

	case opts.OnlyDomain != "":
		if !domainname.Has(sourceDomainSet(*pd), opts.OnlyDomain) {
			return fmt.Errorf("domain %q not found in the source account; available source domains: %s",
				opts.OnlyDomain, strings.Join(sourceDomainNames(*pd), ", "))
		}
		if doMail {
			pd.Mailboxes = filterMailboxesToDomain(pd.Mailboxes, opts.OnlyDomain)
		}
		if doFile {
			pd.SrcDocroots = filterDocrootsToDomain(pd.SrcDocroots, opts.OnlyDomain)
		}
		// Warn when every IN-SCOPE slice is empty. Checking only the selected flows
		// matters: under --domain X --mail the docroots slice is left full (not in
		// scope), so a bare len()==0 on both would never fire for a mail-only run.
		if (!doMail || len(pd.Mailboxes) == 0) && (!doFile || len(pd.SrcDocroots) == 0) {
			log.Warn("--domain %s: nothing in scope on the source for the selected flow(s); nothing to migrate", opts.OnlyDomain)
		} else {
			log.Info("scope narrowed to domain %s", opts.OnlyDomain)
		}
	}
	return nil
}

// mailboxPresent reports whether an ACTIVE mailbox matching local@domain exists
// (domain compared canonically; local matched exactly, as it is a maildir path
// segment).
func mailboxPresent(in []model.Mailbox, local, domain string) bool {
	for _, m := range in {
		if m.User == local && domainname.Equal(m.Domain, domain) {
			return true
		}
	}
	return false
}

// filterMailboxesToOne keeps only the mailbox matching local@domain. The result
// uses a fresh backing array so the input is never aliased.
func filterMailboxesToOne(in []model.Mailbox, local, domain string) []model.Mailbox {
	out := in[:0:0]
	for _, m := range in {
		if m.User == local && domainname.Equal(m.Domain, domain) {
			out = append(out, m)
		}
	}
	return out
}

// filterMailboxesToDomain keeps only mailboxes on the given domain (canonical).
func filterMailboxesToDomain(in []model.Mailbox, domain string) []model.Mailbox {
	out := in[:0:0]
	for _, m := range in {
		if domainname.Equal(m.Domain, domain) {
			out = append(out, m)
		}
	}
	return out
}

// filterDocrootsToDomain keeps only source docroot entries for the given domain.
func filterDocrootsToDomain(in []cpanel.DomainDataEntry, domain string) []cpanel.DomainDataEntry {
	out := in[:0:0]
	for _, e := range in {
		if domainname.Equal(e.Domain, domain) {
			out = append(out, e)
		}
	}
	return out
}

// sourceDomainNames returns the source domain names, sorted, for error hints.
func sourceDomainNames(pd migrationData) []string {
	names := make([]string, 0, len(pd.SrcDomains))
	for _, d := range pd.SrcDomains {
		names = append(names, d.Name)
	}
	sort.Strings(names)
	return names
}

// availableMailboxesHint lists the active local-parts on domain, for the
// "mailbox not found" error.
func availableMailboxesHint(in []model.Mailbox, domain string) string {
	var locals []string
	for _, m := range in {
		if domainname.Equal(m.Domain, domain) {
			locals = append(locals, m.User)
		}
	}
	if len(locals) == 0 {
		return fmt.Sprintf("domain %q has no active mailboxes (or is not a source domain)", domain)
	}
	sort.Strings(locals)
	return fmt.Sprintf("available on %s: %s", domain, strings.Join(locals, ", "))
}
