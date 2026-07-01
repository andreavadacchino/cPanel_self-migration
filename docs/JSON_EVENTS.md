# JSON Events & Report — PR 1

## New CLI Flags

### `--run-id <string>`
Optional run identifier. If omitted, auto-generated as `run-YYYYMMDD-HHMMSS`.
Validated: no slashes, no null bytes, max 128 characters.

### `--output-dir <path>`
Output directory for all artifacts (logs/, events.jsonl, report.json).
Default: current working directory (existing behavior preserved).

### `--json-events`
Write JSONL events to `<output-dir>/events.jsonl`. One JSON object per line,
append-only. Does NOT suppress the existing human-readable stdout output.

### `--report-json`
Write a JSON summary to `<output-dir>/report.json` at run completion.
Does NOT suppress the existing human-readable stdout output.

## Event Format (JSONL)

Each line in `events.jsonl` is a self-contained JSON object:

```json
{
  "run_id": "run-20260701-153000",
  "ts": "2026-07-01T15:30:00.123Z",
  "level": "info",
  "phase": "connect",
  "event": "phase_started",
  "message": "Connecting to servers",
  "source": {"ip": "1.2.3.4", "user": "srcuser"},
  "destination": {"ip": "5.6.7.8", "user": "destuser"}
}
```

### Levels
- `info` — normal progress
- `warn` — non-fatal issue
- `error` — phase failure

### Event Types
- `run_started`, `run_completed`, `run_failed`
- `phase_started`, `phase_completed`, `phase_skipped`, `phase_failed`

### Phases
`connect`, `analyze_mail`, `analyze_files`, `analyze_db`, `gather_data`,
`compare_mail`, `compare_files`, `compare_db`, `create_domains`,
`migrate_mail`, `verify_mail`, `copy_files`, `verify_files`,
`migrate_db`, `verify_db`

## Report Format (JSON)

```json
{
  "run_id": "run-20260701-153000",
  "version": "2.2.1",
  "mode": "dry-run",
  "scope": {
    "mail": true,
    "files": true,
    "databases": true
  },
  "exit_status": "success",
  "errors": [],
  "artifacts": {}
}
```

### Exit Statuses
- `success` — run completed without errors
- `failed` — run completed with errors
- `interrupted` — run was interrupted (Ctrl-C)

## Security

- No passwords, tokens, or secrets appear in events or report.
- The `HostRef` struct contains only `ip` and `user` (no `ssh_pass`, no `cpanel_token`).
- Event `data` field is redacted using the same sensitive-key detection as the existing `debug.go`.

## Backward Compatibility

When none of the new flags are passed, behavior is identical to the previous version.
The `Events` field in `migrate.Options` uses a zero-value `Emitter` (nil callback),
which is a no-op.
