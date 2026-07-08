# Sprint 3.5 — Inventory Coverage Audit & Gap Closure

> Read-only. No writes, no migration, no rsync/SSH/IMAP/MySQL dump, no DNS
> changes, no apply/cutover/rollback, no remediation. Only verified read-only
> cPanel API functions. No token / `auth_ref` / `Authorization` in any
> snapshot, report, log or API response.

## Obiettivo

Sapere con precisione **cosa** l'inventory cPanel legge in read-only, **cosa non
legge** e **perché**, distinguendo `succeeded / empty / partial / unsupported /
unavailable / failed / unverified`. Chiudere il gap P0 su **cron** e **DNS** e
impedire che una categoria non leggibile generi falsi blocker nella comparison.

## Non-obiettivi

- Nessuna scrittura cPanel (create/edit/delete di email, db, file, cron, domini,
  zone).
- Nessuna migrazione reale, rsync, SSH/IMAP reale, MySQL dump.
- Nessuna modifica DNS. `DNS::parse_zone` è **sola lettura**.
- Nessuna auth completa, nessun Celery/RQ/BackgroundTasks.
- Nessun refactor largo: si estende l'adapter esistente, non lo si riscrive.

## Stato attuale inventory (prima di questa PR)

`packages/adapters/adapters/cpanel/inventory.py` legge via UAPI:

| Categoria | Funzione | Esito reale |
|---|---|---|
| account | `StatsBar::get_stats` | ok |
| domains | `DomainInfo::list_domains` | ok (gate connect/auth) |
| email_accounts | `Email::list_pops` | ok |
| databases | `Mysql::list_databases` | ok |
| ssl | `SSL::installed_hosts` | ok |
| cron_jobs | `Cron::list_cron` (UAPI) | **rotto** — vedi diagnosi |
| dns_records | — (non tentato) | assente per design |

Lo snapshot persiste `summary` (conteggi) + `data` (liste normalizzate) come JSON.
Le capabilities erano probe-driven ma **binarie** (`can_read_x` true/false), senza
distinguere empty / unsupported / unavailable / failed.

## Gap rilevati

1. **Cron non compare in runtime reale.** Vedi diagnosi qui sotto: `Cron::list_cron`
   **non è una funzione UAPI**.
2. **DNS non letto.** Nessun tentativo; `can_read_dns` hardcoded a false.
3. **Nessuna coverage matrix.** Impossibile distinguere "letto e vuoto" da "non
   supportato" o "fallito". Rischio di falsa sensazione di completezza.
4. **Capabilities troppo grezze** per guidare la comparison in modo onesto.

## Diagnosi cron (runtime reale)

Investigazione del codice + verifica sulla documentazione ufficiale cPanel & WHM
v136 (`api.docs.cpanel.net`):

- Il codice chiamava `self._client.call_uapi("Cron", "list_cron")` →
  `GET /execute/Cron/list_cron`.
- **Verifica doc**: cercando `list_cron` e `cron job` nella *API Reference*
  (che indicizza UAPI + WHM API 1) **non esiste alcuna funzione UAPI `Cron`**.
  Le uniche funzioni Cron sono in **cPanel API 2** (deprecata ma ancora
  supportata): `Cron::listcron`, `Cron::fetchcron`, `Cron::add_line`, ecc.
  Da notare: il nome non è nemmeno `list_cron` ma **`listcron`** (senza underscore).
- **Causa reale**: la chiamata UAPI a una funzione inesistente ritorna, sul
  cPanel reale, HTTP 404 (→ `CpanelUnsupportedFunctionError`) oppure un envelope
  UAPI con `status=0` (→ `CpanelApiError`). Entrambe cadono in `except CpanelError`
  dentro `_read`, quindi `can_read_cron` restava `False` e il cron finiva in
  `capabilities.limitations` come `cron_read_unavailable`, con conteggio `None`.
  Il normalizer `_norm_cron` (che si aspettava una `list`) non veniva mai
  raggiunto sul reale; nei test passava solo perché il mock restituiva una lista.
- **Perché i test non lo prendevano**: `test_inventory_source.py` mockava
  `("Cron","list_cron"): [ {...} ]` come lista → percorso felice fittizio.

