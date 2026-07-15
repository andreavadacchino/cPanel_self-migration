# Migration Platform V2 — Execution Roadmap

- **Stato:** proposta operativa
- **Data:** 2026-07-15
- **Base:** `main` dopo `#111`
- **Architettura vincolante:** [`../../docs/ADR_V2_GO_EXECUTOR.md`](../../docs/ADR_V2_GO_EXECUTOR.md)
- **Stato corrente:** [`CURRENT_STATE.md`](CURRENT_STATE.md)

## 1. Scopo

Questa roadmap definisce il percorso necessario per portare Migration Platform V2 da control plane
read-only a piattaforma capace di governare una migrazione reale, osservabile e recuperabile.

La roadmap non misura il progresso in base al numero di modelli, endpoint o componenti implementati.
Ogni milestone termina con una capability operativa verificabile end-to-end.

Il percorso è deliberatamente incrementale:

1. dry-run governato e realmente eseguito;
2. recovery deterministica dopo crash e riavvio;
3. prima write su una sola mailbox sacrificabile;
4. estensione separata a file e database;
5. readiness per un account cliente mono-account;
6. hardening multi-operatore e Campaign Mode.

## 2. Non-obiettivi immediati

Restano fuori dalla fase di abilitazione del primo dry-run e del primo apply:

- Campaign Mode;
- esposizione della piattaforma fuori da localhost;
- riscrittura dei writer Go in Python;
- seconda control plane nella WebUI Go;
- DNS automatico;
- promessa di migrazione integrale di ogni configurazione cPanel;
- retry automatici dopo l'inizio di una fase potenzialmente mutante;
- apply sull'intero account come primo test reale.

## 3. Definizione dei livelli di readiness

### R0 — Read-only control plane

La piattaforma acquisisce inventari, produce comparison e genera un piano. Nessun executor viene
avviato.

### R1 — Governed dry-run

Una execution viene creata, accodata, eseguita dal binario Go con credenziali materializzate a runtime,
monitorata tramite eventi versionati e terminalizzata in PostgreSQL.

Il dry-run deve usare connessioni SSH reali verso entrambi gli endpoint, ma non deve effettuare write sui
server.

### R2 — Recoverable dry-run

Una execution sopravvive a restart di API, Redis e worker. Un crash del worker non lascia record
indefinitamente `running`, non perde gli artifact necessari all'audit e non permette retry ambigui.

### R3 — Sacrificial write

La piattaforma esegue un apply limitato a una sola mailbox sacrificabile, verifica il risultato e conserva
prove sufficienti per classificare l'esito come `succeeded`, `failed`, `partial`, `cancelled` o
`interrupted`.

### R4 — Customer mono-account ready

Mail, file e database supportati vengono trattati come capability distinte, ognuna con gate, smoke,
recovery e limiti documentati. Le attività manuali restano visibili e non sono rappresentate come
completate automaticamente.

### R5 — Multi-operator / Campaign ready

La piattaforma possiede autenticazione, autorizzazione, protezione SSRF, audit multi-operatore, limiti di
concorrenza, capacity planning e recupero provato su più esecuzioni concorrenti.

## 4. Invarianti architetturali

Questi vincoli non possono essere aggirati per accelerare una milestone:

1. Il binario Go è l'unico componente che legge o scrive dati applicativi sui server cPanel.
2. PostgreSQL è la fonte di verità durevole; Redis è trasporto, non stato.
3. I segreti non entrano nello spec persistito, negli eventi, nei report o nei log.
4. Le credenziali vengono risolte e materializzate dal worker solo a runtime.
5. Il source resta read-only.
6. DNS resta fuori dall'auto-run.
7. Una sola execution mutante per migration può essere attiva.
8. La compatibilità fra Platform, contratto ed executor viene verificata prima del subprocess.
9. La freschezza del piano viene ricontrollata immediatamente prima dell'avvio.
10. Nessun retry automatico è consentito dopo la prima operazione potenzialmente mutante.
11. Un record `running` deve corrispondere a un owner vivo o essere riconciliabile automaticamente.
12. Gli artifact necessari a determinare l'esito devono sopravvivere al processo che li ha prodotti.
13. La UI riflette lo stato server-side; non costituisce un gate di sicurezza.
14. La capability `apply` resta invisibile finché i gate reali non sono stati superati.

## 5. Falle strutturali da chiudere

### 5.1 Ownership e liveness del subprocess

Il modello deve poter distinguere:

- execution accodata ma non ancora presa;
- execution posseduta da un worker vivo;
- execution abbandonata dopo crash;
- executor terminato ma risultato non ancora ingerito;
- execution con write già iniziate e risultato incompleto.

Servono almeno:

- numero di attempt immutabile;
- worker identity;
- lease o heartbeat con timestamp;
- timestamp di ultimo evento ingerito;
- fase corrente e indicazione durevole `writes_started`;
- procedura di startup reconciliation.

### 5.2 Durabilità degli artifact

`events.jsonl`, `report.json`, spec canonico e manifest non possono dipendere esclusivamente dal
filesystem effimero del container.

La prima implementazione può usare un volume persistente locale, purché definisca:

- directory per execution e attempt;
- permessi privati;
- scrittura atomica dei file terminali;
- digest SHA-256, dimensione e tipo nel manifest;
- stato di ingestione;
- retention;
- garbage collection solo dopo terminalizzazione e verifica del manifest.

L'architettura deve permettere in futuro di sostituire il volume con object storage senza cambiare il
contratto di execution.

### 5.3 Retry, resume e nuova execution

I tre concetti non sono equivalenti:

- **retry tecnico:** nuovo tentativo della stessa execution prima di qualsiasi write;
- **resume:** continuazione esplicita da un checkpoint supportato dall'executor;
- **rerun:** nuova execution ancorata a nuovi gate e, se necessario, nuovi snapshot.

Finché l'executor non implementa checkpoint verificabili, `resume` non esiste.

Dopo `writes_started=true`:

- il broker non effettua retry automatici;
- il worker terminalizza o riconcilia l'esecuzione;
- un nuovo tentativo richiede decisione esplicita dell'operatore;
- la destinazione deve essere nuovamente inventariata prima di un rerun.

### 5.4 Credenziali MySQL indipendenti da SSH

La migrazione database non può dipendere dal fatto che il source SSH utilizzi una password. La
credenziale MySQL source deve diventare una capability distinta dall'autenticazione SSH.

Finché questo gap non è chiuso:

- un source key-only non è DB-apply-ready;
- la UI deve mostrare database come non eseguibili, non come errore generico;
- mail e file possono avanzare separatamente se i loro gate sono verdi.

### 5.5 Contratto degli stati

Gli stati esistenti devono avere transizioni, ownership e prove formali.

| Stato | Significato | Owner della transizione in uscita |
|---|---|---|
| `pending` | record creato, non dispatchato | API/service |
| `queued` | messaggio pubblicato, nessun lease acquisito | worker |
| `running` | lease attivo e subprocess avviato o in avvio | worker |
| `cancel_requested` | richiesta durevole di stop | worker |
| `succeeded` | report valido, nessun errore terminale | worker/ingestor |
| `failed` | nessuna write effettuata oppure fallimento classificato non parziale | worker/reconciler |
| `partial` | almeno una write confermata e completamento non raggiunto | worker/reconciler |
| `cancelled` | stop confermato prima di qualsiasi write | worker/reconciler |
| `interrupted` | processo terminato senza risultato completo e senza write confermate | reconciler |

Regole minime:

- ogni transizione usa compare-and-set o lock equivalente;
- `finished_at` è scritto insieme allo stato terminale;
- uno stato terminale è immutabile;
- `partial` prevale su `failed`, `cancelled` e `interrupted` quando `writes_started=true`;
- l'assenza di report non implica automaticamente assenza di write;
- l'ultimo evento da solo non è prova sufficiente: serve un indicatore durevole o un evento di fase con
  semantica contrattuale.

## 6. Sequenza delle milestone

## M0 — Contratti operativi e CI

### Obiettivo

Rendere verificabili gli invarianti prima di aggiungere il subprocess.

### Deliverable

- questa roadmap approvata;
- ADR o decision record dedicato a lifecycle, retry, recovery e artifact durability;
- matrice formale delle transizioni;
- test cross-language dei contratti in CI;
- CI per Go, API, domain, worker, migration Alembic e build frontend;
- verifica che i test importino il codice del worktree corretto;
- policy di compatibilità Platform/executor.

### Definition of done

- una PR non può essere mergiata se rompe contratto Go/Python o migration DB;
- la semantica di crash, cancel e retry non è lasciata all'implementazione del singolo actor;
- nessun deploy non-locale viene autorizzato.

## M1 — SSH runtime e workspace privata

### Dipendenza

La persistenza delle credenziali SSH degli endpoint deve essere disponibile.

### Deliverable

- persistenza host identity associata a host e porta;
- invalidazione esplicita del pin quando cambiano coordinate endpoint;
- resolver fail-closed per `direct` e `ref`;
- validazione completa della riga SSH prima del decrypt;
- materializzazione di chiave, passphrase, password e `known_hosts` con permessi minimi;
- generazione runtime di `host.yaml` senza segreti negli artifact persistiti;
- cleanup deterministico della workspace segreta;
- test che provano assenza di materiale segreto in response, log, eventi e manifest.

