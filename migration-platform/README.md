# Migration Platform — V2

Piattaforma di migrazione cPanel **greenfield, API-first, operator-first**.

> **Stato: vertical slice fino al readiness report dei writer.** Endpoint,
> inventari, comparazione, checklist, planner, simulazione persistente e analisi
> read-only dei gap sono operativi. I writer reali restano deliberatamente disabilitati.

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
    adapters/               # client Python cPanel; boundary SSH/IMAP preparati
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
| Postgres  | porta host definita da `POSTGRES_PORT` (`55432` nell'ambiente pilota) |
| Redis     | localhost:6379          |

All'avvio l'API esegue `alembic upgrade head` e poi serve le route.

## Endpoint disponibili

| Metodo | Path                     | Descrizione                    |
|--------|--------------------------|--------------------------------|
| GET    | `/health`                | liveness                       |
| GET    | `/api/health`            | liveness (namespace API)       |
| GET    | `/api/migrations`        | elenco migrazioni              |
| POST   | `/api/migrations`        | crea migrazione (solo record)  |
| GET    | `/api/migrations/{id}`   | dettaglio migrazione           |
| GET    | `/api/jobs`              | elenco job                     |
| GET    | `/api/migrations/{id}/endpoints` | endpoint sorgente/destinazione |
| POST   | `/api/migrations/{id}/endpoints` | crea un endpoint               |
| PATCH  | `/api/endpoints/{id}` | modifica un endpoint                |
| PATCH  | `/api/endpoints/{id}/credentials` | sostituisce il token       |
| POST   | `/api/endpoints/{id}/test-connection` | prova autenticazione UAPI |
| DELETE | `/api/endpoints/{id}` | elimina un endpoint                 |
| POST   | `/api/migrations/{id}/preflight` | acquisisce i due inventari  |
| GET    | `/api/migrations/{id}/jobs/current` | ultimo job della migrazione |
| GET    | `/api/migrations/{id}/events` | eventi persistenti del job       |
| GET    | `/api/migrations/{id}/inventory` | ultimi snapshot per ruolo     |
| POST   | `/api/migrations/{id}/comparison` | genera una comparazione      |
| GET    | `/api/migrations/{id}/comparison` | ultima comparazione           |
| GET    | `/api/migrations/{id}/manual-tasks` | checklist manuale           |
| PATCH  | `/api/manual-tasks/{id}` | aggiorna lo stato operativo       |
| POST   | `/api/manual-tasks/{id}/verify` | verifica su nuove evidenze    |
| POST   | `/api/migrations/{id}/plan` | genera il piano senza scritture   |
| GET    | `/api/migrations/{id}/plan` | legge l'ultimo piano              |
| POST   | `/api/migrations/{id}/writer-readiness?plan_id={plan_id}` | genera readiness read-only |
| GET    | `/api/migrations/{id}/writer-readiness` | legge l'ultimo readiness report |
| POST   | `/api/migrations/{id}/executions` | crea preview dry-run selettiva |
| GET    | `/api/migrations/{id}/executions/latest` | ultimo execution run |
| GET    | `/api/executions/{id}` | dettaglio e audit del run |
| POST   | `/api/executions/{id}/confirm` | conferma forte e rivalida destinazione |
| POST   | `/api/executions/{id}/run` | esegue soltanto la simulazione |
| POST   | `/api/executions/{id}/cancel` | annulla un run non terminale |
| POST   | `/api/executions/{id}/dispatch` | avvia un run reale (disabilitato di default) |

## Credenziali cPanel

I token diretti vengono cifrati con Fernet prima del salvataggio e non sono mai
restituiti dalle API. Configurare una chiave persistente prima di usarli:

```bash
python -c "from cryptography.fernet import Fernet; print(Fernet.generate_key().decode())"
```

Salvare il risultato in `CREDENTIAL_ENCRYPTION_KEY`. La stessa chiave deve essere
disponibile dopo ogni riavvio: perderla rende illeggibili i token già salvati.

È supportato anche `auth_type=token_ref` con riferimenti `env://NOME_VARIABILE`.
In questo caso il segreto non viene scritto nel database.

Il test connessione usa esclusivamente UAPI account-level:
`Variables::get_user_information`. Un esito positivo certifica connessione,
autenticazione e lettura delle informazioni account; le altre capability restano
false finché i relativi probe del preflight non sono stati eseguiti.

L'adapter supporta entrambe le forme di risposta osservate nelle versioni cPanel:
la forma moderna con `status`/`data` al livello principale e la forma legacy
incapsulata in `result`. Questo evita falsi errori di autenticazione su server che
restituiscono HTTP 200 e `status: 1` senza il wrapper.

### Boundary cPanel hardenato (`packages/adapters/adapters/cpanel`)

Il client cPanel è un *boundary* tipizzato con transport HTTP condiviso, timeout
espliciti, retry sicuro e redazione dei segreti. Superficie pubblica:

- `CpanelClient.read(SafeRead)` — lettura read-only, ritentabile su errori
  transitori. `write(DestinationWrite)` — scrittura sulla destinazione,
  **disabilitata per default** e mai ritentata se non idempotente.
- `safe_read(module, function, params, api_version="uapi"|"api2")` e
  `destination_write(..., idempotent=False)` costruiscono operazioni validate.
  Reads e writes sono tipi distinti: un writer non può usare per errore una
  primitiva di lettura come scrittura o viceversa.
- Le convenience `execute` / `api2` / `ping` restano invariate per i collector.
- Ogni chiamata restituisce un `CpanelResult` con `payload` + `CpanelCallAudit`
  redatto (`as_evidence()` è JSON-safe e privo di segreti).

**Timeout** — `CpanelTimeouts(connect, read, write, pool)`, default
`10/30/30/10s`. Un `timeout_seconds` legacy viene distribuito su tutte le fasi.

**Retry** — `RetryPolicy(max_attempts=3, base_delay=0.2, max_delay=5.0,
multiplier=2.0, jitter_ratio=0.25, retry_idempotent_writes=False)`. Backoff
esponenziale con jitter deterministico applicato **solo ai safe read** e ai casi
transitori dimostrabili (timeout, connessione/TLS, HTTP 429/5xx). Le scritture
non idempotenti non vengono mai ritentate; una scrittura idempotente è ritentata
solo se `retry_idempotent_writes=True`. `read`/`write` accettano un
`threading.Event` di `cancel` verificato prima di ogni tentativo e del backoff.

**Gerarchia di errori (senza segreti)** — `CpanelAuthError` (401/403),
`CpanelUnsupportedError` (funzione non supportata), `CpanelRateLimitError`
(429/503, transitorio), `CpanelConnectionError` (timeout/connessione/TLS),
`CpanelInvalidResponseError` (JSON malformato o schema inatteso, **fail-closed**),
`CpanelApplicationError` (rifiuto applicativo con HTTP 200),
`CpanelConflictError` (risorsa già esistente), `CpanelCancelledError`,
`CpanelWriteDisabledError`.

**Segreti e TLS** — il token non compare mai in `repr`, log, eccezioni o audit
(`Field(repr=False)` sulle credenziali; ogni messaggio passa dalla redazione, che
rilegge il token corrente così una rotazione resta coperta). TLS è verificato per
default; disabilitarlo richiede `verify_tls=False` con `tls_override_reason`
esplicito, registrato nell'audit senza esporre segreti. Modulo/funzione sono
validati (`^[A-Za-z0-9_]+$`) e l'`host` delle credenziali è validato (niente
userinfo `@`, spazi, CRLF o schema) come difesa in profondità contro l'invio
dell'header `Authorization` a un host non previsto. Le **letture** viaggiano in
query string (nessun segreto); le **scritture** usano POST con i parametri nel
body, così un valore sensibile (es. password di una nuova casella) non finisce
mai nell'URL di access-log o proxy intermedi. Le risposte ambigue — JSON non
oggetto, `status` mancante, envelope API2 privo di `error`/`event`/`data` —
falliscono **fail-closed** con `CpanelInvalidResponseError`, mai come successo
vuoto.

### Operazioni domini e regole di sicurezza (B3a)

`packages/adapters/adapters/cpanel/domains.py` aggiunge operazioni tipizzate per i
domini sopra il boundary B1: `read_domains` / `read_single_domain` (via `SafeRead`,
parsing di `DomainInfo::domains_data` in `DomainRecord` con tipo, docroot e
internal label, **fail-closed** su payload malformato) e `build_create`, che
produce una `DestinationWrite` **non idempotente** per addon/subdomain/alias. Un
tipo non creabile account-level (es. dominio principale) solleva: va classificato
come attività manuale, senza fallback WHM. Reads e creates sono tipi distinti, e
le create restano **irraggiungibili dal runtime** finché B3b non le collega dietro
il doppio gate `DOMAIN_WRITER_MODE` + `REAL_EXECUTION_MODE` (entrambi disabilitati
per default).

`apps/api/app/modules/executions/domain_rules.py` contiene le regole pure che
decidono se una create additiva è sicura, senza I/O né segreti:

- `normalize_domain` — folding di case, trailing dot e IDNA, con rifiuto di label
  vuoti/troppo lunghi e caratteri che potrebbero uscire dall'host previsto;
- `validate_docroot` — blocca traversal (`..`), home estranee, `~`, backslash,
  byte di controllo e path che normalizzano fuori dalla home dell'account;
- `decide_additive` — su una **lettura live** (fresh read) restituisce
  `create` / `already_present` / `blocked` / `unsupported`: un dominio equivalente
  è un no-op verificato; uno con tipo/owner/label/docroot diverso è bloccato
  (nessun overwrite implicito); collisioni di internal label o overlap di docroot
  bloccano; una collisione comparsa dopo lo snapshot è rilevata perché la
  decisione opera sui record live. Le decisioni `create` portano `compensation`
  metadata redatti per una futura rimozione manuale controllata.

### Boundary SSH: contratto, host-key, esecuzione comandi (B2a)

`packages/adapters/adapters/ssh` sostituisce lo stub `SshClient.run` con un boundary
tipizzato per l'**esecuzione di comandi** SSH verificata su host key. Lo streaming,
lo stdin e il backpressure arrivano in **B2b**; qui non c'è ancora trasferimento
file/database/posta e nessun writer reale è collegato.

Proprietà di sicurezza:

- **Sorgente strutturalmente read-only.** `open_source_read_session` restituisce
  una `SshReadSession` che non espone alcuna primitiva di write/stdin;
  `open_destination_write_session` restituisce una `SshWriteSession` distinta le cui
  scritture (`run_write`) restano **disabilitate per default** (`allow_writes=False`)
  e richiedono anche una destinazione verificata.
- **Nessuna shell arbitraria.** Un comando si costruisce solo con `command(program,
  *args)`: il programma è validato da whitelist e gli argomenti sono citati con
  `shlex.join`, così un metacarattere (`$(...)`, `;`, `|`) viene consegnato letterale
  e non può iniettare un secondo comando. Non esiste un entry point a stringa grezza.
- **Host key verificata prima dell'autenticazione.** La verifica avviene sul
  `KnownHostsStore` persistente **prima** di inviare qualsiasi credenziale: host
  sconosciuto → rifiutato in modalità `strict` (default); `accept_new` (override
  esplicito e **audibile**, la chiave viene registrata) accetta solo host nuovi; una
  **host key cambiata è sempre rifiutata** in entrambe le modalità. Nessun
  `AutoAddPolicy` silenzioso. Il fingerprint `SHA256:` è sempre nell'audit.
- **Segreti mai esposti.** Password/chiave/passphrase hanno `repr=False`, sono
  escluse da repr, errori (redazione via `redact`), risultati e audit; l'audit
  registra solo il metodo di auth e il fingerprint, mai la credenziale.
- **Output limitato.** stdout/stderr sono separati e limitati (`OutputLimits`); i
  byte oltre il cap sono scartati (non bufferizzati) e viene segnalato `truncated`,
  impedendo l'esaurimento di memoria.
- **Timeout e cancellazione.** Timeout `connect`/`command`/`idle` sono passati al
  transport; la cancellazione cooperativa (`threading.Event`) è verificata prima e
  durante il comando e chiude il canale. `close()` è idempotente.
- **Retry solo su connect.** Solo la fase di connect (errori transitori) viene
  ritentata con backoff prima di qualsiasi comando; **un comando non viene mai
  ritentato** (nessun replay di una scrittura o di uno stream parziale).

Dipendenza scelta: **paramiko** (libreria SSH2 pure-Python matura e mantenuta,
compatibile con il runtime ≥3.11). È usata solo dal backend di trasporto reale
(`paramiko_backend.py`, importato lazy) che apre la `Transport` a basso livello per
poter leggere e verificare la host key **prima** dell'auth; tutta la logica di
policy/sicurezza vive in `client.py`, testato al 99% con un **fake backend
deterministico** (`fakes.py`) — nessun test contatta un server reale.

