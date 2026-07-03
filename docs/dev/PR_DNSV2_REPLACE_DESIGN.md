# DNS Apply v2 — Replace/Edit Design

Date: 2026-07-04. Frozen before implementation — adversarial review required.

## Problem

v1 (#52/#54) is ADD-ONLY: imports missing rrsets but skips `ActionReplace`
ops (`skipped_replace_v1`). At cutover this is the critical gap:

- **A apex stale**: dest still points to old IP after ip-map translation
- **SPF with old IP**: TXT containing mapped source IP (currently MANUAL
  in the plan, not replace — see below)
- **DKIM divergence**: source and dest have different DKIM keys

## What already works (v1, no change)

| Layer | Replace support | Notes |
|-------|----------------|-------|
| Plan (`dnsplan.go`) | `ActionReplace` + `Records` populated | classify() rule 9 |
| Verify (`dnsverify.go`) | applied/pending/drift | verifyOp handles add+replace |
| Report types (`dnsapply.go`) | `DNSOpSkippedReplaceV1` | to be retained for v1 report compat |
| Report writers | renders `skipped_replace_v1` | no change needed |
| Backup | full zone pre-write state | sufficient for replace rollback |

## What needs implementing

1. **`MassEditZoneBatch` primitive** — combined remove+add in one `mass_edit_zone` call
2. **Replace path in apply cmd** — precondition check, line resolution, batch call, verify-after
3. **Replace rollback** — reverse the replace using backup + report
4. **Dry-run preview** — show replace as writable, not skipped
5. **Test stub** — handle combined remove+add parameters
6. **`writableZones`** — include zones with replace ops

## Critical design decision: remove+add, NOT edit[]

The `edit[]` parameter of `mass_edit_zone` is documented in cPanel API
but was **NOT byte-verified** in PR6D_PRE_CAPTURES (only `add[]` and
`remove[]` were). Rather than introducing an unproven primitive:

**Replace = remove old records + add new records in a SINGLE `mass_edit_zone` call.**

Rationale:
- Both `remove-N=` and `add-N=` are byte-verified (PR6D_PRE_CAPTURES fact 1, 5)
- `mass_edit_zone` processes removes BEFORE adds in a single zone file write → atomic
- No window where the record is absent (zone file written once)
- Reuses proven parameter formats
- No new primitive risk

If future evidence shows `edit[]` is needed (e.g., for record attributes
the remove+add pattern can't preserve), it can be added as v3 on top of
this foundation.

## New primitive: `MassEditZoneBatch`

```go
func MassEditZoneBatch(ctx, runner, zone, serial string,
    removeLines []int, addRecords []MassEditAddRecord) (MassEditResult, error)
```

Same file (`internal/cpanel/dns_apply.go`), same allowlist. Combines
`remove-N=` and `add-N=` parameters in one `DNS::mass_edit_zone` call:

```
remove-0=<line_idx_0>, remove-1=<line_idx_1>, ...
add-0=<record_json_0>, add-1=<record_json_1>, ...
zone=<zone>, serial=<serial>
```

When `removeLines` is empty: equivalent to `MassEditZoneAdd`.
When `addRecords` is empty: equivalent to `MassEditZoneRemove`.

## Guardrails preserved by the plan (no change)

| Type | Plan classification | Replace? |
|------|-------------------|----------|
| SOA | ActionSkip (rule 1) | Never |
| NS | ActionManual (rule 3) | Never |
| `_acme-challenge` / DCV | ActionSkip (rule 2) | Never |
| TXT with mapped IPs (SPF) | ActionManual (rule 8) | Never auto |
| Unsupported types | ActionManual (rule 4) | Never |

Only A/AAAA (mapped), CNAME, MX, and TXT (no mapped IPs, valid UTF-8) can
reach `ActionReplace` through classify() rule 9.

## DKIM handling

`default._domainkey` TXT records with different values reach `ActionReplace`.
This is **intentionally allowed** — the plan carries both source and dest
values; the operator reviews the plan before `--yes-apply-writes`.

Note: at cutover, the **destination's** DKIM key should typically be
preserved (emails will be sent from .78). The plan's `Records` field
carries the source DKIM key as "desired". The operator MUST review DKIM
replace ops and either:
- Accept (if source key transfer is intended)
- Manually skip (edit the plan or handle outside the tool)

Future improvement: plan-level DKIM-aware classification (mark as manual
or provide dest-key-wins option). NOT in this PR.

## Replace preconditions (per-op)

For each replace op in the plan:

1. **Re-fetch fresh zone** (already fetched for the zone — shared state)
2. **Find matching dest records** by type + canonical name + plan-time
   `DestinationValues`:
   - Use `planValue()` to compute canonical value for each live record
   - Match against `op.DestinationValues` (the plan-recorded dest state)
3. **Decision table**:

| Live state | Status | Action |
|-----------|--------|--------|
| Live values == desired `Records` | `skipped` + "already_present" | No write |
| Live values == plan-time `DestinationValues` | proceed | Remove old + add new |
| Live values == neither (drift) | `refused_precondition` | No write, fail-closed |
| Type+name not found on dest | `refused_precondition` | No write (rrset vanished) |

4. **Line index resolution**: from the matched records (step 2), extract
   `Line` field (0-based). Valid ONLY against the current serial.

## Apply flow (zones with replace ops)

For each zone:

```
1. Classify ops: add[] / replace[] / skip / manual
2. For replace ops: resolve line indexes + precondition check
3. Batch: remove lines (from replace) + add records (from replace + add ops)
   → single MassEditZoneBatch call with serial guard
4. Verify-after: re-fetch zone
   - For add ops: desired records present
   - For replace ops: desired records present AND old values absent
5. Status per op: applied / failed / refused_precondition / skipped (already_present)
```

If a zone has ONLY add ops (no replace): use existing `MassEditZoneAdd`
(no change to v1 codepath).

If a zone has replace ops: all ops (add + replace) batched into one
`MassEditZoneBatch` call.

## Backup (no format change)

The existing backup already archives full zone state (Records, Raw, Serial)
before any writes. This is sufficient for replace rollback — the backup
records capture the pre-replace values.

## Rollback for replace ops

Report-driven, same pattern as add rollback:

1. Load backup (pre-write zone state) + report (what was applied)
2. For each zone, identify applied replace ops from the report
3. For rollback: need to remove the NEW values (what was written) and
   restore the OLD values (from the backup)
4. **Find new values**: match op.Records against the current live zone
   → get line indexes
5. **Reconstruct old values**: from backup zone records, find records
   matching op type+name with plan-time DestinationValues → build
   `MassEditAddRecord` entries
6. **Guard**: current value must match what the tool wrote (op.Records);
   if divergence → refused (someone changed it after apply)
7. Execute: `MassEditZoneBatch(removes=new_value_lines, adds=old_values)`
8. Verify-after: old values present, new values absent

Degraded rollback (`--accept-report-loss`): all zones become MANUAL
(no change from v1).

## Verify (no code change)

`dnsverify.go` already handles `ActionReplace`:
- Live matches desired → `applied`
- Live matches plan-time dest → `pending`
- Otherwise → `drift`

## Report rendering (minimal change)

`dnsapply_write.go`: replace ops that get `applied` / `failed` / `refused`
render the same as add ops (already handled by status-based rendering).
`skipped_replace_v1` is retained for backward compatibility with v1 reports.

## Dry-run preview (change)

`printDNSApplyDryRun`: replace ops currently show "skipped_replace_v1".
Change to show them as writable:

```
  [zone] replace  TYPE NAME (N record(s) → N record(s))
```

Count replace ops in the `writes` counter.

## Exit codes (no change)

- 0: all ops ok (applied/skipped/manual)
- 1: any op failed
- 3: any op refused_precondition

## Safety (delta only)

- `mass_edit_zone` already allowlisted in `dns_safety_test.go`
- `MassEditZoneBatch` in same file (`dns_apply.go`) → no allowlist change
- New function uses literal `"DNS"` / `"mass_edit_zone"` → passes AST test
- Add test: replace ops in plan must NEVER produce SOA/NS actions (plan
  invariant, not writer concern — but verify in test)

## Test stub delta

`dnsStubScript` python block: currently handles `remove-` and `add-`
prefixed args in separate passes. For combined remove+add: process
removes FIRST (decrement line indexes for subsequent removes), then
process adds. Both already use the same state file format.

## Out of scope

- `edit[]` parameter (no byte-verification)
- Plan-level DKIM-aware classification
- Multi-record rrset partial edit (v2 replaces the entire rrset)
- SPF rewriting (stays MANUAL per plan rule 8)
- Campaign Mode, SQLite, batch queue
