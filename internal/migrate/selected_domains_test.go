package migrate

import (
	"context"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/dbmig"
	"github.com/tis24dev/cPanel_self-migration/internal/domainname"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/model"
	"github.com/tis24dev/cPanel_self-migration/internal/wpconfig"
)

func TestUpdateSelectedDomainCoverageBlocksDomainsStep8CannotCreate(t *testing.T) {
	pd := migrationData{
		SrcDomains: []model.Domain{{Name: "listed.example", Type: model.Addon}},
		DestDomainSet: map[string]bool{
			"present.example": true,
		},
		Mailboxes: []model.Mailbox{
			{Domain: "mail-missing.example", User: "info"},
			{Domain: "present.example", User: "sales"},
		},
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "web-missing.example", DocumentRoot: "/home/src/web-missing.example", Type: "addon_domain"},
			{Domain: "listed.example", DocumentRoot: "/home/src/listed.example", Type: "addon_domain"},
			{Domain: "db-missing.example", DocumentRoot: "/home/src/db-missing.example", Type: "addon_domain"},
		},
		Databases: []cpanel.DatabaseEntry{{Database: "src_db", Users: []string{"src_user"}}},
		DBUsers:   []cpanel.DBUserEntry{{User: "src_user", Databases: []string{"src_db"}}},
		SiteCreds: []dbmig.SiteCreds{{
			Docroot:    "/home/src/db-missing.example",
			ConfigPath: "/home/src/db-missing.example/wp-config.php",
			Kind:       dbmig.KindWordPress,
			Creds: wpconfig.Creds{
				DBName:     "src_db",
				DBUser:     "src_user",
				DBPassword: "pw",
			},
		}},
	}

	uses := updateSelectedDomainCoverage(&pd, Options{DoMail: true, DoFile: true, DoDB: true}, nil)
	if len(uses) != 6 {
		t.Fatalf("selected uses = %+v, want 6", uses)
	}
	if len(pd.BlockedDomains) != 3 {
		t.Fatalf("BlockedDomains = %+v, want 3 missing domains", pd.BlockedDomains)
	}
	for _, want := range []string{
		"mail-missing.example",
		"mail: info@mail-missing.example",
		"web-missing.example",
		"web: /home/src/web-missing.example",
		"db-missing.example",
		"db: /home/src/db-missing.example/wp-config.php for src_db",
		"Step 8 cannot create",
	} {
		if !strings.Contains(blockedDomainsMessage(pd.BlockedDomains), want) {
			t.Fatalf("BlockedDomains = %+v missing %q", pd.BlockedDomains, want)
		}
	}
	if _, ok := pd.BlockedDomains[domainname.Key("present.example")]; ok {
		t.Fatalf("destination-present domain must not be blocked: %+v", pd.BlockedDomains)
	}
	if _, ok := pd.BlockedDomains[domainname.Key("listed.example")]; ok {
		t.Fatalf("source-listed domain must not be blocked: %+v", pd.BlockedDomains)
	}
}

func TestUpdateSelectedDomainCoverageAllowsSourceOrDestinationCoveredDomains(t *testing.T) {
	pd := migrationData{
		SrcDomains:    []model.Domain{{Name: "source.example", Type: model.Addon}},
		DestDomainSet: map[string]bool{"dest.example": true},
		Mailboxes: []model.Mailbox{
			{Domain: "source.example", User: "info"},
			{Domain: "dest.example", User: "sales"},
		},
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "source.example", DocumentRoot: "/home/src/source.example"},
			{Domain: "dest.example", DocumentRoot: "/home/src/dest.example"},
		},
	}

	updateSelectedDomainCoverage(&pd, Options{DoMail: true, DoFile: true}, nil)
	if len(pd.BlockedDomains) != 0 {
		t.Fatalf("BlockedDomains = %+v, want none", pd.BlockedDomains)
	}
}

