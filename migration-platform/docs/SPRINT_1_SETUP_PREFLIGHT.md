# Sprint 1 — Setup & Preflight skeleton

Record tecnico della prima vertical slice reale della Migration Platform V2.
Riferimento PR: **#87** (`feat(platform): add setup and preflight skeleton`),
base `main`.

## 1. Obiettivo e risultato

Rendere possibile, end-to-end e su dati persistiti, il flusso:

```text
crea migrazione → endpoint source/destination → test connessione (mock)
→ avvia preflight (skeleton) → stato/job/eventi su PostgreSQL → UI setup/preflight
```

Tutto realizzato **senza** migrazione reale, adapter cPanel reale, chiamate di
rete verso cPanel, auth, comparison/plan completi o apply/cutover/DNS.

## 2. Modello dati — tabella `endpoints`

Migrazione Alembic `0002_endpoints` (reversibile). FK verso `migrations` con
`ON DELETE CASCADE`, indice su `migration_id`.

| Campo               | Tipo             | Note                                                |
|---------------------|------------------|-----------------------------------------------------|
| `id`                | int PK           |                                                     |
| `migration_id`      | int FK           | → `migrations.id`, CASCADE, NOT NULL, indicizzato   |
| `role`              | str(16)          | `source` \| `destination`                           |
| `label`             | str(255) null    |                                                     |
| `host`              | str(255)         | NOT NULL                                            |
| `port`              | int              | default **2083**                                    |
| `username`          | str(255)         | NOT NULL                                            |
| `auth_type`         | str(16)          | `none` \| `token_ref` \| `password_ref` \| `mock`   |
| `auth_ref`          | str(255) null    | **riferimento opaco**, mai un segreto reale         |
| `connection_status` | str(16)          | `unknown` \| `testing` \| `connected` \| `failed`   |
| `last_checked_at`   | datetime null    |                                                     |
| `last_error`        | text null        |                                                     |
| `capabilities`      | JSON null        | popolato dal mock probe                             |
| `created_at`        | datetime         |                                                     |
| `updated_at`        | datetime         |                                                     |

**Regola di sicurezza (enforced, non solo convenzione):** non esistono colonne
per segreti. `auth_ref` è validato all'ingresso (vedi §4).

## 3. API

Tutte le response endpoint usano lo stesso schema `EndpointRead`; nessun campo
segreto è mai esposto.

### 3.1 Gestione endpoint

| Metodo | Path                                    | Esito                          |
|--------|-----------------------------------------|--------------------------------|
| GET    | `/api/migrations/{id}/endpoints`        | 200 lista (404 se migrazione mancante) |
| POST   | `/api/migrations/{id}/endpoints`        | 201 endpoint (404/422)         |
| GET    | `/api/endpoints/{id}`                    | 200 endpoint (404)             |
| POST   | `/api/endpoints/{id}/test-connection`   | 200 endpoint aggiornato (404)  |

Payload creazione:

```json
{
  "role": "source",
  "label": "Server sorgente",
  "host": "source.example.com",
  "port": 2083,
  "username": "cpaneluser",
  "auth_type": "mock",
  "auth_ref": null
}
```

### 3.2 Preflight

| Metodo | Path                                    | Esito                               |
|--------|-----------------------------------------|-------------------------------------|
| POST   | `/api/migrations/{id}/preflight`        | 201 Job (404 migrazione, 409 stato) |
| GET    | `/api/migrations/{id}/jobs/current`     | 200 Job corrente (404 se nessuno)   |
| GET    | `/api/migrations/{id}/events`           | 200 lista eventi (404 migrazione)   |

Codici 409 possibili su `POST /preflight`:

- `Preflight requires both a source and a destination endpoint.` — manca un endpoint;
- `A preflight is already queued or running for this migration.` — idempotenza.

## 4. Mock connection

`app/modules/endpoints/mock_connection.py` — funzione pura, **nessuna rete**:

- se `host` contiene la sottostringa `fail` → `connection_status=failed` +
  `last_error` valorizzato;