Test mirati (branch coverage):

```bash
cd packages/adapters && PYTHONPATH=. python -m pytest adapters/ssh/tests -q \
  --cov=adapters/ssh --cov-report=term-missing --cov-branch
```

### Motore di streaming source→destination (B2b-i)

`adapters/ssh/streaming.py` aggiunge il motore `pump(source, sink, options, …)` che
copia byte da un `ByteSource` (la sorgente **produce solo byte**) verso uno
`StdinSink` (stdin di destinazione) con **backpressure reale**: scrive un chunk per
intero prima di leggere il successivo, così un consumer lento arresta naturalmente
il producer. Tiene in memoria **un solo chunk** (`chunk_size`, high-water mark),
quindi la memoria massima è indipendente dalla dimensione totale — nessuna coda non
limitata. Gestisce short write (completa il chunk senza perdita), chiusura stdin a
EOF, stderr bounded/troncato di entrambi i lati, exit code/signal di entrambi i
lati, conteggio byte e una progress callback **rate-limited e senza payload**. La
cancellazione cooperativa è verificata prima dello start, durante read, durante una
write bloccata e nell'attesa exit; timeout distinti `start`/`idle`/`total`/`close`
su clock monotòno iniettabile. Un timeout/cancel/interruzione restituisce un
`StreamResult` **parziale tipizzato** con byte trasferiti e lato del guasto; un
flusso iniziato **non viene mai ritentato**. Entrambi i canali sono chiusi
esattamente una volta (destinazione drenata anche se la sorgente fallisce). Dati
trasferiti e segreti non compaiono mai in log/errori/audit/progress. Il wiring dei
ruoli sulle sessioni (`start_stdout`/`start_stdin` autorizzato) e il backend
paramiko di streaming sono in **B2b-ii**.

### Stato di implementazione

| Area | Stato |
|------|-------|
| CRUD endpoint | Funzionante |
| Token cifrati / riferimenti env | Funzionante |
| Test autenticazione cPanel UAPI | Funzionante |
| Capability per categoria | Prima versione UAPI funzionante |
| Inventario sorgente/destinazione | Funzionante, snapshot persistenti |
| Comparazione | Funzionante sulle categorie coperte |
| Checklist manuale | Generata e persistita dalla comparazione |
| Esecutore dry-run | Funzionante, nessuna scrittura reale |
| Readiness writer reali | Report persistente read-only, nessun dispatch |
| Boundary SSH (B2a) | Esecuzione comandi verificata host-key; non collegato al dispatch |
| Motore streaming SSH (B2b-i) | `pump()` backpressured/bounded/cancellabile; wiring sessioni+paramiko in B2b-ii |
| Sessioni streaming SSH (B2b-ii) | `start_stdout` solo read/source; `start_stdin` solo destination write autorizzata; transport Paramiko incrementale |
| Email category pipeline (B4e-iii-b) | Readiness/comparazione evidence-bound per le 5 categorie email |
| Email phase registry (B4e-iii-c-i) | Typed registry e resolver evidence-bound per le 5 categorie |
| Email gateway runtime (B4e-iii-c-ii) | Client factory, gateway, executor, backup binding — non cablato al worker |
| Email worker coordinator (B4e-iii-c-iii-a) | Orchestratore deterministico categorie email, terminal-agnostic |
| Email dispatch wiring (B4e-iii-c-iii-b) | Coordinatore cablato a `worker_start`, terminazione atomica, 6 categorie implementate, C3 sbloccato |
| Migrazione dati/configurazioni | Writer reali non abilitati di default; crash/resume attempt `running` resta in C4 |

## Copertura del preflight

Il preflight esegue letture account-level e salva uno snapshot separato per
sorgente e destinazione. Ogni categoria conserva metodo, esito, conteggio,
messaggio ed evidenza che la lettura è read-only.

| Categoria | Lettura |
|-----------|---------|
| Account | `Variables::get_user_information` |
| Domini | `DomainInfo::list_domains` (enumerazione) |
| Contratto domini | `DomainInfo::domains_data` (dettaglio ricco) → `domains_contract` |
| Caselle email | `Email::list_pops_with_disk` |
| Database | `Mysql::list_databases` |
| Utenti MySQL | `Mysql::list_users` |
| Grant MySQL | `Mysql::get_privileges_on_database` per coppia utente/database |
| DNS | `DNS::parse_zone` |
| Certificati | `SSL::list_certs` |
| Forwarder | `Email::list_forwarders` |
| Autoresponder | `Email::list_auto_responders` per dominio + `Email::get_auto_responder` per indirizzo |
| FTP | `Ftp::list_ftp_with_disk` |
| Filtri email | `Email::list_filters` per account e casella |
| Mailing list | `Email::list_lists` |
| Redirect | `Mime::list_redirects` |
| PHP | `LangPHP::php_get_vhost_versions` |
| PostgreSQL | `Postgresql::list_databases` |
| Subaccount | `UserManager::list_users` |
| Cron | API 2 `Cron::listcron` (non esiste equivalente UAPI) |

Gli esiti possibili sono `succeeded`, `empty`, `partial`, `unsupported`,
`unavailable`, `failed` e `unverified`. Una chiamata fallita non produce mai un
conteggio zero. Tutte le categorie elencate nella tabella hanno un collector.

### Contratto domini ricco (`domains_data`, task B3c-i)

Il writer reale (B3b-ii) ha bisogno di record domini tipizzati (tipo, docroot,
internal label), non della sola lista nomi di `list_domains`. Il collector legge
quindi il dettaglio account-level `DomainInfo::domains_data` **in sola lettura**
(SafeRead, adapter B3a) e persiste nello snapshot un envelope versionato sotto la
chiave dedicata `data["domains_contract"]` (**non** `data["domains_data"]`, che il
writer `_source_domain_records` interpreta ancora nella shape grezza cPanel:
occuparla con l'envelope lo farebbe misparsare a elenco vuoto — il bridge al
writer è compito di B3c-ii), riconciliato contro l'enumerazione `list_domains`,
senza mai inventare valori:

```jsonc
{
  "version": 1,
  "status": "succeeded",              // vedi stati sotto
  "method": "UAPI DomainInfo::domains_data",
  "account": "<account cPanel>",
  "records": [{
    "normalized": "app.example.test", // IDNA/case-folded, null se non parseabile
    "raw": "app.example.test",        // spelling grezzo dal payload
    "type": "subdomain",              // main|addon|subdomain|alias, null se ignoto
    "docroot": "/home/acct/app",      // null se non verificabile
    "internal_label": "app",          // null se non verificabile
    "parent": "example.test",         // dominio proprietario proper-suffix, o null
    "account": "<account cPanel>",
    "method": "UAPI DomainInfo::domains_data",
    "complete": true,                 // false se mancano campi richiesti
    "issues": []                      // es. missing_docroot, type_conflict, ...
  }],
  "reconciliation": {                 // list_domains vs domains_data
    "enumerated": 4, "detailed": 4,
    "missing_detail": [], "unexpected_detail": [],
    "duplicates": [], "type_conflicts": []
  }
}
```

Stato del contratto (`coverage["domains_contract"]`):

- **`succeeded`** — ogni dominio enumerato ha un dettaglio completo e coerente
  (zero record = account senza domini, *non* un errore);
- **`partial`** — un dominio è enumerato senza dettaglio, oppure un record manca
  di un campo richiesto (docroot/label/parent) e non è eleggibile;
- **`ambiguous`** — un dettaglio non enumerato, un duplicato o un tipo
  conflittuale richiede revisione;
- **`failed`** — la lettura `domains_data` è fallita o malformata: **mai** un
  elenco vuoto assunto;
- **`unavailable`** — l'enumerazione stessa non è leggibile.

Un campo non verificabile resta `null` con un `issue` esplicito; una failure non
diventa mai «nessun dominio». Il contratto distingue in modo affidabile «account
senza domini» (`succeeded`+0) da «non siamo riusciti a leggere i domini»
(`failed`/`unavailable`).

**Compatibilità snapshot legacy:** uno snapshot precedente privo dell'envelope è
classificato `legacy` da `domain_contract.read_contract`, mai promosso
implicitamente a `succeeded` né letto come elenco vuoto; l'envelope è versionato
(`version`), quindi una versione/stato sconosciuti sono trattati come `failed`.

> B3c-i **produce e persiste** soltanto questa evidenza. L'integrazione
> readiness/gate e il bridge sul writer sono di **B3c-ii** (vedi «Readiness e
> bridge del contratto domini» qui sotto), che **chiude la limitazione residua
> (a)** di B3b-ii. La limitazione crash/recovery di B3b-ii resta assegnata a **C4**.

#### Readiness e bridge del contratto domini (task B3c-ii)

La categoria `domains` diventa `eligible_for_real_design` **solo** quando il
contratto ricco è `succeeded` e coerente su **entrambi** gli endpoint. La
readiness **non si fida della sola stringa `status`**: per un envelope dichiarato
`succeeded`, `domain_contract.verify_contract` ricostruisce i record e ri-esegue
`reconcile` contro l'enumerazione `list_domains` persistita, e resta eleggibile
solo se anche la re-derivazione indipendente dà `succeeded`. Ogni motivo di
non-eleggibilità produce un gap code stabile e redatto
`domains_contract_<source|destination>_<reason>`, dove `reason` distingue:
`absent` (contratto assente/legacy), `unsupported_version`, `read_failed`,
`partial`, `ambiguous`, `unavailable`, `incomplete_record` (succeeded dichiarato
ma un record incompleto) e `incoherent` (succeeded dichiarato ma incoerente con
l'enumerazione). Il safety gate non duplica la validazione: rifiuta la fase
riferendosi al risultato readiness evidence-bound (report ancorato a
plan/comparison/snapshot correnti), quindi un contratto legacy/partial/invalido
non raggiunge mai una scrittura.

**Bridge writer.** `dispatch._source_domain_records` legge **esclusivamente**
`data["domains_contract"]` tramite `verify_contract`/`project_records` — mai
`data["domains_data"]`, `list_domains` o ricostruzioni euristiche. Solo un
contratto ancora `succeeded` e coerente fornisce record; qualsiasi altro stato
solleva un esito esplicito fail-closed (mai un `[]` silenzioso), così il worker
si ferma **prima** di ogni scrittura. Se il contratto degrada tra readiness e
worker (TOCTOU), la ri-validazione al momento dell'esecuzione lo blocca. Con un
contratto valido, un dominio sorgente mancante sulla destinazione raggiunge
l'engine additivo B3b-i come `RequestedDomain` completo (tipo, docroot ribasato,
internal label) ed esegue `create`/`already_present` — **non più `manual` per
assenza dell'envelope**. La limitazione residua (a) di B3b-ii è quindi chiusa e
verificata dai test end-to-end (`test_real_dispatch.py`); la crash/recovery dei
tentativi `running` resta assegnata a **C4**.
Una singola installazione può comunque restituire `unsupported` o `unavailable`
per feature disabilitate, privilegi mancanti o API non offerte dal server.
Una categoria `unsupported` su entrambi gli endpoint è considerata non
applicabile: resta visibile nella copertura ma non genera un'attività manuale.

Per la progettazione dei writer, il preflight conserva inoltre la matrice
MySQL utente→database→privilegi con coverage autonoma `mysql_grants`. La lettura
è effettuata per ogni coppia inventariata; errori parziali non diventano mai una
matrice vuota verificata. Gli account FTP migrabili sono marcati completi solo
quando `list_ftp_with_disk` fornisce quota e home directory. Per le mailing list
il campo `private` viene considerato verificato soltanto se restituito dal server
o derivabile dall'esplicito `listtype`. Se UAPI non lo espone, il collector usa
il fallback read-only API 2 `Email::listlists` e registra `_privacy_source=api2`;
il valore è derivato dai campi espliciti `archive_private`, `advertised` e
`subscribe_policy` secondo la semantica Mailman documentata, non da euristiche;
se nessuna lettura fornisce un valore esplicito, la coverage resta `partial`.

Il contract test read-only dei database è conservato nello snapshot come
`database_contract`: combina `Mysql::get_restrictions`, limite
`maximum_databases` e conteggio corrente. Solo coverage `succeeded` permette al
readiness report di classificare `databases` come `eligible_for_real_design`.
`mysql_grant_contract` verifica inoltre che tutte le coppie previste siano state
lette e che ogni privilegio appartenga all'insieme supportato dall'API. Solo un
esito riuscito su entrambi gli endpoint rende `mysql_users` eleggibile al design reale.

`ftp_contract` valida il mapping non sensibile `login→user/domain/quota/homedir`
e la presenza del limite `maximum_ftp_accounts`; `mailing_list_contract` valida
`address→list/domain/private` e `maximum_mailing_lists`. Entrambe le evidenze
devono riuscire su sorgente e destinazione per rendere la categoria eleggibile
al design reale. Non contengono né richiedono password.

