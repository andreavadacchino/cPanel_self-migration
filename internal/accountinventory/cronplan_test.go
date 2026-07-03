package accountinventory

import (
	"strings"
	"testing"
)

func cpInventory(side, user string) NormalizedInventory {
	inv := NewEmptyInventory(user, "host", side)
	inv.Cron.Available = true
	inv.Cron.Method = "ssh_crontab_l"
	return inv
}

func findCronOp(t *testing.T, plan CronApplyPlan, section, key string) CronPlanOp {
	t.Helper()
	for _, op := range plan.Ops {
		if op.Section == section && op.Key == key {
			return op
		}
	}
	t.Fatalf("no cron op for %s/%s in %d ops", section, key, len(plan.Ops))
	return CronPlanOp{}
}

func TestCronPlanCreateActiveJob(t *testing.T) {
	src := cpInventory("source", "srcacct")
	src.Cron.Jobs = []CronJobEntry{
		{Type: "schedule", Minute: "0", Hour: "3", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
			CommandRedacted: "/usr/bin/true", CommandClear: "/usr/bin/true",
			CommandSHA256: "sha256:abc", RawLine: "0 3 * * * /usr/bin/true",
			Enabled: true, CommandCollected: true},
	}
	dest := cpInventory("destination", "destacct")

	p := BuildCronPlan(src, dest)
	op := findCronOp(t, p, CronSectionJobs, "/usr/bin/true")
	if op.Action != CronActionCreate {
		t.Fatalf("active source-only job: action = %q, want create", op.Action)
	}
	if op.Line == "" {
		t.Error("create op must carry the installable line")
	}
}

func TestCronPlanSkipIdenticalJob(t *testing.T) {
	job := CronJobEntry{
		Type: "schedule", Minute: "0", Hour: "3", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		CommandRedacted: "/usr/bin/true", CommandClear: "/usr/bin/true",
		CommandSHA256: "sha256:abc", RawLine: "0 3 * * * /usr/bin/true",
		Enabled: true, CommandCollected: true,
	}
	src := cpInventory("source", "acct")
	src.Cron.Jobs = []CronJobEntry{job}
	dest := cpInventory("destination", "acct")
	dest.Cron.Jobs = []CronJobEntry{job}

	p := BuildCronPlan(src, dest)
	op := findCronOp(t, p, CronSectionJobs, "/usr/bin/true")
	if op.Action != CronActionSkip {
		t.Fatalf("identical job: action = %q, want skip", op.Action)
	}
}

func TestCronPlanDisabledJobManual(t *testing.T) {
	src := cpInventory("source", "acct")
	src.Cron.Jobs = []CronJobEntry{
		{Type: "schedule", Minute: "0", Hour: "0", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
			CommandRedacted: "/usr/bin/false", CommandClear: "/usr/bin/false",
			CommandSHA256: "sha256:def", Enabled: false, CommandCollected: true},
	}
	dest := cpInventory("destination", "acct")

	p := BuildCronPlan(src, dest)
	op := findCronOp(t, p, CronSectionJobs, "/usr/bin/false")
	if op.Action != CronActionManual {
		t.Fatalf("disabled job: action = %q, want manual", op.Action)
	}
	if !strings.Contains(op.Reason, "disabled") {
		t.Errorf("reason %q should mention disabled", op.Reason)
	}
}

func TestCronPlanDifferentScheduleManual(t *testing.T) {
	src := cpInventory("source", "acct")
	src.Cron.Jobs = []CronJobEntry{
		{Type: "schedule", Minute: "0", Hour: "3", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
			CommandRedacted: "/usr/bin/true", CommandClear: "/usr/bin/true",
			CommandSHA256: "sha256:abc", Enabled: true, CommandCollected: true},
	}
	dest := cpInventory("destination", "acct")
	dest.Cron.Jobs = []CronJobEntry{
		{Type: "schedule", Minute: "30", Hour: "4", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
			CommandRedacted: "/usr/bin/true", CommandClear: "/usr/bin/true",
			CommandSHA256: "sha256:abc", Enabled: true, CommandCollected: true},
	}

	p := BuildCronPlan(src, dest)
	op := findCronOp(t, p, CronSectionJobs, "/usr/bin/true")
	if op.Action != CronActionManual {
		t.Fatalf("different schedule: action = %q, want manual", op.Action)
	}
}

