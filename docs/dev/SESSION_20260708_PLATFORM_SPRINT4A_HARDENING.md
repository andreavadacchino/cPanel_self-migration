# Session 2026-07-08 — Platform V2: MySQL normalization, Migration Plan, Hardening

Sessione su **Migration Platform V2** (`migration-platform/`, stack greenfield
FastAPI + React + Dramatiq — distinto dalla WebUI Go legacy). Tre PR, tutte
sviluppate in TDD (RED→GREEN), ognuna con review adversariale e gate reali.

| PR | Titolo | Stato | SHA merge / HEAD |
|----|--------|-------|------------------|
| #98 | fix(platform): normalize mysql identities across cpanel prefixes | **MERGED** | `57b8741` |
| #99 | feat(platform): add read-only migration plan | **MERGED** | `607252a` |
| #100 | fix(platform): harden plan UI errors and 204 routes | **OPEN / MERGE-READY** | branch `fix/platform-v2-plan-ui-api-hardening`, HEAD `a881ef0` |

`fork/main` a fine sessione = **`607252a`** (dopo #99; #100 non ancora mergiata).
Remote: `origin`=tis24dev (Go legacy), `fork`=andreavadacchino (platform); il
"main" di lavoro = **`fork/main`**. Alembic lineare `0001→0007` single-head.

Metodo operativo per ogni PR: **git worktree isolato** in scratchpad da
`fork/main`, venv dedicato con pacchetti editable, branch legacy
`feat/operator-landing-prune` (con modifiche Go non committate) **mai toccato**.
PR sempre verso `--repo andreavadacchino/cPanel_self-migration` base `main`.

---

## PR #98 — MySQL prefix/logical normalization (cross-account)

**Problema.** Il comparison confrontava database e utenti MySQL per **nome
completo** cPanel (`giorginisposi_wp`). Funziona solo se source e destination
condividono lo stesso username cPanel. In una migrazione cross-account
(`vecchio123_wp` vs `nuovo456_wp`) generava **falsi blocker** anche quando la
parte logica è identica.

**Modello di normalizzazione.** `_split_identity(name)` via `partition("_")` sul
**primo** underscore → `(prefix, logical)`; se prefix o logical sarebbero vuoti
(`_wp`, `foo_`) il nome resta intero con `prefix=null` (nessun falso match). Il
prefisso è **derivato dal nome**, non assunto uguale allo username cPanel
(miglioramento futuro: username esplicito nello snapshot).

- `_norm_databases` → aggiunge `logical_name` / `prefix` (mantiene `name`).
- `_norm_mysql_users` → aggiunge `logical_user` / `prefix` / `logical_databases`
  (mantiene `user` / `databases` / `relationship_present`).
- `comparison_engine`: `_key_db` / `_key_mysql_user` usano il campo logico se
  presente (fallback full name legacy); **fingerprint per-categoria**
  (`_CategorySpec.fingerprint_fn`) — db su `logical_name`, user su
  `(logical_user, sorted logical_databases, relationship_present)`.

**Review python R1→R2 APPROVE, 3 fix applicati:**
1. HIGH — il fingerprint faceva `.lower()` sul **contenuto** → `ShopDB`/`shopdb`
   falso match (MySQL su Linux `lower_case_table_names=0`); per `mysql_users`
   nascondeva un BLOCKER reale. Fix: `.strip()` senza `.lower()` nel
   fingerprint; il `.lower()` resta solo nelle `_key_*` per l'indicizzazione.
2. HIGH — `_index` con key logica (non unica) scartava **silenziosamente** un
   item distinto in collisione (es. `shop_wp` + `wp` → logical `wp`). Fix:
   `_index` ritorna `(map, collisions)`; `compare()` emette entry «identità
   logica ambigua» con **severity = stato reale** se la key è su un solo lato
   (db/mysql assenti su dest = BLOCKER, non warning); duplicato esatto ≠
   collisione.
3. MEDIUM — `relationship_present` reinserito nel fingerprint mysql_user.

**Gate:** scope-diff vuoto, `docker compose config` OK, **API 225→255 passed**,
worker 15, web build OK. **Real-smoke NON eseguito** (credenziali non
disponibili); regressione `giorginisposi` (3 utenti source → 1 dest = 2 blocker
reali) coperta dal test deterministico `test_giorginisposi_regression_two_real_blockers`.

Doc: `migration-platform/docs/MYSQL_PREFIX_NORMALIZATION.md`.

---

## PR #99 — Migration Plan read-only (Sprint 4A)

Primo livello **operativo**: da «strumento che confronta» a «strumento che guida
l'operatore», **senza eseguire nulla** (no write cPanel/DNS/DB/email, no
rsync/SSH/IMAP/dump, no apply/cutover/rollback, no worker di migrazione, no
bottoni Start/Apply/Execute, no auth API).

