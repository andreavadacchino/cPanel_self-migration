package webui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/events"
)

// Fixtures marshal REAL events.Event values — the same encoder shape the
// writer produces — so the parser is tested against the wire format, not
// against a private mirror.

var monT0 = time.Date(2026, 7, 2, 15, 0, 0, 0, time.UTC)

func monEv(runID string, offset time.Duration, phase events.Phase, typ events.EventType, level events.Level, msg string, data any) events.Event {
	return events.Event{
		RunID: runID, TS: monT0.Add(offset), Level: level,
		Phase: phase, Type: typ, Message: msg, Data: data,
	}
}

func monLines(t *testing.T, evs ...events.Event) []byte {
	t.Helper()
	var sb strings.Builder
	for _, ev := range evs {
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	return []byte(sb.String())
}

// monNow is "shortly after the last fixture event": runs look live.
var monNow = monT0.Add(2 * time.Minute)

func fullApplyRun(runID string) []events.Event {
	return []events.Event{
		monEv(runID, 0, "", events.EventRunStarted, events.LevelInfo, "migration started — mode: APPLY", nil),
		monEv(runID, 1*time.Second, events.PhaseConnect, events.EventPhaseStarted, events.LevelInfo, "", nil),
		monEv(runID, 2*time.Second, events.PhaseConnect, events.EventPhaseCompleted, events.LevelInfo, "", nil),
		monEv(runID, 3*time.Second, events.PhaseCreateDomains, events.EventPhaseStarted, events.LevelInfo, "", nil),
		monEv(runID, 4*time.Second, events.PhaseCreateDomains, events.EventPhaseCompleted, events.LevelInfo, "", map[string]any{
			"failed_domains": []string{}, "blocked_domains": []string{}}),
		monEv(runID, 5*time.Second, events.PhaseMigrateMail, events.EventPhaseStarted, events.LevelInfo, "", nil),
		monEv(runID, 20*time.Second, events.PhaseMigrateMail, events.EventPhaseCompleted, events.LevelInfo, "", map[string]any{
			"items": []map[string]any{
				{"item": "info@example.com", "status": "migrated"},
				{"item": "shop@example.com", "status": "unchanged"},
				{"item": "bad@example.com", "status": "failed", "note": "quota"},
			},
			"failed": 1, "unverified": 0}),
		monEv(runID, 21*time.Second, events.PhaseVerifyMail, events.EventPhaseStarted, events.LevelInfo, "", nil),
		monEv(runID, 22*time.Second, events.PhaseVerifyMail, events.EventPhaseCompleted, events.LevelInfo, "", map[string]any{"divergent": 0}),
		monEv(runID, 23*time.Second, events.PhaseCopyFiles, events.EventPhaseStarted, events.LevelInfo, "", nil),
		monEv(runID, 40*time.Second, events.PhaseCopyFiles, events.EventPhaseCompleted, events.LevelInfo, "", map[string]any{"failed": 0}),
		monEv(runID, 41*time.Second, events.PhaseVerifyFiles, events.EventPhaseCompleted, events.LevelInfo, "", map[string]any{"divergent": 0}),
		monEv(runID, 42*time.Second, events.PhaseMigrateDB, events.EventPhaseCompleted, events.LevelInfo, "", map[string]any{
			"migrated": []string{"dst_wp"}, "failed": 0, "config_not_rewritten": 0, "config_unmigrated": 0}),
		monEv(runID, 43*time.Second, events.PhaseVerifyDB, events.EventPhaseCompleted, events.LevelInfo, "", map[string]any{"divergent": 0}),
		monEv(runID, 44*time.Second, "", events.EventRunCompleted, events.LevelInfo, "migration complete", nil),
	}
}

func monPhase(t *testing.T, m *runMonitor, phase string) monitorPhase {
	t.Helper()
	for _, p := range m.Phases {
		if p.Phase == phase {
			return p
		}
	}
	t.Fatalf("phase %q not in monitor (phases: %+v)", phase, m.Phases)
	return monitorPhase{}
}

func TestParseRunMonitorEmpty(t *testing.T) {
	for _, data := range [][]byte{nil, {}, []byte("\n\n"), []byte("   \n")} {
		if m := parseRunMonitor(data, monNow); m != nil {
			t.Errorf("parseRunMonitor(%q) = %+v, want nil", data, m)
		}
	}
}

func TestParseRunMonitorFullApplyRun(t *testing.T) {
	m := parseRunMonitor(monLines(t, fullApplyRun("run-20260702-150000")...), monNow)
	if m == nil {
		t.Fatal("monitor is nil")
	}
	if m.RunID != "run-20260702-150000" {
		t.Errorf("run id = %q", m.RunID)
	}
	if !m.IsApply {
		t.Error("apply phases present — IsApply must be true")
	}
	if m.State != "completed" || m.Live || m.Stalled {
		t.Errorf("state = %q live=%v stalled=%v, want completed/false/false", m.State, m.Live, m.Stalled)
	}
	if got := monPhase(t, m, "connect").State; got != "completed" {
		t.Errorf("connect state = %q", got)
	}
	mail := monPhase(t, m, "migrate_mail")
	if mail.State != "completed" {
		t.Errorf("migrate_mail state = %q", mail.State)
	}
	if !strings.Contains(mail.Summary, "3 caselle") || !strings.Contains(mail.Summary, "1 fallite") {
		t.Errorf("mail summary = %q, want item and failed counts", mail.Summary)
	}
	if !strings.Contains(mail.Summary, "bad@example.com") {
		t.Errorf("mail summary = %q, must name the failed item", mail.Summary)
	}
	if strings.Contains(mail.Summary, "info@example.com") {
		t.Errorf("mail summary = %q, must NOT list happy items", mail.Summary)
	}
	if got := monPhase(t, m, "verify_mail").Summary; !strings.Contains(got, "Differenze residue: 0") {
		t.Errorf("verify_mail summary = %q", got)
	}
	if got := monPhase(t, m, "migrate_db").Summary; !strings.Contains(got, "dst_wp") {
		t.Errorf("migrate_db summary = %q", got)
	}
	// Phase order is first-appearance order.
	var idxConnect, idxMail int
	for i, p := range m.Phases {
		switch p.Phase {
		case "connect":
			idxConnect = i
		case "migrate_mail":
			idxMail = i
		}
	}
	if idxConnect > idxMail {
		t.Errorf("phase order lost: connect@%d after migrate_mail@%d", idxConnect, idxMail)
	}
	if len(m.Errors) != 0 {
		t.Errorf("errors = %v, want none", m.Errors)
	}
}

func TestParseRunMonitorInProgressIsLive(t *testing.T) {
	evs := []events.Event{
		monEv("run-x", 0, "", events.EventRunStarted, events.LevelInfo, "migration started — mode: APPLY", nil),
		monEv("run-x", time.Minute, events.PhaseMigrateMail, events.EventPhaseStarted, events.LevelInfo, "", nil),
	}
	m := parseRunMonitor(monLines(t, evs...), monNow)
	if m == nil {
		t.Fatal("monitor is nil")
	}
	if m.State != "running" || !m.Live || m.Stalled {
		t.Errorf("state=%q live=%v stalled=%v, want running/true/false", m.State, m.Live, m.Stalled)
	}
	if got := monPhase(t, m, "migrate_mail").State; got != "running" {
		t.Errorf("migrate_mail state = %q, want running", got)
	}
}

func TestParseRunMonitorStalledRunStopsRefreshing(t *testing.T) {
	evs := []events.Event{
		monEv("run-x", 0, "", events.EventRunStarted, events.LevelInfo, "", nil),
		monEv("run-x", time.Minute, events.PhaseCopyFiles, events.EventPhaseStarted, events.LevelInfo, "", nil),
	}
	late := monT0.Add(time.Minute).Add(11 * time.Minute) // > 10m after last event
	m := parseRunMonitor(monLines(t, evs...), late)
	if m == nil {
		t.Fatal("monitor is nil")
	}
	if m.State != "running" || !m.Stalled || m.Live {
		t.Errorf("state=%q stalled=%v live=%v, want running/true/false (dead apply must not refresh forever)", m.State, m.Stalled, m.Live)
	}
}

func TestParseRunMonitorFailedRun(t *testing.T) {
	evs := []events.Event{
		monEv("run-x", 0, "", events.EventRunStarted, events.LevelInfo, "", nil),
		monEv("run-x", 1*time.Second, events.PhaseCopyFiles, events.EventPhaseStarted, events.LevelInfo, "", nil),
		monEv("run-x", 2*time.Second, events.PhaseCopyFiles, events.EventPhaseFailed, events.LevelError, "tar: broken pipe", nil),
		monEv("run-x", 3*time.Second, "", events.EventRunFailed, events.LevelError, "migration failed", nil),
	}
	m := parseRunMonitor(monLines(t, evs...), monNow)
	if m == nil {
		t.Fatal("monitor is nil")
	}
	if m.State != "failed" || m.Live {
		t.Errorf("state = %q live=%v, want failed/false", m.State, m.Live)
	}
	if got := monPhase(t, m, "copy_files").State; got != "failed" {
		t.Errorf("copy_files state = %q", got)
	}
	joined := strings.Join(m.Errors, "\n")
	if !strings.Contains(joined, "tar: broken pipe") || !strings.Contains(joined, "migration failed") {
		t.Errorf("errors = %v, want both error messages", m.Errors)
	}
}

func TestParseRunMonitorShowsOnlyLastRun(t *testing.T) {
	old := fullApplyRun("run-OLD")
	fresh := []events.Event{
		monEv("run-NEW", time.Hour, "", events.EventRunStarted, events.LevelInfo, "", nil),
		monEv("run-NEW", time.Hour+time.Second, events.PhaseConnect, events.EventPhaseStarted, events.LevelInfo, "", nil),
	}
	m := parseRunMonitor(monLines(t, append(old, fresh...)...), monT0.Add(time.Hour+2*time.Minute))
	if m == nil {
		t.Fatal("monitor is nil")
	}
	if m.RunID != "run-NEW" {
		t.Errorf("run id = %q, want the LAST run", m.RunID)
	}
	for _, p := range m.Phases {
		if p.Phase == "migrate_mail" {
			t.Error("phases of the previous run leaked into the monitor")
		}
	}
	if m.IsApply {
		t.Error("the new run has no apply phase yet — IsApply must be false")
	}
}

func TestParseRunMonitorToleratesPartialTrailingLine(t *testing.T) {
	data := monLines(t, fullApplyRun("run-x")...)
	data = append(data, []byte(`{"run_id":"run-x","ts":"2026-07-02T15:01:0`)...) // in-flight write, no newline
	m := parseRunMonitor(data, monNow)
	if m == nil {
		t.Fatal("monitor is nil")
	}
	if m.ParseNote != "" {
		t.Errorf("a partial trailing line is an in-flight write, not a parse problem (note: %q)", m.ParseNote)
	}
	if m.State != "completed" {
		t.Errorf("state = %q", m.State)
	}
}

func TestParseRunMonitorNotesGarbageLines(t *testing.T) {
	lines := monLines(t, fullApplyRun("run-x")...)
	parts := strings.SplitAfterN(string(lines), "\n", 3)
	data := []byte(parts[0] + "NOT-JSON-AT-ALL\n" + parts[1] + parts[2])
	m := parseRunMonitor(data, monNow)
	if m == nil {
		t.Fatal("monitor is nil")
	}
	if !strings.Contains(m.ParseNote, "1 unparsable") {
		t.Errorf("parse note = %q, want a count of skipped garbage lines", m.ParseNote)
	}
}

func TestParseRunMonitorNoRunStartedFallsBack(t *testing.T) {
	evs := []events.Event{ // truncated tail: run_started lost
		monEv("run-tail", 0, events.PhaseMigrateMail, events.EventPhaseStarted, events.LevelInfo, "", nil),
		monEv("run-tail", time.Second, events.PhaseMigrateMail, events.EventPhaseCompleted, events.LevelInfo, "", map[string]any{
			"items": []map[string]any{}, "failed": 0, "unverified": 0}),
	}
	m := parseRunMonitor(monLines(t, evs...), monNow)
	if m == nil {
		t.Fatal("monitor is nil")
	}
	if m.RunID != "run-tail" {
		t.Errorf("run id = %q, want fallback to the last event's run id", m.RunID)
	}
	if got := monPhase(t, m, "migrate_mail").State; got != "completed" {
		t.Errorf("migrate_mail state = %q", got)
	}
}

func TestParseRunMonitorHandlesHugeLines(t *testing.T) {
	items := make([]map[string]any, 0, 3000)
	for i := 0; i < 3000; i++ { // ~120KB single JSON line — must parse whole
		items = append(items, map[string]any{
			"item": fmt.Sprintf("mailbox-%04d@example.com", i), "status": "migrated"})
	}
	evs := []events.Event{
		monEv("run-x", 0, "", events.EventRunStarted, events.LevelInfo, "", nil),
		monEv("run-x", time.Second, events.PhaseMigrateMail, events.EventPhaseCompleted, events.LevelInfo, "", map[string]any{
			"items": items, "failed": 0, "unverified": 0}),
	}
	m := parseRunMonitor(monLines(t, evs...), monNow)
	if m == nil {
		t.Fatal("monitor is nil")
	}
	sum := monPhase(t, m, "migrate_mail").Summary
	if !strings.Contains(sum, "3000 caselle") {
		t.Errorf("summary = %q, want the full item count", sum)
	}
	if len(sum) > 500 {
		t.Errorf("summary is %d bytes — happy items must not be listed", len(sum))
	}
}

func TestParseRunMonitorBoundsFailedItemNames(t *testing.T) {
	items := make([]map[string]any, 0, 30)
	for i := 0; i < 30; i++ {
		items = append(items, map[string]any{
			"item": fmt.Sprintf("dead-%02d@example.com", i), "status": "failed"})
	}
	evs := []events.Event{
		monEv("run-x", 0, "", events.EventRunStarted, events.LevelInfo, "", nil),
		monEv("run-x", time.Second, events.PhaseMigrateMail, events.EventPhaseCompleted, events.LevelInfo, "", map[string]any{
			"items": items, "failed": 30, "unverified": 0}),
	}
	m := parseRunMonitor(monLines(t, evs...), monNow)
	sum := monPhase(t, m, "migrate_mail").Summary
	if !strings.Contains(sum, "dead-00@example.com") {
		t.Errorf("summary = %q, want the first failed names", sum)
	}
	if strings.Contains(sum, "dead-29@example.com") || !strings.Contains(sum, "more") {
		t.Errorf("summary = %q, want a bounded list with an overflow marker", sum)
	}
}

// --- loader ----------------------------------------------------------------

func TestLoadRunMonitorMissingFile(t *testing.T) {
	if m := loadRunMonitor(t.TempDir(), monNow); m != nil {
		t.Errorf("monitor = %+v, want nil without events.jsonl", m)
	}
}

func TestLoadRunMonitorReadsOnlyTheTail(t *testing.T) {
	dir := t.TempDir()
	var sb strings.Builder
	filler := monLines(t, fullApplyRun("run-OLD")...)
	for sb.Len() < monitorTailBytes+len(filler) { // force > 2 MiB
		sb.Write(filler)
	}
	fresh := monLines(t, fullApplyRun("run-FRESH")...)
	sb.Write(fresh)
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	m := loadRunMonitor(dir, monNow)
	if m == nil {
		t.Fatal("monitor is nil")
	}
	if !m.Truncated {
		t.Error("a >2MiB file must be flagged as tail-truncated")
	}
	if m.RunID != "run-FRESH" {
		t.Errorf("run id = %q, want the last run from the tail", m.RunID)
	}
}

// --- dashboard integration --------------------------------------------------

func writeMonitorEvents(t *testing.T, dir string, evs ...events.Event) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), monLines(t, evs...), 0o600); err != nil {
		t.Fatal(err)
	}
}

