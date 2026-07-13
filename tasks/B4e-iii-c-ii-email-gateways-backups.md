# Task B4e-iii-c-ii: Destination gateways and durable backup bindings

| Field | Value |
|---|---|
| **ID** | `B4e-iii-c-ii` |
| **Status** | `[x]` |
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

## Completion Record

**Date:** 2026-07-14

**Commit (`e_pending`):** destination-only gateways, single-category executor, backup binding.

**Misurazione raw:** 391 righe / 5 file (budget: 500 / 8).

**Componenti implementati:**

1. **`email_category_runtime.py`** (202 righe) — modulo principale:
   - `is_category_enabled()` — flag check via `REGISTRY[cat].flag_property`, exact-match, fail-closed
   - `_build_destination_client()` — client factory destination-only, role guard, write abilitato
   - `ForwarderGateway` — typed `SafeRead`/`DestinationWrite` (list_forwarders, add_forwarder)
   - `_make_backup_persister()` — callback per DA/routing, fingerprint deterministico SHA-256
   - `run_email_category()` — single-category executor con pre-gate chain:
     unknown_category → dry_run → before_write_required → evidence → blocked → flag → client
   - Multi-scope: filtri per scope, autoresponder per dominio, stop dopo primo gruppo fallito
   - `_merge()` — aggregazione `ok`/`pending`/`completed`/`compensation`/`reason`
   - Client chiuso in `finally` su ogni path

2. **`forwarder_rules.py`** (+14 righe) — typed ops:
   - `list_forwarders_op()` → `SafeRead("Email", "list_forwarders")`
   - `add_forwarder_op(source, destination)` → `DestinationWrite("Email", "add_forwarder", ...)`

**Review adversariale R1:** 1 Critical + 3 High trovati e corretti:

1. **CRITICAL → FIXED:** fingerprint non deterministico (`hash()` salted) → `hashlib.sha256` su JSON
2. **HIGH → FIXED:** forwarder re-parsava step_id → ora usa `verified_pairs` da c-i
3. **HIGH → FIXED:** nessun guard `dry_run` → aggiunto check fail-closed
4. **HIGH → FIXED:** `before_write` opzionale → ora required (reject se None)

**Invarianti preservati:**
- `dispatch.py` non importa il nuovo modulo
- `IMPLEMENTED_REAL_CATEGORIES == frozenset({"domains"})`
- Worker 18 passed (invariato)
- Routing inerte: `policies={}`, zero write, non falso successo
- Nessun payload sensibile in reason/repr/eventi

**Limite fisico documentato:** cPanel non supporta fencing token remoto; la finestra tra
l'ultimo check locale e la write remota è inevitabile. Solo fencing PostgreSQL locale è
enforced (via `persist_email_backup` e `before_write`/`authorize`).

**Tests:** 818 API (28 in runtime file). Worker 18 passed. Web build OK. Compose OK.
Review finale: 0 Critical, 0 High, 0 Medium.
