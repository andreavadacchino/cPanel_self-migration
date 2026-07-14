# Current State — Migration Platform V2

> Documento vivo. Descrive **cosa è vero adesso**, non cosa è pianificato.
> Aggiornato: 2026-07-15 · `fork/main` = `e89a985`

Per la direzione architetturale vincolante vedi [`../../docs/ADR_V2_GO_EXECUTOR.md`](../../docs/ADR_V2_GO_EXECUTOR.md).

---

## ⚠️ Trappola di checkout — leggere prima di scrivere codice

`migration-platform/` è **untracked** sul branch `feat/operator-landing-prune` (e su ogni branch
della WebUI Go). Su quel checkout la directory contiene solo residui di scaffolding Sprint 0:
una migration Alembic, tre tabelle, un actor Dramatiq che scrive una riga di log, adapter che
sollevano `NotImplementedError`.

**La piattaforma reale esiste solo su `fork/main`**: 179 file, moduli `comparison`, `endpoints`,
`inventory`, `plan`, catena Alembic `0001→0007`, cifratura Fernet dei token.
`origin/main` (upstream `tis24dev`) **non contiene affatto** la directory.

Chi inizia a lavorare dal working tree sbagliato conclude che la piattaforma è un guscio vuoto
e ricostruisce da zero codice già mergiato.

```bash
# sempre, all'inizio di ogni sessione V2
git fetch fork
git worktree add --detach <path> fork/main
```

## Topologia dei remote

| Remote | Repo | Contiene |
|---|---|---|
| `origin` | `tis24dev/cPanel_self-migration` | upstream, **solo** il motore Go legacy |
| `fork` | `andreavadacchino/cPanel_self-migration` | motore Go **+** `migration-platform/` |

Il "main" di ogni task sulla piattaforma è **`fork/main`**. Le PR vanno sempre al fork.

## Due architetture parallele nello stesso repo

Non condividono un solo file. Non importarsi a vicenda è una regola, non un caso.

| | Motore Go legacy | Migration Platform V2 |
|---|---|---|
| Dove | `cmd/`, `internal/` | `migration-platform/` |
| Cosa | CLI di migrazione + WebUI Go (`internal/webui/`) | control plane FastAPI + React |
| Stato | maturo, esegue write reali su cPanel | read-only, non esegue nulla |
| Roadmap | **congelato** (vedi ADR) | attivo |

Le PR **#84** e **#85** (WebUI Go operator-first) sono congelate: sono evoluzioni della WebUI Go,
non della piattaforma, e non devono competere per diventare il control plane.

---

## Cosa la piattaforma sa fare oggi

- Creare/leggere migrazioni (record Postgres).
- Configurare endpoint source/destination, con token cPanel **cifrato at-rest** (Fernet,
  `PLATFORM_SECRET_KEY`); l'API restituisce solo `has_auth_secret`/`has_auth_ref`, mai il segreto.
- Test connessione reale (UAPI) + diagnostica errori (TLS/DNS/refused), opt-out TLS per endpoint,
  normalizzazione host.
- Preflight asincrono (Dramatiq) che raccoglie **inventory read-only reale** via UAPI/API2 e
  persiste snapshot + coverage matrix + capabilities.
- Comparison source↔destination item-level su **10 categorie**.
- Migration Plan **read-only**: proiezione pura di inventory + coverage + comparison.
- **Creare una migration execution `dry_run`** (`POST /api/migrations/{id}/executions`), con i gate
  ricalcolati **lato server**: piano risolto (mai nominato dal client), ancoraggio agli id che
  l'operatore ha visto (`plan.generated_from`), **freschezza** rispetto agli ultimi snapshot e
  comparison *succeeded*, e rifiuto degli scope che l'executor non saprebbe eseguire. L'esecuzione
  nasce `pending` ed è ancorata ai byte esatti dello spec via `spec_sha256`.

## Cosa la piattaforma NON sa fare — per design, oggi

Nessuna write su cPanel. Nessun DNS, DB, email, cron. Nessun rsync/SSH/IMAP/dump.
Nessun apply/cutover/rollback. **Nessun bottone di esecuzione.** Nessuna autenticazione API.

**Nessuna esecuzione parte.** La creazione di una execution è governata, ma **non accoda nulla**:
non esiste ancora un actor che consumi le executions, e una riga in `queued` con niente dall'altro
capo della coda sarebbe uno stato che mente. La execution resta `pending` finché non arriva il
worker che sa onorarla. Nessun `job` viene creato, Redis non trasporta nulla.

**Nessuna credenziale SSH è modellata** (`endpoints` ha solo il token cPanel; l'adapter SSH è uno
stub che solleva `NotImplementedError`). È il prerequisito bloccante del dry-run end-to-end: senza
di essa il worker non può generare l'`host.yaml` che il motore Go richiede a runtime.