func TestAddonLabelPreflightBlocksSelectedAddonLabelCollisions(t *testing.T) {
	pd := migrationData{
		SrcDomains: []model.Domain{
			{Name: "my-site.example", Type: model.Addon},
			{Name: "mysite.example", Type: model.Addon},
		},
		Mailboxes: []model.Mailbox{
			{Domain: "my-site.example", User: "info"},
			{Domain: "mysite.example", User: "sales"},
		},
	}

	uses := updateSelectedDomainCoverage(&pd, Options{DoMail: true}, nil)
	addons, _ := plannedDomainCreates(pd, uses)
	addons = preflightAddonLabelCollisions(&pd, addons, nil)

	if len(addons) != 0 {
		t.Fatalf("colliding addons left to create = %v, want none", addons)
	}
	if len(pd.BlockedDomains) != 2 {
		t.Fatalf("BlockedDomains = %+v, want both colliding domains", pd.BlockedDomains)
	}
	for _, domain := range []string{"my-site.example", "mysite.example"} {
		reason, ok := domainBlocked(pd, domain)
		if !ok {
			t.Fatalf("%s not blocked: %+v", domain, pd.BlockedDomains)
		}
		for _, want := range []string{"addon label collision", "mysiteexample", "my-site.example", "mysite.example"} {
			if !strings.Contains(reason, want) {
				t.Fatalf("blocked reason for %s missing %q: %s", domain, want, reason)
			}
		}
	}
	if len(pd.FailedDomains) != 0 {
		t.Fatalf("preflight collision must not mark failed domains: %+v", pd.FailedDomains)
	}
}

func TestAddonLabelPreflightIgnoresUnselectedDomain(t *testing.T) {
	pd := migrationData{
		SrcDomains: []model.Domain{
			{Name: "my-site.example", Type: model.Addon},
			{Name: "mysite.example", Type: model.Addon},
		},
		Mailboxes: []model.Mailbox{{Domain: "my-site.example", User: "info"}},
	}

	uses := updateSelectedDomainCoverage(&pd, Options{DoMail: true}, nil)
	addons, _ := plannedDomainCreates(pd, uses)
	addons = preflightAddonLabelCollisions(&pd, addons, nil)

	if len(addons) != 1 || addons[0] != "my-site.example" {
		t.Fatalf("selected non-colliding create set = %v, want [my-site.example]", addons)
	}
	if len(pd.BlockedDomains) != 0 {
		t.Fatalf("unselected collision partner must not block selected domain: %+v", pd.BlockedDomains)
	}
}

func TestAddonLabelPreflightBlocksDestinationReservation(t *testing.T) {
	pd := migrationData{
		SrcDomains: []model.Domain{{Name: "foo-bar.example", Type: model.Addon}},
		DestDomains: []model.Domain{
			{Name: "dest-main.example", Type: model.Main},
			{Name: "foobarexample.dest-main.example", Type: model.Sub},
		},
		Mailboxes: []model.Mailbox{{Domain: "foo-bar.example", User: "info"}},
	}
	pd.DestDomainSet = cpanel.DomainNameSet(pd.DestDomains)

	uses := updateSelectedDomainCoverage(&pd, Options{DoMail: true}, nil)
	addons, _ := plannedDomainCreates(pd, uses)
	addons = preflightAddonLabelCollisions(&pd, addons, nil)

	if len(addons) != 0 {
		t.Fatalf("destination-reserved addon left to create = %v, want none", addons)
	}
	reason, ok := domainBlocked(pd, "foo-bar.example")
	if !ok {
		t.Fatalf("foo-bar.example not blocked: %+v", pd.BlockedDomains)
	}
	for _, want := range []string{"addon label collision", "foobarexample", "foobarexample.dest-main.example"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reservation reason missing %q: %s", want, reason)
		}
	}
}

func TestAddonLabelPreflightBlocksPlannedSubdomainReservation(t *testing.T) {
	pd := migrationData{
		SrcDomains: []model.Domain{
			{Name: "foo-bar.com", Type: model.Addon},
			{Name: "foobarcom.example.com", Type: model.Sub},
		},
		DestDomains: []model.Domain{{Name: "example.com", Type: model.Main}},
		Mailboxes: []model.Mailbox{
			{Domain: "foo-bar.com", User: "info"},
			{Domain: "foobarcom.example.com", User: "sales"},
		},
	}
	pd.DestDomainSet = cpanel.DomainNameSet(pd.DestDomains)

	uses := updateSelectedDomainCoverage(&pd, Options{DoMail: true}, nil)
	addons, subs := plannedDomainCreates(pd, uses)
	addons = preflightAddonLabelCollisions(&pd, addons, subs)

	if len(addons) != 0 {
		t.Fatalf("planned-subdomain-reserved addon left to create = %v, want none", addons)
	}
	if len(subs) != 1 || subs[0] != "foobarcom.example.com" {
		t.Fatalf("planned subdomain should remain creatable, got %v", subs)
	}
	reason, ok := domainBlocked(pd, "foo-bar.com")
	if !ok {
		t.Fatalf("foo-bar.com not blocked: %+v", pd.BlockedDomains)
	}
	for _, want := range []string{"addon label collision", "foobarcom", "foobarcom.example.com"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("planned reservation reason missing %q: %s", want, reason)
		}
	}
	if _, blocked := domainBlocked(pd, "foobarcom.example.com"); blocked {
		t.Fatalf("planned subdomain must not be blocked by the addon collision: %+v", pd.BlockedDomains)
	}
}

