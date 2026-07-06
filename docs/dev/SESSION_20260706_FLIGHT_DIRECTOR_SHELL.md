# Sessione 2026-07-06 — webui: Flight Director UI shell (#75)

Report completo della sessione. Un intervento, **solo-presentazione** (motore,
sicurezza, comportamento invariati; zero regressioni; tutto in italiano):
il salto da «schermate workbench» a **cabina di regia della migrazione**.

- **PR #75** (merged, merge commit `d8c2a6f`) — Flight Director UI shell.

Riferimenti: `FRONTEND_FLIGHT_DIRECTOR_ROADMAP.md` (§5 pattern Flight Director),
`DEVELOPMENT_STATE.md` (ledger #75), `HANDOFF_NEXT_SESSION.md` (stato post-merge).

---

## Problema

Dopo #66 (7 schermate guidate), #72 (wizard) e #73 (next action scope-aware) la UI
era buona ma restava **troppo vicina al modello ingegneristico**: navigazione a
pillole fra schermate, next action per-schermata, identità della migrazione e stato
del job sparsi. L'operatore, durante una migrazione lunga (refresh, sleep, job
interrotto, cambio schermata), rischiava ancora di **perdere il contesto**: dove
sono, cosa sta succedendo, qual è il rischio, qual è la prossima azione.

La roadmap (§5) chiedeva il pattern **Flight Director**: header persistente, timeline
laterale, main stage contestuale, risk badge, next recommended action — senza toccare
il motore. Vincolo esplicito: **UI shell, non riscrittura**; niente SSE/WebSocket,
niente Campaign Mode, niente writer/collector/runner, niente gating server-side di
`/exec`, niente `host.yaml` generation.

## Analisi (ground truth verificata prima di scrivere)

Ispezione di `workbench_view.go`, `workbench.go`, `job_journal.go`, `types.go` e dei
template. Fatti chiave che hanno guidato il design:

- Ogni schermata rende `wbHead` → `wbNav` (pill nav + jobBanner) → contenuto →
  `wbFooter`. La Panoramica è `workbench_detail.html`; le altre 6 sono i `define
  screen_*` in `workbench_screens.html`. `handleScreen` costruisce `buildWorkbenchView`
  e chiama `ExecuteTemplate` sul template della schermata.
