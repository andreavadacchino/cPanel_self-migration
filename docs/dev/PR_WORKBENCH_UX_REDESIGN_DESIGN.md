# Design — Workbench UX Redesign v1: da dashboard tecnica a percorso guidato

Stato: **BOZZA per riesame** · Scope: **SOLO UX/presentazione** · Base: #57/#58/#59/#61/#62

## 0. Principio di non-regressione (vincolante)

NESSUNA modifica a: `internal/workbench` (state machine, status.go, types.go, store.go),
`internal/accountinventory` (checklist, coverage, policy, formati artifact),
`internal/migrate` (runner off-limits), writer, collector, API.
Gli enum tecnici (`Status`, `Step`, `ArtifactKind`, `OverallStatus`, `CoverageState`,
manual-action types) restano **byte-identici**. La traduzione IT e il raggruppamento
avvengono SOLO nel layer `internal/webui` (template + view-model read-only).

Fatto architetturale accertato (lettura del codice, non assunzione):
la webui è **single-account**: dashboard `/` e workbench `/workbench/session/<id>`
**condividono la stessa artifact dir** `o.Dir` (`s.dir` == `ws.dir`). Gli artifact
reali (`inventory_*.json`, `migration_checklist.json`, `acceptances.json`,
`*_verify_report.json`, `host.yaml`) vivono in `o.Dir`. `session.ArtifactDir`
contiene solo `session.json` + registry dei path (che puntano a `o.Dir`).
→ Le schermate leggono gli artifact da `o.Dir` esattamente come fa già la dashboard
(`isApplyBlockedByChecklist`, `isVerifyClean`, `index`).

## 1. Architettura — navigazione a 7 schermate (additiva, non-breaking)

### 1.1 Routing
Oggi `routeWorkbench` gestisce: `GET /workbench/session/<id>` (detail),
`POST .../status|attach|exec`. Il `default` di azioni sconosciute è `404`.

Aggiungo **GET sub-view** come nuovi segmenti d'azione (additivi, nessuna rotta
esistente cambia):

| Rotta | Schermata |
|-------|-----------|
| `GET /workbench/session/<id>` (invariata) | **1 · Panoramica** (default) |
| `GET /workbench/session/<id>/preflight` | 2 · Preflight |
| `GET /workbench/session/<id>/inventario` | 3 · Fotografia account |
| `GET /workbench/session/<id>/migrazione` | 4 · Cosa verrà migrato |
| `GET /workbench/session/<id>/conferme` | 5 · Conferme operatore |
| `GET /workbench/session/<id>/applica` | 6 · Applica e verifica |
| `GET /workbench/session/<id>/chiusura` | 7 · Chiusura |

POST esistenti (`status`/`attach`/`exec`) **invariati**. Screen 5 introduce UN
nuovo POST `/workbench/session/<id>/accept` (vedi §4.5) — thin wrapper sulla stessa
logica `saveAccept` già esistente, solo con redirect alla schermata conferme invece
che a `/`.

Motivazione della scelta (vs tab single-page o `?screen=`): una rotta per schermata
= URL riflette "dove sono", golden httptest indipendente per schermata (§5), e si
innesta pulito sullo switch di `routeWorkbench` senza toccare il parsing esistente.
La base `GET /workbench/session/<id>` continua a rendere la Panoramica → la sessione
`mig_20260704_1a4eaa2cc7d7` (ready_for_cutover) resta renderizzata correttamente.

### 1.2 View-model (read-only, presentazione)
`workbenchServer` oggi ha `{store, tpl, csrf}`. Aggiungo `dir string` (== `o.Dir`)
per leggere gli artifact — **plumbing di presentazione**, nessun nuovo stato.
`handleDetail` diventa un dispatcher per schermata; ogni schermata costruisce il
proprio view-model leggendo gli artifact e passandolo al template.

