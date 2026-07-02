package webui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/events"
)

// Apply/run monitor (UI phase 3). The dashboard READS events.jsonl —
// written by a terminal `--apply --json-events` (or dry-run) — and shows
// the last run's phases live. Monitor-only by design: the UI never
// launches an apply and never blocks one; this file is pure consumption.
// Design: docs/dev/UI3_APPLY_MONITOR_DESIGN.md.

// monitorStallCutoff: a run with no terminal event whose last event is
// older than this is shown as stalled and stops the page auto-refresh
// (a killed apply leaves no run_completed/run_failed behind).
const monitorStallCutoff = 10 * time.Minute

// monitorTailBytes bounds how much of events.jsonl is read per page
// build: the file is append-only across runs and can grow unbounded.
const monitorTailBytes = 2 << 20 // 2 MiB

// monitorMaxItemNames bounds the failed/unverified item names listed in
// a phase summary; monitorMaxErrors bounds the error list.
const (
	monitorMaxItemNames = 10
	monitorMaxErrors    = 8
)

// applyPhaseSet mirrors the run-report collector's criterion in cmd:
// a run is an APPLY run iff one of these phases appears.
var applyPhaseSet = map[events.Phase]bool{
	events.PhaseCreateDomains: true,
	events.PhaseMigrateMail:   true,
	events.PhaseVerifyMail:    true,
	events.PhaseCopyFiles:     true,
	events.PhaseVerifyFiles:   true,
	events.PhaseMigrateDB:     true,
	events.PhaseVerifyDB:      true,
}

type monitorPhase struct {
	Phase   string
	State   string // running | completed | failed | skipped
	Summary string
}

type runMonitor struct {
	RunID       string
	IsApply     bool
	State       string // running | completed | failed
	Stalled     bool
	Live        bool // running && !stalled — drives the meta-refresh
	StartedAt   string
	LastEventAt string
	Phases      []monitorPhase
	Errors      []string
	ParseNote   string
	Truncated   bool // set by the loader when only the file tail was read
}

