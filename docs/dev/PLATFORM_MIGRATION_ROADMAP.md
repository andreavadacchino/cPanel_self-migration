# Platform Migration Roadmap

> **Status:** product direction + development plan — 2026-07-06
> **Tipo:** docs-only. Nessun codice operativo in questa PR.
> **Scopo:** trasformare `cpanel-self-migration` da workbench tecnico a
> **piattaforma smart di migrazione** per operatori anche non super-tecnici,
> senza rimuovere controlli e senza regressioni sul motore live-proven.

Questo documento è basato sull'ispezione della codebase reale (non su assunzioni).
Ogni affermazione sullo stato attuale cita `file:funzione` verificabili.

---

## 1. Visione prodotto

La piattaforma deve sembrare un'applicazione moderna del 2026: wizard chiaro,
stati comprensibili, una sola azione principale quando possibile, progress reali,
errori spiegati in linguaggio umano, task manuali guidati, verifiche automatiche,
report finale chiaro.

Non deve sembrare: una console tecnica, una lista di artifact, una collezione di
bottoni apply/verify, un pannello da sistemista che costringe a conoscere i
dettagli interni.

Principio guida (già radicato nel codice, `FRONTEND_FLIGHT_DIRECTOR_ROADMAP.md`):

> **Simple for the operator, auditable for the tool.**

Il rischio numero uno non è la confusione, è la **false confidence**: "verde" deve
significare *migrazione sicura secondo evidenze fresche*, non "l'operatore ha
cliccato conferme". Questa invariante è già difesa dal codice
(`buildRiskBadge` in `workbench_flightdirector.go`, verdetti da artifact e non da
status forzabile — provato in `DOGFOODING_3_UX_WALK.md`) e va preservata.

---

## 2. Principi non negoziabili

1. **Non rimuovere controlli — automatizzarli.** La sicurezza si sposta da
   "approvazioni manuali ripetitive" a "controlli automatici + piano chiaro +
   una conferma consapevole".
2. **Una conferma forte, non N.** Oggi ogni scrittura richiede di ridigitare il
   nome account. La piattaforma punta a una sola strong-confirmation dopo che
   l'operatore ha visto il piano.
3. **Nessuna regressione.** I writer esistenti (DNS/email/cron/file/db/mailbox)
   sono live-proven: non vanno riscritti né rimossi. `internal/migrate/runner.go`
   resta off-limits salvo necessità motivata.
4. **Nessun nuovo writer rischioso in questa fase.** L'orchestratore compone
   writer esistenti; non introduce nuove primitive di scrittura.
5. **Evidence over clicks.** Ogni stato "pronto/verde" deriva da artifact freschi
   su disco, mai da uno status mutabile a mano.
6. **DNS non è un blocker generico.** È discoverable, classificabile e verificabile
   nel flusso primario; l'apply DNS resta azione avanzata.
7. **Single-account.** Nessun Campaign Mode, nessuna queue, nessuna migrazione
   parallela (non-goal permanenti, ribaditi in `HANDOFF_NEXT_SESSION.md`).

---

## 3. Flusso operatore desiderato

```
1. Nuova migrazione     sorgente · destinazione · account · scope
2. Pre-analisi          fotografia src+dst · cosa è migrabile · blocchi · task manuali
3. Piano migrazione     automatico / manuale verificabile / bloccante / escluso
4. Scelta scope         completa / email / file / database / sito / personalizzata
5. Avvia migrazione     UNA conferma forte → esecuzione automatica aree safe in-scope
6. Task manuali         src vs dst · valori copiabili · "Verifica ora" · fallback manuale
7. Comparativa finale   presente su src · presente su dst · uguale / diverso / manuale
8. Stato finale         completata / completata con task aperti / non pronta / pronta cutover
```

Il DNS attraversa i passi 2, 6 e 7 come **track manuale verificabile**, mai come
passo automatico del punto 5.

---

## 4. Stato attuale della codebase (brutalmente onesto)

### 4.1 Il motore è maturo e LIVE-PROVEN

L'assunto "il motore è granulare/incompleto" è **falso**. Due motori distinti:

- **Motore config** (`internal/accountinventory`): inventory + diff + policy +
  checklist + plan/apply/verify/rollback per DNS, email, cron.
