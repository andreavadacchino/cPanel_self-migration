package migrate

import (
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/dbmig"
)

func TestDomainFailed(t *testing.T) {
	pd := migrationData{FailedDomains: map[string]bool{"bad.it": true}}
	if !domainFailed(pd, "bad.it") {
		t.Error("bad.it should be reported failed")
	}
	if domainFailed(pd, "good.it") {
		t.Error("good.it should not be failed")
	}
	// Safe on a nil map.
	if domainFailed(migrationData{}, "x") {
		t.Error("nil FailedDomains must be safe and return false")
	}
}

func TestDBAllDomainsUnavailableForApplyKeepsBlockedReason(t *testing.T) {
	reason := `addon label collision: cPanel would use internal addon subdomain label "mysiteexample" for my-site.example, mysite.example`
	pd := migrationData{
		SrcDocroots: []cpanel.DomainDataEntry{{
			Domain:       "my-site.example",
			DocumentRoot: "/home/u/my-site.example",
		}},
		BlockedDomains: map[string]string{
			"my-site.example": reason,
		},
	}
	it := dbmig.DBPlanItem{SrcDB: "u_wp", Configs: []dbmig.DBConfigRef{{
		Docroot:    "/home/u/my-site.example",
		ConfigPath: "/home/u/my-site.example/wp-config.php",
	}}}

	skip, got := dbAllDomainsUnavailableForApply(pd, it)
	if !skip {
		t.Fatal("DB referenced only by an addon-label collision domain must be skipped")
	}
	if !strings.Contains(got, "addon label collision") || !strings.Contains(got, "mysiteexample") {
		t.Fatalf("skip reason should preserve blocked-domain cause, got %q", got)
	}
	if strings.Contains(got, "inventory coverage") {
		t.Fatalf("skip reason should not use inventory-only wording, got %q", got)
	}
}

// A DB referenced by a FAILED domain AND by a second config that does not resolve to
// any source docroot must NOT be skipped: the unresolved reference might point at a
// still-needed site, and skipping a needed DB loses its data. (Without the
// unresolved-config guard this would skip, because the only RESOLVED domain failed.)
func TestDBAllDomainsUnavailableForApplyMigratesWhenAConfigUnresolved(t *testing.T) {
	pd := migrationData{
		SrcDocroots: []cpanel.DomainDataEntry{{
			Domain:       "bad.example",
			DocumentRoot: "/home/u/bad.example",
		}},
		FailedDomains: map[string]bool{"bad.example": true},
	}
	it := dbmig.DBPlanItem{SrcDB: "u_wp", Configs: []dbmig.DBConfigRef{
		{Docroot: "/home/u/bad.example", ConfigPath: "/home/u/bad.example/wp-config.php"}, // resolves -> failed domain
		{ConfigPath: "/somewhere/unmapped/wp-config.php"},                                 // does NOT resolve to a source docroot
	}}
	if skip, reason := dbAllDomainsUnavailableForApply(pd, it); skip {
		t.Fatalf("a DB with an unresolved referencing config must be migrated, not skipped (reason=%q)", reason)
	}
}

func TestDomainBlocked(t *testing.T) {
	pd := migrationData{BlockedDomains: map[string]string{"bad.it": "inventory gap"}}
	reason, ok := domainBlocked(pd, "bad.it")
	if !ok || reason != "inventory gap" {
		t.Fatalf("domainBlocked(bad.it) = (%q, %v), want inventory gap/true", reason, ok)
	}
	if _, ok := domainBlocked(pd, "good.it"); ok {
		t.Fatal("good.it should not be blocked")
	}
	if _, ok := domainBlocked(migrationData{}, "x"); ok {
		t.Fatal("nil BlockedDomains must be safe and return false")
	}
}

// dbAllDomainsFailed decides whether a database is skipped because every site
// that references it is on a failed or blocked domain.
func TestDBAllDomainsFailed(t *testing.T) {
	// Source docroots that the configs map back to.
	pd := migrationData{
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "bad.it", DocumentRoot: "/home/u/bad.it"},
			{Domain: "good.it", DocumentRoot: "/home/u/good.it"},
		},
		FailedDomains: map[string]bool{"bad.it": true},
		BlockedDomains: map[string]string{
			"blocked.it": "inventory gap",
		},
	}
	pd.SrcDocroots = append(pd.SrcDocroots, cpanel.DomainDataEntry{
		Domain: "blocked.it", DocumentRoot: "/home/u/blocked.it",
	})

	// DB used only by the failed domain -> skip.
	onlyBad := dbmig.DBPlanItem{SrcDB: "u_bad", Configs: []dbmig.DBConfigRef{
		{Docroot: "/home/u/bad.it", ConfigPath: "/home/u/bad.it/wp-config.php"},
	}}
	if !dbAllDomainsFailed(pd, onlyBad) {
		t.Error("DB referenced only by a failed domain must be skipped")
	}

	// DB shared by a failed AND a healthy domain -> keep (good.it still needs it).
	shared := dbmig.DBPlanItem{SrcDB: "u_shared", Configs: []dbmig.DBConfigRef{
		{Docroot: "/home/u/bad.it", ConfigPath: "/home/u/bad.it/wp-config.php"},
		{Docroot: "/home/u/good.it", ConfigPath: "/home/u/good.it/wp-config.php"},
	}}
	if dbAllDomainsFailed(pd, shared) {
		t.Error("DB shared with a healthy domain must NOT be skipped")
	}

	onlyBlocked := dbmig.DBPlanItem{SrcDB: "u_blocked", Configs: []dbmig.DBConfigRef{
		{Docroot: "/home/u/blocked.it", ConfigPath: "/home/u/blocked.it/wp-config.php"},
	}}
	if !dbAllDomainsFailed(pd, onlyBlocked) {
		t.Error("DB referenced only by a blocked domain must be skipped")
	}

	sharedBlocked := dbmig.DBPlanItem{SrcDB: "u_shared_blocked", Configs: []dbmig.DBConfigRef{
		{Docroot: "/home/u/blocked.it", ConfigPath: "/home/u/blocked.it/wp-config.php"},
		{Docroot: "/home/u/good.it", ConfigPath: "/home/u/good.it/wp-config.php"},
	}}
	if dbAllDomainsFailed(pd, sharedBlocked) {
		t.Error("DB shared with a healthy domain must NOT be skipped even when another domain is blocked")
	}

	// Orphan DB (no configs) -> keep migrating regardless of failures.
	orphan := dbmig.DBPlanItem{SrcDB: "u_orphan"}
	if dbAllDomainsFailed(pd, orphan) {
		t.Error("orphan DB (no configs) must not be skipped")
	}

	// No failures at all -> never skip.
	pdClean := migrationData{SrcDocroots: pd.SrcDocroots}
	if dbAllDomainsFailed(pdClean, onlyBad) {
		t.Error("with no failed domains nothing should be skipped")
	}
}
