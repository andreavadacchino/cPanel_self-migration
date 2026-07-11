# Prompt per la prossima sessione di sviluppo

Continua lo sviluppo della piattaforma V2 in:

`/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration/migration-platform`

Lingua di comunicazione: italiano.

## Obiettivo generale

Completare una piattaforma cPanel→cPanel senza root/WHM usando credenziali e
token account-level. Deve inventariare senza omissioni, distinguere risorse
mancanti/differenti/non applicabili/non verificabili, pianificare operazioni
automatiche e manuali, conservare audit ed evidenze, richiedere intervento umano
per password o privilegi mancanti e scalare in seguito oltre 120 account.

Lavora esclusivamente sulla V2 FastAPI, SQLAlchemy/Alembic/PostgreSQL,
Dramatiq/Redis, adapter Python cPanel/SSH/IMAP e frontend React/Vite/TypeScript.
Il codice Go nella root può essere consultato come riferimento funzionale ma non
deve diventare il motore della V2.

## Sicurezza non negoziabile

- Non stampare token, password, ciphertext, chiave Fernet o `auth_secret`.
- Non effettuare scritture sui cPanel reali senza una nuova autorizzazione
  esplicita dell'utente. Le autorizzazioni precedenti coprono solo sviluppo e
  test mock-only.
- Tutti i writer devono restare `disabled` nello stack.
- La sorgente è rigorosamente read-only e la destinazione è l'unico target.
- Nessun delete/overwrite implicito e nessun trasferimento di chiavi SSL private.
- Una lettura fallita non deve diventare `empty`.
- Non dichiarare `verified` da una conferma operatore: servono evidenze fresche.
- Non trasformare un execution run dry-run in una scrittura.

La configurazione locale è in `.env`; `CREDENTIAL_ENCRYPTION_KEY` è già
configurata. Non mostrarne il valore.

## Stack locale

- UI: http://localhost:5173
- API: http://localhost:8000
- OpenAPI: http://localhost:8000/docs
- PostgreSQL host: localhost:55432
- Redis: localhost:6379

Docker è attivo. All'avvio l'API esegue `alembic upgrade head`. Alembic è a
`0007_writer_readiness`.

## Funzionalità disponibili

### Endpoint e credenziali

- CRUD sorgente/destinazione.
- Token diretti cifrati Fernet e supporto `env://VARIABILE`.
- Segreti esclusi dalle risposte.
- Test read-only tramite `Variables::get_user_information`.
- Risposte UAPI flat e wrapped supportate.

### Preflight asincrono e inventory

- FastAPI crea job, Dramatiq esegue, PostgreSQL è fonte di verità.
- Snapshot immutabili distinti per sorgente/destinazione.
- Stati coverage: `succeeded`, `empty`, `partial`, `unsupported`,
  `unavailable`, `failed`, `unverified`.
- Categorie: account, domini, email, database/utenti MySQL, DNS, SSL,
  forwarder, autoresponder, FTP, redirect, filtri, mailing list, PHP,
  PostgreSQL, subaccount e cron.
- DNS è per zona proprietaria (main/addon/alias, non ogni sottodominio), decodifica Base64 ed esclude SOA/NS/DCV/servizi cPanel.
- Autoresponder: `list_auto_responders` per dominio e
  `get_auto_responder` per indirizzo. Vengono raccolti corpo, from, subject,
  interval, is_html, charset, start e stop. Ogni elemento ha
  `_detail_status=succeeded|failed`; fallimenti di dettaglio producono `partial`.
- La matrice `mysql_grants` legge `Mysql::get_privileges_on_database` per ogni
  coppia utente/database e conserva privilegi e coverage separata.
- FTP marca quota/home per gli account migrabili; mailing list marca `private`
  verificato solo quando esplicitamente disponibile da UAPI o dal fallback
  read-only API 2 `Email::listlists`, altrimenti resta `partial`.

