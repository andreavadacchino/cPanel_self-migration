# Sprint 2 — Read-only cPanel inventory (documentazione completa)

> Riferimento tecnico completo della slice Sprint 2 della Migration Platform V2.
> Per il micro-design sintetico pre-implementazione vedi
> [`sprint-2-cpanel-readonly.md`](./sprint-2-cpanel-readonly.md).
> Stato: PR #89 (fork `andreavadacchino/cPanel_self-migration`, base `main`).

---

## 1. Obiettivo e non-obiettivi

**Obiettivo**: dimostrare che la piattaforma sa **leggere dati reali** da un
account cPanel in **sola lettura**, senza migrare nulla. È il primo adapter reale
dopo gli stub di Sprint 0.

**Non-obiettivi (regole non negoziabili rispettate)**: nessuna migrazione reale,
nessuna funzione **write** cPanel, nessun rsync, SSH reale, IMAP reale, MySQL
dump, creazione email/db/file, DNS write, apply/cutover/rollback, vault reale,
comparison completa, migration plan, codice legacy Go, Celery/RQ/BackgroundTasks.

---

## 2. Confini architetturali

```
apps/web (React)  ──HTTP──▶  apps/api (FastAPI)  ──enqueue──▶  Redis (Dramatiq)
                                    │                               │
                                    ▼                               ▼
                               PostgreSQL  ◀───DML (Core)───  apps/worker (Dramatiq)
                                    ▲                               │
                                    └───────── packages/adapters ───┘  (condiviso)
```

- **`packages/adapters`** è il pacchetto **condiviso** che contiene TUTTA la logica
  cPanel/inventory/credenziali. Non dipende da FastAPI né dal DB. Lo usano sia
  l'API (test-connection) sia il worker (preflight).
- **Il worker NON importa l'app FastAPI** (`apps/api`). Importa solo
  `packages/adapters` (pacchetto neutro) e il proprio thin DB layer.
- **Postgres è la fonte di verità**; Redis è solo trasporto. Ogni job aggiorna
  Postgres.
- **Confine API↔worker**: l'API dichiara un *producer* Dramatiq (`run_preflight`,
  StubBroker sotto `DRAMATIQ_TESTING=1`) e fa `.send(job_id)`; il worker registra
  il *consumer* omonimo. Contratto = sola stringa actor-name (ereditato Sprint 1).

---

## 3. Mappa dei moduli

### 3.1 `packages/adapters/adapters/`
| File | Ruolo |
|---|---|
| `cpanel/client.py` | `CpanelClient` reale (httpx). URL `/execute/{Module}/{function}`, header auth, timeout, parsing envelope UAPI, mappatura errori tipizzati, `close()`/context-manager. Token mai in `repr`/log/errori. |
| `cpanel/errors.py` | Gerarchia errori: `CpanelError` → `CpanelConnectionError`, `CpanelTimeoutError`, `CpanelAuthError`, `CpanelApiError`, `CpanelParseError`, `CpanelUnsupportedFunctionError`. |
| `cpanel/schemas.py` | `CpanelUapiResponse` (envelope `result.{status,data,errors,messages,warnings}`, proprietà `ok`). |
| `cpanel/inventory.py` | `CpanelInventorySource`: `probe()` (connect/auth minimo) + `collect()` (capability scan + normalizzatori) + `close()`. |
| `inventory.py` | Modelli neutri `CapabilityReport`/`ProbeOutcome`/`InventoryResult`, protocol `InventorySource`, `InventoryError`, `MockInventorySource`, factory `build_inventory_source`, helper `build_summary`. |
| `credentials.py` | `resolve_credential(env://VAR)` con **allowlist**; errori `CredentialError`/`CredentialNotFound`/`CredentialResolverNotImplemented`. |

