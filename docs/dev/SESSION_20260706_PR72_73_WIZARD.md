# Sessione 2026-07-06 — New Migration Wizard + scope-aware UI (PR #72, #73)

Report della sessione che ha consegnato il **setup flow** operatore e la sua
coerenza a valle. Due PR piccole e mirate, entrambe **mergiate** su `main` del
fork.

| PR | Titolo | Merge | Ambito |
|----|--------|-------|--------|
| #72 | `feat(webui): setup flow for new migration wizard` | `43f29d6` | Wizard + modello dati + gating schermo Applica |
| #73 | `feat(webui): make next actions scope-aware` | `3c30b45` | Banner «prossima azione» coerente con lo scope |

Principio guida della fase (Flight Director): *la UI non deve chiedere
all'operatore di ragionare in termini di artifact/policy/session state; deve
guidarlo nella creazione di una migrazione concreta.*

---

## 1. Contesto e problema

Prima di questa sessione, creare una sessione di migrazione significava solo
inserire `name` + due profili testuali (`source_profile`/`destination_profile`).
Non equivaleva a **definire** davvero sorgente, destinazione, account e contenuti
da migrare. Il flusso iniziale era tecnico e povero.

Obiettivo prodotto: `nuova migrazione → sorgente → destinazione → account →
cosa migrare → preflight`, in linguaggio operatore.

---

## 2. PR #72 — New Migration Wizard (setup flow)

### 2.1 Cosa

Nuovo percorso guidato `GET/POST /workbench/new`: nome + dominio + note →
server sorgente → server destinazione → account cPanel → **cosa migrare** →
riepilogo/preflight. Alla creazione porta alla Panoramica della sessione, con il
**controllo preliminare (preflight)** come prossima azione.

### 2.2 Modello dati (`internal/workbench/types.go`)

```go
type Endpoint struct {            // NESSUN campo segreto (per costruzione)
    Host    string `json:"host,omitempty"`
    Port    int    `json:"port,omitempty"`
    Account string `json:"account,omitempty"` // cPanel account == utente SSH
}

type ContentSelection struct {    // DNS deliberatamente separato
    Files, Databases, Email, EmailConfig, Cron, DNS bool
}

type SetupMeta struct {
    PrimaryDomain string
    Notes         string
    Source        Endpoint
    Destination   Endpoint
    Content       ContentSelection
}

// Session gains:
Setup *SetupMeta `json:"setup,omitempty"`   // pointer → sessioni vecchie leggono
```

`Store.CreateWithSetup(name, srcProfile, dstProfile, setup, now)` — il vecchio
`Create` delega con `setup=nil`. Stessa atomicità/lock/permessi 0600.

### 2.3 Decisione credenziali — **metadata-only** (motivata dal codice)

Scoperta decisiva ispezionando `internal/webui/webui.go`: il form `/config`
**già** scrive `host.yaml` con `ssh_pass` in modo sicuro (0600, atomico,
validato da `config.Load`, password vuota eredita, mai ri-mostrata).

Quindi:

- il wizard **non raccoglie né persiste segreti** — `Endpoint` non ha campo
  segreto, e l'handler costruisce `SetupMeta` da una **whitelist esplicita** di
  campi (nessun binding generico): un `src_pass`/`password` iniettato nel POST è
  strutturalmente inerte;
- le credenziali restano in `host.yaml`, configurate dal form `/config`
  esistente; una **callout** nella Panoramica collega i due finché `host.yaml`
  non esiste;
- il wizard **non genera** `host.yaml` (scelta minima e robusta: la gestione
  segreti esiste già e non va duplicata).

Garanzie anti-leak: `TestEndpointHasNoSecretField` (strutturale), canary su
disco (`TestWizardNoSecretLeaksToDisk`), `TestSessionJSONNoCredentialFields`
ristretto ai nomi realmente segreti (host/port ora sono coordinate legittime
non segrete, non credenziali).

