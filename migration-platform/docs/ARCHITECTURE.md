# Migration Platform V2 — Architettura

> Documento vivo. Descrive lo stato **corrente** della piattaforma (Sprint 0 +
> Sprint 1). Non descrive funzionalità future se non nella sezione _Roadmap_.

## 1. Cos'è (e cosa non è)

Piattaforma di migrazione cPanel **greenfield, API-first, operator-first**. È un
progetto nuovo che vive in `migration-platform/` dentro il repo
`cPanel_self-migration`; **non** è un'evoluzione della vecchia WebUI Go
(`internal/webui/`) e non importa mai codice legacy.

Allo stato attuale la piattaforma sa:

- creare/leggere migrazioni (record su Postgres);
- configurare endpoint **sorgente** e **destinazione** per una migrazione;
- testare la connessione a un endpoint in modalità **mock** (nessuna rete);
- avviare un **preflight skeleton** eseguito da un worker asincrono e vederne
  stato + eventi persistiti su Postgres;
- una UI di setup/preflight.

**Non** fa (per design, in questa fase): migrazione reale, adapter cPanel reale,
chiamate di rete verso cPanel, comparison/plan completi, apply/cutover/DNS,
autenticazione, storage di segreti.

## 2. Stack

| Livello   | Tecnologia                                                        |
|-----------|-------------------------------------------------------------------|
| API       | FastAPI + SQLAlchemy 2.0 + Alembic + pydantic-settings + psycopg  |
| Worker    | Dramatiq (broker Redis) + SQLAlchemy Core                         |
| Frontend  | React 18 + Vite 5 + TypeScript + React Router (CSS puro)          |
| Dati      | PostgreSQL 16                                                     |
| Coda      | Redis 7                                                           |
| Infra     | Docker Compose                                                    |

Regola architetturale sulla coda: **niente Celery/RQ/FastAPI BackgroundTasks**.
L'esecuzione asincrona passa esclusivamente da Dramatiq + Redis.

## 3. Layout del monorepo

```text
migration-platform/
  docker-compose.yml        # postgres + redis + api + worker + web
  Makefile
  README.md
  docs/                     # questo documento + record di sprint
  apps/
    api/                    # FastAPI + SQLAlchemy + Alembic
      app/
        core/               # config, errori, queue (producer Dramatiq)
        db/                 # Base declarative + session
        modules/            # migrations, jobs, endpoints, preflight, health
        tests/              # pytest (SQLite in-memory, no Postgres)
      alembic/versions/     # 0001_initial, 0002_endpoints
    worker/                 # Dramatiq: broker + actors + DB layer proprio
      worker/
        actors/             # health, preflight
        db.py               # SQLAlchemy Core minimale (jobs, job_events)
        tests/              # pytest (SQLite in-memory, StubBroker)
    web/                    # React + Vite + TS
      src/
        components/         # shell + badge riusabili
        features/migrations # dashboard + setup/preflight
        lib/api.ts          # client HTTP tipizzato
  packages/
    domain/                 # modelli Pydantic puri (reference, non wirati)
    adapters/               # stub cPanel/SSH/IMAP (NotImplementedError)
```

Ogni componente Python ha il proprio `pyproject.toml` (setuptools,
`packages.find`). I `Dockerfile` di `api`/`worker` hanno build-context = root del
monorepo per copiare i `packages/`.

## 4. Topologia runtime

```text
        ┌───────────┐        ┌───────────┐
        │   web     │  HTTP  │    api    │
        │ Vite/React│───────▶│  FastAPI  │
        └───────────┘        └─────┬─────┘
                                   │ (1) scrive Job(queued) + endpoint/migration
                                   ▼
                            ┌────────────┐
                            │  postgres  │◀──────────────┐
                            └────────────┘               │ (3) UPDATE jobs / INSERT job_events
                                   ▲                      │
             (2) enqueue run_preflight(job_id)            │
                                   │                      │
                            ┌──────┴─────┐         ┌──────┴─────┐
                            │   redis    │────────▶│   worker   │
                            │  (Dramatiq)│  consume│  Dramatiq  │
                            └────────────┘         └────────────┘
```

Flusso preflight: (1) l'API crea il `Job` **queued** su Postgres e committa; (2)
enqueue del messaggio `run_preflight` su Redis; (3) il worker consuma, esegue lo
skeleton e aggiorna `jobs`/`job_events` **direttamente su Postgres**.

## 5. Invarianti non negoziabili

1. **Postgres è la fonte di verità.** Lo stato di un job non vive mai solo in
   Redis: viene scritto su `jobs`/`job_events` prima e durante l'esecuzione.
2. **Confine API ↔ worker.** L'API non importa mai `apps/worker`; il worker non
   importa mai l'app FastAPI. L'unico contratto condiviso è la stringa
   dell'actor-name (`run_preflight`) e l'argomento `job_id`. Vedi §7.
3. **Nessun segreto nel DB.** La tabella `endpoints` non ha colonne per
   password/token. `auth_ref` è un **riferimento opaco** (es. `vault://…`),
   validato all'ingresso e mai una credenziale reale.
4. **Nessuna rete verso cPanel.** Il test connessione è un mock deterministico;
   il worker skeleton non fa I/O di rete.
5. **SQL DB-agnostico nel worker.** Il worker usa SQLAlchemy Core con parametri
   bindati e timestamp calcolati in Python, così gira identico su SQLite (test)
   e Postgres (prod).

## 6. Modello dati

