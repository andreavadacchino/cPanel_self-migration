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

**Implementation summary:** Created `email_worker_coordinator.py` (160 lines) with
`EmailCoordinationResult` dataclass and `coordinate_email_categories()` function. The
coordinator: derives categories and step IDs from the run's preview in plan order with
dedup; checks fresh persisted run status (no-autoflush, column-only query) before each
category; validates per-category authorize/fencing; loads source/destination snapshots
with role verification; calls c-i `resolve_category()` then c-ii `run_email_category()`
with an injected `before_write` callback (fresh status + scoped authorize + fencing);
performs post-phase fencing-only check (no full authorize); invokes injected
`persist_progress` callback only after successful fencing; aggregates results with
fail-closed semantics (failure stops subsequent categories, pending never produces
success, disabled/unknown categories appear in result). Routing is handled by the
existing authorize gate rejection (`needs_contract_test` readiness) — result is
pending/blocked, never a false skip-success.

**Files modified:**
- `app/modules/executions/email_worker_coordinator.py` — NEW (160 lines)
- `app/tests/test_email_worker_coordinator.py` — NEW (29 tests)
- `tasks/B4e-iii-c-iii-a-email-worker-coordinator.md` — NEW task file
- `tasks/B4e-iii-c-iii-b-dispatch-wiring-terminalization.md` — NEW task file
- `tasks/B4e-iii-c-iii-email-worker-dispatch.md` — status → `[/]`
- `tasks/BACKLOG.md` — split formalized, C3 dep → iii-b, graph updated

**Tests/commands run:**
- API: 891 passed (862 baseline + 29 new), 7 warnings
- Worker: 18 passed (unchanged)
- Frontend build: OK (352ms)
- Docker compose: valid

**Review outcome:** Adversarial review 11/11 PASS (identity-map stale, autoflush,
disabled ignored, resolver mismatch, full authorize post-write, fenced-out progress,
checkpoint sensitive, categories after failure/cancel, routing false success, mutation
run/attempt, import dispatch/actor).

**Residual limitations:**
- Coordinator is not wired to `worker_start` or dispatch — `B4e-iii-c-iii-b` will
  connect it with atomic terminalization and progress persistence.
- `IMPLEMENTED_REAL_CATEGORIES` still `frozenset({"domains"})` — updated by iii-b.
- C3 remains blocked on `B4e-iii-c-iii-b`.
