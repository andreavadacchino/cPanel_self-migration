# PR 7C — apply evidence (micro-design)

Goal: make an `--apply` run leave verifiable, machine-readable evidence of
what it did, so `inventory checklist` can honestly upgrade migration
evidence from `run_level` to `per_item`.

Three layers, three small changes:

1. **Events** — emit the seven apply-phase events that have been defined
   since PR 1 but never emitted (`events/event.go:31-37`):
   `create_domains`, `migrate_mail`, `verify_mail`, `copy_files`,
   `verify_files`, `migrate_db`, `verify_db`.
2. **Report** — populate `phases_completed` and `artifacts` in
   `report.json` (`buildRunReport`, `cmd/.../main.go`), today always empty.
3. **Checklist** — `computeEvidence` upgrades a section to
   `per_item` when the (successful) apply report proves BOTH the migrate
   and the verify phase of that section's flow completed.

## Perimeter (hard constraint)

The migrate package is off-limits except:
- `apply*.go` — where the events are emitted;
- ONE line in `runner.go`: `opts.RunID = runID` after run-ID resolution,
  so `runApply` (which receives `opts`) emits with the RESOLVED run ID
  instead of the possibly-empty user flag. No signature changes, so the
  existing `runApply(...)` test call sites stay untouched.

`verify.go` (mail verify internals), `data.go`, and everything else in
the package stay byte-identical to main.

## Where events are emitted

All from `runApply` in `apply.go`, around the existing flow calls —
never inside `runner.go`:

| Phase | started before | completed after | Data on completed |
|-------|----------------|-----------------|-------------------|
| `create_domains` | `applyDomains` | idem | `{failed_domains: [names], blocked_domains: [names]}` (sorted, from `pd.FailedDomains`/`pd.BlockedDomains`) |
| `migrate_mail` | `applyMailboxes` | idem | `{items: [{item, status, note?}], failed, unverified}` |
| `verify_mail` | `verify` | idem | `{divergent: n}` |
| `copy_files` | `applyWebFiles` | idem | `{failed: n}` |
| `verify_files` | `verifyWebFiles` | idem | `{divergent: n}` |
| `migrate_db` | `applyDBs` | idem | `{migrated: [dest db names], failed, config_not_rewritten, config_unmigrated}` |
| `verify_db` | `verifyDBs` | idem | `{divergent: n}` |

On a flow-call error: `phase_failed` (level error, message = error), then
the error propagates exactly as today. Host refs are rebuilt from `cfg`
(`hostRef{User: cfg.Src.SSHUser, IP: cfg.Src.IP, Port: cfg.Src.Port}`),
same data `Run` itself uses.

**Semantics: `phase_completed` = "the phase ran to completion", NOT "every
item succeeded".** Per-item failures live in the Data payload, in the
tally, and in the final process error — which drives `exit_status`. The
checklist reads `phases_completed` only on `exit_status == "success"`
(pre-existing gate), so a lossy run can never upgrade evidence.

## Per-item Data: what is available, honestly

The real per-item loops collapse outcomes to integer counts (audited in
this design's investigation). Per-item names in this PR come only from
places that need no signature changes and no excluded files:

- **domains**: failed/blocked names persist on `pd` — full detail.
- **mail**: `mailboxApplyResult` (internal to `apply_mailboxes.go`) grows
  an `items []applyItem` slice recorded at the existing counter sites
  (statuses: `migrated`, `unchanged`, `skipped`, `failed`, `unverified`).
  No signature change.
- **db**: migrated destination DB names = the keys of the `destCreds`
  map `applyDBs` already returns.
- **web copy/verify + mail/db verify**: counts only. Web per-item would
  require changing `applyWebFiles`/`verifyWebFiles` signatures (8 test
  call sites) and mail-verify per-item lives in `verify.go` (excluded).
  Deferred; the per-item lines are still in `migration_report.log`.

`applyItem` (defined in `apply.go`):
```go
type applyItem struct {
    Item   string `json:"item"`
    Status string `json:"status"`
    Note   string `json:"note,omitempty"`
}
```

## report.json (main.go)

- The emitter in `main()` becomes a tee: it ALWAYS records
  `phase_completed` phases into an ordered, deduplicated collector
  (mutex-guarded — the race CI job is authoritative), and forwards to the
  JSONL writer only when `--json-events` is set. `--report-json` alone
  now gets real `phases_completed`.
- `buildRunReport` gains `phases []events.Phase, artifacts map[string]string`
  parameters and writes them through.
- Artifacts are recorded by EXISTENCE CHECK after the run, never on trust:
  - `migration_report_log` → `<outDir>/logs/migration_report.log`
    (apply runs only);
  - `events_jsonl` → `<outDir>/events.jsonl` (when `--json-events`).

## Checklist upgrade (accountinventory)

`MigrationReportInfo` gains `PhasesCompleted []string
`json:"phases_completed"`` (mirrors `RunReport`; absent field in older
reports decodes to nil = no upgrade, full backward compatibility).

`computeEvidence` upgrade table — a section reaches `per_item` only when
its migrate AND verify phases BOTH completed (domains have no verify
phase; creation is itself per-item and failures gate the exit status):

| Section | per_item requires |
|---------|-------------------|
| mailboxes | `migrate_mail` + `verify_mail` |
| web_files | `copy_files` + `verify_files` |
| databases | `migrate_db` + `verify_db` |
| domains | `create_domains` |

Everything else about the evidence gate is UNCHANGED: report must be an
apply run, `exit_status == "success"`, scope selects the section. The
per-item claim is honest because the verify phases are per-item
integrity passes whose failures make the run non-success — so
"successful run + both phases completed" proves each item was
individually processed and verified.

## Testing

- `internal/migrate`: `runApply`-level tests with an in-test collector
  Emitter, modeled on `TestRunApplyEmptyHashFailsEvenWhenEmptyMailboxVerifiesClean`
  (these pass on macOS too — no GNU tooling in the exercised path).
  Assert: phase sequence, run-ID propagation from `opts.RunID`, per-item
  mail Data (unverified mailbox), domain blocked/failed names in Data,
  events still emitted (completed) when the run ends with a process
  error from the tally.
- Full-package regression on Linux via Docker (`golang:1.25`), since the
  package is baseline-green there and macOS failures are environmental.
- `cmd`: unit tests for the phase collector and `buildRunReport`.
- `accountinventory`: `computeEvidence` upgrade/partial/legacy-report
  tests; goldens untouched (no schema change without phases present).

The `opts.RunID = runID` runner line has no isolated test: no test can
reach `runApply` through `Run` without a full apply harness. It is
one assignment, visually reviewable; the event tests pin the consumer
side (`runApply` uses `opts.RunID`).