**Design chiave — proiezione pura, severità ereditate.** Il piano NON re-inventa
la gravità: la eredita dalla **comparison** (single source of truth) e la
instrada in 6 sezioni. Questo garantisce «non crea falsi blocker».

Funzione pura `build_migration_plan(source_inv, destination_inv, comparison)
-> MigrationPlanOutput` in `packages/domain/domain/migration_plan.py` (no
DB/API/cPanel). Routing:

- **blockers** = entry comparison `severity=blocker` e categoria ∉
  {capabilities, coverage}.
- **manual_tasks** = warning in categorie non automatizzabili {dns_records,
  cron_jobs, ssl} + forwarders/autoresponders/ftp presenti sul source (non
  confrontati item-per-item, derivati dall'inventory).
- **warnings** = altri warning/info non instradati.
- **unknowns** = entry categoria {capabilities, coverage} (read-gap → **mai
  blocker**).
- **ready_steps** = match per-categoria dal `summary.by_category` (descrittivi,
  non per-item).
- **cutover_notes** = note statiche (read-only + DNS re-point + SSL re-issue).

`status`: `blocked` se blockers>0 altrimenti `ready_for_review`. Unknown **non**
aumenta `blockers_count`.

**Decisione documentata sul cron.** Il brief elencava «cron mancante» sia tra i
blocker sia tra i manual task (contraddizione). Risolto a favore della
comparison: l'engine classifica il cron mancante come **warning** (ricreabile al
cutover) → il piano lo tratta come **manual task, NON blocker**. Così vale sempre
«blocked ⟺ la comparison ha blocker».

