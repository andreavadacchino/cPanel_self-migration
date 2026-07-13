# Task B4e-iii-c: Email runtime registry and dispatch (aggregator)

| Field | Value |
|---|---|
| **ID** | `B4e-iii-c` |
| **Status** | `[/]` (retired — split into c-i/c-ii/c-iii) |
| **Priority** | High |
| **Size** | L |
| **Dependencies** | B4e-iii-a, B4e-iii-b, B4e-ii, B4a, B4b-ii, B4c-ii, B4d-ii |
| **Branch** | `feat/b4e-iii-c-email-runtime-registry-dispatch` |

> **Split record (2026-07-14, formalized after B4e-iii-b analysis).** This aggregator is retired
> `[/]` and replaced by three effective sub-tasks, each ≤8 files / ≤500 lines:
>
> - **B4e-iii-c-i — Email registry and evidence resolvers** (dep: B4e-iii-b, B4e-ii, B4a,
>   B4b-ii, B4c-ii, B4d-ii) →
>   [B4e-iii-c-i-email-registry-resolvers.md](B4e-iii-c-i-email-registry-resolvers.md): typed
>   registry and evidence-bound source payload resolvers for all 5 email categories. No gateway,
>   no dispatch wiring, no backup binding.
> - **B4e-iii-c-ii — Destination gateways and durable backup bindings** (dep: B4e-iii-c-i,
>   B4e-iii-a) →
>   [B4e-iii-c-ii-email-gateways-backups.md](B4e-iii-c-ii-email-gateways-backups.md): real
>   destination-only gateway builders, backup store binding for default-address/routing,
>   per-category flag checking. Not wired to worker.
> - **B4e-iii-c-iii — Worker email dispatch and terminal semantics** (dep: B4e-iii-c-ii) →
>   [B4e-iii-c-iii-email-worker-dispatch.md](B4e-iii-c-iii-email-worker-dispatch.md): wires
>   to `worker_start`, authorize per-category/per-write/post-phase, cancellation, terminal
>   semantics, atomic run+attempt commit. Unblocks `C3`.
>
> `C3` now depends on **B4e-iii-c-iii**. Crash/resume recovery stays with **C4**. The
> requirements below are preserved as the umbrella spec the three sub-tasks jointly satisfy.

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
not declare production-ready recovery. Completing **B4e-iii-c-iii** unblocks `C3`.

**Acceptance Criteria (aggregator):**

- [ ] B4e-iii-c-i, B4e-iii-c-ii, B4e-iii-c-iii formalized, each ≤8 files / ≤500 lines,
      landed and verified; `C3` unblocked once B4e-iii-c-iii completes.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```
