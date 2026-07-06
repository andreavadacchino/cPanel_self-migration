# Dogfooding 4 — Smart Migration Orchestrator Walkthrough

> Data: 2026-07-07 · Metodo: **UI-walk in browser reale (Chrome)** su server locale
> loopback + **verifica di codice/test** + **una invocazione reale dell'orchestratore**
> osservata end-to-end. **Nessuna scrittura su alcun server cPanel.**
>
> ⚠️ **DOGFOODING NON COMPLETO sull'asse "apply reale"**: non è stata eseguita una
> migrazione reale verso un account cPanel. L'unica esecuzione reale dell'orchestratore
> è **fallita istantaneamente al caricamento della configurazione** (dir isolata senza
> `host.yaml`), *prima* di qualunque connessione SSH — quindi ha esercitato davvero il
> percorso di **fallimento parziale** senza contattare nessun server. Vedi §2.

---

## 1. Obiettivo

Verificare se il flusso piattaforma costruito nelle Fasi 1–3 è davvero usabile e
coerente col piano Opus:

```
Nuova migrazione → preflight → piano migrazione → conferma scope
→ Avvia migrazione → stato parziale/finale → cosa resta manuale
```

Focus prodotto: *«Simple for the operator, auditable for the tool.»* Un operatore
anche non super-tecnico deve poter creare una migrazione, capire cosa verrà migrato,
confermare lo scope, premere **un solo bottone**, capire cosa è successo e cosa resta
manuale. Non implementare nulla: solo dogfooding.

---

## 2. Ambiente usato

| Voce | Valore |
|------|--------|
| **Sorgente (dichiarata nel wizard)** | `giorginisposi@192.168.1.193:22` — IP privato non instradabile, **mai contattato** |
| **Destinazione (dichiarata nel wizard)** | `giorginisposi@192.168.1.78:22` — **mai contattata** |
| **Account** | `giorginisposi` (nome sessione: `dogfood-walk`) |
| **Store sessioni** | `CPANEL_MIGRATION_HOME=<scratchpad>/csm-store` — **isolato**, creato vuoto per la walk |
| **Artifact dir (`--dir`)** | `<scratchpad>/csm-dir` — seminata con i **soli artifact read-model** copiati da `dogfood_giorginisposi/`: `inventory_source/destination/diff`, `policy_report`, `migration_checklist`. **Niente `host.yaml`, niente `*_apply_report`, niente backup.** |
| **Binary** | build da `main @ a0bfa9c` (`0.0.0-20260706220218-a0bfa9c129b3`) |
| **UI** | `cpanel-self-migration ui --dir <csm-dir> --listen 127.0.0.1:8477` (loopback) |

### Cosa è stato realmente eseguito
- ✅ `go build ./cmd/cpanel-self-migration` → OK.
- ✅ `go vet ./internal/webui/` → OK.
- ✅ Suite di test mirata (orchestratore + piano + scope): **43/43 PASS** (§4.1).
- ✅ Walk in **browser reale**: wizard → panoramica → piano migrazione → conferma
  scope → CTA «Avvia migrazione» → **run reale dell'orchestratore** (§3, §4.2).
- ✅ Osservata la **fase Contenuti fallita** al config-load + flash + badge + job journal
  su disco (§4.2, §4.3).

### Cosa NON è stato eseguito (onestà)
- ❌ **Nessuna migrazione reale** verso un account cPanel. Il server sorgente `.193`
  (produzione) e il sacrificale `.78` **non sono stati contattati**.
- ❌ Nessun `--apply` reale, nessun `dns apply`, nessun cutover, nessuno switch DNS.
- ❌ Non ho osservato una migrazione **lunga in corso**: la sola esecuzione reale è
  fallita in ~20 ms al caricamento config. Quindi il giudizio su «meta-refresh basta /
  serve SSE» è **parziale e dichiarato tale** (§9).
- ❌ Le fasi `email_config` e `cron` non sono state esercitate come fasi automatiche
  (i loro piani `email_apply_plan.json`/`cron_apply_plan.json` non erano nella dir
  seminata → classificate «Informativo», non «Automatico» — vedi §4.2, coerente).