Il confine è dichiarato in punti coerenti:

- `apps/web/src/features/migrations/MigrationPlanPanel.tsx` — banner «Questo piano è read-only.
  Non esegue modifiche sui server.»
- `apps/api/app/modules/plan/service.py:1` — docstring «no network, no slow».
- `apps/api/app/modules/plan/router.py:1`
- `apps/api/app/modules/executions/router.py:1` — l'unica non-GET è il create, e non avvia nulla;
  l'invariante è un test (`test_there_is_no_route_that_starts_cancels_or_mutates_an_execution`).
- [`MIGRATION_PLAN_READONLY.md`](MIGRATION_PLAN_READONLY.md)

## Copertura inventory ↔ comparison

Invariante: **ogni categoria letta dall'inventory deve essere visibile alla comparison.**
Una categoria letta ma non confrontata è peggio di una non letta: l'operatore vede "nessuna
differenza" dove esiste un item mancante sulla destinazione.

Stato su `fork/main`: `_LIST_CATEGORIES` (item-diff) e `_COVERAGE_CATEGORIES` in
`packages/domain/domain/comparison_engine.py` contengono **entrambe le stesse 10 chiavi**:

```
domains · email_accounts · databases · mysql_users · cron_jobs
ssl · dns_records · email_forwarders · email_autoresponders · ftp_accounts
```

Fino alla PR #96, `email_forwarders`, `email_autoresponders` e `ftp_accounts` erano lette dalla
coverage ma **invisibili** all'item-diff. Regressione da presidiare a ogni PR che tocchi l'inventory.

## Schema dati

Alembic lineare, single head, `0001 → 0008`. Nove tabelle su Postgres:

```
migrations · jobs · job_events · endpoints
inventory_snapshots · comparison_reports · migration_plans
migration_executions · alembic_version
```

`jobs` non modella l'esecuzione: `JobStatus` = `pending queued running succeeded failed`, e non ha
`partial`. Per questo l'esecuzione vive in `migration_executions` (PR #107), il cui `ExecutionStatus`
include `partial`, `interrupted`, `cancel_requested` — la differenza fra "non è successo niente" e
"metà destinazione è scritta" è l'unica cosa che serve sapere dopo un fallimento a metà.

Due vincoli sono **del database**, non di un servizio:

- `uq_migration_executions_active_mutating` — unique parziale: **una sola esecuzione mutante attiva
  per migrazione**. I `dry_run` sono esclusi di proposito (non toccano nulla, e se ne deve poter
  lanciare uno rileggendo il report del precedente). Verificato su Postgres reale: 16 dry-run
  concorrenti riescono tutti, con `run_id` distinti.
- FK `RESTRICT` su `plan_id` / snapshot / comparison — il piano dietro un'esecuzione non è
  cancellabile. **Non testabile su SQLite** (`PRAGMA foreign_keys` off): verificato su Postgres.

Nessuna modellazione di credenziali SSH.

## Confine API ↔ worker

L'API **non importa** il worker. Dichiara un producer stub Dramatiq e fa `.send(job_id)`; il worker
registra il consumer omonimo. Il contratto è la sola stringa del nome dell'actor.

**PostgreSQL è la fonte di verità. Redis è solo trasporto.** Nessuno stato vive nella coda.

## Sicurezza — stato reale

- Token cPanel: cifrati at-rest (Fernet); mai in DB in chiaro, snapshot, response, log.
- L'handler custom `RequestValidationError` (`app/core/errors.py`) **rimuove il campo `input`** dai
  422 di FastAPI: senza di esso il token in chiaro tornerebbe nella response.
- Comando cron **mai persistito** (contiene spesso segreti): solo schedule + `command_present`.
- Hash delle password email = **segreti**. Mai in DB, API, eventi, log, artifact.

**Nessuna autenticazione API.** Conseguenza vincolante: la piattaforma resta **localhost e
mono-operatore** finché auth, autorizzazione, protezione SSRF e host allowlist non esistono.

---

## Gate di qualità

**Sul fork non esiste CI funzionale.** `gh pr checks` mostra solo `Sourcery review: skipping`.
Lo stato `MERGEABLE`/`CLEAN` di GitHub **non dice nulla sulla correttezza**. Ogni gate va eseguito
localmente, sempre.

```bash
make api-test      # pytest apps/api
make worker-test   # DRAMATIQ_TESTING=1 pytest apps/worker
make web-build     # tsc --noEmit && vite build
make config        # docker compose config
make up            # smoke completo
```

Due trappole verificate sul campo:

1. **`docker compose up` riusa il volume `pgdata`.** Se il DB è già a `0007`, Alembic non applica
   nulla e il log non prova niente. Per validare la catena serve `docker compose down -v` prima.
2. **La versione di FastAPI risolta nell'immagine non è il minimo dichiarato.** Il `pyproject`
   dichiara `fastapi>=0.111`; l'immagine risolve una versione molto più recente. Bug che si
   manifestano solo su `0.111.1` (es. `204` + `response_model` truthy) non compaiono nello smoke.

## Stato dei gate — Fase A/1 (2026-07-15, `fork/main` = `e89a985`)

Eseguiti da un **venv creato nel worktree del branch**, con provenance verificata (`__file__` di
`app`, `domain`, `adapters`, `worker` tutti dentro quel worktree). Un editable install che punta a
un worktree vecchio produce verde falso: è già successo.

| Gate | Esito |
|---|---|
| `pytest` API | 356 passed |
| `pytest` domain | 132 passed |
| `pytest` worker (`DRAMATIQ_TESTING=1`) | 15 passed |
| Alembic up/down/up (SQLite) | OK |
| Alembic `0001→0008` su **Postgres reale**, volume nuovo | OK (+ down→up) |
| Concorrenza su **Postgres reale** | 16 create dry-run concorrenti: 16 OK, 16 `run_id` unici |
| FK `RESTRICT` su **Postgres reale** | regge: il piano dietro un'esecuzione non è cancellabile |
| `docker compose config -q` | OK |
| `npm run build` | **non eseguito** — il web non è toccato da questa PR |
| Smoke read-only cPanel reale | **NON eseguito** — nessuna credenziale in sessione |

---

## Debito noto e rischi aperti

- **Nessuna auth API** (pre-esistente, per design). Blocca qualunque deploy non-locale.
- `fetchCurrentJob` / `fetchComparison` in `apps/web/src/lib/api.ts` hanno ancora lo *swallow* dei
  500 corretto solo su `fetchPlan` dalla PR #100: un 500 transitorio durante il polling del
  preflight può fermarlo silenziosamente, mostrando "nessun job" invece di un errore.
- `worker/db.get_engine()` è un singleton lazy non sincronizzato.
- Nessun test runner frontend.
- `ready_steps` del piano sono per-categoria, non per-item.
- Il prefisso MySQL è derivato dal nome, non dall'username cPanel esplicito nello snapshot.
- **`execution-spec-v1` accetta un filtro vuoto** (`"domain_filter": ""`) in entrambe le lingue: il
  contratto ne controlla il tipo, non il contenuto. Il motore legge `OnlyDomain: ""` come *nessun
  filtro*, quindi uno spec del genere **allarga silenziosamente lo scope all'intero account**. La
  piattaforma lo rifiuta (`scope_blank_filter`), quindi nessuno spec generato da qui può contenerne
  uno — ma uno spec scritto a mano e passato al binario no. **Il fix va nel contratto** (Go + Python
  + corpus `testdata/execution-contract/`): è la prossima PR di contratto.
- **Freschezza e INSERT non sono atomici**: `create_dry_run_execution` legge gli anchor correnti e
  poi inserisce, senza lock. Una comparison che atterra in quella finestra (millisecondi) produce
  un'esecuzione ancorata a un piano appena diventato stale. Innocuo per un `dry_run` (non scrive
  nulla) e improbabile (preflight e comparison durano minuti e sono guidati dall'operatore), **ma
  non accettabile per l'apply**: l'ADR chiede la freschezza ricalcolata *immediatamente prima
  dell'avvio*, e quel ricalcolo è compito del worker, non di questa rotta.

## Prossimo passo

1. **Credenziali SSH degli endpoint** — prerequisito bloccante: senza, il worker non può generare
   l'`host.yaml` che il motore richiede a runtime, e il dry-run end-to-end non esiste. Vanno
   modellate come capability distinta dal token cPanel (ADR: `cpanel_api_access` ≠
   `ssh_account_access`), cifrate at-rest come il token, con un `known_hosts` deterministico —
   in un container `~/.ssh/known_hosts` è effimero e il TOFU del motore degrada ad "accetta
   qualunque chiave al primo run".
2. **Worker + subprocess**: `pending → queued`, dispatch del solo execution id, workspace privata
   per run (il bridge **rifiuta** una `--output-dir` già usata, anche su retry), verifica della
   versione del binario, ingestione incrementale di `execution-event-v1` e del risultato,
   terminalizzazione atomica, cleanup dei file temporanei.

Il primo apply reale resta **bloccato**: manca un account sacrificabile con accesso SSH su entrambi
i lati. Finché lo smoke non passa, la capability di apply **non compare nella UI**.