func TestAddonLabelPreflightUsesDestinationServerNameReservation(t *testing.T) {
	pd := migrationData{
		SrcDomains:  []model.Domain{{Name: "taken.com", Type: model.Addon}},
		DestDomains: []model.Domain{{Name: "example.com", Type: model.Main}, {Name: "custom-addon.example", Type: model.Addon}},
		DestDocroots: []cpanel.DomainDataEntry{
			{Domain: "custom-addon.example", Type: "addon_domain", ServerName: "takencom.example.com"},
		},
		Mailboxes: []model.Mailbox{{Domain: "taken.com", User: "info"}},
	}
	pd.DestDomainSet = cpanel.DomainNameSet(pd.DestDomains)

	uses := updateSelectedDomainCoverage(&pd, Options{DoMail: true}, nil)
	addons, subs := plannedDomainCreates(pd, uses)
	addons = preflightAddonLabelCollisions(&pd, addons, subs)

	if len(addons) != 0 {
		t.Fatalf("destination-servername-reserved addon left to create = %v, want none", addons)
	}
	reason, ok := domainBlocked(pd, "taken.com")
	if !ok {
		t.Fatalf("taken.com not blocked: %+v", pd.BlockedDomains)
	}
	for _, want := range []string{"addon label collision", "takencom.example.com", "custom-addon.example"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("servername reservation reason missing %q: %s", want, reason)
		}
	}
}

func TestAddonLabelPreflightDoesNotDeriveExistingAddonReservationFromDomainName(t *testing.T) {
	pd := migrationData{
		SrcDomains:  []model.Domain{{Name: "mysite.example", Type: model.Addon}},
		DestDomains: []model.Domain{{Name: "example.com", Type: model.Main}, {Name: "my-site.example", Type: model.Addon}},
		DestDocroots: []cpanel.DomainDataEntry{
			{Domain: "my-site.example", Type: "addon_domain", ServerName: "customlabel.example.com"},
		},
		Mailboxes: []model.Mailbox{{Domain: "mysite.example", User: "info"}},
	}
	pd.DestDomainSet = cpanel.DomainNameSet(pd.DestDomains)

	uses := updateSelectedDomainCoverage(&pd, Options{DoMail: true}, nil)
	addons, subs := plannedDomainCreates(pd, uses)
	addons = preflightAddonLabelCollisions(&pd, addons, subs)

	if len(addons) != 1 || addons[0] != "mysite.example" {
		t.Fatalf("custom-label destination addon should not reserve by addon domain name; addons=%v blocked=%+v", addons, pd.BlockedDomains)
	}
	if _, blocked := domainBlocked(pd, "mysite.example"); blocked {
		t.Fatalf("mysite.example should not be blocked by derived addon-domain label: %+v", pd.BlockedDomains)
	}
}