Motivo del non-apply: (a) nessuna autorizzazione operativa esplicita per una scrittura
reale in questa sessione; (b) `.78` è **membro del cluster DNS di produzione** (rischio
documentato in `CUTOVER_RUNBOOK.md`); (c) il pattern dogfooding consolidato (#2 UI-only,
#3 browser read-only) è read-only/UI. Coerente con la Regola assoluta del prompt.

---

## 3. Percorso eseguito

1. **Wizard** (`/workbench/new`): nome, dominio, sorgente (IP/porta/account),
   destinazione, **«Cosa vuoi migrare?»** (5 checkbox: file, database, email/Maildir,
   config email, cron) + box **«DNS — area delicata»**. Selezionato *tutto il migrabile*,
   DNS **non** incluso. → sessione `mig_20260706_c02940da2ff0`.
2. **Panoramica**: badge `Bozza · Bloccante · DNS non incluso`; «PROSSIMA AZIONE:
   Configura le connessioni ed esegui il preflight»; «Stato per fase» a semafori;
   «Contenuti da migrare: File / Database / Email / Config. email / Cron — DNS non incluso».
3. **Cosa verrà migrato** (Piano migrazione): badge **«Pronto per migrare»**; tre
   sezioni — *Automatico* (File, Database, Email/Maildir), *Manuale/verificabile*
   (Config email + Cron come **Informativo**), *Escluso* (DNS «non incluso»).
4. **Conferma scope**: preset `Tutto il migrabile / Solo sito / Solo email / Solo file /
   Solo database / Personalizzata` + «Includi DNS come task manuale/verificabile».
   Confermato `all_safe` → badge **«Scope confermato»**, flash «Scope aggiornato».
5. **Avvia migrazione**: card con **una sola strong-confirmation** («digita il nome
   dell'account `dogfood-walk`») + copy che spiega stop-on-first-failure e nessun
   rollback. Eseguito con nome corretto.
6. **Risultato**: redirect `?migrate=partial`; badge → `Ultimo job fallito`; flash
   «Migrazione interrotta al primo errore. Le fasi già completate restano registrate.
   Nessun rollback automatico è stato eseguito.»

---

## 4. Evidenze raccolte

### 4.1 Test (comportamento orchestratore — evidenza riproducibile)

`go test ./internal/webui/ -run 'TestOrchestrator|TestMigrationPlan|TestConfirmScope|TestScope' -v` → **43/43 PASS**. I test codificano *esattamente* gli scenari di prodotto richiesti:

| Scenario prodotto | Test | Esito |
|-------------------|------|-------|
| Solo sito → solo `migrate_content --file --db` (no `--mail`) | `TestOrchestratorSiteScopeContentOnly` | PASS |
| Solo email → `migrate_content --mail` + `email_apply` + `email_verify --fail-on-drift` | `TestOrchestratorEmailScopeRunsApplyVerify` | PASS |
| Cron in scope → `cron_apply` + `cron_verify --fail-on-drift` | `TestOrchestratorCronScopeRunsApplyVerify` | PASS |
| Cron senza piano → **non eseguito** | `TestOrchestratorCronWithoutPlanNotRun` | PASS |
| **DNS mai in auto-run** (anche con IncludeDNS) | `TestOrchestratorNeverRunsDNS` | PASS |
| DNS-only → rifiutato (`no_auto`) | `TestOrchestratorRefusesDNSOnly` | PASS |
| Scope non confermato → rifiutato | `TestOrchestratorRefusesUnconfirmedScope` | PASS |
| Checklist bloccante → rifiutato | `TestOrchestratorRefusesBlockedChecklist` | PASS |
| **Una sola conferma** esegue tutte le fasi | `TestOrchestratorSingleConfirmationRunsAllPhases` | PASS |
| Conferma errata → 403 | `TestOrchestratorWrongConfirmation` | PASS |
| CSRF obbligatorio | `TestOrchestratorRequiresCSRF` | PASS |
| **Stop-on-first-failure** (apply / verify) | `TestOrchestratorStopsOnApplyFailure` / `...OnVerifyFailure` | PASS |
| **Gate checklist ricontrollato per fase** | `TestOrchestratorGateReCheckedPerPhase` | PASS |
| Slot single-writer occupato → 409 | `TestOrchestratorBusySlot409` | PASS |
| Timeline registrata | `TestOrchestratorTimelineRecorded` | PASS |
| CTA attiva solo se ready + scope confermato | `TestOrchestratorUIShowsStartButtonWhenReady` / `...HidesStartButtonWhenUnconfirmed` | PASS |
| **Stato parziale in UI** | `TestOrchestratorUIShowsPartialState` | PASS |

### 4.2 Stati UI (browser reale) — schermate osservate

- **Wizard §4 «Cosa vuoi migrare?»** — *«Nessuna opzione «migra tutto»: seleziona solo
  ciò che ti serve.»* + box rosso **«DNS — area delicata: … modificarlo può raggiungere
  i nameserver di produzione … Non è mai incluso automaticamente in una migrazione di
  contenuti. Attivalo solo se sai di volerlo gestire da qui.»**
- **Piano migrazione** — File/Database/Email = badge verde **Automatico**;
  Config email/Cron = **Informativo** («Genera il piano email/cron nel preflight per
  classificare quest'area»); DNS = **Escluso dallo scope** («non incluso in questa
  migrazione»). Coerente col codice: `email_config`/`cron` diventano «Automatico» solo
  se `f.Email.PlanPresent`/`f.Cron.PlanPresent` (assenti nella dir seminata).
- **Conferma scope** → badge **«Scope confermato»**; scope resta editabile
  (`canEditScope` true perché nessun `*_apply_report` presente).
- **Avvia migrazione** — copy: *«Avvieremo automaticamente le aree selezionate e sicure,
  una fase dopo l'altra. Il DNS non verrà modificato automaticamente. La migrazione si
  fermerà al primo errore … Non verrà eseguito alcun rollback automatico.»*
- **Post-run** — badge `Ultimo job fallito`; flash `migrate=partial`.

### 4.3 Job journal / timeline (artifact reali su disco)

`job.json` scritto dall'orchestratore:
```json
{ "action": "migrazione automatica", "state": "failed", "phase": "Contenuti",
  "error": "migrate content: exit status 1", "session_id": "mig_20260706_c02940da2ff0" }
```
Timeline sessione (`session.json`, status resta `draft`):
```
scope confermato: file, database, email, config email, cron
avvio migrazione: content=failed [interrotta]
```
Coerenza perfetta: è stata costruita **una sola** fase automatica (Contenuti — perché
email/cron erano «Informativo»), fallita al primo step, stop-on-first-failure, nessun
rollback, fasi successive `not_run`.

### 4.4 Report / artifact
Nessun report di apply è stato generato (la fase è fallita prima di produrre artifact).
Il fallimento reale osservato: `error: read config ".../host.yaml": no such file or
directory` — la conferma che lo step fallisce **prima di qualsiasi SSH**.

---

## 5. Cosa funziona

- **Wizard chiaro e non tecnico.** Linguaggio umano, rassicurazioni corrette («Il server
  di partenza viene solo letto: non viene mai modificato»), nessun bottone «migra tutto».
- **DNS spiegato benissimo** già nel wizard (box «area delicata») e nel piano (Escluso /
  Manuale verificabile). Mai trattato come blocker generico, mai in auto-run.
- **Piano migrazione onesto**: distingue Automatico / Informativo / Escluso e **non
  sovradichiara** (email/cron restano «Informativo» finché il piano non esiste).
- **Una sola strong-confirmation** per l'intera migrazione. La CTA appare solo quando il
  piano è pronto **e** lo scope è confermato (state-aware; altrimenti badge disabilitato
  con motivo).
- **Stato parziale leggibile**: badge `Ultimo job fallito` + flash umano + job journal
  con fase e errore. Nessun rollback silenzioso; il testo lo dichiara *prima* dell'avvio.
- **Gate server-side reali** (verificati dai test): scope confermato obbligatorio,
  `contentScope` come gate d'esecuzione, checklist ricontrollata per fase, DNS escluso,
  slot single-writer, CSRF.

---

## 6. Attriti UX / prodotto

1. **Dissonanza «prossima azione» vs «piano pronto».** In alto la Panoramica dice
   *«PROSSIMA AZIONE: esegui il preflight»*, mentre il piano dice *«Pronto per migrare»*
   e la CTA è attiva. Nella mia walk è in parte artefatto del seeding (checklist presente
   senza aver eseguito il preflight *in-sessione*), ma rivela che **readiness del piano e
   next-action della sessione derivano da fonti diverse e possono contraddirsi**. Da
   allineare (messaggistica), non un bug bloccante.
2. **Badge «Bloccante» in testa mentre la migrazione è avviabile.** La checklist
   giorginisposi è `OverallStatus=BLOCKED` ma `ApplyBlocked=false`: è **bloccante-cutover,
   non bloccante-migrazione**, quindi l'orchestratore parte legittimamente. Corretto nel
   modello, ma l'operatore vede «Bloccante» in alto e una CTA «Avvia migrazione» attiva:
   **potenziale confusione**. La distinzione (tassonomia roadmap §6) non è resa esplicita
   nel badge di testa.
3. **«Cosa resta manuale o verificabile» è ancora povero.** Oggi è testo informativo
   («genera il piano…»); non c'è ancora il confronto src/dst né «Verifica ora» (è la
   Fase 5). Per un cutover reale serve, ma **non** per completare la migrazione.
4. **Scroll lungo.** La schermata «Cosa verrà migrato» concentra piano + conferma scope +
   CTA + tabella coverage: molto scroll. Accettabile, ma la CTA è in fondo a una pagina lunga.

---

## 7. Bug o rischi reali

- **Nessun bug bloccante** trovato nell'orchestratore. Comportamento allineato al codice
  e ai test; il fallimento parziale reale osservato è quello atteso.
- **Rischio residuo dichiarato:** non avendo eseguito una migrazione **lunga reale**, non
  ho evidenza diretta del comportamento del monitor durante una fase contenuti da minuti
  (è esattamente il buco che la Fase 4 deve colmare — §9).
- Gli attriti §6.1 e §6.2 sono **friction di messaggistica**, non richiedono un bugfix
  PR separato: vanno indirizzati nella Fase 4/6 (allineare readiness ↔ next-action e
  rendere esplicito «bloccante-cutover ≠ bloccante-migrazione»).

---

## 8. DNS e task manuali

- **Il DNS è chiaro?** Sì. È spiegato nel wizard (box «area delicata»), nel piano
  (Escluso o Manuale verificabile) e nella CTA («Il DNS non verrà modificato
  automaticamente»).
- **Trattato come manuale/verificabile?** Sì come **classificazione**; **no** ancora come
  **task comparativo operativo** (src vs dst, valore copiabile, «Verifica ora»): quello è
  Fase 5, non presente.
- **Cosa manca:** il track DNS comparativo (5 categorie, src/dst, «Verifica ora») e gli
  altri task manuali strutturali (filtri multi-regola, db-config CMS). Nessuno di questi
  blocca la migrazione automatica; servono per la **chiusura/cutover**.

---

## 9. Progress / monitor

- **Meta-refresh basta?** Non dimostrabile qui: la mia unica esecuzione reale è durata
  ~20 ms (fallimento al config-load), quindi **non ho visto una fase lunga in corso**.
- **Job journal basta?** Come *stato* (running/failed + fase + errore) sì: è scritto e
  leggibile (§4.3). Come *progresso* di una fase contenuti lunga, **no**: mostra solo
  l'etichetta di fase, senza avanzamento per-item.
- **Serve SSE?** **Non ancora deciso onestamente.** La roadmap (§5, §11) rimanda la
  decisione SSE a «dopo dogfooding reale su migrazione lunga» — e questo dogfooding **non**
  l'ha fornita. Il minimo (meta-refresh 2s + job journal + `events.jsonl` per
  `migrate_content`) è ragionevole come MVP; SSE resta un enhancement, non un requisito.
