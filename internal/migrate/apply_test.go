package migrate

import (
	"strings"
	"testing"
)

// TestApplyOutcome verifies the aggregate exit-status logic: a clean run yields
// nil, and any per-flow apply failure, post-verify divergence, or failed-domain
// cascade produces a non-zero error naming each affected category and pointing at
// the report — so a migration that lost data (whether it errored during the copy
// or only showed up as a verify divergence) is never reported as a clean success.
func TestApplyOutcome(t *testing.T) {
	if err := applyOutcome(applyTally{}); err != nil {
		t.Errorf("applyOutcome(empty) = %v, want nil (clean success)", err)
	}

	cases := []struct {
		name  string
		tally applyTally
		want  []string
	}{
		{"mail apply fail", applyTally{mailFailed: 2}, []string{"2 mailbox(es) failed to migrate"}},
		{"web apply fail", applyTally{webFailed: 1}, []string{"1 docroot(s) failed to copy"}},
		{"db apply fail", applyTally{dbFailed: 3}, []string{"3 database(s) failed to migrate"}},
		{"domains", applyTally{failedDomains: 1}, []string{"1 domain(s) failed to create"}},
		{"blocked domains", applyTally{blockedDomains: 1}, []string{"1 domain(s) blocked by domain creation preflight"}},
		{"mail apply unverified", applyTally{mailUnverified: 1}, []string{"1 mailbox(es) missing source password hash", "account/password not applied"}},
		{"mail verify divergence", applyTally{mailDiff: 4}, []string{"4 mailbox(es) still divergent after verify"}},
		{"web verify divergence", applyTally{webDiff: 1}, []string{"1 docroot(s) still divergent after verify"}},
		{"db verify divergence", applyTally{dbDiff: 2}, []string{"2 database(s) still divergent after verify"}},
		{"db config not rewritten", applyTally{dbConfigNotRewritten: 2},
			[]string{"2 database(s) migrated but their site config was NOT rewritten", "OLD database"}},
		{"db config unmigrated (Tier-C)", applyTally{dbConfigUnmigrated: 1},
			[]string{"1 site(s) use a DB-config format this tool does not migrate/verify", "set their destination DB by hand"}},
		{"all", applyTally{mailFailed: 2, mailUnverified: 8, webFailed: 1, dbFailed: 3, mailDiff: 5, webDiff: 6, dbDiff: 7, failedDomains: 4, blockedDomains: 1},
			[]string{"2 mailbox(es) failed", "1 docroot(s) failed", "3 database(s) failed",
				"5 mailbox(es) still divergent", "6 docroot(s) still divergent", "7 database(s) still divergent",
				"4 domain(s) failed to create", "1 domain(s) blocked", "8 mailbox(es) missing source password hash"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := applyOutcome(c.tally)
			if err == nil {
				t.Fatalf("applyOutcome(%+v) = nil, want an error", c.tally)
			}
			for _, sub := range c.want {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q is missing %q", err.Error(), sub)
				}
			}
			if !strings.Contains(err.Error(), "migration_report.log") {
				t.Errorf("error %q should point at the report log", err.Error())
			}
			if !strings.Contains(err.Error(), "FAIL/UNVERIFIED/skip/verify") {
				t.Errorf("error %q should name every relevant report line class", err.Error())
			}
		})
	}
}
