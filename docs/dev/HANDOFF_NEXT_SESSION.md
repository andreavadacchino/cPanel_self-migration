# Prompt di avvio — prossima sessione

Stai lavorando sul tool Go **cpanel-self-migration**, directory locale abituale:
`/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration`.

## Leggi PRIMA

1. `docs/dev/FRONTEND_FLIGHT_DIRECTOR_ROADMAP.md`
2. `docs/dev/PR69_JOB_JOURNAL_DESIGN.md` (spec della fase Job Journal — **IMPLEMENTATA**, GitHub PR #70)
3. `docs/dev/DEVELOPMENT_STATE.md`
4. `docs/dev/DOGFOODING_2_REPORT.md`
5. `docs/dev/DOGFOODING_3_UX_WALK.md`
6. `docs/dev/CUTOVER_RUNBOOK.md`

## PR #70 — In-Flight Job Rehydration Journal — COMPLETATA (2026-07-06)

Prima fase tecnica della roadmap documentale #69 (Setup/Rehydration Foundation),
mergiata come **GitHub PR #70**. Consegnato:

- `job.json` per-working-dir (atomico 0600, come `store.writeSession`): identità+fase
  dell'exec in corso/ultimo. Schema **lean** ratificato (no item-level in job.json:
  riusato da `loadRunMonitor`/`events.jsonl`); granularity **opzione B** (nessun writer toccato).
- rehydration minima dell'exec in corso su refresh (banner running/interrupted);
- **409 leggibili** su tutti e 3 i chiamanti dello slot (`/run`, `/accept`, `/exec`) via `writeBusy409`;
- **recovery** `running`→`interrupted` allo startup + reconcile read-time (no-write su GET);
- **rollback gated by backup** (`areaFacts.BackupPresent` → pulsante solo se `<area>_backup.json` esiste);
- **meta-refresh 2s** sulle schermate workbench mentre `JobLive` (riuso pattern dashboard);
- anti-leak: journal solo identità+fase, mai credenziali/argv (testato anche sul failure path).

**SSE NON implementata** — rimandata, da **rivalutare solo dopo dogfooding reale su una
migrazione lunga**. Molto del valore SSE (reconnect, phase progress, stati) è già coperto da
`job.json` + `loadRunMonitor` + meta-refresh; l'incremento reale è UX (no flicker, log-tail live)
a costo di complessità (endpoint streaming long-lived, gate su GET persistente, reconnect).

## PR #72 — New Migration Wizard (setup flow) — COMPLETATA (2026-07-06)

Mergiata su fork main (merge `43f29d6`). Consegnato:

- **Wizard** `/workbench/new`: nuova migrazione → sorgente → destinazione → account cPanel →
  cosa migrare → preflight, in linguaggio operatore.
