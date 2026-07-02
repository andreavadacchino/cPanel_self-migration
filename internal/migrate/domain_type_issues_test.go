package migrate

import (
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/domainname"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
)

func TestUpdateDomainTypeIssuesPolicy(t *testing.T) {
	cases := []struct {
		name       string
		srcType    model.DomainType
		destType   model.DomainType
		docType    string
		wantIssue  bool
		wantBlock  bool
		wantReason string
	}{
		{"parked to addon allowed", model.Parked, model.Addon, "addon_domain", false, false, ""},
		{"parked to parked blocked", model.Parked, model.Parked, "parked_domain", true, true, "destination has parked"},
		{"addon to sub blocked", model.Addon, model.Sub, "sub_domain", true, true, "source addon expects destination addon"},
		{"sub to addon blocked", model.Sub, model.Addon, "addon_domain", true, true, "source sub expects destination sub"},
		{"main destination blocked", model.Addon, model.Main, "main_domain", true, true, "destination has main"},
		{"conflicting docroot type blocked", model.Addon, model.Addon, "parked_domain", true, true, "docroot type parked_domain"},
		{"unknown docroot type blocked", model.Addon, model.Addon, "weird_domain", true, true, "docroot type weird_domain"},
		{"same-name main to main allowed", model.Main, model.Main, "main_domain", false, false, ""},
		{"main to main with parked docroot blocked", model.Main, model.Main, "parked_domain", true, true, "docroot type parked_domain"},
		{"main to main with unknown docroot blocked", model.Main, model.Main, "weird_domain", true, true, "docroot type weird_domain"},
		{"main to parked still blocked", model.Main, model.Parked, "parked_domain", true, true, "destination has parked"},
		{"main to addon still expected mapping", model.Main, model.Addon, "addon_domain", false, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pd := migrationData{
				SrcDomains:  []model.Domain{{Name: "example.com", Type: c.srcType}},
				DestDomains: []model.Domain{{Name: "example.com", Type: c.destType}},
				DestDocroots: []cpanel.DomainDataEntry{{
					Domain: "example.com", DocumentRoot: "/home/dest/public_html/example.com", Type: c.docType,
				}},
			}
			updateDomainTypeIssuesForUses(&pd, []selectedDomainUse{{Domain: "example.com", Flow: "web", Item: "/home/src/example.com"}})
			issue, ok := domainTypeIssue(pd, "example.com")
			if ok != c.wantIssue {
				t.Fatalf("domainTypeIssue ok = %v, want %v (%+v)", ok, c.wantIssue, pd.DomainTypeIssues)
			}
			if !c.wantIssue {
				return
			}
			if issue.BlockWeb != c.wantBlock || issue.BlockDBConfig != c.wantBlock {
				t.Fatalf("block flags = web:%v db:%v, want %v; issue=%+v", issue.BlockWeb, issue.BlockDBConfig, c.wantBlock, issue)
			}
			if !strings.Contains(issue.Reason(), c.wantReason) {
				t.Fatalf("Reason() = %q, missing %q", issue.Reason(), c.wantReason)
			}
		})
	}
}

func TestUpdateDomainTypeIssuesMainToMainWithoutDocrootFailsClosed(t *testing.T) {
	pd := migrationData{
		SrcDomains:  []model.Domain{{Name: "example.com", Type: model.Main}},
		DestDomains: []model.Domain{{Name: "example.com", Type: model.Main}},
		// No DestDocroots entry: the destination docroot cannot be validated,
		// so the same-name main→main carve-out must NOT apply.
	}
	updateDomainTypeIssuesForUses(&pd, []selectedDomainUse{{Domain: "example.com", Flow: "web", Item: "/home/src/example.com"}})
	issue, ok := domainTypeIssue(pd, "example.com")
	if !ok {
		t.Fatalf("type issue missing for main→main without destination docroot: %+v", pd.DomainTypeIssues)
	}
	if !issue.BlockWeb || !issue.BlockDBConfig {
		t.Fatalf("main→main without destination docroot should fail closed: %+v", issue)
	}
}

func TestUpdateDomainTypeIssuesMainToMainWithDuplicateDocrootFailsClosed(t *testing.T) {
	pd := migrationData{
		SrcDomains:  []model.Domain{{Name: "example.com", Type: model.Main}},
		DestDomains: []model.Domain{{Name: "example.com", Type: model.Main}},
		// Two docroot rows for the same canonical domain: uniqueDestDocrootEntry
		// collapses this to hasDoc=false, so the carve-out must NOT apply.
		DestDocroots: []cpanel.DomainDataEntry{
			{Domain: "example.com", DocumentRoot: "/home/dest/public_html", Type: "main_domain"},
			{Domain: "Example.COM", DocumentRoot: "/home/dest/public_html/alias", Type: "main_domain"},
		},
	}
	updateDomainTypeIssuesForUses(&pd, []selectedDomainUse{{Domain: "example.com", Flow: "web", Item: "/home/src/example.com"}})
	issue, ok := domainTypeIssue(pd, "example.com")
	if !ok {
		t.Fatalf("type issue missing for main→main with duplicate destination docroots: %+v", pd.DomainTypeIssues)
	}
	if !issue.BlockWeb || !issue.BlockDBConfig {
		t.Fatalf("main→main with duplicate destination docroots should fail closed: %+v", issue)
	}
}

