# Task A5: Real execution safety gates

| Field | Value |
|---|---|
| **ID** | `A5` |
| **Status** | `[ ]` |
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

- [ ] Happy path produces persisted, evidence-bound results.
- [ ] Failure and stale/ambiguous input fail closed without source mutation.
- [ ] Retry is idempotent and secrets are absent from logs/events/API output.
- [ ] New safety-critical code has at least 90% line coverage.

**Acceptance Criteria:**

- [ ] source mutation is structurally impossible; stale evidence blocks; confirmation and lease are rechecked per phase.
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

