package migrate

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/domainname"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
	"github.com/tis24dev/cPanel_self-migration/internal/report"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

const temporaryAddonTokenTTL = 15 * time.Minute
const tokenRevokeTimeout = 20 * time.Second

// applyDomains is Step 8: create the source domains missing on the destination,
// preserving type (main/addon/parked -> addon; sub -> subdomain). Addon domains
// need temporary expiring full-access API tokens for the api2 cpsrvd call. Each
// token is revoked immediately after its addon attempt, with a deferred cleanup
// guard for cancellation/panic paths. After creation the destination domain set
// in pd is refreshed.
func applyDomains(ctx context.Context, pool *sshx.Pool, cfg config.Config, pd *migrationData, opts Options, log *logx.Logger, rep *report.Reporter) error {
	log.Step("Creating missing destination domains ...")
	domRep := newDomainReport(rep)

	log.Detail("re-reading destination domains before planning creation ...")
	if err := refreshDestinationDomains(ctx, pool, pd); err != nil {
		return domRep.StepError(err)
	}
	overrides := dbOverrides(cfg)
	docrootsInScope := pd.SrcDocroots != nil || pd.DestDocroots != nil
	if err := refreshDocroots(ctx, pool, pd, log, opts.OnlyDomain); err != nil {
		return domRep.StepError(err)
	}
	uses := updateSelectedDomainCoverage(pd, opts, overrides)
	updateDomainTypeIssuesForUses(pd, uses)
	addons, subs := plannedDomainCreates(*pd, uses)
	addons = preflightAddonLabelCollisions(pd, addons, subs)
	if len(addons) > 0 && len(pd.DestDocroots) == 0 {
		if err := refreshDestinationDocrootsForAddonPreflight(ctx, pool, pd); err != nil {
			return domRep.StepError(err)
		}
		addons = preflightAddonLabelCollisions(pd, addons, subs)
	}

	if len(addons) == 0 && len(subs) == 0 {
		warnDomainTypeIssues(*pd, log, domRep)
		warnMissingDestinationDocroots(*pd, log, domRep)
		warnBlockedDomains(*pd, log, domRep)
		domRep.Summary()
		if n := len(pd.BlockedDomains); n > 0 {
			log.Warn("domain creation step done with %d BLOCKED selected domain(s) — dependent mail/files/databases will be skipped and the run will end with an error", n)
		} else if pd.SrcDocroots == nil && pd.DestDocroots == nil {
			log.OK("no selected domains need creation")
		} else {
			log.OK("no selected domains need creation — refreshed docroots")
		}
		return nil
	}

	log.Detail("to create: %d addon, %d subdomain", len(addons), len(subs))

	// A create that errors is NOT failed immediately: cPanel rejects an
	// already-existing domain with a LOCALIZED "already exists" error (the
	// destination here answers in Polish), and the pre-creation list_domains
	// snapshot can lag, so a domain that is in fact present (re-run, race) would be
	// wrongly marked failed and have its mail/files/databases skipped. Instead,
	// remember each create error and reconcile it AFTER the authoritative re-read
	// below: present-on-dest => idempotent success; still-absent => real failure.
	// This mirrors provisionDest, which decides DB success from real state, not
	// from the localized error text. Keyed by domain -> first create error + kind.
	pendingErr := map[string]domErr{}
	attemptedOK := map[string]string{}

	// Only addon creation needs the api2 token.
	if len(addons) > 0 {
		// Defensive cleanup: revoke any leftover tool tokens from a previous run
		// that crashed before its own revoke (best-effort, read-only list first).
		revokeLeftoverTokens(ctx, pool.Dest, log)

		for _, dom := range addons {
			if err := addAddonWithTemporaryToken(ctx, pool, cfg, dom, log); err != nil {
				// Defer the verdict to reconcileDomainErrors (the domain may already
				// exist). Log at Detail, not Warn — a benign re-run must not look
				// alarming; the real Warn only fires if reconciliation confirms a
				// genuine failure.
				log.Detail("addon %s: create returned an error; will verify existence before deciding: %v", dom, err)
				pendingErr[dom] = domErr{kind: "addon", err: err}
			} else {
				log.Item("addon created: %s", dom)
				attemptedOK[domainname.Key(dom)] = "addon"
			}
		}
	}

	for _, dom := range subs {
		if err := cpanel.AddSubdomain(ctx, pool.Dest, dom); err != nil {
			log.Detail("subdomain %s: create returned an error; will verify existence before deciding: %v", dom, err)
			pendingErr[dom] = domErr{kind: "subdomain", err: err}
		} else {
			log.Item("subdomain created: %s", dom)
			attemptedOK[domainname.Key(dom)] = "subdomain"
		}
	}

	// Re-check state AFTER the write, the read-after-write discipline: a domain
	// just created on the destination now has a destination docroot that did NOT
	// exist when the analysis ran, so re-read the authoritative lists rather than
	// trusting the pre-creation snapshot. Without this, the web-file step (which
	// joins SrcDocroots/DestDocroots) and the database wp-config rewrite (which
	// maps a source docroot to its destination path) would never see the new
	// domain and would silently skip its files. Refresh both the domain NAME set
	// (for the mailbox step) and the docroots (for files + DB), reading from each
	// side again.
	log.Detail("re-reading destination domains and docroots after creation ...")
	if err := refreshDestinationDomains(ctx, pool, pd); err != nil {
		return domRep.StepError(err)
	}

	// Reconcile the deferred create errors against the fresh, authoritative domain
	// set: a domain present now was effectively created (idempotent); one still
	// absent is a real failure. Done BEFORE refreshDocroots so the idempotently-
	// present domains' docroots get picked up for the file/DB steps, and BEFORE the
	// failed-count log below so it reports the post-reconciliation total.
	reconcileDomainErrors(pendingErr, pd.DestDomainSet, pd, log, domRep)

	created := append(append([]string{}, addons...), subs...)
	markAbsentCreatedDomainsFailed(pd, created, log, domRep)
	reportCreatedDomains(attemptedOK, created, *pd, domRep)

	if docrootsInScope {
		if err := refreshDocroots(ctx, pool, pd, log, opts.OnlyDomain); err != nil {
			return domRep.StepError(err)
		}
	} else if len(addons) > 0 {
		if err := refreshDestinationDocrootsForAddonPreflight(ctx, pool, pd); err != nil {
			return domRep.StepError(err)
		}
	}
	uses = updateSelectedDomainCoverage(pd, opts, overrides)
	addons, subs = plannedDomainCreates(*pd, uses)
	// Called for its side effect on pd (it records colliding addon labels in
	// pd.BlockedDomains); the filtered return is not used here, so do not reassign it.
	preflightAddonLabelCollisions(pd, addons, subs)
	updateDomainTypeIssuesForUses(pd, uses)
	warnDomainTypeIssues(*pd, log, domRep)
	warnMissingDestinationDocroots(*pd, log, domRep)
	warnBlockedDomains(*pd, log, domRep)
	domRep.Summary()
	if failed, blocked := len(pd.FailedDomains), len(pd.BlockedDomains); failed > 0 || blocked > 0 {
		log.Warn("domain creation step done with %d FAILED and %d BLOCKED domain(s) — dependent mail/files/databases will be skipped and the run will end with an error", failed, blocked)
	} else {
		log.OK("domain creation step done")
	}
	return nil
}