### 3.2 `apps/api/app/`
| File | Modifica |
|---|---|
| `modules/endpoints/models.py` | Aggiunta property `has_auth_ref` (bool). |
| `modules/endpoints/schemas.py` | `EndpointRead`: rimosso `auth_ref`, aggiunto `has_auth_ref`. |
| `modules/endpoints/service.py` | `test_connection` → `_probe_endpoint` (mock/token_ref/vault), `source.close()` in `finally`. |
| `modules/endpoints/mock_connection.py` | **Rimosso** (logica mock spostata in `MockInventorySource`). |
| `modules/inventory/{models,schemas,service,router}.py` | Nuovo modulo read-only inventory. |
| `core/errors.py` | Aggiunto `UnprocessableError` → HTTP 422. |
| `main.py` | Wiring `inventory_router` + `capabilities_router`. |
| `alembic/versions/0003_inventory_snapshots.py` | Nuova tabella. |
| `alembic/env.py`, `tests/conftest.py` | Import del model inventory. |

### 3.3 `apps/worker/worker/`
| File | Modifica |
|---|---|
| `db.py` | Tabelle Core `endpoints` + `inventory_snapshots` + funzioni `get_job_migration_id`, `get_endpoints_for_migration`, `update_endpoint_capabilities`, `create_inventory_snapshot`. |
| `actors/preflight.py` | Preflight read-only reale (legge inventory, scrive snapshot + capabilities). |
| `Dockerfile`, `pyproject.toml` | Installa `packages/adapters` (+ httpx). |

### 3.4 `apps/web/src/`
`lib/api.ts` (tipi + fetch), `features/migrations/{EndpointCard,EndpointForm,PreflightPanel,MigrationSetupPage}.tsx`, nuovo `InventorySummaryPanel.tsx`.

---

## 4. Schema database

### 4.1 `inventory_snapshots` (Alembic `0003_inventory_snapshots`)
| Colonna | Tipo | Note |
|---|---|---|
| `id` | Integer PK | |
| `migration_id` | Integer FK→migrations CASCADE, index | |
| `endpoint_id` | Integer FK→endpoints CASCADE, index | |
| `endpoint_role` | String(16) | `source` \| `destination` |
| `status` | String(16) | `pending`\|`running`\|`succeeded`\|`failed` (default pending) |
| `captured_at` | DateTime(tz) | |
| `summary` | JSON | **solo conteggi/stato** |
| `data` | JSON | dati normalizzati (mai raw, mai segreti) |
| `error` | Text | |
| `created_at`/`updated_at` | DateTime(tz) | server_default now() |

Reversibile: `downgrade` droppa indici + tabella (verificato up→down→up).

### 4.2 `endpoints.capabilities` (già presente da Sprint 1)
Popolata dal preflight/test-connection con il `CapabilityReport` serializzato.

### 4.3 Coerenza tri-partita
Nomi/tipi colonna **combaciano** tra Alembic (autoritativo), ORM
(`apps/api/.../inventory/models.py`) e tabella Core del worker
(`apps/worker/worker/db.py`), così la DML by-name del worker su Postgres è sicura.

---

## 5. Catalogo funzioni UAPI

Tutte **read-only account-level**, chiamate come `GET /execute/{Module}/{function}`.

| Categoria | UAPI | Stato verifica | Fonte |
|---|---|---|---|
| account/server | `StatsBar::get_stats` | ✅ verificata read-only | api.docs.cpanel.net operation `get_stats` |
| domini | `DomainInfo::list_domains` | ✅ verificata GET read-only | api.docs.cpanel.net operation `list_domains` |
| email | `Email::list_pops` | ✅ pagina "Return email accounts" | api.docs.cpanel.net operation `list_pops` |
| database | `Mysql::list_databases` | ✅ pagina "Return MySQL databases" | api.docs.cpanel.net operation `list_databases` |
| ssl | `SSL::installed_hosts` | ✅ pagina "Return domains with SSL certificate information" | api.docs.cpanel.net operation `installed_hosts` |
| cron | `Cron::list_cron` | ⚠️ funzione UAPI standard; slug operation-page in 404 → **auto-verifica probe-driven a runtime** | UAPI Cron module |
| dns | — | ❌ **non implementato**: nessuna funzione account-level read-only verificata | — |