func TestCompareDryRunReportsAddonLabelCollisionAsBlocked(t *testing.T) {
	reason := `addon label collision: cPanel would use internal addon subdomain label "mysiteexample" for my-site.example, mysite.example`
	pd := migrationData{
		SrcDomains: []model.Domain{
			{Name: "my-site.example", Type: model.Addon},
			{Name: "mysite.example", Type: model.Addon},
		},
		Mailboxes: []model.Mailbox{
			{Domain: "my-site.example", User: "info"},
			{Domain: "mysite.example", User: "sales"},
		},
		BlockedDomains: map[string]string{
			domainname.Key("my-site.example"): reason,
			domainname.Key("mysite.example"):  reason,
		},
	}
	var buf strings.Builder
	compareDryRun(context.Background(), &comparator{}, pd, logx.NewTo(&buf, 0), false)

	out := buf.String()
	if !strings.Contains(out, "BLOCKED") || !strings.Contains(out, "addon label collision") || !strings.Contains(out, "2 blocked") {
		t.Fatalf("dry-run should surface addon label collision blocks:\n%s", out)
	}
	if !strings.Contains(out, "info@my-site.example") || !strings.Contains(out, "sales@mysite.example") {
		t.Fatalf("dry-run should surface blocked mailbox rows without reading destination stats:\n%s", out)
	}
	if strings.Contains(out, "MISSING on dest — will create (addon)") {
		t.Fatalf("blocked collision domains must not be previewed as creatable addons:\n%s", out)
	}
	if strings.Contains(out, "TO MIGRATE") {
		t.Fatalf("blocked collision mailboxes must not be previewed as generic to-migrate rows:\n%s", out)
	}
}

func TestUpdateSelectedDomainCoverageUsesCanonicalSourceDomainIdentity(t *testing.T) {
	pd := migrationData{
		SrcDomains: []model.Domain{{Name: "Example.COM", Type: model.Addon}},
		Mailboxes:  []model.Mailbox{{Domain: "example.com", User: "info"}},
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "example.com.", DocumentRoot: "/home/src/example.com"},
		},
	}

	updateSelectedDomainCoverage(&pd, Options{DoMail: true, DoFile: true}, nil)
	if len(pd.BlockedDomains) != 0 {
		t.Fatalf("canonical source domain variant must cover selected data: %+v", pd.BlockedDomains)
	}
}

func TestUpdateSelectedDomainCoverageUsesCanonicalDestinationDomainIdentity(t *testing.T) {
	pd := migrationData{
		DestDomainSet: cpanel.DomainNameSet([]model.Domain{{Name: "example.com."}}),
		Mailboxes:     []model.Mailbox{{Domain: "Example.COM", User: "info"}},
	}

	updateSelectedDomainCoverage(&pd, Options{DoMail: true}, nil)
	if len(pd.BlockedDomains) != 0 {
		t.Fatalf("canonical destination domain variant must cover selected data: %+v", pd.BlockedDomains)
	}
}

func TestUpdateSelectedDomainCoverageRespectsSelectedScopes(t *testing.T) {
	pd := migrationData{
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "web-only.example", DocumentRoot: "/home/src/web-only.example"},
		},
	}

	updateSelectedDomainCoverage(&pd, Options{DoDB: true}, nil)
	if len(pd.BlockedDomains) != 0 {
		t.Fatalf("DB-only preflight must ignore unreferenced web docroots: %+v", pd.BlockedDomains)
	}
	updateSelectedDomainCoverage(&pd, Options{DoFile: true}, nil)
	if _, ok := pd.BlockedDomains[domainname.Key("web-only.example")]; !ok {
		t.Fatalf("file preflight must include selected web docroots: %+v", pd.BlockedDomains)
	}
}

func TestUpdateSelectedDomainCoverageIgnoresDBRegistryOnlyCreds(t *testing.T) {
	pd := migrationData{
		SrcDocroots: []cpanel.DomainDataEntry{
			{Domain: "registry.example", DocumentRoot: "/home/src/registry.example"},
		},
		Databases: []cpanel.DatabaseEntry{{Database: "src_db", Users: []string{"src_user"}}},
		DBUsers:   []cpanel.DBUserEntry{{User: "src_user", Databases: []string{"src_db"}}},
		SiteCreds: []dbmig.SiteCreds{{
			Docroot:      "/home/src/registry.example",
			ConfigPath:   "/home/src/registry.example/wp-config.php",
			FromRegistry: true,
			Creds: wpconfig.Creds{
				DBName:     "src_db",
				DBUser:     "src_user",
				DBPassword: "pw",
			},
		}},
	}

	updateSelectedDomainCoverage(&pd, Options{DoDB: true}, nil)
	if len(pd.BlockedDomains) != 0 {
		t.Fatalf("registry-only DB credentials must not require a site domain: %+v", pd.BlockedDomains)
	}
}

func blockedDomainsMessage(blocked map[string]string) string {
	var b strings.Builder
	for domain, reason := range blocked {
		b.WriteString(domain)
		b.WriteString(": ")
		b.WriteString(reason)
		b.WriteString("\n")
	}
	return b.String()
}
