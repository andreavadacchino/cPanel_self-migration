# Task A2: Real execution contract

| Field | Value |
|---|---|
| **ID** | `A2` |
| **Status** | `[x]` |
| **Priority** | Critical |
| **Size** | L |
| **Dependencies** | A1 |
| **Branch** | `hotfix/a2-real-execution-contract` |

**Goal:** Add an Alembic-backed real execution/phase/attempt model with legal transitions, immutable evidence references, and redacted audit events.

**Current State:** `ExecutionRun` models preview/mock state but has no explicit real-run state machine, attempt, lease, checkpoint, or compensation contract.

```text
apps/api/app/modules/executions/models.py
```

**Scope:** Modify or create only the focused module above, its nearest tests, and any required schema/migration or adapter contract. Split the task if implementation exceeds eight files or 500 changed lines.

**Implementation:**

1. Define the typed contract and failure states in the named module.
2. Implement the smallest production path behind disabled-by-default configuration.
3. Persist redacted audit evidence and add deterministic tests for success, failure, stale state, and retry.
4. Update V2 documentation with configuration, operational limits, and recovery behavior.

**Testing Requirements:**

- [x] Happy path produces persisted, evidence-bound results. *(A real attempt persists with monotonic number, running status and started_at, bound to the run's immutable snapshot/plan/comparison references.)*
- [x] Failure and stale/ambiguous input fail closed without source mutation. *(`assert_transition` rejects illegal/terminal/unknown transitions; `open_attempt` refuses when real execution is disabled, on a dry-run, or on a terminal run — no adapter/source call is ever made.)*
- [x] Retry is idempotent and secrets are absent from logs/events/API output. *(Retry is a fresh monotonic attempt guarded by the unique constraint; checkpoint/compensation/error/lease_key columns hold only ids and redacted messages, asserted by tests.)*
- [x] New safety-critical code has at least 90% line coverage. *(models.py 100%; new service helpers and cancel fully covered; changed-module total 92%.)*

**Acceptance Criteria:**

- [x] illegal transitions fail; crash/retry state is representable; migrations upgrade and downgrade.
- [x] No new test, typecheck, Compose, or coverage regression.
- [x] Real behavior remains disabled by default until explicitly enabled for an authorized environment.

**Risk & Rollback:** Main risk is an unintended destination mutation or false verification. Keep the feature flag disabled, revert the PR/schema migration if needed, and use only recorded compensation steps; never compensate by mutating the source.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```

## Completion Record

- **Date:** 2026-07-11
- **Summary:** Introduced the real execution contract without enabling any real
  behavior. Added a typed state machine (`ExecutionStatus`, `LEGAL_TRANSITIONS`,
  `assert_transition`) that fails closed on illegal/terminal/unknown transitions
  and now guards every existing dry-run transition and cancellation. Added the
  `ExecutionAttempt` model (Alembic `0008`) making crash/retry/checkpoint/
  compensation/lease state representable: monotonic `attempt_number` unique per
  run, redacted `checkpoint`/`compensation`/`error`/`lease_key`. Added a
  fail-closed `open_attempt`/`finalize_attempt` production path behind the new
  `REAL_EXECUTION_MODE` master switch (default `disabled`).
- **Files changed:**
  - `apps/api/app/modules/executions/models.py` — state machine + `ExecutionAttempt`.
  - `apps/api/app/modules/executions/service.py` — transition guards + attempt helpers.
  - `apps/api/app/core/config.py` — `real_execution_mode` flag + `real_execution_enabled`.
  - `apps/api/alembic/versions/0008_execution_attempts.py` — new table (up/down).
  - `apps/api/app/tests/test_execution_contract.py` — 14 new tests.
  - `README.md`, `.env.example` — contract, flag, states, migration/rollback docs.
- **Tests / commands run:**
  - `apps/api` `pytest` → **131 passed** (was 117; +14 A2 tests).
  - Changed-module coverage → models.py **100%**, service.py 88%, total **92%**.
  - `apps/worker` `DRAMATIQ_TESTING=1 pytest` → **17 passed**.
  - `apps/web` `npm run build` → **OK**; `docker compose config -q` → **OK**.
  - Alembic: programmatic `upgrade head` + `downgrade 0007` in test → table created
    then dropped; `alembic heads` → single head `0008`.
- **Review:** Self-review across correctness, scope (7 files, ~200 lines, under the
  8-file/500-line guardrail), idempotency/retry, source-read-only (no adapter or
  source call anywhere), fail-closed, secret redaction, migration up/down, and
  frontend compatibility (no router/schema/api.ts change). Findings addressed:
  (1) missing env/README docs for the new flag and contract — added;
  (2) `open_attempt` terminal-run branch and `cancel()` uncovered — added tests to
  reach the coverage target. Noted but out of scope: a **pre-existing** Alembic
  model/migration drift on `ix_inventory_migration_role` and `ix_job_events_job_id`
  (inventory/jobs, untouched by A2); `execution_attempts` itself is drift-free.
- **Docs updated:** `README.md` (state machine, real-execution-contract section,
  `execution_attempts` table row), `.env.example` (`REAL_EXECUTION_MODE`).
- **Residual limitations:** Run-level lifecycle transitions for real runs, durable
  dispatch, lease acquisition, safety gates and compensation execution remain
  deliberately unimplemented and are owned by tasks A3, A4, A5 and D3. The
  pre-existing index drift should be reconciled under E1 (quality gates/CI).

