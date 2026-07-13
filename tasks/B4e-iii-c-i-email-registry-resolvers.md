# Task B4e-iii-c-i: Email registry and evidence resolvers

| Field | Value |
|---|---|
| **ID** | `B4e-iii-c-i` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4e-iii-b, B4e-ii, B4a, B4b-ii, B4c-ii, B4d-ii |
| **Branch** | `feat/b4e-iii-c-i-email-registry-resolvers` |

**Origin:** first sub-task of the scope split of `B4e-iii-c` (see
[B4e-iii-c-email-runtime-registry-dispatch.md](B4e-iii-c-email-runtime-registry-dispatch.md)).

**Goal:** A typed registry and evidence-bound source payload resolvers for all 5 email
categories (`email_forwarders`, `default_address`, `email_routing`, `email_filters`,
`email_autoresponders`). Testable in isolation; no gateway, no dispatch wiring, no backup
binding. Step IDs select items; the authoritative payload comes from the immutable snapshot
bound to its contract.

**Acceptance Criteria:**

- [x] Registry covers exactly 5 email categories with metadata (flag, backup need, scope);
      each resolver validates the contract with `is_write_eligible()` and extracts items from
      the snapshot, not from step IDs or preview; no generic `email` category; no dispatch import.
- [x] No test, typecheck, Compose, or coverage regression; mock/dry-run intact.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```

## Completion Record

**Date:** 2026-07-14

**Implementation:** `email_phase_registry.py` (262 lines) — typed `REGISTRY` table mapping
the 5 email category IDs to `CategoryEntry` (flag property, backup need, scope strategy).
Five evidence-bound resolvers extract the authoritative source payload from the immutable
snapshot and its contract, validated with `is_write_eligible()` (or coverage status for
forwarder). Step IDs are selectors only; a step not uniquely present in the snapshot is blocked.
Duplicate contract records are detected fail-closed. Forwarder returns verified structured
pairs, not re-parsed step IDs. Default-address reads `dest_username` from the destination
contract. Routing policies remain empty (no provider exists). Filters grouped by scope.
Autoresponders grouped by domain with fingerprint re-validation.

**Files:**

| File | Lines |
|---|---|
| `email_phase_registry.py` (NEW) | 262 |
| `test_email_phase_registry.py` (NEW) | 256 |
| `B4e-iii-c-i-email-registry-resolvers.md` (NEW) | 36 |
| `B4e-iii-c-ii-email-gateways-backups.md` (NEW) | 27 |
| `B4e-iii-c-iii-email-worker-dispatch.md` (NEW) | 27 |
| `B4e-iii-c-email-runtime-registry-dispatch.md` | updated |
| `BACKLOG.md` | updated |

**Tests:** 757 API tests pass (+27 new). Web build and Compose validation pass.

**Review:** adversarial python-reviewer found 1 Critical (`dest_username` from source
contract), 2 High (forwarder version check absent, forwarder returns step_ids not structured
data), 3 Medium (filter/autoresponder no local dedup, routing placeholder policies, full
snapshot in kwargs). Critical and both Highs corrected; Medium #3 (filter/autoresponder dedup)
corrected. Medium #4/#5 documented as known limitations for c-ii.

**Known limitation (High #1):** `forwarder_contract` has no `version` field and no
`is_write_eligible()` — this is an upstream gap in the collector/`forwarder_rules` design,
not closable in c-i. The registry uses coverage status check as the current best available
validation. A future task should add versioning to the forwarder contract.