**Fix scelto** (deciso col committente): leggere il cron via **cPanel API 2
`Cron::listcron`** — funzione ufficiale di sola lettura (nessun parametro).
È una deroga *esplicita e documentata* al vincolo "solo UAPI", giustificata dal
fatto che UAPI non offre alcun modo di leggere il cron; resta comunque read-only,
no-write, no-segreti. Il client viene esteso con un metodo read-only `call_cpapi2`.

Envelope verificato di `Cron::listcron` (API 2):

```json
{ "cpanelresult": {
    "apiversion": 2, "func": "listcron", "module": "Cron",
    "data": [ {"minute":"0","hour":"2","day":"*","month":"*","weekday":"*",
               "command":"...","command_htmlsafe":"...","linekey":"...","count":1},
              {"count": 3} ],
    "event": {"result": 1} } }
```

`data` è una **lista**; successo = `cpanelresult.event.result == 1`; l'ultimo item
è un artefatto di conteggio (solo `count`) e va scartato. Il **command non viene
mai salvato** (può contenere segreti: `mysqldump -pXXX`, URL con token…): si
persiste solo lo schedule + `command_present: true`.

## Catalogo UAPI/API2 candidate (verificato su doc ufficiale v136)

| Categoria | Modulo::funzione | API | Read-only | Decisione | Motivo |
|---|---|---|---|---|---|
| cron_jobs | `Cron::listcron` | cPanel API 2 | sì (GET/list) | **implementata** | unico modo di leggere il cron |
| dns_records | `DNS::parse_zone` | UAPI | sì (GET, param `zone`) | **implementata** | funzione UAPI read-only verificata |
| email_forwarders | `Email::list_forwarders` | UAPI | sì (GET) | **implementata** (P1) | verificata read-only |
| email_autoresponders | `Email::list_auto_responders` | UAPI | sì (GET) | **implementata** (P1) | verificata read-only |
| ftp_accounts | `Ftp::list_ftp` | UAPI | sì (GET, no password) | **implementata** (P1) | verificata read-only |
| redirects | — | — | non verificato | **esclusa** → `unverified` | nessuna funzione UAPI read-only chiara (Mime è API2/htaccess) |
| email_filters | `Email::list_filters` | UAPI | non verificata in questa PR | **esclusa** → `unverified` | P2, non verificata |
| mailing_lists | `Email::list_lists` | UAPI | non verificata | **esclusa** → `unverified` | P2 |
| php_settings | MultiPHP | — | non verificata | **esclusa** → `unverified` | P2 |
| postgres_databases | `Postgresql::list_databases` | UAPI | non verificata | **esclusa** → `unverified` | P2 |
| subaccounts | `SubAccount::list_subaccounts` | UAPI | non verificata | **esclusa** → `unverified` | P2 |

Regola applicata: se una funzione non è verificata read-only sulla doc ufficiale,
**non** viene usata; la categoria è marcata `unverified`.

## Funzioni implementate

- **cron_jobs** → `Cron::listcron` (API 2), normalizzato a schedule + `command_present`.
- **dns_records** → `DNS::parse_zone` (UAPI), una chiamata per zona (main/addon/parked),
  decodifica base64 di `dname_b64`/`data_b64`, normalizzato a `{domain,name,type,value,ttl}`.
- **email_forwarders** → `Email::list_forwarders` (UAPI).
- **email_autoresponders** → `Email::list_auto_responders` (UAPI).
- **ftp_accounts** → `Ftp::list_ftp` (UAPI), solo `user`/`type` (mai password).

## Funzioni escluse

redirects, email_filters, mailing_lists, php_settings, postgres_databases,
subaccounts → coverage `unverified` (non implementate perché non verificate come
read-only in questa PR). Nessuna chiamata effettuata.

## Modello coverage matrix

Vive in `data["coverage"]` (nessuna migration DB, nessun cambio schema API —
`data` è già JSON persistito e restituito). Per ogni categoria:

```json
{ "status": "succeeded|empty|partial|unsupported|unavailable|failed|unverified",
  "method": "DNS::parse_zone" ,   // null se non implementata
  "read_only_verified": true,
  "items_count": 3,               // null se non applicabile
  "message": null }
```

Derivazione status:

