# Task B4e-iii-c: Email runtime registry and dispatch

| Field | Value |
|---|---|
| **ID** | `B4e-iii-c` |
| **Status** | `[ ]` |
| **Priority** | High |
| **Size** | L |
| **Dependencies** | B4e-iii-a, B4e-iii-b, B4e-ii, B4a, B4b-ii, B4c-ii, B4d-ii |
| **Branch** | `feat/b4e-iii-c-email-runtime-registry-dispatch` |

**Origin:** third sub-task of the scope split of `B4e-iii` (see
[B4e-iii-email-dispatch-integration.md](B4e-iii-email-dispatch-integration.md), split record).

**Goal:** A uniform per-category engine registry that connects forwarder (B4a), default-address
(B4b-ii), routing (B4c-ii), filters (B4d-ii) and autoresponder (B4e-ii) to the **real worker**,
consuming the iii-b evidence-bound categories and the iii-a durable backup store.

**Requirements (uniform dispatch):** register only categories with a completed engine AND
evidence contract; no generic `email` category; map exact plan/preview category IDs to engines;
re-validate `authorize()`/lease/fencing before each category, via `before_write` before each
write, and after the phase before commit; build gateways from the destination only; load source
payloads only from evidence-bound snapshots; never put sensitive email payload in run/attempt/
event/checkpoint/public-compensation; persist protected backups for default-address/routing
**before** the write through the iii-a store; per-category/item checkpoints; never re-run a
verified item without a fresh-read; explicit terminal-state semantics (all selected verified →
`succeeded`; any manual/blocked/unsupported → `halted`/`failed`; unimplemented categories →
never `succeeded`; a mixed completed/pending run is never a success); atomic run+attempt commit;
a fenced-out worker persists nothing; a stale confirmation/evidence between categories stops the
next one; cancellation prevents further writes; every category flag + master switch exact-match
enabled; disabled by default; no source write; no real contact in tests; mock/dry-run intact.

**Notes:** routing needs an evidence-bound `RoutingSetPolicy` source (empty by default → every
set blocked); provisioning policies is out of scope (routing stays inert until a policy source
exists — record as an open limitation). Crash/resume recovery stays with **C4**; this task must
not declare production-ready recovery. Completing this task **unblocks `C3`**.

**Acceptance Criteria:**

- [ ] A uniform registry dispatches the wired email categories under per-category/per-write/
      post-phase authorize+lease+fencing, atomic run+attempt commit, durable backups for
      default-address/routing, and explicit terminal semantics; disabled by default; no source
      write; mock/dry-run intact.
- [ ] No test, typecheck, Compose, or coverage regression; `C3` unblocked.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```
