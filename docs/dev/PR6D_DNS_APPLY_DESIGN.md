# PR 6D — dns apply (delta design, v1)

Date: 2026-07-03. Delta from the frozen PR6A design — only the writer
specifics that PR6A left to 6D. The full contract (backup-or-nothing,
serial guard, never-delete, rollback <60s) is in PR6A_DNS_IMPORT_DESIGN.md.
The byte-verified write facts are in PR6B_PRE_CAPTURES.md.

## Write primitive

`DNS::mass_edit_zone` via `RunUAPI` with literal module/function names
(covered by `TestDNSAPICallsUseLiteralNames`). This is the ONLY DNS
write the tool ever issues. No API2, no raw zone-file writes.

## Op universe (v1)

The 6B plan produces four action types: `add`, `replace`, `manual`, `skip`.

**v1 implements `add` only.** `replace` is deferred to a future PR:
- `add` appends records that don't exist on the destination.
- `replace` requires `edit` (line_index-addressed) or `remove` + `add`,
  both of which need line_index resolution — higher risk, separate PR.
- `manual` and `skip` are never written (terminal/noop).

A plan with `replace` ops: the writer applies all `add` ops and skips
`replace` ops with status `skipped_replace_v1` (the verify will report
them as `pending`, not `applied`). The operator reviews them manually
or waits for a future PR.

## Serial guard (per-zone)

1. `parse_zone` → fresh zone content + SOA serial.
2. `mass_edit_zone zone=<zone> serial=<serial> add=[...]` — the serial
   is the optimistic lock; if the zone changed since the fetch, the call
   fails (stale-serial error, shape TBD in 6D-pre).
3. On stale-serial error: `refused_precondition` for ALL ops on that
   zone. No retry (the operator must re-plan).
4. On success: response carries `data.new_serial`. If more ops target
   the same zone (batching), the new serial becomes the guard for the
   next batch.

## Batching

All `add` ops for the same zone are batched into a SINGLE
`mass_edit_zone` call (one SSH round-trip per zone). The `add[]` array
carries all records. This is both faster and safer (one serial guard
covers all ops atomically).

## Backup

Before the first write on a zone: `parse_zone` → full zone backup saved
as `dns_backup_<zone>_<ts>.json` (raw UAPI response + normalized
records + serial). No backup ⇒ no write.

## Verify-after

After each `mass_edit_zone` call: `parse_zone` again → check that every
planned record is observably present. An `add` op is `applied` only
when the added record exists in the post-write zone. Convergence:
if the record already exists before the write → `already_present` (the
`mass_edit_zone add` is a no-op for exact duplicates, per cPanel docs).

Post-apply certification: `dns verify --fail-on-drift` (PR 6C) is the
full verification pass, stale-plan SHA256-gated. The writer's inline
verify is a quick per-op check; 6C is the thorough certification.

## Rollback

`dns apply --rollback <backup-file>` — paired report required (same
pattern as email rollback).

Inverse of an applied `add` = `mass_edit_zone` with `remove[]` carrying
the line indexes of the added records. Line indexes are re-resolved on
a fresh `parse_zone` (never reused from the apply fetch — they may have
shifted). The re-resolution matches records by content (type + name +
TTL + data) against the fresh zone; if the content diverged (a human
edited the record since) → `refused`, manual resolution required.

Degraded (no report): ALL rollback ops are MANUAL. Without the report,
the tool cannot know which records it added vs. which were already
present.

## Safety test amendments

- `dns_safety_test.go`: `TestNoDNSWriteFunctions` and
  `TestNoDNSWritePatternsModuleWide` get their FIRST per-file allowlist:
  `internal/cpanel/dns_apply.go` + the `dns apply` command file.
- `dnsplan_safety_test.go`: unchanged (the plan is offline, no allowlist).
- `checklist_safety_test.go`: `mass_edit_zone` added to `writeCalls`.
- `TestDNSAPICallsUseLiteralNames`: the new `RunUAPI` call in
  `dns_apply.go` is automatically covered.

## Command surface

- `dns apply` — the writer (connects DEST only). Flags mirror
  `email apply`: `--plan`, `--config`, `--yes-apply-writes`, `--ip-map`,
  `--rollback`, `--output-json/-md`.
- Post-apply: `dns verify --fail-on-drift` for certification.

## Out of scope (v1)

- `replace` ops (edit/remove + add — future PR).
- Zone creation/deletion.
- NS/SOA record changes.
- Any write to zones not in the plan.
