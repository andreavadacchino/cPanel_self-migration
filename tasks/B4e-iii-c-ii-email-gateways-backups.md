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

- [x] Gateway builders construct destination-only gateways; backup binding connects
      `persist_email_backup` to default-address/routing; per-category flags checked; no worker
      wiring; no source write.
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

**Commit 1 (`7c209df`):** initial implementation — 5 file, 437 insertions / 3 deletions.

1. **`email_category_runtime.py`** (202 righe) — modulo principale:
   - `is_category_enabled()` — flag check via `REGISTRY[cat].flag_property`, exact-match, fail-closed
   - `_build_destination_client()` — client factory destination-only, role guard, write abilitato
   - `ForwarderGateway` — typed `SafeRead`/`DestinationWrite` (list_forwarders, add_forwarder)
   - `_make_backup_persister()` — callback per DA/routing, fingerprint deterministico SHA-256
   - `run_email_category()` — single-category executor con multi-scope aggregation
   - Client chiuso in `finally` su ogni path

2. **`forwarder_rules.py`** (+14 righe) — typed ops:
   - `list_forwarders_op()` → `SafeRead("Email", "list_forwarders")`
   - `add_forwarder_op(source, destination)` → `DestinationWrite("Email", "add_forwarder", ...)`

**Review R1:** 1 Critical + 3 High trovati e corretti in-commit:
fingerprint non deterministico → SHA-256; forwarder re-parse step_id → verified_pairs;
dry_run guard mancante → aggiunto; before_write opzionale → required.

**Commit 2 (correttivo):** category/evidence binding, run/attempt execution binding,
backup binding testato realmente, routing non-vacuo.

1. **Category/evidence binding** — `resolved.category` deve coincidere esattamente con
   `category`; mismatch → `category_evidence_mismatch`, zero effetti.

2. **Run/attempt execution binding** — validazione fail-closed prima del client:
   `run.status == running`, `attempt.status == running`,
   `attempt.execution_run_id == run.id`, `fencing_token` intero valido.
   Tutte le cinque categorie ora protette (non solo DA/routing via backup store).

3. **Backup binding testato realmente** — `_make_backup_persister` esercitato con argomenti
   reali: run_id, attempt_id, category, item_key, fencing_token, fingerprint deterministico
   (prefisso `efp1:`). Test: backup failure → zero before_write/write; before_write failure
   dopo backup → zero write; fingerprint identico per stesso payload, diverso per payload
   diverso; forwarder/filter/autoresponder senza callback backup.

4. **Routing non-vacuo** — step reale `email_routing:example.test` con source `local`,
   destination live `remote`, `policies={}`: decisione blocked (no policy), zero write,
   `ok=False`, step non in completed. Caso already-present: source==destination, no-op,
   step completed, zero write/backup.

**Files modified (corrective commit):**

| File | Change |
|---|---|
| `email_category_runtime.py` | +10 (category binding, run/attempt binding, ExecutionStatus import) |
| `test_email_category_runtime.py` | +90 (15 nuovi test: binding, backup, routing non-vacuo) |
| `B4e-iii-c-ii task file` | AC spuntate, Completion Record corretto |
| `BACKLOG.md` | status [x]→[~]→[x] |

**Tests:** 833 API (43 in runtime file). Worker 18 passed. Web build OK. Compose OK.

**Invarianti preservati:**
- `dispatch.py` non importa il nuovo modulo
- `IMPLEMENTED_REAL_CATEGORIES == frozenset({"domains"})`
- Worker 18 passed (invariato)
- Routing inerte: `policies={}`, zero write, non falso successo
- Nessun payload sensibile in reason/repr/eventi
- c-iii resta `[ ]`, C3 bloccato

**Limite fisico documentato:** cPanel non supporta fencing token remoto; la finestra tra
l'ultimo check locale e la write remota è inevitabile. Solo fencing PostgreSQL locale
enforced (via `persist_email_backup` e `before_write`/`authorize`).

Review finale: 0 Critical, 0 High, 0 Medium.
