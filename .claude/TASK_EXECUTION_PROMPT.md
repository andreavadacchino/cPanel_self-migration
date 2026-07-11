# Prompt Claude Code — Esecuzione backlog Migration Platform V2

Lavora nel repository `cPanel_self-migration` e completa i task della **Migration Platform V2** descritti in `tasks/BACKLOG.md`.

## Obiettivo

Esegui i task realmente, uno alla volta e nell'ordine consentito dalle dipendenze. Per ogni task devi implementare il codice, aggiungere o aggiornare i test, verificare il risultato, effettuare una review critica delle modifiche, correggere ogni problema trovato e aggiornare lo stato documentale.

Non limitarti a proporre un piano o descrivere cosa andrebbe fatto. Continua fino a quando il task selezionato è implementato e verificato, oppure fino a quando esiste un blocco reale che non può essere risolto senza una decisione o credenziale dell'utente.

## Contesto obbligatorio

Prima di modificare file:

1. Leggi `AGENTS.md`.
2. Leggi tutti i file in `.ai/rules/`.
3. Leggi `tasks/BACKLOG.md` e `tasks/README.md`.
4. Leggi integralmente il file del task selezionato.
5. Ispeziona il codice e i test interessati; non basarti soltanto sulla descrizione del task.

La CLI Go nella root è esclusivamente un riferimento comportamentale. Il prodotto da completare è `migration-platform/`, usando FastAPI, SQLAlchemy/Alembic/PostgreSQL, Dramatiq/Redis, adapter Python e React/Vite/TypeScript. Non usare il binario Go come motore della V2.

## Selezione del task

- Se l'utente indica un ID, esegui quel task solo se tutte le dipendenze sono `[x]`.
- Se non indica un ID, seleziona il primo task `[ ]` del backlog le cui dipendenze sono tutte `[x]`.
- Non iniziare un task `[!]`, `[~]` o già `[x]`.
- Non saltare le dipendenze e non iniziare più task contemporaneamente.
- Prima di lavorare, cambia lo stato del task da `[ ]` a `[~]` sia in `tasks/BACKLOG.md` sia nel file del task.

## Regole di esecuzione

1. Mantieni il lavoro entro lo scope del task.
2. Se saranno necessari più di 8 file o 500 righe modificate, fermati e suddividi il task in sotto-task documentati prima di implementare.
3. Preserva le modifiche preesistenti e non sovrascrivere lavoro non correlato.
4. Implementa prima il comportamento minimo completo, poi i test vicini al codice.
5. Mantieni ogni writer reale disabilitato per default.
6. La sorgente cPanel deve essere strutturalmente read-only. Solo la destinazione può ricevere scritture autorizzate.
7. Non inserire token, password, ciphertext o altri segreti in log, eventi, eccezioni, payload Redis, risposte API, fixture o documentazione.
8. Una lettura fallita, parziale, ambigua o obsoleta non equivale mai a uno stato vuoto o verificato.
9. Le operazioni reali devono essere idempotenti, verificabili e protette dai gate richiesti nel task.
10. Non effettuare scritture su server cPanel reali senza una nuova autorizzazione esplicita dell'utente.

## Test e verifica

Esegui prima i test più piccoli relativi ai file modificati, quindi i gate completi applicabili:

```bash
cd migration-platform/apps/api
PYTHONPATH=../../packages/adapters python -m pytest

cd ../worker
DRAMATIQ_TESTING=1 python -m pytest

cd ../web
npm run build

cd ../..
docker compose config -q
```

Quando il task introduce tooling aggiuntivo, esegui anche lint, formatter, type checker, coverage o test frontend previsti dal task.

Non dichiarare un comando superato se non è stato realmente eseguito. Se un comando fallisce per una causa ambientale, prova a ripristinare l'ambiente usando le dipendenze e le istruzioni del progetto. Distingui chiaramente un difetto del codice da un blocco dell'ambiente.

## Review obbligatoria

Dopo l'implementazione e prima di chiudere il task, esegui una review autonoma dell'intero diff. Controlla almeno:

- correttezza rispetto a Goal e Acceptance Criteria;
- errori, edge case e regressioni;
- rispetto delle dipendenze e dello scope;
- idempotenza, concorrenza, retry, crash e cancellazione quando pertinenti;
- source-read-only e destination-only writes;
- staleness, fresh-read e fail-closed behavior;
- redazione dei segreti;
- migrazioni database e rollback;
- qualità e sufficienza dei test;
- compatibilità API e frontend;
- documentazione operativa e configurazione.

Elenca internamente i rilievi per severità. Correggi tutti i rilievi Critical, High e Medium prima di completare il task. Riesegui i test interessati dopo ogni correzione e infine riesegui i gate completi.

## Documentazione

Aggiorna la documentazione quando la modifica introduce o cambia:

- variabili d'ambiente o feature flag;
- endpoint, schemi API o stati;
- migrazioni e procedure di rollback;
- actor, code path asincroni o retry;
- procedure operative, limiti o requisiti di sicurezza;
- comandi di sviluppo, test o deployment.

Preferisci aggiornare `migration-platform/README.md` e i documenti tecnici esistenti. Crea un nuovo documento solo quando il contenuto non appartiene chiaramente a un documento già presente. La documentazione deve descrivere esclusivamente comportamento realmente implementato e verificato.

## Completamento e aggiornamento del backlog

Contrassegna il task come completato soltanto quando:

- tutte le Acceptance Criteria sono soddisfatte;
- i test richiesti sono stati aggiunti e superano;
- la review è terminata e i rilievi rilevanti sono risolti;
- la documentazione necessaria è aggiornata;
- non rimane lavoro necessario nello scope del task.

Quando il task è completo:

1. Spunta tutte le checkbox realmente soddisfatte nel file `tasks/<ID>-*.md`.
2. Cambia `**Status**` da `[~]` a `[x]` nel file del task.
3. Cambia lo stato dello stesso task da `[~]` a `[x]` in `tasks/BACKLOG.md`.
4. Aggiungi in fondo al file del task una sezione `Completion Record` contenente:
   - data di completamento;
   - riepilogo conciso dell'implementazione;
   - file principali modificati;
   - test e comandi eseguiti con risultato;
   - esito della review;
   - documentazione creata o aggiornata;
   - eventuali limitazioni residue spostate in nuovi task.
5. Verifica se il completamento sblocca il task successivo, senza iniziarlo nello stesso ciclo salvo richiesta esplicita dell'utente.

Se il task è realmente bloccato:

1. Non marcarlo `[x]`.
2. Cambia lo stato in `[!]` nel file del task e nel backlog.
3. Aggiungi una sezione `Blocker Record` con evidenze, tentativi effettuati, impatto e decisione precisa richiesta all'utente.
4. Non usare `[!]` per semplici test falliti o problemi risolvibili nel repository.

## Risposta finale

Alla fine comunica in modo sintetico:

- ID e titolo del task;
- risultato ottenuto;
- modifiche principali;
- test eseguiti e relativo esito;
- review e correzioni effettuate;
- documentazione aggiornata;
- stato registrato nel backlog;
- prossimo task ora sbloccato, senza iniziarlo automaticamente.

## Avvio

Inizia ora: leggi i file obbligatori, individua il primo task eseguibile e portalo a completamento seguendo tutte le istruzioni sopra.
