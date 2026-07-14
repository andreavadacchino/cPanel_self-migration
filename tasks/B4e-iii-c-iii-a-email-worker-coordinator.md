# Task B4e-iii-c-iii-a: Email worker coordinator

| Field | Value |
|---|---|
| **ID** | `B4e-iii-c-iii-a` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4e-iii-c-ii |
| **Branch** | `feat/b4e-iii-c-ii-email-gateways-backups` |

**Origin:** first sub-task of the scope split of `B4e-iii-c-iii`.

**Goal:** Create a standalone email coordinator (`email_worker_coordinator.py`) that
deterministically orchestrates the selected email categories from a run's preview and
returns a terminal-agnostic, redacted `EmailCoordinationResult`. The coordinator reuses
the c-i registry/resolvers, c-ii single-category executor, `safety_gates.authorize`,
A4 fencing, and persisted snapshots — but is NOT imported by dispatch/actor/router,
does NOT modify run/attempt terminal state, does NOT call `finalize_attempt`, and does
NOT update `IMPLEMENTED_REAL_CATEGORIES`.

**Acceptance Criteria:**

- [x] `email_worker_coordinator.py` orchestrates email categories from the run preview
      with: per-category authorize/fencing, fresh cancellation check (no identity-map
      stale), before_write callback (fresh status + authorize + fencing), post-phase
      fencing-only (no full authorize on unrelated evidence), injected progress callback
      (never called if fenced-out), redacted `EmailCoordinationResult` (no snapshot,
      contract, kwargs, token, routing raw, filter rules, autoresponder body, ciphertext).
      Routing remains registered but non-executable (readiness `needs_contract_test`,
      authorize rejects, result pending/blocked). Not imported from dispatch/actor.
      `IMPLEMENTED_REAL_CATEGORIES == frozenset({"domains"})`. Mock/dry-run intact.
- [x] No test, typecheck, Compose, or coverage regression.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```

## Completion Record

**Date:** 2026-07-14

### Commit 1 (a8504b4): initial implementation

- `email_worker_coordinator.py`: 231 lines (NOT 160 as initially stated)
- `test_email_worker_coordinator.py`: 343 lines, 29 tests
- Total: 744 insertions / 12 deletions / 7 files
- **Guardrail deviation:** 744 total lines exceeds 500-line budget; production code
  (231 lines) within single-file limit; test file (343 lines) within 400-line limit.
- Adversarial review 11/11 PASS (initial cycle)

### Commit 2 (corrective): 7 verified issues fixed

**Issues corrected:**
1. Pre-phase authorize rejection continued loop → now sets `stopped=True` (Correction E)
2. Post-phase fencing loss caught and returned result → now propagates `ConflictError` (Correction D)
3. All ConflictError classified as cancellation → typed `_CoordinationCancelled` / `_CategoryGateRejected` with fresh-status re-read for ConflictError from runner (Correction B/C)
4. Non-email categories (domains) marked unknown/pending → `_select_email_categories()` filters to `EMAIL_CATEGORIES` only (Correction A)
5. Raw `phase_result.reason` leaked step IDs/addresses → stable reason codes only (Correction F)
6. Post-phase test `assert call_count >= 1` insufficient → exact authorize count tests (Correction G)
7. Completion Record line counts incorrect → corrected above

**Files modified (corrective):**
- `app/modules/executions/email_worker_coordinator.py` — rewritten (269 lines)
- `app/tests/test_email_worker_coordinator.py` — rewritten (305 lines, 39 tests)
- `tasks/B4e-iii-c-iii-a-email-worker-coordinator.md` — Completion Record corrected
- `tasks/BACKLOG.md` — status restored `[x]`

**Tests/commands run (corrective):**
- API: 901 passed (862 baseline + 39 new), 7 warnings
- Worker: 18 passed (unchanged)
- Frontend build: OK
- Docker compose: valid

**Review outcome (corrective):** Adversarial review 8/8 PASS (gate-rejection stop-all,
post-phase fencing propagation, ConflictError classification, email-only filtering,
reason redaction, before_write typed exceptions, no run/attempt mutation, no
dispatch/actor import). 0 Critical/High/Medium residui.

**Residual limitations:**
- Coordinator is not wired to `worker_start` or dispatch — `B4e-iii-c-iii-b` will
  connect it with atomic terminalization and progress persistence.
- `IMPLEMENTED_REAL_CATEGORIES` still `frozenset({"domains"})` — updated by iii-b.
- C3 remains blocked on `B4e-iii-c-iii-b`.
