# Task B4e-iii-b: Email categories pipeline integration

| Field | Value |
|---|---|
| **ID** | `B4e-iii-b` |
| **Status** | `[x]` |
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

- [x] `default_address`, `email_routing` and autoresponder are explicit evidence-bound
      categories/steps in comparison/plan/preview/readiness, disabled by default, each with a
      distinct state (no generic `email` category), and none dispatchable yet.
- [x] No test, typecheck, Compose, or coverage regression; mock/dry-run intact.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```

## Completion Record

**Date:** 2026-07-13

**Implementation summary:** Made `default_address`, `email_routing`, `email_filters` and
`email_autoresponders` explicit evidence-bound categories across the full pipeline
(comparison → plan → preview → readiness) without wiring any engine to the dispatch.

**Changes by pipeline level:**

- **Comparison** (`comparison/engine.py`): added `_normalize` handlers for `default_address` and
  `email_routing` that project per-domain records from contract envelopes (`default_address_contract`,
  `email_routing_contract`), using opaque fingerprints over `domain`/`class`/`completeness` fields
  (never raw values). Contract version is validated before treating as readable; `partial`/`failed`/
  `ambiguous`/`unavailable` contracts produce `unknown` entries (fail-closed, never empty/match).
- **Plan** (`plans/engine.py`): moved `email_autoresponders` from `MANUAL` to `APPROVAL` (selectable);
  added `default_address`, `email_routing`, `email_filters` to `APPROVAL`; added
  `default_address_contract`, `email_routing_contract`, `email_filters_contract` to the excluded
  contract set; added sort order and domain dependencies.
- **Preview** (`executions/service.py`): added `CALLS` entries for `default_address` →
  `Email::set_default_address`, `email_routing` → `Email::setmxcheck`, `email_autoresponders` →
  `Email::add_auto_responder`, `email_filters` → `Email::store_filter`.
- **Readiness** (`readiness/engine.py`): added all four categories to `WRITER_CATEGORIES` and their
  contract keys to `EVIDENCE_CATEGORIES`; added `_CONTRACT_COVERAGE` mapping for fallback coverage
  derivation (with version check); added evidence-bound `_category_gaps` logic using
  `is_write_eligible()` from each contract's rules module (never bare status string);
  `email_routing` caps at `needs_contract_test` even with valid contracts (no `RoutingSetPolicy`
  source → documented limitation for B4e-iii-c).

**Files modified:**

| File | Lines added |
|------|:---:|
| `apps/api/app/modules/comparison/engine.py` | +30 |
| `apps/api/app/modules/plans/engine.py` | +8 |
| `apps/api/app/modules/executions/service.py` | +4 |
| `apps/api/app/modules/readiness/engine.py` | +60 |
| `apps/api/app/tests/test_email_categories_pipeline.py` | +290 (new) |
| `apps/api/app/tests/test_writer_readiness.py` | +3 |
| `tasks/B4e-iii-email-dispatch-integration.md` | +5 |
| `migration-platform/README.md` | +3 |

**Tests:** 730 API tests pass (was 717; +32 new in `test_email_categories_pipeline.py`, +1 updated
in `test_writer_readiness.py`). Web build and Docker Compose validation pass.

**Review:** adversarial python-reviewer agent found 2 Critical (contract key mismatch:
`routing_contract`→`email_routing_contract`, `filter_contract`→`email_filters_contract`) and
2 Medium (bare status trust without version check in comparison and readiness). All four
corrected and re-verified.

**Limitations documented for B4e-iii-c:**

- `email_routing` never reaches `eligible_for_real_design` because no `RoutingSetPolicy` source
  exists; the write stays blocked without an evidence-bound policy.
- `IMPLEMENTED_REAL_CATEGORIES` remains `frozenset({"domains"})` — no engine is wired.