| Esito lettura | status |
|---|---|
| call ok, item > 0 | `succeeded` |
| call ok, 0 item | `empty` |
| alcune zone/parti ok, altre no (solo DNS) | `partial` |
| `CpanelUnsupportedFunctionError` / HTTP 404 | `unsupported` |
| `CpanelApiError` (status=0, modulo disabilitato/non disponibile) | `unavailable` |
| `CpanelParseError` / shape inattesa | `failed` |
| categoria non implementata / non verificata | `unverified` |
| connection/timeout/auth | fatale → snapshot intero `failed` (comportamento Sprint 2) |

Le **capabilities derivano dalla coverage**: `can_read_<x> = status ∈ {succeeded, empty}`.
`limitations` = lista di `<categoria>_<status>` per le categorie non leggibili.

## Modifiche snapshot

- `data["coverage"]` aggiunto (matrix sopra). Nessun secret.
- `summary.cron_jobs_count` e `summary.dns_records_count` ora valorizzati.
- Snapshot legacy senza `coverage` restano validi (campo opzionale ovunque).

## Modifiche comparison

- Nuova list-category `dns_records` (severità WARNING su missing/only/different:
  il DNS si ripunta al cutover, non è un blocker di per sé).
- Gating invariato + coverage-aware: se una categoria non è leggibile
  (`can_read_x` False, derivato da coverage) su source o destination, il confronto
  per-item viene **saltato** (nessun falso `missing_on_destination`).
- Nuove entry `category: "coverage"` (`state: unknown`, `severity: warning`) per
  ogni categoria dati (domains, email_accounts, databases, cron_jobs, ssl,
  dns_records, email_forwarders, email_autoresponders, ftp_accounts) non leggibile
  su almeno un lato. Le categorie P2 `unverified` NON generano warning di
  comparison (sono mostrate solo nella coverage matrix in UI) per evitare rumore.
- Snapshot legacy (senza coverage) → nessuna entry coverage, comportamento invariato.

## Modifiche UI

- Nuovo pannello **"Copertura inventory"** (source + destination): per ogni
  categoria mostra stato (Letto/Vuoto/Parziale/Non supportato/Non verificato/
  Fallito), metodo UAPI/API2, count, messaggio. Nessuna frase "tutto letto/completo"
  se ci sono stati non-ok.
- Tipi TS aggiornati (`CoverageEntry`, `capabilities` estese).

## API

Scelta: **nessuna nuova route.** `coverage` viaggia dentro le response inventory
esistenti (`GET /api/migrations/{id}/inventory` → `snapshot.data.coverage`).
Motivo: retro-compatibile, zero superficie nuova, il read-model UI lo consuma già
da `data`. Le route dedicate `/inventory/coverage[...]` sono opzionali e rimandate.

## Test previsti

Adapter: cron API2 (succeeded/empty/unavailable/unsupported), command mai in
data/snapshot, DNS (succeeded/empty/partial/unavailable/unverified), decode base64,
capabilities coerenti con coverage, nessun token/auth_ref/Authorization.
API: response include coverage, coverage senza secret, snapshot legacy senza
coverage non rompe. Comparison: categoria unreadable → nessun falso blocker;
dns/cron unreadable → warning coverage/unknown; dns readable + missing → warning.
UI: `npm run build`.

## Rischi aperti

- **Smoke reale cPanel**: eseguito/non eseguito dichiarato in PR (nessuna
  credenziale reale disponibile in sessione → dichiarato esplicitamente se non fatto).
  Il bug nasce dal runtime reale: la validazione finale su un account reale resta
  l'unico modo per certificare cron+DNS end-to-end.
- **DNS::parse_zone** richiede una chiamata per zona: costo O(numero domini).
  Bounded ai domini main/addon/parked (i subdomain condividono la zona del padre).
- **DNS su hosting condiviso**: se l'account non ha autorità locale sulla zona,
  `parse_zone` può dare `unavailable`/`empty` — gestito dalla coverage, non è un errore.
- **cron via API 2**: deroga esplicita al "solo UAPI"; API 2 è deprecata ma
  ancora supportata in v136. Se cPanel la rimuovesse, coverage → `unsupported`.
- **Valori DNS (TXT)**: il DNS è informazione pubblica; si salva il valore
  decodificato (troncato). Nessun segreto *nostro*; eventuali contenuti pubblicati
  dall'utente restano suoi.
