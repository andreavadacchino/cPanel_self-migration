# Task B4e-iii-c-iii-b: Dispatch wiring and atomic terminalization

| Field | Value |
|---|---|
| **ID** | `B4e-iii-c-iii-b` |
| **Status** | `[x]` |
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

- [x] `worker_start` calls the email coordinator after domains, wires the progress
      callback to attempt checkpoint/compensation persistence, commits run+attempt
      atomically via `finalize_attempt`, explicit terminal semantics
      (succeeded/halted/failed), disabled by default, no source write, mock/dry-run
      intact, `C3` unblocked.
- [x] No test, typecheck, Compose, or coverage regression.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```

## Completion Record

**Date:** 2026-07-14

**Implementation summary:** Wired the email coordinator into `worker_start` with
atomic terminalization. Key changes:

- **`IMPLEMENTED_REAL_CATEGORIES`** updated to 6 categories (domains + 5 email).
- **`_executable_categories()`** refactored: domains via `domain_real_writer_enabled`,
  email via per-category `is_category_enabled()` from c-ii. Disabled = not executable.
- **`worker_start()`** refactored: runs domains (if executable) then email coordinator
  (if email executable and domains ok). Domain failure stops email. Email cancellation
  (fresh run status = cancelled) finalizes attempt to cancelled without overwriting run.
  ConflictError from coordinator (fencing loss) triggers rollback and propagation.
- **`dispatch_terminal.py`** NEW: extracted `finalize_terminal()` (generalized atomic
  terminal) and `make_progress_persister()` (fencing-checked progress callback).
  Keeps dispatch.py at 368 lines (≤400).
- **Defense-in-depth**: explicit fresh-status re-read before final `finalize_terminal`
  prevents overwriting a run already cancelled by the API.
- **Regression test updates**: 16 tests across 13 files updated for the new
  `IMPLEMENTED_REAL_CATEGORIES` value and checkpoint format.

**Files modified:**
- `app/modules/executions/dispatch.py` — refactored (368 lines, was 404)
- `app/modules/executions/dispatch_terminal.py` — NEW (62 lines)
- `app/tests/test_dispatch_email_wiring.py` — NEW (15 tests)
- 13 existing test files — regression updates (IMPL assertions, checkpoint format)
- `tasks/B4e-iii-c-iii-b-dispatch-wiring-terminalization.md` — status `[x]`
- `tasks/BACKLOG.md` — status `[x]`

**Tests/commands run:**
- API: 916 passed (862 baseline + 39 coordinator + 15 wiring), 7 warnings
- Worker: 18 passed (unchanged)
- Frontend build: OK
- Docker compose: valid

**Review outcome:** Adversarial review 12/12 PASS + 1 MEDIUM hardening applied
(explicit fresh-status re-check before finalize_terminal).

**Residual limitations:**
- Crash/resume of `running` attempts stays with C4.
- Routing is registered but not executable until readiness changes from
  `needs_contract_test` — no policy provisioning in scope.
- Not production-ready: E1/E2/E3 still pending.