`forwarder_contract` conserva le coppie complete sorgente→destinazione e prova
che `Email::list_forwarders` può essere riutilizzata come fresh read
pre-scrittura. `autoresponder_contract` prova lista per dominio e dettaglio per
indirizzo, ma conserva soltanto metadati strutturali: body, subject e from non
entrano nell'evidenza. Entrambi richiedono successo sui due endpoint.

`dns_contract` conserva zone proprietarie attese, identità ambigue, tipi non
supportati e la strategia di fresh read `parse_zone_per_owned_zone`. I passi del
piano mantengono anche `comparison_state`: soltanto `missing_on_destination`
senza ambiguità può restare candidato additivo con approval; `different` e
`unknown` sono bloccati come `not_ready`.

DNS viene interrogato separatamente per ogni zona proprietaria (dominio
principale, addon e alias) tramite il parametro obbligatorio `zone`; i
sottodomini restano record della zona genitore e non sono interrogati come zone
autonome. Un errore su una zona produce `partial` se le altre sono
state lette, non cancella i record già acquisiti.
Le righe Base64 di `parse_zone` vengono normalizzate; commenti, direttive, SOA e
NS sono esclusi dal confronto perché dipendono dal server DNS autorevole.
Sono esclusi anche i record temporanei DCV/ACME e i record di servizio cPanel
rigenerabili. Le differenze DNS restano avvisi di cutover e non blocker della
copia dei contenuti.

## Esecuzione asincrona

In ambiente Docker, `POST /preflight` crea un job `queued` in PostgreSQL e invia
al worker Dramatiq soltanto il suo ID. Il worker rilegge endpoint e credenziali,
porta il job a `running`, acquisisce gli snapshot e conclude con `succeeded` o
`failed`. Redis trasporta il messaggio ma non è mai la fonte di verità.

Per il debug locale senza Redis è possibile impostare `PREFLIGHT_INLINE=true`.
I test con SQLite utilizzano automaticamente questa modalità. Non abilitarla
nel normale ambiente batch: una chiamata HTTP resterebbe occupata per tutta la
durata del preflight.

Il worker deve ricevere la stessa `CREDENTIAL_ENCRYPTION_KEY` dell'API, altrimenti
non può decifrare i token diretti. I riferimenti `env://` devono essere disponibili
anche nel container worker.

## Modello dati persistente

Le migrazioni Alembic correnti arrivano a `0007_writer_readiness`:

| Tabelle | Scopo |
|---------|-------|
| `migrations` | contenitore della migrazione account |
| `endpoints` | sorgente/destinazione e credenziali cifrate/riferite |
| `jobs`, `job_events` | stato durevole del preflight asincrono |
| `inventory_snapshots` | evidenze immutabili per endpoint e ruolo |
| `comparison_reports` | confronto legato agli ID esatti degli snapshot |
| `manual_tasks` | checklist storica e verifica evidence-based |
| `migration_plans` | classificazione e dipendenze dei passi |
| `execution_runs` | selezione, piano/report/snapshot, stato e segreti cifrati |
| `execution_events` | preview, risultato e verifica per passo |
| `execution_attempts` | tentativi reali con numero monotòno, checkpoint, compensazione e stato |
| `account_execution_leases` | lease di mutua esclusione per account destinazione con fencing token |
| `writer_readiness_reports` | gap immutabili legati a piano, comparazione e snapshot esatti |

PostgreSQL è la fonte di verità; Redis trasporta soltanto messaggi. Gli snapshot
non vengono aggiornati in-place. Un nuovo preflight crea nuove righe e rende
obsoleti comparazioni, piani e preview precedenti finché non vengono rigenerati.

Comandi di verifica:

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest

cd ../worker
DRAMATIQ_TESTING=1 python -m pytest