func refreshDestinationDomains(ctx context.Context, pool *sshx.Pool, pd *migrationData) error {
	destDomains, err := cpanel.ListDomains(ctx, pool.Dest)
	if err != nil {
		return fmt.Errorf("refresh destination domains: %w", err)
	}
	pd.DestDomains = destDomains
	pd.DestDomainSet = cpanel.DomainNameSet(destDomains)
	return nil
}

func refreshDestinationDocrootsForAddonPreflight(ctx context.Context, pool *sshx.Pool, pd *migrationData) error {
	destDocroots, err := cpanel.ListDocroots(ctx, pool.Dest)
	if err != nil {
		return fmt.Errorf("destination docroots for addon-label preflight: %w", err)
	}
	pd.DestDocroots = destDocroots
	return nil
}

func plannedDomainCreates(pd migrationData, uses []selectedDomainUse) (addons, subs []string) {
	selectedDomains := selectedDomainSet(uses)
	for _, d := range pd.SrcDomains {
		if !domainname.Has(selectedDomains, d.Name) {
			continue
		}
		switch model.ActionFor(d.Type, domainname.Has(pd.DestDomainSet, d.Name)) {
		case model.CreateAddon:
			addons = append(addons, d.Name)
		case model.CreateSub:
			subs = append(subs, d.Name)
		}
	}
	return addons, subs
}