- **Cosa manca prima della Fase 4:** poter **osservare una migrazione reale in corso**.
  Il monitor d'esecuzione è il prerequisito per eseguire in sicurezza uno **Scenario A**
  (apply reale su account sacrificale) e giudicare davvero meta-refresh vs SSE.

---

## 10. Stato finale percepito

- **Completata?** L'operatore capisce **se una migrazione automatica è completata** (flash
  «Migrazione automatica completata» / `done`), non testato dal vivo ma reso dai test e dal
  codice `migrateFlash`.
- **Parziale?** Sì, chiarissimo: badge `Ultimo job fallito` + flash «interrotta al primo
  errore … nessun rollback» + job journal con fase/errore. **Verificato dal vivo.**
- **Cosa resta manuale?** Parzialmente: il piano dice cosa è Escluso/Manuale, ma il
  **dettaglio operativo** dei task manuali (DNS comparativo, «Verifica ora») non c'è
  ancora (Fase 5).

---

## 11. Decisione prossima fase

### Opzione A — Fase 4 Progress + Execution Monitor  ✅ **SCELTA**
Motivo: il singolo buco non colmabile di questo dogfooding è stato *«cosa succede
durante una migrazione reale in corso»*. Non ho potuto vedere una fase lunga perché non
c'è stato un apply reale, e la roadmap stessa **subordina la decisione SSE a questa
osservazione**. Il monitor d'esecuzione è il prerequisito per poi fare in sicurezza uno
Scenario A (apply reale su sacrificale) e osservarlo. La Fase 4 sblocca il prossimo
dogfooding *vero*.

