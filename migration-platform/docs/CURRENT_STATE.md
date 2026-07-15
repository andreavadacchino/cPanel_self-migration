# Current State — Migration Platform V2

> Documento vivo. Descrive **cosa è vero adesso**, non cosa è pianificato.
> Aggiornato: 2026-07-15 · `fork/main` = `db8d0c4` · + branch `feat/platform-v2-host-identity-persistence`

Per la direzione architetturale vincolante vedi [`../../docs/ADR_V2_GO_EXECUTOR.md`](../../docs/ADR_V2_GO_EXECUTOR.md)
e [`../../docs/ADR_V2_EXECUTION_OWNERSHIP_RECOVERY.md`](../../docs/ADR_V2_EXECUTION_OWNERSHIP_RECOVERY.md).

---

## ⚠️ Trappola di checkout — leggere prima di scrivere codice

`migration-platform/` è **untracked** sul branch `feat/operator-landing-prune` (e su ogni branch
della WebUI Go). Su quel checkout la directory contiene solo residui di scaffolding Sprint 0:
una migration Alembic, tre tabelle, un actor Dramatiq che scrive una riga di log, adapter che
sollevano `NotImplementedError`.

**La piattaforma reale esiste solo su `fork/main`**: moduli `comparison`, `endpoints`,
`inventory`, `plan`, `executions`, catena Alembic `0001→0011`, cifratura Fernet dei token.
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

**L'autenticazione SSH degli endpoint è persistita, ma non ancora usata.** `endpoints` ha ora le
colonne `ssh_*` (metodo `none|password|private_key` × sorgente `direct|ref`, segreti cifrati Fernet
o riferimenti opachi), settabili via `PUT /api/endpoints/{id}/ssh-credentials` con un bundle
tipizzato. Una chiave privata diretta è **validata crittograficamente** all'inserimento
(`adapters.ssh_keys`, via `cryptography`+`bcrypt`): materiale non parsabile, chiave pubblica,
passphrase errata/mancante o applicata a chiave non cifrata sono rifiutati con 422 generico (mai il
PEM o la passphrase nell'errore), invece di essere accettati e rifiutati dal motore solo all'avvio.
Quattro **CHECK constraint** DB presidiano enum/porta/coerenza-`none` (il worker leggerà le righe
come verità). Impostare/ruotare l'SSH **non tocca** lo stato del probe cPanel
(`connection_status`/`capabilities`/`last_*`): sono capability distinte. È **sola persistenza**:
nessun decrypt, nessun `host.yaml`, nessun `known_hosts`, nessuna connessione — l'adapter SSH resta
uno stub `NotImplementedError`. **L'host-identity trust ora esiste** (il pin della host key, tabella
figlia `endpoint_ssh_host_keys`, migration `0011`; vedi [SSH_HOST_IDENTITY.md](SSH_HOST_IDENTITY.md)),
ma manca ancora tutto il **runtime** (risoluzione dei ref, costruzione di `host.yaml`/`known_hosts`,
subprocess). Il dry-run end-to-end resta quindi **bloccato**.

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

Alembic lineare, single head, `0001 → 0011`. Undici tabelle su Postgres:

```
migrations · jobs · job_events · endpoints
inventory_snapshots · comparison_reports · migration_plans
migration_executions · execution_attempts · endpoint_ssh_host_keys · alembic_version
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

`endpoints` (migration `0009`) porta le colonne `ssh_*` per l'autenticazione SSH — capability
distinta dal token cPanel, default `ssh_auth_method = 'none'` che preserva ogni riga esistente. Solo
persistenza (segreti Fernet o riferimenti opachi, mai in chiaro nell'API). Quattro CHECK constraint
(`ck_endpoints_ssh_*`) presidiano enum, range porta e coerenza `none` — verificate su Postgres reale
(un INSERT con porta 70000 è rifiutato).

`endpoint_ssh_host_keys` (migration `0011`) è il **pin della host key SSH** di un endpoint: la chiave
pubblica che il server presenta, canonicalizzata, con la sua fingerprint OpenSSH `SHA256:`, legata a
uno **snapshot server-side** di `host + ssh_port` al momento del pin. **Sola persistenza + API**
(`GET`/`PUT`/`DELETE /api/endpoints/{id}/ssh-host-key`): nessuna connessione, nessun `ssh-keyscan`,
nessun TOFU, nessun `known_hosts`. Il client invia **solo** la chiave pubblica; host, porta e
fingerprint sono derivati/calcolati **lato server** (`extra='forbid'` rifiuta un host/porta/fingerprint
inviati dal client). Vincoli DB: FK `ON DELETE CASCADE` verso `endpoints`, **unique per endpoint**
(un solo pin attivo), range porta, non-blank su host/tipo/chiave, formato `SHA256:` — verificati su
Postgres reale (introspezione + INSERT invalidi rifiutati). Il pin è **invalidato nella stessa
transazione** quando cambiano `host` o `ssh_port` dell'endpoint (o l'SSH è azzerato a `none`); ruotare
segreto/sorgente/username a coordinate invariate lo **preserva** (la host key è l'identità del server,
indipendente da come ci autentichiamo). La `GET` è **fail-closed**: una riga il cui snapshot non
coincide più con le coordinate correnti dell'endpoint **non** è presentata come identità valida (404).
Dettaglio, threat model e matrice di invalidazione in [SSH_HOST_IDENTITY.md](SSH_HOST_IDENTITY.md).

`execution_attempts` (migration `0010`) modella ownership, lease e recovery di **un** tentativo di
*un* worker su un'execution. Vedi [ADR-002](../../docs/ADR_V2_EXECUTION_OWNERSHIP_RECOVERY.md). Il
servizio `app/modules/executions/attempts.py` è l'unica autorità sul lifecycle e sulla
classificazione degli orfani; **non** avvia subprocess, non apre SSH, non risolve segreti.

- `attempt_number` immutabile e monotono (unique per execution): un nuovo tentativo è una nuova riga,
  mai un riuso.
- **Lease su tempo PostgreSQL.** Ogni decisione di scadenza legge un singolo `clock_timestamp()`
  *dopo* il lock di riga e lo riusa per tutta la transizione (mai `datetime.now()` del processo, mai
  `now()`/`transaction_timestamp()` che si congelano a inizio transazione). SQLite (test
  mono-connessione) usa `now()` e **non** prova concorrenza.
- `writes_started` monotono `false→true` sull'attempt e come aggregato sull'execution, flippati nella
  stessa transazione; prevale su ogni classificazione ottimistica.
- Reconciler (`reconcile_expired_attempts`): attempt scaduto senza write → `interrupted`, con write →
  `partial`, entrambi propagati all'execution; idempotente, **nessun retry automatico**, nessun
  takeover (niente A2 creato all'acquisizione). Il retry tecnico è policy futura ed esplicita.
- Fencing = **solo control-plane**: un worker con lease scaduto non aggiorna più PostgreSQL. Non
  fencia un subprocess Go orfano che scrive da remoto — per questo `partial` non invita mai un retry.
- **Ordine dei lock uniforme**: `migration_execution` poi `execution_attempt`, sempre. Il clock DB
  viene letto **dopo** l'ultimo lock, così un lease che scade mentre l'operazione è bloccata sulla
  riga execution è visto come scaduto (`LeaseExpired`), non mutato con timestamp obsoleto —
  verificato su Postgres con contention reale (due connessioni) su `mark_writes_started` e
  `finish_attempt`.
- **Validazione input fail-closed**: `lease_seconds<=0` → `InvalidLeaseDuration`, `worker_id`
  vuoto/whitespace → `InvalidWorkerIdentity` (nessun errore riporta il token). `IntegrityError`
  classificato come lease conflict solo se il constraint è `uq_execution_one_active_attempt`/
  `uq_execution_attempt_number`; altrimenti propagato.

Vincoli **del database** (non di un servizio):

- `uq_execution_one_active_attempt` — unique parziale su `execution_id WHERE status IN
  ('acquired','running')`: **un solo attempt attivo per execution**. Due worker che acquisiscono in
  gara producono un solo vincitore (lock `FOR UPDATE` sulla riga execution + questo indice come
  backstop) — verificato su Postgres reale con due connessioni/thread.
- Quattro `CHECK` (difesa in profondità contro scritture fuori dal service): `ck_..._status` (enum
  valido), `ck_..._number_positive` (`attempt_number>0`), `ck_..._lease_interval`
  (`lease_expires_at>lease_acquired_at`), `ck_..._finished_when_terminal` (`finished_at` presente se
  terminale) — presenza verificata via introspezione su Postgres reale.

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

**Sul fork esiste ora CI funzionale e obbligatoria.** Il workflow `Platform V2 Required Gates`
(`.github/workflows/platform-v2-gates.yml`) gira su ogni PR e push verso `main` con quattro job —
`platform-postgres` (l'intera suite Python su un **PostgreSQL reale**, fail-closed se la suite PG è
saltata invece che eseguita: `--min-total 300 --pg-min 9 --require-pg-module`), `platform-go-contract`,
`platform-frontend`, `platform-compose` — aggregati dal check `platform-required`. Anche il **Race
Detector** (`race.yml`, check `Go race detector`) è obbligatorio. `main` è **protetto**: entrambi i
context (`Go race detector`, `platform-required`, GitHub App 15368) sono **required**, `strict=true`,
`enforce_admins=true`, force-push e deletion disabilitati. Restano comunque da eseguire
**localmente**, sempre: lo stato `MERGEABLE`/`CLEAN` di GitHub non dice nulla sulla correttezza, e la
CI non copre lo smoke Docker né lo smoke cPanel reale.

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

## Stato dei gate — SSH auth persistence (2026-07-15, `fork/main` = `b326723`)

Eseguiti da un **venv creato nel worktree del branch**, con provenance verificata (`__file__` di
`app`, `domain`, `adapters`, `worker` tutti dentro quel worktree). Un editable install che punta a
un worktree vecchio produce verde falso: è già successo.

| Gate | Esito |
|---|---|
| `pytest` API | 385 passed |
| `pytest` domain | 147 passed |
| `pytest` worker (`DRAMATIQ_TESTING=1`) | 15 passed |
| Alembic up/down/up (SQLite) | OK (0001→0009, batch add-column + CHECK) |
| Alembic `0001→0009` su **Postgres reale**, volume nuovo | OK (+ down→up); 10 colonne `ssh_*`, 4 CHECK, INSERT porta invalida rifiutato |
| `docker compose config -q` | OK |
| `npm run build` | **non eseguito** — il web non è toccato da questa PR |
| Smoke read-only cPanel reale | **NON eseguito** — nessuna credenziale in sessione |

## Stato dei gate — execution attempts/lease/recovery (2026-07-15, branch `feat/platform-v2-execution-attempts`)

Eseguiti da un **venv creato fuori dal worktree**, con provenance verificata (`__file__` di `app`,
`domain`, `adapters` tutti dentro questo worktree).

| Gate | Esito |
|---|---|
| `pytest` API | 428 passed (385 base + 43 nuovi) |
| `pytest` domain | 147 passed |
| `pytest` worker (`DRAMATIQ_TESTING=1`) | 15 passed |
| `pytest` concorrenza **Postgres reale** (`TEST_POSTGRES_URL`) | 9 passed — acquire single-winner (2 thread), unique partial index sotto contention, lease-expiry via `clock_timestamp()`, reconcile concorrente (una volta sola), **lease scade durante l'attesa del lock** (mark + finish, 2 connessioni), CHECK rifiuta righe invalide, `IntegrityError` non-conflict non mascherato, migration up/down/up |
| Race + contention ripetute | 5 ripetizioni, zero flake |
| Alembic up/down/up (SQLite) | OK (0001→0010, incl. `drop_column writes_started`) |
| Alembic `0001→0010` su **Postgres reale**, volume nuovo | OK (+ down→up); introspezione conferma tabella + `writes_started` + 4 `CHECK` + indici |
| `docker compose config` | OK |
| Contratto Go / file `contract` | **non toccati** (verificato: zero file Go/contract nel diff) |
| `npm run build` | **non eseguito** — il web non è toccato da questa PR |
| Smoke read-only cPanel reale | **NON eseguito** — nessuna credenziale in sessione |

I test di concorrenza girano solo con `TEST_POSTGRES_URL` impostato, **non** in `make api-test` di
default — coerente con la convenzione del repo per le proprietà Postgres-only. La CI obbligatoria
(`platform-postgres`) li rende automatici: esegue l'intera `app/tests` su PostgreSQL reale. Per
eseguirli in locale: `make api-test-pg` (ora ogni modulo `*_pg.py`) o `TEST_POSTGRES_URL=… make api-test`.

## Stato dei gate — host identity persistence (2026-07-15, branch `feat/platform-v2-host-identity-persistence`)

Eseguiti da un **venv creato fuori dal worktree**, provenance verificata (`app`/`domain`/`adapters`/
`worker` tutti dentro questo worktree).

| Gate | Esito |
|---|---|
| `pytest` API (SQLite) | 473 passed, 26 skipped (i 26 = suite Postgres-only) |
| `pytest` API su **Postgres reale** (l'intera `app/tests`) | **499 passed, 0 skipped** — replica del gate CI `platform-postgres`; `check_pytest_report.py --min-total 300 --pg-min 9 --require-pg-module` → **GATE OK** |
| di cui suite host-key PG (`*_pg.py`) | 26 passed (9 attempts + 17 host-key): serializzazione lock su `set_ssh_host_key`/`update_endpoint`/`set_ssh_credentials`, nessun pin stale su cambio host/porta (contention deterministica via `pg_stat_activity`), due PUT concorrenti → una sola riga, FK CASCADE, CHECK, unique, introspezione migration, up/down/up |
| `pytest` adapter host-key (`test_ssh_host_keys.py`) | 17 passed — fingerprint verificata contro `ssh-keygen -lf` (known-answer vector), keyscan-form/chiave privata/multi-linea rifiutati |
| `pytest` worker (`DRAMATIQ_TESTING=1`) | 15 passed |
| Alembic up/down/up (SQLite) | OK (0001→0011) |
| Alembic `0001→0011` su **Postgres reale**, DB pristine | OK (up→down→up; introspezione: tabella, FK CASCADE, unique, 5 CHECK, indice; single head `0011`) |
| `docker compose config` | OK |
| `npm run build` | **OK** (gate required; nessun file web toccato) |
| Import provenance (`ci/check_import_provenance.py`) | OK |
| Smoke read-only cPanel reale | **NON eseguito** — nessuna credenziale in sessione; non necessario (persistenza + API, nessuna connessione) |

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
- ~~`execution-spec-v1` accetta un filtro vuoto~~ **RISOLTO** (contratto cross-language): un
  `domain_filter`/`mailbox_filter` **presente** deve essere non vuoto e non whitespace-only, con
  messaggio identico in Go e Python (`invalid field <campo>: must not be blank when present`) e
  quattro fixture nel corpus condiviso `testdata/execution-contract/` che entrambi i validatori
  rifiutano. Nessuna normalizzazione silenziosa: un valore blank è rifiutato, non trimmato ad assente.
  La difesa della piattaforma (`scope_blank_filter`, 422 prima di costruire lo spec) resta come strato
  anticipato. Il fix chiude anche uno spec scritto a mano e passato direttamente al binario.
  *Limite noto, deliberato:* "blank" è definito su un set ASCII fisso (` \t\n\v\f\r`), non su
  `unicode.IsSpace`/`str.strip()` nudi, che divergerebbero fra Go e Python. Un filtro fatto solo di
  whitespace Unicode (NBSP, U+3000, …) è quindi accettato — ma è **fail-closed** nel motore (non
  matcha alcun dominio, run vuoto), non fail-open come la stringa vuota. La parità cross-language è
  garantita per costruzione (verificata empiricamente su 16+ input, zero divergenze).
- **Freschezza e INSERT non sono atomici**: `create_dry_run_execution` legge gli anchor correnti e
  poi inserisce, senza lock. Una comparison che atterra in quella finestra (millisecondi) produce
  un'esecuzione ancorata a un piano appena diventato stale. Innocuo per un `dry_run` (non scrive
  nulla) e improbabile (preflight e comparison durano minuti e sono guidati dall'operatore), **ma
  non accettabile per l'apply**: l'ADR chiede la freschezza ricalcolata *immediatamente prima
  dell'avvio*, e quel ricalcolo è compito del worker, non di questa rotta.
- **La coerenza direct/ref del segreto SSH NON è un vincolo DB.** Le 4 CHECK di `endpoints`
  presidiano enum, range porta e "`none` è vuoto", ma **non** che `method=password/direct` implichi
  `ssh_password_enc IS NOT NULL` (predicato lungo e fragile, escluso di proposito). Una riga di
  quel tipo può quindi esistere se scritta fuori dall'API. **Vincolo per la PR runtime**: il worker
  deve validare ogni riga SSH *fail-closed* — verificare che il segreto atteso dal metodo/sorgente
  sia effettivamente presente — **prima** di decifrare o materializzare qualsiasi cosa, senza
  assumere che il DB lo garantisca.

## Prossimo passo

Fatti (Gruppo A della roadmap): **credenziali SSH degli endpoint** (PR #112, migration `0009`),
**modello attempts/lease/recovery** (PR #114, migration `0010`, [ADR-002](../../docs/ADR_V2_EXECUTION_OWNERSHIP_RECOVERY.md)),
**CI obbligatoria cross-language + Postgres** (PR #115) e **host identity persistence** (questa PR,
migration `0011`). Il fondamento di ownership, recovery e trust dell'host esiste; **la piattaforma
non è ancora pronta per un dry-run end-to-end.**

Prossimo incremento isolato:

1. **SSH runtime resolver + workspace builder** — materializzazione fail-closed di chiave/passphrase/
   password/`known_hosts` con permessi minimi, generazione dell'`host.yaml` senza segreti negli
   artifact, cleanup deterministico; ancora **nessun subprocess**. Il pin di host identity (questa PR)
   è la fonte per il `known_hosts` effimero del container: un `known_hosts` costruito dal pin evita che
   il TOFU del motore degradi ad "accetta qualunque chiave al primo run". **Regola per il runtime**:
   prima di fidarsi di un pin deve ri-verificare che `pin.host == endpoint.host` e
   `pin.port == endpoint.ssh_port` (la coerenza è fail-closed nel service, non un vincolo DB).
2. **Executor packaging + compatibility handshake** — binario Go identificato per digest/versione,
   allowlist di contratto, avvio rifiutato prima del subprocess se incompatibile.

Solo dopo questi si implementano dry-run actor, ingestione eventi/risultato e terminalizzazione dal
subprocess. Il primo apply reale resta **bloccato**: manca un account sacrificabile con accesso SSH
su entrambi i lati. Finché lo smoke non passa, la capability di apply **non compare nella UI**.
