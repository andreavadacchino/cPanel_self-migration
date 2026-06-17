package dbmig

import (
	"reflect"
	"strings"
	"testing"
)

func item(srcDB, destDB, destUser, password string) DBPlanItem {
	return DBPlanItem{SrcDB: srcDB, DestDB: destDB, DestUser: destUser, Password: password}
}

func TestValidatePlanClean(t *testing.T) {
	// Distinct destination DBs; a shared user with the SAME non-empty password is
	// legitimate (one cPanel user owning several databases).
	plan := []DBPlanItem{
		item("a", "d_a", "d_u1", "pw1"),
		item("b", "d_b", "d_u1", "pw1"), // same user, same password -> OK
		item("c", "d_c", "d_u2", ""),    // single-item user, generate -> OK
	}
	if got := ValidatePlan(plan); len(got) != 0 {
		t.Errorf("clean plan must have no conflicts, got %+v", got)
	}
}

func TestValidatePlanDuplicateDestDB(t *testing.T) {
	// Two source databases collapse to one destination database (mixed/restored
	// inventory): --apply would import the second over the first.
	plan := []DBPlanItem{
		item("acc_blog", "dest_blog", "dest_u1", "pw1"),
		item("dest_blog", "dest_blog", "dest_u2", "pw2"), // already dest-prefixed -> same DestDB
	}
	got := ValidatePlan(plan)
	if len(got) != 1 || got[0].Kind != ConflictDestDB {
		t.Fatalf("want one ConflictDestDB, got %+v", got)
	}
	if got[0].Key != "dest_blog" {
		t.Errorf("Key = %q, want dest_blog", got[0].Key)
	}
	if !reflect.DeepEqual(got[0].SrcDBs, []string{"acc_blog", "dest_blog"}) {
		t.Errorf("SrcDBs = %v, want sorted [acc_blog dest_blog]", got[0].SrcDBs)
	}
}

func TestValidatePlanSharedUserConflicts(t *testing.T) {
	cases := []struct {
		name string
		plan []DBPlanItem
		want bool // expect a ConflictDestUser
	}{
		{"same user, same non-empty password is OK",
			[]DBPlanItem{item("a", "d_a", "u", "pw"), item("b", "d_b", "u", "pw")}, false},
		{"same user, DIFFERENT passwords conflict",
			[]DBPlanItem{item("a", "d_a", "u", "pw1"), item("b", "d_b", "u", "pw2")}, true},
		{"same user, one known one generate conflict",
			[]DBPlanItem{item("a", "d_a", "u", "pw"), item("b", "d_b", "u", "")}, true},
		{"same user, both generate conflict (diverge per DB)",
			[]DBPlanItem{item("a", "d_a", "u", ""), item("b", "d_b", "u", "")}, true},
		{"single-item user never conflicts even when generated",
			[]DBPlanItem{item("a", "d_a", "u", "")}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ValidatePlan(c.plan)
			hasUser := false
			for _, x := range got {
				if x.Kind == ConflictDestUser {
					hasUser = true
				}
			}
			if hasUser != c.want {
				t.Errorf("ConflictDestUser = %v, want %v (conflicts: %+v)", hasUser, c.want, got)
			}
		})
	}
}

func TestValidatePlanDeterministicOrder(t *testing.T) {
	// A duplicate DestDB and a shared-user conflict; output is sorted by Kind then Key
	// so the report is stable.
	plan := []DBPlanItem{
		item("a", "dup", "ua", "p1"),
		item("b", "dup", "ub", "p2"), // ConflictDestDB on "dup"
		item("c", "d_c", "shared", "x"),
		item("d", "d_d", "shared", "y"), // ConflictDestUser on "shared"
	}
	got := ValidatePlan(plan)
	if len(got) != 2 {
		t.Fatalf("want 2 conflicts, got %d: %+v", len(got), got)
	}
	if got[0].Kind != ConflictDestDB || got[1].Kind != ConflictDestUser {
		t.Errorf("conflicts not ordered DestDB-then-DestUser: %+v", got)
	}
}

func TestValidateDestNameLimits(t *testing.T) {
	plan := []DBPlanItem{
		item("src_ok", "dest_ok", "dest_user", "pw"),
		item("src_long", "dest_name_is_too_long", "dest_user_is_too_long", "pw"),
		item("src_bad", "dest;bad", "dest_user", "pw"),
	}
	got := ValidateDestNameLimits(plan, 12, 10, NamePrefix{Enabled: true, Value: "dest_"})
	if len(got) != 4 {
		t.Fatalf("want 4 name violations, got %d: %+v", len(got), got)
	}
	var sawLongDB, sawLongUser, sawInvalid, sawMissingPrefix bool
	for _, v := range got {
		if v.SrcDB == "src_long" && v.Field == "destination database" {
			sawLongDB = true
		}
		if v.SrcDB == "src_long" && v.Field == "destination user" {
			sawLongUser = true
		}
		if v.SrcDB == "src_bad" && v.Field == "destination database" {
			sawInvalid = true
		}
		if v.SrcDB == "src_bad" && v.Field == "destination database" && strings.Contains(v.Detail, "does not use destination MySQL prefix") {
			sawMissingPrefix = true
		}
	}
	if !sawLongDB || !sawLongUser || !sawInvalid || !sawMissingPrefix {
		t.Errorf("missing expected violations: %+v", got)
	}
}

func TestValidateDestNameLimitsDatabaseUnderscoresCountTwice(t *testing.T) {
	plan := []DBPlanItem{
		item("src", "ab_cd", "ab_cd", "pw"),
	}
	got := ValidateDestNameLimits(plan, 5, 5, NamePrefix{})
	if len(got) != 1 {
		t.Fatalf("want only database length violation, got %+v", got)
	}
	if got[0].Field != "destination database" {
		t.Fatalf("violation field = %q, want destination database", got[0].Field)
	}
}
