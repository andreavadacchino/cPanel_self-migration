# Task B4e-iii-c-ii: Destination gateways and durable backup bindings

| Field | Value |
|---|---|
| **ID** | `B4e-iii-c-ii` |
| **Status** | `[ ]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4e-iii-c-i, B4e-iii-a |
| **Branch** | `feat/b4e-iii-c-ii-email-gateways-backups` |

**Origin:** second sub-task of the scope split of `B4e-iii-c`.

**Goal:** Real destination-only gateway builders for all 5 email categories, backup store
binding for default-address/routing through the iii-a durable store, per-category flag checking.
Not wired to the worker.

**Acceptance Criteria:**

- [ ] Gateway builders construct destination-only gateways; backup binding connects
      `persist_email_backup` to default-address/routing; per-category flags checked; no worker
      wiring; no source write.
- [ ] No test, typecheck, Compose, or coverage regression.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```