// markAbsentCreatedDomainsFailed is belt-and-suspenders for the data contract Step 10
// depends on: a create that returned SUCCESS (so it never entered pendingErr) but
// whose domain is STILL absent from the authoritative refreshed destination set — an
// API that lied, or a cache that has not caught up — would otherwise leave the domain
// unmarked, its mail/files/databases silently skipped while the run exited 0. Any
// domain Step 8 was responsible for creating that is still absent after the refresh is
// a real failure, regardless of what the create API returned. (Domains that ERRORED
// are already handled by reconcileDomainErrors.) Pure except for the logger + pd
// mutation; unit-tested.
func markAbsentCreatedDomainsFailed(pd *migrationData, created []string, log *logx.Logger, rep *domainReport) {
	for _, dom := range created {
		if !domainname.Has(pd.DestDomainSet, dom) && !domainFailed(*pd, dom) {
			log.Warn("%s create reported success but the domain is ABSENT from the destination domain list — treating as FAILED (its mail/files/databases will be skipped). Re-run if this was a provisioning lag.", dom)
			markDomainFailed(pd, dom)
			rep.Failed(dom, "create reported success but domain absent after refresh; dependent mail/files/databases skipped")
		}
	}
}

func reportCreatedDomains(attemptedOK map[string]string, created []string, pd migrationData, rep *domainReport) {
	for _, dom := range created {
		kind := attemptedOK[domainname.Key(dom)]
		if kind == "" || domainFailed(pd, dom) || !domainname.Has(pd.DestDomainSet, dom) {
			continue
		}
		rep.Created(dom, kind)
	}
}

func warnMissingDestinationDocroots(pd migrationData, log *logx.Logger, rep *domainReport) {
	seen := map[string]bool{}
	for _, e := range pd.SrcDocroots {
		if seen[e.Domain] {
			continue
		}
		seen[e.Domain] = true
		if destinationDomainMissingDocroot(pd, e.Domain) {
			log.Warn("%s exists on the destination domain list but has no destination docroot in DomainInfo::domains_data; web files will fail for this domain and DB config rewrites may require manual action", e.Domain)
			rep.Warn(e.Domain, "exists on destination domain list but has no destination docroot in DomainInfo::domains_data; web files will fail and DB config rewrites may require manual action")
		}
	}
}

func warnDomainTypeIssues(pd migrationData, log *logx.Logger, rep *domainReport) {
	domains := make([]string, 0, len(pd.DomainTypeIssues))
	for domain := range pd.DomainTypeIssues {
		domains = append(domains, domain)
	}
	sort.Strings(domains)
	for _, domain := range domains {
		issue := pd.DomainTypeIssues[domain]
		switch {
		case issue.BlockWeb || issue.BlockDBConfig:
			log.Warn("%s; web copy and DB config rewrite will be blocked/manual for this domain", issue.Reason())
			rep.Warn(domain, issue.Reason()+"; web copy and DB config rewrite will be blocked/manual for this domain")
		default:
			log.Warn("%s; mail will still be attempted and web/DB use the destination docroot", issue.Reason())
			rep.Warn(domain, issue.Reason()+"; mail will still be attempted and web/DB use the destination docroot")
		}
	}
}

