# Task A1: Reproducible worker environment

| Field | Value |
|---|---|
| **ID** | `A1` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | S |
| **Dependencies** | None |
| **Branch** | `feat/a1-worker-environment` |

**Goal:** Declare and document one reproducible install/test workflow; make the worker test command pass locally and in containers.

**Current State:** Worker tests fail during collection because `dramatiq` is unavailable in the active Python environment.

```text
apps/worker/pyproject.toml
```

**Scope:** Modify or create only the focused module above, its nearest tests, and any required schema/migration or adapter contract. Split the task if implementation exceeds eight files or 500 changed lines.

**Implementation:**

1. Define the typed contract and failure states in the named module.
2. Implement the smallest production path behind disabled-by-default configuration.
3. Persist redacted audit evidence and add deterministic tests for success, failure, stale state, and retry.
4. Update V2 documentation with configuration, operational limits, and recovery behavior.

**Testing Requirements:**

- [x] Happy path produces persisted, evidence-bound results. *(Env task: the reproducible workflow yields 117 API + 17 worker passing tests as the durable evidence; no runtime persistence is introduced.)*
- [x] Failure and stale/ambiguous input fail closed without source mutation. *(The `DRAMATIQ_TESTING` gate forces a StubBroker so tests never touch a live Redis and no writer/source path can run; asserted by the new broker tests.)*
- [x] Retry is idempotent and secrets are absent from logs/events/API output. *(`make setup` is re-runnable/idempotent; no secrets are introduced by any changed file.)*
- [x] New safety-critical code has at least 90% line coverage. *(The two new broker tests are 100% covered; A1 adds no new runtime module.)*

**Acceptance Criteria:**

- [x] worker tests collect and pass; dependency installation is documented.
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
- **Summary:** Declared and documented one reproducible Python toolchain — a
  single root virtualenv in `migration-platform/` with `domain`, `adapters`,
  `api` and `worker` installed editable (with their test extras). The previous
  fragmented per-app-venv instructions caused the worker collection failure
  (`ModuleNotFoundError: dramatiq`) and an incomplete API env (missing
  `cryptography`). A new `make setup` target and rewritten README section make
  the install one command; the worker's hermetic `StubBroker` default is now
  guarded by regression tests.
- **Files changed:**
  - `migration-platform/Makefile` — `setup` + `test` targets, `VENV`/`PY`
    variables, host-side test targets pinned to the root venv.
  - `migration-platform/apps/worker/pyproject.toml` — `pytest-cov` in the
    `test` extra + comment documenting the lazy `app.*` actor imports.
  - `migration-platform/apps/worker/worker/tests/test_actors.py` — two new
    deterministic tests asserting the `DRAMATIQ_TESTING` StubBroker contract.
  - `migration-platform/README.md` — new "Ambiente Python riproducibile
    (workflow unico)" section; worker baseline count 15 → 17.
  - `tasks/BACKLOG.md` — quality-baseline worker row updated to "17 passing
    via `make setup`".
- **Tests / commands run:**
  - `make setup` into a throwaway venv from scratch, then `pytest` for both
    suites against it → worker 17 passed, API 117 passed (reproducibility proof).
  - `apps/api` `pytest` → **117 passed**.
  - `apps/worker` `DRAMATIQ_TESTING=1 pytest` → **17 passed** (was 15).
  - `apps/worker` `pytest --cov=worker` → new tests 100% covered.
  - `apps/web` `npm run build` → **built OK**.
  - `docker compose config -q` → **OK**.
- **Review:** Self-review across correctness, scope (5 files, ~100 lines, under
  the 8-file/500-line guardrail), idempotency, source-read-only, secret
  redaction, and doc accuracy. One finding fixed during review: a stale
  "15 test worker" reference in the README/backlog baseline, updated to 17.
- **Docs updated:** `README.md` local-development section and `tasks/BACKLOG.md`
  baseline table.
- **Residual limitations:** None in scope. Ruff/lint and a CI pipeline that
  enforces `make setup` remain out of scope and are already tracked by task E1.