### Opzione B — Anticipare Fase 5 Manual Tasks comparativi
Motivo (scartato): i task manuali/DNS comparativi servono per la **chiusura/cutover**,
non per completare la migrazione automatica. Anticiparla ottimizza una fase che l'operatore
raggiunge *dopo* aver eseguito e monitorato la migrazione. Prima serve poter osservare
l'esecuzione.

### Opzione C — Bugfix prima di nuove fasi
Motivo (scartato): nessun bug bloccante. Gli attriti §6.1/§6.2 sono messaggistica e vanno
indirizzati **dentro** la Fase 4/6, non in un bugfix PR a sé.

---

## 12. Verdetto

## 🔵 Buono ma serve Fase 4

L'orchestratore Fase 3 è **solido, ben testato e usabile** anche per un operatore non
super-tecnico: wizard chiaro, DNS spiegato e mai in auto-run, una sola conferma forte,
stato parziale leggibile, gate server-side reali. **Non è pronto a dichiararsi
production-trusted** finché non si può **osservare una migrazione reale in corso**: manca
il Progress + Execution Monitor (Fase 4), prerequisito per un apply reale su account
sacrificale (Scenario A) da fare nel prossimo dogfooding.

---

### Appendice — Risposte brutali alle 15 domande

1. *Dove iniziare?* Sì — «+ Nuova migrazione guidata» + «PROSSIMA AZIONE» sempre visibile.
2. *Wizard chiaro o tecnico?* **Chiaro**, linguaggio umano, niente «migra tutto».
3. *Preflight → fotografia comprensibile?* Non ri-testato qui (usati artifact esistenti);
   il piano che ne deriva è comprensibile.
