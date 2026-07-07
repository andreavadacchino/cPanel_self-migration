# Sprint 2 — Read-only cPanel inventory (micro-design)

Obiettivo: leggere dati **reali** da un account cPanel in sola lettura (UAPI
account-level), senza migrare nulla. Nessuna funzione write, nessun rsync/SSH/
IMAP/MySQL dump, nessun DNS write, nessun apply/cutover.

## File nuovi

Adapters (pacchetto condiviso, senza DB/FastAPI):
- `packages/adapters/adapters/cpanel/client.py` — `CpanelClient` reale (httpx).
- `packages/adapters/adapters/cpanel/errors.py` — errori tipizzati.
- `packages/adapters/adapters/cpanel/inventory.py` — `CpanelInventorySource`
  (capability scan + collect via UAPI read-only).
- `packages/adapters/adapters/inventory.py` — modelli normalizzati
  (`CapabilityReport`, `InventoryResult`, `ProbeOutcome`), protocol
  `InventorySource`, `MockInventorySource`, factory `build_inventory_source`.
- `packages/adapters/adapters/credentials.py` — resolver `env://` (+ errori).

API:
- `apps/api/app/modules/inventory/{__init__,models,schemas,service,router}.py`
- `apps/api/alembic/versions/0003_inventory_snapshots.py`
- test: `apps/api/app/tests/test_cpanel_client.py`, `test_credentials.py`,
  `test_inventory_source.py`, `test_inventory_api.py`.

Worker:
- test: `apps/worker/worker/tests/test_inventory.py`.

Frontend:
- `apps/web/src/features/migrations/InventorySummaryPanel.tsx`.

## File modificati

- `packages/adapters/adapters/cpanel/schemas.py` — aggiunge `CpanelUapiResponse`.
- `packages/adapters/adapters/cpanel/__init__.py` — export nuovi simboli.
- `packages/adapters/pyproject.toml` — dipendenza `httpx`.
- `apps/api/app/modules/endpoints/schemas.py` — `EndpointRead`: **rimuove**
  `auth_ref`, aggiunge `has_auth_ref: bool` (debito Sprint 1).
- `apps/api/app/modules/endpoints/service.py` — `test_connection` per
  mock / token_ref+env:// / vault:// (non implementato → 422).
- `apps/api/app/modules/endpoints/mock_connection.py` — rimosso (logica mock
  passa a `MockInventorySource`).
- `apps/api/app/core/errors.py` — `UnprocessableError` → 422.
- `apps/api/app/main.py` — include `inventory_router`.
- `apps/api/alembic/env.py` + `apps/api/app/tests/conftest.py` — import del
  nuovo model inventory.
- `apps/api/app/tests/test_endpoints.py` — aggiorna il test del debito
  (`auth_ref` non più in risposta → `has_auth_ref`).
- `apps/worker/worker/db.py` — tabelle `endpoints` + `inventory_snapshots`
  (subset Core) + funzioni read endpoint / update capabilities / snapshot.
- `apps/worker/worker/actors/preflight.py` — preflight read-only reale.
- `apps/worker/pyproject.toml` + `apps/worker/Dockerfile` — dipendenza adapters.
- `apps/worker/worker/tests/test_preflight.py` — riscrive il guard Sprint 1
  ("no network") nel nuovo invariante ("mock non fa I/O reale").
- `apps/web/src/lib/api.ts`, `EndpointCard.tsx`, `PreflightPanel.tsx`,
  `EndpointForm.tsx`, `MigrationSetupPage.tsx`.

## Nuova tabella Alembic — `0003_inventory_snapshots`

`inventory_snapshots`: `id`, `migration_id` (FK migrations CASCADE),
`endpoint_id` (FK endpoints CASCADE), `endpoint_role` (source|destination),
`status` (pending|running|succeeded|failed), `captured_at`, `summary` (JSON),
`data` (JSON), `error` (Text), `created_at`, `updated_at`. Indici su
`migration_id` e `endpoint_id`. Downgrade droppa tabella+indici.

## Nuove API

- `GET /api/migrations/{id}/inventory` → `{ source: Snapshot|null,
  destination: Snapshot|null }` (ultimo snapshot per ruolo; 200 anche se vuoto).
- `GET /api/migrations/{id}/inventory/source` → Snapshot (404 se assente).
- `GET /api/migrations/{id}/inventory/destination` → Snapshot (404 se assente).
- `GET /api/endpoints/{id}/capabilities` → `{ endpoint_id, connection_status,
  last_checked_at, capabilities }`.

Response **safe**: nessun token/auth_ref/Authorization/secret.

## Funzioni UAPI read-only selezionate (verificate su api.docs.cpanel.net)