**Auth** (fonte ufficiale api.docs.cpanel.net/cpanel/tokens):
`Authorization: cpanel USERNAME:APITOKEN`, porta sicura **2083**, pattern
`https://host:2083/execute/Module/function?param=value`.

**Regola d'oro**: lo scanner è **probe-driven**. Ogni funzione che fallisce (non
supportata dal server) marca la relativa capability come non disponibile con una
limitation, invece di far cadere l'intero preflight.

---

## 6. Capability scanner e formato inventory

### 6.1 `CapabilityReport` (salvato su endpoint e snapshot)
```json
{ "source": "cpanel|mock", "can_connect": true, "can_authenticate": true,
  "can_read_account_info": true, "can_read_domains": true, "can_read_email": true,
  "can_read_databases": true, "can_read_cron": true, "can_read_ssl": true,
  "can_read_dns": false, "limitations": ["dns_read_unavailable_or_unsupported"] }
```
Nessuna capability è hardcodata a `true`: deriva dall'esito reale della lettura.

### 6.2 `summary` (solo conteggi; `null` = categoria non letta)
```json
{ "domains_count": 2, "email_accounts_count": 5, "databases_count": 3,
  "cron_jobs_count": 1, "dns_records_count": null, "ssl_items_count": 1,
  "warnings_count": 1 }
```
`warnings_count = 1 (nota DNS) + numero di categorie fallite`.

### 6.3 `data` (normalizzato, mai raw, mai segreti)
- `domains`: `[{domain, type: main|addon|parked|sub}]`
- `email_accounts`: `[{email, domain}]`
- `databases`: `[{name}]`
- `cron_jobs`: `[{minute, hour, day, month, weekday}]` — **mai il comando** (vedi §7.3)
- `ssl`: `[{host}]`
- `account`: `{available: true}` (nessuno stat raw persistito)
- `dns`: `null`
- `warnings`: lista tag (es. `dns_read_unavailable_or_unsupported`)

### 6.4 Semantica di `collect()`
La lettura `DomainInfo::list_domains` funge da **gate connect/auth**:
- `CpanelConnectionError`/`CpanelTimeoutError` → `InventoryError` (snapshot failed, job failed).
- `CpanelAuthError` → `InventoryError`.
- altro `CpanelError` (api/parse/unsupported) → `can_connect/can_authenticate=True`, categoria segnata non disponibile, scan prosegue.
Le altre 5 letture: successo → capability true + normalizza; errore non-fatale → capability false + warning + count null; errore di connessione/timeout → fatale (`InventoryError`).

### 6.5 Semantica di `probe()` (test-connection minimo)
Una sola chiamata `DomainInfo::list_domains`:
- successo o `CpanelApiError`/unsupported → `connected=True, authenticated=True`;
- `CpanelAuthError` → `connected=True, authenticated=False`;
- `CpanelParseError` → `connected=True, authenticated=False` (host raggiunto ma **non è cPanel** — fix review, non si dichiara autenticato);
- connect/timeout → `connected=False`.

---

## 7. Modello di sicurezza

### 7.1 Token
- Il token risolto vive **solo in memoria** per la durata della chiamata.
- Mai in `repr` del client, mai loggato (nessun `logger`/`print` tocca il token o l'header), mai nei messaggi d'errore (costruiti da testo statico + module/function + `errors` server).
- Mai nel DB, negli snapshot, nelle response API.
- Test: `test_repr_does_not_leak_token`, `test_uapi_status_zero_maps_to_api_error_without_token`.

### 7.2 `auth_ref` (debito Sprint 1 corretto)
`EndpointRead` **non** contiene più `auth_ref`: espone solo `has_auth_ref: bool`
(property ORM `auth_ref is not None`, letta via pydantic `from_attributes`).
Il valore `auth_ref` resta nel DB come **riferimento opaco** (mai un segreto raw:
validato allo scheme, raw→422).

### 7.3 Credential resolver + allowlist (fix CRITICAL review)
`resolve_credential("env://VAR")` risolve solo se `VAR` è un **identificatore
uppercase che contiene `CPANEL`** (`^[A-Z][A-Z0-9_]*$` + substring `CPANEL`),
validato **prima** di leggere `os.environ`. Questo impedisce a un chiamante di
nominare un segreto d'ambiente arbitrario (`DATABASE_URL`, `REDIS_URL`, cloud
creds) e farselo inviare come bearer verso un host che controlla.
`vault://`/`secretsmanager://`/`ref://` → `CredentialResolverNotImplemented`.
Il comando cron **non è mai persistito** (spesso contiene segreti: `mysqldump
-pXXX`, `Authorization:`, `user:pass@host`): salviamo solo lo schedule + conteggio.

