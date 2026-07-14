# Task B4e-iii-c-iii-b: Dispatch wiring and atomic terminalization

| Field | Value |
|---|---|
| **ID** | `B4e-iii-c-iii-b` |
| **Status** | `[~]` |
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

**Initial commit measurement (4becb0e):** 20 files, 477 ins / 154 del = 631 modified.
Guardrail deviation: 20 files (limit 8), 631 lines (limit 500).

## Correction Plan

- **R1**: Atomic persistence, cancellation safety, compensation preservation.
- **R2**: Global completeness, domain ordering/pending, lifecycle, end-to-end.

## Correction Record R1

**Date:** 2026-07-14

**Issues corrected:**
1. `finalize_terminal` now fresh-reads run/attempt with `SELECT FOR UPDATE`, validates
   status, fencing, and rolls back on any error. Concurrent cancellation detected →
   attempt cancelled, run preserved, checkpoint/compensation kept.
2. `make_progress_persister` validates fresh run/attempt status, fencing token match,
   checkpoint shape (categories/step IDs/reason codes), compensation forbidden keys
   (recursive scan). Rejects oversized payloads. Rolls back on any failure.
3. All terminal branches in `worker_start` now pass compensation via `_comp()` helper —
   domain failure, email failure, cancellation all preserve backup refs.
4. Cancellation branch uses `finalize_terminal` (fresh-aware) instead of raw
   `service.finalize_attempt`, preserving existing checkpoint on concurrent cancel.

**Files modified (R1):**
- `dispatch_terminal.py` — rewritten (204 lines): fresh reads, validation, rollback
- `dispatch.py` — 357 lines: all terminal branches via `finalize_terminal` + `_comp()`
- `test_dispatch_email_wiring.py` — 346 lines (25 tests): R1 atomicity/progress/compensation
- `tasks/B4e-iii-c-iii-b-*` — status `[~]`, Correction Record R1
- `tasks/BACKLOG.md` — status `[~]`

**Tests/commands run (R1):**
- API: 926 passed (862 baseline + 39 coordinator + 25 wiring), 0 failed
- Worker: 18 passed
- Frontend build: OK
- Compose: valid

**R1 budget:** 5 files, 313 ins / 68 del = 381 modified. Within 8/500.

## Correction Record R1-bis

**Date:** 2026-07-14

**Issues corrected:**
1. `_fresh_read` used ORM `select(ExecutionRun)` returning identity-map cached objects
   → replaced with scalar column queries (`select(Run.id, Run.status, ...)`) that
   bypass the identity map and always return persisted DB state.
2. `_validate_checkpoint` accepted arbitrary top-level keys, missing required keys,
   empty entries, unvalidated completed lists → extracted to `dispatch_validation.py`
   with exact-shape enforcement: required keys, EMAIL_CATEGORIES whitelist, reason
   code whitelist, step-id union check, duplicate detection.
3. `json.dumps(default=str)` silently serialized non-JSON types → `assert_strict_json`
   recursive type checker: only None/bool/int/float(finite)/str/list/dict allowed.
4. `_validate_compensation` only checked forbidden keys → per-category schema with
   allowed-key sets derived from actual `compensation_*` functions, backup_ref
   restricted to default_address/routing, scalar-only values enforced.
5. `_merge_compensation` used `id()` dedup → canonical JSON dedup with `json.dumps
   (sort_keys=True)`, deep-copy, shape-conflict detection.
6. Terminal checkpoints not validated → `validate_terminal_checkpoint` with closed
   key set, forbidden-key scan, applied before `finalize_attempt`.
7. `finalize_terminal(terminal=cancelled)` on a fresh-running run → rejected with
   ConflictError; cancellation accepted only when DB already shows cancelled.

**Files modified (R1-bis):**
- `dispatch_validation.py` — NEW (188 lines): strict JSON, checkpoint/compensation/terminal validators
- `dispatch_terminal.py` — rewritten (172 lines): scalar column fresh reads, deterministic merge
- `test_dispatch_email_wiring.py` — 401 lines (39 tests): parametrized validation tests
- `tasks/B4e-iii-c-iii-b-*` — Correction Record R1-bis

**Tests/commands run (R1-bis):**
- API: 940 passed, 0 failed
- Worker: 18 passed
- Frontend build: OK
- Compose: valid

**R1-bis budget:** 4 files, 188 new + 175 ins / 152 del = ~363 net. Within 6/500.

**Residual for R2:**
- domain_result.pending does not block email execution
- No exact selected-step completeness check before succeeded
- Domain before_write does not detect fresh cancellation
- Domain client not closed on exception path
- Crash/resume of `running` attempts stays with C4
- Routing registered but not executable (needs_contract_test)
- Not production-ready: E1/E2/E3 pending
