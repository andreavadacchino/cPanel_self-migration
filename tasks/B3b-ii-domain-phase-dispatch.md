# Task B3b-ii: Domain phase dispatch wiring

| Field | Value |
|---|---|
| **ID** | `B3b-ii` |
| **Status** | `[ ]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B3b-i |
| **Branch** | `feat/b3b-ii-domain-phase-dispatch` |

**Origin:** second half of the split of `B3b` (see
[B3b-real-domain-writer-dispatch.md](B3b-real-domain-writer-dispatch.md)). B3b-ii
wires the B3b-i engine into the real dispatch/actor behind the double gate, adding
the lease/fencing/authorize integration and the terminal-state selection.

> A local patch with the drafted wiring (from the pre-split implementation) is
> saved in the session scratchpad as `b3b-ii-wiring.patch` (~242 lines touching
> `dispatch.py` and `config.py`); use it as a starting reference.

**Scope:**

- `apps/api/app/modules/executions/dispatch.py` — `_executable_categories`,
  gateway factory, `_run_domain_phase`, terminal-state selection in
  `worker_start`; keep the halt path for non-executable runs.
- `apps/api/app/core/config.py` — `domain_real_writer_enabled` double-gate property.
- `apps/api/app/tests/test_real_dispatch.py` — integration tests.
- `migration-platform/README.md`, `.env.example` — flags and operational docs.

**Implementation:**

1. Double gate: a real create is reachable only when `REAL_EXECUTION_MODE=enabled`
   AND `DOMAIN_WRITER_MODE=enabled`; both default disabled.
2. `authorize()`/`WriteTarget`/lease/fencing before the phase, before the
   fresh-read, immediately before each write (via the engine's `before_write`
   hook), and after the write before persisting (via `finalize_attempt`, which
   re-checks fencing).
3. Terminal-state selection: solo eligible domains → `succeeded`; only
   unimplemented categories → `halted`; mixed / manual-pending → `halted`
   (never `succeeded` while selected categories remain unexecuted); hard failure
   → `failed`; fenced-out after write → no success persisted.
4. `ExecutionAttempt` checkpoint + legal transitions; crash/retry does not
   duplicate the domain (fresh read → `already_present`).

**Testing Requirements:**

- [ ] Real/domain flag disabled; source rejected as target.
- [ ] Solo-domains run does not halt; only-unimplemented run halts; mixed run is
      not falsely fully successful.
- [ ] Gate or lease expired before the write; fencing lost after the write → no
      success; stale confirmation/evidence between dispatch and phase.
- [ ] Crash/retry idempotency; compensation metadata persisted and redacted; no
      secret in events; mock writer and dry-run do not regress.

**Acceptance Criteria:**

- [ ] Real domain writer reachable only under both flags; source structurally
      unusable as a write target; a fenced-out worker cannot record success.
- [ ] No new test, typecheck, Compose, or coverage regression.
- [ ] Real behavior disabled by default.

**Risk & Rollback:** Main risk is an unintended destination mutation or false
verification. Keep both flags disabled, revert the PR if needed, and use only
recorded compensation steps; never compensate by mutating the source.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```
