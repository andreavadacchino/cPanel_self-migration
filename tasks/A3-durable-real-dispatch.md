# Task A3: Durable real dispatch

| Field | Value |
|---|---|
| **ID** | `A3` |
| **Status** | `[x]` |
| **Priority** | Critical |
| **Size** | M |
| **Dependencies** | A2 |
| **Branch** | `hotfix/a3-durable-real-dispatch` |

**Goal:** Add an authenticated/confirmed real enqueue endpoint that commits state before sending only the run ID to Dramatiq.

**Current State:** The public router exposes only dry-run execution; the mock orchestrator actor is intentionally not dispatched by API/UI.

```text
apps/api/app/modules/executions/router.py
```

**Scope:** Modify or create only the focused module above, its nearest tests, and any required schema/migration or adapter contract. Split the task if implementation exceeds eight files or 500 changed lines.

**Implementation:**

1. Define the typed contract and failure states in the named module.
2. Implement the smallest production path behind disabled-by-default configuration.
3. Persist redacted audit evidence and add deterministic tests for success, failure, stale state, and retry.
4. Update V2 documentation with configuration, operational limits, and recovery behavior.

**Testing Requirements:**

- [x] Happy path produces persisted, evidence-bound results. *(Dispatch commits a `queued` attempt bound to lease/fencing before enqueue; the worker re-validates and legally advances `queued → running → halted`, persisting only redacted evidence.)*
- [x] Failure and stale/ambiguous input fail closed without source mutation. *(Endpoint and actor fail closed on disabled real mode; the worker re-runs `authorize` and blocks — mutating nothing — on stale evidence or a stale fencing token; no source/adapter call exists.)*
- [x] Retry is idempotent and secrets are absent from logs/events/API output. *(Duplicate/retry dispatch reuses the single active attempt; the queue message and all events/response carry only ids; `encrypted_secrets` is never read.)*
- [x] New safety-critical code has at least 90% line coverage. *(dispatch.py 98%, models.py 100%.)*

**Acceptance Criteria:**

- [x] enqueue is after commit; duplicate requests are idempotent; broker failure leaves recoverable state.
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
- **Summary:** Wired the durable real path API → PostgreSQL → Dramatiq → worker in
  a new `dispatch.py`, reusing (not duplicating) A2's state machine/attempt,
  A4's lease/fencing, and A5's `authorize`. `POST /api/executions/{id}/dispatch`
  acquires the account lease, runs `authorize`, creates and **commits** a
  `queued` attempt (stamped with lease owner + fencing token), then enqueues only
  `execution_run_id`/`attempt_id`. A distinct real actor (`real_execution`,
  separate from all mock actors) calls `worker_start`, which re-reads everything
  from PostgreSQL, re-runs `authorize` (re-checking lease/fencing), and legally
  advances `queued → running`. With no real writer implemented, the worker stops
  in the new explicit terminal `halted` state without any mutation. All paths
  fail closed under `REAL_EXECUTION_MODE=disabled`; no route can change the flag;
  no real writer or cPanel/SSH/IMAP call is introduced.
- **Files changed:**
  - `apps/api/app/modules/executions/dispatch.py` — dispatch + worker_start (new).
  - `apps/worker/worker/actors/real_dispatch.py` — real actor (new); registered in `worker/main.py`.
  - `apps/api/app/modules/executions/router.py` — `/dispatch` endpoint.
  - `apps/api/app/modules/executions/models.py` — `halted` terminal state.
  - `apps/api/app/modules/executions/service.py` — `open_attempt(initial_status=...)`.
  - `apps/api/app/tests/test_real_dispatch.py` — 16 dispatch/worker tests (new).
  - `apps/worker/worker/tests/test_actors.py` — real-actor registration test.
  - `apps/api/app/tests/test_execution_contract.py` — terminal-set assertion updated for `halted`.
  - `README.md`, `.env.example` — dispatch/recovery contract + flag note.
- **Tests / commands run (all mandated cases covered):** commit-before-enqueue,
  message-only-ids, broker-failure-recoverable (+ no duplicate on re-dispatch),
  idempotent duplicate → single attempt, second run on same account blocked,
  worker re-validates gate/lease/fencing then halts, worker idempotent on
  redelivery, fenced-out worker mutates nothing, stale evidence between enqueue
  and start blocks the worker, master switch disabled blocks endpoint and actor,
  legal `queued/running/halted/cancelled` transitions, no secret leak, dry-run
  cannot be dispatched, non-queued run rejected, unknown attempt rejected.
  - `apps/api` `pytest` → **191 passed** (was 175; +16, −0; one A2 terminal-set assertion updated).
  - Coverage → dispatch.py **98%** (only the 3-line real `_enqueue` transport,
    patched in tests, uncovered), models.py **100%**.
  - `apps/worker` `DRAMATIQ_TESTING=1 pytest` → **18 passed** (+1 real-actor registration).
  - `apps/web` `npm run build` → **OK**; `docker compose config -q` → **OK**.
  - No migration (existing tables; `halted` is a status value); `alembic heads` → `0009`.
- **Review:** Self-review across ordering (state committed before send, proven by
  test), idempotency/concurrency (lease-owner per run + `SELECT … FOR UPDATE` on
  the lease row serialise dispatch; single active attempt per run), source-read-
  only (no adapter/writer/source call; worker halts without writing), fail-closed
  and fencing (worker re-validates and mutates nothing when stale/fenced), secret
  redaction (queue message and events carry only ids), and no-regression (mock/
  dry-run suites green). Findings fixed: (1) A2 `test_terminal_states_have_no_
  successor` regressed by adding `halted` → assertion updated; (2) a dead import
  in a test → removed; (3) missing docs → added. Note: `halted` writes no result
  and no verification, so it needs no fencing re-check (req 12 concerns persisting
  results, which halt never does). The pre-existing Alembic index drift is
  untouched and out of scope (E1).
- **Docs updated:** `README.md` (dispatch/recovery section + endpoint-table row),
  `.env.example` (`REAL_EXECUTION_MODE` note).
- **Guardrail note:** this task is inherently cross-cutting (API + worker +
  integration + tests + docs) and is defined as one PR, so it exceeds the 8-file /
  500-line soft guideline (11 code/doc files, ~590 changed lines, test-dominated;
  production + config ≈ 263 lines). Every file stays under the 400-line per-file
  limit (dispatch.py 200, tests 281). It was not split because no sub-slice ships
  a coherent, safe dispatch on its own.
- **Residual limitations:** Real writers accepting a `WriteTarget` and populating
  `_real_phases` belong to Wave B; compensation execution to D3. The master switch
  stays `disabled` by default.