### 7.4 Residuo di sicurezza noto (documentato, non bloccante nel contesto locale)
L'allowlist riduce il blast radius ma **non** lega un endpoint alla *sua*
variabile. Poiché **l'intera piattaforma non ha autenticazione API** (condizione
pre-esistente di tutti gli sprint, per design del tool localhost mono-operatore),
un chiamante che raggiunge l'API potrebbe comunque far inviare un token cPanel di
un altro endpoint verso un host arbitrario (SSRF/exfil). **Gate obbligatorio
prima di qualsiasi deploy non-locale/multi-tenant**: (1) autenticazione su tutte
le route; (2) validazione `host` con blocco range privati/loopback/link-local;
(3) binding per-endpoint del nome variabile. Verificato in R2 review: fix
applicati chiusi, residuo pre-esistente non introdotto da questo diff.

---

## 8. Tassonomia errori cPanel → HTTP

| Condizione | Errore adapter | Esito test-connection | Esito preflight |
|---|---|---|---|
| DNS/TCP/TLS | `CpanelConnectionError` | connection_status=failed | job failed (source) |
| timeout | `CpanelTimeoutError` | failed | job failed |
| HTTP 401/403 | `CpanelAuthError` | failed | job failed |
| UAPI status=0 / HTTP≥400 | `CpanelApiError` | (per-categoria unavailable) | snapshot con warnings |
| body non-JSON / envelope errato | `CpanelParseError` | failed (non autenticato) | categoria unavailable |
| HTTP 404 su /execute | `CpanelUnsupportedFunctionError` | — | categoria unavailable |
| env var mancante | `CredentialNotFound` | failed(200) + last_error col nome var | job failed |
| scheme non implementato / var fuori allowlist | `CredentialResolverNotImplemented` / `CredentialError` | **422** | job failed |

---

## 9. API reference

### `POST /api/endpoints/{id}/test-connection`
- `auth_type=mock` → probe offline (host con `fail` → failed).
- `auth_type=token_ref` + `env://<...CPANEL...>` → risolve env, probe cPanel reale.
- `auth_type=token_ref` + `vault://` → **422** (`{"detail": "..."}`).
- env var mancante → **200**, `connection_status=failed`, `last_error` col nome variabile.

Response = `EndpointRead` (con `has_auth_ref`, `capabilities`, **senza** `auth_ref`).

### `GET /api/migrations/{id}/inventory`
```json
{ "source": <InventorySnapshot|null>, "destination": <InventorySnapshot|null> }
```
200 anche se vuoto (migrazione senza snapshot); 404 se la migrazione non esiste.

### `GET /api/migrations/{id}/inventory/{source|destination}`
`InventorySnapshotRead` (ultimo snapshot per ruolo); **404** se assente.

### `GET /api/endpoints/{id}/capabilities`
```json
{ "endpoint_id": 1, "connection_status": "connected",
  "last_checked_at": "...", "capabilities": { ...CapabilityReport... } }
```
Tutte le response sono **prive di token/auth_ref/Authorization/secret**.

---

## 10. Flusso preflight worker

