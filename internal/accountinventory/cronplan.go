package accountinventory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Cron apply plan (PR 2A). BuildCronPlan is fully offline: it consumes
// two normalized inventories and produces a reviewable plan of what
// `cron apply` would install into the DESTINATION account's crontab.
// It never connects anywhere and never generates delete ops for
// destination-only entries; the design lives in PR2A_CRON_APPLY_DESIGN.md.
//
// The write primitive is SSH `crontab -` which REPLACES the entire
// crontab: the plan records the complete expected destination crontab
// for the atomic guard, plus per-line ops for the human review.

const (
	CronActionCreate = "create"
	CronActionSkip   = "skip"
	CronActionManual = "manual"
)

const (
	CronSectionJobs = "cron_jobs"
	CronSectionEnv  = "cron_env"
)

// CronPlanOp is the decision for one crontab line.
type CronPlanOp struct {
	Section string `json:"section"`
	Action  string `json:"action"`
	Key     string `json:"key"`
	Reason  string `json:"reason,omitempty"`

	// The installable crontab line (raw, un-redacted). For create ops,
	// this is the line that will be appended to the destination crontab.
	// For skip ops, it records the matched line for verify.
	Line string `json:"line,omitempty"`
	// SourceLine is the original source line (before path adaptation).
	SourceLine string `json:"source_line,omitempty"`
	// PathAdapted is true when /home/<srcuser>/ was replaced with
	// /home/<destuser>/ in the installable line.
	PathAdapted bool `json:"path_adapted,omitempty"`

	// Display context (redacted).
	SourceValue      string `json:"source_value,omitempty"`
	DestinationValue string `json:"destination_value,omitempty"`
}

// CronPlanInfo is a destination-only line: listed, never deleted.
type CronPlanInfo struct {
	Section string `json:"section"`
	Key     string `json:"key"`
	Value   string `json:"value"`
}

type CronPlanSummary struct {
	Create        int `json:"create"`
	Skip          int `json:"skip"`
	Manual        int `json:"manual"`
	Informational int `json:"informational"`
}

type CronApplyPlan struct {
	Mode              string         `json:"mode"`
	FormatVersion     int            `json:"format_version"`
	GeneratedAt       string         `json:"generated_at"`
	SourceFile        string         `json:"source_file,omitempty"`
	SourceSHA256      string         `json:"source_sha256,omitempty"`
	DestinationFile   string         `json:"destination_file,omitempty"`
	DestinationSHA256 string         `json:"destination_sha256,omitempty"`
	SourceUser        string         `json:"source_user"`
	DestinationUser   string         `json:"destination_user"`
	Ops               []CronPlanOp   `json:"ops"`
	Informational     []CronPlanInfo `json:"informational,omitempty"`
	Summary           CronPlanSummary `json:"summary"`

	// PlanTimeDestCrontab is the SHA256 of the raw destination crontab
	// at plan time. The apply guard compares this against the current
	// crontab before writing — any change requires a re-plan.
	PlanTimeDestCrontab string `json:"plan_time_dest_crontab_sha256,omitempty"`
}

// cronJobKey returns the diff-compatible key for a cron job (the redacted
// command — deterministic, human-readable, matches the diff output).
func cronJobKey(j CronJobEntry) string {
	return j.CommandRedacted
}

// cronScheduleStr renders the schedule fields as a single string.
func cronScheduleStr(j CronJobEntry) string {
	if j.Type == "macro" {
		return j.Macro
	}
	return strings.Join([]string{j.Minute, j.Hour, j.DayOfMonth, j.Month, j.DayOfWeek}, " ")
}

// cronJobLine reconstructs the installable crontab line from a job entry.
func cronJobLine(j CronJobEntry) string {
	if j.RawLine != "" {
		return j.RawLine
	}
	if j.Type == "macro" {
		return j.Macro + " " + j.CommandClear
	}
	return cronScheduleStr(j) + " " + j.CommandClear
}

// cronEnvLine reconstructs an installable env line.
func cronEnvLine(e CronEnvEntry) string {
	return e.Name + "=" + e.ValueClear
}

// adaptCronPath replaces /home/<srcUser>/ with /home/<destUser>/ in a
// crontab line, if applicable.
func adaptCronPath(line, srcUser, destUser string) (adapted string, changed bool) {
	if srcUser == destUser || srcUser == "" || destUser == "" {
		return line, false
	}
	old := "/home/" + srcUser + "/"
	new := "/home/" + destUser + "/"
	if !strings.Contains(line, old) {
		return line, false
	}
	return strings.ReplaceAll(line, old, new), true
}

// cronJobsEquivalent reports whether two jobs are functionally identical:
// same schedule, same command, same enabled state. When both sides have
// the clear command collected, the clear text is compared directly for
// correctness; otherwise falls back to SHA256 of the redacted form.
func cronJobsEquivalent(a, b CronJobEntry) bool {
	if cronScheduleStr(a) != cronScheduleStr(b) || a.Enabled != b.Enabled {
		return false
	}
	if a.CommandCollected && b.CommandCollected {
		return a.CommandClear == b.CommandClear
	}
	return a.CommandSHA256 == b.CommandSHA256
}

// cronEnvsEquivalent reports whether two env entries match. When both
// sides have the clear value collected, it is compared directly —
// comparing only ValueRedacted would false-positive on sensitive vars
// where both sides redact to "[REDACTED]" (H1 fix).
func cronEnvsEquivalent(a, b CronEnvEntry) bool {
	if a.Name != b.Name {
		return false
	}
	if a.ValueCollected && b.ValueCollected {
		return a.ValueClear == b.ValueClear
	}
	return a.ValueRedacted == b.ValueRedacted
}

