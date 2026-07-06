# Prompt di avvio — prossima sessione

Stai lavorando sul tool Go **cpanel-self-migration**, directory locale abituale:
`/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration`.

## Leggi PRIMA

1. `docs/dev/FRONTEND_FLIGHT_DIRECTOR_ROADMAP.md`
2. `docs/dev/PR69_JOB_JOURNAL_DESIGN.md` (spec developer-ready della prossima PR)
3. `docs/dev/DEVELOPMENT_STATE.md`
4. `docs/dev/DOGFOODING_2_REPORT.md`
5. `docs/dev/DOGFOODING_3_UX_WALK.md`
6. `docs/dev/CUTOVER_RUNBOOK.md`

## Stato consolidato al 2026-07-06

Il tool è molto avanzato sul singolo account:

- core migrazione contenuti presente;
- inventory/diff/policy/checklist presenti;
- DNS/email/cron plan/apply/verify presenti;
- workbench session model presente;
- artifact registry presente;
- UI locale presente;
- UI italiana presente;
- design system moderno presente;
- dogfooding UI-only fino a `ready_for_cutover` documentato.

Le PR recenti hanno chiuso diversi blocchi importanti:

- **#63**: fix encoding UAPI `+/%` per evitare corruzione TXT/DKIM/SPF.
- **#64**: fix apex DNS `@` → FQDN per `mass_edit_zone`.
- **#65**: dogfooding #2 aggiornato: ciclo UI-only completabile fino a `ready_for_cutover`.
- **#66**: workbench UX redesign in 7 schermate.
- **#67**: traduzione IT delle manual actions a livello presentazione.
- **#68**: design system condiviso e landing moderna.

## Correzione strategica

Non considerare la UI “finita” solo perché è più moderna.

La UI è migliorata, ma resta ancora troppo vicina al modello ingegneristico: sessioni, artifact, policy, acceptances, status governance, apply/verify report.

La prossima fase NON deve essere un altro restyling.

La prossima fase deve trasformare la UI in un **Flight Director**: una cabina di regia migration-first che impedisce all’operatore di perdere il controllo durante migrazioni lunghe, refresh, job interrotti, azioni manuali e cutover.

Principio guida:

> Prima rendi impossibile perdere il controllo. Poi rendi l’interfaccia bella.

## Decisione di prodotto

Il tool non deve esporre come esperienza principale:

- artifact;
- policy;
- acceptance;
- raw status transitions;
- JSON/report tecnici.

Deve invece guidare l’operatore con:

- nuova migrazione;
- sorgente;
- destinazione;
- account sorgente/destinazione;
- cosa vuoi migrare;
- preflight;
- avvia migrazione;
- progress/log live;
- checklist comparativa source/destination;
- task manuali con valori copiabili;
- verifica finale;
- cutover gateway;
- archivio/report.

Gli artifact restano fonte auditabile, ma non devono essere il linguaggio primario della UI.

## Roadmap frontend aggiornata

### PR 69 — In-Flight Job Rehydration (Job Journal)

Questa è la prossima PR consigliata. **Spec developer-ready: `PR69_JOB_JOURNAL_DESIGN.md`.**

