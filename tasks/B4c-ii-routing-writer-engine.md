# Task B4c-ii: Compensable routing writer engine

| Field | Value |
|---|---|
| **ID** | `B4c-ii` |
| **Status** | `[ ]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4c-i |
| **Branch** | `feat/b4c-ii-routing-writer-engine` |

**Origin:** second sub-task of the scope split of `B4c` (see
[B4c-email-routing-writer.md](B4c-email-routing-writer.md), split record).

**Goal:** Implement the per-domain routing writer as a *compensable* phase reusing
`execute_email_phase` and the existing B4b-ii `backup_of`/`persist_backup` seam (no
`email_write.py` change): fresh-read live routing → decide (B4c-i rules + policy) →
persist a typed backup of the previous routing before the write → gated
`setmxcheck` → live post-write verify → redacted compensation. Not wired into the
runtime dispatch (that stays with B4e).

**Scope (≤8 files / ≤500 changed lines):**

- `routing_writer.py` (new) — evidence adapters over the live `list_mxs` read, the
  framework decider (policy-gated `set`→create), `backup_of` (previous routing →
  restore descriptor), redacted plan/compensation, a destination-only gateway
  (SafeRead + `setmxcheck` DestinationWrite), and the phase runner.
- tests + docs.

**Category behavior:**

- `setmxcheck` overwrites, so a `set` is only reached on an exact policy-authorized
  transition; a differing/custom routing is blocking; secondary/unknown → manual.
- Previous routing backed up (redacted reference in compensation) before any write;
  backup unbuildable/not persisted → zero write; live post-write verify.
- Non-idempotent write: never auto-retried; timeout/ambiguous → fresh read, never a
  blind second write. `before_write` remains the B4e gate/fencing seam.

**Testing Requirements (deterministic fake gateway, no real servers):** flag
disabled; source impossible; equivalent → zero write; policy-authorized different →
backup + one write + verify; different without policy → blocked; secondary/unknown →
manual; domain missing → blocked; no DNS/MX inference; backup from live not snapshot;
backup before write; backup failure/invalid ref → zero write; ambiguous positive /
negative; no second write; post-write mismatch; race after snapshot; `before_write`
failure keeps backup and skips write; redacted compensation; no raw/secret leak; B4a
forwarder + B4b default-address without regressions; ≥90% coverage.

**Adversarial review:** routing inferred from MX; overwrite of a differing state;
backup from the snapshot; backup after the write; retry of set; false post-write
success; wrong domain; raw/payload in events.

**Acceptance Criteria:**

- [ ] Only a policy-authorized transition is set, backed up before the write and
      verified live; a differing routing is never overwritten; the source is never
      written.
- [ ] No test, typecheck, Compose, or coverage regression.
- [ ] Real behavior disabled by default and unreachable from the runtime until B4e.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```
