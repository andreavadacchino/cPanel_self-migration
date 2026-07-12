# Task B4e-iii: Email phases pipeline and dispatch integration (aggregator)

| Field | Value |
|---|---|
| **ID** | `B4e-iii` |
| **Status** | `[/]` (retired — split into iii-a/b/c) |
| **Priority** | High |
| **Size** | L |
| **Dependencies** | B4e-ii, B4a, B4b-ii, B4c-ii, B4d-ii |
| **Branch** | `feat/b4e-iii-email-dispatch-integration` |

> **Split record (2026-07-12, formalized after B4e-ii).** This aggregator is retired `[/]` and
> replaced by three effective sub-tasks, each ≤8 files / ≤500 lines:
>
> - **B4e-iii-a — Durable email backup store** (dep: B4b-ii, B4c-ii) →
>   [B4e-iii-a-durable-email-backup-store.md](B4e-iii-a-durable-email-backup-store.md): makes the
>   pre-write backups durable (encrypted PostgreSQL store + Alembic migration + service). AD2.
> - **B4e-iii-b — Email categories pipeline integration** (dep: B4e-i, B4d-i, B4b-i, B4c-i) →
>   [B4e-iii-b-email-categories-pipeline.md](B4e-iii-b-email-categories-pipeline.md): exposes
>   `default_address`/`email_routing`/autoresponder as explicit evidence-bound categories/steps. AD1.
> - **B4e-iii-c — Email runtime registry and dispatch** (dep: B4e-iii-a, B4e-iii-b, B4e-ii, B4a,
>   B4b-ii, B4c-ii, B4d-ii) →
>   [B4e-iii-c-email-runtime-registry-dispatch.md](B4e-iii-c-email-runtime-registry-dispatch.md):
>   wires the engines to the real worker.
>
> `C3` now depends on **B4e-iii-c**. The requirements below are preserved as the umbrella spec the
> three sub-tasks jointly satisfy; do not implement against this ID directly.

**Origin:** third sub-task of the scope split of `B4e` (see
[B4e-autoresponder-dispatch.md](B4e-autoresponder-dispatch.md), split record). **Aggregator:**
this task is itself expected to exceed the 8-file/500-line guardrails and will be **further
split** (formalized after B4e-ii, with an updated measurement) into:

- **B4e-iii-a — Durable email backup store** (dep: B4a; enables B4b-ii/B4c-ii wiring): a
  durable PostgreSQL backup store (a new table + Alembic migration + model + a real
  `persist_backup`) so the compensable default-address/routing writers can persist a protected
  backup **before** the write (backup-or-nothing). **AD2 (confirmed):** no compensable
  default-address/routing write may be wired until this store is complete.
- **B4e-iii-b — Email categories pipeline integration** (dep: B4e-iii-a, B4e-ii): make
  `default_address`, `email_routing` and the autoresponder explicit **evidence-bound**
  categories across comparison, plan, preview and readiness (extend `_normalize`,
  `build_steps`, `WRITER_CATEGORIES`, `CALLS`, the eligibility gaps). **AD1 (confirmed):**
  extend the pipeline — these must not stay unreachable writers or optional follow-ups. Keep
  each category evidence-bound and disabled by default; do not create a generic `email`
  category that hides distinct states.
- **B4e-iii-c — Email runtime registry and dispatch** (dep: B4e-iii-b): a uniform per-category
  engine registry driving forwarder (B4a), default-address (B4b-ii), routing (B4c-ii), filters
  (B4d-ii) and autoresponder (B4e-ii); per-category + per-write (`before_write`) + post-phase
  `authorize`/lease/fencing re-validation; destination-only gateways; source payloads loaded
  only from evidence-bound snapshots; atomic run+attempt commit; per-category/item checkpoint;
  explicit terminal semantics (all selected verified → `succeeded`; any manual/blocked/
  unsupported → `halted`/`failed`; unimplemented categories → never `succeeded`; a mixed
  completed/pending run is never a success).

**Cross-cutting facts (from the B4e analysis, 2026-07-12):**

- Category IDs: `email_forwarders`, `email_filters`, `email_autoresponders` exist in the
  preview pipeline; `default_address`/`email_routing` exist **only** as per-domain evidence
  contracts (no preview/plan/readiness category) — hence B4e-iii-b.
- The autoresponder category is `MANUAL` in `plans/engine.py` and excluded by the preview
  builder — B4e-iii-b must make it selectable without weakening its evidence gate.
- No durable backup store exists (`persist_backup` is only a test callback) — hence B4e-iii-a.
- Engine interfaces are heterogeneous — the registry (iii-c) normalizes them with per-category
  adapters.
- `safety_gates.authorize(categories=)` already supports per-category readiness gating; each
  wired category needs a `eligible_for_real_design` readiness path (added in iii-b).
- Routing needs an evidence-bound `RoutingSetPolicy` source (empty by default → every set
  blocked); provisioning policies is out of scope for the initial wiring (routing stays inert
  until a policy source exists — record as an open limitation).
- No `partial` status; `halted` models partial success (keep). The A3 actor cannot resume a
  `running` attempt — **crash/resume recovery stays with C4**; B4e-iii must not declare
  production-ready recovery.

**Requirements (uniform dispatch):** register only categories with a completed engine AND
evidence contract; no generic `email` category; map exact plan/preview category IDs to engines;
re-validate `authorize()`/lease/fencing before each category, via `before_write` before each
write, and after the phase before commit; build gateways from the destination only; load source
payloads only from evidence-bound snapshots; never put sensitive email payload in run/attempt/
event/checkpoint/public-compensation; persist protected backups for default-address/routing
before the write; per-category/item checkpoints; never re-run a verified item without a
fresh-read; explicit terminal-state semantics (above); atomic run+attempt commit; a fenced-out
worker persists nothing; a stale confirmation/evidence between categories stops the next one;
cancellation prevents further writes; every category flag + master switch exact-match enabled;
disabled by default; no source write; no real contact in tests; mock/dry-run intact.

**Acceptance Criteria (aggregator):**

- [ ] B4e-iii-a, B4e-iii-b, B4e-iii-c formalized (after B4e-ii), each ≤8 files / ≤500 lines,
      landed and verified; `C3` unblocked once B4e-iii-c completes.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```