// monitorEvent mirrors events.Event with Data kept raw so each phase
// payload can be decoded by shape.
type monitorEvent struct {
	RunID   string          `json:"run_id"`
	TS      time.Time       `json:"ts"`
	Level   string          `json:"level"`
	Phase   string          `json:"phase"`
	Type    string          `json:"event"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// Local mirrors of the 7C apply payloads (the originals are unexported
// in internal/migrate; apply_events_test.go pins the emission side).
type monitorApplyItem struct {
	Item   string `json:"item"`
	Status string `json:"status"`
	Note   string `json:"note"`
}

type monitorMailData struct {
	Items      []monitorApplyItem `json:"items"`
	Failed     int                `json:"failed"`
	Unverified int                `json:"unverified"`
}

type monitorDomainData struct {
	FailedDomains  []string `json:"failed_domains"`
	BlockedDomains []string `json:"blocked_domains"`
}

type monitorDBData struct {
	Migrated           []string `json:"migrated"`
	Failed             int      `json:"failed"`
	ConfigNotRewritten int      `json:"config_not_rewritten"`
	ConfigUnmigrated   int      `json:"config_unmigrated"`
}

type monitorCountData struct {
	Failed    int `json:"failed"`
	Divergent int `json:"divergent"`
}

// loadRunMonitor reads at most the last monitorTailBytes of
// <dir>/events.jsonl and parses the last run. Missing file → nil
// (panel hidden); read errors degrade to nil as well — the monitor is
// informational and must never break the dashboard.
func loadRunMonitor(dir string, now time.Time) *runMonitor {
	path := filepath.Join(dir, "events.jsonl")
	f, err := os.Open(path) // #nosec G304 -- fixed name in the operator-chosen dir
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	truncated := false
	var data []byte
	if fi.Size() > monitorTailBytes {
		truncated = true
		if _, err := f.Seek(fi.Size()-monitorTailBytes, io.SeekStart); err != nil {
			return nil
		}
		buf := make([]byte, monitorTailBytes)
		n, rerr := io.ReadFull(f, buf)
		if n == 0 {
			return nil
		}
		data = buf[:n]
		_ = rerr // partial read of a shrinking file: parse what we got
		// Drop the first, almost certainly partial line of the tail.
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	} else {
		data, err = io.ReadAll(f)
		if err != nil {
			return nil
		}
	}
	m := parseRunMonitor(data, now)
	if m != nil {
		m.Truncated = truncated
	}
	return m
}

// parseRunMonitor extracts the LAST run from JSONL event data. Garbage
// lines are skipped and counted; a partial trailing line is an in-flight
// write and is ignored silently. Returns nil when no event parses.
func parseRunMonitor(data []byte, now time.Time) *runMonitor {
	lines := bytes.Split(data, []byte("\n"))
	// A file that does not end in \n has an in-flight last line: never
	// count it as garbage. (bytes.Split leaves a trailing "" when the
	// data DOES end in \n, which the empty-line skip already handles.)
	partialTail := len(data) > 0 && data[len(data)-1] != '\n'

	var evs []monitorEvent
	garbage := 0
	for i, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var ev monitorEvent
		if err := json.Unmarshal(line, &ev); err != nil || ev.Type == "" {
			if partialTail && i == len(lines)-1 {
				continue
			}
			garbage++
			continue
		}
		evs = append(evs, ev)
	}
	if len(evs) == 0 {
		return nil
	}

	// The monitored run: everything from the LAST run_started onward.
	// Fallback (truncated tail lost the run_started): the last event's
	// run id, over the whole slice.
	start := -1
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == string(events.EventRunStarted) {
			start = i
			break
		}
	}
	var run []monitorEvent
	if start >= 0 {
		run = evs[start:]
	} else {
		lastID := evs[len(evs)-1].RunID
		for _, ev := range evs {
			if ev.RunID == lastID {
				run = append(run, ev)
			}
		}
	}

	m := &runMonitor{RunID: run[0].RunID, State: "running"}
	if garbage > 0 {
		m.ParseNote = fmt.Sprintf("%d unparsable line(s) skipped", garbage)
	}
	m.StartedAt = run[0].TS.Format("15:04:05")
	last := run[len(run)-1]
	m.LastEventAt = last.TS.Format("15:04:05")

	phaseIdx := map[string]int{}
	for _, ev := range run {
		if ev.Level == string(events.LevelError) && ev.Message != "" && len(m.Errors) < monitorMaxErrors {
			m.Errors = append(m.Errors, ev.Message)
		}
		if ev.Phase != "" && applyPhaseSet[events.Phase(ev.Phase)] {
			m.IsApply = true
		}
		switch ev.Type {
		case string(events.EventRunCompleted):
			m.State = "completed"
		case string(events.EventRunFailed):
			m.State = "failed"
		case string(events.EventPhaseStarted), string(events.EventPhaseCompleted),
			string(events.EventPhaseFailed), string(events.EventPhaseSkipped):
			if ev.Phase == "" {
				continue
			}
			i, seen := phaseIdx[ev.Phase]
			if !seen {
				m.Phases = append(m.Phases, monitorPhase{Phase: ev.Phase, State: "running"})
				i = len(m.Phases) - 1
				phaseIdx[ev.Phase] = i
			}
			switch ev.Type {
			case string(events.EventPhaseStarted):
				m.Phases[i].State = "running"
			case string(events.EventPhaseCompleted):
				m.Phases[i].State = "completed"
				m.Phases[i].Summary = phaseSummary(ev)
			case string(events.EventPhaseFailed):
				m.Phases[i].State = "failed"
				m.Phases[i].Summary = ev.Message
			case string(events.EventPhaseSkipped):
				m.Phases[i].State = "skipped"
			}
		}
	}

	if m.State == "running" && now.Sub(last.TS) > monitorStallCutoff {
		m.Stalled = true
	}
	m.Live = m.State == "running" && !m.Stalled
	return m
}

// phaseSummary renders the compact, bounded description of a completed
// phase from its 7C Data payload. Unknown or absent payloads yield "".
func phaseSummary(ev monitorEvent) string {
	if len(ev.Data) == 0 {
		return ""
	}
	switch events.Phase(ev.Phase) {
	case events.PhaseCreateDomains:
		var d monitorDomainData
		if json.Unmarshal(ev.Data, &d) != nil {
			return ""
		}
		if len(d.FailedDomains) == 0 && len(d.BlockedDomains) == 0 {
			return "no failures"
		}
		var parts []string
		if len(d.FailedDomains) > 0 {
			parts = append(parts, "failed: "+boundedList(d.FailedDomains))
		}
		if len(d.BlockedDomains) > 0 {
			parts = append(parts, "blocked: "+boundedList(d.BlockedDomains))
		}
		return strings.Join(parts, " — ")

	case events.PhaseMigrateMail:
		var d monitorMailData
		if json.Unmarshal(ev.Data, &d) != nil {
			return ""
		}
		s := fmt.Sprintf("%d item(s) — %d failed, %d unverified", len(d.Items), d.Failed, d.Unverified)
		var bad []string
		for _, it := range d.Items {
			if it.Status == "failed" || it.Status == "unverified" {
				bad = append(bad, it.Item)
			}
		}
		if len(bad) > 0 {
			s += ": " + boundedList(bad)
		}
		return s

	case events.PhaseMigrateDB:
		var d monitorDBData
		if json.Unmarshal(ev.Data, &d) != nil {
			return ""
		}
		s := "migrated: " + boundedList(d.Migrated)
		if len(d.Migrated) == 0 {
			s = "nothing migrated"
		}
		if d.Failed > 0 {
			s += fmt.Sprintf(" — %d failed", d.Failed)
		}
		if d.ConfigNotRewritten > 0 {
			s += fmt.Sprintf(" — %d config not rewritten", d.ConfigNotRewritten)
		}
		if d.ConfigUnmigrated > 0 {
			s += fmt.Sprintf(" — %d config unmigrated", d.ConfigUnmigrated)
		}
		return s

	case events.PhaseCopyFiles:
		var d monitorCountData
		if json.Unmarshal(ev.Data, &d) != nil {
			return ""
		}
		return fmt.Sprintf("failed: %d", d.Failed)

	case events.PhaseVerifyMail, events.PhaseVerifyFiles, events.PhaseVerifyDB:
		var d monitorCountData
		if json.Unmarshal(ev.Data, &d) != nil {
			return ""
		}
		return fmt.Sprintf("divergent: %d", d.Divergent)
	}
	return ""
}

// boundedList joins at most monitorMaxItemNames entries, with an
// explicit overflow marker — a phase with hundreds of failures must not
// balloon the page.
func boundedList(items []string) string {
	if len(items) <= monitorMaxItemNames {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:monitorMaxItemNames], ", ") +
		fmt.Sprintf(" (+%d more)", len(items)-monitorMaxItemNames)
}