func TestCronPlanPathAdaptation(t *testing.T) {
	src := cpInventory("source", "olduser")
	src.Cron.Jobs = []CronJobEntry{
		{Type: "schedule", Minute: "0", Hour: "3", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
			CommandRedacted: "/home/[REDACTED]/backup.sh", CommandClear: "/home/olduser/backup.sh",
			CommandSHA256: "sha256:path1", RawLine: "0 3 * * * /home/olduser/backup.sh",
			Enabled: true, CommandCollected: true},
	}
	dest := cpInventory("destination", "newuser")

	p := BuildCronPlan(src, dest)
	op := findCronOp(t, p, CronSectionJobs, "/home/[REDACTED]/backup.sh")
	if op.Action != CronActionCreate {
		t.Fatalf("action = %q, want create", op.Action)
	}
	if !op.PathAdapted {
		t.Error("path_adapted should be true")
	}
	if !strings.Contains(op.Line, "/home/newuser/") {
		t.Errorf("adapted line %q should contain /home/newuser/", op.Line)
	}
	if !strings.Contains(op.SourceLine, "/home/olduser/") {
		t.Errorf("source line %q should contain /home/olduser/", op.SourceLine)
	}
}

func TestCronPlanNotCollectedManual(t *testing.T) {
	src := cpInventory("source", "acct")
	src.Cron.Jobs = []CronJobEntry{
		{Type: "schedule", Minute: "0", Hour: "0", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
			CommandRedacted: "/usr/bin/something", CommandSHA256: "sha256:old",
			Enabled: true, CommandCollected: false},
	}
	dest := cpInventory("destination", "acct")

	p := BuildCronPlan(src, dest)
	op := findCronOp(t, p, CronSectionJobs, "/usr/bin/something")
	if op.Action != CronActionManual {
		t.Fatalf("not collected: action = %q, want manual", op.Action)
	}
	if !strings.Contains(op.Reason, "pre-2A") {
		t.Errorf("reason %q should mention pre-2A", op.Reason)
	}
}

func TestCronPlanEnvCreate(t *testing.T) {
	src := cpInventory("source", "acct")
	src.Cron.Environment = []CronEnvEntry{
		{Name: "MAILTO", ValueRedacted: "test@example.com", ValueClear: "test@example.com", ValueCollected: true},
	}
	dest := cpInventory("destination", "acct")

	p := BuildCronPlan(src, dest)
	op := findCronOp(t, p, CronSectionEnv, "MAILTO")
	if op.Action != CronActionCreate {
		t.Fatalf("env create: action = %q, want create", op.Action)
	}
	if op.Line != "MAILTO=test@example.com" {
		t.Errorf("env line = %q, want MAILTO=test@example.com", op.Line)
	}
}

func TestCronPlanEnvSkip(t *testing.T) {
	env := CronEnvEntry{Name: "PATH", ValueRedacted: "/usr/bin", ValueClear: "/usr/bin", ValueCollected: true}
	src := cpInventory("source", "acct")
	src.Cron.Environment = []CronEnvEntry{env}
	dest := cpInventory("destination", "acct")
	dest.Cron.Environment = []CronEnvEntry{env}

	p := BuildCronPlan(src, dest)
	op := findCronOp(t, p, CronSectionEnv, "PATH")
	if op.Action != CronActionSkip {
		t.Fatalf("env skip: action = %q, want skip", op.Action)
	}
}

func TestCronPlanEnvDifferentManual(t *testing.T) {
	src := cpInventory("source", "acct")
	src.Cron.Environment = []CronEnvEntry{
		{Name: "MAILTO", ValueRedacted: "src@x.com", ValueClear: "src@x.com", ValueCollected: true},
	}
	dest := cpInventory("destination", "acct")
	dest.Cron.Environment = []CronEnvEntry{
		{Name: "MAILTO", ValueRedacted: "dest@x.com", ValueClear: "dest@x.com", ValueCollected: true},
	}

	p := BuildCronPlan(src, dest)
	op := findCronOp(t, p, CronSectionEnv, "MAILTO")
	if op.Action != CronActionManual {
		t.Fatalf("env different: action = %q, want manual", op.Action)
	}
}

func TestCronPlanSummary(t *testing.T) {
	src := cpInventory("source", "acct")
	src.Cron.Jobs = []CronJobEntry{
		{Type: "schedule", Minute: "0", Hour: "0", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
			CommandRedacted: "/usr/bin/create-me", CommandClear: "/usr/bin/create-me",
			CommandSHA256: "sha256:c1", Enabled: true, CommandCollected: true},
		{Type: "schedule", Minute: "0", Hour: "0", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
			CommandRedacted: "/usr/bin/disabled", CommandClear: "/usr/bin/disabled",
			CommandSHA256: "sha256:d1", Enabled: false, CommandCollected: true},
	}
	dest := cpInventory("destination", "acct")

	p := BuildCronPlan(src, dest)
	if p.Summary.Create != 1 || p.Summary.Manual != 1 {
		t.Errorf("summary = %+v, want create=1 manual=1", p.Summary)
	}
}
