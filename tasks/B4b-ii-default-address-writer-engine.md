# Task B4b-ii: Compensable default-address writer engine

| Field | Value |
|---|---|
| **ID** | `B4b-ii` |
| **Status** | `[ ]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4b-i |
| **Branch** | `feat/b4b-ii-default-address-writer-engine` |

**Origin:** second sub-task of the scope split of `B4b` (see
[B4b-default-address-writer.md](B4b-default-address-writer.md), split record).
Builds the compensable writer engine on top of the B4b-i evidence contract and
pure rules, and the B4a shared framework.

**Goal:** Implement the per-domain default-address writer as a *compensable, not
additive* phase: fresh-read live → decide (B4b-i rules) → persist a redacted
backup atomically **before** the write → gated `set_default_address` → live
post-write verify → redacted compensation. It reuses `execute_email_phase` from
B4a without duplicating its lifecycle. Not wired into the runtime dispatch (that
stays with B4e).

**Scope (≤8 files / ≤500 changed lines):**

- `email_write.py` — add an optional `backup_of` seam: when a category provides it,
  the engine computes a redacted backup from the pre-write live evidence and records
  it **before** the write; if the backup cannot be secured, the write is not reached
  (backup-or-nothing). Default `None` keeps the forwarder path unchanged.
- `default_address_writer.py` (new) — resolve items from the preview default-address
  steps, plan the redacted call, provide `backup_of` (previous fresh value → restore
  descriptor), and run the compensable phase with the B4b-i rules.
- tests + docs.

**Category behavior:**

- `set_default_address` overwrites, so a `set` is only reached for a fresh
  destination (per B4b-i); a differing custom catch-all is blocking.
- The previous verified value is persisted as redacted backup/compensation metadata
  **before** any write; if the backup is not persisted atomically, the write is not
  reached; live post-write verification.
- Non-idempotent write: never auto-retried; a timeout/ambiguous outcome is resolved
  by a fresh read, never a blind second write.
- `before_write` remains the B4e authorize/lease/fencing seam.

**Testing Requirements (deterministic fake gateway, no real servers):** the full
compensable matrix — flag disabled; source impossible; equivalent → zero write;
destination missing/empty → backup + one write + verify; different → blocked, zero
write; source/destination unreadable/ambiguous → manual; destination domain
missing → blocked; reject/fail preserved; forward-to-address preserved;
pipe/command → manual; collector duplicates/conflict; failure never empty; legacy
snapshot ineligible; backup persisted before the write; backup failure → zero
write; race (value appeared after snapshot) → blocked; ambiguous write + positive
fresh-read; ambiguous write + negative fresh-read; no second write; post-write
mismatch; `before_write` fails → zero write; compensation metadata complete and
redacted; no address/sensitive payload in events unless approved by the redacted
contract; B4a framework and forwarder without regressions; ≥90% coverage.

**Adversarial review:** overwrite of an existing catch-all; false empty; backup
after the write; insufficient compensation; pipe/command reinterpreted; domain
normalized with wrong meaning; race between fresh-read and write; non-idempotent
retry; false post-write success; secret/address leakage.

**Acceptance Criteria:**

- [ ] Only a fresh destination is set, backed up before the write and verified live;
      a differing catch-all is never overwritten; the source is never written.
- [ ] No test, typecheck, Compose, or coverage regression.
- [ ] Real behavior disabled by default and unreachable from the runtime until B4e.

**Risk & Rollback:** Main risk is an unintended overwrite or a false verification.
Keep the flag disabled, revert the module if needed, never compensate by mutating
the source.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```
