# Task B4e-iii-b: Email categories pipeline integration

| Field | Value |
|---|---|
| **ID** | `B4e-iii-b` |
| **Status** | `[ ]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4e-i, B4d-i, B4b-i, B4c-i |
| **Branch** | `feat/b4e-iii-b-email-categories-pipeline` |

**Origin:** second sub-task of the scope split of `B4e-iii` (see
[B4e-iii-email-dispatch-integration.md](B4e-iii-email-dispatch-integration.md), split record).

**Goal:** Make `default_address`, `email_routing` and the autoresponder explicit
**evidence-bound** categories/steps across comparison, plan, preview and readiness, so the
iii-c registry has real, selectable, evidence-gated steps to dispatch. **AD1 (confirmed):**
extend the pipeline — these must not stay unreachable writers or optional follow-ups.

**Scope.** Extend `_normalize`, `build_steps`, `WRITER_CATEGORIES`, `CALLS` and the readiness
eligibility gaps so each category is represented with distinct states; add an
`eligible_for_real_design` readiness path per wired category. Keep each category evidence-bound
and **disabled by default**; do **not** create a generic `email` category that hides distinct
states; the autoresponder category must become selectable without weakening its evidence gate.
No engine wiring and no real dispatch (that is iii-c); no source write.

**Facts (from the B4e analysis):** `email_forwarders`/`email_filters`/`email_autoresponders`
exist in the preview pipeline; `default_address`/`email_routing` exist **only** as per-domain
evidence contracts (no preview/plan/readiness category); the autoresponder category is `MANUAL`
in `plans/engine.py` and excluded by the preview builder.

**Acceptance Criteria:**

- [ ] `default_address`, `email_routing` and autoresponder are explicit evidence-bound
      categories/steps in comparison/plan/preview/readiness, disabled by default, each with a
      distinct state (no generic `email` category), and none dispatchable yet.
- [ ] No test, typecheck, Compose, or coverage regression; mock/dry-run intact.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```