### 2.4 DNS separato e opt-in

Nel wizard il DNS è in una `dangerzone` a parte con avviso (raggiunge i
nameserver di produzione), **mai** incluso da un gesto «migra tutto» (che non
esiste). È un flag isolato.

### 2.5 Gating dello schermo «Applica»

La scelta contenuti **governa la UI operativa**, non è più descrittiva:

- `contentScope` + `deriveContentScope(sess)` in `workbench_view.go` → booleani
  `Include{Files,Databases,EmailContent,EmailConfig,Cron,DNS}` +
  `ShowMigrateContent`, esposti come `workbenchView.Scope`;
- regola: `Setup==nil` (legacy/avanzato) → tutto incluso (**invariato**);
  `Setup!=nil` → solo le aree selezionate;
- template `screen_applica`: aree escluse mostrano nota «non incluso/non incluse
  in questa migrazione» (linguaggio operatore), niente `apply`/`verify`/
  `rollback`; `migrate_content` mostra solo le checkbox File/Database/Email
  selezionate ed è nascosto se nessuna; il DNS mantiene danger zone + conferma
  forte solo con `DNS=true`.

### 2.6 File principali

- `internal/workbench/types.go`, `internal/workbench/store.go`
- `internal/webui/workbench_wizard.go` (nuovo), `templates/workbench_new.html`
  (nuovo)
- `internal/webui/workbench_view.go`, `templates/workbench_screens.html`,
  `templates/workbench_detail.html`, `templates/workbench_list.html`
- `internal/webui/webui.go`, `workbench.go` (routing + embed)

---

## 3. PR #73 — Next actions scope-aware

### 3.1 Debito chiuso

Il banner «prossima azione consigliata» (`nextAction`) citava ancora
`(contenuti, email, cron, DNS)` anche quando email/cron/DNS erano esclusi nel
wizard — falsa confusione (LOW segnalato nella review di #72).

### 3.2 Cosa

In `internal/webui/workbench_view.go`:

- helper su `contentScope`: `applyAreaLabels()`, `includedAreaLabels()`,
  `includedDetail()`;
- firma `nextAction(status, facts, scope)` e `missingVerifies(facts, scope)`;
  `areaOrder` con predicato `inScope` (DNS→`IncludeDNS`,
  Email→`IncludeEmailConfig`, Cron→`IncludeCron`);
- **preflight/bozza**: il dettaglio elenca le aree incluse («Questa migrazione
  include: File del sito, Database.») — vuoto per legacy;
- **pronto per applicare**: cita solo le aree incluse; DNS incluso → dettaglio
  **prudente** («DNS incluso come area delicata: verifica piano e conferme prima
  di applicare.»);
- **applicazione/verifica**: `missingVerifies` salta le aree fuori scope.

### 3.3 Compatibilità legacy

`Setup==nil` → scope include tutto: `applyAreaLabels()` ramo `!HasSetup` ritorna
i 4 label storici → testo apply **byte-identico**; dettaglio preflight vuoto; il
dettaglio DNS-prudente è gated da `HasSetup` (non scatta per legacy). I test di
view esistenti passano uno `legacyScope()` e restano invariati nell'intento.

---

## 4. Limite dichiarato (onesto)

Il gating è **frontend-only**. L'endpoint `/exec` **non** è gateato server-side
(fuori scope: nessuna modifica ad apply/verify/runner). La protezione di
scrittura reale resta la **conferma forte per-account** (digitare il nome
account). Il reviewer ha verificato che nascondere le checkbox `migrate_content`
ne impedisce l'invio da browser normale; un POST artigianale potrebbe tentare
l'azione ma richiede comunque la conferma forte. Candidato follow-up se emerge
la necessità.

---

## 5. Gate eseguiti (reali, non dichiarati a vuoto)

Per **entrambe** le PR:

- `gofmt -l` pulito sui file toccati;
- `go test ./internal/webui/... ./internal/workbench/... ./internal/config/...` ✅;
- `go vet ./...` ✅; `go test -race ./internal/webui/... ./internal/workbench/...` ✅;
- **Docker `golang:1.25` LINUX_ALL_GREEN** sul HEAD finale (intero `./...` + `vet`, zero FAIL);
- `internal/migrate` e `internal/webfiles` falliscono **solo su macOS** (differenze
  GNU/BSD: `sha256sum`, `sort -z`, `printf %P`) — dimostrato identico su `main`
  via `git stash`.

### Copertura TDD (RED → GREEN)

- #72: render form; submit valido crea sessione con setup; submit incompleto →
  400 leggibile; porta fuori range → 400; CSRF (403); DNS separato/non implicito;
  nessun contenuto → 400; anti-leak canary; redirect a sessione; next action =
  preflight; callout host.yaml (mostrata/nascosta); sessione legacy compatibile;
  CTA lista; gating schermo Applica (DNS/Cron/EmailConfig nascosti + note,
  migrate_content scoped, legacy mostra tutto, DNS-selected mantiene danger zone,
  anti-leak render).
- #73: `nextAction`/`missingVerifies` scope (files+db only; cron/dns/email-config
  esclusi; dns incluso prudente; legacy invariato; preflight include list) +
  render Panoramica che asserisce il testo apply scoped.