- altrimenti → `connection_status=connected` + `capabilities` (mock);
- in ogni caso persiste `connection_status`, `last_error`, `capabilities`,
  `last_checked_at` sull'endpoint.

Il vero adapter cPanel (`packages/adapters`) sarà cablato in uno sprint successivo.

### `auth_ref` — validazione (fix da review sicurezza)

`EndpointCreate` applica un `model_validator`:

- `auth_type ∈ {none, mock}` ⇒ `auth_ref` **deve** essere `null`;
- `auth_type ∈ {token_ref, password_ref}` ⇒ `auth_ref` obbligatorio e deve
  iniziare con uno schema di riferimento consentito
  (`vault://`, `secretsmanager://`, `env://`, `ref://`).

Un `auth_ref` che sembra una credenziale grezza (es. `hunter2`) viene rifiutato
con **422** e non viene mai persistito né restituito.

## 5. Ciclo di vita del preflight

```text
              POST /preflight
                   │
   validazioni (migrazione, 2 endpoint, idempotenza)
                   │  crea Job(queued) su Postgres  → 201
                   │  enqueue run_preflight(job_id) → Redis
                   ▼
   worker: execute_preflight(job_id)
      queued
        └─▶ running   phase=starting            progress=10   event
                      phase=validating_endpoints progress=40   event
                      phase=checks               progress=70   event
        └─▶ succeeded phase=done                 progress=100  event
```

Se l'enqueue fallisce dopo il commit, il job passa a `failed` (niente job orfano).
Se il worker incontra un'eccezione, marca il job `failed` con l'errore.

## 6. Confine API ↔ worker

Vedi `ARCHITECTURE.md §7`. In sintesi:

- **API** — `app/core/queue.py`: broker (StubBroker sotto `DRAMATIQ_TESTING=1`,
  altrimenti RedisBroker) + actor stub `run_preflight` (corpo che solleva) +
  `enqueue_preflight(job_id)`.
- **Worker** — `worker/actors/preflight.py`: actor reale `run_preflight` →
  `execute_preflight(job_id, engine)` (funzione pura, engine iniettabile per i
  test) + `worker/db.py` (SQLAlchemy Core minimale su `jobs`/`job_events`).

Nessun import incrociato; contratto = actor-name `run_preflight` + `job_id`.

## 7. Frontend

Routing con `react-router-dom`:

- `/` — `MigrationDashboard`: lista migrazioni + `CreateMigrationForm`; ogni card
  linka al dettaglio.
- `/migrations/:id` — `MigrationSetupPage`: carica migrazione, endpoint, job
  corrente ed eventi; effettua polling del job mentre è in-flight.

Componenti: `CreateMigrationForm`, `EndpointCard`, `EndpointForm`,
`ConnectionStatusBadge`, `JobStatusBadge`, `PreflightPanel`, `JobStatusPanel`,
`JobEventsList`. Il pulsante **Avvia preflight** è abilitato solo quando esistono
sia source sia destination. Stati leggibili, errori leggibili, empty-state chiari;
CSS coerente con i token dello Sprint 0.

## 8. Test

| Suite   | Conteggio | Copertura                                                       |
|---------|-----------|-----------------------------------------------------------------|
| API     | **30**    | crea source/destination, list, 404 endpoint, test-connection success/fail mock, auth_ref (round-trip ref + raw→422 + coerenza), preflight 404/409/201, idempotenza 409, enqueue-fail→job failed, jobs/current, events |
| Worker  | **8**     | actor importabile/registrato, no-network, queued→succeeded, eventi ordinati/progress monotono, job mancante = no-op |

- API: SQLite in-memory + StubBroker.
- Worker: SQLite in-memory (stessa `metadata` del worker) + StubBroker.
- Web: `npm run build` (49 moduli).

## 9. Gate eseguiti (commit `07cc08e`)

| Gate | Esito |
|---|---|
| Isolamento vs base reale `fork/main`=`7074f53` | ✅ 41 file, tutti sotto `migration-platform/` |
| `docker compose config` | ✅ VALID |
| API `pytest` | ✅ 30 passed |
| Worker `pytest` (`DRAMATIQ_TESTING=1`) | ✅ 8 passed |
| `npm run build` | ✅ OK |
| Alembic `upgrade head` + `downgrade base` | ✅ reversibile |
| Stack Docker (porte alt) | ✅ 5/5 up, 0 restart |