func warnBlockedDomains(pd migrationData, log *logx.Logger, rep *domainReport) {
	domains := make([]string, 0, len(pd.BlockedDomains))
	for domain := range pd.BlockedDomains {
		domains = append(domains, domain)
	}
	sort.Strings(domains)
	for _, domain := range domains {
		log.Warn("%s — %s; dependent mail/files/databases will be skipped and the run will end with an error", domain, pd.BlockedDomains[domain])
		rep.Blocked(domain, pd.BlockedDomains[domain]+"; dependent mail/files/databases skipped")
	}
}

// markDomainFailed records a domain whose creation failed, so the mail/file/db
// phases skip everything tied to it and the run ends non-zero.
func markDomainFailed(pd *migrationData, dom string) {
	if pd.FailedDomains == nil {
		pd.FailedDomains = map[string]bool{}
	}
	pd.FailedDomains[domainname.Key(dom)] = true
}

// domErr remembers a deferred domain-create error and which kind of create it was
// (for the log line), so reconcileDomainErrors can decide its verdict against the
// authoritative post-creation domain set.
type domErr struct {
	kind string // "addon" | "subdomain"
	err  error
}

// reconcileDomainErrors turns deferred create errors into final verdicts using
// real state, not the (localized) error text — the same discipline as
// provisionDest for databases. For each domain that errored during creation:
//
//   - if it is PRESENT in destSet (re-read from DomainInfo::list_domains after the
//     creation loop) it was effectively created — a re-run, a race, or a stale
//     pre-creation snapshot — so it is treated as an idempotent success and NOT
//     marked failed;
//   - otherwise it is genuinely absent after an authoritative read, so it is a
//     real failure: log a Warn and markDomainFailed (its mail/files/databases are
//     skipped and the run ends non-zero).
//
// A create error that matches a localized "already exists" marker but whose domain
// is STILL absent from the authoritative list is NOT trusted as created: it is
// failed like any other absent domain (with a more specific warning). Trusting the
// marker over the authoritative list would only suppress the failure signal — it
// does not make the domain usable, because every downstream step gates on the same
// absent DestDomainSet/DestDocroots, so the domain's data is skipped regardless. A
// genuine cache lag resolves on a re-run; an "exists under another account" conflict
// is surfaced for the operator instead of silently skipped.
//
// Pure except for the logger and the pd mutation; unit-tested.
func reconcileDomainErrors(pendingErr map[string]domErr, destSet map[string]bool, pd *migrationData, log *logx.Logger, rep *domainReport) {
	for dom, de := range pendingErr {
		switch {
		case domainname.Has(destSet, dom):
			log.Item("%s already present on destination — treated as created (idempotent)", dom)
			rep.Present(dom, de.kind)
			logx.Debug("reconcileDomainErrors: %s (%s) create error was benign (exists on dest now): %v", dom, de.kind, de.err)
		case isAlreadyExists(de.err):
			// The create error matched an "already exists" marker, but the domain is
			// ABSENT from the authoritative list_domains re-read — a contradiction. It
			// must NOT be trusted as created: doing so left the domain unmarked while
			// its mail/files/databases were skipped downstream (those gate on the same
			// absent DestDomainSet/DestDocroots) and the run still exited 0. So fail it
			// loudly. The two realistic causes both want this: a userdata-cache lag
			// resolves on a re-run (the domain then appears in the list and is
			// processed), and a domain that "already exists" under ANOTHER cPanel
			// account is a real conflict to resolve, not a silent skip.
			log.Warn("%s %s reported 'already exists' but is ABSENT from the destination domain list — treating as FAILED (its mail/files/databases will be skipped). If this was a provisioning lag, re-run; if the domain exists under another account, resolve the conflict: %v", de.kind, dom, de.err)
			markDomainFailed(pd, dom)
			rep.Failed(dom, fmt.Sprintf("%s reported 'already exists' but domain absent after refresh: %v", de.kind, de.err))
		default:
			log.Warn("%s %s FAILED — its mail/files/databases will be skipped: %v", de.kind, dom, de.err)
			markDomainFailed(pd, dom)
			rep.Failed(dom, fmt.Sprintf("%s create failed: %v", de.kind, de.err))
		}
	}
}