cd ../web
npm run build
```

## Comparazione e attività manuali

La comparazione usa gli ultimi snapshot riusciti e produce, per ogni elemento:

- `match`: presente e equivalente sui due account;
- `missing_on_destination`: presente sul sorgente, assente sulla destinazione;
- `only_on_destination`: presente soltanto sulla destinazione;
- `different`: presente sui due lati con configurazione differente;
- `unknown`: categoria non leggibile con affidabilità su uno dei due lati.

Le categorie `unsupported`, `unavailable`, `failed` o `unverified` vengono
saltate e producono un avviso `unknown`: non vengono mai trasformate in falsi
elementi mancanti. Fingerprint e report sono persistenti e riferiti agli ID
esatti dei due snapshot.

Le identità sono specifiche per categoria: dominio per domini, indirizzo per
caselle, coppia sorgente→destinazione per forwarder, login per FTP e copertura
del nome DNS per SSL. Gli ID dei certificati, le date di rinnovo, l'uso disco e
gli account FTP principali/log non sono trattati come risorse da migrare.

Per ogni elemento mancante, differente o ignoto viene generata una
`manual_task`. L'attività contiene categoria, chiave, istruzioni, stato operativo
e stato di verifica. La UI consente di marcarla `pending`, `in_progress`, `done`
o `skipped`.
La checklist operativa mostra soltanto le attività dell'ultima comparazione; i
report precedenti restano nel database esclusivamente come storico di audit.

La verifica è evidence-based: dopo aver segnato un'attività come completata,
l'operatore deve rieseguire preflight e comparazione. Per una risorsa la nuova
comparazione deve risultare `match`; per un gap di copertura la categoria deve
essere stata finalmente letta. La sola conferma manuale non produce mai lo stato
`verified`.

## Piano di migrazione

Il planner converte l'ultima comparazione in passi ordinati e deduplicati:

- `automatic`: writer account-level disponibile;
- `approval`: scrittura possibile ma subordinata a conferma esplicita;
- `secret_required`: serve una nuova password non recuperabile dal sorgente;
- `manual`: nessun writer automatico considerato sicuro;
- `excluded`: duplicato o risorsa deliberatamente esclusa.

Il piano è read-only. Generarlo non modifica alcun cPanel. Domini precedono PHP,
SSL e DNS; database precedono gli utenti MySQL. Un subaccount che rappresenta lo
stesso login FTP viene escluso come duplicato.

Gli utenti MySQL, FTP e mailing list richiedono nuove password. PHP resta manuale
perché la funzione ufficiale di scrittura è WHM-level; SSL viene escluso dalla
copia e rigenerato con AutoSSL dopo domini/DNS. Autoresponder, filtri,
default-address e routing richiedono conferma esplicita (`approval`); il
readiness report ne valida i contratti evidence-bound su entrambi gli endpoint
prima di dichiararli eleggibili. Nessun engine email è collegato al dispatch.

### Inventario autoresponder dettagliato

## Writer readiness report

Il contratto completo, gli stati e lo schema delle evidenze sono documentati in
[`docs/READINESS_CONTRACTS.md`](docs/READINESS_CONTRACTS.md).

Il readiness report è esclusivamente read-only. Copre tutte le categorie dei
writer mock e ogni passo del piano; le categorie senza writer sono dichiarate
esplicitamente `not_ready`. Blocker globali e gap specifici restano separati.
Gli stati sono `not_ready`, `needs_inventory`, `needs_contract_test`,
`needs_operator_input` ed `eligible_for_real_design`.

La generazione accetta soltanto l'ultimo piano costruito sull'ultima
comparazione e sugli ultimi snapshot sorgente/destinazione; evidenze superate
producono HTTP 409. Sono elaborati soltanto coverage, modalità e dipendenze. Il
report non legge né restituisce token, password, ciphertext o body/subject/from
degli autoresponder. La UI non offre dispatch e ricorda che il contratto di
esecuzione reale non esiste ancora.

### Evidenze read-only per il design reale

Le evidenze di contratto vivono negli snapshot immutabili, non in stato globale
o nei risultati dei writer. La comparazione può mostrarne le differenze, mentre
il planner le marca `excluded`: sono prerequisiti del passo operativo, non
risorse da migrare.

| Evidenza | Letture | Condizione `succeeded` | Effetto readiness |
|----------|---------|------------------------|------------------|
| `database_contract` | `Mysql::get_restrictions`, account e database inventory | restrizioni presenti e quota nota su entrambi i lati | `databases` → `eligible_for_real_design` |
| `mysql_grant_contract` | `Mysql::get_privileges_on_database` per ogni coppia | tutte le coppie lette e privilegi nel set supportato | `mysql_users` → `eligible_for_real_design` |
| `ftp_contract` | `Ftp::list_ftp_with_disk`, account inventory | mapping quota/home valido e limite FTP noto su entrambi i lati | `ftp_accounts` → `eligible_for_real_design` |
| `mailing_list_contract` | UAPI `list_lists`, fallback API 2 `listlists`, account inventory | mapping private valido e limite mailing list noto su entrambi i lati | `mailing_lists` → `eligible_for_real_design` |
| `forwarder_contract` | UAPI `Email::list_forwarders` | coppie complete leggibili per il futuro fresh check su entrambi i lati | `email_forwarders` → `eligible_for_real_design` |
| `autoresponder_contract` | UAPI lista per dominio + dettaglio per indirizzo | dettagli completi; contenuti sensibili esclusi dall'evidenza | `email_autoresponders` → `eligible_for_real_design` |
| `dns_contract` | UAPI `DNS::parse_zone` per zona proprietaria | tutte le zone leggibili, collisioni e tipi non supportati censiti | `dns_records` → `eligible_for_real_design`; passi non additivi restano `not_ready` |

`eligible_for_real_design` non abilita un writer e non equivale a `verified`:
significa soltanto che inventario e contratto read-only sono sufficienti per
progettare il futuro percorso reale. Rimangono necessari autorizzazione
separata, pre-write re-check, conferma forte e verifica post-write fresca.

Il collector elenca gli autoresponder separatamente per ogni dominio e usa
`Email::get_auto_responder` per recuperare corpo, mittente, oggetto, intervallo,
HTML, charset, inizio e fine. L'esistenza resta basata sulla lista; la sola
risposta del dettaglio non viene usata per dedurre che la risorsa esista.

Ogni elemento conserva `_detail_status=succeeded|failed`. Se una lista riesce ma
un dettaglio fallisce, la categoria è `partial`, mantiene l'elemento sommario e
non diventa mai `empty`. Se nessun dominio è leggibile è `unavailable`. Il
fingerprint di comparazione include tutti i campi round-trip, quindi differenze
di corpo o pianificazione sono visibili.

## Esecutore sicuro dry-run

La migrazione Alembic `0006_execution_runs` aggiunge `execution_runs` e
`execution_events`. Ogni run è legato in modo immutabile a migrazione, piano,
comparazione, snapshot sorgente/destinazione ed endpoint destinazione. Conserva
selezione, timestamp, chiamate previste, risultati simulati e stato della
verifica. Le nuove password vengono cifrate con Fernet; risposte, preview ed
eventi espongono soltanto gli ID coperti e il valore `[REDACTED]`, mai il segreto
o il ciphertext.

Stati supportati: `previewed`, `awaiting_confirmation`, `queued`, `running`,
`succeeded`, `failed`, `cancelled`, più `compensating`/`compensated` per la
compensazione reale. Le transizioni sono governate da una macchina a stati
tipizzata (`LEGAL_TRANSITIONS`/`assert_transition`): ogni cambio di stato,
compreso l'annullamento, verifica la legalità e fallisce chiuso (`409`) su una
transizione non ammessa o da uno stato terminale/sconosciuto. La creazione
completa subito la preview e porta normalmente il run in `awaiting_confirmation`;
`previewed` resta lo stato iniziale del modello per future generazioni asincrone.

La preview accetta soltanto passi `automatic`, `approval` o `secret_required` e
blocca ID estranei, dipendenze di categoria non selezionate, password mancanti,
piani basati su comparazioni superate e snapshot non più correnti. Quando la UI
genera un nuovo piano, il pannello dry-run lo ricarica automaticamente.
La conferma richiede contemporaneamente:

- frase esatta `CONFERMO DRY-RUN PIANO {id}`;
- ID piano coincidente;
- assenza di comparazioni più recenti;
- ultimi snapshot coincidenti con quelli del report;
- configurazione destinazione non modificata;
- nuovo test UAPI read-only riuscito sulla destinazione.

Solo dopo la conferma il run diventa `queued`. L'endpoint `/run` registra
`running`, simula ogni chiamata e termina `succeeded`; ogni risultato dichiara
`write_performed=false`. La verifica per passo è `not_applicable` in dry-run:
non essendoci una modifica reale, non viene fabbricata evidenza di destinazione.
Il sorgente non è mai un target. Il primo actor writer esiste soltanto per test
mock ed è descritto sotto; non è raggiungibile dalla UI o dalle API operative.

Procedura operativa:

1. completare preflight, comparazione e piano;
2. selezionare i passi e inserire le nuove password richieste;
3. creare e ispezionare l'anteprima redatta;
4. digitare la frase esatta e rivalidare la destinazione;
5. avviare esplicitamente la simulazione;
6. controllare eventi e conteggio delle chiamate simulate.

Limiti correnti: nessuna chiamata writer reale, nessun rollback reale e nessun
nuovo preflight post-operazione, perché il dry-run non modifica lo stato remoto.
L'esecuzione è sincrona e breve; PostgreSQL resta comunque la fonte di verità.

### Contratto di esecuzione reale (disabilitato per default)

La migrazione Alembic `0008_execution_attempts` aggiunge `execution_attempts`: il
contratto durevole che rende rappresentabili crash, retry, checkpoint e
compensazione **prima** di implementare l'esecuzione reale (task A3–A5, D3).

- **Tentativi.** Ogni tentativo reale è una riga con `attempt_number` monotòno e
  univoco per run (`uq_execution_attempt_number`): un retry è un tentativo nuovo,
  mai una sovrascrittura, e un doppio avvio è respinto dal vincolo anziché aprire
  due tentativi concorrenti. Il controllo di concorrenza vero appartiene al lease
  per-account (A4); qui `lease_key` è solo il riferimento rappresentabile.
- **Checkpoint e compensazione.** `checkpoint` registra l'ultimo progresso
  durevole (ID di passo e contatori) per riprendere senza ripetere lavoro;
  `compensation` conserva i descrittori dell'azione reversibile che D3 eseguirà.
  Nessuna di queste colonne — né `error` né `lease_key` — può contenere segreti:
  contengono soltanto identificatori e messaggi già redatti.
- **Riferimenti di evidenza immutabili.** Il tentativo eredita dal run gli ID di
  piano, comparazione e snapshot sorgente/destinazione: l'evidenza vive negli
  snapshot immutabili, mai in uno stato globale mutabile.

Interruttore generale: `REAL_EXECUTION_MODE` (default `disabled`; accetta solo
`disabled`/`enabled`). Con l'esecuzione reale disabilitata, `open_attempt`
fallisce chiuso e nessun tentativo, lease o mutazione della destinazione può
essere aperto. Un dry-run non apre mai tentativi. Rollback: `alembic downgrade
0007_writer_readiness` elimina `execution_attempts` senza toccare le altre
tabelle.

#### Lease per account di destinazione (fencing)

La migrazione Alembic `0009_account_leases` aggiunge `account_execution_leases` e
la colonna `execution_attempts.fencing_token`. Il lease garantisce che **un solo
writer** muti un account di destinazione alla volta (una riga per endpoint,
vincolo `uq_account_lease_endpoint`).

- **Un solo vincitore.** `acquire` respinge un secondo writer finché il lease è
  attivo; il riacquisto dello stesso owner è idempotente e non incrementa il
  token (i retry non si auto-escludono).
- **Takeover sicuro.** Un lease scaduto (nessun heartbeat entro
  `EXECUTION_LEASE_TTL_SECONDS`, default 300s) o rilasciato può essere acquisito
  da un altro worker: `fencing_token` viene incrementato in modo monotòno.
- **Fencing.** Il tentativo memorizza il `fencing_token` sotto cui gira.
  `finalize_attempt` chiama `assert_fencing_current` prima di persistere un esito
  terminale: un worker il cui lease è stato sottratto (token obsoleto, lease
  scaduto o assente) **non può completare il run né scrivere risultati** e viene
  respinto con `409`, lasciando il tentativo invariato.
- **Heartbeat/release.** Solo l'owner con il token corrente può rinnovare o
  rilasciare; un detentore obsoleto è respinto. `owner` è un identificatore
  opaco del worker: il lease non contiene segreti.

Come tutto il percorso reale, `acquire` fallisce chiuso quando
`REAL_EXECUTION_MODE=disabled`. Rollback: `alembic downgrade
0008_execution_attempts` elimina `account_execution_leases` e la colonna
`fencing_token`.

#### Gate di sicurezza pre-scrittura (`safety_gates`)

`app/modules/executions/safety_gates.authorize` è l'unica pre-validazione
**fail-closed** che il dispatch reale (A3) dovrà superare **prima di ogni fase di
scrittura**. Non esegue e non accoda nulla: prova, da riletture fresche delle
evidenze persistite, che una mutazione sarebbe sicura, altrimenti solleva
`SafetyGateError` (`409`). Non introduce migrazioni: usa le tabelle esistenti.

Protezione strutturale della sorgente: un writer reale accetterà soltanto un
`WriteTarget`, e l'unico costruttore è `WriteTarget.for_endpoint`, che rifiuta
qualsiasi endpoint con ruolo diverso da `destination`. Non esiste un percorso che
produca un `WriteTarget` per la sorgente: read source e write destination sono
tipi distinti e non interscambiabili, quindi la sorgente non può raggiungere un
writer.

`authorize` ricombina a ogni chiamata, con letture fresche: master switch reale
attivo; run reale e non terminale; targeting solo-destinazione; conferma forte
presente e non scaduta (`REAL_CONFIRMATION_TTL_SECONDS`); coerenza **e** attualità
di piano/comparazione/snapshot (il run deve riferire l'evidenza più recente);
leggibilità dello snapshot (solo `succeeded`, mai `partial`/`failed`/
`unavailable`/`empty`/ambiguo); capability per categoria (un readiness report
corrente che marca la categoria `eligible_for_real_design`); lease attivo con
fencing token corrente. Ogni input mancante, stale o ambiguo blocca.

Poiché ogni chiamata rilegge lo stato, invocare `authorize` prima di ciascuna
fase fa sì che un drift intervenuto (nuovo snapshot, nuova comparazione, conferma
scaduta, lease sottratto) fermi la fase successiva. La `GateDecision` restituita
contiene solo id, nomi di categoria e fencing token: nessun segreto viene letto o
restituito. Interruttore: `REAL_EXECUTION_MODE` (default `disabled`) — con
l'esecuzione reale disabilitata `authorize` fallisce chiuso.

#### Dispatch durevole reale (`dispatch`)

`app/modules/executions/dispatch.py` collega il percorso reale
**API → PostgreSQL → Dramatiq → worker**, riusando (senza duplicarli) la state
machine/`ExecutionAttempt` (A2), il lease/fencing (A4) e `authorize` (A5).
Nessun writer reale né chiamata cPanel/SSH/IMAP è introdotto: senza fasi reali il
worker si ferma nello stato terminale sicuro `halted`.

| Metodo | Path | Descrizione |
|--------|------|-------------|
| POST | `/api/executions/{run_id}/dispatch` | avvia un run reale confermato e in coda |

Sequenza dell'endpoint (`dispatch`): con `REAL_EXECUTION_MODE=enabled`, per un
run reale (non dry-run) in `queued`, acquisisce il lease dell'account, esegue
`authorize`, crea e **committa** un tentativo `queued` (con lease/fencing token),
e **solo dopo** invia alla coda **soltanto** `execution_run_id` e `attempt_id` —
mai token, password, ciphertext, snapshot o payload operativi.

Il worker (`worker_start`, actor `real_execution`, distinto dagli actor mock)
rilegge tutto da PostgreSQL, riesegue `authorize` (che riverifica lease e
fencing) e porta legalmente il run `queued → running`; prima di ogni futura fase
di scrittura rivalida gate e fencing. Un worker con fencing obsoleto o evidenza
diventata stale non muta nulla.

Recovery e idempotenza:

- **Broker failure dopo il commit**: lo stato è persistito prima dell'invio; il
  tentativo resta `queued` e riaccodabile. Un nuovo dispatch riusa lo stesso
  tentativo (aggiornandone il fencing token al lease corrente), mai uno nuovo.
- **Run `queued` mai ricevuto dal worker**: stessa procedura — ripetere il
  dispatch riaccoda il medesimo tentativo.
- **Richieste duplicate / retry**: idempotenti (un solo tentativo attivo per run).
- **Due enqueue concorrenti per lo stesso account**: il lease per-account (owner
  legato al run) fa vincere un solo writer; il secondo run è respinto.
- **Fenced-out / stale**: l'actor solleva senza aggiornare run o tentativo, che
  restano recuperabili.

Interruttore `REAL_EXECUTION_MODE` (default `disabled`): endpoint e actor
falliscono chiusi. Nessuna route o UI può modificare il flag. A3 non aggiunge
migrazioni (usa le tabelle esistenti; `halted` è un nuovo valore di stato).

### Fase domini reale nel worker (B3b-ii)

Sotto il **doppio gate** `REAL_EXECUTION_MODE=enabled` **e**
`DOMAIN_WRITER_MODE=enabled` (proprietà `settings.domain_real_writer_enabled`,
entrambi `disabled` per default) `worker_start` collega il motore additivo di
B3b-i (`real_domain_writer.py`) al percorso reale. Con il gate spento la categoria
`domains` non è eseguibile e il run si ferma in `halted` senza mutazioni.

- **Gateway solo-destinazione**: `_build_domain_gateway` costruisce il client
  cPanel **esclusivamente** dall'endpoint destinazione (`allow_destination_writes=True`);
  nessun endpoint/credenziale/client sorgente raggiunge il motore, e un endpoint
  non-`destination` è rifiutato.
- **Rivalidazione a tre stadi**: `authorize` (lease + fencing + evidenza fresca)
  prima della fase, il hook `before_write` **immediatamente prima di ogni create**,
  e `authorize` + `finalize_attempt` (che riverifica il fencing) prima di persistere
  esito/checkpoint/compensation. Un worker fenced-out dopo la write non registra
  successo.
- **Evidenza sorgente fail-closed (bridge B3c-ii)**: il motore richiede record
  ricchi (tipo + docroot). `_source_domain_records` legge **esclusivamente** il
  contratto `data["domains_contract"]` (B3c-i) tramite
  `domain_contract.verify_contract`/`project_records`, ri-validandolo al momento
  dell'esecuzione — mai `domains_data`/`list_domains`/euristiche. Un contratto
  `succeeded` e coerente fornisce record completi; qualsiasi altro stato solleva un
  esito esplicito fail-closed (mai `[]` silenzioso) e il worker si ferma prima di
  ogni write. Un passo il cui dominio non è nel contratto o è un dominio main resta
  **manual/pending** (→ `halted`), mai una write fabbricata. La limitazione (a)
  «inventario privo dell'envelope → passi domini manual/pending» è **chiusa**: con
  un contratto valido un dominio mancante sulla destinazione esegue
  `create`/`already_present` (vedi «Readiness e bridge del contratto domini»).
- **Stato terminale**: solo domini idonei tutti verificati (incluso
  `already_present` senza write) → `succeeded`; passo `blocked`/create non
  verificata → `failed`; passo manuale o categoria non implementata presente nel
  run → `halted` con `pending_categories`/`manual_pending` nel checkpoint — **mai**
  `succeeded` mentre restano categorie selezionate non eseguite.

La create è **non idempotente e mai ri-tentata**: un esito ambiguo è risolto da
una rilettura fresca e un retry che rilegge il dominio presente lo classifica
`already_present` senza duplicarlo. Checkpoint e compensation contengono solo
descrittori redatti (`reverse: manual_removal_only`); nessun segreto entra in
eventi, coda, risposte o eccezioni. B3b-ii non aggiunge migrazioni né stati
(riusa `succeeded`/`failed`/`halted`). Esito e checkpoint sono persistiti da un
unico commit di `finalize_attempt` (run e tentativo insieme, atomico). Limitazione
residua ereditata da A3: un crash del worker *durante* la fase (dopo il commit
`running`) lascia un tentativo `running` non riaccodabile; il recupero richiede
una reconciliation esterna (fuori scope), ma la rilettura fresca del motore
garantisce che un tentativo ripreso classifichi il dominio già creato come
`already_present` senza duplicarlo.

## Writer domini mock-only

`worker.actors.domain_writer.domain_writer_actor` prepara il flusso asincrono
del primo writer, ma il servizio accetta esclusivamente:

- `DOMAIN_WRITER_MODE=mock`;
- execution run non dry-run creati esclusivamente dai test;
- endpoint con ruolo `destination` e `auth_type=mock`;
- passi della categoria `domains`.

La configurazione Docker e `.env.example` usano
`DOMAIN_WRITER_MODE=disabled`. I soli valori ammessi sono `disabled`, `mock`
(questo writer simulato) ed `enabled` (writer domini reale di B3b-ii, inerte
senza `REAL_EXECUTION_MODE=enabled`); qualunque altro valore — incluso il
ritirato `real` — è rifiutato **fail-closed al load** (l'app non parte). Non
esiste alcuna route o controllo UI che accodi il writer. Questa separazione
impedisce di trasformare accidentalmente un dry-run confermato in una scrittura.

Il writer mock esegue un controllo di presenza prima della creazione, non
cancella e non sovrascrive domini, conserva chiamata prevista, risultato e
verifica negli `execution_events`. Un retry usa un evento già verificato come
checkpoint e registra `already_completed` senza ripetere l'azione. La verifica
legge lo stato del target mock. Per il futuro writer reale questa evidenza dovrà
essere sostituita obbligatoriamente da nuovo preflight e comparazione; rollback
e attività manuale devono essere definiti prima di esporre qualsiasi dispatch.

Test mirati:

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest app/tests/test_domain_writer.py

# nell'immagine Docker, che include Dramatiq
docker compose exec api sh -lc \
  'cd /srv/apps/worker && DRAMATIQ_TESTING=1 python -m pytest worker/tests/test_actors.py'
```

