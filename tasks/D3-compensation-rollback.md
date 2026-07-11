# Task D3: Compensation and rollback

| Field | Value |
|---|---|
| **ID** | `D3` |
| **Status** | `[ ]` |
| **Priority** | Medium |
| **Size** | L |
| **Dependencies** | D1 |
| **Branch** | `feat/d3-compensation-rollback` |

**Goal:** Persist compensation actions and encrypted/protected backup references; expose operator-triggered rollback for supported additive writes.

**Current State:** Mock orchestration explicitly performs no compensation and real backup artifacts are absent.

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

- [ ] rollback requires confirmation and fresh-read; partial rollback is reported honestly; destructive cleanup stays manual.
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

