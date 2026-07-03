# PR 2A — cron apply (design, v1)

Date: 2026-07-03. The first cron writer of the tool. Motivation: the
first real apply (Fase 0.2, `FASE0_2_FIRST_APPLY.md`) left cron as a
blocking manual action on the checklist (ADAPT_CRON_PATH/RECREATE_CRON).
The master plan lists 2A as the "symbolic case of forgetting something".

## House contract (unchanged, inherited from 2B)

- Offline plan → dry-run by default → `--yes-apply-writes` → backup before
  the first write (backup-or-nothing) → idempotent apply → verify-after →
  conscious amendment of the safety tests.
- SOURCE is never written, in any phase, for any reason.
- **Never delete destination-only cron entries.** The only cron changes the
  tool makes are additions of source entries absent on the destination; the
  rollback inverse removes ONLY those added entries.
- `manual` is terminal: "flagged but applied anyway" does not exist.

## Write primitive: SSH `crontab -`

### Why SSH, not API

Both `cpapi2 Cron` and `uapi Cron` are broken on .78:
- `cpapi2`: depends on `/usr/local/cpanel/cpanel` (missing).
- `uapi Cron`: module `Cpanel::API::Cron` fails to load.
See `CPAPI2_DIAGNOSIS_78.md`.

`crontab -l` (read) and `crontab -` (write from stdin) work normally
via SSH. This is the only available primitive.

### Semantics

`crontab -` REPLACES the entire user crontab with the content piped to
stdin. This is inherently destructive — the same class as
`set_default_address` and `store_filter` (upsert), with an even wider
blast radius (the whole crontab, not one entry).

Compensation (design of the apply loop):
1. **Fresh read** (`crontab -l`) immediately before every write cycle.
2. **Merge**: append the planned line(s) to the read content, or remove
   the planned line(s) from the read content (rollback). Never reorder,
   never modify existing lines.
3. **Install**: pipe the merged content to `crontab -`.
4. **Verify**: `crontab -l` again, compare the output byte-for-byte
   against the intended merged content.
5. **If verify fails**: emit `failed`, DO NOT retry (the crontab is now
   in an unknown state → the operator reviews the backup).

### Atomic window

The window between the read (step 1) and the install (step 3) is
seconds-wide (single-operator tool). A racing `crontab -e` by a human
during that window would be overwritten — the same residual as the
email writers' TOCTOU window, bounded by the verify-after.

## Collector extension: commands in clear

By user decision (2A gate, option A): `CronJobEntry` gets `CommandClear`
(the raw, un-redacted command) alongside the existing `CommandRedacted`/
`CommandSHA256`. `CronEnvEntry` gets `ValueClear` alongside
`ValueRedacted`. Both carry a `Collected bool` honesty marker (pattern:
`BodyCollected`, `RulesCollected`).

Display is UNCHANGED: diff, policy, checklist, Markdown tables continue
to use the redacted fields. The clear fields live only in the JSON raw
(local, gitignorato). The clear text is needed for the `crontab -`
install: redacted commands cannot be installed.

### Path adaptation

The master plan mentions `/home/<olduser>` → `/home/<newuser>` path
adaptation. This is implemented in the plan builder, NOT in the
collector: the collector stores the raw command verbatim, the plan
transforms paths only when the source and destination users differ.

## Plan ops

One op per cron job (keyed by `CommandSHA256` — the diff's matching key):

| Source state | Dest state | Action | Notes |
|---|---|---|---|
| Active job, dest missing | — | `create` | Append line to crontab |
| Active job, dest has IDENTICAL | — | `skip` | Command+schedule match |
| Active job, dest has DIFFERENT schedule | — | `manual` | Never overwrite |
| Disabled job, dest missing | — | `manual` | User decides if needed |
| Either side not collected | — | `manual` | Re-run inventory |
| — | Dest-only job | `informational` | Never deleted |

### Path adaptation in `create` ops

When `source_user != destination_user` and the command contains
`/home/<source_user>/`, the plan:
1. Records the ORIGINAL command in `SourceValue`.
2. Records the ADAPTED command in `Value` (the installable form).
3. Marks `PathAdapted: true` in the op.

The operator sees both in the plan Markdown. The writer installs `Value`.

### Environment lines

Env lines (`MAILTO=…`, `PATH=…`) follow the same pattern:
- Identical on both sides → `skip`.
- Source-only → `create` (append to crontab before the first job).
- Different value → `manual` (never overwrite an operator's PATH/MAILTO).
- Dest-only → `informational`.

## Freshness guard

`crontab -` replaces the ENTIRE crontab → the guard is a single
full-content comparison:

1. Before the first write: `crontab -l` → raw backup.
2. The guard compares the CURRENT crontab against the plan-time
   destination crontab (stored as `PlanTimeDestCrontab` in the plan):
   - If different (someone changed the crontab since the plan was
     generated): **refuse ALL ops** (not per-op — the crontab is an
     atomic unit). Re-plan required.
   - If identical: proceed with the merge.
3. After install: `crontab -l` → verify the installed content.

This is MORE conservative than the email per-op guard because `crontab -`
is inherently whole-crontab (not per-item).

## Backup and rollback

- **Backup**: the raw `crontab -l` output BEFORE the first write, saved
  as `cron_backup_<account>_<ts>.txt`. No backup ⇒ no write.
- **Rollback** (`cron apply --rollback <backup-file>`): installs the
  backup content via `crontab -`. Paired report required (same pattern
  as email rollback). Without the report: MANUAL.
- **Rollback guard**: `crontab -l` on the destination must match the
  post-apply crontab (from the report); if it diverged, rollback refuses.

## Safety test amendments

- `crontab -r` (remove crontab entirely): FORBIDDEN everywhere, no
  allowlist. The tool never removes a crontab.
- `crontab -` (install): allowed ONLY in the writer file
  (`internal/cpanel/cron_apply.go` + the `cron apply` command file).
- `cronplan_safety_test.go`: new, mirrors `emailplan_safety_test.go`.
- Module-wide scan: extend the cron forbidden-verb list.
- Checklist safety: extend `writeCalls`.

## Command surface

- `inventory cron-plan` — OFFLINE plan builder (consumes both
  inventories, produces `cron_apply_plan.json` + `.md`).
- `cron apply` — the writer (connects DEST only). Flags mirror
  `email apply`: `--plan`, `--config`, `--yes-apply-writes`,
  `--rollback`, `--output-json/-md`.
- `cron verify` — read-only re-certification against a plan.

## Out of scope (explicit)

- Modifying existing destination cron entries (only additions).
- Cron API (broken on .78, see diagnosis).
- Email routing setmxcheck (blocked, separate issue).
- Any `internal/migrate/runner.go` change.