- **Motore contenuto** (`internal/migrate` + `dbmig`, `webfiles`, `maildir`):
  mail, file, database via SSH. Orchestratore `runApply` (`migrate/apply.go:97`).

Tutti i writer sono implementati con UAPI/SSH reali e provati: apply reale 14/14
fasi su giorginisposi (`.193→.78`, `FASE0_2_FIRST_APPLY.md`), tutti i writer
config byte-verificati sul sacrificale `.78`, prima rollback live in PR #49.

| Dominio | Fotografa | Scrive su dest | Primitiva reale | Rischio |
|---------|-----------|----------------|-----------------|---------|
| Inventory / diff / policy / checklist | ✅ | ❌ mai | — | Nullo |
| File (webfiles) | ✅ | ✅ | tar-stream SSH + `emptyDest` guard | Alto |
| Database (dbmig) | ✅ | ✅ | `create_user`/`create_database` + **GRANT** + mysqldump | Alto |
| Mailbox (maildir) | ✅ | ✅ | tar-stream SSH + `EnsureAccount` | Medio (Alto con `--mirror`) |
| Email config | ✅ | ✅ | `Email::add_forwarder/set_default_address/...` | Medio-alto |
| Cron | ✅ | ✅ | SSH `crontab -` (whole replace) | Medio |
| DNS | ✅ | ✅ | `DNS::mass_edit_zone` + serial guard | Alto |

Pattern di sicurezza trasversale (tutti i domini config): **plan offline → backup
obbligatorio ("no backup ⇒ no write") → apply con guard freschezza/serial/sha256 →
verify-after per-op → rollback simmetrico fail-closed** (inverte solo le proprie op
`applied`, mai le `already_present`).

### 4.2 Cosa è cablato vs presentation-only

**Cablato al backend** (scrive/esegue):
- `POST /workbench/session/<id>/exec` → `handleExec` (`workbench_exec.go:290`)
  lancia subprocess del binario stesso, allega artifact, transiziona status.
- `POST /config`, `POST /run`, `POST /accept`, `/status`, `/attach`.
- Auto-transizione a `ready_for_cutover` (`tryAutoTransitionReadyForCutover`,
  `workbench_exec.go:479`) solo se i 3 verify report sono `clean:true`.

**Presentation-only** (traduzione di fatti on-disk, nessuna logica operativa):
- Tutto `workbench_view.go` e `workbench_flightdirector.go`: risk badge, timeline,
  phases, coverage, nextAction, cutoverReadiness.
- PR #66/#67/#68/#73/#75 (7 schermate, IT, design system, next-actions scope-aware,
  Flight Director shell). Marcate esplicitamente "SOLO presentazione" nel ledger.
- `contentScope` (`workbench_view.go:412-445`): governa solo cosa mostrare,
  **non gatea l'exec** (commento `workbench_view.go:409-411`).
- Attestazione checkbox DNS standalone (`workbench_screens.html:168`): è UI, non un
  controllo applicato dal tool.

### 4.3 Le 14 action lanciabili (registry)

`actionRegistry` (`workbench_exec.go:45`) — le UNICHE operazioni lanciabili dal
workbench, ognuna singolarmente:

- **write (strong-confirm):** `migrate_content`, `email_apply`, `cron_apply`, `dns_apply`
- **rollback (double-confirm):** `dns_rollback`, `email_rollback`, `cron_rollback`
- **verify:** `dns_verify`, `email_verify`, `cron_verify`
- **plan/read-only:** `dns_plan`, `email_plan`, `cron_plan`, `run_pipeline` (pipeline)

`inventory_source/destination/diff`, `policy_report`, `migration_checklist` **non
sono action** — sono `ArtifactKind` (`workbench/types.go:100-118`) prodotti come step
interni di `run_pipeline` (`pipelineSteps`), non lanciabili singolarmente.

---

## 5. Gap principali

1. **Orchestrazione assente.** Non esiste un orchestratore che concatena scritture.
   Ogni write è lanciata individualmente via `/exec` con la propria
   strong-confirmation (`handleExec`, gate `workbench_exec.go:305-322`). Solo
   `run_pipeline` concatena step, ma è read-only (`writeOp:false`) → **è il template
   riusabile** per l'orchestratore (loop tollerante su `pipelineSteps`,
   `workbench_exec.go:360-379`).