```
queued ─▶ running(starting,10) ─▶ validating_endpoints(25)
      ─▶ source_inventory(40): collect → snapshot(succeeded) + capabilities(connected)
      ─▶ destination_inventory(80): collect → snapshot(succeeded) + capabilities(connected)
      ─▶ succeeded(done,100)
```
- Endpoint mancanti (source o destination) → job **failed** subito.
- **Fallimento source** (connessione/auth/credenziali/InventoryError) → snapshot
  source `failed` + endpoint failed + job **failed** + `return` (destination **non
  tentato**).
- Mock: `MockInventorySource` offline deterministico (nessun I/O reale).
- token_ref: `CpanelInventorySource` reale (solo se le env var esistono).
- Ogni fase scrive `job_events` (phase + message + progress); il client cPanel
  viene sempre chiuso (`try/finally source.close()`).

---

## 11. Frontend

- `api.ts`: rimosso `auth_ref` da `Endpoint` → `has_auth_ref`; nuovi tipi
  `Capabilities`, `InventorySummary`, `InventorySnapshot`, `InventoryOverview`;
  `fetchInventory()`.
- `EndpointCard`: `CapabilitiesView` (badge per categoria letta/non letta +
  modalità mock/cPanel + limitazioni).
- `PreflightPanel`: testo aggiornato ("il preflight legge l'inventario").
- `InventorySummaryPanel`: conteggi source/destination + avvisi.
- `EndpointForm`: selettore auth Mock | Token cPanel (`env://`).
- `MigrationSetupPage`: fetch inventory iniziale + refresh durante il polling job.

---

## 12. Matrice di test (83 test)

### Adapter (in `apps/api/app/tests/`, l'API installa `adapters` + httpx)
- `test_cpanel_client.py` (11): URL `/execute/...`, header `cpanel user:TOKEN`,
  https di default, params query, `repr` senza token, 401→auth, status0→api
  (senza token nel messaggio), body non-JSON→parse, envelope errato→parse,
  timeout→timeout, connect→connection, nessun metodo write pubblico.
- `test_credentials.py` (7): env risolve, mancante→NotFound col nome, vault→NotImplemented,
  scheme sconosciuto→error, `env://` vuoto→error, **DATABASE_URL fuori allowlist→error senza leggere env**, lowercase→error.
- `test_inventory_source.py` (9): collect full capabilities+counts, api-error→capability off,
  connect-error→InventoryError, probe auth-failure, snapshot senza chiavi sensibili,
  mock probe/collect, mock fail-host, factory mock, factory token_ref via MockTransport.

### API (in `apps/api/app/tests/`)
- `test_endpoints.py`: debito corretto (`auth_ref` NON in response, `has_auth_ref`),
  has_auth_ref false, + i test Sprint 1 (mock connect/fail, auth_ref opaco/raw→422).
- `test_endpoints_connection.py` (4): token_ref success (fake), auth-failure (fake),
  vault→422, env mancante→failed(200) col nome variabile.
- `test_inventory_api.py` (8): overview vuoto coerente, overview 404 migrazione mancante,
  overview latest-per-role, source endpoint, role mancante→404, snapshot senza secret,
  capabilities riflette test-connection, capabilities 404.

### Worker (`apps/worker/worker/tests/`)
- `test_preflight.py`: job→succeeded (mock endpoints), eventi ordinati, missing-job noop,
  senza-endpoint→failed, actor registrato.
- `test_inventory.py` (5): snapshot source+destination, capabilities salvate su endpoint,
  snapshot senza secret, eventi con fasi inventory, credential-error→job failed (destination non creato).

---

## 13. Gate eseguiti (reali, non dichiarati a vuoto)

| Gate | Comando | Esito |
|---|---|---|
| Scope | `git diff --name-only fork/main \| grep -v '^migration-platform/'` | **vuoto** ✅ |
| Compose | `docker compose config` | OK ✅ |
| API | `cd apps/api && pytest` | **70 passed** ✅ |
| Worker | `cd apps/worker && DRAMATIQ_TESTING=1 pytest` | **13 passed** ✅ |
| Web | `cd apps/web && npm install && npm run build` | OK (tsc+vite) ✅ |
| Alembic | `alembic upgrade head` → `downgrade base` → `upgrade head` | OK ✅ |
| Smoke Docker mock | porte alt 18000/15173/15432/16379 | E2E ✅ |
| Smoke reale cPanel | — | **NON eseguito: credenziali non disponibili** |

