# Design — Platform UI V2 (operator-first, SaaS shell)

Stato: **IN CORSO** — PR `feat/platform-ui-v2` (branch base `feat/operator-first-guided-ux`, sopra PR #83).
Sessione 2026-07-07. **Presentation-only**: nessun writer/CLI/SSE/migration_plan.json,
motore intatto. Fedeltà: i 14 mockup approvati in `screenshot/`.

## Obiettivo

Un **guscio prodotto** SaaS 2026, parallelo alla workbench, su route `/platform`.
La workbench esistente (`/workbench/*`, già operatore/esperto dopo #83) diventa la
**Modalità esperto / dettagli tecnici**. La UI V2 non ragiona in termini grezzi di
workbench: consuma un read-model dedicato (`platformPage`) che **adatta**
`buildWorkbenchView` + `store.List()`, senza duplicare la logica del motore né i
gate di sicurezza.

> Principio: *Fedele al mockup nello stile, onesto nei dati.* Mai inventare numeri.

## Confine architetturale

```
UI V2 prodotto  = /platform/*        → percorso operatore (nuovo guscio SaaS)
Workbench       = /workbench/*        → Modalità esperto / fallback (invariata)
Dashboard R/O   = /                   → workstation legacy (invariata)
```

Le **mutazioni** (crea sessione, conferma scope, avvio migrazione, accept, exec)
**riusano gli handler POST esistenti e testati** — nessun nuovo writer, nessuna
nuova semantica, stesso CSRF (token per-server), stessa strong-confirmation,
stesso `startAllowed`. Il layer platform è presentazione + navigazione.

## 1. File nuovi

| File | Ruolo |
|---|---|
| `internal/webui/platform_view.go` | Read-model `platformPage` + adapter `buildPlatform*` (dashboard, cockpit, plan, tasks, report, compare) su `buildWorkbenchView`/`store.List` |
| `internal/webui/platform_view_test.go` | Test read-model: no-nil, fallback onesti, expert URL, gating start invariato, dashboard stats da stati reali |
| `internal/webui/platform.go` | `platformServer` + `routePlatform`, handler `handleDashboard/handleSessionCockpit/handlePlan/handleTasks/handleReport/handleCompare` |
| `internal/webui/platform_wizard.go` | Wizard V2 GET/POST — riusa helper condiviso `parseWizardSubmission` estratto da `handleWizardCreate` |
| `internal/webui/platform_render_test.go` | Test render 7 schermate (11 casi obbligatori del brief) |
| `internal/webui/platform_wizard_test.go` | Test wizard render + submit valido/invalido riusa store |
| `internal/webui/templates/platform_theme.html` | Shell SaaS: `<head>`+CSS inline, sidebar, topbar, `platformStepper`, `platformSessionHeader`, badge, card. Zero asset esterni |
| `internal/webui/templates/platform_dashboard.html` | Schermata 1 — Dashboard migrazioni |
| `internal/webui/templates/platform_wizard.html` | Schermata 2 — Nuova migrazione / Setup wizard |
| `internal/webui/templates/platform_plan.html` | Schermata 3 — Preflight / Piano migrazione |
| `internal/webui/templates/platform_cockpit.html` | Schermata 4 — Cockpit (route principale sessione) |
| `internal/webui/templates/platform_tasks.html` | Schermata 5 — Task manuali / Verifica finale (read-only) |
| `internal/webui/templates/platform_report.html` | Schermata 6 — Report finale (read-only) |
| `internal/webui/templates/platform_compare.html` | Schermata 7 — Comparativa dettagliata (read-only, degrada onesto) |

## 2. File esistenti modificati

| File | Modifica | Rischio |
|---|---|---|
| `internal/webui/webui.go` | `New()`: costruisce `platformServer` (se SessionStore ≠ nil). `route()`: dispatch prefisso `/platform`. Aggiunge `host.yaml`… nessun nuovo artifact | Basso — additivo, nessuna route esistente toccata |
| `internal/webui/workbench_wizard.go` | Estrae `parseWizardSubmission(r) (setup, name, srcProfile, dstProfile, view, ok)` da `handleWizardCreate`; l'handler esistente lo chiama e **mantiene identico** redirect `/workbench/session/:id` | Basso — refactor puro, comportamento workbench invariato (test esistenti verdi) |

Nessun altro file toccato. `internal/workbench`, `internal/accountinventory`,
orchestratore, exec, monitor: **diff zero**.

## 3. Nuove route

| Metodo | Route | Handler | Note |
|---|---|---|---|
| GET | `/platform` → 303 `/platform/migrations` | — | entrypoint |
| GET | `/platform/migrations` | `handleDashboard` | schermata 1 |
| GET | `/platform/migrations/new` | `handlePlatformWizardForm` | schermata 2 |
| POST | `/platform/migrations/new` | `handlePlatformWizardCreate` | riusa `parseWizardSubmission`+`store.CreateWithSetup`, redirect `/platform/migrations/:id` |
| GET | `/platform/migrations/:id` | `handleSessionCockpit` | schermata 4 (cockpit) |
| GET | `/platform/migrations/:id/plan` | `handlePlan` | schermata 3 |
| GET | `/platform/migrations/:id/tasks` | `handleTasks` | schermata 5 |
| GET | `/platform/migrations/:id/report` | `handleReport` | schermata 6 |
| GET | `/platform/migrations/:id/compare` | `handleCompare` | schermata 7 |

Le CTA mutanti (Conferma scope, Avvia migrazione, Registra conferma, Verifica/Apply
singoli, Rollback, DNS) **puntano agli endpoint workbench esistenti**
(`/workbench/session/:id/{scope,start-migration,accept,exec}`): il redirect
riporta al percorso workbench (Modalità esperto) — la conferma irreversibile
avviene sullo schermo già interamente testato. Vedi §7 (seam dichiarato).

Gate di richiesta invariati: loopback + anti-rebinding Host + Origin + CSRF su
POST, ereditati da `route()`/`post()`. Nessun file-serving, nessun redirect oltre
il 303 post-azione.

## 4. Read-model `platformPage`

```go
type platformPage struct {
    Nav        platformNav        // voci sidebar (solo reali) + expert URL
    Header     platformHeader     // topbar: brand, ricerca (statica), utente
    // Dashboard
    Stats      []platformStat     // In corso / In attesa / Completate / Con task manuali (da stati reali)
    Migrations []platformMigRow   // righe tabella (sessioni reali)
    Activity   []platformActivity // "Attività recenti": timeline eventi aggregata, bounded
    // Sessione (schermate 3-7): adapter su workbenchView
    Session    *workbench.Session
    Steps      []platformStep     // stepper 7 fasi (da timeline/cockpit steps)
    SrcDst     platformSrcDst     // chip Sorgente → Destinazione
    Plan       migrationPlan      // riuso diretto
    Cockpit    cockpitModel       // riuso diretto (comparativa, monitor, buckets)
    Cutover    cutoverVerdict
    Tasks      []accountinventory.ManualAction
    Compare    []platformCompareRow // area-per-area (Counts+stato); dettaglio file = fallback onesto
    ReportView platformReport
    Flash      string
    CSRF       string
    ExpertURL  string             // link alla stessa sessione in workbench (Modalità esperto)
}
```

`platformStat`, `platformMigRow`, `platformStep`, `platformActivity`,
`platformCompareRow`, `platformReport`, `platformNav`, `platformHeader`,
`platformSrcDst` sono struct di presentazione. **`Plan`, `Cockpit`, `Cutover`,
`Tasks` sono riuso diretto** dei tipi già costruiti da `buildWorkbenchView`:
zero duplicazione della logica di readiness/gating.

Costruttori:
- `buildPlatformDashboard(store, dir, csrf) platformPage` — `store.List()` → stats+righe+activity.
- `buildPlatformSession(dir, csrf, sess, jobBusy, screen) platformPage` — chiama
  `buildWorkbenchView(dir, csrf, screenPanoramica, sess, jobBusy)` e mappa i campi.

## 5. Mapping schermata → dati necessari

| # | Schermata | Dati (fonte reale) | Fallback onesto |
|---|---|---|---|
| 1 | Dashboard | `store.List()`: conteggi per stato, righe (dominio=`Setup.PrimaryDomain`/`Name`, src/dst=profile, stato=`statusLabelIT`, prossima azione=`nextAction` riconciliata, aggiornato=`UpdatedAt`); activity=timeline aggregata | Nessuna sessione → empty-state "Nessuna migrazione: creane una". Stat metriche non calcolabili (tasso successo, MB/s) **omesse** |
| 2 | Wizard | form non-secret (host/porta/account src+dst, toggle contenuti) → `store.CreateWithSetup` | Password/"Raggiungibile live" **non presenti** (credenziali in host.yaml 0600; niente probe live) |
| 3 | Preflight/Piano | `Plan` (buckets Automatico/Manuale/Escluso, blockers migrazione/cutover), `Cockpit.Comparison`, scope | Senza checklist → `Plan.NotReadyMessage` "Esegui il preflight…" |
| 4 | Cockpit | `Cockpit` (hero+CTA da `startAllowed`, stepper, comparativa, monitor phase+log, task aperti), `Plan` | Monitor: **fasi reali + log reali**; %/ETA/MB-s globali **non mostrati** se non c'è fonte (stepper come progresso) |
| 5 | Task manuali | `Tasks` = `Confirms`/`ManualActions` raggruppate; `manualTitleIT`/`manualActionIT`; dati copiabili (testo selezionabile) | Nessun task → "Nessun task manuale". "Verifica ora" → link expert |
| 6 | Report | success banner se `CutoverDone`; comparativa finale (`Counts`), timeline, presenza `report.json` | Nessun PDF generato → "Report tecnico negli artifact (Modalità esperto)". Se non completata → stato reale |
| 7 | Comparativa | area-per-area (`Counts`/`Comparison`: src/dst/stato) | Diff a livello file/codice **non disponibile** (inventory_diff è per-sezione) → pannello dettaglio degrada: "Dettaglio file non disponibile" |

## 6. Mapping schermata → artifact esistenti

`inventory_source/destination.json`, `inventory_diff.json`, `policy_report.json`,
`migration_checklist.json`, `report.json`, `events.jsonl`, `*_apply/verify_report.json`,
`*_plan.json`, `acceptances.json` — **tutti già letti** da `readArtifactFacts` /
`loadRunMonitor`. Il layer platform non apre nuovi file: consuma `artifactFacts`,
`migrationPlan`, `cockpitModel`, `runMonitor` già prodotti.

## 7. Cosa resta in Modalità esperto (fuori dal percorso primario)

`host.yaml` / nome-file · SHA256 + tabella artifact · governance / cambio-forza
stato · attach manuale report · «Stato per fase» tecnico · cronologia eventi
completa · apply/verify singoli · **DNS Danger Zone** · rollback · definizione
tecnica migrazione. Tutto raggiungibile via **`ExpertURL`** (link
`/workbench/session/:id`) su ogni schermata sessione, e via voce sidebar
«Modalità esperto / Dettagli tecnici».

**Seam dichiarato (follow-up):** le CTA mutanti del percorso platform reindirizzano
agli schermi workbench; l'operatore che conferma scope/avvia migrazione atterra
sulla vista workbench (esperto), che post-#83 è comunque operatore-first. Scelta
di sicurezza deliberata: la conferma irreversibile avviene sullo schermo già
interamente testato, zero rischio di mis-wiring di un trigger di migrazione. Far
tornare il redirect a `/platform` è un follow-up presentation-only.

## 8. Rischi di regressione

1. **Refactor `handleWizardCreate`** → mitigato: estrazione pura di
   `parseWizardSubmission`, handler workbench mantiene redirect e comportamento
   identici; i test wizard esistenti (`workbench_wizard_test.go`) restano verdi.
2. **Routing `/platform` in `route()`** → additivo; le route esistenti (`/`,
   `/config`, `/run`, `/accept`, `/workbench/*`) intatte. Test `webui_test.go`
   e `workbench_test.go` restano verdi.
3. **Gate start** → `startAllowed` non toccato; la platform lo **consuma** e mostra
   la CTA abilitata solo quando `true`, come il cockpit. Hero+CTA derivano dallo
   stesso gate ⇒ non divergono.
4. **CSRF / strong-confirmation / DNS fuori auto-run / single-writer / rollback
   gated-by-backup** → invariati: le mutazioni passano dagli handler esistenti.
5. **`html/template`** auto-escape su tutti i campi; nessun `template.HTML` grezzo,
   `ExpertURL`/`ModeQuery` literal.

## 9. Scope di QUESTA PR

Reali e fedeli: **1 Dashboard · 2 Wizard · 3 Preflight/Piano · 4 Cockpit**.
Iniziali read-only (layout fedele, dati reali dove esistono, fallback onesti):
**5 Task manuali · 6 Report · 7 Comparativa**.

Fuori scope: Campaign Mode, SSE, PDF report generator, diff file-level, DNS
classification esterna, metriche aggregate (tasso successo/MB-s), voci sidebar
aspirazionali (Team/Modelli/Account/Impostazioni) — omesse, non finte.