**API** `app/modules/plan/`: `POST/GET /api/migrations/{id}/plan` (409 senza
comparison SUCCEEDED, 404 senza piano; ancora il piano all'ultima comparison
SUCCEEDED + i suoi 2 snapshot; persiste piano FAILED su eccezione builder poi
rilancia). Tabella `migration_plans` (**Alembic 0007**, FK CASCADE, summary/
sections/generated_from JSON). **UI** `MigrationPlanPanel` read-only (status
badge, summary counts, 6 sezioni, copy "Questo piano è read-only. Non esegue
modifiche sui server.", **zero bottoni di esecuzione**).

**Review python R1 APPROVE**, 2 MEDIUM fixati:
- `destination_inventory` era parametro morto → ora usato (conteggio
  destination nei manual task forwarders/autoresponders/ftp).
- Guardie `isinstance` anti-crash su `entries`/`by_category`/input malformati.

**Gate:** scope vuoto, docker OK, **API 254 passed**, worker 15, alembic
up/down(0006)/up(0007) reversibile single-head, web build OK. **Real-smoke NON
eseguito** (credenziali non disponibili).

Doc: `migration-platform/docs/MIGRATION_PLAN_READONLY.md`.

---

## PR #100 — Hardening plan UI + 204 route (OPEN, MERGE-READY)

PR piccola di sola robustezza, **nessuna feature nuova**. Due fix mirati.

### Bug 1 (backend, riprodotto) — route 204 + `-> None`

La DELETE `/api/endpoints/{id}` aveva `status_code=204` + annotazione `-> None`.
Con `from __future__ import annotations` in testa al router, `-> None` diventa
`ForwardRef("None")` → FastAPI la interpreta come response_model **truthy**. Su
**`fastapi==0.111.1`** (il minimo di `fastapi>=0.111`) la registrazione della
route fallisce con `AssertionError: Status code 204 must not have a response
body` (routing.py:468) → **`from app.main import app` crasha** → l'intera suite
non fa neanche collection. Su `0.139.0` è tollerato (bug version-specific).

Riproduzione: venv dedicato pinned a `fastapi==0.111.1` → import crash
confermato; il code-reviewer l'ha **riprodotto indipendentemente** confermando
la root cause (ForwardRef da `from __future__ import annotations`).

**Fix conservativo:** `response_model=None` esplicito sulla DELETE (disabilita il
response model; il 204 resta body-less; `-> None` mantenuto per leggibilità/
mypy). Verificato: è l'**unica** route DELETE/204 del backend.

### Bug 2 (frontend) — fetchPlan inghiotte ogni errore

`fetchPlan()` faceva `catch { return null }` generico → un 500/409/network
appariva come "nessun piano generato".

**Fix:** nuova classe tipizzata `ApiError extends Error` (`status` + `body`
opzionale) lanciata da `request()`; `fetchPlan` ritorna null **solo** su
`ApiError.status === 404`, rilancia il resto (un errore di rete resta
`TypeError` non-`ApiError` → rilanciato); `MigrationPlanPanel` fa `.catch` sul
load → errore **visibile** vs "nessun piano" **neutro**.

### Test / Gate

- 3 test `test_app_204_hardening.py` (import/openapi/204-no-body): RED su 0.111.1
  senza fix (collection crash), GREEN su **0.111.1 E 0.139.0** dopo il fix.
- **API 257 passed su entrambe** le versioni FastAPI; worker 15; scope vuoto;
  docker OK; web build OK (tsc + vite).
- **Real-smoke NON eseguito: non necessario** per hardening UI/API.

**Review code-reviewer APPROVE** (0 CRITICAL/HIGH; no regressioni sui chiamanti
di `request()`; `ApiError.body` mai letto in UI → nessun leak).

---

## Decisioni chiave della sessione

1. **Severità = single source of truth nella comparison.** Il Migration Plan non
   re-inventa mai la gravità; la eredita e la instrada. Evita falsi blocker e
   incoerenze piano↔comparison.
2. **Cron = manual task, non blocker** (risolve la contraddizione del brief #99).
3. **Fingerprint case-sensitive** (#98): MySQL su Linux è case-sensitive; il
   lowercase resta solo per l'indicizzazione, mai per decidere match/different.
4. **Collisioni logiche mai scartate silenziosamente** (#98): sempre un'entry
   visibile con severity coerente allo stato reale.
5. **Fix 204 conservativo** (#100): `response_model=None`, semantica invariata,
   robusto su tutto il range `fastapi>=0.111`.

## Follow-up aperti (non bloccanti)

- **`fetchCurrentJob` / `fetchComparison`** (`apps/web/src/lib/api.ts`) hanno lo
  **stesso** pattern swallow-500 corretto per `fetchPlan` in #100. Impatto
  concreto: un 500 transitorio su `jobs/current` durante il polling preflight
  può **fermarlo silenziosamente** mostrando "nessun job" invece di un errore.
  Fix = narrow-catch a 404 anche lì, ma richiede aggiungere gestione errore in
  `ComparisonPanel` e `MigrationSetupPage` → rimandato (fuori scope PR piccola).
- **Nessun test runner frontend** (vitest/jest assenti): i fix UI sono coperti
  da tipi + build, non da unit test. Aggiungere un runner è nuova infra.
- **Ready steps per-categoria, non per-item** (#99): la comparison omette i
  match dalle entry (solo conteggi).
- **Forwarders/autoresponders/ftp** non confrontati item-per-item (solo
  coverage, Sprint 3.5): marcati manual task se presenti sul source.
- **`MigrationPlanPanel`** non resetta l'errore a inizio effect (LOW, non
  raggiungibile oggi — remount completo su navigazione).
- **Real-smoke cPanel** mai eseguito in queste PR (credenziali non disponibili
  in sessione). Le regressioni critiche sono coperte da unit test deterministici.
- **Nessuna auth API** sull'intera piattaforma (pre-esistente, by design in
  questi sprint): prima di deploy non-locale servono auth + validazione host.

## Note ambiente

- Worktree isolati in scratchpad di sessione (`wt-mysql-prefix`,
  `wt-migration-plan`, `wt-hardening`); venv dedicati (`venv-mysql`,
  `venv-plan`, `venv-hard`, `venv-fa111` pinned a fastapi 0.111.1 per riprodurre
  il bug 204). Branch legacy `feat/operator-landing-prune` con modifiche Go non
  committate **mai toccato** in tutta la sessione.
- La suite gira su venv dedicato con `pip install -e packages/domain
  packages/adapters apps/api[test] apps/worker[test]`. Docker build risolve
  `fastapi>=0.111` all'ultima versione al momento del build; il fix #100 rende
  comunque il 204 sicuro su tutto il range.

## Prossimi passi suggeriti

1. Mergiare #100 (merge-ready).
2. Follow-up narrow-catch su `fetchCurrentJob`/`fetchComparison` (+ gestione
   errore nei loro consumer).
3. Sprint 4B: probabile manual-tasks tracking / apply gating (sempre read-only
   finché non c'è un motore di esecuzione con guardrail).