// revokeLeftoverTokens revokes any of this tool's API tokens still present on the
// destination from a previous run that crashed before revoking its own. It is
// best-effort and never fails the run: a list/revoke error is only logged. This
// bounds the "token left active" risk across crashes, not just within one run.
func revokeLeftoverTokens(ctx context.Context, dest *sshx.Client, log *logx.Logger) {
	names, err := cpanel.ListTokenNames(ctx, dest)
	if err != nil {
		log.Warn("could not list API tokens for leftover cleanup; check cPanel > Manage API Tokens for %q tokens and revoke stale ones manually: %v", cpanel.TokenNamePrefix, err)
		return
	}
	for _, n := range cpanel.LeftoverToolTokens(names) {
		if err := cpanel.RevokeToken(ctx, dest, n); err != nil {
			log.Warn("could not revoke leftover API token %q (revoke it manually in cPanel > Manage API Tokens): %v", n, err)
		} else {
			log.Detail("revoked leftover API token from a previous run: %s", n)
		}
	}
}

type domainReport struct {
	rep     *report.Reporter
	header  bool
	created map[string]bool
	present map[string]bool
	failed  map[string]bool
	blocked map[string]bool
	warned  map[string]bool
	counts  struct {
		created int
		present int
		failed  int
		blocked int
		warned  int
	}
}

func newDomainReport(rep *report.Reporter) *domainReport {
	return &domainReport{
		rep:     rep,
		created: map[string]bool{},
		present: map[string]bool{},
		failed:  map[string]bool{},
		blocked: map[string]bool{},
		warned:  map[string]bool{},
	}
}

func (r *domainReport) ensureHeader() {
	if r == nil || r.rep == nil || r.header {
		return
	}
	r.rep.FileOnlyf("")
	r.rep.FileOnlyf("%s", report.DomainHeaderLine())
	r.header = true
}

func (r *domainReport) Created(domain, kind string) {
	key := kind + "\x00" + domainname.Key(domain)
	if r == nil || r.created[key] {
		return
	}
	r.ensureHeader()
	if r.rep == nil {
		return
	}
	r.created[key] = true
	r.counts.created++
	r.rep.FileOnlyf("%s", report.DomainCreatedLine(domain, kind))
}

func (r *domainReport) Present(domain, kind string) {
	key := kind + "\x00" + domainname.Key(domain)
	if r == nil || r.present[key] {
		return
	}
	r.ensureHeader()
	if r.rep == nil {
		return
	}
	r.present[key] = true
	r.counts.present++
	r.rep.FileOnlyf("%s", report.DomainPresentLine(domain, kind))
}

func (r *domainReport) Failed(domain, reason string) {
	key := domainname.Key(domain)
	if r == nil || r.failed[key] {
		return
	}
	r.ensureHeader()
	if r.rep == nil {
		return
	}
	r.failed[key] = true
	r.counts.failed++
	r.rep.FileOnlyf("%s", report.DomainFailLine(domain, reason))
}

func (r *domainReport) Blocked(domain, reason string) {
	key := domainname.Key(domain)
	if r == nil || r.blocked[key] {
		return
	}
	r.ensureHeader()
	if r.rep == nil {
		return
	}
	r.blocked[key] = true
	r.counts.blocked++
	r.rep.FileOnlyf("%s", report.DomainBlockedLine(domain, reason))
}

func (r *domainReport) Warn(domain, reason string) {
	key := domainname.Key(domain) + "\x00" + reason
	if r == nil || r.warned[key] {
		return
	}
	r.ensureHeader()
	if r.rep == nil {
		return
	}
	r.warned[key] = true
	r.counts.warned++
	r.rep.FileOnlyf("%s", report.DomainWarnLine(domain, reason))
}