### Definition of done

Il worker può costruire una workspace completa per entrambi gli endpoint e distruggerne il materiale
segreto senza ancora avviare il binario.

## M2 — Packaging e compatibilità executor

### Deliverable

- worker image multi-stage o artifact immutabile contenente il binario Go;
- executor identificato tramite digest e build version;
- comando di capability/version handshake;
- allowlist esplicita delle versioni di contratto supportate;
- avvio rifiutato prima del subprocess in caso di incompatibilità;
- SBOM o almeno provenance riproducibile dell'executor incluso nell'immagine.

### Definition of done

Ogni execution registra esattamente quale executor sarebbe stato invocato e fallisce prima della
connessione SSH se la versione non è compatibile.

## M3 — Vertical slice dry-run end-to-end

### Deliverable

- dispatch del solo `execution_id`;
- transizioni `pending -> queued -> running` atomiche;
- actor che ricarica tutto da PostgreSQL;
- ricontrollo freshness immediatamente pre-start;
- generazione dello spec canonico nella workspace;
- subprocess in process group dedicato;
- ingestione incrementale di `execution-event-v1`;
- validazione e ingestione di `execution-result-v1`;
- manifest degli artifact con digest;
- terminalizzazione atomica;
- endpoint read per execution e timeline;
- UI con sola azione `Avvia dry-run` e stato reale.

### Definition of done

Da UI o API una execution dry-run raggiunge uno stato terminale dopo connessioni SSH reali a source e
destination. Riavviare l'API non perde stato o progresso persistito.

## M4 — Recovery, cancel e durabilità

### Deliverable

- lease/heartbeat;
- attempt table o modello equivalente immutabile;
- startup reconciliation;
- rilevamento di execution orfane;
- riconciliazione di `report.json` prodotto ma non ingerito;
- cancel durevole e terminazione del process group;
- escalation `SIGTERM -> timeout -> SIGKILL` o equivalente portabile;
- classificazione deterministica di `partial`;
- volume persistente o artifact store;
- retention e garbage collection;
- test kill/restart durante setup, connessione, ingestione e terminalizzazione.

### Definition of done

Spegnere brutalmente il worker non lascia execution indefinitamente `running`, non perde le prove
dell'esito e non causa un retry automatico ambiguo.

Il raggiungimento di M4 porta la piattaforma a **R2**.

## M5 — Apply su singola mailbox sacrificabile

### Prerequisiti

- account e mailbox dedicati;
- accesso SSH su entrambi gli endpoint;
- destinazione inizialmente vuota;
- password storica nota;
- cleanup autorizzato e verificato;
- M4 completata.

### Deliverable

- nuovo contratto versionato per modalità mutante;
- scope applicativo limitato a una sola mailbox;
- strong confirmation server-side;
- gate di freshness immediatamente pre-start;
- `writes_started` persistito con semantica contrattuale;
- nessun retry automatico dopo l'inizio write;
- verifica IMAP/autenticazione con password storica;
- report e cleanup dello smoke;
- capability apply ancora nascosta per scope più ampi.

### Definition of done

Lo smoke reale passa integralmente e produce evidenza ripetibile di:

- mailbox creata correttamente;
- autenticazione con password storica funzionante;
- nessun segreto negli artifact;
- stato terminale corretto;
- cleanup riuscito;
- comportamento noto in almeno un test di interruzione controllata.

Il raggiungimento di M5 porta la piattaforma a **R3**.

## M6 — File come capability mutante separata

### Deliverable

- scope ristretto a un singolo dominio/docroot sacrificabile;
- preflight spazio e permessi;
- checksum/verifica post-copy;
- comportamento definito su file già presenti;
- distinzione copy/mirror;
- backup e recovery coerenti con la modalità;
- smoke e test di interruzione dedicati.

### Definition of done

La capability file può essere abilitata senza implicare che mail o database siano eseguibili.

## M7 — Database come capability mutante separata

### Deliverable

- modello credenziale MySQL indipendente da SSH;
- resolver e redazione dedicati;
- preflight versioni, privilegi, spazio e collisioni;
- dump/import con artifact e checksum;
- classificazione di partial su dump completato/import fallito;
- verifica applicativa o strutturale documentata;
- smoke su database sacrificabile.

### Definition of done

Un source autenticato via chiave SSH può migrare database senza introdurre una password SSH artificiale.

## M8 — Customer mono-account readiness

### Deliverable

- checklist di cutover;
- task manuali DNS, SSL, cron, FTP, forwarder, filter e categorie non automatizzate;
- report finale che distingue capability automatiche da task manuali;
- runbook per interrupted/partial;
- procedura di forward recovery e, dove realmente supportato, rollback;
- retention e gestione artifact operative;
- test completo su un account non critico autorizzato.