- **Tutti i dati per la shell esistono già** nel view-model: `Session` (+`Setup`),
  `StatusLabel`, `Scope` (da `deriveContentScope`, #72), `Next` (scope-aware, #73),
  `Job`/`JobLive` (job journal #70), `Facts` (artifact facts fail-soft).
- Le route delle 7 schermate sono fisse (`screenTemplates`): la timeline **non deve
  inventare route nuove**, deve mappare le 7 esistenti.
- `Session.Setup` è un pointer omitempty: le sessioni legacy sono `nil` → serve un
  fallback pulito a `SourceProfile`/`DestinationProfile`.

## Decisione di design — shell attorno, funzioni pure sotto

Approccio a churn minimo e testabile:

1. **Funzioni pure nel view-model** (`internal/webui/workbench_flightdirector.go`,
   NUOVO): `buildRiskBadge` e `buildTimeline` (+ helper `applyPhaseState`). Traducono
   status + artifact facts + job journal + scope in un `riskBadge{Label,Class}` e in
   `[]timelineStep{Label,Screen,State,Current}`. Nessuna logica operativa, nessun I/O:
   come tutto `workbench_view.go`, è pura traduzione di fatti già calcolati.
2. **Template della shell** (`workbench_detail.html`): `wbHead` apre la shell
   (appHeader, container, `fdHeader` persistente, griglia a 2 colonne con
   `<aside class="fd-rail">` = timeline e `<main class="fd-stage">`). `wbFooter` chiude
   main/grid/container. Nuovi `define` `fdHeader` e `fdTimeline`. Il vecchio pill-nav
   `wbNav` è rimosso (chiamate tolte dalle 6 schermate + Panoramica), il jobBanner è
   migrato dentro `fdHeader`.
3. **CSS** in `_theme.html` (`define themeCSS`): `.fdhead*`, `.fd-grid/.fd-rail/.fd-steps`,
   `.badge.warn`, responsive < 820px (timeline in riga). Rimosso il CSS morto `.wbnav`.
4. **Helper FuncMap** in `workbench.go`: `fdDot` (state→classe dot) e `fdStateLabel`
   (state→etichetta IT).

Le schermate esistenti (contenuto del main stage) **non sono state riscritte**: solo
la riga `{{template "wbNav" .}}` è stata rimossa e la Panoramica ha cambiato l'`<h1>`
da «nome + status» (ora nell'header) a titolo schermata.

### Header persistente (`fdHeader`)

Sempre visibile: nome migrazione, dominio principale, **sorgente → destinazione con
account/porte**, `statusBadge` (governance), **risk badge** onesto, tag DNS
incluso(«area delicata»)/escluso, jobBanner (running/interrupted), prossima azione.
- Wizard (`Session.Setup != nil`): account@host per sorgente e destinazione, dominio.
- Legacy (`Setup == nil`): fallback a `SourceProfile → DestinationProfile`, nessun
  tag DNS inventato (gated da `Scope.HasSetup`).

### Timeline laterale (`fdTimeline`)

Le 7 fasi mappate 1:1 alle route reali: Panoramica · Preflight · Fotografia account ·
Cosa verrà migrato · Conferme operatore · Applica e verifica · Chiusura. Per ogni fase
uno stato sintetico (Da fare / In corso / Fatto / Attenzione), la fase corrente
evidenziata (`fd-current` + `aria-current="step"`), link alla schermata.

### Risk badge onesto — precedenza (dopo review)

`buildRiskBadge` sceglie **un** badge per urgenza decrescente. L'ordine finale (dopo i
tre giri di review) è:

1. **Job live / interrotto / fallito — priorità UNCONDIZIONATA in cima.** L'exec è
   raggiungibile anche su una sessione terminale (l'exec path non ha gate di status),
   e perdere di vista un job è esattamente ciò che il job journal (#70) esiste per
   evitare: quindi vince anche su uno stato terminale.
2. **Stati terminali** (`cutover_done` → «Cutover completato»/done; `archived` →
   «Archiviata»/draft): vincono su una checklist stale mai rigenerata dopo il cutover.
3. Governance fallita (`blocked`/`failed` → «Attenzione»).
4. Checklist blocker (`ApplyBlocked`/`Blocked`/`NotReady` → «Bloccante»).
5. Wizard senza `host.yaml` → «Configurazione richiesta» (warn).
6. Calm states (`ready_for_cutover` → «Pronto per il cutover»; `draft`/`preflight` →
   «Da configurare»); default «In corso».

**Non promette mai falso verde**: nessun «OK/verde» per una bozza; il verde è solo per
`ready_for_cutover`/`cutover_done`.

## Test (TDD)

- `workbench_flightdirector_test.go` — unit sulle funzioni pure: precedenza risk badge
  (job running/interrupted, config-required, blocking, ready-for-cutover, draft
  non-verde, **stati terminali che ignorano una checklist stale**, **job che vince sui
  terminali**); timeline (7 fasi + route reali, fase corrente unica, apply-blocked →
  warn, job running → doing, cutover_done → done, **staleness terminale risolta**,
  **inventory-ready → doing**).
- `workbench_flightdirector_render_test.go` — render HTTP della shell: identità header
  wizard (dominio/sorgente/dest), fallback legacy, timeline + fase corrente, job
  running visibile (via `newWorkbenchServer` con `jobBusy=true`), job interrotto marcato
  attenzione + niente meta-refresh, config-required, DNS incluso prudente, DNS escluso
  senza falso warning, **form Applica intatti** (`migrate_content`, `confirm_account`,
  `csrf`, `dns-apply-btn`).

## Review adversariale (go-reviewer — 3 giri)

- **R1 → REQUEST CHANGES.**
  - HIGH #1: gli stati terminali (`Archived`/`CutoverDone`) erano mal rappresentati
    perché `migration_checklist.json` non viene rigenerato all'archiviazione — una
    checklist con `ApplyBlocked` stale faceva il risk badge dire «Bloccante» su una
    migrazione conclusa, e la timeline mostrava «Attenzione»/«Da fare» su fasi in realtà
    chiuse. Fix: gli stati terminali vincono sulla checklist stale (badge + timeline);
    `invState` intermedio; +test di regressione.
  - MEDIUM #2: `invState` (timeline) keyed su `hasChecklist` contraddiceva il widget
    «Stato per fase → Inventario» keyed sulla presenza inventario. Fix: `invState` =
    done se checklist, altrimenti «In corso» se inventario presente.
  - LOW #3: rimosso CSS morto `.wbnav`.
- **R2 → REQUEST CHANGES.** Il fix R1 aveva **introdotto una regressione**: mettendo i
  terminali come primo controllo, un job **live/interrotto** su una sessione
  archiviata/cutover veniva **nascosto** (proprio il segnale del job journal #70). Il
  reviewer ha verificato che l'exec path non ha gate di status, quindi il caso è
  raggiungibile. Fix: **swap** — job live/interrotto/fallito torna priorità assoluta,
  i terminali subito dopo; +`TestRiskBadgeJobBeatsTerminalStatus`.
- **R3 → APPROVE.** Verificato lo scenario combinato peggiore (checklist stale +
  archived + job live → «Job in corso»). Tutti e tre i finding risolti, nessuna nuova
  regressione.

## Gate (eseguiti, non promessi)

- `gofmt -l` pulito sui file modificati.
- `go test ./internal/webui/... ./internal/workbench/... ./internal/config/...` → ok.
- `go vet ./...` → pulito.
- `go test -race ./internal/webui/... ./internal/workbench/...` → ok.
- **Docker LINUX_ALL_GREEN**: `docker run --rm -v "$PWD":/src -w /src golang:1.25 bash -c
  'export PATH=$PATH:/usr/local/go/bin && go test ./... && go vet ./...'` → tutti i
  pacchetti ok, 0 FAIL (rieseguito dopo il fix R2).
- Verifica visiva reale nel browser: sessione wizard `giorginisposi` — header
  persistente (risk «Configurazione richiesta», tag «DNS non incluso»), timeline con
  fase corrente evidenziata, schermata Applica con form `migrate_content` + conferma
  forte intatti.

## File toccati

| File | Natura |
|------|--------|
| `internal/webui/workbench_flightdirector.go` | NUOVO — `buildRiskBadge`/`buildTimeline`/`applyPhaseState`, tipi `riskBadge`/`timelineStep` (funzioni pure) |
| `internal/webui/workbench_flightdirector_test.go` | NUOVO — unit risk badge + timeline (incl. stati terminali + job-priority) |
| `internal/webui/workbench_flightdirector_render_test.go` | NUOVO — render HTTP della shell |
| `internal/webui/workbench_view.go` | +campi `Risk`/`Timeline` a `workbenchView`, popolati in `buildWorkbenchView` |
| `internal/webui/workbench.go` | +helper FuncMap `fdDot`/`fdStateLabel` |
| `internal/webui/templates/workbench_detail.html` | `wbHead`/`wbFooter` ristrutturati, `fdHeader`/`fdTimeline` nuovi, `wbNav` rimosso |
| `internal/webui/templates/workbench_screens.html` | rimosse le 6 chiamate `{{template "wbNav" .}}` |
| `internal/webui/templates/_theme.html` | +CSS shell (`fdhead`/`fd-grid`/`fd-steps`/`badge.warn`), −CSS morto `.wbnav` |
| `docs/dev/HANDOFF_NEXT_SESSION.md`, `docs/dev/DEVELOPMENT_STATE.md` | ledger + handoff post-merge |

## Compromessi dichiarati (onestà)

- **Gating frontend-only** (invariante preesistente, non peggiorato): la timeline e i
  badge sono presentazione; il vero gate di scrittura resta la conferma forte
  per-account su `/exec`, non gateato server-side per status.
- **Doppio badge** in header (`statusBadge` governance + risk badge): per `Blocked`/
  `Failed` si sovrappongono in rosso. Scelta di design (due segnali distinti:
  stato vs rischio), non difetto — flag del reviewer lasciato come possibile polish.
- La checklist non viene rigenerata all'archiviazione: la coerenza dei terminali è
  ottenuta a livello di presentazione (gli stati terminali vincono), non ripulendo
  l'artifact.

## Cosa resta fuori

SSE/WebSocket, Campaign Mode, queue multi-account, migrazioni parallele, comparative
checklist completa, manual actions verificabili, final sync, cutover gateway completo,
nuovi writer/collector, gating server-side di `/exec`, `host.yaml` generation, modifiche
a `runner.go`. **SSE ancora rimandata** a dopo un dogfooding reale su migrazione lunga.

## Prossima direzione consigliata (NON iniziata)

**Dogfooding UI reale** su una migrazione lunga (per decidere se SSE serve davvero)
**oppure** **Comparative Checklist UI** (source vs destination per area). **Mai**
Campaign Mode.