## Writer database MySQL mock-only

`worker.actors.database_writer.database_writer_actor` prepara il secondo writer
seguendo gli stessi guardrail del writer domini. È governato dal flag separato
`DATABASE_WRITER_MODE`, che vale `disabled` sia in Docker sia nel file di
esempio. Solo i test lo impostano temporaneamente a `mock`; `real` viene sempre
rifiutato e non esistono route o pulsanti che possano accodarlo.

Il controllo idempotente legge i database dallo snapshot destinazione usando
`database` o `name` come identità. La chiamata prevista è
`Mysql::create_database`; il mock restituisce `already_present` senza modifiche
quando la risorsa esiste, oppure `created` e verifica subito il target simulato.
Retry successivi usano l'evento verificato come checkpoint persistente. Utenti
e privilegi sono gestiti dal writer mock separato descritto sotto, così la
creazione del database resta un'unità idempotente indipendente.

Test mirato:

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest app/tests/test_database_writer.py
```

## Writer utenti MySQL e privilegi mock-only

`worker.actors.mysql_user_writer.mysql_user_writer_actor` prepara creazione
utente e grant con il flag `MYSQL_USER_WRITER_MODE`, disabilitato nello stack.
Il servizio accetta soltanto endpoint destinazione mock, run non dry-run creati
dai test e nuove password già cifrate con Fernet. Il segreto viene decifrato
soltanto durante la chiamata mock, eliminato subito dalla variabile locale e non
compare in preview, eventi, risultati o verifiche.

L'inventario account-level corrente non espone ancora una mappatura affidabile
utente→database. Per questo il writer non inventa assegnazioni: richiede
esattamente un passo database nel run e prova che quel database sia presente
nello snapshot destinazione oppure verificato da un evento del writer database.
Con zero o più database selezionati l'operazione viene bloccata. Nel mock il
grant è `ALL PRIVILEGES`; prima di un writer reale l'inventario dovrà acquisire
i privilegi sorgente e il planner dovrà produrre dipendenze per risorsa, non
soltanto per categoria.

Creazione utente e grant sono verificati sul target mock e auditati come unica
unità operativa. Retry successivi usano il checkpoint verificato e non ripetono
la password o il grant. Il valore `real` non è implementato, e non esistono
route/UI per accodare questo actor.

Test mirato:

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest app/tests/test_mysql_user_writer.py
```

## Writer forwarder mock-only

`worker.actors.forwarder_writer.forwarder_writer_actor` prepara i forwarder con
`FORWARDER_WRITER_MODE=disabled`. La risorsa è identificata dalla coppia esatta
`sorgente -> destinazione`; una stessa sorgente verso un target diverso non è
considerata già migrata e non viene sovrascritta o cancellata. Il parser rifiuta
chiavi ambigue, sorgenti senza `@` e destinazioni vuote.

Il mock simula `Email::add_forwarder`, verifica la coppia sul target simulato e
registra chiamata, risultato e verifica. Retry successivi usano il checkpoint
persistente. Non sono richieste password. Endpoint reali, run dry-run e valori
diversi da `mock` sono bloccati; non esistono route o azioni UI di dispatch.

Test mirato:

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest app/tests/test_forwarder_writer.py
```

### Framework writer email reale + forwarder additivo (B4a)

`app/modules/executions/email_write.py` introduce il **framework condiviso** per i
writer email reali che tutte le categorie (forwarder, default-address, routing,
filtri, autoresponder) riusano, così la forma di sicurezza è scritta e testata una
sola volta. Per ogni elemento il motore `execute_email_phase`:

- fa una **fresh-read live** della destinazione e applica una funzione di decisione
  di categoria;
- `already_present` (match) → no-op verificato (nessuna scrittura);
- `create` → **unico** percorso che raggiunge una `DestinationWrite`; **mai
  ritentato**, e un esito ambiguo/timeout è risolto da una fresh-read, non da una
  seconda scrittura cieca;
- `blocked` (different / only-on-destination / non esprimibile) → fail closed;
- `manual` (illeggibile / parziale / non risolvibile) → pending, mai scrittura
  silenziosa;
- verifica post-write rileggendo live e fidandosi solo se la decisione torna
  `already_present`.

Il motore non ha concern di runtime: appende eventi di audit **redatti** su
`run.events` (solo una label sicura, mai token/password/body/regole) e restituisce
un risultato aggregato, senza toccare sessione DB, macchina a stati o gate
lease/fencing. Il `EmailGateway` espone solo operazioni di destinazione (nessuna
primitiva di scrittura sorgente): la sorgente resta strutturalmente read-only. Un
hook `before_write` è il seam che B4e userà per rivalidare gate + fencing
immediatamente prima di ogni mutazione.

La prima categoria reale è il **forwarder** additivo (`forwarder_rules.py` +
`forwarder_writer.run_forwarder_phase`): chiave composta `sorgente→destinazione`,
create solo se la coppia esatta è assente, una coppia con destinazione diversa
dalla stessa sorgente è **additiva** (crea la nuova, non sostituisce l'esistente),
forme non esprimibili come `add_forwarder` (pipe/programma/`:fail:`) o evidenza live
ambigua/illeggibile falliscono chiuse. Dietro il doppio gate
`FORWARDER_WRITER_MODE=enabled` + `REAL_EXECUTION_MODE=enabled` (exact-match,
**disabilitato per default**, valore ignoto rifiutato allo startup) e **non ancora
cablato** nel dispatch runtime — `IMPLEMENTED_REAL_CATEGORIES` non include categorie
email finché B4e non le collega, quindi un run di soli passi email si ferma
(`halted`) senza mutazioni.

Test mirato (coverage): `email_write.py` 99%, `forwarder_rules.py` 100%.

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest app/tests/test_real_forwarder_writer.py \
  --cov=app.modules.executions.email_write --cov=app.modules.executions.forwarder_rules --cov-branch
```

### Contratto evidence default-address (catch-all) e regole pure (B4b-i)

Il catch-all è *compensabile, non additivo*: `Email::set_default_address`
**sovrascrive** il valore corrente. B4b-i costruisce il fondamento decisionale
— nessuna scrittura — e B4b-ii aggiunge l'engine writer compensabile (non cablato nel
dispatch fino a B4e).

`default_address_rules.py` (puro) tiene ogni valore **byte-faithful** (il default
cPanel è il letterale `:fail: No Such User Here`, confrontato come stringa opaca) e
lo classifica senza mai mutarlo: `fail` / `blackhole` / `account_default` (== username
account, legato all'evidenza) / `address` (forward semplice, parser esplicito) /
`other` (pipe/programma/path/quoting inatteso — mai indovinato). Espone le op tipizzate
SafeRead `list_default_address_op()` e DestinationWrite `set_default_address_op()`
(costruibili e testabili ma irraggiungibili dal runtime: la write resta disabilitata).

Il collector persiste `default_address_contract`, un envelope **versionato e
fail-closed**: una lettura fallita è `failed`/`unavailable` (mai `empty`), un dominio
verificato senza record è `partial`, duplicati conflittuali o record inattesi sono
`ambiguous`, e `is_write_eligible` richiede versione corrente **e** stato `succeeded`
(mai la sola stringa di stato → snapshot legacy leggibili ma non eleggibili). La
matrice decisionale pura: raw equivalenti → `already_present`; destinazione fresca
(fail/blackhole/account_default) con sorgente round-trippabile → `set`; destinazione
customizzata → `blocked` (mai overwrite); dominio assente sulla destinazione →
`blocked`; sorgente `other`/mancante o evidenza illeggibile/ambigua → `manual`.

**Engine compensabile (B4b-ii).** `default_address_writer.py` riusa
`execute_email_phase` e le decisioni B4b-i, estendendo il framework con il seam
generico `backup_of`/`persist_backup` (il forwarder additivo resta invariato,
senza backup). Una `set_default_address` avviene **solo** su decisione live `set`;
prima della write il valore live precedente viene salvato come **backup tipizzato
persistito atomicamente** (backup non costruibile o non persistito → zero write). Il
compensation metadata contiene **solo il riferimento** al backup (nessun raw); il raw
vive esclusivamente nel contenitore protetto del seam. Nessun retry: una risposta
ambigua risolve con una nuova fresh-read (equivalente→verified, altrimenti failed con
compensation reference disponibile), mai una seconda write; verifica post-write via
decisione B4b-i (solo l'equivalenza produce verified). Gateway solo-destinazione
(fresh-read SafeRead + set DestinationWrite B4b-i), non registrato nel dispatch.

Doppio gate `DEFAULT_ADDRESS_WRITER_MODE=enabled` + `REAL_EXECUTION_MODE=enabled`
(exact-match, disabled-by-default, validator fail-closed). Coverage:
`default_address_rules.py`, `default_address_writer.py` e il seam di `email_write.py`
100%.

### Contratto evidence routing email e policy gate (B4c-i)

Il routing è *compensabile*: `Email::setmxcheck` (**API2**) **sovrascrive** lo stato
esistente; la lettura è `Email::list_mxs` (**UAPI**). B4c-i costruisce il fondamento
decisionale — nessuna scrittura — e B4c-ii aggiunge l'engine (riusando il seam B4b-ii,
senza toccare `email_write.py`).

`routing_rules.py` (puro) classifica **solo** il campo configurato `mxcheck` in
`local`/`remote`/`auto`/`secondary`/`unknown`; `detected`, gli MX e il DNS **non sono
mai** input decisionali (conservati come evidenza), `alwaysaccept` non trasforma la
classe, e una combinazione incoerente (es. `mxcheck=local` con flag `remote`) →
`unknown`. Op tipizzate SafeRead `list_mxs_op()` (UAPI) e DestinationWrite
`setmxcheck_op()` (API2, costruibili/testabili ma irraggiungibili). Il collector
persiste `email_routing_contract` versionato e fail-closed (lettura fallita→
`failed`/`unavailable` mai `empty`; dominio mail-routing atteso senza record→`partial`;
duplicati conflittuali/record inattesi→`ambiguous`; `is_write_eligible` richiede
versione corrente **e** `succeeded`).

**Policy gate evidence-bound.** Nessuno stato destination è "fresh" per default: la
matrice pura fa `already_present` sugli equivalenti (senza policy) e `blocked` su ogni
differenza, **salvo** una `RoutingSetPolicy` esplicita e approvata che vincola
esattamente la transizione osservata (dominio + routing source richiesto + routing
destination live + `evidence_fingerprint` + scadenza + id approvazione redatto). Una
policy generica, di dominio/source/destination errati, con fingerprint stale o scaduta
→ `blocked`. `secondary` e `unknown` sono sempre `manual` (anche con policy);
partial/unreadable/ambiguous → `manual`; dominio assente → `blocked`.

Doppio gate `ROUTING_WRITER_MODE=enabled` + `REAL_EXECUTION_MODE=enabled` (exact-match,
disabled-by-default, validator fail-closed). Coverage: `routing_rules.py` 100%.

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest app/tests/test_default_address_contract.py \
  --cov=app.modules.executions.default_address_rules --cov-report=term-missing
```

### Engine writer routing compensabile (B4c-ii)

`routing_writer.py` riusa `execute_email_phase` (B4a) e il seam `backup_of`/
`persist_backup` (B4b-ii) senza toccare `email_write.py`, consumando **solo** contratto/
regole/policy di B4c-i. Poiché `setmxcheck` sovrascrive, la scrittura è raggiunta **solo**
su decisione `set` — una singola transizione esatta autorizzata da una `RoutingSetPolicy`
su evidenza live verificata e non driftata; un routing differente o custom è `blocked` e
**mai** sovrascritto, `secondary`/`unknown` è `manual`. La policy è **consumata così com'è**
validata da B4c-i: il writer non la costruisce né la allarga, e `policy_authorizes` ri-deriva
il fingerprint dalla lettura **live**, così una destination driftata rispetto allo snapshot
approvato fallisce l'exact-match.

Flusso per dominio: fresh-read live `list_mxs` → `RoutingEvidence` costruita solo dal
payload live → `decide()` con dominio/source/destination live/policy → backup tipizzato del
routing precedente **dal live** (backup-or-nothing, persistito **prima** della scrittura) →
`before_write` (seam gate/fencing B4e) → unica `setmxcheck` (mai auto-retry; timeout/ambiguo
→ fresh-read, mai seconda write) → verify live (equivalenza con il source richiesto).
La `mxcheck` è un enum (`local`/`remote`/`auto`) non sensibile; il raw `mxcheck` precedente
vive **solo** nel backup store, mai in eventi/error/result. La compensation redatta porta il
solo backup reference. Gateway destination-only (nessuna primitiva source).

Doppio gate `ROUTING_WRITER_MODE=enabled` + `REAL_EXECUTION_MODE=enabled` (disabled-by-default);
l'engine resta **irraggiungibile dal runtime** (non in `IMPLEMENTED_REAL_CATEGORIES`) fino al
cablaggio dispatch/authorize/lease/fencing di B4e. Coverage: `routing_writer.py` 100%.

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest app/tests/test_real_routing_writer.py \
  --cov=app.modules.executions.routing_writer --cov-report=term-missing
```

### Contratto evidence filtri email, fingerprint e regole (B4d-i)

I filtri sono *additivi*: `Email::store_filter` (**API2**) è un **UPSERT** — un filtro
omonimo ma diverso non va mai sovrascritto. La lettura è `Email::list_filters` (**UAPI**)
per **scope** — account-level (`account` assente) e per mailbox (`account=local@domain`) —
con dettaglio `Email::get_filter`. B4d-i costruisce il fondamento decisionale (nessuna
scrittura); B4d-ii aggiunge l'engine additive-only (riusando `execute_email_phase`, senza
toccare `email_write.py`).

