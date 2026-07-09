# Sessione 2026-07-07 — platform/workbench: isolamento sessione + stato/step coerenti

Report di handoff della sessione corrente. Focus: bug reali di runtime/UI, non
restyling.

## Obiettivo effettivo della sessione

Chiudere due problemi strutturali emersi nel dogfooding reale:

1. **Contaminazione fra migrazioni**: la UI/workbench leggeva e scriveva ancora
   artefatti nella root globale in alcuni punti, quindi una sessione poteva
   vedere `report.json`, `job.json`, checklist o acceptances di un'altra.
2. **Stepper/stato incoerente**: la piattaforma restava visivamente su
   `setup`, anche dopo la creazione della migrazione o durante l'avanzamento,
   perché:
   - il wizard creava la sessione ma la lasciava in `draft/setup`;
   - `Session.CurrentStep` non veniva aggiornato quando `Status` cambiava.

## Cosa è stato verificato prima di toccare il codice

- Il bug di contaminazione non era teorico: diverse route workbench/platform
  passavano ancora `s.dir`/`ws.dir` globali invece della `ArtifactDir`
  della sessione.
- La UI su `127.0.0.1:8422` già in esecuzione nel workspace non era quella
  pulita avviata in questa sessione; esponeva sessioni e stato di run precedenti.
- Il bug dello stepper non era un problema di template: la barra legge
  `statusStepIndex(sess.Status)` sulla piattaforma, ma molti flussi continuavano
  a lasciare la sessione in `draft`, e `CurrentStep` restava fermo a `setup`.

## Fix runtime completati

### 1. Isolamento per-sessione degli artefatti

Fix già completato e verificato nella sessione precedente di lavoro su questo
turno, poi riconfermato qui:

- le schermate workbench leggono da `sessionWorkDir(sess, fallback)`;
- `exec`, `start-migration`, `accept`, `scope confirm`, render platform e SSE
  usano ora sempre `sess.ArtifactDir` quando presente;
- i test che simulavano ancora la root globale sono stati riallineati.

File chiave toccati in questo ciclo di lavoro:

- `internal/webui/webui.go`
- `internal/webui/workbench.go`
- `internal/webui/workbench_scope_confirm.go`
- `internal/webui/platform.go`
- relativi test webui/workbench

Effetto: una migrazione non pesca più `job.json`, `report.json`,
`migration_checklist.json`, `host.yaml` o acceptances di un'altra.

### 2. Wizard → `preflight_required` immediato

Correzione applicata sia al wizard workbench sia al wizard platform:

- dopo `CreateWithSetup(...)`, il wizard esegue subito
  `SetStatus(..., StatusPreflightRequired, ...)`.

File:

- `internal/webui/workbench_wizard.go`
- `internal/webui/platform.go`

Effetto:

- una nuova sessione non resta più in `draft/setup`;
- la UI parte coerentemente dal passo Preflight.

### 3. `CurrentStep` coerente con `Status`

Aggiunta la mappatura centralizzata `stepForStatus(...)` nel session store:

- `SetStatus(...)` aggiorna anche `sess.CurrentStep`;
- `blocked` e `failed` **preservano** l'ultimo passo operativo valido, invece di
  degradare a uno step inventato;
- `ready_for_apply` / `apply_in_progress` / `apply_done` mappano a
  `StepApplyCore`;
- `verification_required` → `StepVerify`;
- `ready_for_cutover` / `cutover_done` → `StepCutover`.

File:

- `internal/workbench/store.go`

Effetto:

- lo stepper non resta più inchiodato a `setup`;
- workbench e platform possono leggere un `CurrentStep` credibile e stabile.

## Test eseguiti

Mirati:

- `go test ./internal/workbench ./internal/webui -run 'TestUpdateStatus|TestUpdateStatusPreservesOperationalStepOnBlockedAndFailed|TestWizardCreatesSessionWithSetup|TestPlatformWizardCreatesSessionAndRedirects|TestPlatformCockpitMonitorHonest|TestStatusStepIndexInRange'`

Suite complete:

- `go test ./internal/workbench ./internal/webui`

Esito: **verde**.

## Stato reale attuale della UI

### Istanza pulita avviata in questa sessione

- URL: `http://127.0.0.1:8488/platform/migrations`
- dir runtime: `.tmp/ui-smoke-run`
- serve per smoke test locale isolato

### Istanza storica già attiva nel workspace

- URL: `http://127.0.0.1:8422`
- processo già esistente, con sessioni e job vecchi
- non usarla per validare i fix senza riavviarla, perché confonde il risultato

## Problemi ancora APERTI

### A. Monitor esecuzione troppo opaco

Bug/problema prodotto reale confermato dall'utente:

- la barra/stepper non basta;
- l'operatore non capisce **cosa** sta migrando in quel momento;
- oggi il cockpit mostra:
  - fasi macro;
  - item-level solo quando `events.jsonl` contiene dati già emessi dal motore;
  - log safe di errori/eventi, ma non una vista “alla WHM Transfer Tool”.

### B. Il motore NON espone ancora il file corrente

Verifica fatta nel codice:

- `migrate_mail` già emette gli item (`items[]`) con mailbox e stato;
- `migrate_db` già emette i DB migrati;
- `create_domains` emette domini failed/blocked;
- `copy_files` **non** emette il file corrente o una lista file: oggi espone solo
  `failed: N`.

Quindi:

- si può migliorare SUBITO la UI con un box “Attività reale” usando i dati già
  presenti;
- **non** si può fingere il nome del file corrente senza estendere il motore o
  il formato eventi.

### C. Stepper platform ancora troppo grossolano

Anche dopo il fix runtime, la piattaforma usa ancora una mappatura coarse:

- `statusStepIndex(status)` in `internal/webui/platform_view.go`

È coerente, ma non sempre abbastanza espressiva per distinguere bene:

- Piano vs Scope confermato;
- Migrazione avviata vs Migrazione completata ma ancora da verificare;
- Task manuali pre-apply vs post-apply.

Non è rotto come prima, ma è ancora migliorabile.

## Raccomandazione precisa per la prossima sessione

Priorità alta:

1. **Box “Attività reale” nel cockpit/platform cockpit**
   - mostrare mailbox / database / domini già disponibili da `events.jsonl`;
   - se la fase è file-copy, dire esplicitamente che il motore oggi non espone il
     file corrente, invece di fingere dettaglio.
2. **Riesame dello stepper platform**
   - verificare se `manual_actions_required` e `apply_done/verification_required`
     vadano rappresentati meglio nel percorso 1..7.

Priorità media:

3. decidere se il monitor debba restare a `details` collassato o diventare un box
   sempre visibile nel cockpit operatore.

## File principali toccati in questa sessione

- `internal/workbench/store.go`
- `internal/workbench/store_test.go`
- `internal/webui/workbench_wizard.go`
- `internal/webui/platform.go`
- `internal/webui/workbench_wizard_test.go`
- `internal/webui/platform_render_test.go`

## Giudizio onesto

Il bug dello stepper fermo su `setup` era reale ed era nel modello/stato, non nei
CSS. È stato corretto.

La UX di monitoraggio invece è ancora **insufficiente** per una piattaforma di
migrazione “smart”: manca una vista chiara di attività corrente, e il vincolo
tecnico più duro è che il motore non espone ancora il file corrente della fase
web files.
