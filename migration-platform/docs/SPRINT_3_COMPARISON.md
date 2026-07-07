# Sprint 3 — Read-only Comparison Engine (micro-design)

> Trasforma gli inventory snapshot (source/destination) di Sprint 2 in un
> report leggibile dall'operatore: cosa manca, cosa è diverso, cosa può
> bloccare una migrazione futura. **Nessuna migrazione reale, nessun write.**

## Obiettivo

- Dato l'ultimo snapshot `succeeded` di **source** e quello di **destination**,
  calcolare un delta per categoria e classificarlo in `blocker | warning | info`.
- Persistere un `comparison_report` (nuova tabella + Alembic `0004`).
- Esporre API di generazione/lettura/lettura-entry-filtrate.
- Mostrare in UI un pannello comparativo (summary + tabella entry + filtri).

## Non-obiettivi (fuori scope, invariati)

Nessuna migrazione reale · nessun write cPanel · nessun rsync/SSH/IMAP/MySQL
dump · nessun DNS write · nessun apply/cutover/rollback · nessuna remediation
automatica · nessun migration plan esecutivo · nessun codice legacy Go · nessuna
auth API completa · nessun BackgroundTasks/Celery/RQ.

## Decisione: engine sincrono, NON worker

L'handoff Sprint 2 ipotizzava un job `comparison`. **In questa PR il comparison è
sincrono lato API** perché è puro CPU + read DB (nessuna rete, nessun I/O lento):
un worker aggiungerebbe accoppiamento producer/consumer e stato asincrono senza
beneficio. `JobType.COMPARISON` resta nell'enum ma **non** viene usato qui. Il
confine resta rispettato: Postgres fonte di verità, nessun nuovo trasporto.

## Contratto dati REALE di Sprint 2 (verificato, non l'esempio del prompt)

`InventorySnapshot.data` (scritto dal worker preflight) ha queste chiavi:

```
domains:        [{"domain": str, "type": "main|addon|parked|sub"}]
email_accounts: [{"email": str|None, "domain": str|None}]
databases:      [{"name": str}]
cron_jobs:      [{"minute"?, "hour"?, "day"?, "month"?, "weekday"?}]   # solo schedule
ssl:            [{"host": str}]                                        # NON "ssl_items"
dns:            None                                                   # mai letto in Sprint 2
account, warnings: ignorati dal comparison
```

Le **capabilities NON sono nello snapshot**: vivono su `Endpoint.capabilities`
(JSON, shape `CapabilityReport`: `can_read_domains/email/databases/cron/ssl/dns/
account_info`, tutti bool). Il servizio API le inietta nell'input dell'engine.

> Riconciliazione col prompt: la categoria che il prompt chiama `ssl_items`
> mappa sulla chiave reale `ssl`; il cron reale non ha `command` (solo schedule).
> L'engine legge le **chiavi reali** di Sprint 2, non la fixture illustrativa.

## File nuovi

```
packages/domain/domain/comparison_engine.py      # dominio puro: fingerprint + compare
apps/api/app/modules/comparison/__init__.py
apps/api/app/modules/comparison/models.py        # tabella comparison_reports
apps/api/app/modules/comparison/schemas.py       # Pydantic read
apps/api/app/modules/comparison/service.py       # find snapshot + run engine + persist + filtri
apps/api/app/modules/comparison/router.py        # POST/GET/GET entries
apps/api/alembic/versions/0004_comparison_reports.py
apps/api/app/tests/test_comparison_engine.py     # test dominio (gira sotto pytest api)
apps/api/app/tests/test_comparison_api.py         # test API
apps/web/src/features/migrations/ComparisonPanel.tsx
apps/web/src/features/migrations/ComparisonSummaryCards.tsx
apps/web/src/features/migrations/ComparisonEntriesTable.tsx
apps/web/src/features/migrations/SeverityBadge.tsx
apps/web/src/features/migrations/StateBadge.tsx
docs/SPRINT_3_COMPARISON.md
```

## File modificati

```
apps/api/app/main.py                 # include comparison_router
apps/api/alembic/env.py              # import comparison models (metadata)
apps/api/app/tests/conftest.py       # import comparison models (metadata)
apps/web/src/lib/api.ts              # tipi + generate/fetch comparison + entries
apps/web/src/features/migrations/MigrationSetupPage.tsx  # monta ComparisonPanel
apps/web/src/index.css               # classi summary cards / tabella / badge
```