**Regola critica `get_filter`.** Su un filtro **inesistente** cPanel ritorna `status:1` con
un **TEMPLATE** (`filtername="Rule 1"`, una regola/azione vuota), non un errore. L'esistenza
è quindi gateata **solo** su `list_filters`: `get_filter` non è mai un existence check, il
template non è mai un filtro reale, un nome del dettaglio ≠ nome enumerato → `ambiguous`, un
dettaglio template/vuoto → `incomplete`. Una failure del dettaglio rende lo scope `partial`,
mai `empty`.

`filter_rules.py` (puro) fornisce: op tipizzate SafeRead `list_filters_op`/`get_filter_op`
(get solo dopo esistenza provata dalla lista) e DestinationWrite `store_filter_op` (API2,
costruibile ma irraggiungibile; **nessuna** `DeleteFilter`); un **canonical fingerprint**
deterministico e order-preserving sul payload completo (ogni rule = `part`/`match`/`opt`
incl. null/`val`/`number`, ogni action = `action`/`dest`/`number`; ordine di rules/actions
preservato, nessun sorting, nessuna normalizzazione di regex/whitespace/quoting; distingue
null/empty/missing/zero) — il fingerprint è un hash opaco, il payload completo resta nel
contratto protetto ma **mai** in log/audit/errori; classificazione `complete`/`incomplete`/
`unsupported` (operatore/azione sconosciuti tenuti, mai scartati); contratto `email_filters`
versionato a due scope, fail-closed (list failure → `failed`/`unavailable` mai `empty`;
detail failure → `partial`; name-mismatch/template o duplicato conflittuale → `ambiguous`;
duplicato equivalente → dedup; account-level `succeeded` non nasconde una mailbox `partial`,
lo status complessivo è il peggiore degli scope; `is_write_eligible` richiede versione
corrente **e** tutti gli scope succeeded/empty). Matrice pura: same scope+name+fingerprint →
`already_present`; nome live-assente + source completa/supportata → `create`; stesso nome,
fingerprint diverso → `blocked`; destination-only → preserve/no-op (mai delete);
source incomplete/unsupported → `manual`; destination partial/unreadable/ambiguous → `manual`;
mailbox destination assente → `blocked`. Nessun rename/reorder/replace/delete, nessuna write.

Doppio gate `FILTER_WRITER_MODE=enabled` + `REAL_EXECUTION_MODE=enabled` (exact-match,
disabled-by-default, validator fail-closed). Coverage: `filter_rules.py` 100%.

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest app/tests/test_email_filter_contract.py \
  --cov=app.modules.executions.filter_rules --cov-report=term-missing
```

### Engine writer filtri additive-only (B4d-ii)

`filter_writer.py` riusa `execute_email_phase` (B4a) e consuma **solo** contratto/
fingerprint/regole di B4d-i, **senza toccare** `email_write.py`. Poiché `store_filter` è un
**UPSERT**, la write è raggiunta solo su nome live-assente, provato da **due fresh-read
distinte**: la `read_live` iniziale dietro la decisione, e una seconda `list_filters` di
**guardia** eseguita **dentro** il gateway di scrittura, immediatamente adiacente a
`store_filter`. L'ordine effettivo — dato l'ordine del framework `before_write` →
`gateway.create` — è: read-live → decide → `before_write` (seam authorize/fencing B4e) →
fresh-list guard → unica `store_filter`; dopo la guardia non esiste altra logica fallibile
prima della chiamata API. La guardia prova l'assenza **solo per enumerazione** (mai
`get_filter`), riusa lo stesso scope, e non riusa la prima lista; se il nome è ricomparso o
la lista è unreadable/malformed → zero write.

Un filtro omonimo ma con fingerprint diverso è `blocked` (mai sovrascritto); un filtro
destination-only non è mai toccato; **nessun** `DeleteFilter`. Verify tramite fingerprint
**completo** su una nuova lista (enumerazione → `get_filter` → confronto scope/nome/rules/
actions; un template non produce mai successo). Una sola `store_filter`, mai retry;
timeout/ambiguo → fresh-read, mai seconda write. Il payload rules/actions non entra in
eventi/error/result (planned_call = scope + nome). La compensation redatta
(`manual_remove_created_filter`, scope + nome + fingerprint + conferma richiesta) è attaccata
**solo** per una create che il gateway ha realmente scritto **e** che la rilettura ha
verificato — mai per `already_present` né per una write saltata dalla guardia, così non può
mai rimuovere un filtro preesistente.

Doppio gate `FILTER_WRITER_MODE=enabled` + `REAL_EXECUTION_MODE=enabled` (disabled-by-default);
l'engine resta **irraggiungibile dal runtime** (non in `IMPLEMENTED_REAL_CATEGORIES`) fino al
cablaggio dispatch/authorize/lease/fencing di B4e. Coverage: `filter_writer.py` 100%.

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest app/tests/test_real_filter_writer.py \
  --cov=app.modules.executions.filter_writer --cov-report=term-missing
```

### Contratto evidence autoresponder, fingerprint e regole (B4e-i)

Gli autoresponder sono *additivi*: `Email::add_auto_responder` (`domain`+`email` local part;
`start`/`stop` omessi se 0) è un **UPSERT** — un autoresponder omonimo ma diverso non va mai
sovrascritto. La lettura è `Email::list_auto_responders` (**UAPI**) **per dominio**, con
dettaglio `Email::get_auto_responder` per indirizzo. B4e-i costruisce il fondamento
decisionale (nessuna scrittura); B4e-ii aggiunge l'engine additive-only; B4e-iii cabla il
dispatch.

**Esistenza e redazione.** L'esistenza è provata **solo** da `list_auto_responders`
(`get_auto_responder` mai come existence check; un detail che porta un indirizzo in conflitto
→ `ambiguous`); un fallimento del dettaglio rende il dominio `partial`, una lista fallita è
`failed`/`unavailable` (mai `empty`), uno zero-responder reale resta distinto dall'illeggibile.
Il **canonical fingerprint** è deterministico e order-stable sul payload completo
(`from`/`subject`/`body`/`interval`/`is_html`/`charset`/`start`/`stop` + campi extra;
distingue null/empty/missing/zero/`"0"`/bool; nessuna normalizzazione di body/subject/
whitespace/HTML/charset), ma **solo l'hash opaco e i metadati non sensibili**
(`interval`/`is_html`/`charset`/`start`/`stop`) entrano nel contratto — `from`/`subject`/`body`
non compaiono mai nel contratto persistito, log, audit, eventi, errori o `repr`.

`autoresponder_rules.py` (puro) fornisce op tipizzate `list_auto_responders_op`/
`get_auto_responder_op`/`add_auto_responder_op` (irraggiungibile; **nessun** delete),
`classify_completeness` (`complete`/`incomplete`/`unsupported`, modalità HTML sconosciuta
tenuta come unsupported), contratto `autoresponder_contract` versionato per-dominio fail-closed
(worst-of-domains, `is_write_eligible` richiede versione corrente **e** tutti i domini
succeeded/empty), e la matrice additiva `decide` (same address+fingerprint → already_present;
address live-assente + source completa → create; stesso address, fingerprint diverso → blocked;
source incomplete/unsupported → manual; destination partial/ambiguous → manual; dominio assente
→ blocked; nessun overwrite/upsert/delete). Il collector persiste il contratto versionato senza
toccare il flat `email_autoresponders` (comparison/mock invariati); la categoria resta `MANUAL`
e non dispatchabile fino a B4e-iii.

Doppio gate `AUTORESPONDER_WRITER_MODE=enabled` + `REAL_EXECUTION_MODE=enabled` (exact-match,
disabled-by-default, validator fail-closed; il valore `mock` guida il writer mock separato).
Coverage: `autoresponder_rules.py` 100%.

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest app/tests/test_email_autoresponder_contract.py \
  --cov=app.modules.executions.autoresponder_rules --cov-report=term-missing
```

### Engine writer autoresponder additive-only (B4e-ii)

`real_autoresponder_writer.py` riusa `execute_email_phase` (B4a) e consuma **solo** contratto/
fingerprint/regole di B4e-i, **senza toccare** `email_write.py`; il writer mock
`autoresponder_writer.py` resta intatto. Poiché `add_auto_responder` è un **UPSERT**, la write è
raggiunta solo su indirizzo live-assente, provato da **due fresh-read distinte**: la `read_live`
iniziale dietro la decisione, e una seconda `list_auto_responders` di **guardia** eseguita
**dentro** il gateway, immediatamente adiacente a `add_auto_responder`. L'ordine effettivo è
read-live → decide → `before_write` (seam authorize/fencing B4e-iii) → fresh-list guard → unica
`add_auto_responder`; dopo la guardia non esiste altra logica fallibile prima della chiamata API.
La guardia prova l'assenza **solo per enumerazione** (mai `get_auto_responder`), riusa lo stesso
dominio e non riusa la prima lista; indirizzo ricomparso o lista unreadable/malformed → zero write.

**Provenienza del payload.** Il payload operativo completo
(`from`/`subject`/`body`/`interval`/`is_html`/`charset`/`start`/`stop`) è risolto **solo** dallo
snapshot sorgente immutabile (`source_snapshot.data["email_autoresponders"]`) e **vincolato** al
contratto B4e-i: il fingerprint ricostruito dallo snapshot deve coincidere con quello registrato
nel contratto per quell'indirizzo, e dominio + local part devono coincidere. Payload assente/
duplicato/detail-fallito/fingerprint-mismatch → `manual`/`blocked`, zero write; nessun campo
mancante viene defaultato. Il payload completo vive **solo** in memoria (`EmailItem.payload`): non
entra mai in eventi, planned_call, audit, errori o compensation (planned_call = dominio + indirizzo
+ fingerprint + metadati non sensibili). Payload da request body/preview/destination è ignorato.

Un autoresponder omonimo ma con fingerprint diverso è `blocked` (mai sovrascritto); uno
destination-only non è mai toccato; **nessun** `DeleteAutoresponder`. Verify tramite fingerprint
**completo** su nuova lista+dettaglio (un template non produce mai successo). Una sola
`add_auto_responder`, mai retry; timeout/ambiguo → fresh-read, mai seconda write. La compensation
redatta (`manual_remove_created_autoresponder`, dominio + indirizzo + fingerprint + conferma
richiesta) è attaccata **solo** per una create realmente scritta **e** verificata dalla rilettura —
mai per `already_present` né per una write saltata dalla guardia, così non può mai rimuovere un
autoresponder preesistente.

Doppio gate `AUTORESPONDER_WRITER_MODE=enabled` + `REAL_EXECUTION_MODE=enabled`
(disabled-by-default); l'engine resta **irraggiungibile dal runtime** (non in
`IMPLEMENTED_REAL_CATEGORIES`) e la categoria resta `MANUAL` fino al cablaggio dispatch di
B4e-iii. Coverage: `real_autoresponder_writer.py` 96%.

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest app/tests/test_real_autoresponder_writer.py \
  --cov=app.modules.executions.real_autoresponder_writer --cov-report=term-missing
```

### Pipeline categorie email nel readiness e nella comparazione (B4e-iii-b)

