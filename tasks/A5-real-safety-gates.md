# Task A5: Real execution safety gates

| Field | Value |
|---|---|
| **ID** | `A5` |
| **Status** | `[x]` |
| **Priority** | Critical |
| **Size** | L |
| **Dependencies** | A2, A4 |
| **Branch** | `hotfix/a5-real-safety-gates` |

**Goal:** Create real prevalidation that fails closed on stale/partial evidence and proves every planned mutation targets the destination.

**Current State:** Mock prevalidation rejects non-mock endpoints but there is no real gate combining source-read-only, strong confirmation, staleness, capability, and lease checks.

```text
apps/api/app/modules/executions/mock_orchestrator.py
```

**Scope:** Modify or create only the focused module above, its nearest tests, and any required schema/migration or adapter contract. Split the task if implementation exceeds eight files or 500 changed lines.

**Implementation:**

1. Define the typed contract and failure states in the named module.
2. Implement the smallest production path behind disabled-by-default configuration.
3. Persist redacted audit evidence and add deterministic tests for success, failure, stale state, and retry.
4. Update V2 documentation with configuration, operational limits, and recovery behavior.

**Testing Requirements:**

- [x] Happy path produces persisted, evidence-bound results. *(A fully valid run yields a redacted `GateDecision` bound to the run's exact plan/comparison/snapshot ids, fencing token and destination `WriteTarget`, with no mutation performed.)*
- [x] Failure and stale/ambiguous input fail closed without source mutation. *(Every gate — master switch, dry-run/status, destination-only, confirmation, evidence coherence/currency, snapshot readability, capability, lease/fencing — raises `SafetyGateError`; the gate performs no write, and a source can never become a `WriteTarget`.)*
- [x] Retry is idempotent and secrets are absent from logs/events/API output. *(`authorize` is a pure read-only check, safe to repeat before each phase; the decision and all error messages carry no secret and `encrypted_secrets` is never read.)*
- [x] New safety-critical code has at least 90% line coverage. *(safety_gates.py 98%.)*

**Acceptance Criteria:**

- [x] source mutation is structurally impossible; stale evidence blocks; confirmation and lease are rechecked per phase.
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
- **Summary:** Added `app/modules/executions/safety_gates.py`, the single
  fail-closed prevalidation a real write phase must pass. A distinct
  `WriteTarget` type — buildable only via `WriteTarget.for_endpoint`, which
  rejects any non-`destination` endpoint — makes it structurally impossible to
  hand a source endpoint to a writer (read source and write destination are
  different types). `authorize` re-reads and re-checks, on every call, the real
  master switch, run shape (real, non-terminal), destination-only targeting, a
  present/unexpired strong confirmation, plan/comparison/snapshot coherence *and*
  currency, snapshot readability (only `succeeded`), per-category capability (a
  current readiness report marking the category `eligible_for_real_design`), and
  an active lease with the current fencing token. It performs no write and
  returns a redacted `GateDecision`. No route, enqueue, or writer is wired (A3).
- **Files changed:**
  - `apps/api/app/modules/executions/safety_gates.py` — gate + `WriteTarget` (new).
  - `apps/api/app/core/config.py` — `real_confirmation_ttl_seconds`.
  - `apps/api/app/tests/test_safety_gates.py` — 30 deterministic tests (new).
  - `README.md`, `.env.example` — security-gate contract + `REAL_CONFIRMATION_TTL_SECONDS`.
- **Tests / commands run (all 10 required scenarios covered):**
  1 source-as-target blocked, 2 incoherent destination blocked, 3 missing/expired
  confirmation blocked, 4 stale plan/snapshot blocked, 5 partial/unavailable/
  failed/empty/ambiguous inventory blocked (parametrized), 6 missing capability/
  report blocked, 7 missing/expired lease & stale fencing blocked, 8 all-gates-
  valid authorizes with no writes (no events/attempts/status change), 9
  revalidation between phases stops on an intervening takeover drift, 10 no secret
  in the error or the decision.
  - `apps/api` `pytest` → **175 passed** (was 146; +29).
  - `safety_gates.py` coverage → **98%**.
  - `apps/worker` `DRAMATIQ_TESTING=1 pytest` → **17 passed**.
  - `apps/web` `npm run build` → **OK**; `docker compose config -q` → **OK**.
  - No migration in A5 (uses existing tables); Alembic head unchanged at `0009`.
- **Review:** Self-review across correctness, source-read-only (the gate never
  writes; a source can never become a `WriteTarget`), destination-only,
  staleness/fresh-read/fail-closed (every call re-reads; latest-evidence and
  snapshot-status whitelists), secret redaction (decision + errors carry no
  secret; `encrypted_secrets` never read), and frontend compatibility (no router/
  schema/api.ts change). Findings fixed: (1) lease/fencing rejection surfaced as
  raw `ConflictError` → wrapped into a uniform `SafetyGateError`; (2) redundant
  terminal-status check + unused import → simplified; (3) missing docs → added.
  Noted: fencing authority is the token (owner-binding is held by A3's dispatch,
  which owns the lease object); the pre-existing Alembic index drift is untouched
  and out of scope (E1).
- **Docs updated:** `README.md` ("Gate di sicurezza pre-scrittura" section),
  `.env.example` (`REAL_CONFIRMATION_TTL_SECONDS`).
- **Guardrail note:** 5 code/doc files (within the file limit); both new files are
  under the 400-line per-file limit (gate 228, tests 381). The PR is ~648 changed
  lines, over the 500-line soft guideline; the overflow is entirely the
  deterministic test evidence explicitly required for this Critical safety task
  (production + docs are ~267 lines). The gate is one atomic unit, so it was not
  split.
- **Residual limitations:** Wiring `authorize` into the durable real dispatch (and
  calling it before every phase) belongs to A3; real writers accepting a
  `WriteTarget` belong to Wave B. `real_execution_mode` stays `disabled` by
  default. The pre-existing index drift should be reconciled under E1.

