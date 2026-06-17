package migrate

import (
	"context"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
)

// TestWebPlanSkipsDomainMissingFromDestDocroots is the regression for the
// "files of a just-created domain are silently skipped" bug found during the
// first real apply: site2.example was created during the run (step 8) but its
// files were skipped (step 11) because the destination docroot list used by the
// plan was the PRE-creation snapshot, so the domain had no destination match and
// BuildPlan marked it Skip. The fix (refreshDocroots, called right after domain
// creation) re-reads DestDocroots so the domain is present and copied.
//
// This asserts the plan-level cause/cure directly: with the domain ABSENT from
// DestDocroots the item is skipped; once present (what the refresh achieves) it
// is a real copy target.
func TestWebPlanSkipsDomainMissingFromDestDocroots(t *testing.T) {
	src := []cpanel.DomainDataEntry{
		{Domain: "site2.example", DocumentRoot: "/home/srcacct/site2.example", Type: "addon_domain"},
	}

	// BEFORE refresh: the destination docroot list does NOT yet include the
	// just-created domain (this is the stale pre-creation snapshot).
	pdStale := migrationData{
		SrcDocroots: src,
		DestDocroots: []cpanel.DomainDataEntry{
			// only the destination's own main domain — no site2.example
			{Domain: "destacct.vh.net.pl", DocumentRoot: "/home/destacct/public_html", Type: "main_domain"},
		},
	}
	stale := webPlan(pdStale)
	if len(stale) != 1 {
		t.Fatalf("expected 1 plan item, got %d", len(stale))
	}
	if !stale[0].Skip {
		t.Error("BUG REPRO: with the domain absent from DestDocroots the item must be Skip (this was the silent skip)")
	}
	if stale[0].DestDocroot != "" {
		t.Errorf("a skipped item must have no destination docroot, got %q", stale[0].DestDocroot)
	}

	// AFTER refresh: the destination docroot list now includes the created domain
	// (exactly what refreshDocroots re-reads from the destination). The item must
	// become a real copy target.
	pdFresh := migrationData{
		SrcDocroots: src,
		DestDocroots: []cpanel.DomainDataEntry{
			{Domain: "destacct.vh.net.pl", DocumentRoot: "/home/destacct/public_html", Type: "main_domain"},
			{Domain: "site2.example", DocumentRoot: "/home/destacct/public_html/site2.example", Type: "addon_domain"},
		},
	}
	fresh := webPlan(pdFresh)
	if len(fresh) != 1 {
		t.Fatalf("expected 1 plan item, got %d", len(fresh))
	}
	if fresh[0].Skip {
		t.Error("after refresh the domain has a destination docroot and must NOT be skipped")
	}
	if fresh[0].DestDocroot != "/home/destacct/public_html/site2.example" {
		t.Errorf("destination docroot = %q, want the created path", fresh[0].DestDocroot)
	}
}

// TestRefreshDocrootsNoOpWhenNotInScope verifies the mail-only guard: when
// docroots were never gathered (both slices nil), refreshDocroots makes no SSH
// calls and leaves pd untouched. (A nil pool would panic if it tried to read.)
func TestRefreshDocrootsNoOpWhenNotInScope(t *testing.T) {
	pd := migrationData{} // mail-only: SrcDocroots and DestDocroots are nil
	// pool is nil on purpose: if refreshDocroots tried to read, it would panic.
	if err := refreshDocroots(context.TODO(), nil, &pd, nil); err != nil {
		t.Errorf("mail-only refresh must be a no-op returning nil, got %v", err)
	}
	if pd.SrcDocroots != nil || pd.DestDocroots != nil {
		t.Error("mail-only refresh must not populate docroots")
	}
}
