# Migration Platform — V2

Piattaforma di migrazione cPanel **greenfield, API-first, operator-first**.

> **Sprint 0 — scaffolding.** Questo repo contiene solo le fondamenta: nessuna
> logica di migrazione reale, nessun adapter cPanel reale, nessuno script bash.
> L'obiettivo è un monorepo che parte con `docker compose up` ed espone API,
> frontend, PostgreSQL, Redis e un worker Dramatiq.

## Documentazione

- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — architettura V2: stack, layout,
  topologia runtime, invarianti, modello dati, modello di esecuzione async.
- [`docs/SPRINT_1_SETUP_PREFLIGHT.md`](docs/SPRINT_1_SETUP_PREFLIGHT.md) — record
  dello Sprint 1: modello dati, contratto API, mock connection, ciclo preflight,
  test, gate, review, rischi aperti.

## Struttura

```text
migration-platform/
  docker-compose.yml        # postgres + redis + api + worker + web
  .env.example
  apps/
    api/                    # FastAPI + SQLAlchemy + Alembic
    worker/                 # Dramatiq (broker Redis)
    web/                    # React + Vite + TypeScript
  packages/
    domain/                 # modelli di dominio puri (Pydantic) — reference
    adapters/               # stub adapter cPanel/SSH/IMAP (nessuna logica reale)
```

## Avvio rapido (Docker)

```bash
cp .env.example .env
docker compose up --build
```

Servizi esposti:

| Servizio  | URL                     |
|-----------|-------------------------|
| API       | http://localhost:8000   |
| API docs  | http://localhost:8000/docs |
| Web       | http://localhost:5173   |
| Postgres  | localhost:5432          |
| Redis     | localhost:6379          |

All'avvio l'API esegue `alembic upgrade head` e poi serve le route.

## Endpoint disponibili (Sprint 0)

| Metodo | Path                     | Descrizione                    |
|--------|--------------------------|--------------------------------|
| GET    | `/health`                | liveness                       |
| GET    | `/api/health`            | liveness (namespace API)       |
| GET    | `/api/migrations`        | elenco migrazioni              |
| POST   | `/api/migrations`        | crea migrazione (solo record)  |
| GET    | `/api/migrations/{id}`   | dettaglio migrazione           |
| GET    | `/api/jobs`              | elenco job                     |

## Endpoint disponibili (Sprint 1 — setup & preflight)

| Metodo | Path                                          | Descrizione                          |
|--------|-----------------------------------------------|--------------------------------------|
| GET    | `/api/migrations/{id}/endpoints`              | elenco endpoint della migrazione     |
| POST   | `/api/migrations/{id}/endpoints`              | crea endpoint source/destination     |
| GET    | `/api/endpoints/{id}`                         | dettaglio endpoint                   |
| POST   | `/api/endpoints/{id}/test-connection`         | test connessione **mock** (no rete)  |
| POST   | `/api/migrations/{id}/preflight`              | avvia il job preflight skeleton      |
| GET    | `/api/migrations/{id}/jobs/current`           | job corrente della migrazione        |
| GET    | `/api/migrations/{id}/events`                 | eventi dei job della migrazione      |

> **Nessun segreto nel DB.** L'endpoint ha `auth_type` (`none`/`token_ref`/`password_ref`/`mock`)
> e `auth_ref`, che è **solo un riferimento opaco** (es. `vault://…`), mai una
> credenziale reale. L'API rifiuta (422) un `auth_ref` che non sia un riferimento.

> **Worker entrypoint.** L'unico comando corretto è `dramatiq worker.main`.
> **Non** eseguire `dramatiq app.core.queue`: nell'API `run_preflight` è solo un
> *producer stub* (il suo body solleva di proposito), serve a fare `.send(job_id)`;
> l'implementazione reale vive nel worker con lo stesso actor-name.

## Sviluppo locale (senza Docker)

### API

```bash
cd apps/api
python -m venv .venv && source .venv/bin/activate
pip install -e ../../packages/domain -e ../../packages/adapters -e .
# test (usano SQLite in-memory, non serve Postgres):
python -m pytest
# dev server (richiede DATABASE_URL o usa il default SQLite):
alembic upgrade head
uvicorn app.main:app --reload
```

### Worker

```bash
cd apps/worker
pip install -e ../../packages/domain -e .
DRAMATIQ_TESTING=1 python -m pytest      # test senza Redis
dramatiq worker.main                     # richiede Redis attivo
```

### Web

```bash
cd apps/web
npm install
npm run dev        # http://localhost:5173
npm run build      # gate di compilazione
```

## Principio architetturale

> **Tutti i job aggiornano PostgreSQL.** Lo stato di un job non deve mai vivere
> solo nella coda: la queue è volatile, Postgres è la fonte di verità. Le tabelle
> `jobs` / `job_events` esistono già in Sprint 0 proprio per questo. L'actor
> dimostrativo `health_check_actor` per ora scrive solo un log — la scrittura su
> Postgres sarà il pattern obbligatorio per ogni job reale della vertical slice
> successiva (Setup → Preflight → Comparison → Plan).
```