> ⚠️ **Attenzione all'isolamento.** `git diff main...HEAD` con il `main` **locale
> stale** dà un falso positivo (mostra i file Go legacy arrivati via `fork/main`).
> La base reale della PR è `fork/main` (`7074f53`): verificare con
> `git diff --name-only fork/main...HEAD`.

## 10. Smoke test end-to-end (Docker, porte alternative)

```bash
cd migration-platform
POSTGRES_PORT=15432 REDIS_PORT=16379 API_PORT=18000 WEB_PORT=15173 \
CORS_ORIGINS=http://localhost:15173 VITE_API_BASE_URL=http://localhost:18000 \
docker compose up --build -d

B=http://localhost:18000
curl -f $B/health && curl -f $B/api/health
curl -sS -X POST $B/api/migrations -H 'Content-Type: application/json' \
  -d '{"name":"Sprint 1 smoke","domain":"smoke.example"}'
curl -sS -X POST $B/api/migrations/1/endpoints -H 'Content-Type: application/json' \
  -d '{"role":"source","host":"source.example.com","port":2083,"username":"sourceuser","auth_type":"mock"}'
curl -sS -X POST $B/api/migrations/1/endpoints -H 'Content-Type: application/json' \
  -d '{"role":"destination","host":"destination.example.com","port":2083,"username":"destuser","auth_type":"mock"}'
curl -sS -X POST $B/api/endpoints/1/test-connection    # → connected
curl -sS -X POST $B/api/endpoints/2/test-connection    # → connected
curl -sS -X POST $B/api/migrations/1/preflight         # → 201 queued
curl -sS $B/api/migrations/1/jobs/current              # → succeeded (dopo il worker)
curl -sS $B/api/migrations/1/events                    # → 4 eventi
curl -I  http://localhost:15173                        # → 200

docker compose down -v
```

Esito osservato: preflight `queued → succeeded` (progress 100, 4 eventi persistiti
su Postgres dal container worker separato); secondo preflight immediato → **409**
idempotenza; `auth_ref` raw secret → **422**, `vault://…` → **201**; CORS runtime
onora l'origin sulla porta alternativa; 0 restart su tutti i container.

## 11. Review adversariale (2 agenti in parallelo)

- **Security** — trovato che `auth_ref` non aveva validazione server-side
  (invariante "solo riferimento opaco" affidata a una docstring) e che il test
  "no secret in response" verificava le **chiavi** del dict, non i valori (falso
  positivo). **Corretto**: validator su `auth_type`/`auth_ref` + test riscritti
  sui valori con casi negativi.
- **Go/Python** — confine API↔worker validato con **Redis reale** (producer/
  consumer), 0 bug CRITICAL. **Corretto**: idempotenza del preflight (409) e job
  marcato `failed` se l'enqueue fallisce (niente orfani); commento di test
  fuorviante sistemato.

Commit: `bb9f2b3` (feature) → `61da17f` (fix sicurezza) → `07cc08e` (fix robustezza).

## 12. Rischi aperti / follow-up per Sprint 2

- Actor-name duplicato API/worker: contratto stringa non type-safe.
- Il worker aggiunge `sqlalchemy` + `psycopg[binary]` (peso immagine) e duplica
  una definizione tabella minimale (subset dello schema Alembic).
- `test-connection` è sincrono lato server: lo stato `testing` è solo ottimistico
  in UI, non persistito.
- Nessun vincolo impedisce più endpoint con lo stesso `role` su una migrazione
  (rilevante quando servirà scegliere "il" source in modo deterministico).
- Nessuno smoke test in CI: la verifica end-to-end è stata eseguita manualmente.
- `npm audit`: 2 vulnerabilità su dev-deps transitive (pre-esistenti da Sprint 0).
- Web servita come Vite dev-server (`npm run dev`), non build statica nginx.
