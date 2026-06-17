package webfiles

import (
	"strings"
	"testing"
)

// srcDocroots mirrors the verified SOURCE layout: main docroot == public_html,
// addons in dedicated HOME dirs, one sub.
func srcDocroots() []DocrootEntry {
	return []DocrootEntry{
		{Domain: "main.example", DocumentRoot: "/home/srcacct/public_html", Type: "main_domain"},
		{Domain: "addon1.example", DocumentRoot: "/home/srcacct/addon1.example", Type: "addon_domain"},
		{Domain: "domain3.example", DocumentRoot: "/home/srcacct/domain3.example", Type: "addon_domain"},
		{Domain: "sub1.example", DocumentRoot: "/home/srcacct/sub1.example", Type: "sub_domain"},
	}
}

// destDocroots mirrors the verified DESTINATION layout: every migrated domain is
// an addon/sub under public_html/, plus the destination's OWN main domain which
// has no source counterpart and must never be touched.
func destDocroots() []DocrootEntry {
	return []DocrootEntry{
		{Domain: "destacct.vh.net.pl", DocumentRoot: "/home/destacct/public_html", Type: "main_domain"},
		{Domain: "main.example", DocumentRoot: "/home/destacct/public_html/main.example", Type: "addon_domain"},
		{Domain: "addon1.example", DocumentRoot: "/home/destacct/public_html/addon1.example", Type: "addon_domain"},
		{Domain: "domain3.example", DocumentRoot: "/home/destacct/public_html/domain3.example", Type: "addon_domain"},
		{Domain: "sub1.example", DocumentRoot: "/home/destacct/public_html/sub1.example", Type: "sub_domain"},
	}
}

func TestBuildPlanJoinsByName(t *testing.T) {
	items := BuildPlan(srcDocroots(), destDocroots())

	// 4 source domains -> 4 items, sorted by name. The dest-only main is absent.
	if len(items) != 4 {
		t.Fatalf("got %d items, want 4: %+v", len(items), items)
	}
	wantOrder := []string{"addon1.example", "domain3.example", "main.example", "sub1.example"}
	for i, w := range wantOrder {
		if items[i].Domain != w {
			t.Errorf("item[%d].Domain = %q, want %q", i, items[i].Domain, w)
		}
	}

	byName := map[string]WebPlanItem{}
	for _, it := range items {
		byName[it.Domain] = it
	}

	// Each side keeps its OWN docroot (the whole point of the join).
	tis := byName["main.example"]
	if tis.SrcDocroot != "/home/srcacct/public_html" {
		t.Errorf("srcacction src docroot = %q", tis.SrcDocroot)
	}
	if tis.DestDocroot != "/home/destacct/public_html/main.example" {
		t.Errorf("srcacction dest docroot = %q", tis.DestDocroot)
	}
	if tis.Skip {
		t.Errorf("srcacction should not be skipped: %+v", tis)
	}

	// The destination-only main domain must NOT appear in the plan.
	if _, present := byName["destacct.vh.net.pl"]; present {
		t.Errorf("destination main destacct.vh.net.pl leaked into the plan")
	}
}

func TestBuildPlanSkipsSourceWithoutDestMatch(t *testing.T) {
	src := []DocrootEntry{
		{Domain: "domain4.example", DocumentRoot: "/home/srcacct/domain4.example", Type: "addon_domain"},
		{Domain: "onlysrc.example", DocumentRoot: "/home/srcacct/onlysrc.example", Type: "addon_domain"},
	}
	dest := []DocrootEntry{
		{Domain: "domain4.example", DocumentRoot: "/home/destacct/public_html/domain4.example", Type: "addon_domain"},
	}
	items := BuildPlan(src, dest)
	byName := map[string]WebPlanItem{}
	for _, it := range items {
		byName[it.Domain] = it
	}

	if byName["domain4.example"].Skip {
		t.Errorf("domain4.example has a dest match, should not skip")
	}
	only := byName["onlysrc.example"]
	if !only.Skip {
		t.Errorf("onlysrc.example has no dest match, should skip")
	}
	if only.DestDocroot != "" {
		t.Errorf("skipped item should have empty dest docroot, got %q", only.DestDocroot)
	}
	if len(only.Notes) == 0 {
		t.Errorf("skipped item should carry a note")
	}
}

func TestBuildPlanJoinsByCanonicalDomain(t *testing.T) {
	items := BuildPlan(
		[]DocrootEntry{{Domain: "Example.COM", DocumentRoot: "/home/src/public_html", Type: "addon_domain"}},
		[]DocrootEntry{{Domain: "example.com.", DocumentRoot: "/home/dest/public_html/example.com", Type: "addon_domain"}},
	)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].Skip {
		t.Fatalf("canonical domain match should not skip: %+v", items[0])
	}
	if items[0].Domain != "Example.COM" {
		t.Fatalf("Domain = %q, want source spelling", items[0].Domain)
	}
	if items[0].DestDocroot != "/home/dest/public_html/example.com" {
		t.Fatalf("DestDocroot = %q", items[0].DestDocroot)
	}
}

func TestBuildPlanCanonicalPunycodeCaseMatch(t *testing.T) {
	items := BuildPlan(
		[]DocrootEntry{{Domain: "XN--MNCHEN-3YA.DE", DocumentRoot: "/home/src/site", Type: "addon_domain"}},
		[]DocrootEntry{{Domain: "xn--mnchen-3ya.de", DocumentRoot: "/home/dest/site", Type: "addon_domain"}},
	)
	if len(items) != 1 || items[0].Skip || items[0].DestDocroot != "/home/dest/site" {
		t.Fatalf("punycode case variant should join by canonical domain: %+v", items)
	}
}

func TestBuildPlanCanonicalDestinationCollisionSkips(t *testing.T) {
	items := BuildPlan(
		[]DocrootEntry{{Domain: "example.com", DocumentRoot: "/home/src/site", Type: "addon_domain"}},
		[]DocrootEntry{
			{Domain: "Example.COM", DocumentRoot: "/home/dest/a", Type: "addon_domain"},
			{Domain: "example.com.", DocumentRoot: "/home/dest/b", Type: "addon_domain"},
		},
	)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if !items[0].Skip {
		t.Fatalf("canonical destination collision must skip instead of picking an arbitrary docroot: %+v", items[0])
	}
	if len(items[0].Notes) == 0 || !strings.Contains(items[0].Notes[0], "canonical domain collision") {
		t.Fatalf("collision note missing: %+v", items[0].Notes)
	}
	if items[0].DestDocroot != "" {
		t.Fatalf("collision skip must not choose a destination docroot, got %q", items[0].DestDocroot)
	}
}