```text
migrations (0001)              jobs (0001)                    job_events (0001)
  id                             id                             id
  name                           migration_id ─FK(SET NULL)     job_id ─FK(CASCADE)→jobs
  domain                         type            (preflight…)   level
  status  (draft…)               status          (queued…)      phase
  created_at                     current_phase                  message
  updated_at                     progress_percent               progress
                                 created_at/started_at/         created_at
endpoints (0002)                 finished_at
  id                             error
  migration_id ─FK(CASCADE)→migrations
  role            (source | destination)
  label
  host / port(2083) / username
  auth_type       (none | token_ref | password_ref | mock)
  auth_ref        (riferimento opaco, nullable)
  connection_status (unknown | testing | connected | failed)
  last_checked_at / last_error
  capabilities    (JSON, nullable)
  created_at / updated_at
```

Le migrazioni Alembic sono lineari e reversibili: `0001_initial` (migrations,
jobs, job_events) → `0002_endpoints` (endpoints). All'avvio l'API esegue
`alembic upgrade head`.

## 7. Modello di esecuzione asincrona (producer/consumer)

Il punto architetturale più delicato è **come l'API accoda lavoro al worker senza
accoppiarli**.

- **Producer (API)** — `app/core/queue.py` costruisce un broker Dramatiq
  (`RedisBroker`, oppure `StubBroker` se `DRAMATIQ_TESTING=1`) e dichiara un
  *actor stub* con `actor_name="run_preflight"`. Il corpo dello stub **solleva di
  proposito**: esiste solo per fornire `run_preflight.send(job_id)`.
- **Consumer (worker)** — `worker/actors/preflight.py` registra un actor con lo
  **stesso** `actor_name="run_preflight"` sulla stessa queue (`default`). Questo
  è l'unico che esegue davvero.

Dramatiq instrada per nome dell'actor: il messaggio prodotto dall'API viene
consumato dal worker. Nessuno dei due importa il codice dell'altro; il contratto
è la coppia `("run_preflight", job_id)`.

> ⚠️ **Operativo.** L'unico entrypoint corretto del worker è
> `dramatiq worker.main`. **Non** eseguire `dramatiq app.core.queue` (consumerebbe
> i messaggi con lo stub che solleva → job bloccati `queued`).

Trade-off accettato: l'actor-name è duplicato tra i due lati come stringa (non
type-safe). Rinominarlo in un solo lato lascerebbe i messaggi orfani.

## 8. Robustezza del preflight

`POST /preflight` (`app/modules/preflight/service.py`):

1. verifica che la migrazione esista (404 altrimenti);
2. verifica che esistano sia source sia destination (409 altrimenti);
3. **idempotenza**: rifiuta con 409 se esiste già un job preflight `queued`/
   `running` per quella migrazione (un doppio click non crea due run);
4. crea `Job(queued)` su Postgres e committa;
5. enqueue su Redis; **se l'enqueue fallisce** (es. Redis down) il job viene
   marcato `failed` invece di restare orfano `queued`, poi l'errore risale.

Il worker (`execute_preflight`) porta il job `queued → running → succeeded`
scrivendo eventi ordinati per ogni fase (`starting` 10% → `validating_endpoints`
40% → `checks` 70% → `done` 100%), senza rete/cPanel/sleep. In caso di eccezione
inattesa marca il job `failed`.

## 9. Configurazione

Variabili d'ambiente principali (default per sviluppo tra parentesi):

| Variabile            | Servizio | Default                              |
|----------------------|----------|--------------------------------------|
| `DATABASE_URL`       | api,wkr  | sqlite locale / Postgres in Docker   |
| `REDIS_URL`          | api,wkr  | `redis://localhost:6379/0`           |
| `CORS_ORIGINS`       | api      | `http://localhost:5173`              |
| `VITE_API_BASE_URL`  | web      | `http://localhost:8000`              |
| `DRAMATIQ_TESTING`   | test     | `1` → StubBroker (no Redis)          |

Le porte host di Docker Compose sono overridabili
(`POSTGRES_PORT`/`REDIS_PORT`/`API_PORT`/`WEB_PORT`) per evitare conflitti con
servizi locali.

## 10. Strategia di test

- **API** (`apps/api/app/tests`): `pytest` su **SQLite in-memory** (StaticPool),
  nessun Postgres né Alembic. Broker forzato a StubBroker via `DRAMATIQ_TESTING=1`
  nel conftest, così l'enqueue non richiede Redis.
- **Worker** (`apps/worker/worker/tests`): `pytest` con StubBroker; la logica DB
  è testata contro SQLite in-memory usando la stessa `metadata` del worker.
- **Web**: gate di compilazione `npm run build` (`tsc --noEmit` + vite).
- **Integrazione**: `docker compose up` con smoke test end-to-end (vedi il record
  di sprint per la sequenza esatta).

Nota: l'host di sviluppo potrebbe non avere le dipendenze runtime (dramatiq,
sqlalchemy worker-side). I gate host vanno eseguiti in venv isolati che
installano le dipendenze dichiarate nei rispettivi `pyproject.toml`.

## 11. Come eseguire

```bash
# Docker (stack completo)
cd migration-platform
cp .env.example .env
docker compose up --build

# Sviluppo locale: vedi README.md (api / worker / web)
```

## 12. Roadmap

- **Sprint 0** — scaffold (migrazioni come record, health, jobs list). ✅ mergiato.
- **Sprint 1** — setup, endpoint, mock connection, preflight skeleton. ✅ (questa
  documentazione). Vedi `docs/SPRINT_1_SETUP_PREFLIGHT.md`.
- **Sprint 2+** — adapter cPanel reale, comparison, migration plan, apply/cutover.
