package webui

// Job journal (PR 69 — In-Flight Job Rehydration). A single per-working-dir
// artifact — job.json — that persists the IDENTITY and coarse phase of the
// in-flight/last exec so a refresh, a sleep or a process restart never loses
// control of a running migration:
//
//   - completed-state rehydration already exists (readArtifactFacts, dogfooding
//     #3) and item-level progress for migrate_content already exists
//     (loadRunMonitor over events.jsonl). This file adds ONLY the missing piece:
//     the job identity, so the opaque 409 becomes a readable state and a killed
//     ui reconstructs "migrate_content interrupted".
//
// Security (roadmap §12): the journal records identity + progress ONLY — never
// credentials, never the resolved argv. The struct below has no field that can
// hold either, so the anti-leak guarantee holds by construction.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/version"
)

// jobJournalState is the lifecycle of the journalled exec.
type jobJournalState string

const (
	jobStateRunning     jobJournalState = "running"
	jobStateCompleted   jobJournalState = "completed"
	jobStateFailed      jobJournalState = "failed"
	jobStateInterrupted jobJournalState = "interrupted"
)

// jobJournal is the on-disk shape of job.json. Deliberately minimal: identity
// (session_id, action), timing (started_at/updated_at), coarse phase and the
// terminal error. Item-level detail is NOT stored here — for migrate_content it
// is reconstructed at the view layer from the existing run monitor
// (loadRunMonitor / events.jsonl), so no writer is touched and no fake
// precision is invented for phases that have only phase-level truth.
type jobJournal struct {
	SessionID   string          `json:"session_id"`
	Action      string          `json:"action"`
	StartedAt   time.Time       `json:"started_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	State       jobJournalState `json:"state"`
	Phase       string          `json:"phase"`
	Error       string          `json:"error,omitempty"`
	ToolVersion string          `json:"tool_version"`
}

// jobJournalName is the fixed filename in the working dir (next to the other
// artifacts, same dir as host.yaml — single-account, single journal).
const jobJournalName = "job.json"

func jobJournalPath(dir string) string { return filepath.Join(dir, jobJournalName) }

// writeJobJournal persists j atomically (write-temp + fsync + chmod 0600 +
// rename) — the SAME posture as workbench store.writeSession, so a crash never
// leaves a half-written or world-readable journal.
func writeJobJournal(dir string, j jobJournal) error {
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal job journal: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, jobJournalName+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp job journal: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp job journal: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync temp job journal: %w", err)
	}
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp job journal: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp job journal: %w", err)
	}
	if err := os.Rename(tmpName, jobJournalPath(dir)); err != nil {
		cleanup()
		return fmt.Errorf("rename job journal: %w", err)
	}
	return nil
}

// readJobJournal reads job.json fail-soft: a missing or unreadable/corrupt file
// returns (nil, false), never an error — same posture as readArtifactFacts.
func readJobJournal(dir string) (*jobJournal, bool) {
	b, err := os.ReadFile(jobJournalPath(dir)) // #nosec G304 -- fixed name in the operator-chosen dir
	if err != nil {
		return nil, false
	}
	var j jobJournal
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, false
	}
	if j.State == "" {
		return nil, false
	}
	return &j, true
}

// startJobJournal writes the running record BEFORE the subprocess is launched.
// Best-effort: a journal write failure must never block the actual exec (the
// exec is the operator's real intent; the journal is observability only).
func startJobJournal(dir, sessionID, action string, startedAt time.Time) {
	_ = writeJobJournal(dir, jobJournal{
		SessionID:   sessionID,
		Action:      action,
		StartedAt:   startedAt,
		UpdatedAt:   startedAt,
		State:       jobStateRunning,
		Phase:       action,
		ToolVersion: version.String(),
	})
}

// finishJobJournal closes the journal to completed|failed. Called from the exec
// defer so it runs on every return path.
func finishJobJournal(dir, sessionID, action, phase string, startedAt, now time.Time, execErr error) {
	j := jobJournal{
		SessionID:   sessionID,
		Action:      action,
		StartedAt:   startedAt,
		UpdatedAt:   now,
		State:       jobStateCompleted,
		Phase:       phase,
		ToolVersion: version.String(),
	}
	if execErr != nil {
		j.State = jobStateFailed
		j.Error = execErr.Error()
	}
	_ = writeJobJournal(dir, j)
}

// recoverJobJournal runs once at ui startup (New): the in-memory slot is free by
// construction, so any journal still marked running is the residue of a ui that
// died mid-exec — its subprocess died with it. Persist that as interrupted.
func recoverJobJournal(dir string, now time.Time) {
	jj, ok := readJobJournal(dir)
	if !ok || jj.State != jobStateRunning {
		return
	}
	jj.State = jobStateInterrupted
	jj.UpdatedAt = now
	_ = writeJobJournal(dir, *jj)
}

// reconcileJobJournal presents the journal against the LIVE slot: a running
// record with a free slot is an exec that died without persisting a terminal
// state (belt-and-suspenders for the startup recovery). Read-only — it never
// writes during a GET (this layer must stay side-effect free). Returns nil when
// there is no journal.
func reconcileJobJournal(dir string, slotBusy bool) *jobJournal {
	jj, ok := readJobJournal(dir)
	if !ok {
		return nil
	}
	if jj.State == jobStateRunning && !slotBusy {
		jj.State = jobStateInterrupted
	}
	return jj
}

// formatRunningMessage renders the shared "«action» già in corso …" copy for a
// running slot holder. The phase is appended only when it adds information over
// the action name itself (the journal defaults Phase to the action, so a bare
// exec shows no redundant "(fase …)").
func formatRunningMessage(action string, startedAt time.Time, phase string) string {
	msg := fmt.Sprintf("«%s» già in corso dalle %s UTC",
		action, startedAt.UTC().Format("15:04:05"))
	if phase != "" && phase != action {
		msg += fmt.Sprintf(" (fase %s)", phase)
	}
	return msg + " — attendi il completamento o riapri la pagina per seguirne l'avanzamento."
}

// journalMatchesReservedHolder reports whether a running journal describes the
// SAME run as the live reservedHolder — so it can be trusted to enrich the
// holder's identity with a phase rather than being a stale record of a previous
// action. Coherence is action + started-at: the started-at is the nanosecond
// reserve timestamp, unique per reserve event on the EXCLUSIVE slot (the next
// holder can only reserve after the previous one released), and it is persisted
// with full precision (RFC 3339 nano round-trip), so time.Time.Equal is exact —
// no formatted-string compare, no arbitrary tolerance. A session ID would not
// improve discrimination in this single-account / single-slot model, so it is
// deliberately not part of the reservation identity.
func journalMatchesReservedHolder(jj *jobJournal, action string, startedAt time.Time) bool {
	return jj != nil && jj.Action == action && jj.StartedAt.Equal(startedAt)
}

// busyMessage renders the readable state that replaces the opaque 409 for every
// caller of the shared single-writer slot (/run, /accept, /exec, orchestrator).
// Precedence (roadmap §7, finding F1):
//
//  1. A LIVE reservedHolder is the truth during a live process — it is published
//     atomically with the busy flag under jobManager.mu. A running journal is
//     used only when it is the SAME run (richer phase); a stale or mismatched
//     journal never overrides the live identity with a different action.
//  2. No live holder: a running journal covers refresh/recovery and processes
//     where the journal is the only surviving record.
//  3. The read-only analysis pipeline holds the slot with its state in memory,
//     not on disk — fall back to that live snapshot.
//  4. Only a genuine race leaves the generic message.
func busyMessage(dir string, j *jobManager) string {
	// Collect the three sources ONCE, holding no lock across I/O: reservedHolder
	// and snapshot each take jobManager.mu only long enough to copy their fields,
	// and readJobJournal is a plain os.ReadFile with no lock held at all.
	var (
		holderAction string
		holderStart  time.Time
		haveHolder   bool
	)
	if j != nil {
		holderAction, holderStart, haveHolder = j.reservedHolder()
	}
	jj, haveJournal := readJobJournal(dir)
	journalRunning := haveJournal && jj.State == jobStateRunning

	// 1. Live holder wins. An empty action is treated as no nameable holder
	//    (fail-closed): fall through to the journal/analysis/generic layers.
	if haveHolder && holderAction != "" {
		if journalRunning && journalMatchesReservedHolder(jj, holderAction, holderStart) {
			return formatRunningMessage(jj.Action, jj.StartedAt, jj.Phase)
		}
		return formatRunningMessage(holderAction, holderStart, "")
	}

	// 2. No live holder: a running journal names refresh/recovery.
	if journalRunning {
		action := jj.Action
		if action == "" {
			action = "un'operazione"
		}
		return formatRunningMessage(action, jj.StartedAt, jj.Phase)
	}

	// 3. Analysis pipeline (state in memory, not on disk).
	if j != nil {
		if s := j.snapshot(); s.State == "running" {
			started := ""
			if s.StartedAt != "" {
				started = " dalle " + s.StartedAt
			}
			return fmt.Sprintf("Un'analisi è in corso%s — attendi il completamento prima di lanciare un'altra operazione.", started)
		}
	}
	return "Un'operazione è già in corso — attendi il completamento prima di lanciarne un'altra."
}

// writeBusy409 sends the readable busy state as a 409 Conflict.
func writeBusy409(w http.ResponseWriter, dir string, j *jobManager) {
	http.Error(w, busyMessage(dir, j), http.StatusConflict)
}