### Definition of done

Un operatore può determinare senza ambiguità:

- cosa verrà migrato automaticamente;
- cosa resterà manuale;
- quale stato ha ogni capability;
- come reagire a failure o partial;
- quali prove rendono il cutover accettabile.

Il raggiungimento di M8 porta la piattaforma a **R4**.

## M9 — Hardening e Campaign Mode

Questa milestone inizia solo dopo R4.

### Deliverable

- autenticazione e autorizzazione;
- allowlist endpoint e protezione SSRF;
- rate limiting;
- audit multi-operatore;
- segregazione dei segreti;
- limiti di concorrenza globali e per server;
- backpressure;
- capacity planning;
- dashboard campagna;
- recovery provata con execution concorrenti.

## 7. Sequenza raccomandata delle PR

Le PR devono restare piccole e mergeabili, ma ogni gruppo termina con una vertical slice.

### Gruppo A — Foundation

1. **SSH auth persistence** — PR esistente.
2. **Host identity persistence** — nessun runtime.
3. **Lifecycle/recovery ADR + schema attempts/lease/artifacts**.
4. **CI obbligatoria cross-language e migration**.

### Gruppo B — Runtime dry-run

5. **SSH runtime resolver + workspace builder** — nessun subprocess.
6. **Executor packaging + compatibility handshake**.
7. **Dry-run actor + event/result ingestion**.
8. **Execution read API + monitor UI**.

### Gruppo C — Recovery

9. **Heartbeat/lease + orphan reconciliation**.
10. **Persistent artifact store + retention**.
11. **Cancel/process group + deterministic terminalization**.
12. **Crash matrix integration tests**.

### Gruppo D — First write

13. **Apply contract v2 + mailbox-only gates**.
14. **Sacrificial mailbox smoke harness**.
15. **Controlled live smoke and evidence report**.

### Gruppo E — Capability expansion

16. **Files apply capability**.
17. **Independent MySQL credentials**.
18. **Database apply capability**.
19. **Customer cutover/report/runbook**.

## 8. Matrice minima dei test di crash

| Punto di arresto | Write possibili | Esito atteso dopo reconciliation | Retry automatico |
|---|---:|---|---:|
| prima del lease | no | `queued` nuovamente consumabile | sì |
| dopo lease, prima del subprocess | no | `interrupted` o retry tecnico controllato | limitato |
| subprocess avviato, nessun evento di write | no | `interrupted` | no implicito |
| dopo `writes_started` | sì | `partial` salvo prova terminale contraria | mai |
| report scritto, non ingerito | dipende | ingestione e terminalizzazione dal report | no |
| stato terminale scritto, cleanup incompleto | già noto | stato invariato, cleanup ripetibile | non applicabile |

Questa matrice deve diventare una suite di integrazione prima di esporre apply.

## 9. Gate per rendere visibile Apply

Il pulsante o endpoint mutante non viene reso disponibile finché non sono vere tutte le condizioni:

- M4 completata;
- executor e contratto compatibili;
- artifact durevoli;
- recovery provata tramite kill test;
- account sacrificabile autorizzato;
- scope limitato a una mailbox;
- freshness verificata immediatamente pre-start;
- strong confirmation;
- nessun blocker nel piano;
- procedura di cleanup disponibile;
- runbook `partial` e `interrupted` approvato.

## 10. Criterio di completamento della piattaforma V2 mono-account

La piattaforma non è considerata completata solo perché può avviare il binario.

È completata per il primo uso mono-account quando:

1. ogni execution possiede un audit trail durevole;
2. ogni stato terminale è derivato da prove verificabili;
3. un crash non produce un writer fantasma o un retry ambiguo;
4. mail, file e database hanno capability e gate indipendenti;
5. i limiti del source key-only per MySQL sono stati rimossi oppure esposti come blocco esplicito;
6. le attività manuali non vengono conteggiate come completate;
7. esiste un runbook operativo per success, failed, partial, cancelled e interrupted;
8. il cutover su account non critico è stato eseguito e documentato.

## 11. Prossimo incremento vincolante

Dopo il merge della persistenza SSH, il prossimo obiettivo non è ancora l'actor.

Il prossimo incremento deve chiudere insieme:

- host identity persistence;
- decisione lifecycle/recovery;
- modello attempts/lease/artifact durability;
- CI obbligatoria per i contratti.

Solo dopo questi vincoli si implementano resolver, workspace e subprocess. Questo evita di costruire un
worker apparentemente funzionante che diventa ambiguo al primo crash.