| Categoria | UAPI | URL |
|---|---|---|
| account/server info | `StatsBar::get_stats` | `/execute/StatsBar/get_stats` |
| domini | `DomainInfo::list_domains` | `/execute/DomainInfo/list_domains` |
| email | `Email::list_pops` | `/execute/Email/list_pops` |
| database | `Mysql::list_databases` | `/execute/Mysql/list_databases` |
| cron | `Cron::list_cron` | `/execute/Cron/list_cron` |
| ssl | `SSL::installed_hosts` | `/execute/SSL/installed_hosts` |
| dns | **non implementato** — non verificato → capability=false, limitation |

Auth: `Authorization: cpanel USERNAME:APITOKEN`; porta sicura 2083; pattern
`https://host:2083/execute/Module/function`. Fonte: api.docs.cpanel.net
(guida Tokens + operation pages). Lo scanner è **probe-driven**: ogni funzione
che fallisce (non supportata dal server) marca la capability come non
disponibile invece di far cadere l'intero preflight.

## Formato normalizzato inventory

`capabilities` (salvato su endpoint + snapshot):
```json
{ "source": "cpanel|mock", "can_connect": true, "can_authenticate": true,
  "can_read_account_info": true, "can_read_domains": true, "can_read_email": true,
  "can_read_databases": true, "can_read_cron": true, "can_read_ssl": false,
  "can_read_dns": false, "limitations": ["dns_read_unavailable_or_unsupported"] }
```
`summary` (solo conteggi/stato, `null` = categoria non letta):
```json
{ "domains_count": 2, "email_accounts_count": 5, "databases_count": 3,
  "cron_jobs_count": 1, "dns_records_count": null, "ssl_items_count": null,
  "warnings_count": 2 }
```
`data` (normalizzato, mai raw completo, mai segreti): liste con soli campi
identificativi (dominio, email, nome db, comando cron redatto, host ssl).

## Strategia credential resolver

`resolve_credential(auth_ref)`: `env://VAR` → legge `os.environ[VAR]`, errore
chiaro se mancante (senza il valore). `vault://`/`secretsmanager://`/`ref://` →
`CredentialResolverNotImplemented`. Mai loggato, mai restituito alla UI.

**Allowlist di sicurezza**: il nome della variabile deve essere un identificatore
uppercase che contiene `CPANEL` (convenzione `SOURCE_CPANEL_TOKEN`/
`DEST_CPANEL_TOKEN`). Impedisce a un chiamante di nominare un segreto d'ambiente
non correlato (`DATABASE_URL`, `REDIS_URL`, cloud creds) e farselo inviare come
bearer verso un host arbitrario. È una mitigazione, NON un sostituto
dell'autenticazione API (vedi rischi).

## Strategia error handling cPanel

Errori tipizzati (nessun collasso su `Exception`):
`CpanelConnectionError`, `CpanelTimeoutError`, `CpanelAuthError` (HTTP 401/403),
`CpanelApiError` (UAPI `status=0`), `CpanelParseError` (JSON non valido),
`CpanelUnsupportedFunctionError`. Base comune `CpanelError`. Il token non
compare mai in `repr`/messaggi/log.

## Strategia timeout/retry

Timeout esplicito (`httpx.Timeout`, default 10s). Sprint 2: **singolo
tentativo** (nessun retry automatico) — mappa timeout→`CpanelTimeoutError`,
connect→`CpanelConnectionError`. Retry con backoff = fuori scope (annotato).

## Fuori scope (invariato dalle regole)

Migrazione reale, write cPanel, rsync, SSH reale, IMAP reale, MySQL dump,
creazione email/db/file, DNS write, apply/cutover/rollback, vault reale,
comparison completa, migration plan, codice legacy Go, Celery/RQ/BackgroundTasks.

## Rischi aperti

- **Nessuna autenticazione API** (condizione dell'intera piattaforma, non solo
  Sprint 2): tutte le route sono aperte. Prima di un deploy non-locale servono
  auth + validazione host (blocco range privati/loopback) per chiudere del tutto
  il vettore SSRF/token-to-arbitrary-host. L'allowlist env riduce il blast
  radius ma non sostituisce l'auth.
- `worker/db.get_engine()` è un singleton lazy non sincronizzato (codice
  ereditato da Sprint 1): due thread al primo avvio potrebbero creare due engine.
  Impatto basso; da mettere sotto lock o costruire all'import in un follow-up.
- Il comando cron NON viene mai persistito (può contenere segreti): salviamo
  solo lo schedule + il conteggio. Se in futuro serve il comando, va redatto.
- `Cron::list_cron` non catturato dalle operation-page api.docs (404 slug); è
  funzione UAPI standard e comunque probe-driven → si auto-verifica a runtime.
- DNS read non implementato (non verificato) → capability sempre false.
- Smoke reale cPanel dipende da env var reali; se assenti non viene eseguito.
- Worker importa `packages/adapters` (non l'app FastAPI): boundary rispettato.