### Comparazione, checklist e planner

- Report persistenti legati agli snapshot esatti.
- Stati: `match`, `missing_on_destination`, `only_on_destination`,
  `different`, `unknown`.
- Checklist manuale storica; verifica solo dopo nuova comparazione.
- Planner con `automatic`, `approval`, `secret_required`, `manual`, `excluded`.
- Il fingerprint autoresponder include tutti i campi dettagliati.

### Esecutore dry-run

- Modelli `execution_runs` e `execution_events`.
- Selezione passi, preview redatta, password cifrate, audit persistente.
- Stati: `previewed`, `awaiting_confirmation`, `queued`, `running`,
  `succeeded`, `failed`, `cancelled`.
- Conferma forte con frase esatta, ID piano, snapshot/report correnti e nuovo
  test read-only destinazione.
- Blocchi per comparazioni/snapshot superati, password mancanti e dipendenze.
- Nessuna scrittura reale; il run dry-run registra `write_performed=false`.

### Writer readiness report read-only

- Tabella `writer_readiness_reports` legata a piano, comparazione e snapshot esatti.
- Stati `not_ready`, `needs_inventory`, `needs_contract_test`, `needs_operator_input`, `eligible_for_real_design`.
- API `POST/GET /api/migrations/{id}/writer-readiness`; POST richiede `plan_id` e rifiuta evidenze obsolete.
- Copertura delle nove categorie writer e di ogni passo, con blocker globali e gap specifici.
- UI non operativa, nessun dispatch e contenuti sensibili esclusi.
- Primo contract test read-only: `database_contract` in ogni snapshot combina
  `Mysql::get_restrictions`, quota `maximum_databases` e conteggio corrente;
  readiness promuove database solo con evidenza riuscita su entrambi i lati.
- `mysql_grant_contract` verifica completezza delle coppie e insieme dei
  privilegi; database e utenti MySQL sono oggi `eligible_for_real_design`.

### Writer mock-only già preparati

Sono presenti actor e servizi per:

1. domini;
2. database MySQL;
3. utenti MySQL + grant;
4. forwarder;
5. cron con approvazione;
6. FTP con password cifrata;
7. mailing list con password cifrata;
8. DNS additivo con approvazione.
9. autoresponder additivo con protezione anti-upsert e audit redatto.

Ogni writer accetta solo endpoint `auth_type=mock`, run non dry-run creati dai
test e relativo flag impostato temporaneamente a `mock`. Endpoint reali, flag
`real`, normali dry-run e target non-destinazione vengono rifiutati. Non esiste
alcuna route o UI che accodi questi writer.

Flag, tutti `disabled` in `.env.example` e Docker:

- `DOMAIN_WRITER_MODE`
- `DATABASE_WRITER_MODE`
- `MYSQL_USER_WRITER_MODE`
- `FORWARDER_WRITER_MODE`
- `CRON_WRITER_MODE`
- `FTP_WRITER_MODE`
- `MAILING_LIST_WRITER_MODE`
- `DNS_WRITER_MODE`
- `AUTORESPONDER_WRITER_MODE`

## Stato pilota da rileggere prima di usarlo

Ultima fotografia nota:

- migrazione `1`;
- job preflight `20`, succeeded;
- snapshot `39/40`;
- comparazione `19`;
- piano `13`;
- readiness report `11`;
- MySQL grants sorgente: 3 associazioni, 6/6 coppie verificate;
- FTP writer metadata completo; mailing list sorgente `succeeded`, `private`
  ricavato da campi privacy espliciti con provenienza registrata;
- DNS sorgente/destinazione `succeeded`; i sottodomini non vengono più trattati come zone autonome;
- readiness categorie operative: `needs_inventory=0`, `needs_contract_test=0`,
  `eligible_for_real_design=7`;
- database è `eligible_for_real_design`: contract read-only riuscito su entrambi
  i lati; sorgente quota 6/2 usati, destinazione quota illimitata/1 usato.