func (r *domainReport) Summary() {
	if r == nil || r.rep == nil || !r.header {
		return
	}
	r.rep.FileOnlyf("")
	r.rep.FileOnlyf("%s", report.DomainSummaryLine(r.counts.created, r.counts.present, r.counts.failed, r.counts.blocked, r.counts.warned))
}

func (r *domainReport) StepError(err error) error {
	if err == nil {
		return nil
	}
	r.Failed("domain step", err.Error())
	r.Summary()
	return err
}

func addAddonWithTemporaryToken(ctx context.Context, pool *sshx.Pool, cfg config.Config, dom string, log *logx.Logger) error {
	tokenName, err := cpanel.RandomTokenName()
	if err != nil {
		return fmt.Errorf("create API token: %w", err)
	}
	expiresAt := time.Now().Add(temporaryAddonTokenTTL)
	log.Detail("creating temporary API token (%s, expires in %s) ...", tokenName, temporaryAddonTokenTTL)
	token, err := cpanel.CreateFullAccessToken(ctx, pool.Dest, tokenName, expiresAt)
	if err != nil {
		return fmt.Errorf("create API token: %w", err)
	}

	// If the host ignored our requested expiry (ExpiresAt == 0), the token lives only
	// until the revoke below. Show that caveat on an OVERWRITABLE line: if the run is
	// interrupted before revoke it stays on screen (manual cleanup needed); on a clean
	// revoke the "revoked" line overwrites it so no stale warning lingers.
	var replaceCaveat func(string)
	if token.ExpiresAt == 0 {
		// Keep this caveat SHORT so it never wraps: a transient line that wraps cannot be
		// fully erased in place (\r + clear-to-EOL clears only the last row), leaving stray
		// remnants after the overwrite. The full "where to remove it" instructions live in
		// the revoke-FAILED message below (the case where the operator must act), which is a
		// final committed line and may wrap freely.
		replaceCaveat = log.Notice(fmt.Sprintf("     %s token has no expiry; remove %q* by hand if interrupted", log.Red("!"), cpanel.TokenNamePrefix))
	}

	revokeToken := addonTokenRevokeGuard(pool.Dest, token.Name, log, replaceCaveat)
	defer revokeToken()

	err = cpanel.AddAddonDomain(ctx, pool.Dest, cfg.Dest.SSHUser, token, dom)
	revokeToken()
	return err
}

// addonTokenRevokeGuard returns a once-only revoke. replaceCaveat, when non-nil, is the
// overwrite handle for the no-expiry caveat line: on a clean revoke it replaces the
// caveat with the "revoked" confirmation (so a successful run shows no stale warning);
// on a failed revoke it replaces it with a warning that STAYS (the token may still
// exist). When nil (the host applied an expiry, so no caveat was shown) it prints the
// outcome as an ordinary line.
func addonTokenRevokeGuard(dest *sshx.Client, tokenName string, log *logx.Logger, replaceCaveat func(string)) func() {
	revoked := false
	return func() {
		if revoked {
			return
		}
		revoked = true
		rctx, cancel := context.WithTimeout(context.Background(), tokenRevokeTimeout)
		defer cancel()
		if err := cpanel.RevokeToken(rctx, dest, tokenName); err != nil {
			if replaceCaveat != nil {
				replaceCaveat(fmt.Sprintf("     %s token revoke FAILED; revoke %q manually in cPanel > Manage API Tokens: %v", log.Red("!"), tokenName, err))
			} else {
				log.Warn("token revoke FAILED; revoke %q manually in cPanel > Manage API Tokens: %v", tokenName, err)
			}
			return
		}
		if replaceCaveat != nil {
			replaceCaveat("     -> temporary API token revoked")
		} else {
			log.Detail("temporary API token revoked")
		}
	}
}
