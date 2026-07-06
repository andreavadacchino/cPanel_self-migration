# Prompt di avvio — prossima sessione

Stai lavorando sul tool Go **cpanel-self-migration**, directory locale abituale:
`/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration`.

## Leggi PRIMA

1. **`docs/dev/PLATFORM_MIGRATION_ROADMAP.md`** ⭐ — direzione prodotto attuale (tool → piattaforma smart) + roadmap a fasi (Fase 1…7)
2. `docs/dev/FRONTEND_FLIGHT_DIRECTOR_ROADMAP.md`
3. `docs/dev/PR69_JOB_JOURNAL_DESIGN.md` (spec della fase Job Journal — **IMPLEMENTATA**, GitHub PR #70)
4. `docs/dev/DEVELOPMENT_STATE.md`
5. `docs/dev/DOGFOODING_2_REPORT.md`
6. `docs/dev/DOGFOODING_3_UX_WALK.md`
7. `docs/dev/DOGFOODING_4_SMART_ORCHESTRATOR_WALKTHROUGH.md` (dogfooding Fase 3 — verdetto 🔵 «serve Fase 4»)
8. `docs/dev/CUTOVER_RUNBOOK.md`

## Direzione prodotto — decisioni bloccate (2026-07-06, PLATFORM_MIGRATION_ROADMAP)

Trasformazione da workbench tecnico a **piattaforma smart di migrazione**. Scoperta
chiave: **il motore è maturo e live-proven** — il gap è **orchestrazione + contratto
Plan→Scope→Execution**, non il motore. Tre decisioni bloccate (tutte Opzione 1):

1. **Orchestratore (Fase 3):** una sola conferma forte → esegue in sequenza le aree
   safe E in-scope, stop-on-first-failure, verify per fase, report unico. **DNS mai
   nell'auto-run.** Riusa `pipelineSteps` + gate esistenti.
2. **DNS:** flusso primario = fotografa → classifica (5 categorie) → task manuali
   verificabili. `dns_apply` resta azione avanzata / Danger Zone, nessuna regressione.
3. **Migration Plan (Fase 1):** read-model aggregato sopra artifact esistenti
   (riusa `readArtifactFacts`), nessun nuovo writer/CLI. `migration_plan.json`
   persistente rimandato finché lo schema non è product-validated.

**Fase 1 (PR #78), Fase 2 (PR #79), Fase 3 e Fase 4: IMPLEMENTATE.** Prossima fase tecnica
consigliata: **Fase 5 — Comparative Manual Tasks** (task DNS src/dst, copia valore, «Verifica
ora», stati task; riusa acceptance model). SSE resta rimandata finché un dogfooding reale su una
migrazione lunga non la giustifica — il monitor esecuzione della Fase 4 (meta-refresh 2s + job
journal + events.jsonl) è ora il prerequisito per osservare quel run. I numeri GitHub reali sono
assegnati all'apertura delle PR.

**Dogfooding #4 (2026-07-07, `DOGFOODING_4_SMART_ORCHESTRATOR_WALKTHROUGH.md`):** UI-walk in browser
reale + suite test (43/43) + una esecuzione reale dell'orchestratore osservata end-to-end (fallita al
config-load, dir isolata senza `host.yaml` → **nessun server contattato**, ma percorso di fallimento
parziale reale). Verdetto **🔵 Buono ma serve Fase 4**: flusso wizard→piano→scope→una-conferma→stato
parziale coerente e usabile; DNS spiegato e mai in auto-run; **non** dimostrabile «meta-refresh vs SSE»
senza una migrazione lunga reale → il monitor d'esecuzione (Fase 4) è prerequisito per un apply reale
su sacrificale (Scenario A). Nessun bug bloccante; due friction di messaggistica annotate
(readiness↔next-action; badge «Bloccante»-cutover vs migrazione avviabile).

## Fase 4 — Modern Migration Cockpit + Execution Monitor — COMPLETATA (2026-07-07)

Trasforma la Panoramica in una **cabina di regia** (presentation-only). Consegnato:

- **`internal/webui/workbench_cockpit.go`** (NUOVO, read-model puro): `buildCockpit(...)` aggrega
  hero-state + CTA dominante, stepper orizzontale (riusa i 7 step di `buildTimeline`), **comparativa
  sorgente↔destinazione** (`buildCockpitComparison` — conteggi SOLO da `MigrationChecklist.Sections`
  `SourceCount/DestinationCount`; files/email_config senza conteggio onesto → «—», mai inventati),
  piano semplificato in 3 bucket (`bucketPlanAreas`: automatico/manuale/escluso) ed **execution
  monitor** (`buildCockpitMonitor`: fasi Contenuti/Config email/Cron/DNS con stati
  not_run/running/completed/completed_with_report/failed/skipped/manual). **DNS sempre manuale, mai
  auto-run.**
- **Cablaggio `loadRunMonitor` nel workbench**: l'item-level da `events.jsonl` (mailbox/DB-name/file
  falliti — **non** messaggi/tabelle/file-count) era wired solo nella dashboard legacy; ora è nel
  cockpit via `buildWorkbenchView` (fail-soft). Il «Log esecuzione» espone SOLO `run.Errors` (redatti
  by construction) + `job.Error` — **mai** raw tail/host/argv (il tail exec non è persistito né redatto).