- utenti MySQL sono `eligible_for_real_design`: 6/6 coppie sorgente e 1/1
  destinazione verificate, nessun privilegio fuori contratto.
- FTP e mailing list hanno ora contract evidence read-only esplicite
  (`ftp_contract`, `mailing_list_contract`); la fotografia pilota deve essere
  rigenerata prima di attribuire loro uno stato aggiornato.
- Forwarder e autoresponder hanno ora evidenze read-only esplicite
  (`forwarder_contract`, `autoresponder_contract`) per il futuro fresh check;
  l'evidenza autoresponder esclude body, subject e from.
- DNS ha ora `dns_contract`: zone proprietarie attese, collision keys, tipi non
  supportati e strategia fresh-read. I passi conservano `comparison_state` e
  solo `missing_on_destination` non ambiguo può restare candidato additivo.
- 3 autoresponder sorgente con dettaglio riuscito, 0 destinazione;
- i 3 autoresponder sono `missing_on_destination` e ancora `manual`;
- execution run non dry-run: 0;
- tutti i writer disabilitati.

Non fare affidamento cieco su questi ID: rileggi sempre le API perché possono
essere stati creati nuovi job/report/piani.

## Writer autoresponder appena completato

Il writer mock-only è in
`apps/api/app/modules/executions/autoresponder_writer.py`, con actor
`worker.actors.autoresponder_writer`. Usa
`AUTORESPONDER_WRITER_MODE=disabled`, accetta soltanto
`missing_on_destination`, legge il payload completo dallo snapshot sorgente e
protegge dall'upsert tramite fresh mock pre-write check. Corpo, subject e from
sono redatti; l'audit conserva fingerprint e metadati non sensibili. Il target
live mock per i test di race usa `email_autoresponders_live` nello snapshot
destinazione. Nessuna route/UI lo accoda.

## Incremento completato: orchestrazione mock end-to-end

L'orchestrazione descritta sotto è stata implementata in
`apps/api/app/modules/executions/mock_orchestrator.py`, con actor
`worker.actors.mock_orchestrator`. Resta documentata come contratto verificato:
non deve essere reinterpretata come lavoro ancora da svolgere.

Una review adversariale Python+security ha verificato redazione, isolamento del
target, blocco dry-run→scrittura, flag, failure propagation e verifica finale
dagli eventi immutabili. Sono stati chiusi i finding su flag `real`, ramo
`ConflictError` downstream, ordine dei guard, categoria preview, guard simmetrico
di `execute_dry_run` e limite del fresh-read autoresponder reale.

### Contratto storico readiness (già completato)

I requisiti seguenti descrivono il report già implementato e devono restare
come riferimento di regressione, non come backlog ancora aperto.

Un singolo execution run mock non dry-run deve poter coordinare i passi
selezionati nell'ordine corretto, fermarsi in sicurezza al primo blocco,
conservare audit per ogni fase e produrre una verifica finale coerente. Non deve
essere necessario manipolare manualmente `run.status` tra un writer e l'altro.

### Requisiti funzionali

1. Aggiungere un orchestratore esplicito, preferibilmente
   `app.modules.executions.mock_orchestrator`, e un actor Dramatiq registrato.
2. Introdurre `MOCK_ORCHESTRATOR_MODE=disabled` in settings, Compose e
   `.env.example`. Solo i test possono impostarlo a `mock`; `real` deve essere
   rifiutato.
3. Nessuna route o azione UI deve accodare l'orchestratore in questo incremento.
4. Accettare soltanto execution run:
   - `dry_run=false`;
   - `status=queued`;
   - endpoint `role=destination`, `auth_type=mock`;
   - piano, comparazione e snapshot coerenti con il run;
   - conferma forte presente quando esistono passi `approval`.
5. Validare prima dell'avvio tutti i passi selezionati, le password cifrate e le
   dipendenze. Un errore di pre-validazione non deve eseguire alcuna fase.