Le cinque categorie email (`email_forwarders`, `default_address`, `email_routing`,
`email_filters`, `email_autoresponders`) sono integrate nella pipeline
comparazione→piano→readiness con gap code evidence-bound. Per ciascuna la readiness
richiede contratti sorgente e destinazione validi (versionati, `is_write_eligible`)
e coverage `succeeded`; un contratto assente, legacy, parziale, ambiguo o fallito
produce un gap code stabile `<category>_<side>_<reason>`. Il planner classifica
ciascuna come `approval` (non `automatic`); il readiness report la valida come
`eligible_for_real_design` solo quando entrambi i contratti superano il doppio
check. Nessun engine email è collegato al dispatch; le categorie restano `MANUAL`
fino al wiring di B4e-iii-c.

### Typed registry e evidence resolver per categorie email (B4e-iii-c-i)

`email_phase_registry.py` mappa le cinque categorie email a metadata tipizzati
(`CategoryEntry`: flag property, needs_backup, scope strategy) e fornisce
resolver evidence-bound (`resolve_category → ResolvedEvidence`) che estraggono
il payload autoritativo esclusivamente dallo snapshot immutabile e dal suo
contratto — mai da step ID, preview, eventi o request. Ogni resolver valida il
contratto con `is_write_eligible()`, riconcilia flat e contratto, e produce
`kwargs` tipizzati consumabili direttamente dagli engine writer.

Proprietà di sicurezza:
- Forwarder: `_reconcile_endpoint()` valida flat↔contratto **simmetricamente** su
  source e destination; item non-dict, source invalida, destination non-stringa,
  duplicati e mismatch producono reason side-specific redatto.
- Autoresponder: contratto proiettato con soli domini/record selezionati; gate
  completeness/issue, cross-check domain tag flat↔contratto, dedup step_id.
- `is_write_eligible()` (forwarder): `invalid_sources` deve essere `isinstance(list)`;
  source validata con `_is_valid_source()`; destination con `_is_plain_forward()`.
- Nessun import in `dispatch.py`; irraggiungibile dal runtime fino a c-ii/c-iii.

### Destination gateway, single-category executor e backup binding (B4e-iii-c-ii)

`email_category_runtime.py` costruisce il boundary effectful — **ancora irraggiungibile
dal worker** — che collega resolver (c-i), engine writer (B4a/B4b-ii/B4c-ii/B4d-ii/B4e-ii)
e backup store (iii-a):

- **Client factory destination-only** — `_build_destination_client()` legge
  `run.destination_endpoint_id`, richiede `role == "destination"`, risolve credenziali
  dalla destinazione, costruisce `CpanelClient(allow_destination_writes=True)`, chiuso in
  `finally`. Nessun fallback alla sorgente.
- **`ForwarderGateway`** — typed `list_forwarders_op()` (SafeRead) e `add_forwarder_op()`
  (DestinationWrite). Nessun raw `execute()`.
- **Per-category flag** — `is_category_enabled()` usa `REGISTRY[cat].flag_property` con
  exact-match `True`; categoria sconosciuta o property mancante → fail-closed.
- **`run_email_category()`** — single-category executor con pre-gate chain fail-closed:
  unknown_category → category/evidence mismatch → run_id/attempt_id invalid →
  dry_run → before_write callable → run/attempt status running → attempt_run_mismatch →
  fencing_token positive int → destination_endpoint_id → evidence → blocked → flag →
  client. `_is_positive_int()` (`type(v) is int and v > 0`) esclude bool, 0, negativi.
- **Multi-scope** — filtri raggruppati per scope (account/mailbox), autoresponder per
  dominio; un gateway distinto per gruppo; stop dopo il primo gruppo fallito; nessun
  ampliamento degli step.
- **Backup binding** — `_make_backup_persister()` produce una callback che chiama
  `persist_email_backup()` (iii-a) con `run_id`, `attempt_id`, `category`, `item_key`,
  `fencing_token` e fingerprint deterministico SHA-256 (`efp1:` prefix). Disponibile
  **solo** per `default_address` e `email_routing` (`REGISTRY[cat].needs_backup`).
- **Routing inerte** — `policies={}`, clock iniettato, zero write senza policy; la
  categoria non è mai un falso successo quando blocked.
- **Limite fencing** — cPanel non supporta fencing token remoto; la finestra tra l'ultimo
  check locale e la write è inevitabile. Solo fencing PostgreSQL locale è enforced.

Invarianti: `dispatch.py` non importa il modulo; `IMPLEMENTED_REAL_CATEGORIES ==
frozenset({"domains"})`; nessun payload sensibile in reason/repr/eventi. c-iii-a
(coordinatore) è completo; c-iii-b (`[ ]`) cablierà il dispatch nel worker.

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest app/tests/test_email_category_runtime.py \
  --cov=app.modules.executions.email_category_runtime --cov-report=term-missing
```

### Email worker coordinator (B4e-iii-c-iii-a)

`email_worker_coordinator.py` orchestra deterministicamente le categorie email selezionate
dalla preview del run e restituisce un `EmailCoordinationResult` redatto e terminal-agnostic
— **non cablato al worker** fino a c-iii-b:

- **Selezione e ordine** — `_select_categories()` estrae categorie e step ID dalla preview
  in ordine piano, con dedup preservando il primo.
- **Fresh cancellation check** — `_fresh_run_status()` usa `db.no_autoflush` con select
  column-only (mai ORM identity-map) prima di ogni categoria e nel `before_write`.
- **Preflight per-categoria** — flag → scoped `authorize()` → snapshot (ruolo verificato)
  → `resolve_category()` c-i → unresolved/blocked → fail-closed. Categoria sconosciuta,
  disabled, o authorize-rejected → pending, mai falso successo.
- **before_write** — fresh status + scoped `authorize()` + `assert_fencing_current()`.
- **Post-phase fencing-only** — `assert_fencing_current()` dopo la fase, nessun full
  `authorize()` su evidenza/conferma estranea. Fenced-out → zero progress callback.
- **Progress callback** — `persist_progress(checkpoint, compensation)` iniettata, invocata
  solo dopo fencing post-fase. c-iii-b collegherà alla persistenza dell'attempt.
- **Aggregazione** — failure → stop immediato; pending → nessun falso successo; cancelled →
  zero write successive; tutti gli step selezionati compaiono nel risultato.

Invarianti: `dispatch.py` non importa il modulo; `IMPLEMENTED_REAL_CATEGORIES ==
frozenset({"domains"})`; nessun payload sensibile in checkpoint/compensation/repr.

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest app/tests/test_email_worker_coordinator.py \
  --cov=app.modules.executions.email_worker_coordinator --cov-report=term-missing
```

### Store durevole cifrato dei backup pre-write (B4e-iii-a)

I writer compensabili default-address (B4b-ii) e routing (B4c-ii) devono persistere il valore
live **precedente** *prima* di sovrascriverlo (backup-or-nothing). `email_backup.py` fornisce lo
store durevole: tabella `email_write_backups` (migrazione Alembic `0010`) + servizio interno
tipizzato **senza route HTTP** e **senza API di query/list**.

**Chiave di cifratura dedicata.** Il payload protetto è cifrato con `EMAIL_BACKUP_ENCRYPTION_KEY`
(Fernet), **separata** da `CREDENTIAL_ENCRYPTION_KEY` — **nessun fallback silenzioso**. La chiave
serve solo quando si persiste o si legge un backup e la sua assenza fallisce **fail-closed prima
della write**. Generazione:

```bash
python -c "from cryptography.fernet import Fernet; print(Fernet.generate_key().decode())"
```

Formato/versione espliciti (`ebk1:` + `key_version`), serializzazione JSON deterministica che
preserva null/stringhe/numeri/raw; il ciphertext non entra mai in API/log/eventi/`repr`; un
errore di decrypt diventa un errore tipizzato redatto. **La perdita della chiave rende
impossibile il rollback.** La key rotation non è ancora implementata (`key_version` conservato).

**Servizio.** `persist_email_backup(db, run_id, attempt_id, category, item_key,
evidence_fingerprint, payload, fencing_token) -> backup_ref` esegue, fail-closed in ordine:
chiave presente → categoria ammessa (solo `default_address`/`email_routing`) → rilettura e
binding run+attempt → fase reale attiva → destinazione valida → **fencing corrente (A4)** →
schema/size del payload → idempotenza → cifra → commit. Il chiamante riceve il `backup_ref`
opaco (UUID, mai l'id sequenziale) **solo dopo un commit riuscito**; l'operazione è idempotente
(stessa chiave logica + evidence + payload → stesso ref) e non sovrascrive mai un backup
divergente (conflict). `item_key` è una **hash redatta** (mai address/dominio in chiaro).
`load_email_backup(db, backup_ref, expected_run_id, expected_category)` valida
ownership/run/categoria, richiede stato `active`, decifra fail-closed, non enumera e non muta.

**Atomicità (contratto per B4e-iii-c).** Il backup è committato **prima** della write remota; se
il commit fallisce il writer non scrive. Write remota e PostgreSQL non sono un'unica transazione
distribuita: tra commit del backup e write può restare un backup `active` **inutilizzato**
(accettabile e distinguibile); il backup non viene marcato «usato» finché B4e-iii-c non collega
il writer; nessuna transazione DB resta aperta durante la futura chiamata remota.

**Nessun wiring attivo.** B4e-iii-a introduce solo lo store: nessun writer o dispatch è cablato
qui. Le migrazioni hanno **un solo head** (`0010_email_write_backups`); `alembic upgrade head` e
`alembic downgrade 0009_account_leases` sono verificati.

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest app/tests/test_email_backup_store.py \
  --cov=app.modules.executions.email_backup --cov-report=term-missing
```

## Writer cron mock-only con approvazione

`worker.actors.cron_writer.cron_writer_actor` è governato da
`CRON_WRITER_MODE=disabled`. Oltre ai guardrail comuni, richiede che il run
contenga `confirmed_at` e che ogni passo cron sia ancora classificato
`approval` nel piano persistente. La sola presenza del passo nella preview non
costituisce autorizzazione.

La chiave deve avere forma `minuto ora giorno mese giorno_settimana|comando`.
Sono richiesti esattamente cinque campi di pianificazione e un comando non
vuoto; il comando può contenere ulteriori `|`. L'identità idempotente comprende
sia pianificazione sia comando, quindi una variazione non sovrascrive il cron
esistente. Il mock usa la chiamata prevista API 2 `Cron::add_line`, coerente con
l'assenza di un equivalente UAPI, e registra nell'evidenza la conferma forte.

Il valore `real`, endpoint reali e run dry-run sono bloccati; nessuna route/UI
può accodare l'actor.

Test mirato:

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest app/tests/test_cron_writer.py
```

## Writer FTP mock-only

`worker.actors.ftp_writer.ftp_writer_actor` usa `FTP_WRITER_MODE=disabled` e
richiede una nuova password Fernet per ogni passo. Il segreto viene decifrato
solo in memoria, eliminato subito e sostituito con `[REDACTED]` nell'audit. Sono
accettati soltanto login `utente@dominio`; account anonimi, principali o di
servizio come `*_logs` vengono rifiutati.

Il mock simula `Ftp::add_ftp`, controlla prima la presenza del login e verifica
il target simulato. Quota e home directory non vengono inventate: preview e
risultato le marcano esplicitamente `NOT_CONFIGURED`/false. L'inventario dovrà
acquisirle e il planner dovrà richiederle prima di qualsiasi writer reale.
Retry successivi usano il checkpoint persistente; endpoint reali e dry-run sono
bloccati e non esiste dispatch operativo.

Test mirato:

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest app/tests/test_ftp_writer.py
```

## Writer mailing list mock-only

`worker.actors.mailing_list_writer.mailing_list_writer_actor` usa
`MAILING_LIST_WRITER_MODE=disabled` e richiede una nuova password Fernet. Le
risposte cPanel con indirizzo completo oppure campi separati `list` + `domain`
vengono normalizzate in `lista@dominio`.

Il writer legge l'eventuale attributo `private` esclusivamente dallo snapshot
sorgente immutabile. Se manca non inventa un valore: audit e risultato riportano
`[NOT_CONFIGURED]`, che dovrà essere risolto prima del writer reale. Il mock
simula `Email::add_list`, controlla presenza e verifica il target; password e
ciphertext non compaiono negli eventi. Retry usa il checkpoint persistente.
Endpoint reali, dry-run e dispatch operativo restano bloccati.

Test mirato:

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest app/tests/test_mailing_list_writer.py
```

## Writer DNS mock-only additivo

`worker.actors.dns_writer.dns_writer_actor` usa `DNS_WRITER_MODE=disabled` e
richiede conferma forte, passo `approval` e zona già presente nello snapshot
destinazione o verificata dal writer domini. È deliberatamente solo additivo:
accetta esclusivamente elementi `missing_on_destination` dell'esatta
comparazione del run. Stati `different`, `unknown` o `match` vengono bloccati;
non esistono delete o overwrite impliciti.

I record sorgente vengono riletti dallo snapshot immutabile, inclusa la
decodifica Base64. Sono consentiti A, AAAA, CNAME, MX, TXT, CAA e SRV; SOA, NS,
record cPanel e DCV restano esclusi dalla normalizzazione. Se più record
sorgente collassano sulla stessa chiave di confronto, il writer blocca il passo
come ambiguo per evitare omissioni. La chiamata mock prevista è
`DNS::add_zone_record`, seguita da verifica sul target simulato e checkpoint di
retry. Endpoint reali e dispatch operativo restano bloccati.

Test mirato:

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest app/tests/test_dns_writer.py
```

## Writer autoresponder mock-only additivo

`worker.actors.autoresponder_writer.autoresponder_writer_actor` usa
`AUTORESPONDER_WRITER_MODE=disabled` e non è accodato da alcuna route o UI.
`Email::add_auto_responder` è un upsert e sovrascriverebbe un autoresponder già
presente, quindi il writer è deliberatamente additivo e difensivo. Accetta
esclusivamente elementi `missing_on_destination` dell'esatta comparazione del
run; `different`, `unknown`, `match` e `only_on_destination` restano manuali.

Il payload completo (local part, domain, from, subject, body, interval, is_html,
charset, start, stop) viene letto soltanto dallo snapshot sorgente immutabile.
Se il dettaglio sorgente non è `succeeded` o manca un campo necessario
(`from`, `subject`, `body`, `interval`) il passo è bloccato con istruzione
manuale, senza inventare valori. Un `interval` pari a `0` è un payload valido.

Prima di ogni scrittura il writer esegue un fresh mock pre-write check per
indirizzo: se nel target è comparso — dopo lo snapshot di piano — un
autoresponder differente, il passo è bloccato perché la scrittura lo
sovrascriverebbe. Un autoresponder comparso ma byte-identico al payload di piano
è trattato come `already_present` idempotente. La chiamata prevista è
`Email::add_auto_responder`, seguita da verifica sul target mock e checkpoint di
retry.

Corpo, oggetto e mittente possono essere dati sensibili: non compaiono mai in
chiaro né nei messaggi né nella chiamata prevista persistente. L'audit conserva
soltanto metadati non sensibili (`interval`, `is_html`, `charset`, `start`,
`stop`), i campi sensibili redatti come `[REDACTED]` e un `payload_fingerprint`
deterministico che verifica l'equivalenza senza esporre il contenuto.

Test mirato:

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest app/tests/test_autoresponder_writer.py
```

## Orchestrazione mock end-to-end

`worker.actors.mock_orchestrator.mock_orchestrator_actor` coordina i writer mock
in un unico execution run non dry-run ed è gateato da
`MOCK_ORCHESTRATOR_MODE=disabled` (il valore `real` è rifiutato). Nessuna
route/UI lo accoda in questo incremento.

Ogni writer espone ora un contratto di fase condiviso — `validate_phase` (i
guardrail di sicurezza) e `apply_phase` (esecuzione e verifica) — mentre lo
`execute` standalone resta invariato e continua a essere gateato dal flag
per-writer. L'orchestratore riusa lo stesso contratto senza forzare
ripetutamente `run.status=queued`: lo stato terminale del run appartiene solo
all'orchestratore, così nessuna singola fase marca il run `succeeded` mentre
restano fasi da eseguire.

I flag per-writer (`DOMAIN_WRITER_MODE`, …) gateano **solo** il percorso
standalone: un run orchestrato è gateato da `MOCK_ORCHESTRATOR_MODE` più
`auth_type=mock` sull'endpoint, non dai singoli flag (comportamento
intenzionale, coperto da test di regressione). Come difesa in profondità
l'orchestratore rifiuta però esplicitamente qualunque categoria il cui flag
per-writer sia `real`: un writer reale non è implementato e non deve essere
eseguito nemmeno tramite l'orchestratore.