- **Modello dati** `internal/workbench/types.go`: `Endpoint` (host/porta/account, NESSUN campo
  segreto), `ContentSelection` (files/db/email/email_config/cron/**dns separato opt-in**),
  `SetupMeta`, `Session.Setup *SetupMeta` (pointer omitempty → sessioni vecchie leggono).
  `Store.CreateWithSetup` (Create delega).
- **Credenziali metadata-only** (scelta motivata dal codice): il wizard NON raccoglie segreti;
  `host.yaml` (0600) resta la sede delle credenziali via il form `/config` esistente, collegato da
  una callout. Anti-leak per costruzione (Endpoint senza segreto) + canary su disco + guardia
  strutturale. Il wizard NON genera `host.yaml`.
- **Gating frontend** dello schermo «Applica» su `Session.Setup.Content` (`contentScope`/
  `deriveContentScope` → `workbenchView.Scope`): aree non selezionate = «non incluso», niente
  apply/verify/rollback. Legacy `Setup==nil` invariato. Gating **frontend-only dichiarato**:
  `/exec` non è gateato server-side, la conferma forte per-account resta il vero gate di scrittura.

## PR #73 — Next actions scope-aware — COMPLETATA (2026-07-06)

Chiude il debito UX residuo di #72: il banner «prossima azione consigliata» non cita più aree
escluse dallo scope. `nextAction`/`missingVerifies` filtrano per `contentScope`; preflight elenca
le aree incluse; DNS incluso → nota prudente; DNS/Cron/EmailConfig esclusi → mai citati; legacy
invariato. Presentation-only, nessun writer/runner/apply/verify toccato.

## PR — Flight Director UI shell — COMPLETATA (2026-07-06)

Salto da «schermate workbench» a **cabina di regia**, SOLO presentazione. Consegnato:

- **Header persistente** (`fdHeader`) su tutte le schermate sessione: nome migrazione, dominio
  principale, sorgente→destinazione con account/porte, stato governance, **risk badge** onesto,
  tag DNS incluso/escluso, job in corso/interrotto, prossima azione. Wizard mostra account@host;
  legacy `Setup==nil` fa fallback a source/destination profile.
- **Timeline laterale** (`fdTimeline`): le 7 fasi (Panoramica · Preflight · Fotografia account ·
  Cosa verrà migrato · Conferme operatore · Applica e verifica · Chiusura) con stato sintetico
  (Da fare / In corso / Fatto / Attenzione), fase corrente evidenziata, link alle **route reali**
  esistenti (nessuna route inventata). Sostituisce il vecchio pill-nav `wbNav`.
- **Main stage** contestuale: i template delle schermate esistenti restano invariati dentro
  `<main class="fd-stage">`; `wbHead`/`wbFooter` ristrutturati per la shell a due colonne.
- **Risk badge onesto** (`buildRiskBadge`) e **timeline** (`buildTimeline`): funzioni pure in
  `workbench_flightdirector.go`, derivate da status + artifact facts + job journal + scope. NON
  promettono falso verde: stati terminali (cutover completato / archiviata) vincono su una
  checklist stale; job running/interrotto e blocker restano visibili.

Nessun writer/runner/apply/verify/collector toccato; nessun endpoint/SSE nuovo; `/exec` immutato;
form critici (migrate_content, conferma forte per-account, DNS danger zone, CSRF) intatti; legacy
invariato. Gate: go-reviewer R1 REQUEST CHANGES (stati terminali + coerenza inventario) → R2
APPROVE; Docker LINUX_ALL_GREEN; race webui+workbench verde.

**Prossima direzione consigliata (NON iniziata):** o **dogfooding UI reale** su una migrazione
lunga (per decidere se SSE serve davvero), oppure **Comparative Checklist UI** (source vs
destination per area). NON Campaign Mode. **SSE ancora rimandata** — non iniziare codice SSE.

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
- **#69**: roadmap Flight Director (docs-only) + spec dev-ready Job Journal.
- **#70**: In-Flight Job Rehydration Journal (`job.json`, 409 leggibili, recovery interrupted,
  rollback gated by backup, meta-refresh live). SSE NON inclusa (rimandata).
- **#72**: New Migration Wizard + `Session.Setup`/`ContentSelection` (DNS separato/opt-in).
- **#73**: Next actions scope-aware.
- **Flight Director UI shell**: header persistente + timeline laterale + risk badge onesto
  (presentation-only, SSE ancora rimandata).

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

### PR 69 — In-Flight Job Rehydration (Job Journal) — ✅ FATTA (GitHub PR #70)

Implementata e mergiata (vedi «PR #70 — COMPLETATA» sopra). Spec: `PR69_JOB_JOURNAL_DESIGN.md`.
Il setup wizard (69b) NON è incluso: è la prossima direzione consigliata (Setup Flow).

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

### PR 70 (roadmap) — Live Job Engine: SSE + Progress/Log History — ⏸️ RIMANDATA

Da rivalutare **solo dopo dogfooding reale su una migrazione lunga**. Non iniziare in questa fase.

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
7. ~~**Schema `job.json`**~~ — **RATIFICATO (PR #70)**: schema lean (`session_id, action, started_at,
   updated_at, state, phase, error, tool_version`), path `<dir>/job.json`, nessun TTL (un journal per
   working dir, sovrascritto). Item-level NON in job.json (riusato da `loadRunMonitor`).
8. ~~**Progress granularity**~~ — **RATIFICATO (PR #70)**: opzione B (phase-level dal journal;
   item-level solo per `migrate_content` dal monitor esistente). Nessun writer toccato. Opzione A → PR SSE futura.
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
