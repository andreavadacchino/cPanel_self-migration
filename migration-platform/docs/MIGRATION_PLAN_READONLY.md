# Migration Plan (read-only) — Sprint 4A

Un **piano di migrazione read-only**: una fotografia derivata da inventory,
coverage e comparison che guida l'operatore, **senza eseguire nulla**. Nessun
write cPanel, nessun DNS, nessun DB/utente/email, nessun rsync/SSH/IMAP/dump,
nessun apply/cutover/rollback, nessun worker di migrazione, nessun bottone di
esecuzione. Scope: solo `migration-platform/`.

---

## Scopo

Trasformare la piattaforma da «strumento che confronta» a «strumento che guida
l'operatore»: dato lo stato letto (inventory source + destination + comparison +
coverage + capabilities), produrre un piano leggibile che dica cosa è allineato,
cosa manca, cosa è bloccante, cosa richiede intervento manuale, cosa è
sconosciuto perché non leggibile, e cosa non deve essere automatizzato ora.

Il piano è una **proiezione pura** dello stato corrente: non introduce giudizi
nuovi di gravità, **eredita le severità dalla comparison** (single source of
truth) e le instrada in sezioni. Questo garantisce «non crea falsi blocker» e
«deriva tutto da inventory/comparison/coverage».

## Input richiesti

Il piano si genera solo se esistono tutti e tre:

```
- latest source inventory snapshot (status succeeded)
- latest destination inventory snapshot (status succeeded)
- latest comparison report (status succeeded)
```

Se manca inventory o comparison → **HTTP 409** con messaggio chiaro
(«Generate comparison before creating a migration plan»). La generazione della
comparison a sua volta richiede i due snapshot (già 409 nel modulo comparison).

La funzione pura del dominio riceve dati già estratti (nessun DB dentro):

```
build_migration_plan(source_inventory, destination_inventory, comparison)
    -> MigrationPlanOutput
```

`source_inventory`/`destination_inventory` = `snapshot.data` (liste normalizzate
+ `coverage` matrix; `capabilities` iniettate dal service come fa la comparison).
`comparison` = `{summary, entries}` del report. No cPanel, no API, no DB nella
funzione pura.

## Output del piano

```json
{
  "id": 1,
  "migration_id": 1,
  "status": "blocked | ready_for_review",
  "summary": {
    "blockers_count": 0,
    "warnings_count": 0,
    "manual_tasks_count": 0,
    "unknowns_count": 0,
    "ready_steps_count": 0
  },
  "sections": {
    "blockers": [],
    "manual_tasks": [],
    "warnings": [],
    "unknowns": [],
    "ready_steps": [],
    "cutover_notes": []
  },
  "generated_from": {
    "source_snapshot_id": 10,
    "destination_snapshot_id": 11,
    "comparison_report_id": 5
  }
}
```

Ogni item di sezione è descrittivo e non eseguibile:
`{category, key, title, message}` (+ `state`/`severity` dove derivano da una
entry comparison). Nessun item contiene comandi, azioni o segreti — solo la
`key` naturale (dominio/email/nome logico), mai l'item grezzo né token/password.

## Stati del piano

```
blockers_count > 0  → blocked
blockers_count == 0 → ready_for_review
```