func TestUpdateDomainTypeIssuesUsesCanonicalIdentity(t *testing.T) {
	pd := migrationData{
		SrcDomains:  []model.Domain{{Name: "Example.COM", Type: model.Addon}},
		DestDomains: []model.Domain{{Name: "example.com.", Type: model.Parked}},
		DestDocroots: []cpanel.DomainDataEntry{{
			Domain: "EXAMPLE.com.", DocumentRoot: "/home/dest/public_html/alias", Type: "parked_domain",
		}},
	}
	updateDomainTypeIssuesForUses(&pd, []selectedDomainUse{{Domain: "example.com", Flow: "web", Item: "/home/src/example.com"}})
	issue, ok := pd.DomainTypeIssues[domainname.Key("Example.COM.")]
	if !ok {
		t.Fatalf("canonical type issue missing: %+v", pd.DomainTypeIssues)
	}
	if !issue.BlockWeb || !issue.BlockDBConfig {
		t.Fatalf("parked canonical destination should block web/db: %+v", issue)
	}
}

func TestUpdateDomainTypeIssuesUsesSourceDocrootWhenSourceDomainMissing(t *testing.T) {
	pd := migrationData{
		SrcDocroots: []cpanel.DomainDataEntry{{
			Domain: "ghost.example", DocumentRoot: "/home/src/ghost.example", Type: "addon_domain",
		}},
		DestDomains: []model.Domain{{Name: "ghost.example", Type: model.Parked}},
		DestDocroots: []cpanel.DomainDataEntry{{
			Domain: "ghost.example", DocumentRoot: "/home/dest/public_html/other-site", Type: "parked_domain",
		}},
	}
	updateDomainTypeIssuesForUses(&pd, []selectedDomainUse{{Domain: "ghost.example", Flow: "web", Item: "/home/src/ghost.example"}})
	issue, ok := domainTypeIssue(pd, "ghost.example")
	if !ok {
		t.Fatalf("type issue missing for docroot-only source domain: %+v", pd.DomainTypeIssues)
	}
	if issue.SourceType != model.Addon || !issue.BlockWeb || !issue.BlockDBConfig {
		t.Fatalf("docroot-derived source type should block parked destination: %+v", issue)
	}
}

func TestUpdateDomainTypeIssuesFailsClosedWhenSourceTypeUnknown(t *testing.T) {
	pd := migrationData{
		SrcDocroots: []cpanel.DomainDataEntry{{
			Domain: "ghost.example", DocumentRoot: "/home/src/ghost.example", Type: "",
		}},
		DestDomains: []model.Domain{{Name: "ghost.example", Type: model.Addon}},
		DestDocroots: []cpanel.DomainDataEntry{{
			Domain: "ghost.example", DocumentRoot: "/home/dest/public_html/ghost.example", Type: "addon_domain",
		}},
	}
	updateDomainTypeIssuesForUses(&pd, []selectedDomainUse{{Domain: "ghost.example", Flow: "db", Item: "/home/src/ghost.example/wp-config.php for src_db"}})
	issue, ok := domainTypeIssue(pd, "ghost.example")
	if !ok {
		t.Fatalf("type issue missing for unknown source type: %+v", pd.DomainTypeIssues)
	}
	if !issue.BlockWeb || !issue.BlockDBConfig || !strings.Contains(issue.Reason(), "cannot be validated") {
		t.Fatalf("unknown source type should fail closed for web/db: %+v reason=%q", issue, issue.Reason())
	}
}

func TestUpdateDomainTypeIssuesCanonicalDestinationCollisionFailsClosed(t *testing.T) {
	pd := migrationData{
		SrcDomains: []model.Domain{{Name: "example.com", Type: model.Addon}},
		DestDomains: []model.Domain{
			{Name: "Example.COM", Type: model.Addon},
			{Name: "example.com.", Type: model.Parked},
		},
	}
	updateDomainTypeIssuesForUses(&pd, []selectedDomainUse{{Domain: "example.com", Flow: "web", Item: "/home/src/example.com"}})
	issue, ok := domainTypeIssue(pd, "example.com")
	if !ok {
		t.Fatalf("type issue missing for canonical destination collision: %+v", pd.DomainTypeIssues)
	}
	if !issue.BlockWeb || !issue.BlockDBConfig || !strings.Contains(issue.Reason(), "canonical domain collision") {
		t.Fatalf("canonical collision should fail closed: %+v reason=%q", issue, issue.Reason())
	}
}