`packages/domain/domain/comparison.py` (reference Pydantic minimale di Sprint 0)
resta **intatto**; l'engine è un modulo nuovo e self-contained.

## Nuova tabella Alembic — `comparison_reports` (0004, down_revision 0003)

```
id                     PK
migration_id           FK migrations(id)            ON DELETE CASCADE, indexed
source_snapshot_id     FK inventory_snapshots(id)   ON DELETE CASCADE
destination_snapshot_id FK inventory_snapshots(id)  ON DELETE CASCADE
status                 pending|running|succeeded|failed  (default pending)
summary                JSON
entries                JSON
blockers_count         Integer default 0
warnings_count         Integer default 0
infos_count            Integer default 0
error                  Text null
created_at/updated_at  timestamptz
```

Vincoli di sicurezza: nessun secret, nessun `auth_ref`, nessuna raw response
cPanel. Le `entries` contengono solo `key` (identità naturale non sensibile:
dominio/email/nome-db/host/schedule) + `fingerprint` (hash SHA-256 opaco). **Mai
l'item normalizzato grezzo** → impossibile far trapelare un campo per costruzione.

## Modello comparison (entry normalizzata)

```json
{
  "category": "email_accounts",
  "key": "info@example.com",
  "state": "missing_on_destination",
  "severity": "blocker",
  "title": "...",
  "message": "...",
  "source":      {"exists": true,  "fingerprint": "<sha256>"},
  "destination": {"exists": false, "fingerprint": null}
}
```

Stati: `match | missing_on_destination | only_on_destination | different | unknown`.
Severity: `blocker | warning | info`.

`summary`:

```json
{
  "blockers_count": N, "warnings_count": N, "infos_count": N,
  "categories": ["domains","email_accounts","databases","cron_jobs","ssl","capabilities"],
  "by_category": {"domains": {"source":s,"destination":d,"match":m,"blocker":b,"warning":w,"info":i}, ...}
}
```

## Fingerprint — `stable_fingerprint(item: dict) -> str`

- Rimuove ricorsivamente chiavi **volatili** (`captured_at,created_at,updated_at,
  timestamp,last_checked_at,id,count`) e qualsiasi chiave che sembri un
  **segreto** (`password,token,secret,auth,authorization,header,api_key,
  credential,private_key`, match case-insensitive per sottostringa).
