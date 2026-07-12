# Task B4e-ii: Additive-only autoresponder writer engine

| Field | Value |
|---|---|
| **ID** | `B4e-ii` |
| **Status** | `[ ]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4e-i |
| **Branch** | `feat/b4e-ii-autoresponder-writer-engine` |

**Origin:** second sub-task of the scope split of `B4e` (see
[B4e-autoresponder-dispatch.md](B4e-autoresponder-dispatch.md), split record).

**Goal:** Implement the per-address autoresponder writer as an *additive-only* phase reusing
`execute_email_phase` and the B4e-i contract/fingerprint/rules (no `email_write.py` change).
`Email::add_auto_responder` UPSERTS, so a write is reached only on a live-absent address —
proven by TWO distinct fresh reads (initial decision + a guard immediately before the write)
— then verified live by the complete fingerprint. Not wired into the runtime dispatch (B4e-iii).

**Naming:** the existing `autoresponder_writer.py` is the mock orchestration and must stay
intact (mock/dry-run unchanged); the real engine lands in a new module (e.g.
`real_autoresponder_writer.py`).

**Category behavior:**

- Reuse `execute_email_phase`; destination-only gateway; fresh-read per domain immediately
  before the decision.
- Two anti-upsert fresh reads: the initial decision read, and a guard (`list_auto_responders`
  in the same domain, absence by enumeration only — never `get_auto_responder`) immediately
  before the single `add_auto_responder`.
- A single create only on a live-absent address; no delete/rollback; no auto-retry;
  timeout/ambiguous → fresh list/detail, never a second create.
- Post-write verification via the complete fingerprint (not the body in logs).
- `before_write` remains the B4e-iii gate/fencing seam.
- Compensation metadata (redacted: scope/address/fingerprint, controlled future removal of the
  just-created responder, confirmation required) attached **only** for a create the gateway
  actually wrote and verified; never for `already_present` or a guard-skipped write, so it can
  never remove a pre-existing responder.

**Testing Requirements (deterministic fake gateway, no real servers):** flag disabled; source
impossible/unsupported; same address+fingerprint → zero write; live-absent address → guard +
one write + verify; same address different fingerprint → blocked; destination-only preserved;
source incomplete → manual; destination partial → manual; domain missing → blocked; race after
snapshot / immediately before the create → zero write; guard uses the same scope and never
`get_auto_responder` for absence; before_write failure skips guard+write; ambiguous
positive/negative; no second write; post-write fingerprint mismatch; no delete; redacted
compensation only for a verified create; no raw/body/secret leak; B4a–B4d without regressions;
≥90% coverage.

**Adversarial review:** unintended upsert; same-address collision ignored; body/subject leak;
verify-by-address-only; retry of the create; implicit delete; template accepted; compensation
able to remove a pre-existing responder; DestinationWrite payload in events.

**Acceptance Criteria:**

- [ ] An autoresponder is created only when its address is live-absent, guarded against the
      upsert race, and verified live by the complete fingerprint; a same-address different
      responder is never overwritten; nothing is deleted; the source is never written.
- [ ] No test, typecheck, Compose, or coverage regression.
- [ ] Real behavior disabled by default and unreachable from the runtime until B4e-iii.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```
