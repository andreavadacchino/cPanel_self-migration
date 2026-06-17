package dbmig

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/validate"
)

// PlanConflictKind is the kind of data-destroying collision in a built DB plan.
type PlanConflictKind int

const (
	// ConflictDestDB: two or more SOURCE databases map to ONE destination database.
	// --apply provisions+empties+imports each plan item in turn, so the second import
	// drops and overwrites the first's data on the same destination database. Silent,
	// irreversible data loss. Arises with a mixed/restored source inventory where the
	// prefix remap collapses two distinct source names to one destination name.
	ConflictDestDB PlanConflictKind = iota
	// ConflictDestUser: two or more databases are planned under ONE destination user
	// whose passwords are not provably one identical known value. --apply provisions
	// the user once (or per item, last-writer-wins), so only one password takes effect
	// and the other databases' rewritten site configs authenticate with the wrong
	// password. A shared user with the SAME explicit password on every database is
	// legitimate and is NOT flagged.
	ConflictDestUser
)

func (k PlanConflictKind) String() string {
	switch k {
	case ConflictDestDB:
		return "duplicate destination database"
	case ConflictDestUser:
		return "shared destination user with diverging passwords"
	default:
		return "unknown"
	}
}

// PlanConflict is one collision in a built plan that would make --apply destroy or
// corrupt data. SrcDBs are the source databases involved (sorted, for a stable report).
type PlanConflict struct {
	Kind   PlanConflictKind
	Key    string   // the colliding DestDB (ConflictDestDB) or DestUser (ConflictDestUser)
	SrcDBs []string // source databases that collide on Key
	Detail string   // human explanation for the operator
}

// PlanNameViolation is one destination DB/user name that cPanel will reject or
// that fails this tool's defensive identifier checks.
type PlanNameViolation struct {
	SrcDB  string
	Field  string
	Name   string
	Max    int
	Detail string
}

// ValidateDestNameLimits checks the generated destination database/user names
// against cPanel's Mysql::get_restrictions result. It is separate from
// ValidatePlan because length/identifier violations are not collisions; they are
// hard plan errors that must stop provisioning before any write.
func ValidateDestNameLimits(plan []DBPlanItem, maxDBName, maxUserName int, destPrefix NamePrefix) []PlanNameViolation {
	var out []PlanNameViolation
	for _, it := range plan {
		out = append(out, validateDestName(it.SrcDB, "destination database", it.DestDB, maxDBName, destPrefix, databaseNameLength)...)
		out = append(out, validateDestName(it.SrcDB, "destination user", it.DestUser, maxUserName, destPrefix, byteNameLength)...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SrcDB != out[j].SrcDB {
			return out[i].SrcDB < out[j].SrcDB
		}
		if out[i].Field != out[j].Field {
			return out[i].Field < out[j].Field
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func validateDestName(srcDB, field, name string, max int, destPrefix NamePrefix, nameLen func(string) int) []PlanNameViolation {
	var out []PlanNameViolation
	if err := validate.DBName(name); err != nil {
		out = append(out, PlanNameViolation{
			SrcDB:  srcDB,
			Field:  field,
			Name:   name,
			Max:    max,
			Detail: fmt.Sprintf("%s %q for source database %s is invalid: %v", field, name, srcDB, err),
		})
	}
	if destPrefix.Enabled && !strings.HasPrefix(name, destPrefix.Value) {
		out = append(out, PlanNameViolation{
			SrcDB:  srcDB,
			Field:  field,
			Name:   name,
			Max:    max,
			Detail: fmt.Sprintf("%s %q for source database %s does not use destination MySQL prefix %q", field, name, srcDB, destPrefix.Value),
		})
	}
	n := nameLen(name)
	if max > 0 && n > max {
		out = append(out, PlanNameViolation{
			SrcDB:  srcDB,
			Field:  field,
			Name:   name,
			Max:    max,
			Detail: fmt.Sprintf("%s %q for source database %s is %d character(s) by cPanel rules, exceeds cPanel limit %d", field, name, srcDB, n, max),
		})
	}
	return out
}

func databaseNameLength(name string) int {
	return len(name) + strings.Count(name, "_")
}

func byteNameLength(name string) int {
	return len(name)
}

// ValidatePlan returns every data-destroying collision in a built plan, empty if the
// plan is safe to apply. It is the shared preflight: the dry-run warns on the result,
// and --apply refuses (fails) the colliding items before any destructive operation, so
// a collapsed or password-conflicting plan can never overwrite one database with
// another or leave a database behind a wrong password. Pure and deterministic
// (conflicts sorted by Key); unit-tested.
func ValidatePlan(plan []DBPlanItem) []PlanConflict {
	var out []PlanConflict

	// (a) Duplicate destination database: the second import drops+overwrites the first.
	bySrc := map[string][]string{}
	for _, it := range plan {
		bySrc[it.DestDB] = append(bySrc[it.DestDB], it.SrcDB)
	}
	for destDB, srcs := range bySrc {
		if len(srcs) > 1 {
			s := append([]string{}, srcs...)
			sort.Strings(s)
			out = append(out, PlanConflict{
				Kind:   ConflictDestDB,
				Key:    destDB,
				SrcDBs: s,
				Detail: fmt.Sprintf("%d source databases (%s) map to one destination database %q; --apply imports them in turn, each dropping the previous", len(s), joinSorted(s), destDB),
			})
		}
	}

	// (b) Shared destination user whose passwords are not one identical known value.
	type userInfo struct {
		srcs []string
		pwds map[string]bool
	}
	byUser := map[string]*userInfo{}
	for _, it := range plan {
		ui := byUser[it.DestUser]
		if ui == nil {
			ui = &userInfo{pwds: map[string]bool{}}
			byUser[it.DestUser] = ui
		}
		ui.srcs = append(ui.srcs, it.SrcDB)
		ui.pwds[it.Password] = true
	}
	for user, ui := range byUser {
		if len(ui.srcs) < 2 {
			continue
		}
		// Safe iff every database carries the SAME, non-empty password. An empty
		// password means "generate on apply", which diverges per database, so any empty
		// (or any disagreement) among a shared user is a conflict.
		if len(ui.pwds) == 1 && !ui.pwds[""] {
			continue
		}
		s := append([]string{}, ui.srcs...)
		sort.Strings(s)
		out = append(out, PlanConflict{
			Kind:   ConflictDestUser,
			Key:    user,
			SrcDBs: s,
			Detail: fmt.Sprintf("destination user %q is shared by %d databases (%s) whose passwords are not one identical known value; --apply sets one password and leaves the others authenticating with the wrong one", user, len(s), joinSorted(s)),
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func joinSorted(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}