// BuildCronPlan computes the cron apply plan.
func BuildCronPlan(src, dest NormalizedInventory) CronApplyPlan {
	plan := CronApplyPlan{
		Mode:            "cron-apply-plan",
		FormatVersion:   1,
		SourceUser:      src.Account.User,
		DestinationUser: dest.Account.User,
		Ops:             []CronPlanOp{},
	}

	if !src.Cron.Available || !dest.Cron.Available {
		return plan
	}

	planCronJobs(&plan, src, dest)
	planCronEnvs(&plan, src, dest)

	sort.Slice(plan.Ops, func(i, j int) bool {
		a, b := plan.Ops[i], plan.Ops[j]
		if a.Section != b.Section {
			if a.Section == CronSectionEnv {
				return true
			}
			return false
		}
		return a.Key < b.Key
	})

	for _, op := range plan.Ops {
		switch op.Action {
		case CronActionCreate:
			plan.Summary.Create++
		case CronActionSkip:
			plan.Summary.Skip++
		case CronActionManual:
			plan.Summary.Manual++
		}
	}
	plan.Summary.Informational = len(plan.Informational)
	return plan
}

func planCronJobs(plan *CronApplyPlan, src, dest NormalizedInventory) {
	destByHash := map[string][]CronJobEntry{}
	for _, j := range dest.Cron.Jobs {
		destByHash[j.CommandSHA256] = append(destByHash[j.CommandSHA256], j)
	}

	srcSeen := map[string]bool{}
	for _, j := range src.Cron.Jobs {
		if srcSeen[j.CommandSHA256] {
			continue
		}
		srcSeen[j.CommandSHA256] = true

		op := CronPlanOp{
			Section:     CronSectionJobs,
			Key:         cronJobKey(j),
			SourceValue: cronScheduleStr(j) + " enabled=" + fmt.Sprintf("%v", j.Enabled),
		}

		destJobs := destByHash[j.CommandSHA256]
		if len(destJobs) > 0 {
			d := destJobs[0]
			op.DestinationValue = cronScheduleStr(d) + " enabled=" + fmt.Sprintf("%v", d.Enabled)
		}

		switch {
		case !j.CommandCollected:
			op.Action = CronActionManual
			op.Reason = "the source inventory carries no raw cron command (pre-2A artifact) — re-run --account-inventory on the source, then re-plan"
		case !j.Enabled:
			op.Action = CronActionManual
			op.Reason = "disabled cron job — the operator decides whether to recreate it on the destination"
		case len(destJobs) > 0 && cronJobsEquivalent(j, destJobs[0]):
			op.Action = CronActionSkip
			op.Line = cronJobLine(j)
		case len(destJobs) > 0:
			op.Action = CronActionManual
			op.Reason = "the destination has a job with the same command but different schedule or enabled state — resolve by hand"
		default:
			line := cronJobLine(j)
			adapted, changed := adaptCronPath(line, plan.SourceUser, plan.DestinationUser)
			op.Action = CronActionCreate
			op.SourceLine = line
			op.Line = adapted
			op.PathAdapted = changed
		}
		plan.Ops = append(plan.Ops, op)
	}

	for _, j := range dest.Cron.Jobs {
		if srcSeen[j.CommandSHA256] {
			continue
		}
		plan.Informational = append(plan.Informational, CronPlanInfo{
			Section: CronSectionJobs,
			Key:     cronJobKey(j),
			Value:   cronScheduleStr(j),
		})
	}
}

func planCronEnvs(plan *CronApplyPlan, src, dest NormalizedInventory) {
	destByName := map[string]CronEnvEntry{}
	for _, e := range dest.Cron.Environment {
		destByName[e.Name] = e
	}

	srcSeen := map[string]bool{}
	for _, e := range src.Cron.Environment {
		if srcSeen[e.Name] {
			continue
		}
		srcSeen[e.Name] = true

		op := CronPlanOp{
			Section:     CronSectionEnv,
			Key:         e.Name,
			SourceValue: e.ValueRedacted,
		}

		d, onDest := destByName[e.Name]
		if onDest {
			op.DestinationValue = d.ValueRedacted
		}

		switch {
		case !e.ValueCollected:
			op.Action = CronActionManual
			op.Reason = "source env value not collected (pre-2A artifact) — re-run inventory"
		case onDest && cronEnvsEquivalent(e, d):
			op.Action = CronActionSkip
			op.Line = cronEnvLine(e)
		case onDest:
			op.Action = CronActionManual
			op.Reason = fmt.Sprintf("env %s has different value on destination (%s vs %s) — resolve by hand", e.Name, e.ValueRedacted, d.ValueRedacted)
		default:
			op.Action = CronActionCreate
			op.Line = cronEnvLine(e)
		}
		plan.Ops = append(plan.Ops, op)
	}

	for _, e := range dest.Cron.Environment {
		if srcSeen[e.Name] {
			continue
		}
		plan.Informational = append(plan.Informational, CronPlanInfo{
			Section: CronSectionEnv,
			Key:     e.Name,
			Value:   e.ValueRedacted,
		})
	}
}

// CronPlanDestCrontabHash computes the SHA256 of the raw destination
// crontab content, for the plan-time atomic guard.
func CronPlanDestCrontabHash(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(h[:])
}