2. **Contratto Plan → Scope → Execution mancante.** I dati esistono sparsi
   (artifactFacts, checklist, policy_report, coverage, contentScope) ma non c'è un
   read-model unico che risponda "cosa succederà se premo Avvia".
3. **`contentScope` non-gating.** È solo presentazione; il gate reale resta la
   strong-confirmation per-account + `isApplyBlockedByChecklist`.
4. **Nessun delta engine incrementale.** "Final sync" non esiste come
   riconciliazione: sarebbe re-run dell'apply + freeze/maintenance window + warning
   di staleness (vincolo no-new-writer, `FRONTEND_FLIGHT_DIRECTOR_ROADMAP.md` §10).
5. **SSE rimandata.** L'exec è sincrono con meta-refresh 2s (`JobLive` →
   `<meta http-equiv="refresh">`). SSE va decisa solo dopo dogfooding reale su
   migrazione lunga.
6. **Affordance UI cluster-DNS-standalone mancante.** Il gate "peer cluster in
   standalone" oggi è verificato out-of-band con `dig` (dogfooding #2, N2); manca
   l'esposizione in UI.
7. **Nessun preflight destinazione standalone.** La fotografia dei due lati è uno
   step di `run_pipeline` (via `inventory`), non un comando `preflight` dedicato.
   Da valutare in #76/#77 (vedi §14).

---

## 6. Tassonomia degli stati

Ogni area del piano deve poter dichiarare uno stato, derivato da artifact reali:

| Stato | Significato | Fonte reale che lo determina |
|-------|-------------|------------------------------|
| **Automatico** | Eseguibile dall'orchestratore one-click | area in-scope (`contentScope`) + writer esistente + classificata safe |
| **Manuale verificabile** | Task con src/dst, valore copiabile, "Verifica ora" | `ManualAction` in `migration_checklist.json` + DNS classification |
| **Bloccante migrazione** | Impedisce l'apply finché non risolto | `Checklist.ApplyBlocked` / `BlockersApply` per sezione (`checklist.go:919`) |
| **Bloccante cutover** | Non blocca la migrazione, blocca il cutover | `ManualAction.BlockingCutover && !Accepted` (`pendingConfirmations`) |
| **Informativo** | Va mostrato, non blocca nulla | policy_report note, coverage |
| **Escluso dallo scope** | Area non selezionata nel wizard | `contentScope` = false per l'area |

La distinzione **bloccante-migrazione vs bloccante-cutover** è già nel modello
(`checklist_types.go`): va resa esplicita in UI, non inventata.

---

## 7. Migration Plan (PR #76)

### Decisione (bloccata)

> **The first Migration Plan is a read-only platform view derived from existing
> artifacts; it is not yet a new engine artifact. Persisting `migration_plan.json`
> is deferred until the Plan schema is product-validated.**

PR #76 è un **read-model di piattaforma**: aggregazione READ sopra artifact già
prodotti. **Nessun nuovo writer, nessun nuovo motore, nessun nuovo subcomando CLI.**
Riusa `readArtifactFacts` (`workbench_view.go:88`) e il pattern view-model.

### Input (artifact già su disco)

`inventory_source.json`, `inventory_destination.json`, `inventory_diff.json`,
`policy_report.json`, `migration_checklist.json`, `dns_import_plan.json`,
`email_apply_plan.json`, `cron_apply_plan.json`, `artifactFacts`/`areaFacts`,
`contentScope` / `Session.Setup.Content`, job journal (solo per stato esecuzione).

### Output (struct read-only, nomi indicativi)

```go
type MigrationPlan struct {
    Areas             []MigrationPlanArea   // una per area: file, db, email, ...
    Blockers          []MigrationPlanIssue  // bloccanti migrazione
    ManualTasks       []MigrationManualTask // DNS esterni, filtri multi-regola, CMS db-config
    Scope             contentScope
    CanStartMigration bool                  // false se blocker o scope vuoto
    StartSummary      string                // "cosa succederà se premo Avvia"
}
```

Ogni `MigrationPlanArea` dichiara uno stato della tassonomia §6.

### UI desiderata (schermata "Piano migrazione", dopo il preflight)

