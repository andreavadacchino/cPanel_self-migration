# Current State — Migration Platform V2

> Documento vivo. Descrive **cosa è vero adesso**, non cosa è pianificato.
> Aggiornato: 2026-07-10 · `fork/main` = `5bd60c4`

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

## Cosa la piattaforma NON sa fare — per design, oggi

Nessuna write su cPanel. Nessun DNS, DB, email, cron. Nessun rsync/SSH/IMAP/dump.
Nessun apply/cutover/rollback. **Nessun bottone di esecuzione.** Nessuna autenticazione API.

Il confine è dichiarato in quattro punti coerenti:

- `apps/web/src/features/migrations/MigrationPlanPanel.tsx` — banner «Questo piano è read-only.
  Non esegue modifiche sui server.»
- `apps/api/app/modules/plan/service.py:1` — docstring «no network, no slow».
- `apps/api/app/modules/plan/router.py:1`
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

Alembic lineare, single head, `0001 → 0007`. Otto tabelle su Postgres:

```
migrations · jobs · job_events · endpoints
inventory_snapshots · comparison_reports · migration_plans · alembic_version
```

`jobs` non modella l'esecuzione: `JobStatus` = `pending queued running succeeded failed`.
Non esiste `partial`, quindi oggi non c'è modo di rappresentare "metà destinazione scritta".
Non esistono tabelle di execution, né modellazione di credenziali SSH.

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

## Stato dei gate — Fase 0 (2026-07-10, `fork/main` = `5bd60c4`)

| Gate | Esito |
|---|---|
| `pytest` API | 300 passed |
| `pytest` worker | 15 passed |
| `npm run build` | OK (57 moduli) |
| Alembic up/down/up (SQLite) | OK |
| Alembic `0001→0007` su **Postgres reale** | OK (volume ricreato con `down -v`) |
| Smoke Docker Compose | 5 container, E2E `DELETE` → 204 body-less → riga rimossa |
| Copertura inventory↔comparison | PASS (10 = 10, zero invisibili) |
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

## Prossimo passo

Contratto versionato Platform↔Executor. Vedi l'ADR: la direzione executor→platform **esiste già**
(`internal/events/`) e va versionata; la direzione platform→executor (lo spec di input) va definita.

Il primo apply reale resta **bloccato**: manca un account sacrificabile con accesso SSH su entrambi
i lati. Finché lo smoke non passa, la capability di apply **non compare nella UI**.
