# Task B4e-iii-c-iii-b: Dispatch wiring and atomic terminalization

| Field | Value |
|---|---|
| **ID** | `B4e-iii-c-iii-b` |
| **Status** | `[ ]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4e-iii-c-iii-a |
| **Branch** | `feat/b4e-iii-c-iii-email-worker-dispatch` |

**Origin:** second sub-task of the scope split of `B4e-iii-c-iii`.

**Goal:** Wire the email coordinator into `worker_start` with: the progress callback
bound to attempt persistence, atomic run+attempt terminal commit (succeeded/halted/failed),
`IMPLEMENTED_REAL_CATEGORIES` updated to include all 5 email categories, `C3` unblocked.
Crash/resume of `running` attempts stays with `C4`.

**Acceptance Criteria:**

- [ ] `worker_start` calls the email coordinator after domains, wires the progress
      callback to attempt checkpoint/compensation persistence, commits run+attempt
      atomically via `finalize_attempt`, explicit terminal semantics
      (succeeded/halted/failed), disabled by default, no source write, mock/dry-run
      intact, `C3` unblocked.
- [ ] No test, typecheck, Compose, or coverage regression.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```