```
Automatico:            File · Database · Email/Maildir · Forwarder · Cron
Manuale verificabile:  DNS Google Workspace · TXT Microsoft · CNAME piattaforma esterna
Bloccante:             spazio insufficiente · credenziali errate · account dest mancante
Escluso:               aree non selezionate nello scope
```

PR #76 risponde alla domanda **"Cosa succederà se premo Avvia migrazione?"**.
NON implementa ancora il bottone one-click.

---

## 8. Scope Selection (PR #77)

Mappata su `workbench.ContentSelection` (`types.go:186`), già raccolta dal wizard:

| Scope | Files | Databases | Email (Maildir+account) | EmailConfig (fwd/autoresp/filtri/routing) | Cron | DNS |
|-------|:---:|:---:|:---:|:---:|:---:|:---:|
| Completa | ✅ | ✅ | ✅ | ✅ | ✅ | manuale* |
| Solo email | — | — | ✅ | ✅ | — | manuale* |
| Solo file | ✅ | — | — | — | — | — |
| Solo database | — | ✅ | — | — | — | — |
| Solo sito | ✅ | ✅ | — | — | — | — |
| Personalizzata | libera | libera | libera | libera | libera | manuale* |

\* DNS non entra mai nell'auto-run: se selezionato, alimenta il track manuale
verificabile, non l'orchestratore.