const refreshMeta = `http-equiv="refresh"`

func TestHandlerMonitorHiddenWithoutEvents(t *testing.T) {
	_, body := getIndex(t, t.TempDir())
	if strings.Contains(body, "Monitor esecuzione") {
		t.Error("monitor panel rendered without events.jsonl")
	}
	if !strings.Contains(body, "--json-events") {
		t.Error("missing hint about --json-events")
	}
}

func TestHandlerMonitorShowsCompletedApplyRun(t *testing.T) {
	dir := t.TempDir()
	writeMonitorEvents(t, dir, fullApplyRun("run-20260702-150000")...)
	_, body := getIndex(t, dir)
	for _, want := range []string{
		"Monitor esecuzione", "run-20260702-150000", "APPLY",
		"migrate_mail", "1 fallite", "bad@example.com", "dst_wp",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	if strings.Contains(body, refreshMeta) {
		t.Error("a completed run must not auto-refresh the page")
	}
}

func TestHandlerMonitorLiveRunRefreshes(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeMonitorEvents(t, dir,
		events.Event{RunID: "run-live", TS: now.Add(-30 * time.Second), Level: events.LevelInfo, Type: events.EventRunStarted, Message: "migration started"},
		events.Event{RunID: "run-live", TS: now.Add(-10 * time.Second), Level: events.LevelInfo, Phase: events.PhaseMigrateMail, Type: events.EventPhaseStarted},
	)
	_, body := getIndex(t, dir)
	if !strings.Contains(body, refreshMeta) {
		t.Error("a live run must keep the page auto-refreshing")
	}
	if !strings.Contains(body, "running") {
		t.Error("live run state missing from the page")
	}
}

func TestHandlerMonitorStalledRunDoesNotRefresh(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeMonitorEvents(t, dir,
		events.Event{RunID: "run-dead", TS: now.Add(-40 * time.Minute), Level: events.LevelInfo, Type: events.EventRunStarted},
		events.Event{RunID: "run-dead", TS: now.Add(-30 * time.Minute), Level: events.LevelInfo, Phase: events.PhaseCopyFiles, Type: events.EventPhaseStarted},
	)
	_, body := getIndex(t, dir)
	if strings.Contains(body, refreshMeta) {
		t.Error("a stalled run must not refresh the page forever")
	}
	if !strings.Contains(body, "stalled") {
		t.Error("stalled state must be spelled out for the operator")
	}
}

func TestHandlerMonitorEscapesHTMLInMessages(t *testing.T) {
	dir := t.TempDir()
	writeMonitorEvents(t, dir,
		monEv("run-x", 0, "", events.EventRunStarted, events.LevelInfo, "", nil),
		monEv("run-x", time.Second, events.PhaseCopyFiles, events.EventPhaseFailed, events.LevelError, "<script>alert(1)</script>", nil),
		monEv("run-x", 2*time.Second, "", events.EventRunFailed, events.LevelError, "failed", nil),
	)
	_, body := getIndex(t, dir)
	if strings.Contains(body, "<script>alert(1)") {
		t.Fatal("event message rendered unescaped — XSS via events.jsonl")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("escaped message not rendered")
	}
}

// --- go-reviewer findings (REQUEST CHANGES round) ---------------------------

// Finding HIGH: a forged/corrupted FUTURE timestamp must not keep the page
// refreshing forever — an event from the future is as untrustworthy as one
// that is too old.
func TestParseRunMonitorFutureTimestampIsStalled(t *testing.T) {
	evs := []events.Event{
		monEv("run-x", 0, "", events.EventRunStarted, events.LevelInfo, "", nil),
		monEv("run-x", 24*time.Hour, events.PhaseCopyFiles, events.EventPhaseStarted, events.LevelInfo, "", nil),
	}
	m := parseRunMonitor(monLines(t, evs...), monT0.Add(time.Minute))
	if m == nil {
		t.Fatal("monitor is nil")
	}
	if !m.Stalled || m.Live {
		t.Errorf("stalled=%v live=%v — a future-dated event stream must stall, not refresh forever", m.Stalled, m.Live)
	}
}

// Finding HIGH: a stalled run must NOT wear the green "completed" status
// class — a dead apply colored like a success is an operational lie.
func TestHandlerMonitorStalledIsNotStyledCompleted(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeMonitorEvents(t, dir,
		events.Event{RunID: "run-dead", TS: now.Add(-40 * time.Minute), Level: events.LevelInfo, Type: events.EventRunStarted},
		events.Event{RunID: "run-dead", TS: now.Add(-30 * time.Minute), Level: events.LevelInfo, Phase: events.PhaseCopyFiles, Type: events.EventPhaseStarted},
	)
	_, body := getIndex(t, dir)
	if !strings.Contains(body, `class="status stalled"`) {
		t.Error("stalled run must carry its own status class")
	}
	if strings.Contains(body, `class="status completed"`) {
		t.Error("stalled run styled as completed — looks like a success")
	}
}

// Finding MEDIUM: distinct phase strings from a corrupted file must not
// produce unbounded table rows on every 2s refresh.
func TestParseRunMonitorBoundsPhaseRows(t *testing.T) {
	evs := []events.Event{monEv("run-x", 0, "", events.EventRunStarted, events.LevelInfo, "", nil)}
	for i := 0; i < 500; i++ {
		evs = append(evs, monEv("run-x", time.Duration(i)*time.Second,
			events.Phase(fmt.Sprintf("garbage-phase-%04d", i)), events.EventPhaseStarted, events.LevelInfo, "", nil))
	}
	m := parseRunMonitor(monLines(t, evs...), monT0.Add(510*time.Second))
	if m == nil {
		t.Fatal("monitor is nil")
	}
	if len(m.Phases) > monitorMaxPhases {
		t.Errorf("phases = %d, want at most %d", len(m.Phases), monitorMaxPhases)
	}
	if !strings.Contains(m.ParseNote, "not shown") {
		t.Errorf("parse note = %q, want an overflow marker for the dropped phases", m.ParseNote)
	}
}

// Round-2 findings: the round-1 fixes themselves regressed two edges.

// (c) Ordinary NTP skew (seconds) on a HEALTHY run must not stall it —
// only a timestamp beyond the tolerance is untrustworthy.
func TestParseRunMonitorSmallFutureSkewStaysLive(t *testing.T) {
	evs := []events.Event{
		monEv("run-x", 0, "", events.EventRunStarted, events.LevelInfo, "", nil),
		monEv("run-x", time.Minute, events.PhaseCopyFiles, events.EventPhaseStarted, events.LevelInfo, "", nil),
	}
	skewedNow := monT0.Add(time.Minute).Add(-3 * time.Second) // writer clock 3s ahead
	m := parseRunMonitor(monLines(t, evs...), skewedNow)
	if m == nil {
		t.Fatal("monitor is nil")
	}
	if m.Stalled || !m.Live {
		t.Errorf("stalled=%v live=%v — 3s of clock skew must not stall a healthy run", m.Stalled, m.Live)
	}
}

// (b) The overflow note counts DISTINCT dropped phases, not their events.
func TestParseRunMonitorOverflowNoteCountsDistinctPhases(t *testing.T) {
	evs := []events.Event{monEv("run-x", 0, "", events.EventRunStarted, events.LevelInfo, "", nil)}
	for i := 0; i < monitorMaxPhases; i++ { // fill the cap
		evs = append(evs, monEv("run-x", time.Duration(i)*time.Second,
			events.Phase(fmt.Sprintf("phase-%02d", i)), events.EventPhaseStarted, events.LevelInfo, "", nil))
	}
	// ONE extra phase with THREE events: the note must say 1, not 3.
	for _, typ := range []events.EventType{events.EventPhaseStarted, events.EventPhaseCompleted, events.EventPhaseFailed} {
		evs = append(evs, monEv("run-x", 100*time.Second, "phase-overflow", typ, events.LevelInfo, "", nil))
	}
	m := parseRunMonitor(monLines(t, evs...), monT0.Add(101*time.Second))
	if m == nil {
		t.Fatal("monitor is nil")
	}
	if !strings.Contains(m.ParseNote, "1 additional phase row(s) not shown") {
		t.Errorf("parse note = %q, want a DISTINCT-phase count of 1", m.ParseNote)
	}
}