Obiettivo: la UI non deve mai perdere il controllo di un job in corso. La
rehydration di stato *completato* **esiste già** (`readArtifactFacts` in
`workbench_view.go`, letta da disco a ogni GET — dogfooding #3): va **riusata**, non
riscritta. Il vero gap è l'**in-flight job**: oggi l'exec gira sincrono con tail
in-memory, e un refresh/sleep lo rende irriattaccabile (409 opaco).

Deliverable primario: **job journal (`job.json`)** — identità e progresso persistiti
dell'exec in corso/ultimo, così un refresh ricostruisce «`migrate_content` in corso
dalle HH:MM, fase X» e il 409 diventa uno stato leggibile.

Scope:

- **job journal (`job.json`)**: persistere identità+progresso; superficie dell'exec
  in corso su refresh; eliminare il 409 opaco;
- **riuso** di `readArtifactFacts` (nessuna riscrittura);
- wizard nuova migrazione; source/destination/account setup;
- decisione iniziale su gestione credenziali (§12 roadmap);
- backup detection → Rollback offerto solo se il backup esiste;
- empty/error states chiari.

Se troppo grande, splittare: **69a** job journal + exec in corso (fondazione),
**69b** setup wizard + credenziali.

Fuori scope:

- Campaign Mode;
- queue multi-account;
- nuovi writer;
- nuovi collector;
- full visual redesign;
- cutover automation.

### PR 70 — Live Job Engine: SSE + Progress/Log History

Scope:

- SSE endpoint;
- live log stream;
- historical log tail;
- progress per fase/item;
- reconnect dopo refresh;
- stati interrupted/failed/completed.

SSE è trasporto live, non fonte di verità. La fonte di verità resta sessione + artifact + events/report.

### PR 71 — Flight Director UI

Scope:

- header globale persistente;
- timeline laterale;
- main stage contestuale;
- next recommended action;
- risk badge;
- separazione chiara fra contenuti, email config, cron, DNS, verify, cutover.

### PR 72 — Comparative Checklist UI

Scope:

- vista source vs destination;
- stato per area;
- cosa migrato / mancante / diverso / manuale;
- drilldown tecnico solo su richiesta.

### PR 73 — Manual Actions as Verifiable Tasks

Scope:

- valori sorgente leggibili;
- valori destinazione attuali;
- copia negli appunti;
- azione consigliata;
- `Verify now` dove possibile;
- fallback `Segna come fatto manualmente` solo dove inevitabile;
- acceptance salvata dietro le quinte.

### PR 74 — Final Sync + Cutover Gateway

Scope:

- sync finale;
- warning per DB/siti dinamici;
- verify finale fresco;
- decisione cutover;
- stato osservazione/quarantena prima di considerare il vecchio server dismissible.

### PR 75 — Final Report / Archive

Scope:

- report finale HTML/PDF-style;
- riepilogo dati migrati;
- azioni manuali confermate;
- note irrisolte;
- raccomandazioni post-cutover;
- archivio sessione.

## Domande aperte prima di PR 69

1. Il pulsante “Avvia migrazione” deve includere solo file/db/mail, oppure anche email config e cron?
2. DNS deve essere applicabile dalla UI o inizialmente solo “copy map + verify”?
3. Le credenziali devono essere temporanee per singola migrazione o salvabili come profili?
4. Qual è la soglia di stale snapshot prima del cutover?
5. Cosa significa esattamente `Resume` dopo job interrotto?
6. Quanto deve durare la fase di osservazione/quarantena prima di dire che il vecchio server può essere spento?
7. **Schema `job.json`** — campi/path/TTL da ratificare (proposta in `PR69_JOB_JOURNAL_DESIGN.md` §4).
8. **Progress granularity** — estendere `--json-events` a tutte le fasi, o accettare progress per-fase per DNS/email/cron/pipeline? (`events.jsonl` oggi solo per `migrate_content`).
9. **`host.yaml`** — deciso: resta dov'è ma escluso da ogni archive/report bundle (roadmap §12).

## Non-goal permanenti per questa fase

- Nessun Campaign Mode.
- Nessuna migrazione parallela.
- Nessuna queue batch.
- Nessuna promessa da clone WHM Transfer Tool.
- Nessuna operazione root/WHM.
- Nessun bottone cieco “migra tutto”.
- DNS sempre separato da migrazione contenuti.
- Spegnimento vecchio server mai immediatamente verde senza osservazione/post-check.

## Workflow

- Solo push a fork (`git push fork`).
- PR verso `andreavadacchino/cPanel_self-migration`.
- TDD dove applicabile.
- go-reviewer multi-giro fino APPROVE PULITO.
- Docker LINUX_ALL_GREEN eseguito, non promesso.
- Gate dichiarato nel body PR prima del merge.
- `runner.go` resta off-limits salvo necessità motivata.
- Scritture reali solo su account sacrificale; produzione solo read-only salvo decisione esplicita.
