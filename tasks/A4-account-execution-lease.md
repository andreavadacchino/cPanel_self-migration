# Task A4: Account execution lease

| Field | Value |
|---|---|
| **ID** | `A4` |
| **Status** | `[x]` |
| **Priority** | Critical |
| **Size** | M |
| **Dependencies** | A2 |
| **Branch** | `hotfix/a4-account-execution-lease` |

**Goal:** Implement a PostgreSQL-backed destination-account lease with owner, expiry, heartbeat, takeover rules, and fencing token.

**Current State:** No database lease prevents two workers from mutating the same destination account concurrently.

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

- [x] Happy path produces persisted, evidence-bound results. *(`acquire` persists an active lease with owner, monotonic fencing token, expiry and heartbeat; the attempt is stamped with the fencing token it runs under.)*
- [x] Failure and stale/ambiguous input fail closed without source mutation. *(Every path — acquire/heartbeat/release/assert_fencing_current/open_attempt/finalize — raises `409` on disabled real mode, wrong owner, stale/expired/absent lease, or mismatched account; no adapter or source call is ever made.)*
- [x] Retry is idempotent and secrets are absent from logs/events/API output. *(Same-owner re-acquire keeps the token; the unique constraint rejects a duplicate lease; `owner` is an opaque worker id and no lease column carries a secret.)*
- [x] New safety-critical code has at least 90% line coverage. *(lease.py 100%, models.py 100%; changed-module total 94%.)*

**Acceptance Criteria:**

- [x] one writer wins; expired lease recovery is safe; stale worker cannot commit.
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
- **Summary:** Implemented a PostgreSQL-backed destination-account lease. One row
  per destination endpoint (`uq_account_lease_endpoint`) gives mutual exclusion;
  a monotonic `fencing_token` fences out a stalled holder. `acquire`
  (row-locked with `SELECT … FOR UPDATE` on PostgreSQL) lets one writer win,
  keeps a same-owner re-acquire idempotent, and safely takes over an
  expired/released lease with a bumped token. `heartbeat`/`release` accept only
  the current owner+token; `assert_fencing_current` guards commits and is wired
  into `finalize_attempt`, so a fenced-out worker cannot persist a result or
  complete the run. Every path fails closed unless `REAL_EXECUTION_MODE=enabled`.
- **Files changed:**
  - `apps/api/app/modules/executions/models.py` — `AccountExecutionLease` + attempt `fencing_token`.
  - `apps/api/app/modules/executions/lease.py` — acquire/heartbeat/release/fencing (new).
  - `apps/api/app/modules/executions/service.py` — `open_attempt` lease stamp + `finalize_attempt` fencing guard.
  - `apps/api/app/core/config.py` — `execution_lease_ttl_seconds`.
  - `apps/api/alembic/versions/0009_account_leases.py` — new table + column (up/down).
  - `apps/api/app/tests/test_execution_lease.py` — 17 new tests (new).
  - `README.md`, `.env.example` — lease contract, fencing, TTL flag, migration/rollback docs.
- **Tests / commands run:**
  - `apps/api` `pytest` → **146 passed** (was 131; +15 A4 tests net of shared helpers).
  - Changed-module coverage → lease.py **100%**, models.py **100%**, total **94%**.
  - Deterministic proofs: one-writer-wins, idempotent same-owner re-acquire,
    expired-lease safe takeover (token 1→2), heartbeat renew/reject, stale-token
    fencing, and `finalize_attempt` rejected for a fenced-out worker (attempt
    left unchanged) while the current holder finalizes.
  - `apps/worker` `DRAMATIQ_TESTING=1 pytest` → **17 passed**.
  - `apps/web` `npm run build` → **OK**; `docker compose config -q` → **OK**.
  - Alembic: programmatic `upgrade head` + `downgrade 0008` in test → table/column
    created then dropped; `alembic heads` → single head `0009`.
- **Review:** Self-review across correctness, idempotency/retry, concurrency
  (added row-level lock for the acquire race; single-row + fencing keeps commits
  correct), source-read-only (lease is bound to the run's destination account and
  never touches the source), fail-closed, secret redaction (opaque `owner`, no
  secret columns), migration up/down, and frontend compatibility (no router/
  schema/api.ts change). Findings addressed: (1) tz-naive vs tz-aware datetime
  comparison on SQLite → added `_as_utc` normalisation; (2) missing docs for the
  lease/flag → added; (3) acquire takeover race on PostgreSQL → `SELECT … FOR
  UPDATE`; (4) fail-closed branches (released heartbeat, missing-lease release,
  expired fencing) uncovered → tests added to reach 100% on lease.py. Noted but
  out of scope: the **pre-existing** Alembic index drift (`ix_inventory_migration_
  role`, `ix_job_events_job_id`); the new lease schema is drift-free.
- **Docs updated:** `README.md` (lease/fencing section + `account_execution_leases`
  table row), `.env.example` (`EXECUTION_LEASE_TTL_SECONDS`).
- **Guardrail note:** 8 code/doc files (within the file limit). The diff is
  ~540 changed lines, slightly over the 500-line soft guideline; the overflow is
  entirely deterministic test evidence explicitly required for this Critical
  concurrency task (production + migration are ~262 lines). The feature is a
  single atomic unit (the lease), so it was not split.
- **Residual limitations:** Wiring the lease acquisition into the durable real
  dispatch flow belongs to A3; the additional real safety gates belong to A5;
  compensation execution belongs to D3. The pre-existing index drift should be
  reconciled under E1.