Gli **unknown NON contribuiscono a `blockers_count`** e non bloccano
automaticamente, ma sono resi molto visibili (l'operatore deve verificarli).

## Regole di classificazione (routing, non re-invenzione)

Sorgente primaria = **entry della comparison** (`{category, key, state,
severity, title, message}`). Instradamento:

### Blockers
Entry comparison con `severity == blocker` **e** `category ∉ {capabilities,
coverage}`. Copre: domini/database/mysql_users/email mancanti su destination,
relazione mysql_user→database diversa (la comparison, dopo la normalizzazione
logica #98, marca già questi come blocker). **Non si inventano blocker**: se una
categoria non è leggibile diventa *unknown*, mai blocker.

### Manual tasks
- Entry comparison con `severity == warning` in categorie **non automatizzabili**
  `{dns_records, cron_jobs, ssl}` (diff leggibili che richiedono intervento
  manuale).
- Derivati dall'inventory per le categorie **non confrontate item-per-item**
  `{email_forwarders, email_autoresponders, ftp_accounts}`: se leggibili sul
  source (coverage succeeded/empty) e con almeno un item, **un** manual task per
  categoria («N presenti sul source; non confrontati e non automatizzati →
  ricreare/verificare a mano»).

### Warnings
Entry comparison con `severity ∈ {warning, info}` **non** già instradate a manual
task e **non** di categoria `{capabilities, coverage}` (es. dominio
`only_on_destination`, database `different`, ssl `only_on_destination`).
Advisory, non bloccanti.

### Unknowns
Entry comparison con `category ∈ {capabilities, coverage}`. Mappano su:
coverage `unavailable/failed/unsupported/unverified` e categorie **skipped**
perché `can_read_* == false` su un lato. Regola: *unknown ≠ blocker*; l'operatore
verifica a mano.

### Ready steps (solo descrittivi)
Per ogni categoria-lista con `match > 0` nel `summary.by_category` della
comparison: una riga «N <categoria> risultano allineati (source↔destination)».
Non sono azioni eseguibili. **Nota**: sono per-categoria e non per-item, perché
la comparison omette i match dalle entry (tiene solo i conteggi) — non ricalcolo
l'intersezione per non duplicare l'engine.

### Cutover notes (derivati)
- Sempre: «Questo piano è read-only. Non esegue modifiche sui server.»
- Se il source ha `dns_records` leggibili: «I record DNS vanno ripuntati al
  cutover; la scrittura DNS non è automatizzata da questo piano.»
- Se il source ha `ssl`: «I certificati SSL possono richiedere re-issue/verifica
  sulla destinazione dopo il cutover.»

## Decisione esplicita — conflitto `cron_jobs` nel brief

Il brief elenca «cron_jobs mancanti se leggibili su entrambi i lati» **sia** tra
i blocker **sia** tra i manual task (contraddizione). Risolto a favore della
**comparison come source of truth**: l'engine mergiato classifica cron
mancante/differente come **warning** (il cron è ricreabile al cutover, non rompe
il sito). Il piano lo instrada quindi a **manual task** (richiede revisione
manuale prima di un'eventuale futura automazione), **non** a blocker. Così vale
sempre «blocked ⟺ la comparison ha blocker» e non si generano falsi blocker.
Se in futuro si vorrà rendere il cron bloccante, andrà cambiata la severità
nell'engine di comparison, non re-inventata nel piano.

## Cosa NON viene eseguito

Nessuna migrazione reale, nessun write cPanel, nessuna modifica DNS, nessuna
creazione/modifica/eliminazione di database/utenti MySQL/email, nessun
rsync/SSH/IMAP/MySQL dump/restore, nessun apply/cutover/rollback, nessun worker
di migrazione, nessun bottone «Start/Apply/Execute/Run», nessuna auth API,
nessun salvataggio di token/password/Authorization/auth_ref/segreti.

## Persistenza

Nuova tabella `migration_plans` (Alembic `0007`, chaining su `0006`):
`id, migration_id (FK CASCADE), status, summary JSON, sections JSON,
generated_from JSON, error TEXT, created_at, updated_at`. Ultimo piano = record
con `id` massimo per la migration (come la comparison). JSON basta: nessuna
tabella di dettaglio in questa fase.

## API

```
POST /api/migrations/{migration_id}/plan   → genera/rigenera, 201; 409 se mancano input; 404 migration
GET  /api/migrations/{migration_id}/plan   → ultimo piano, 200; 404 se nessun piano
```

Logica nel service (DB) + funzione pura nel dominio. La route non contiene
logica di dominio.

## Limiti noti

- Ready steps per-categoria, non per-item (i match non sono nelle entry).
- Forwarders/autoresponders/ftp non sono confrontati item-per-item (Sprint 3.5
  li tiene solo in coverage): il piano li marca manual task se presenti sul
  source, senza sapere se allineati sul destination.
- Snapshot misti legacy/nuovi: il piano dipende dalla comparison, che gestisce
  già il fallback; il piano non aggiunge assunzioni.
- `generated_from` referenzia gli id degli snapshot/report al momento della
  generazione; rigenerando si crea un nuovo piano (l'ultimo vince).

## Test previsti

Dominio (`build_migration_plan`):
- blocker comparison → sezione blockers, status `blocked`
- nessun blocker → status `ready_for_review`
- coverage `unavailable` → unknown, non blocker
- capability `can_read_* false` → unknown, non blocker
- dns_records diff → manual task (mai azione automatica)
- cron_jobs mancante → manual task, **non** blocker (decisione documentata)
- mysql_user missing/different → blocker
- identità mysql logica già normalizzata (prefissi diversi) → nessun falso task
- forwarders/autoresponders/ftp presenti sul source → manual task
- unknown non aumenta `blockers_count`
- summary counts == lunghezze delle sezioni

API:
- POST senza inventory/comparison → 409
- GET prima della generazione → 404
- POST con dati validi → 201
- GET dopo generazione → ultimo piano
- rigenerazione → nuovo ultimo piano coerente
- response senza token/auth_ref/password/Authorization
