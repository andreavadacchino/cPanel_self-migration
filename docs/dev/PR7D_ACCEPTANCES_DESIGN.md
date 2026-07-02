# PR 7D — operator acceptance file (micro-design)

Goal: let the operator formally accept reviewed manual actions so they stop
gating the cutover verdict on every regeneration — with an auditable trail
and without ever waving through what must be resolved.

## The identity problem (and the fail-safe answer)

`MA-nnn` action IDs are positional: any new finding shifts the numbering,
so an acceptance bound to `MA-007` would silently apply to a DIFFERENT
action after inputs change. Acceptances therefore bind to a **stable
content key** each action now carries:

```
Key = "AK-" + first 12 hex of sha256(type \x00 section \x00 title \x00 detail)
```

Same underlying fact → same key across regenerations. If the fact changes
(different title/detail), the key changes and the acceptance simply stops
matching: the action RESURFACES un-accepted. Stale acceptances
self-invalidate — the fail-safe direction is automatic.

## acceptances.json

```json
{
  "mode": "operator-acceptances",
  "format_version": 1,
  "checklist_file": "migration_checklist.json",
  "checklist_sha256": "<sha256 of the checklist file the operator reviewed>",
  "acceptances": [
    {
      "action_key": "AK-…",
      "action_id": "MA-007",
      "reason": "sub-FTP accounts are obsolete, confirmed with the customer",
      "accepted_by": "andrea",
      "accepted_at": "2026-07-02T10:00:00Z"
    }
  ]
}
```

- `checklist_sha256` (required): audit anchor — WHICH checklist the
  operator was looking at.
- `checklist_file` (optional): when present, the CLI hashes that file and a
  mismatch with `checklist_sha256` REJECTS the whole acceptance file
  (warning; the checklist is still generated, just without acceptances).
  A relative path resolves against the acceptance file's own directory;
  `format_version` must be 1.
- Per entry: `action_key`, `reason`, `accepted_by`, `accepted_at` required;
  `action_id` is display-only.

## Engine semantics (`BuildChecklist`)

`ChecklistInput` gains `Acceptances []OperatorAcceptance`. Matching is by
`action_key` against the CURRENT actions:

- match + `acceptable: true` → the action is marked accepted
  (`accepted`, `accepted_by`, `accepted_at`, `accepted_reason` fields),
  its ID lands in the owning section's `accepted_by_operator`, and
  `summary.accepted` counts it.
- an accepted action stops counting BOTH as a real action for the section
  status AND as a blocking action for the overall rollup — accepting every
  blocking-but-acceptable action moves `MANUAL_ACTION_REQUIRED` to a
  READY_* verdict. Policy blockers (`SectionBlocked`) are NOT actions and
  are never affected.
- match + `acceptable: false` (CONFIRM_MX_EXTERNAL, blocking
  RECREATE_CRON) → warning, ignored. These must be resolved, not accepted.
- unknown key → warning ("inputs likely changed — re-review"), ignored.
- duplicate key in the file → first entry wins, warning.

`not_inventoried` sections keep their status (the area is STILL not
inventoried — that stays honest); accepting their blocking check only
clears the cutover gate.

## Output / schema changes

- `ManualAction`: `key` (always), `accepted` + `accepted_by` /
  `accepted_at` / `accepted_reason` (omitempty).
- `ChecklistSection.accepted_by_operator`: now populated (was reserved).
- `ChecklistSummary.accepted`: now populated (was always 0).
- `ChecklistInputs`: new `acceptances` input ref (file + sha256) for the
  audit trail; NOT part of the provenance chain verification (an
  acceptance file has no derivation hashes — it is operator input).
- Markdown report: actions table shows the key (operators need it to write
  acceptances) and accepted state. Golden files refreshed.

## CLI

`inventory checklist … --acceptances acceptances.json`. Load/validate
(mode, required fields), optional strict file-hash verification as above,
then feed the engine. Exit codes unchanged; `--fail-on-not-ready` now
naturally passes when everything gating was legitimately accepted.

## Testing

TDD, all offline in `internal/accountinventory` + `cmd`:
- key stability (same input → same key; changed detail → different key);
- accept clears section status and overall rollup (blocking acceptable
  action → READY_WITH_MANUAL_NOTES);
- non-acceptable actions cannot be accepted (warning pinned);
- unknown/duplicate keys warn and are ignored;
- not_inventoried section keeps its status while its gate clears;
- cmd: file loading/validation, checklist_file hash mismatch rejects all;
- goldens refreshed via UPDATE_GOLDEN=1.