- **`reconcileNextAction`** (dogfooding #4 §6.1): allinea la «Prossima azione» persistente alla
  readiness del piano (niente più «esegui preflight» quando checklist+piano dicono «pronto»); deferisce
  agli stati post-apply/terminali e a `ContentApplyPresent` per non ri-offrire «Avvia» dopo una migrazione.
- **`buildRiskBadge` split** (dogfooding #4 §6.2): «Bloccante migrazione» (error, apply-blocked) vs
  «Bloccante cutover» (warn, OverallBlocked ma migrazione avviabile) — resi espliciti anche nel cockpit.
- **UI**: `screen_migrazione` riusa il define condiviso `startMigrationForm` (nessuna duplicazione del
  form pericoloso) e collassa la coverage tecnica sotto `<details>`; `screen_applica` rinominata
  «Azioni avanzate» (percorso esperto, azioni singole intatte, DNS Danger Zone invariata); dettagli
  tecnici (definizione/governance/cronologia/report) collassati sotto `<details>` in Panoramica.
- **Fuori scope confermato**: nessun writer/CLI/`migration_plan.json`, nessuna SSE, nessun nuovo motore
  di comparazione (niente parsing di inventory/diff), orchestratore/gate/CSRF/strong-confirmation immutati.
- Test: 20 nuovi (unit read-model + render HTTP) sui 15 casi del brief; `TestRiskBadge*` aggiornati.
  Gate: gofmt/vet puliti, go test webui/workbench/config verde, race verde, `git diff --check` pulito,
  **Docker LINUX_ALL_GREEN** (go1.25.11, 20 pkg, 0 FAIL); go-reviewer = gate utente.

## Fase 3 — Smart Migration Orchestrator — COMPLETATA (2026-07-06)

Il bottone «Avvia migrazione» diventa reale: **una sola strong-confirmation** avvia in sequenza le
aree automatiche/safe/in-scope, DNS **mai** nell'auto-run. Consegnato:

- **`internal/webui/workbench_orchestrator.go`**: `buildOrchestratorPhases(dir, f, scope)` deriva le
  fasi **server-side** (contenuti se File/DB/Email in scope → un solo `migrate_content` con
  `--file/--db/--mail` coerenti; config email/cron **automatiche solo se il piano esiste**, stessa
  classifica del Migration Plan). `runOrchestration` esegue con **gate `isApplyBlockedByChecklist`
  ricontrollato PRIMA di ogni fase write** (roadmap §14.3), verify inline dove esiste (email/cron con
  **`--fail-on-drift`** → un drift ferma il run come un apply fallito), `migrate_content` =
  `completed_with_report` (nessun verify clean finto), stop-on-first-failure, attach artifact
  best-effort, journal per-fase. **Nessun rollback automatico.**
- **`handleStartMigration`** (POST `/workbench/session/<id>/start-migration`, CSRF via `server.post`):
  richiede Setup + `ScopeConfirmedAt`, **una** strong-confirmation (`validateStrongConfirmation`),
  ricalcola `artifactFacts`+`contentScope`+`MigrationPlan` (non si fida dello scope salvato), rifiuta
  se `!CanStartMigration` o nessuna fase automatica, riserva lo **slot single-writer condiviso**
  (409 leggibile via `busyMessage`), redirect `?migrate=<code>`.
- **`contentScope` è ora gate server-side reale per l'orchestratore**: area esclusa = nessun flag/fase.
  Il path `/exec` avanzato NON è cambiato (rischio invariato: il vero gate write resta la
  strong-confirmation per-account + checklist).
- **Estrazione helper argv condivisi** in `workbench_exec.go` (`migrateContentArgv`,
  `email/cronApplyArgv`, `email/cronVerifyArgv(failOnDrift)`), riusati da registry `/exec` E
  orchestratore → **nessuna duplicazione di logica pericolosa** (`--yes-apply-writes`/`--backup`/`--apply`
  vivono una volta sola), **nessun nuovo writer/CLI**.
- **UI** (`screen_migrazione`): bottone «Avvia migrazione» attivo (`StartEnabled`) solo con piano
  pronto + scope confermato + nessun job live; `migrationCTALabel(p, jobLive)` aggiornata (attiva
  «Avvia migrazione» / «Migrazione in corso»); flash `migrateFlash`
  (done/done_manual/partial/gate_stopped/blocked/no_auto/scope_unconfirmed/needs_setup) — la UI non
  promette «completato» quando è parziale.
- **Fuori scope confermato**: nessun `migration_plan.json`, nessuna SSE, nessun Campaign Mode/queue,
  nessuno switch DNS, `dns_apply` resta azione avanzata / Danger Zone.
- Test: 18 unit/handler webui (15 obbligatori + gate mid-run §14.3 + CSRF + cron-senza-piano);
  regressione `/exec`/plan/scope verde. Gate: gofmt/vet puliti, go test webui/workbench/config verde,
  race verde, `git diff --check` pulito; **go-reviewer + Docker LINUX_ALL_GREEN = gate utente**.

## Fase 2 — Scope Confirmation after Preflight — COMPLETATA (2026-07-06)

Usa il Migration Plan (Fase 1) per far confermare/raffinare all'operatore cosa migrare, DOPO il
preflight, prima dell'orchestratore. Consegnato:

- **`internal/webui/workbench_scope_confirm.go`**: preset → `ContentSelection`
  (`all_safe`/`site`/`email`/`files`/`databases`/`custom`); **DNS mai nel set automatico di un
  preset** (checkbox indipendente «Includi DNS come task manuale/verificabile»). `hasAutomaticArea`
  (DNS-only NON conta). `canEditScope(f, jobLive)`: scope congelato una volta partita una write
  (`report.json` o `<area>_apply_report.json`) o con job live. `handleConfirmScope` (POST
  `/workbench/session/<id>/scope`, CSRF): edit-gate → preset → **rifiuta DNS-only** (redirect
  `?scope=need_area`, nessuna mutazione) → `ConfirmScope` → redirect `?scope=updated`.
- **`internal/workbench` (types+store)**: `SetupMeta.ScopeConfirmedAt *time.Time` (omitempty,
  backward-compatible); `Store.ConfirmScope(id, content, now)` = mutazione METADATA (no write di
  migrazione), timeline event `scope_confirmed`, una sessione legacy (Setup nil) **acquisisce** un
  Setup.
- **UI** (`screen_migrazione`): blocco «Conferma cosa vuoi migrare» (radio preset + checkbox custom
  + DNS), flash `?scope=`, badge «Scope confermato». **CTA state-aware** `migrationCTALabel`:
  non-pronto → «Esegui il preflight…»; bloccato → «Migrazione bloccata…»; non confermato → «Conferma
  lo scope prima di avviare»; confermato+pronto → «Avvia migrazione — disponibile nella Fase 3».
  Il bottone **resta disabilitato**.
- **Non toccati**: `validateStrongConfirmation`, `isApplyBlockedByChecklist`, `actionRegistry`,
  `pipelineSteps`; `contentScope` **non** reso gate server-side (Fase 3). Nessun writer/CLI nuovo.
- Test: 11 unit/handler/render (webui) + 2 store. Gate: gofmt/vet puliti, go test verde, race verde,
  Docker LINUX_ALL_GREEN; go-reviewer.

## Fase 1 — Platform Migration Plan / Readiness — COMPLETATA (2026-07-06, PR #78)

Prima PR di codice della roadmap prodotto. **Read-model only**, risponde a «cosa succede se premo
Avvia migrazione?». Consegnato:

- **`internal/webui/workbench_migration_plan.go`**: read-model puro `migrationPlan` +
  `buildMigrationPlan(f, scope)` che aggrega `artifactFacts` (via `readArtifactFacts`) e
  `contentScope`. 6 categorie (automatic / manual_verifiable / blocking_migration /
  blocking_cutover / informational / excluded). Fail-soft: senza checklist → `Ready=false` +
  messaggio umano.
- **`CanStartMigration`** = stesso oracolo di blocco di `nextAction` (`ApplyBlocked ||
  OverallStatus==NOT_READY`, = gate reale `isApplyBlockedByChecklist`) **più** «almeno un'area
  automatica in scope» (l'orchestratore Fase 3 esegue solo aree automatiche): può solo essere più
  conservativo, mai contraddire il blocco reale.
- **DNS sempre manuale/verificabile, MAI auto-runnable** (dns_apply resta avanzato/Danger Zone).
  Cron/EmailConfig automatici solo se il piano esiste (rischio safe/automatico non finto risolto).
  Blocker di aree escluse mostrati a parte (`ExcludedBlockers`), mai nascosti (gate globale).
- **UI**: schermata «Cosa verrà migrato» (`screen_migrazione`) arricchita col blocco «Piano
  migrazione»; CTA one-click **disabilitata** («Avvia migrazione — disponibile nella Fase 3»).
- **Nessun** nuovo writer/CLI, **nessun** `migration_plan.json` persistente (deferred), `/exec` +
  strong-confirmation immutati, `contentScope` non reso gate server-side.
- Test: 10 unit + 2 render HTML. Gate: gofmt/vet puliti, go test verde, race verde, Docker
  LINUX_ALL_GREEN. Review: go-reviewer R1 REQUEST CHANGES (oracolo `CanStartMigration`;
  `applyBlockers` non scope-aware; dead code) → fix → **R2 APPROVE**.

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
