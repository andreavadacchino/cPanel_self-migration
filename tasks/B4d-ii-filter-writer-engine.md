# Task B4d-ii: Additive-only filter writer engine

| Field | Value |
|---|---|
| **ID** | `B4d-ii` |
| **Status** | `[ ]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4d-i |
| **Branch** | `feat/b4d-ii-filter-writer-engine` |

**Origin:** second sub-task of the scope split of `B4d` (see
[B4d-email-filters-writer.md](B4d-email-filters-writer.md), split record).

**Goal:** Implement the per-scope email filters writer as an *additive-only* phase reusing
`execute_email_phase` and the B4d-i contract/fingerprint/rules (no `email_write.py`
change): fresh-read live per scope → decide (B4d-i rules) → an **upsert-guarded** single
`store_filter` reached only when the name is live-absent → live post-write verify by the
complete fingerprint → redacted compensation. Not wired into the runtime dispatch (B4e).

**Upsert danger.** `Email::store_filter` UPSERTS, so it is non-idempotent and dangerous:

- Introduce a guard **immediately before** the call; if the name appears between the
  fresh-read snapshot and the write, block.
- If a reliable fresh-read cannot be obtained, zero write.
- Never treat the op as idempotent merely because the name matches.
- Never send a source filter over an existing destination filter.

**Category behavior:**

- Reuse `execute_email_phase`; destination-only gateway; fresh-read per scope immediately
  before the decision.
- A single `store_filter` only on a live-absent name; **no** `DeleteFilter`; no auto-retry;
  timeout/ambiguous → fresh-read, never a second `store_filter`.
- Post-write verification via the complete fingerprint (not by name).
- `before_write` remains the B4e gate/fencing seam.
- Compensation metadata indicates a future controlled removal of **only the created
  filter**, with scope/name/fingerprint redacted; no automatic rollback.

**Testing Requirements (deterministic fake gateway, no real servers):** flag disabled;
source impossible/unsupported; same name+fingerprint → zero write; live-absent name →
one write + verify; same name/different fingerprint → blocked; destination-only preserved;
source incomplete → manual; destination partial → manual; mailbox missing → blocked; race
after snapshot → zero write; race immediately before `store_filter` → block; ambiguous
positive/negative; no second write; post-write fingerprint mismatch; zero DeleteFilter;
redacted compensation; no raw/payload/secret leak; B4a/B4b/B4c without regressions; ≥90%
coverage.

**Adversarial review:** unintended upsert; same-name collision ignored; order loss;
partial fingerprint; false-empty per mailbox; sensitive payload in logs; `store_filter`
retry; implicit `DeleteFilter`; verify-by-name-only; account/mailbox scope confused;
compensation that could delete a pre-existing filter.

**Acceptance Criteria:**

- [ ] A filter is created only when its name is live-absent, guarded against the upsert
      race, and verified live by the complete fingerprint; a same-name different filter is
      never overwritten; a destination-only filter is never deleted; the source is never
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