4. *Il piano risponde a «cosa succede se premo Avvia»?* **Sì** (Automatico/Informativo/Escluso + StartSummary).
5. *Scope dopo preflight chiaro?* **Sì**, badge «Scope confermato».
6. *Preset comprensibili?* **Sì** (Tutto il migrabile / Solo sito / Solo email / … / Personalizzata).
7. *DNS escluso spiegato?* **Sì, molto bene** (wizard «area delicata» + piano + CTA).
8. *Bottone «Avvia» al momento giusto?* **Sì**, solo con piano pronto + scope confermato.
9. *Una strong-confirmation basta?* **Sì**, e il copy dichiara stop-on-fail + no rollback.
10. *Durante la migrazione si capisce?* **Non dimostrato** (nessun run lungo) — buco Fase 4.
11. *Meta-refresh/job journal sufficiente?* Per *stato* sì; per *progresso di fase lunga* no.
12. *Serve SSE ora?* **Non deciso onestamente**: manca il dogfooding su migrazione lunga.
13. *Fallimento → stato parziale chiaro?* **Sì, verificato dal vivo** (badge + flash + journal).
14. *Successo → cosa resta manuale?* Parziale: piano sì, dettaglio task manuali (Fase 5) no.
15. *Problema più urgente: Progress o Manual Tasks?* **Progress (Fase 4).**
