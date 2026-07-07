package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Server-Sent Events progress stream for the operator cockpit. It is a thin
// PUSH wrapper over the SAME read model the cockpit already renders server-side
// (loadRunMonitor + the job journal): no new source of truth, no writer touched,
// no invented numbers. It lets the cockpit show a live-advancing bar/phase/log
// without a full-page meta-refresh, degrading to that meta-refresh when the
// browser has no EventSource.

// sseTick is how often the stream re-samples the read model. ~1s gives a
// near-real-time feel without a filesystem watch and mirrors the cheap
// per-refresh cost the meta-refresh already paid.
const sseTick = time.Second

// ssePhase is one migration phase in a progress snapshot.
type ssePhase struct {
	Label string `json:"label"`
	State string `json:"state"`
}

// sseSnapshot is the JSON pushed to the cockpit on each change. pct is derived
// from completed/total phases — never faked (consistent with the engine's
// refusal to print fake percentages, internal/logx/progress.go).
type sseSnapshot struct {
	State        string     `json:"state"`
	Live         bool       `json:"live"`
	Done         bool       `json:"done"`
	Pct          int        `json:"pct"`
	CurrentPhase string     `json:"currentPhase,omitempty"`
	Outcome      string     `json:"outcome,omitempty"`
	Phases       []ssePhase `json:"phases"`
	Log          []string   `json:"log"`
}

// buildSSESnapshot projects the job journal + run monitor into a progress
// snapshot. Done is true when there is no live job (terminal state, stall, or no
// journal at all) so the stream can close and the client stops waiting.
func buildSSESnapshot(dir string, now time.Time) sseSnapshot {
	snap := sseSnapshot{Phases: []ssePhase{}, Log: []string{}}
	jj, hasJob := readJobJournal(dir)
	run := loadRunMonitor(dir, now)

	if hasJob {
		snap.State = string(jj.State)
		snap.CurrentPhase = jj.Phase
		snap.Outcome = jj.Outcome
		snap.Done = jj.State != jobStateRunning
	} else {
		snap.Done = true
	}

	if run != nil {
		snap.Live = run.Live
		total := len(run.Phases)
		completed := 0
		for _, p := range run.Phases {
			snap.Phases = append(snap.Phases, ssePhase{Label: p.Phase, State: p.State})
			switch p.State {
			case "completed", "failed", "skipped":
				completed++
			}
		}
		if total > 0 {
			snap.Pct = completed * 100 / total
		}
		snap.Log = append(snap.Log, run.Errors...)
		// A stalled run (no terminal event, last event too old) is not live:
		// close the stream so the client does not wait on a dead job.
		if !run.Live && snap.State == string(jobStateRunning) {
			snap.Done = true
		}
	}
	if hasJob && jj.State == jobStateCompleted {
		snap.Pct = 100
	}
	if hasJob && jj.State == jobStateFailed && jj.Error != "" {
		snap.Log = append(snap.Log, "Ultimo errore del job: "+jj.Error)
	}
	return snap
}

// handleSessionEvents streams migration progress as Server-Sent Events. GET only
// (no mutation → no CSRF); the loopback + Host/Origin gate in route() already
// applies to every request. It re-samples the read model every sseTick and
// pushes a delta ONLY when it changes, closing on client disconnect or when the
// run is terminal/stalled.
func (ps *platformServer) handleSessionEvents(w http.ResponseWriter, r *http.Request, id string) {
	if _, err := ps.store.Get(id); err != nil {
		http.Error(w, "migrazione non trovata", http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming non supportato", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ctx := r.Context()
	ticker := time.NewTicker(sseTick)
	defer ticker.Stop()

	last := ""
	push := func() bool {
		snap := buildSSESnapshot(ps.dir, time.Now().UTC())
		b, err := json.Marshal(snap)
		if err != nil {
			return true // cannot encode → stop the stream
		}
		if s := string(b); s != last {
			fmt.Fprintf(w, "data: %s\n\n", s)
			flusher.Flush()
			last = s
		}
		return snap.Done
	}

	// Initial push so the client paints immediately; if already terminal, close.
	if push() {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if push() {
				return
			}
		}
	}
}