6. Ordinare deterministicamente le categorie:
   1. `domains`;
   2. `databases`;
   3. `mysql_users`;
   4. `email_forwarders`;
   5. `cron_jobs`;
   6. `ftp_accounts`;
   7. `mailing_lists`;
   8. `dns_records`;
   9. `email_autoresponders`.
7. Rifiutare passi `manual`, `excluded`, categorie sconosciute e dipendenze non
   selezionate/verificate.
8. Non aggirare i guardrail dei writer. L'orchestratore deve invocare un
   contratto di fase condiviso o refactorizzato, non forzare ripetutamente
   `run.status=queued` per adattarsi agli `execute()` attuali.
9. Refactorizzare il lifecycle dei writer con la patch più piccola possibile:
   una fase non deve marcare l'intero run `succeeded` mentre restano fasi da
   eseguire. Lo stato terminale appartiene all'orchestratore.
10. Registrare eventi almeno per:
    - validazione;
    - ordine calcolato;
    - inizio/fine di ogni categoria;
    - passo eseguito, già presente o già completato;
    - blocco/fallimento e categorie downstream non eseguite;
    - verifica finale.
11. Se una fase fallisce o richiede intervento manuale:
    - fermare immediatamente le categorie successive;
    - portare il run a `failed`;
    - non cancellare o compensare automaticamente risorse già simulate;
    - registrare quali passi sono riusciti e quali non sono stati eseguiti.
12. Retry dell'orchestratore: riprendere dai checkpoint verificati senza ripetere
    passi già completati. L'ordine deve restare identico.
13. Nessun contenuto sensibile negli eventi aggregati. Non copiare password,
    ciphertext, body/subject/from autoresponder o token nei messaggi.

### Stato mock e verifica finale

I writer correnti mantengono parte dello stato simulato in memoria e usano
eventi come checkpoint. Definire un contratto coerente per l'intera esecuzione:

- preferire un piccolo `MockDestinationState` condiviso fra le fasi oppure una
  rappresentazione persistente esplicita;
- non mutare gli snapshot originali;
- se serve persistenza aggiuntiva, creare una migrazione Alembic nuova e
  documentarla; non infilare stato opaco in campi non destinati allo scopo;
- al termine costruire una nuova evidenza mock immutabile o un report di verifica
  persistente che dimostri l'esito dei passi;
- non dichiarare `verified` basandosi soltanto sul risultato restituito dal
  writer; la verifica deve rileggere lo stato mock condiviso;
- documentare chiaramente che il futuro percorso reale sostituirà questa fase
  con nuovo preflight e comparazione della destinazione.

### Casi di test minimi

1. Sequenza completa nell'ordine previsto.
2. Selezione parziale senza categorie non necessarie.
3. Dipendenza database→utente MySQL.
4. Dipendenza dominio→DNS.
5. Passi approval senza conferma rifiutati prima dell'avvio.
6. Password mancante rifiutata prima dell'avvio.
7. Fallimento a metà: downstream non eseguito, upstream preservato nell'audit.
8. Retry: checkpoint già verificati saltati.
9. Passo manual/excluded o categoria sconosciuta rifiutato.
10. Autoresponder race anti-upsert propagata come blocco dell'intero run.
11. Nessun segreto o contenuto autoresponder sensibile negli eventi aggregati.
12. Flag disabled, `real`, dry-run, endpoint reale e target sorgente rifiutati.
13. Actor Dramatiq registrato e importato da `worker.main`.
14. Verifica finale riletta dallo stato mock condiviso.

### Limiti dell'incremento completato

- Non implementare writer reali.
- Non aggiungere dispatch API/UI.
- Non modificare `.env` per abilitare modalità mock.
- Non eseguire scritture sui cPanel pilota.
- Non introdurre rollback distruttivi; registrare invece una futura attività
  manuale/compensazione.