Flusso:

1. **Pre-validazione** prima di qualsiasi fase — run non dry-run e in coda,
   endpoint destinazione mock, coerenza piano/comparazione/snapshot, modalità dei
   passi (`manual`, `excluded` e categorie sconosciute rifiutate), conferma forte
   per i passi `approval`, password cifrate presenti e dipendenze selezionate. Un
   errore di pre-validazione non esegue alcuna fase e non muta il run.
2. **Ordine deterministico**: `domains` → `databases` → `mysql_users` →
   `email_forwarders` → `cron_jobs` → `ftp_accounts` → `mailing_lists` →
   `dns_records` → `email_autoresponders`. Le dipendenze
   (database→utente MySQL, dominio→DNS) si propagano tramite gli eventi
   verificati già persistiti nel run.
3. **Arresto al primo blocco**: se una fase fallisce o richiede intervento
   manuale (es. race anti-upsert dell'autoresponder), le categorie successive non
   vengono eseguite, il run passa a `failed` e l'audit registra i passi riusciti
   e quelli non eseguiti. Nessuna compensazione o cancellazione automatica.
4. **Retry**: rieseguendo l'orchestratore i checkpoint già verificati vengono
   saltati (`already_completed`) e l'ordine resta identico.
5. **Verifica finale**: lo stato mock condiviso viene ricostruito
   ESCLUSIVAMENTE dagli eventi immutabili del run (non dai risultati restituiti
   dai writer) e riletto per confermare che ogni passo selezionato risulti
   presente (`evidence=shared_mock_state_reread`). Gli eventi aggregati non
   contengono segreti né contenuti sensibili degli autoresponder.

Il percorso reale futuro sostituirà le fasi mock e la rilettura dello stato mock
con scritture account-level reali seguite da un nuovo preflight e una nuova
comparazione della destinazione.

Test mirato:

```bash
cd apps/api
PYTHONPATH=../../packages/adapters python -m pytest app/tests/test_mock_orchestrator.py
```

## Configurazione e stato dei writer

Tutti i writer sono disabilitati per default e privi di route/UI di dispatch. Il
valore `real` non è implementato e viene rifiutato dal codice. **Eccezione
domini**: il writer reale è raggiungibile dal worker sotto il doppio gate
`REAL_EXECUTION_MODE=enabled` + `DOMAIN_WRITER_MODE=enabled` (vedi «Fase domini
reale nel worker (B3b-ii)»); per `DOMAIN_WRITER_MODE` sono ammessi solo
`disabled`/`mock`/`enabled`, ogni altro valore è rifiutato fail-closed al load.

| Writer | Variabile | Stato predefinito |
|--------|-----------|-------------------|
| Domini | `DOMAIN_WRITER_MODE` | `disabled` |
| Database MySQL | `DATABASE_WRITER_MODE` | `disabled` |
| Utenti e grant MySQL | `MYSQL_USER_WRITER_MODE` | `disabled` |
| Forwarder | `FORWARDER_WRITER_MODE` | `disabled` |
| Default address (catch-all) | `DEFAULT_ADDRESS_WRITER_MODE` | `disabled` |
| Email routing (mail route) | `ROUTING_WRITER_MODE` | `disabled` |
| Email filters | `FILTER_WRITER_MODE` | `disabled` |
| Cron | `CRON_WRITER_MODE` | `disabled` |
| FTP | `FTP_WRITER_MODE` | `disabled` |
| Mailing list | `MAILING_LIST_WRITER_MODE` | `disabled` |
| DNS | `DNS_WRITER_MODE` | `disabled` |
| Autoresponder | `AUTORESPONDER_WRITER_MODE` | `disabled` |

L'orchestrazione mock end-to-end è governata dal flag separato
`MOCK_ORCHESTRATOR_MODE` (default `disabled`, `real` rifiutato), anch'esso privo
di route/UI di dispatch.

Non impostare questi flag a `mock` nello stack pilota: tale modalità è destinata
ai test con endpoint `auth_type=mock`. L'abilitazione futura richiederà un nuovo
contratto di execution non dry-run, conferma separata, pre-write re-check e
preflight/comparazione post-write.

## Stato verificato dell'account pilota

Fotografia al termine dell'ultimo incremento (gli ID sono storici e devono
sempre essere riletti dalle API prima dell'uso):

- migrazione `1`;
- ultimo preflight read-only: job `20`, `succeeded`;
- snapshot sorgente/destinazione: `39` / `40`;
- comparazione corrente: `19`;
- piano corrente: `13`;
- readiness report corrente: `11`;
- readiness categorie operative: `needs_inventory=0`, `needs_contract_test=0`,
  `eligible_for_real_design=7` (database, utenti MySQL, forwarder, FTP,
  mailing list, DNS e autoresponder);
- matrice grant: sorgente 6/6 coppie e 3 grant, destinazione 1/1 e 1 grant;
- FTP quota/home completo; mailing-list privacy verificata da campi espliciti;
- DNS `succeeded` su entrambi i lati, senza interrogare i sottodomini come zone;
- autoresponder sorgente: 3, tutti con dettaglio riuscito;
- autoresponder destinazione: 0;
- tutti e tre risultano `missing_on_destination` e restano `manual`;
- tutti i flag writer sono `disabled`;
- execution run non dry-run nel database: 0.

La baseline di test corrente è 117 test API e 17 test worker. La build frontend,
`docker compose config -q`, health API e stack Docker risultano verdi.

## Limitazioni e prossimi incrementi

- Nessun writer reale o dispatch operativo è disponibile.
- Il writer autoresponder è disponibile solo in modalità mock e protegge
  dall'upsert; il fresh check reale UAPI non è ancora implementato.
- Database e utenti MySQL sono eleggibili soltanto per il design reale; non
  esiste ancora un execution contract né un writer reale autorizzato.
- FTP, mailing list, forwarder e autoresponder hanno contract evidence
  read-only; la fotografia pilota va rigenerata prima di aggiornare il loro
  stato readiness storico.
- DNS dispone del contratto read-only; il futuro writer reale dovrà eseguire la
  fresh read della zona immediatamente prima e dopo la scrittura.
- PHP resta manuale perché il writer documentato richiede privilegi WHM.
- SSL non copia mai chiavi private e dovrà essere rigenerato tramite AutoSSL.
- I contract test read-only pianificati sono completi. Nessuna evidenza
  autorizza scritture reali o sostituisce la futura verifica pre/post-write.

## Sviluppo locale (senza Docker)

### Ambiente Python riproducibile (workflow unico)

Un **solo virtualenv nella root** di `migration-platform/` con tutti i pacchetti
installati editable. È il workflow di riferimento sia in locale sia in CI; le
immagini Docker installano gli stessi pacchetti (vedi `apps/*/Dockerfile`), così
i due percorsi restano allineati. Serve Python **3.11+**.

```bash
cd migration-platform
make setup            # crea .venv e installa domain, adapters, api, worker (con extra test)
source .venv/bin/activate
```

Equivalente senza `make`:

```bash
python3 -m venv .venv && source .venv/bin/activate
pip install -U pip
pip install -e packages/domain -e packages/adapters \
    -e "apps/api[test]" -e "apps/worker[test]"
```

Il worker dipende da `dramatiq`; il comando sopra lo installa insieme al
pacchetto API di cui gli actor hanno bisogno. Senza questo passo i test del
worker falliscono in fase di collection con `ModuleNotFoundError: dramatiq`.

Gate host-side (con la venv attiva, dopo `make setup`):

```bash
make test             # api-test + worker-test + web-build in ordine di dipendenza
# oppure singolarmente:
make api-test         # 117 test, SQLite in-memory, nessun Postgres
make worker-test      # 17 test, StubBroker, nessun Redis
make web-build
```

### API (dev server)

```bash
cd apps/api
# venv già attiva da `make setup`
alembic upgrade head          # usa DATABASE_URL o il default SQLite
uvicorn app.main:app --reload
```

### Worker (esecuzione reale)

```bash
cd apps/worker
# venv già attiva da `make setup`; i writer reali restano disabilitati
dramatiq worker.main          # richiede Redis attivo
```

I test del worker restano ermetici: `conftest.py` forza `DRAMATIQ_TESTING=1`
prima di importare qualsiasi modulo `worker.*`, quindi usano sempre lo
`StubBroker` e non aprono connessioni a Redis. Nessun writer reale viene
attivato dai test.

### Web

```bash
cd apps/web
npm install
npm run dev        # http://localhost:5173
npm run build      # gate di compilazione
```

Validazione completa dello stack:

```bash
docker compose config -q
docker compose up -d --build
curl --fail http://localhost:8000/health
docker compose exec api alembic current
docker compose ps
```

## Principio architetturale

> **Tutti i job e le esecuzioni aggiornano PostgreSQL.** La coda è volatile e
> trasporta soltanto ID. Snapshot, confronti, piani, selezioni, eventi, risultati
> e verifiche devono restare ricostruibili dal database. La sorgente è sempre
> read-only; la destinazione è l'unico target possibile; uno stato non leggibile
> non equivale mai a una risorsa vuota.
```
