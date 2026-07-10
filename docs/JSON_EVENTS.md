# JSON Events & Report

The structured artifacts the engine produces for an external orchestrator:
`events.jsonl` (a stream) and `report.json` (a summary). Both are versioned
documents of the execution contract — see
[`ADR_V2_GO_EXECUTOR.md`](ADR_V2_GO_EXECUTOR.md).

Without the JSON flags, the CLI behaves exactly as before: the flags **add**
artifacts, they never replace the human-readable stdout output.

## CLI Flags

### `--run-id <string>`
Optional run identifier. If omitted, auto-generated as `run-YYYYMMDD-HHMMSS`.
Validated by `events.ValidateRunID`: non-empty, at most 128 characters, no
slashes, no backslashes, no NUL bytes.

### `--output-dir <path>`
Output directory for all artifacts (`logs/`, `events.jsonl`, `report.json`).
Default: the current working directory. This directory is the **run workspace**;
artifact paths in `report.json` are relative to it.

### `--json-events`
Write JSONL events to `<output-dir>/events.jsonl`. One JSON object per line,
append-only.

### `--report-json`
Write a JSON summary to `<output-dir>/report.json` at run completion.

## `format_version` is not `version`

Two different numbers, deliberately kept apart:

| Field | Where | Meaning |
|---|---|---|
| `format_version` | `events.jsonl` **and** `report.json` | version of the **document format** |
| `version` | `report.json` only | **executor build version** (`internal/version`, set via ldflags) |

Both writers stamp `format_version` themselves — `events.Writer.Write` and
`events.WriteReport` — from the single constant `events.CurrentFormatVersion`.
No emitter has to remember it, and no document can leave without it.

Confusing the two would let a consumer accept a document whose shape it does not
understand because the binary happens to be a familiar build, or reject a
document it understands perfectly because the binary was rebuilt.

## Compatibility policy

`format_version = 1`.

- version `1` — supported
- version **absent** — rejected
- version `0` — rejected
- **future** version — rejected
- no automatic downgrade, no best-effort interpretation

**Additive** output fields are tolerated: a consumer must ignore a top-level key
it does not know, so the executor can add one without breaking an older platform.
Any **incompatible** change requires incrementing `format_version`.

The **input** spec is the opposite: unknown fields are rejected at every level. A
field the executor silently ignores may be a field the operator believes is being
honoured.

> Artifacts produced **before** this contract existed carry no `format_version`
> and are **not** version 1 documents. Nothing here promises to read them.

A document must be valid UTF-8. Go's `encoding/json` would otherwise replace an
invalid byte inside a string with U+FFFD and decode a truncated artifact into
mojibake; both validators reject it instead. Silently accepting a corrupted
`run_id` is worse than failing to read the file.

## Schemas

JSON Schema, draft 2020-12, in `schemas/`:

| Document | Schema | Direction |
|---|---|---|
| input spec | `schemas/execution-spec-v1.json` | platform → executor |
| `events.jsonl` line | `schemas/execution-event-v1.json` | executor → platform |
| `report.json` | `schemas/execution-result-v1.json` | executor → platform |

The schemas describe **structure**. Cross-field rules — scope coherence, version
policy, recursive redaction, `finished_at >= started_at`, artifact confinement —
live in two semantic validators that must agree:

- Go: `internal/executioncontract`
- Python: `migration-platform/packages/domain/domain/execution_contract.py`

Both are exercised against the same corpus, `testdata/execution-contract/`, by
`scripts/test-execution-contract.sh`.

## Event Format (JSONL)

Each line in `events.jsonl` is a self-contained JSON object:

```json
{
  "format_version": 1,
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

`ts` is RFC3339 with a timezone; Go emits nanosecond precision when nonzero.

`source` and `destination` are **always present**, even on a run-level event.
`events.HostRef` is a non-pointer struct, so Go's `omitempty` never fires and an
absent host marshals to `{"ip": "", "user": ""}`. Consumers must not treat their
presence as meaningful.

`data` is optional and free-form.

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

**Run-level events carry `"phase": ""`.** The field has no `omitempty`, so the
empty string is a valid phase, not a malformed one.

## Report Format (JSON)

Every field below is always present except `artifacts`, which is omitted when
empty:

```json
{
  "format_version": 1,
  "run_id": "run-20260701-153000",
  "version": "2.2.1",
  "mode": "dry-run",
  "scope": {"mail": true, "files": true, "databases": true},
  "source": {"ip": "1.2.3.4", "user": "srcuser"},
  "destination": {"ip": "5.6.7.8", "user": "destuser"},
  "started_at": "2026-07-01T15:30:00Z",
  "finished_at": "2026-07-01T15:34:12Z",
  "exit_status": "success",
  "phases_completed": ["connect", "analyze_mail"],
  "warnings": [],
  "errors": [],
  "artifacts": {"events_jsonl": "events.jsonl"}
}
```

### Modes
`dry-run`, `apply`, `account-inventory`.

Note the hyphen. The **input** spec's `mode` is `dry_run`, with an underscore:
input and output speak different vocabularies over different value sets, and
merging them would make the contract lie.

### Exit Statuses
- `success` — run completed without errors
- `failed` — run completed with errors
- `interrupted` — run was interrupted (Ctrl-C)

### Scope
A record of what ran. Unlike the spec's scope it has **no** "at least one true"
rule: an `account-inventory` report legitimately carries all three `false`.

### Artifacts
A map of artifact name to a **slash-separated path relative to the run
workspace** (`--output-dir`), recorded only after an existence check on disk.

The consumer resolves these against a workspace it owns, so the validators reject
absolute paths, `..` segments under either separator, NUL bytes, and any `:`
(a colon is both a Windows drive letter and an alternate data stream, and no
engine-produced artifact name contains one). An escaping path is a
write-anywhere primitive.

The colon is rejected as a whole character rather than by position on purpose:
an index-based drive-letter check disagrees across languages the moment the path
holds a multi-byte rune, because Go indexes bytes and Python indexes code points.

## Security

- No passwords, tokens, or secrets appear in events or report.
- `HostRef` carries only `ip` and `user` — never `ssh_pass`, never a cPanel token.
- The event `data` payload is routed through the redaction net in
  `internal/events/redact.go` before being written. A key whose lower-cased,
  trimmed form contains `token`, `secret`, `pass`, `key`, `auth`, `cred`,
  `cookie`, `session` or `bearer` has its non-empty value replaced with
  `<redacted>`. Redaction recurses through nested objects and arrays, and applies
  to typed struct payloads too (they are marshaled to their object form first),
  so a future payload cannot bypass it by not being a `map`.
- The validators enforce the same rule on **read**: a sensitive key holding
  anything other than `null`, `""`, or `<redacted>` rejects the document. This
  holds for additive extra fields as well — extensibility is not a leak channel.

> `internal/cpanel/debug.go` keeps its own copy of the same substring list for
> raw-response logging. The two are identical today but are not shared; changing
> one does not change the other.

## Backward Compatibility

When none of the JSON flags are passed, behaviour is identical to the previous
version. `migrate.Options.Events` holds a zero-value `Emitter` (nil callback),
which is a no-op.