Un unico costruttore `buildWorkbenchView(dir, sess) workbenchView` legge una volta
sola gli artifact presenti e li espone tradotti. Se un artifact manca/è illeggibile
→ la sezione mostra "non ancora disponibile", MAI un 500 (fail-soft, come la dashboard).

Templates: nuovo file `workbench_screens.html` con i 7 `{{define}}` + una nav
condivisa e i partial glossario. `workbench_detail.html` resta come contenitore
Panoramica (o viene ristrutturato in `{{template "screenPanoramica" .}}`).

## 2. Glossario UI (layer template — enum motore intatti)

| Tecnico (enum, invariato) | UI italiano |
|---------------------------|-------------|
| artifact | report |
| migration_checklist | verifica migrazione |
| acceptances / OperatorAcceptance | conferme operatore |
| policy blocker | problema bloccante |
| apply | applica |
| verify | verifica risultato |
| rollback | annulla modifiche |
| BLOCKED | «Non procedere: N problemi da risolvere» |
| MANUAL_ACTION_REQUIRED | «Azioni manuali da confermare» |
| NOT_READY | «Analisi incompleta» |
| READY_WITH_MANUAL_NOTES | «Pronto (con note manuali)» |
| READY_TO_CUTOVER | «Pronto per il cutover» |
| blocks_apply | «impedisce di applicare ora» |
| blocks_cutover | «impedisce il cutover» |
| standalone (cluster DNS) | «peer DNS isolato (non propaga in produzione)» |

