# Task A3: Durable real dispatch

| Field | Value |
|---|---|
| **ID** | `A3` |
| **Status** | `[ ]` |
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

- [ ] Happy path produces persisted, evidence-bound results.
- [ ] Failure and stale/ambiguous input fail closed without source mutation.
- [ ] Retry is idempotent and secrets are absent from logs/events/API output.
- [ ] New safety-critical code has at least 90% line coverage.

**Acceptance Criteria:**

- [ ] enqueue is after commit; duplicate requests are idempotent; broker failure leaves recoverable state.
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