### Walk live reale (server `ui` loopback, curl)

- #72: `GET /workbench/new` → 200; `POST` reale con CSRF → 303 →
  `/workbench/session/mig_…`; Panoramica con «Definizione della migrazione»,
  `giorginisposi@192.168.1.193:2222`, «DNS non incluso», next action = preflight;
  `session.json` su disco = `"setup"`/`"dns":false`/`"account"`, **zero**
  `password`/`ssh_pass`.
- #72 gating: 3 sessioni (DNS-only / Files+DB / legacy) → gating confermato
  end-to-end (aree escluse = note, legacy = tutto visibile).

---

## 6. Review adversariale (go-reviewer, Codex)

- **#72 wizard**: APPROVE, 0 CRIT/HIGH; 1 LOW (errore template scartato in
  `renderWizard`) risolto con render su buffer → 500 pulito.
- **#72 gating**: APPROVE, 0 CRIT/HIGH/MEDIUM; verificato che
  `r.FormValue("scope_*")` default a false quando la checkbox è assente.
- **#73 scope-aware**: APPROVE, 0 CRIT/HIGH; 2 LOW cosmetici (numerazione
  commenti nei test; edge-case zero-contenuti già impedito dalla validazione
  wizard). Confermata la correttezza semantica del mapping Email→EmailConfig.

---

## 7. Fuori scope (rispettato)

SSE/WebSocket, Campaign Mode, queue multi-account, migrazioni parallele, nuovo
design system, Flight Director completo, comparative checklist, manual actions
verificabili, cutover gateway, nuovi writer/collector, root/WHM, host.yaml
generation, gating server-side di `/exec`. Nessun `writer`/`runner`/`apply`/
`verify`/`core migration` toccato.

---

## 8. Stato e prossima direzione

Il setup flow è completo e coerente: creare una migrazione ora definisce
sorgente/destinazione/account/contenuti, la scelta governa lo schermo Applica, e
il banner «prossima azione» non cita aree escluse. Le credenziali restano
metadata-fuori (host.yaml), la UI le collega.

**Prossimo blocco grande consigliato: PR 71 — Flight Director UI** (header
globale persistente, timeline laterale, main stage contestuale, next recommended
action, risk badge). **SSE ancora rimandata** a dopo un dogfooding reale su una
migrazione lunga.

Restano decisioni utente indipendenti dal codice: **finestra di cutover**
(data/orario/variante sync DNS/ordine account — `CUTOVER_RUNBOOK.md` §7) e la
**nota amministrativa** su Orbit (`create_intervention`) per le scritture sul
sacrificale .78 quando il TOTP torna disponibile.
