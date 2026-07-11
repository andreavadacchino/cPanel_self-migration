# Task A2: Real execution contract

| Field | Value |
|---|---|
| **ID** | `A2` |
| **Status** | `[ ]` |
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

- [ ] Happy path produces persisted, evidence-bound results.
- [ ] Failure and stale/ambiguous input fail closed without source mutation.
- [ ] Retry is idempotent and secrets are absent from logs/events/API output.
- [ ] New safety-critical code has at least 90% line coverage.

**Acceptance Criteria:**

- [ ] illegal transitions fail; crash/retry state is representable; migrations upgrade and downgrade.
- [ ] No new test, typecheck, Compose, or coverage regression.
- [ ] Real behavior remains disabled by default until explicitly enabled for an authorized environment.

**Risk & Rollback:** Main risk is an unintended destination mutation or false verification. Keep the feature flag disabled, revert the PR/schema migration if needed, and use only recorded compensation steps; never compensate by mutating the source.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```