**Ogni scope parziale deve avere un finale coerente** (PR #81): "solo email
completata" è uno stato terminale legittimo, non "migrazione incompleta".

> Nota: `Email` e `EmailConfig` sono due flag distinti. `Email` = contenuto caselle
> (Maildir) + creazione account. `EmailConfig` = forwarder/autoresponder/filtri/
> routing. Lo scope "solo email" tipicamente include entrambi.

---

## 9. Smart Migration Orchestrator (PR #78)

### Decisione (bloccata) — Opzione 1

> **Una conferma forte, orchestrazione automatica delle aree safe in scope, DNS
> escluso dall'auto-run e gestito come task manuale verificabile.**

### Comportamento

Una sola strong-confirmation (`Digita il nome account per avviare la migrazione`)
avvia in sequenza tutte le aree **safe E in-scope**. L'orchestratore:

- riusa gli stessi gate esistenti (strong-confirm una volta + checklist non-blocked
  **prima di ogni write**, come `isApplyBlockedByChecklist`);
- esegue solo aree in-scope (`contentScope`) e classificate safe/automatiche;
- si ferma al primo errore reale (**stop-on-first-failure**), non prosegue;
- fa **verify automatico dopo ogni fase applicata**;
- produce **stato di migrazione parziale** se alcune fasi precedenti sono riuscite;
- genera **report unico**;
- registra **timeline / job / progress per fase**;
- **non include DNS** nell'auto-run.

### Ordine fasi (rispecchia `runApply`, `migrate/apply.go:97`)

```
crea domini → mail + verify → file + verify → db + verify
            → email-config + verify → cron + verify
```

### Aree ammesse nell'auto-run

file · database · email/Maildir · account email · forwarder · autoresponder ·
filtri · routing email (se classificato safe) · cron (se classificato safe).

### Fallback / retry / resume

Se una migrazione si ferma, la UI NON offre uno `Start` ambiguo, ma azioni
esplicite che spiegano cosa sovrascrivono/skippano/verificano:
**Resume · Retry failed · Re-run phase · Rollback · Archive**
(`FRONTEND_FLIGHT_DIRECTOR_ROADMAP.md` §11).

### Riuso architetturale

L'orchestratore è l'estensione naturale di `run_pipeline`: stesso loop tollerante
su una lista di step (`pipelineSteps`, `workbench_exec.go:360`), ma per **write
actions** invece che read-only, con gate per-fase e verify inline. Non è un
rewrite: compone `migrate_content`/`email_apply`/`cron_apply` esistenti.

---

## 10. Manual Tasks comparativi (PR #80)

Riusa il modello acceptance esistente: `ManualAction` (`checklist_types.go:134`) con
chiave stabile `AK-<12hex>` = sha256(type\0section\0title\0detail), persistita in
`acceptances.json` legata al `ChecklistSHA256` (`accept.go`, `saveAcceptTo`).

### Stati task (già nel modello, da esporre)

To do · Auto-verified · Done non verificabile · Ignored with reason · N/A · Blocking.

### DNS come track manuale verificabile

> **DNS is discoverable, classifiable and verifiable in the primary platform flow;
> DNS apply remains an advanced operator action, not part of the one-click
> migration path.**

Classificazione DNS in 5 categorie (dal piano DNS + inventory):

1. **Standard cPanel** — record normali generati/attesi da cPanel.
2. **Provider email esterni** — MX/TXT/CNAME di Google Workspace, Microsoft 365,
   antispam, ecc.
3. **Verifiche piattaforme esterne** — TXT/CNAME di verifica dominio, SaaS, CDN,
   marketing.
4. **Record custom / non riconosciuti** — mostrati come task manuale.
5. **Differenze src/dst** — presente su sorgente ma assente/diverso su destinazione.

UI per ogni task DNS:

```
Record MX Google Workspace
Sorgente:      giorginisposi.it MX aspmx.l.google.com
Destinazione:  mancante / diverso / presente
[ Copia valore ]  [ Verifica ora ]  [ Segna come gestito manualmente ]
```

`dns_apply` resta accessibile solo in **Dettagli avanzati / DNS Danger Zone** con
copy chiaro: *"Azione avanzata per operatori esperti. Non necessaria per completare
la migrazione automatica. Non esegue lo switch DNS."* Il writer non va rimosso
(nessuna regressione).

### Altri task manuali strutturali (già noti dal motore)

- **Filtri email multi-regola** → MANUAL: l'API cPanel non round-trippa il
  `match_type` AND/OR (`accountinventory/types.go:251-254`).
- **CMS db-config non coperti** → task manuale: Magento 1 (`local.xml`),
  PrestaShop 1.7 (`parameters.php`), Symfony (`DATABASE_URL`), SilverStripe
  (`dbConfigUnmigrated`, `migrate/apply.go:280`).

---

## 11. UX moderna richiesta

- **Progress per fase** — dal job journal (`job.json`, phase-level) + `events.jsonl`
  per il solo `migrate_content` (item-level via `loadRunMonitor`). Mai mostrare
  precisione item-level per fasi che hanno solo verità phase-level ("fake
  precision", `FRONTEND_FLIGHT_DIRECTOR_ROADMAP.md` §7).
- **Loading / job state** — meta-refresh 2s oggi (`JobLive`); SSE solo se il
  dogfooding lo giustifica (§5, gap 5).
- **Errori in linguaggio umano** — es. `busyMessage` già trasforma il 409 opaco in
  "azione X in corso dalle HH:MM". Estendere il pattern agli errori di fase.
- **Risk badge** — esiste (`buildRiskBadge`, `workbench_flightdirector.go:31`):
  mai verde senza evidenza fresca.
- **Report finale** — unico, leggibile, con `host.yaml` escluso da ogni bundle.

---

## 12. Roadmap PR proposta

| PR | Titolo | Contenuto | Nuovo codice motore? |
|----|--------|-----------|----------------------|
| **#76** | Platform Migration Plan / Readiness | Struct `MigrationPlan` read-only + screen "Piano migrazione" (aggregazione artifact). Include DNS classification, non `dns_apply` primario. Risponde "cosa succede se premo Avvia". | No (read-model) |
| **#77** | Scope Confirmation after Preflight | Schermata conferma scope dopo preflight, usa il Migration Plan read-only. DNS come manual task track, non auto-run. | No |
| **#78** | Smart Migration Orchestrator | Orchestratore server-side: 1 conferma → aree safe in-scope in sequenza, verify per fase, stop-on-fail, stato parziale. Riusa `pipelineSteps` + gate. DNS escluso. | Sì (orchestrazione, non nuovi writer) |
| **#79** | Progress + Execution Monitor | Progress per fase, job state, monitor esecuzione. (SSE solo se dogfooding lo giustifica) | Minimo |
| **#80** | Comparative Manual Tasks | Task DNS src/dst, copia valore, "Verifica ora", stati task. Riusa acceptance model. | No/minimo |
| **#81** | Final Verification / Migration Completion | Verifica finale + tassonomia stato finale (completata / con task aperti / non pronta / pronta cutover). Finale coerente per scope parziali. | No/minimo |
| **#82** | Report finale / Archive | Report unico + archive (`host.yaml` escluso da ogni bundle). | Minimo |
| _futura_ | Persist `migration_plan.json` | Solo dopo stabilizzazione schema, come contratto per orchestrator/report/audit/resume. | Sì (deferred) |

Roadmap **incrementale, non mega-refactor**. Ogni PR è piccola, gateabile e
verificabile in isolamento.

---

## 13. Fuori scope esplicito

**In questa PR (docs-only):** nessuna implementazione di orchestratore, SSE,
Migration Plan, screen nuove.

**Rimandato / non-goal per questa fase:**
- Orchestratore one-click implementato adesso (è PR #78, solo progettato qui).
- SSE (rimandata a dopo dogfooding reale).
- Persistenza `migration_plan.json` (deferred, vedi §7).
- Switch DNS automatico / cutover DNS automatico.
- Delta engine incrementale / riconciliazione silenziosa.

**Non-goal permanenti (`HANDOFF_NEXT_SESSION.md`):** Campaign Mode, multi-account
queue, migrazioni parallele, operazioni root/WHM, clone WHM Transfer Tool, bottone
cieco "migra tutto", spegnimento vecchio server verde senza osservazione.

---

## 14. Rischi aperti (onestà residua)

1. **Preflight destinazione standalone.** Oggi non esiste un comando `preflight`:
   la fotografia dei due lati è uno step di `run_pipeline` (via `inventory`, il cui
   `Collect` fotografa entrambi i lati read-only). Se il flusso prodotto richiede un
   preflight destinazione *prima e separato* dall'apply, va valutato in #76/#77 se
   la pipeline read-only esistente basta o serve un'affordance dedicata. **Non
   risolto in questa sessione docs-only.**
2. **Classificazione "safe/automatica" per area.** Quali condizioni rendono
   `routing email` o `cron` eligible per l'auto-run è un giudizio di prodotto da
   codificare in #76 (definizione dei criteri) ed enforced in #78. Rischio:
   classificare safe qualcosa che in un caso limite non lo è. Mitigazione: la
   freshness guard per-op del motore config (`EvaluateEmailOp`, `CronApplyBackup`
   sha256) fallisce comunque closed.
3. **Una-conferma vs gate per-write.** Collassare N conferme in 1 sposta la
   responsabilità del gate dentro l'orchestratore: DEVE ri-verificare la checklist
   prima di *ogni* fase (non solo all'inizio), altrimenti si indebolisce la
   protezione odierna. Requisito hard per #78.
4. **Affordance cluster-DNS-standalone** ancora assente in UI (§5, gap 6): non
   blocca questa roadmap ma va nel track DNS manuale (#80).

---

## 15. Prossima PR consigliata

**PR #76 — Platform Migration Plan / Readiness.**

Costruisce il contratto dati/UI (read-only) che risponde a "cosa succederà premendo
Avvia migrazione": cosa è automatico, manuale verificabile, bloccante, escluso,
in scope. È il prerequisito di scope-confirmation (#77) e orchestratore (#78), e
non tocca alcun writer.

---

## Appendice — Review adversariale del piano

| Domanda critica | Risposta | Come |
|-----------------|----------|------|
| Riduce davvero i passaggi operatore? | Sì | N conferme → 1 (Opzione 1, §9) |
| I controlli restano, ma automatizzati? | Sì | Gate checklist per-fase dentro l'orchestratore (§9, §14.3) |
| DNS non è trattato come falso blocker? | Sì | Track manuale verificabile, mai auto-run (§10) |
| I task manuali sono verificabili, non solo "confermati"? | Sì | "Verifica ora" + acceptance model (§10) |
| Il one-click non è "vai e spera"? | Sì | Verify per fase + stop-on-fail + stato parziale (§9) |
| Lo scope parziale è supportato? | Sì | `ContentSelection`, finale coerente per scope (§8, §12 #81) |
| Migrazione solo email/file/db ha finale coerente? | Sì | Stato terminale legittimo (#81) |
| Non stiamo costruendo Campaign Mode prematuramente? | Sì (evitato) | Single-account ribadito (§2, §13) |
| Non stiamo aggiungendo SSE solo per estetica? | Sì (evitato) | Rimandata a dogfooding (§5, §11) |
| Il piano è implementabile a PR piccole? | Sì | #76 read-only, incrementali (§12) |
