# ADR-002 — Ownership dell'esecuzione, lease e recovery dopo crash

- **Stato:** Accettata
- **Data:** 2026-07-15
- **Contesto di codice:** `fork/main` = `b5c9d36` (dopo #112, #113)
- **Supersede:** nessuna. Estende [ADR-001](ADR_V2_GO_EXECUTOR.md).
- **Vedi anche:** [`../migration-platform/docs/EXECUTION_ROADMAP.md`](../migration-platform/docs/EXECUTION_ROADMAP.md) (§5.1, §5.5, §8),
  [`../migration-platform/docs/CURRENT_STATE.md`](../migration-platform/docs/CURRENT_STATE.md)

---

## Contesto

ADR-001 stabilisce che il motore Go è l'unico executor e PostgreSQL la fonte di verità. La
roadmap (§5.1) elenca la prima falla strutturale da chiudere prima del subprocess: il modello non
sa distinguere un'esecuzione **posseduta da un worker vivo** da una **abbandonata dopo un crash**.
Senza questa distinzione, il primo crash del worker produce un record `running` che non corrisponde
a nulla, e un retry automatico lo rilancerebbe su una destinazione di cui non si conosce lo stato.

Questa PR modella ownership, lease e recovery **senza** avviare alcun subprocess. Costruisce le
primitive che il worker futuro consumerà, e definisce chi possiede un'esecuzione e come la
piattaforma la recupera quando quel proprietario scompare.

La domanda a cui l'ADR risponde: *chi possiede un'esecuzione, e cosa diventa il suo attempt quando
il proprietario scompare?*

## Decisione

### Un job Dramatiq non è un'esecuzione governata

Un messaggio Dramatiq è un'istruzione effimera: "qualcuno esegua questo". Non ha identità durevole,
non sa dire se un worker lo sta ancora eseguendo, e il broker può ritentarlo silenziosamente. Una
migrazione reale non tollera nessuna di queste proprietà: deve avere un record che sopravvive al
processo, che nomina il suo proprietario, e su cui **nessun retry è implicito**. L'esecuzione vive
quindi in PostgreSQL (`migration_executions`), e la sua ownership in una tabella dedicata
(`execution_attempts`), non nel job.

### Redis non è la fonte di verità

Redis trasporta il messaggio; non ricostruisce l'esecuzione. Se Redis si svuota, il recovery deve
restare possibile leggendo solo PostgreSQL. Perciò nessuno stato necessario al recupero — attempt,
lease, `writes_started` — vive nella coda. Un restart di Redis non perde nulla di ciò che serve a
classificare un'esecuzione orfana.

### Execution contro attempt

- Una **execution** (`migration_executions`) è la richiesta durevole: quale piano, quali snapshot,
  quale scope l'operatore ha approvato. Una sola execution mutante per migrazione può essere attiva
  (già garantito da `uq_migration_executions_active_mutating`).
- Un **attempt** (`execution_attempts`) è **un** tentativo di *un* worker di eseguire quella
  execution. Ha identità immutabile: un nuovo tentativo è una nuova riga con `attempt_number`
  successivo; un attempt precedente non viene mai riusato o sovrascritto, così lo storico resta
  investigabile. Un solo attempt attivo per execution, garantito dall'indice unique parziale
  `uq_execution_one_active_attempt` (non da un controllo di servizio: due worker che leggono "nessun
  attempt attivo" nello stesso istante devono produrre un solo vincitore, e solo il database lo
  garantisce).

### Ownership del worker

Ogni attempt porta un `worker_id`. Ogni mutazione — rinnovo del lease, `writes_started`,
terminalizzazione — verifica che il chiamante sia il proprietario. Un worker estraneo non può
rinnovare, terminare o modificare l'attempt di un altro: riceve `OwnershipMismatch`. Il messaggio
d'errore non riporta mai il token presentato.

### Heartbeat, lease e tempo PostgreSQL

Un attempt possiede un **lease**: `lease_expires_at`. Il worker lo rinnova con un heartbeat
(`renew_attempt_lease`) finché è vivo. Un lease scaduto significa: il proprietario non ha dato segni
di vita entro la finestra, ed è presunto perso.

**Il lease usa esclusivamente il tempo del database, mai l'orologio del processo.** Ogni decisione
che dipende da "il lease è ancora valido?" legge un singolo `clock_timestamp()` *dopo* aver preso il
lock di riga, e riusa quell'unico valore per tutta la transizione (acquisizione, scadenza,
`finished_at`). La scelta fra le tre funzioni non è indifferente:

- `now()` / `transaction_timestamp()` si congelano all'inizio della transazione: una transazione
  rimasta aperta più a lungo del previsto leggerebbe un tempo passato e mentirebbe sulla scadenza;
- `clock_timestamp()` è l'orologio a muro al momento dello statement, che è ciò che un lease deve
  riflettere.

L'orologio del worker Python non è mai autoritativo: una pausa GC del worker non gli fa perdere il
lease se il tempo *del database* non è ancora scaduto, e un worker con clock sfasato non può rubarne
uno valido.

SQLite (solo dev/test mono-connessione) usa `now()`: non prova concorrenza e non pretende di farlo.
Le proprietà che richiedono due connessioni in gara sono provate solo su PostgreSQL reale.

### Cosa il lease fencia — e cosa no

Il lease-DB-time + `attempt_number` monotono + immutabilità terminale fenciano le **mutazioni del
control-plane**: un worker il cui lease è scaduto non può più avanzare PostgreSQL — non rinnova, non
marca `writes_started`, non terminalizza. Ogni operazione write-adjacent richiede un lease valido, e
un attempt terminale è immutabile.

Questo **non** fencia il mondo esterno. Un vecchio subprocess Go potrebbe continuare a scrivere da
remoto anche quando non può più aggiornare PostgreSQL. Il fencing delle operazioni esterne (un
fencing token che la destinazione stessa rifiuta) è un problema distinto e futuro. È proprio per
questo che un attempt scaduto con `writes_started` diventa `partial` e non invita mai un retry
automatico: la piattaforma non può provare che le write remote siano cessate.

### Propagazione monotona di `writes_started`

`writes_started` transita solo da `false` a `true`, mai indietro. Esiste sull'attempt (questo
tentativo ha iniziato una fase potenzialmente mutante) e come aggregato sull'execution. `false→true`
avviene nella **stessa transazione** su entrambi. È l'indicatore durevole che la roadmap §5.5
richiede: l'assenza di un report non prova l'assenza di write, quindi la classificazione di un
orfano non può basarsi sull'ultimo evento — deve basarsi su un indicatore durevole. `writes_started`
è quell'indicatore, e prevale su qualunque classificazione ottimistica.

### Il reconciler è l'unica autorità sugli orfani

La decisione di classificare un attempt orfano vive in un service transazionale riutilizzabile
(`reconcile_expired_attempts` / `reconcile_execution`), non in un actor Dramatiq specifico. In futuro
sarà invocato da startup reconciliation, da un actor periodico o da un comando amministrativo:
nessuno di questi invocatori conterrà regole di classificazione proprie.

Per un attempt attivo con lease scaduto:

- `writes_started = false` → attempt `interrupted`, execution `interrupted`;
- `writes_started = true` → attempt `partial`, execution `partial`.

Un processo scomparso **non** viene classificato `failed`: l'assenza di un report non prova l'assenza
di write. La riconciliazione è idempotente — rieseguirla su uno stato già terminale non produce
modifiche — e non crea mai un nuovo attempt.

### `interrupted` contro `partial`

`interrupted` è la parola del reconciler per un proprietario perso *prima* di qualunque write:
niente è stato scritto sulla destinazione. `partial` è un proprietario perso *dopo* l'inizio di una
write: la destinazione è scritta a metà. La differenza è l'unica cosa che serve all'operatore, e ha
conseguenze opposte: un `interrupted` è potenzialmente eleggibile a un nuovo tentativo esplicito; un
`partial` richiede una nuova inventory/comparison della destinazione prima di qualsiasi rerun.

`partial` prevale: un attempt con `writes_started=true` che un worker vorrebbe chiudere come `failed`
o `cancelled` viene registrato `partial`. `succeeded` resta `succeeded` (le write sono complete).
`interrupted` è riservato al reconciler: un worker non può auto-attribuirselo.

### Retry tecnico, resume e rerun

I tre concetti non sono equivalenti:

- **retry tecnico** — nuovo tentativo della stessa execution *prima* di qualunque write. È una policy
  **futura ed esplicita**: non compare implicitamente in `acquire_attempt`. Questa PR non lo
  implementa; un attempt scaduto viene *solo riconciliato*, mai preso in consegna con la creazione
  automatica di un secondo attempt.
- **resume** — continuazione da un checkpoint supportato dall'executor. **Non esiste** finché il
  motore Go non possiede checkpoint verificabili. Un attempt `partial` non è ripartibile: non c'è
  prova di dove la write si sia fermata.
- **rerun** — nuova execution ancorata a nuovi gate e, se necessario, nuovi snapshot. È l'unica via
  dopo un `partial`.

**Nessun retry automatico è consentito dopo l'inizio di una write.** Il broker non ritenta; il worker
terminalizza o viene riconciliato; un nuovo tentativo richiede una decisione esplicita dell'operatore.

### Mappatura attempt → execution: aggregata, non 1:1 strutturale

In questa PR, senza retry policy, un'execution ha esattamente un attempt, quindi un attempt terminale
terminalizza la sua execution:

| attempt terminale | execution |
|---|---|
| `succeeded` | `succeeded` |
| `partial` | `partial` |
| `cancelled` | `cancelled` |
| `failed` | `failed` |
| `interrupted` | `interrupted` |

Questa è la mappatura **della policy corrente**, non una legge strutturale. Una futura retry policy
la disaccoppierà: un attempt `failed` o `interrupted` non dovrà terminalizzare un'execution ancora
eleggibile a un nuovo tentativo esplicito. Il codice tiene la mappatura in una costante dedicata
(`ATTEMPT_TERMINAL_TO_EXECUTION_STATUS`) proprio perché sia il solo punto da rivedere quando quella
policy arriverà.

## Alternative scartate

- **Fencing token per-account (dal prototipo `a2/a4`).** Il prototipo interno modellava un lease
  *per destination endpoint* con un `fencing_token` monotono copiato sull'attempt. Scartato per
  questa PR: la granularità è quella dell'esecuzione, non dell'account, e la mutua esclusione
  per-attempt è già data da `attempt_number` monotono + indice unique parziale + immutabilità
  terminale, con meno superficie. Il lease per-account resta un concern separato e successivo (M4).
- **Tempo del processo Python per il lease (dal prototipo).** Il prototipo decideva la scadenza con
  `datetime.now()` del worker, iniettabile per i test. Scartato: rende la correttezza dipendente
  dalla sincronizzazione dei clock dei worker. Il tempo del database è l'unica autorità.
- **Retry automatico / takeover in `acquire_attempt`.** Creare un secondo attempt al momento
  dell'acquisizione, riconciliando al volo il precedente scaduto, semplificherebbe la race. Scartato:
  introdurrebbe un retry implicito, esattamente ciò che l'invariante §4.10 vieta. La riconciliazione
  è classificazione, non ripartenza.
- **Classificare come `failed` un processo scomparso.** Scartato: invita l'operatore a ritentare su
  una destinazione di stato ignoto. L'assenza di un report non prova l'assenza di write.

## Conseguenze

- Un solo worker può possedere un'esecuzione; due acquisizioni concorrenti producono un solo
  vincitore, provato su PostgreSQL reale.
- Un crash del worker non lascia un record `running` indefinito: il reconciler lo classifica
  `interrupted` o `partial` in modo idempotente, senza retry ambiguo. Questo porta la piattaforma
  verso **R2** (dry-run recuperabile), ma non la completa: mancano ancora artifact durevoli,
  workspace runtime e il subprocess stesso.
- Il fencing è del control-plane. Il fencing delle write esterne resta aperto e vincola il primo
  apply (M5) a una singola mailbox sacrificabile con `partial` che prevale.

## Rischi residui

- Le proprietà di concorrenza sono provate solo con `TEST_POSTGRES_URL` impostato (compose-smoke),
  non nel `make api-test` di default: la CI obbligatoria cross-language è un incremento distinto
  (roadmap Gruppo A #4).
- Nessun fencing delle operazioni esterne: un subprocess Go orfano può ancora scrivere da remoto.
- Nessun subprocess, nessuna workspace, nessuna host identity: questa PR è solo il fondamento di
  ownership e recovery, non un dry-run end-to-end.