- Serializza in JSON canonico `sort_keys=True` (⇒ indipendente dall'ordine),
  separatori compatti, `ensure_ascii=False`, `default=str`.
- Ritorna `sha256(...).hexdigest()`.

Test: stabile a parità di contenuto indipendentemente dall'ordine chiavi; ignora
campi volatili; nessun segreto entra nell'hash né altrove.

## Algoritmo di confronto (`compare(source, destination) -> ComparisonOutput`)

Puro: input due dict (data + `capabilities` iniettata), nessun DB/rete/FastAPI.

**Gating per capability** (fix review): se un lato ha `can_read_<cat>` esplicito
`False`, la sua lista è vuota per motivi di capability, non perché gli item
siano stati rimossi. Confrontare per item fabbricherebbe falsi `missing/only`
→ la categoria viene **saltata** (`by_category[cat].skipped=True`) e il segnale
reale è portato dalla categoria `capabilities`. Il gating scatta solo su `False`
esplicito (capabilities `None`/assenti ⇒ confronto normale).

Categorie a lista (`domains,email_accounts,databases,cron_jobs,ssl`):
1. costruisci mappa `key -> item` per source e destination (key = identità
   naturale per categoria; duplicati → prima occorrenza, esistenza è ciò che conta).
2. per ogni key in `union(source,dest)` ordinata:
   - in entrambe → `match` se fingerprint uguali, altrimenti `different`.
   - solo source → `missing_on_destination`.
   - solo dest → `only_on_destination`.
3. `match` → conteggiato in `by_category.match`, **omesso** dalle entry di
   dettaglio (prompt lo consente). Gli altri stati → entry con severity da regole.

Chiavi naturali: domains=`domain` (lower); email=`email` (lower); databases=
`name` (lower); ssl=`host` (lower); cron=firma schedule `"min hour day month
weekday"` con `*` per campi assenti (command non disponibile in Sprint 2).

Capabilities (dict di bool, gestita a parte): confronta `can_read_{domains,email,
databases,cron,ssl,dns,account_info}`.
- entrambe true → match (omesso).
- source true, dest false → `missing_on_destination`; severity `blocker` se
  capability **critica** (`domains,email,databases`), altrimenti `warning`.
- source false, dest true → `only_on_destination` → `info`.
- entrambe false → `unknown` (categoria non verificabile) → `warning`.
- Se **entrambe** le capabilities sono assenti (None) → categoria capabilities
  saltata (niente da confrontare). Se solo una è assente → l'altra vs tutto-false.

Entry ordinate `by (severity: blocker<warning<info, categoria, key)`.

## Categorie confrontate e regole di severità

| Categoria | missing_on_dest | only_on_dest | different |
|-----------|-----------------|--------------|-----------|
| domains | **blocker** | warning | warning |
| email_accounts | **blocker** | warning | warning |
| databases | **blocker** | warning | warning |
| cron_jobs | warning | warning | warning |
| ssl | warning | info | warning |
| capabilities | blocker (critica) / warning | info | — (both-false ⇒ unknown⇒warning) |

`match` → conteggiato ma omesso dal dettaglio.

## API aggiunte

```
POST /api/migrations/{id}/comparison   -> 201 report (status succeeded) | 404 migr | 409 snapshot mancante
GET  /api/migrations/{id}/comparison   -> 200 ultimo report | 404 nessun report
GET  /api/migrations/{id}/comparison/entries?severity=&category=&state=
                                        -> 200 entry filtrate+ordinate | 404 nessun report
```

`POST`: 404 se migration inesistente; trova ultimo snapshot `succeeded`
source+dest; se ne manca uno → **409 chiaro**; esegue engine sincrono; salva
report `succeeded`; lo ritorna. Se l'engine solleva un'eccezione inattesa,
persiste un report `failed` con `error` (audit trail, come il worker preflight)
e la rilancia (500). `GET entries`: filtri opzionali combinabili; `severity` e
`state` sono `Literal` (valore invalido → 422).

## UI aggiunta

`ComparisonPanel` (in `MigrationSetupPage`, sotto l'inventario): bottone
**"Genera comparativa"**, empty state, errore leggibile se mancano snapshot,
`ComparisonSummaryCards` (Blocchi critici / Avvisi / Informazioni + categorie),
`ComparisonEntriesTable` con `SeverityBadge`/`StateBadge` e filtri client per
severity/category. Nessuna promessa di migrazione automatica.

## Test previsti

- **Engine**: match non produce blocker; missing domain/email/db → blocker;
  missing cron/ssl → warning; only-on-dest per categoria (domain warning, ssl
  info); different fingerprint → warning; fingerprint stabile per ordine chiavi;
  fingerprint ignora volatili; nessun secret/token/auth nelle entry; capabilities
  source-true/dest-false critica → blocker, source-false/dest-true → info, both
  false → warning.
- **API**: POST senza snapshot → 409; solo source → 409; source+dest → 201
  succeeded; GET latest → 200; GET missing → 404; GET entries → lista; filtri
  severity/category/state; report senza secret/auth_ref/token/Authorization.
- **Worker**: nessun nuovo worker; non rompere i test esistenti.
- **Frontend**: `npm run build` (tsc + vite).

## Rischi aperti / debiti veri

1. **Cron a bassa fedeltà**: solo schedule (no command) ⇒ due cron con stessa
   schedule sono indistinguibili; il delta è a granularità schedule, la
   molteplicità si perde. Onesto ma limitato.
2. **SSL/DB a bassa fedeltà**: `ssl` ha solo `host`, `databases` solo `name` ⇒
   `different` non può emergere (key == contenuto). Nessun confronto scadenza
   certificato / contenuto DB (fuori scope e non disponibile).
3. **Capabilities `dns` sempre false** su entrambi (Sprint 2 non legge DNS) ⇒
   una warning capabilities `dns` ricorrente ma onesta.
4. **Auth API assente** (debito Sprint 2 prioritario prima del non-localhost).
5. **Nessuna paginazione** entries: dataset piccoli in Sprint 2, accettabile.
