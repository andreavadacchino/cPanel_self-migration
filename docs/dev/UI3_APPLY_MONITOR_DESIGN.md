# UI phase 3 — apply/run monitor (micro-design)

Requirement (operator): while a `--apply --json-events` runs FROM THE
TERMINAL, the dashboard shows what it is doing — phase by phase, with
the per-item evidence PR 7C already emits — without the operator
tailing a JSONL by hand.

## Monitor-only, and why not SSE

The UI **never launches** an apply: `--apply` stays terminal-only (the
trust model of phases 1-2 is unchanged — the UI process never mutates
servers; here it does not even launch the CLI, it only READS a file).

The handoff suggested SSE, but SSE requires JavaScript (`EventSource`)
and the UI is deliberately zero-JS. The existing meta-refresh pattern
(2s full reload while something is running) gives the same operator
experience for a file that grows a few lines per minute, with a far
smaller surface: no streaming endpoint, no connection lifecycle, no JS.
The monitor is therefore: re-read `events.jsonl` on every page build,
render the state, and keep the page auto-refreshing while the run looks
live.

## Data source facts (from the events package)

- `events.jsonl` exists only when the run was started with
  `--json-events`; path `<dir>/events.jsonl`. The dashboard says so
  when the file is absent (hint line, no panel).
- The writer opens with `O_APPEND`: **runs accumulate** — the monitor
  segments by the LAST `run_started` line (fallback: the run_id of the
  last valid line, for hand-truncated files).
- Every event is one `json.Encoder.Encode` = one write syscall — lines
  are effectively atomic, but the parser still tolerates a partial or
  garbled trailing line (in-flight write) and skips garbage lines
  elsewhere (counted, surfaced as a parse note — never fatal).
- An APPLY run is recognized exactly as `main.go`'s `phaseCollector`
  does: at least one event whose phase is one of the seven apply phases
  (`create_domains`, `migrate_mail`, `verify_mail`, `copy_files`,
  `verify_files`, `migrate_db`, `verify_db`). Data payloads (per-item
  mail results, db names, divergent counts) ride on `phase_completed`.
- Secrets: `events.Writer` redacts Data BEFORE writing, and
  `html/template` escapes everything at render time — the monitor adds
  no new exposure.

## Parser: pure, tested offline

```go
// internal/webui/monitor.go
func parseRunMonitor(data []byte, now time.Time) *runMonitor
```

`runMonitor` (precomputed for the template — no FuncMap in this UI):
run id, `IsApply`, state `running|completed|failed`, `Stalled` bool,
started/last-event timestamps, ordered phase rows (`Phase, State,
Summary`), error messages (bounded), parse-note. Phase rows keep first-
appearance order; state per phase = latest of
started/completed/failed/skipped. Summaries are compact strings built
from the 7C payloads ("8 items — 1 failed, 2 unverified", "divergent:
0", "migrated: db1, db2"); failed/unverified item names are listed,
bounded, with a "+N more" overflow — never the full happy list.

Liveness (drives the meta-refresh): `running` = no terminal
`run_completed`/`run_failed` for the monitored run. A running state
whose last event is older than 10 minutes becomes `Stalled` — the
refresh stops and the panel says the process may have been interrupted
(a killed apply leaves no terminal event; refreshing forever on a dead
file would lie). `now` is a parameter, so the cutoff is unit-testable.

Reading is bounded: the loader reads at most the final 2 MiB of the
file (seek from the end, drop the first partial line) so a long-lived
events.jsonl cannot balloon page builds; the panel notes when the tail
was truncated. Scanner line buffer is raised to 1 MiB (a mail phase
with many mailboxes produces long Data lines).

## Dashboard integration

- `page` gains `Monitor *runMonitor` (+ `EventsHint` when the file is
  absent); `buildPage` populates it via the checklist-read idiom
  (missing file → nil, read error → note).
- Template: panel right after the job-status block (the "live" region),
  reusing the existing `.status` classes; phases as a plain table;
  errors as a list. Meta-refresh condition becomes
  `{{if or .JobRunning .MonitorLive}}` where `MonitorLive` is
  precomputed (`running && !stalled`).
- No new route, no POST, no CSRF surface: the panel renders inside `/`
  and inherits requestIsLocal + hardening headers. The mirror structs
  for the 7C payloads live in webui (the originals are unexported in
  `internal/migrate`); `apply_events_test.go` pins the emission side,
  the webui tests pin the consumption side.

## Interaction with the UI job

None shared: the jobManager tracks only the UI's own subprocess
pipeline. A terminal apply and a UI analysis run write disjoint files
(known pre-existing caveat: the checklist step may read a mid-write
report.json — unchanged by this PR). The monitor treats events.jsonl
as read-only truth and never blocks or gates anything.

## Testing (TDD)

- Parser (offline, table-driven; fixtures built by marshaling REAL
  `events.Event` values): full apply run → completed, phases ordered,
  summaries exact; in-progress → running + live; `phase_failed` +
  `run_failed` → failed with messages; two runs appended → only the
  last; partial trailing line ignored; garbage mid-file → parse note;
  stalled cutoff honored (`now` injected); missing `run_started` →
  falls back to last run_id; >64KiB line parsed.
- Handler (existing `getIndex` harness): no events.jsonl → hint, no
  panel; apply fixture → run id/phases/counts in the HTML; meta-refresh
  present when live, absent when terminal AND when stalled; a message
  containing HTML renders escaped.

## Out of scope

Launching `--apply` from the UI (explicitly rejected), SSE/JS, reading
`logs/migration_report.log`, cross-process locks, `internal/migrate/*`
(untouched).
