# Design — Operator-First UX Reset (Modalità Operatore / Esperto)

Stato: **IMPLEMENTATA** — PR #83 (branch `feat/operator-first-guided-ux`, base `fork/main` `c771dde`).
Sessione 2026-07-07. **Presentation-only**: nessun writer/CLI/SSE, motore intatto.

## Problema

Anche dopo la Modern Migration Cockpit (Fase 4, PR #82), la WebUI restava
strutturalmente un tool da sistemista: nel **percorso primario** l'operatore
vedeva ancora concetti tecnici interni — `host.yaml`, artifact, SHA, governance,
cambio stato manuale, attach report, azioni singole apply/verify, coverage
tecnica, timeline tecnica. La Fase 4 li aveva **collassati** sotto `<details>`
sulla Panoramica, ma erano ancora **presenti e renderizzati** nel path operatore.

Obiettivo: **separare nettamente Modalità Operatore (predefinita) da Modalità
Esperto**. L'operatore vede solo cosa deve fare; l'esperto può ancora vedere e
fare tutto; il tool conserva audit, artifact e controlli.

> Principio guida: **Simple for the operator, auditable for the tool.**

Questa PR **non** è nella roadmap a Fasi numerata (1-7): è un intervento
presentation-only trasversale, compatibile con la regola «motore intatto» già
usata per Flight Director shell (#75), Wizard (#72), scope-aware (#73), Fase 4.

## Meccanismo

Un unico flag di presentazione, letto per-request, mai persistito, **mai un
gate**.

| Elemento | Dove | Note |
|---|---|---|
| `workbenchView.Expert bool` | `internal/webui/workbench_view.go` (struct `workbenchView`) | default `false` = modalità operatore |
| `workbenchView.ModeQuery string` | idem | `"?mode=expert"` o `""`; suffisso letterale, mai da input utente |
| set del flag | `internal/webui/workbench.go` (`handleScreen`) | `if r.URL.Query().Get("mode") == "expert"` — settato **dopo** che `buildWorkbenchView` ha già costruito il read-model |
| toggle UI | `templates/workbench_detail.html` (`fdHeader`) | presente su ogni schermata via shell `wbHead`; operatore→«Modalità esperto / dettagli tecnici», esperto→«← Modalità operatore» |
| stickiness | `ModeQuery` appeso ai link nav (rail `fdTimeline`, stepper cockpit, `nextActionBox`, CTA link, plan-link) | la modalità persiste sui **link** di navigazione guidata |

**Perché non tocca `buildWorkbenchView`:** il read-model costruisce sempre TUTTI
i dati; sono i **template** a decidere cosa mostrare in base a `.Expert`. Nessun
cambio di firma, nessun dato nuovo, nessun ramo server-side sulla modalità.

**Sicurezza del flag:** `Expert` è settato *dopo* `buildWorkbenchView` e non è
mai passato a `startAllowed`/`cockpitCTAFrom`/nessuna funzione gate — è
strutturalmente irraggiungibile dalla logica di gating. `ModeQuery` è sempre un
literal hardcoded (`html/template` auto-escapes comunque): nessun rischio
injection.

## Cosa vede l'operatore vs cosa resta esperto

### Panoramica (`workbench_detail.html`)

Operatore (default): hero (stato + **UNA** CTA dominante), stepper orizzontale,
comparativa account, piano in 3 card, blocchi/attenzioni, monitor esecuzione,
ultimo errore umano, toggle modalità.

Spostato dietro `{{if .Expert}}` (blocco «Dettagli tecnici», **assente** in
operatore — non solo collassato):

| Superficie | Prima (Fase 4) | Dopo (operatore) | Dopo (esperto) |
|---|---|---|---|
| Governance + Cambia/Forza stato (`/status`) | `<details>` sempre reso | **assente** | `<details>` |
| Artifact + SHA256 + Allega report (`/attach`) | `<details>` sempre reso | **assente** | `<details>` |
| Definizione della migrazione | `<details>` sempre reso | **assente** | `<details>` |
| «Stato per fase» (semafori) | `<details>` sempre reso | **assente** | `<details>` |
| Cronologia completa (timeline eventi) | `<details>` sempre reso | **assente** | `<details>` |
| Copy `host.yaml` (callout credenziali) | «completa il file host.yaml» | «**Connessioni non configurate**» | «host.yaml» |

### Altre schermate

| Schermata | Cambio |
|---|---|
| `screen_preflight` | «host.yaml presente/mancante» solo in esperto; operatore vede «Connessioni configurate» senza nome-file |
| `screen_migrazione` («Cosa verrà migrato») | le 3 liste-documento → **3 card semplici** (Automatico / Manuale-verificabile / Escluso), riuso classi `cockpit-plan-*`. Coverage tecnica resta collassata sotto `<details>`. Form scope + form avvio (azioni operatore) invariati |
| `screen_applica` («Azioni avanzate») | **invariata** — già percorso esperto (banner «Percorso esperto», apply/verify singoli, DNS Danger Zone) |
| `screen_conferme`/`screen_inventario`/`screen_chiusura` | invariate (già operatore-appropriate) |

## Invarianti di sicurezza (verificate, go-reviewer APPROVE)

1. **Gate CTA intatto:** `startAllowed(status, f, plan, job)` in
   `workbench_cockpit.go` ha **diff zero**. `Expert` non lo raggiunge. Hero e CTA
   derivano entrambi dallo stesso `allowed` → **non possono divergere in nessuna
   modalità** (il bug bloccante hero↔CTA di PR #82 non può tornare). Il define
   `startMigrationForm` non ha rami `.Expert`/`.ModeQuery`: action target e copy
   di conferma identici in operatore ed esperto.
2. **Nessun controllo rimosso, solo ricollocato:** i form governance (`/status`)
   e attach (`/attach`) sono spostati in modalità esperto, ciascuno con
   `name="csrf"`. Gli handler POST sono invariati (raggiungibili via esperto).
3. **CSRF/strong-confirmation/DNS Danger Zone/rollback gated-by-backup/
   single-writer slot:** invariati. Il token CSRF è una costante per-server
   (`webui.go`), identica su ogni pagina.
4. **Motore intatto:** `internal/workbench` e `internal/accountinventory` non
   compaiono nel diff.

### Fix catturato: `Session.LastError`

`Session.LastError` (campo safety-critical) era renderizzato **solo** dentro il
blocco «Dettagli tecnici». Gating quel blocco dietro `.Expert` lo avrebbe
**silenziosamente nascosto** all'operatore di default — esattamente il tipo di
regressione che questa PR doveva evitare. Fix: render **incondizionato**
`fdhead-error` nel `fdHeader` (visibile su ogni schermata via `wbHead`), più il
render dell'errore del job fallito già presente nell'hero (via `CTA.Detail`).
Regressione bloccata da `TestOperatorShowsLastError`.

## Test (TDD)

Nuovo `internal/webui/workbench_operator_mode_test.go` (12 test) + 4 test
esistenti aggiornati al nuovo comportamento (governance/`host.yaml`/«Stato per
fase» ora esperto-only) + 1 test timeline riallineato allo split + `extractCSRF`
riallineato (legge il token da `?mode=expert`, dove il form governance è sempre
reso).

Mappa dei 15 casi obbligatori del brief:

| # | Requisito | Test |
|---|---|---|
| 1 | No Governance nel path operatore | `TestOperatorPanoramicaHidesGovernanceExpertShows` |
| 2 | No SHA/artifact raw nel path operatore | `TestOperatorPanoramicaHidesArtifactsExpertShows` |
| 3 | Una sola CTA dominante | `TestOperatorPanoramicaSingleDominantCTA` |
| 4 | host.yaml non nel copy operatore | `TestOperatorHostYAMLCopyNeutralExpertTechnical` + `TestPreflightHostYAMLExpertOnly` |
| 5 | host.yaml solo nei dettagli tecnici | idem (ramo esperto) |
| 6 | Cambio stato governance solo esperto | `TestOperatorPanoramicaHidesGovernanceExpertShows` + `TestCockpitTechnicalCollapsedAndAdvancedDemoted` |
| 7 | Attach report solo esperto | `TestOperatorPanoramicaHidesArtifactsExpertShows` |
| 8 | Apply/verify etichettate avanzate/esperte | `TestCockpitTechnicalCollapsedAndAdvancedDemoted` + `TestApplicaDangerZone` |
| 9 | DNS apply resta Danger Zone | `TestApplicaDangerZone` (esistente) |
| 10 | «Cosa verrà migrato» = 3 card | `TestMigrazioneThreeSimpleCards` |
| 11 | Coverage tecnica collassata | `TestMigrazioneCoverageCollapsed` |
| 12 | Timeline completa collassata | `TestOperatorHidesFullTimelineExpertCollapsed` |
| 13 | Ultimo errore umano visibile | `TestOperatorShowsLastError` |
| 14 | CTA Avvia usa il gate condiviso | `TestModeDoesNotAffectStartGateFailed` + `TestStartableSessionShowsStartFormBothModes` |
| 15 | Nessuna regressione strong-confirmation | `TestApplicaDangerZone` + `TestCockpitReadyShowsStartCTA` (esistenti) |

Gate: `gofmt -l` pulito · `go test ./internal/webui/... ./internal/workbench/...
./internal/config/...` verde · `go test -race ./internal/webui/...
./internal/workbench/...` verde · `go vet ./...` pulito · `git diff --check`
pulito · **Docker LINUX_ALL_GREEN** (go1.25.11 linux, ~20 pkg, 0 FAIL, eseguito
davvero) · **go-reviewer APPROVE** (0 blocking).

## Rischi aperti / follow-up

1. **Rail «Applica e verifica» (pre-esistente, non regressione).**
   `buildTimeline` (`workbench_flightdirector.go`) include sempre lo step
   `screen_applica` nella left-rail, in entrambe le modalità: la superficie più
   tecnica (apply/verify singoli, DNS Danger Zone) resta a un click
   dall'operatore. La schermata è già etichettata «Azioni avanzate / Percorso
   esperto». Follow-up: gating/relabel della rail per modalità.
2. **Persistenza modalità.** Sticky solo sui **link** di navigazione guidata;
   **non** propagata attraverso i redirect POST (`/scope`, `/start-migration`) —
   scelta deliberata per non toccare l'orchestratore safety-critical. Un esperto
   che conferma lo scope o avvia la migrazione torna alla vista operatore
   (azioni operatore-primarie, ritorno voluto).
3. **Testi guida «avanzalo in Governance».** `nextAction` (stati
   `inventory_ready`, `blocked`) cita ancora «Governance», ora esperto-only.
   Non è un vicolo cieco (raggiungibile via toggle); formulazione raffinabile.

## Come estendere

- **Nuova superficie operatore:** aggiungila al template fuori dal blocco
  `{{if .Expert}}`. Usa linguaggio operatore, nessun nome-file/enum/SHA.
- **Nuova superficie esperto/tecnica:** mettila dentro `{{if .Expert}}` (o un
  `<details class="section">` interno). Se contiene un dato safety-critical che
  l'operatore DEVE vedere (es. un errore reale), rendine una sintesi operatore
  incondizionata — vedi il pattern `fdhead-error` / `Session.LastError`.
- **Nuovo link di navigazione guidata:** appendi `{{.ModeQuery}}` (o
  `{{$.ModeQuery}}` dentro un `range`) per mantenere la modalità sticky.
- **Regola d'oro:** il flag `Expert` è solo presentazione. Non passarlo mai a
  `startAllowed` né a nessuna logica di gating/scrittura.