**Smoke Docker E2E** (mock): create migration → 2 endpoint mock → preflight →
worker reale processa via Redis→Postgres → job `succeeded`, eventi con fasi
`source_inventory`/`destination_inventory`, snapshot succeeded per ruolo,
capabilities salvate (`can_read_dns=false` non hardcoded), inventory response
senza segreti, cron redatto (solo schedule), web 200.

---

## 14. Review adversariale (R1 → R2)

| # | Severità | Finding | Fix | R2 |
|---|---|---|---|---|
| 1 | CRITICAL (sec) | resolver env accettava qualsiasi variabile → esfiltrazione segreti d'ambiente arbitrari via host attacker | allowlist `CPANEL` prima di `os.environ` | **CHIUSO** (residuo no-auth documentato) |
| 2 | MED/HIGH (sec) | comando cron persistito verbatim → possibili segreti | `_norm_cron` solo schedule; mock aggiornato | **CHIUSO** |
| 3 | HIGH (py) | `httpx.Client` mai chiuso nei call site | `close()` su source + `try/finally` in service e preflight | applicato |
| 4 | MED/HIGH (py) | `probe()` trattava `CpanelParseError` come autenticato | ParseError → `authenticated=false` | applicato |
| 5 | LOW (sec) | `CpanelCredentials` dead code con token in chiaro | rimosso | **CHIUSO** |
| — | MED (py) | `get_engine()` singleton non sincronizzato | **NON corretto**: pre-esistente Sprint 1 → rischio aperto | — |

`code-reviewer`: **APPROVE, 0 bug**. R2 security: *"I'd ship this PR"* nel contesto
localhost-dev; nessuna vulnerabilità bloccante residua **dato quel contesto**.

---

## 15. Runbook — smoke reale cPanel (opzionale)

Da eseguire **solo** con credenziali reali disponibili (nomi variabile che
contengono `CPANEL`, altrimenti l'allowlist li rifiuta):

```bash
export SOURCE_CPANEL_TOKEN=...   export DEST_CPANEL_TOKEN=...
# crea endpoint via API con auth_type=token_ref, auth_ref=env://SOURCE_CPANEL_TOKEN
curl -X POST .../api/migrations/1/endpoints -d '{... "auth_type":"token_ref","auth_ref":"env://SOURCE_CPANEL_TOKEN"}'
curl -X POST .../api/endpoints/1/test-connection   # connection_status atteso: connected
curl -X POST .../api/migrations/1/preflight        # popola inventory reale
curl .../api/migrations/1/inventory                # conteggi reali; nessun segreto
```
Le env var vanno passate ai container `api` e `worker`. **Nessun token va nel DB
o nei log.**

---

## 16. Rischi aperti

1. **Nessuna autenticazione API** (piattaforma-wide): gate obbligatorio prima di
   deploy non-locale (auth + validazione host + binding per-endpoint). Vedi §7.4.
2. `worker/db.get_engine()` singleton lazy non sincronizzato (ereditato Sprint 1):
   due thread al primo avvio potrebbero creare due engine. Impatto basso → lock o
   costruzione all'import in un follow-up.
3. DNS read non implementato; `Cron::list_cron` non catturato dagli slug api.docs
   (probe-driven a runtime).
4. `test-connection` sincrono (nessuno stato `testing` persistito) — ereditato.
5. 2 vuln npm dev-deps pre-esistenti; web = Vite dev-server (no build nginx prod).

---

## 17. Prossimi passi (Sprint 3)

- **Comparison** source↔destination sugli inventory snapshot (delta domini/email/db/cron/ssl).
- Eventuale `migration plan` read-only derivato dal confronto.
- Prerequisito di sicurezza per uscire dal localhost: auth API + host allowlist.

Vedi [`HANDOFF_SPRINT_3.md`](./HANDOFF_SPRINT_3.md).