Stati di governance (`Status`) → etichette (coerenti con #62):

| Status | Etichetta IT |
|--------|--------------|
| draft | Bozza |
| preflight_required | Preflight richiesto |
| inventory_ready | Inventario pronto |
| checklist_ready | Verifica pronta |
| manual_actions_required | Conferme richieste |
| ready_for_apply | Pronto per applicare |
| apply_in_progress | Applicazione in corso |
| apply_done | Applicazione completata |
| verification_required | Verifica richiesta |
| ready_for_cutover | Pronto per il cutover |
| cutover_done | Cutover completato |
| blocked | Bloccato |
| failed | Fallito |
| archived | Archiviato |

## 3. Mapping stato → PROSSIMA AZIONE CONSIGLIATA (schermata 1)

Tabella COMPLETA e deterministica. Nessuna euristica nuova: la colonna "azione"
è il passo operatore che guida verso il `to` della `allowedTransitions` matrix;
le righe con "refinement" leggono un FATTO già calcolato da helper esistenti
(`ApplyBlocked`, presenza verify report), non uno scoring inventato.

| Status | Prossima azione consigliata (IT) | Schermata target | Refinement da artifact |
|--------|----------------------------------|------------------|------------------------|
| draft | Configura le connessioni ed esegui il preflight | Preflight | — |
| preflight_required | Esegui il preflight (sorgente + destinazione) | Preflight | — |
| inventory_ready | Esegui l'analisi per generare la verifica migrazione | Panoramica (Esegui pipeline) | — |
| checklist_ready | Rivedi «Cosa verrà migrato» e registra le conferme | Migrazione → Conferme | se `ApplyBlocked` → «Risolvi i N problemi bloccanti» + Chiusura |
| manual_actions_required | Registra le conferme operatore mancanti (N) | Conferme | N = manual acceptable & !accepted |
| ready_for_apply | Applica le modifiche | Applica | se `ApplyBlocked` → «Apply bloccato: risolvi i problemi» |
| apply_in_progress | Attendi il completamento dell'applicazione | Applica | — |
| apply_done | Esegui le verifiche (DNS / Email / Cron) | Applica | elenca i verify mancanti |
| verification_required | Esegui le verifiche mancanti (poi la transizione è automatica) | Applica | elenca i verify non-CLEAN/mancanti |
| ready_for_cutover | Sei pronto: vai alla Chiusura | Chiusura | — |
| cutover_done | Migrazione completata — puoi archiviare | Chiusura | — |
| blocked | Risolvi i problemi, poi sblocca (Cambia stato con motivo) | Panoramica | elenca i blocker |
| failed | Rivedi l'ultimo errore, poi decidi come procedere | Panoramica | mostra `LastError` |
| archived | Sessione archiviata (sola lettura) | Panoramica | — |

Semafori per fase (schermata 1) — derivati SOLO da presenza artifact + status:
- **connessioni**: 🟢 se `host.yaml` presente ∧ status ≥ inventory_ready; 🟡 host.yaml presente; ⚪ altrimenti
- **inventory**: 🟢 se `inventory_source.json` ∧ `inventory_destination.json` presenti; ⚪ altrimenti
- **email/cron/dns** (per area): 🟢 se `<area>_apply_report.json` ∧ `<area>_verify_report.json`(clean) presenti; 🟡 se `<area>_*_plan.json` presente; ⚪ altrimenti
- **cutover**: 🟢 se status == cutover_done; 🔵 se ready_for_cutover; ⚪ altrimenti

## 4. Le 7 schermate — fonti dati e riuso

Legenda fonte: [C]=`migration_checklist.json`, [I]=`inventory_*.json`,
[A]=`acceptances.json` (via checklist `Accepted`), [V]=`*_verify_report.json`,
[H]=`host.yaml`/preflight, [S]=`session.json`, [RB]=CUTOVER_RUNBOOK (testo statico).

1. **Panoramica** — [S]+[C]+artifact presence. Nome, stato tradotto, semafori fase,
   blocco "PROSSIMA AZIONE" (§3). Riusa `statusBadge`. Nav alle altre 6.
2. **Preflight** — [H]+[S]. Semafori: sorgente/destinazione raggiungibili, account
   dest, spazio, cluster="da confermare manualmente". JSON grezzo sotto `<details>`
   "Dettagli tecnici". (Preflight reachability = presenza/validità host.yaml + eventuale
   report; se non collezionato → "da eseguire". NESSUN nuovo collector.)
3. **Fotografia account** — [I]. Contatori leggibili dall'inventory
   (domini/mailbox/db/cron/forwarder/filtri/dns/ssl/php) via `NormalizedInventory`
   già parsato. Dettagli in `<details>`.
4. **Cosa verrà migrato** — [C].CoverageManifest + [C].Sections/ManualActions:
   - ✅ automatico: area `covered` senza manual action non-accettata pendente
   - 🟡 richiede conferma: area con manual action acceptable & !accepted (da [C])
   - ⚪ non gestito: `root_only` + `not_collected` (con la `Note` del perché)
   Riusa `CoverageAreas()` — o meglio il manifest già embeddato nel checklist.
5. **Conferme operatore** — [C].ManualActions dove `Acceptable && !Accepted`.
   Per voce: perché serve (Title/Detail), rischio se ignorata (BlockingCutover→
   «impedisce il cutover»), come verificarla (OperatorAction), form "Conferma fatto"
   → POST `/workbench/session/<id>/accept` (campi `action_key`, `reason`, `operator`).
   Mostra anche le già-accettate (`Accepted`, con AcceptedBy/Reason). NON reimplementa
   il merge: riusa la logica di `saveAccept` (MergeAcceptance + rigenerazione checklist).
6. **Applica e verifica** — [C]+[V]+exec forms esistenti. 4 blocchi d'AZIONE
   (contenuti / email / cron / DNS). Per blocco: Mostra piano (dns_plan/email_plan/
   cron_plan, read-only) · Applica (conferma forte esistente) · Verifica · Annulla
   modifiche (rollback, doppia conferma) · Report. Stato in linguaggio operatore
   («Applicata e verificata · Backup automatico disponibile» quando apply_report +
   verify(clean) presenti). **DNS = Danger Zone** (§4.6). Riusa i template
   `execBtnReadOnly`/`execFormWrite`/`execFormRollback`.
7. **Chiusura** — risponde a UNA domanda: «Posso spegnere il vecchio server?»
   - **SÌ** se: status == ready_for_cutover (o cutover_done) ∧ nessun `BlockersCutover`
     residuo ∧ nessuna manual action `BlockingCutover && !Accepted`.
   - **NO** con la lista ESATTA: (a) blocker cutover da [C].Sections.BlockersCutover;
     (b) conferme mancanti (manual BlockingCutover !accepted); (c) decisioni utente
     pendenti dal runbook [RB] §7 (5 voci: data campagna, finestra cutover, ruolo sync
     DNS, ordine account, pulizia zone — testo STATICO, non un motore).
   Artifact/report raggiungibili sotto "Dettagli tecnici", mai nav primaria.
   La UI **non esegue** il cutover (fuori scope).

### 4.6 DNS Danger Zone (evoluzione affordance N2 di #62)
Nel blocco DNS della schermata 6: sezione visivamente separata (bordo rosso).
Il warning cluster #62 resta. Aggiungo una **checkbox out-of-band**
«Ho verificato che il peer DNS della destinazione è isolato (standalone) e NON
propaga in produzione». Il bottone «Applica DNS» è **disabilitato** finché la
checkbox non è spuntata (gate lato client via `disabled` + attributo, + il gate
server esistente `isApplyBlockedByChecklist` resta). La checkbox è un fatto
out-of-band (rule #4): NON viene persistita nel motore, è un'affordance di
attenzione pre-submit. La conferma forte (digitare il nome account) resta.

## 5. Piano di test (TDD, httptest + content assertions, come la convenzione esistente)

- **Mapping stato→azione**: test tabellare — per OGNI `Status` in `AllStatuses` la
  funzione `nextAction(status, facts)` ritorna azione+schermata attese (tabella §3
  al completo; nessuno status senza riga).
- **Golden per schermata** (httptest GET su ognuna delle 7 rotte): asserzioni sui
  contenuti chiave tradotti + assenza di stringhe EN/enum grezzi dove il glossario
  richiede traduzione.
- **Coverage screen (4)**: covered→✅, root_only/not_collected→⚪ con Note, manual
  pendente→🟡. Fixture checklist con le 3 classi.
- **Conferme (5)**: manual acceptable&!accepted appare con form; già-accettata mostra
  AcceptedBy; POST /accept registra e redirige alla schermata conferme (riuso logica).
- **Danger zone (6)**: «Applica DNS» ha `disabled` senza checkbox; markup checkbox
  presente; conferma forte presente. Escaping invariato (html/template).
- **Chiusura (7)**: SÌ quando ready_for_cutover & nessun blocker/conferma pendente;
  NO elenca blockers_cutover + conferme mancanti + 5 decisioni runbook.
- **Regressione**: (a) sessione a `ready_for_cutover` con artifact reali (fixture dal
  mig_20260704) rende Panoramica + Chiusura=SÌ senza errori; (b) flusso #61 (dashboard
  `/`, accept, run) invariato — i suoi test esistenti restano verdi; (c) rotte POST
  esistenti invariate; (d) `go test ./...` verde.

## 6. Fuori scope
Nuovi writer/API/collector; campaign/batch; SQLite; modifiche a state machine,
policy, formati artifact; esecuzione del cutover dalla UI; persistenza della checkbox
danger-zone nel motore.

## 8. Correzioni post-riesame (VINCOLANTI — prevalgono sull'inline sopra)

Riesame opus, verdetto APPROVE-WITH-CHANGES. Delta autoritativo:

- **(a) Schermata 7 — no falso SÌ su status forzato.** Lo `status` di governance è
  forzabile (`force`). Il verdetto «posso spegnere?» NON si basa sullo status ma sul
  FATTO artifact. Condizione **SÌ** = `OverallStatus ∈ {READY_TO_CUTOVER,
  READY_WITH_MANUAL_NOTES}` ∧ nessun `BlockersCutover` residuo ∧ nessuna manual
  `BlockingCutover && !Accepted`. Le **5 decisioni del runbook §7 sono SEMPRE**
  mostrate come «Decisioni che restano a te» (il tool non può risolverle): compaiono
  sia nel SÌ (come note/caveat) sia nel NO (nella lista). `ApplyBlocked` NON è il gate
  del cutover (resta gate dell'apply, schermata 6).

- **(b) /accept resta su `*server`, non su `workbenchServer`.** `saveAccept`
  (accept.go:27) usa `s.job`/`s.dir`, assenti su `workbenchServer`. Il nuovo POST
  `/workbench/session/<id>/accept` si aggiunge come `case` in `routeWorkbench` (che è
  metodo di `*server`) via `s.post(...)`. Refactor accept.go: estrarre
  `saveAcceptTo(w,r,redirectURL)`; `saveAccept` diventa `saveAcceptTo(w,r,"/")`.
  **Test di regressione obbligatorio**: la dashboard `POST /accept` continua a → `/`.

- **(c) Nomi file piani REALI** (override §4 schermata 6): `dns_import_plan.json`,
  `email_apply_plan.json`, `cron_apply_plan.json` (i verify/apply report combaciano).

- **(d) Traduzione = nuove funcMap, non solo `statusBadge`.** Servono
  `statusLabelIT(Status)`, `overallLabelIT(string)`, glyph coverage/semaforo. Un
  UNICO punto per i `{{define}}` condivisi (evitare collisione silenziosa di define
  omonimi tra workbench_detail.html e workbench_screens.html nel ParseFS).

- **(e) Guardia metodo sui nuovi case**: ogni GET sub-view con
  `action=="…" && r.Method==http.MethodGet`; aggiungere il `case action=="accept"`
  POST. Senza, un POST cadrebbe nell'handler GET o accept in `default:404`.

- **(f) Onestà governance-status vs artifact-readiness (N3).** Le transizioni early
  (draft→…→ready_for_apply) NON sono attuate da nessuna schermata: `handleExec`
  registra a status INVARIATO (l'unica auto-transizione è verify→ready_for_cutover).
  Il blocco «PROSSIMA AZIONE» indica il **passo operativo** (es. «Esegui la pipeline»)
  e, dove l'operazione non avanza lo status da sola, aggiunge esplicitamente
  «poi avanza lo stato in Governance». Nessuna promessa che l'azione muova lo status.
  Correzione §3: riga `checklist_ready`+`ApplyBlocked` NON punta a Chiusura (il blocker
  è di apply): punta a Conferme/Applica.

- **(g) Schermata 3 — contatori da `Section.SourceCount`/`DestinationCount`**
  (checklist_types.go:118-119), NON da un nuovo unmarshal degli `inventory_*.json`:
  la webui oggi parsa SOLO `migration_checklist.json`. Meno rischio, zero nuovo parser.

- **(h) Schermata 4 — dipendenza documentata**: il 🟡 fa JOIN
  `CoverageArea.Area == ManualAction.Section`, valido perché il registry pin-a
  Area==nome-sezione (coverage.go:30-31, TestCoverageRegistryLockstepWithChecklistSections).
  Il 🟡 si applica solo alle aree `covered`.

- **(i) Danger zone — etichettata come ATTESTAZIONE, non enforcement.** Il `disabled`
  è solo client-side (bypassabile). La UI deve dire «attestazione manuale», non «gate
  applicato». Enforcement reale = conferma forte (nome account) + CSRF, invariati.
  Nessun gate server può leggere lo stato del cluster (tool user-level).

## 7. Rischi & mitigazioni
- **R1** parsing artifact assenti → fail-soft "non disponibile", mai 500 (test).
- **R2** duplicazione logica accept → riuso `saveAccept` estraendone il redirect
  (parametrizzare la destinazione), zero nuova logica di merge/hash.
- **R3** deriva glossario vs #62 → tabella §2 come unica fonte, test anti-EN.
- **R4** scope creep → questa PR è presentazione+routing read-only + 1 thin POST;
  nessun file di `internal/workbench` o `internal/accountinventory` toccato.
