# Task B4e-iii-c-iii: Worker email dispatch and terminal semantics

| Field | Value |
|---|---|
| **ID** | `B4e-iii-c-iii` |
| **Status** | `[/]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4e-iii-c-ii |
| **Branch** | `feat/b4e-iii-c-iii-email-worker-dispatch` |

**Origin:** third sub-task of the scope split of `B4e-iii-c`. **Retired `[/]`**: split
into `B4e-iii-c-iii-a` (coordinator) and `B4e-iii-c-iii-b` (dispatch wiring +
terminalization). `C3` depends on `B4e-iii-c-iii-b`.

**Goal:** Wire the email registry/gateways/backups to `worker_start`, with authorize
per-category/per-write/post-phase, cancellation, terminal semantics, and atomic run+attempt
commit. Completing this task **unblocks `C3`**. Crash/resume recovery stays with **C4**.

**Acceptance Criteria:**

- [ ] `worker_start` orchestrates 5 email categories with authorize/fencing, durable backups,
      cancellation, explicit terminal semantics (`succeeded`/`halted`/`failed`); disabled by
      default; no source write; mock/dry-run intact; `C3` unblocked.
- [ ] No test, typecheck, Compose, or coverage regression.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```