## Incremento completato: writer readiness report read-only

Il readiness report descritto sotto è stato implementato. Resta documentato
come contratto verificato e non deve essere reinterpretato come lavoro ancora
da svolgere. Nessun flag writer è stato abilitato e non esiste dispatch reale.

## Prossimo incremento richiesto

I contract test read-only previsti (FTP, mailing list, forwarder,
autoresponder e DNS) sono completati e persistiti negli snapshot. Il prossimo
incremento deve consolidare il design del percorso reale senza implementare
writer o dispatch finché manca una nuova autorizzazione esplicita. Se servono
nuove evidenze, persistirle negli snapshot o in un
modello esplicito, escluderle dai passi operativi e aggiornare readiness. Non
implementare writer reali né dispatch operativo senza nuova autorizzazione.

### Obiettivo

Per ogni categoria e passo dell'ultimo piano, spiegare in modo verificabile se
il passaggio da mock a reale è `not_ready`, `needs_inventory`,
`needs_contract_test`, `needs_operator_input` o `eligible_for_real_design`.
Il report non abilita scritture: rende espliciti i gap prima di progettare un
execution run reale.

### Requisiti

1. Legare il report a piano, comparazione e snapshot esatti; evidenze obsolete
   devono essere rifiutate come nell'esecutore dry-run.
2. Coprire tutte le categorie writer e distinguere blocker globali da blocker
   del singolo passo.
3. Calcolare almeno questi gap:
   - domini: contratto ufficiale writer e recovery manuale/rollback;
   - database: contract test account-level e limite quota;
   - utenti MySQL: mappatura utente→database→privilegi sorgente mancante;
   - forwarder: fresh read reale pre-write;
   - cron: writer API 2, approval e rollback;
   - FTP: quota e home directory mancanti;
   - mailing list: `private` eventualmente non verificato;
   - DNS: collisioni, record differenti e verifica zona;
   - autoresponder: fresh UAPI reale anti-upsert.
4. Non leggere o restituire password, token, ciphertext, body/subject/from
   autoresponder o altri contenuti sensibili. Usare solo metadati e stati.
5. Aggiungere API read-only per generare/leggere il report solo se il modello
   dati è chiaro; altrimenti iniziare con engine/service deterministico e test.
6. Aggiungere UI non operativa per readiness e motivi, senza pulsanti reali.
7. Testare staleness, categorie non leggibili, password/approval/dipendenze e
   redazione dei contenuti sensibili.
8. Non impostare alcun flag a `mock` o `real` in `.env` e non aggiungere route di
   dispatch per writer o orchestratore.
9. Aggiornare README e questo prompt con stati, schema e API introdotti.

## Metodo di lavoro

- Ispezionare il codice esistente prima di modificare.
- Riutilizzare il pattern dei writer mock, senza duplicare contratti inutilmente.
- Applicare modifiche incrementali con test vicini al comportamento.
- Aggiornare sempre `README.md`.
- Non cancellare o sovrascrivere lavoro presente; `migration-platform/` può
  risultare interamente non tracciata nella repository principale.

## Validazione minima obbligatoria

```bash
cd /Users/andreavadacchino/Desktop/pADV/cPanel_self-migration/migration-platform/apps/api
PYTHONPATH=../../packages/adapters python -m pytest

cd ../web
npm run build

cd ../../
docker compose config -q
docker compose up -d --build
curl --fail http://localhost:8000/health
docker compose exec -T api alembic current
docker compose exec -T api sh -lc \
  'cd /srv/apps/worker && DRAMATIQ_TESTING=1 python -m pytest worker/tests/test_actors.py'
```

Baseline nota: 117 test API, 15 test worker, frontend build verde, Alembic
`0007_writer_readiness`, stack Docker operativo.

Al termine riferire con precisione file modificati, test eseguiti, limitazioni
rimaste e confermare che non è stata effettuata alcuna scrittura cPanel reale.
