# Sessione 2026-07-05/06 — webui: traduzione IT azioni manuali (#67) + UI moderna / design system (#68)

Report completo della sessione. Due interventi, entrambi **solo-presentazione**
(motore, sicurezza, comportamento invariati; zero regressioni; tutto in italiano).

- **PR #67** (merged) — traduzione IT dei contenuti delle azioni manuali.
- **PR #68** (merged) — design system condiviso + landing "entry unico" (UI moderna).

Riferimenti: `MANUAL_ACTIONS_IT_DESIGN.md` (design doc traduzione), `DEVELOPMENT_STATE.md`
(ledger #67/#68), `HANDOFF_NEXT_SESSION.md` (stato post-merge).

---

## Parte 1 — Traduzione IT azioni manuali (PR #67)

### Problema
Le azioni manuali del checklist (`ManualAction.Title` / `OperatorAction`) erano
l'ultimo residuo EN della UI, identiche su dashboard #61 (`index.html`) e workbench
#66 (schermate Conferme/Chiusura). Composte dal motore in
`internal/accountinventory/checklist.go` (+ `dnsplan.go` per `op.Reason`→Detail).

### Analisi (ground truth verificata)
La UI mostra come prosa traducibile **solo** `Title` e `OperatorAction`. `Detail` è
un diff di valori (`ea-php80 → ea-php82`, `v=spf1 … → …`, `routing: local → auto`):
**dato tecnico**, resta verbatim. `Type` (`CONFIRM_DNS_RECORD`…) resta grezzo.

- `Title` = prosa statica + coda dinamica (nome dominio/record). 25 pattern.
- `OperatorAction` = prosa statica pura (una frase per call-site). 27 stringhe (31
  concrete con le 5 varianti `noun`).
- Tutti i literal sono in `checklist.go` (incl. `addPlanManualAction`); `dnsplan.go`
  produce solo `op.Reason`→Detail.

### Decisione di design — layer di PRESENTAZIONE, non alla sorgente
Motivo **decisivo** (verificato nel codice):

1. **Chiave di acceptance.** `manualActionKey = sha256(type\0section\0title\0detail)`
   (`checklist.go:1068`). Tradurre nel motore cambierebbe ogni `AK-*` → le
   acceptances salvate non combaciano più (fail-safe → azioni riappaiono pending →
   verdetto regredisce). Violerebbe "nessuna regressione".
2. **La UI legge l'artifact JSON congelato** (`os.ReadFile`+`json.Unmarshal`,
   `workbench_view.go:110`, `webui.go:473`), **non rigenera**. Tradurre a
   presentazione **ri-renderizza il JSON congelato ad ogni view** → la sessione
   reale `mig_20260704` diventa IT **subito**, senza rigenerare, senza toccare
   chiavi/JSON/golden/.md.

Correzione onesta all'ipotesi "mappa per Type": un `Type` ha più titoli/operator
diversi per finding → la chiave è la **frase**, non il Type. Il traduttore mappa la
prosa statica (exact/prefix) e lascia verbatim la coda dinamica.

### Implementazione
- `internal/webui/manualaction_it.go` — funzioni pure (pattern esistente
  `statusLabelIT`/`overallLabelIT`):
  - `manualActionIT(a)` — mappa **exact-match** EN→IT (le 5 varianti `noun` enumerate).
  - `manualTitleIT(a)` — regole ordinate: static esatto → famiglia `noun` (`Recreate
    <noun> <ref> on the destination`) → famiglia by-hand (`Resolve/Review the <T>
    record <name> by hand`, mid `" record "`) → prefissi **longest-first** → fallback raw.
- Registrate nel `FuncMap` di **entrambi** i set (`workbench.go`; `webui.go` con
  `template.New("index.html").Funcs(...)` — evita la pagina bianca su `Execute`).
- Template: `{{manualTitleIT .}}` / `{{manualActionIT .}}` in `index.html`,
  `workbench_screens.html` (Conferme + Chiusura).

### Test / drift-guard
- Golden 29 title + 31 operator → IT; fallback su ignoto = raw.
- `TestManualITAnchorsPresentInEngineSource`: ogni anchor EN esiste ancora in
  `checklist.go` (cattura un **reword** del motore).
- `TestManualITNounSetCoveredBothWays`: estrae la noun-map dal motore e verifica
  copertura bidirezionale (cattura un **add** di noun).
- Render end-to-end: dashboard + Conferme rendono IT, EN non trapela.

### Validazione dati reali
Servito il checklist reale di `mig_20260704_1a4eaa2cc7d7` via HTTP: **6/6 azioni in
IT, zero prosa EN**, `Detail`/`AK`/`Type` verbatim (apostrofi HTML-escaped corretti).

### Gate
go-reviewer (opus) R1 APPROVE (LOW-1/LOW-2 fixati) → R2 **APPROVE PULITO**; Docker
LINUX_ALL_GREEN **20 ok / 0 FAIL**.

---

## Parte 2 — UI moderna / design system condiviso (PR #68)

### Problema (analisi investigativa)
Due superfici distinte, look "HTML anni '90", CSS separato per pagina:
- `/` (dashboard #61) = cruscotto **tecnico grezzo** (form SSH → esegui → dump
  checklist in tabelle fitte). Entry confuso.
- `/workbench` (#66) = percorso guidato **già strutturato** (7 tab, prossima azione,
  semafori, verdetto) ma visivamente povero (bottone "Crea sessione" non stilizzato).

Il problema non era la struttura, era **l'assenza di un design system** e l'entry
point poco chiaro.

### Decisioni utente
- Scope: **design system condiviso + entry unico** (una landing su `/` che guida al
  workbench; look coerente su tutte le schermate).
- Stile: **professionale sobrio** (indaco/neutri, molto spazio bianco, card pulite —
  stile Linear/Stripe dashboard).

### Architettura
Nuovo `internal/webui/templates/_theme.html` con due `{{define}}`:
- `themeCSS` — l'intero design system in un `<style>` (ridefinisce **le stesse
  classi già usate** dal markup → restyle senza toccare la struttura).
- `appHeader` — app bar condivisa (brand + nav Cruscotto/Sessioni guidate, con
  **stato attivo** passato come chiave: `dashboard` | `workbench`).

Parsato in **entrambi** i set di template (aggiunto a `//go:embed` **e** `ParseFS`
sia in `webui.go` sia in `workbench.go`). Un set che non lo includa → panic
"template not defined": entrambi lo includono.

### Cosa cambia per superficie
- **`/`**: app bar + **hero** indaco con CTA «Apri le sessioni guidate →» +
  «Modalità avanzata»; sezioni (connessioni, esegui, monitor, verdetto, sezioni,
  azioni manuali, artefatti) in **card**; tabelle in `.tablewrap`.
- **`/workbench`**: lista sessioni in tabella moderna con badge di stato; "Crea
  nuova migrazione" in card.
- **7 schermate**: `wbHead` usa `themeCSS`+`appHeader`+`.container`; `wbFooter`
  chiude `.container`. Stepper (`wbnav`) a **pillole** con attiva in indaco;
  call-out "prossima azione"; semafori; Conferme/Applica in card; **Danger Zone
  DNS** invariata nella semantica (checkbox attestazione disabilita `dns-apply-btn`);
  bottoni sola-lettura resi `btn-secondary`, destructive `danger` (rossi).

### Non-regressione (verificata)
- Preservati id/campi form critici (`dns-apply-btn`, `dns-standalone-attest`,
  `migrate_content`, `confirm_account`, `action_key`), CSRF, glifi ✅🟡⚪, badge,
  verdetti, redazione segreti, traduzioni IT (#67).
- Go toccato **solo** su 4 righe (embed + ParseFS). XSS auto-escaping intatto
  (nessun `| safe`/`template.HTML`).
- Vincolo test unico: `class="status stalled/completed"` e "Monitor esecuzione"
  solo dentro `{{if .Monitor}}` (monitor_test).
- I ~30 test di schermata restano verdi.

### Validazione — browser-walk reale (5 superfici)
Sessione reale `mig_20260704` servita via HTTP, screenshot di: dashboard (hero+CTA+card),
lista sessioni, Panoramica (stepper+semafori+governance), Conferme (azioni IT in card),
Applica (danger zone rossa + conferme forti). Tutte coerenti, moderne, IT.

### Gate
go-reviewer (opus) adversariale **APPROVE** (nessun CRITICAL/HIGH/MEDIUM; 1 LOW
chiuso = stato attivo nav; 1 LOW cosmetico accettato = CSS inline ~5 KB/risposta,
irrilevante in locale). Docker LINUX_ALL_GREEN **20 ok / 0 FAIL** sul commit finale.

---

## Reference — design system (per restyle/estensioni future)

Fonte unica: `internal/webui/templates/_theme.html`. Per cambiare l'aspetto di
**tutta** la UI si edita `themeCSS` lì. Nessun asset esterno (loopback); CSS inline
(CSP consente: solo `frame-ancestors 'none'`).

### Token (`:root` custom properties)
`--bg` #f5f6f8 · `--surface` #fff · `--surface-2` #f9fafb · `--border` #e5e7eb ·
`--text` #1f2a37 / `--text-strong` #101828 / `--muted` #667085 · accento
`--accent` #4f46e5 (indaco) + `--accent-hover` / `--accent-soft` / `--accent-border` ·
semantici `--ok` / `--warn` / `--danger` (+ `-bg` / `-border`) · `--radius` 12px ·
`--shadow` / `--shadow-sm` · `--mono`.

### Classi principali
- Shell: `.appbar` + `.appbar-in` + `.brand` + `.appnav a[.active]`; `.container`
  (max 76rem, centrato); `.card`; `.crumbs`; `.hero`.
- Bottoni: `button`/`.btn`/`.btn-primary` (indaco), `.btn-secondary` (neutro),
  `.btn-ghost`, `button.danger`/`.btn.danger` (rosso), `:disabled`. min-height 40px.
- Tabelle: avvolgere in `.tablewrap`; `th` uppercase muted su `--surface-2`, hover riga.
- Badge/pill: `.badge` + `draft|active|done|error|archived`; `.verdict` + `ok|bad|warn`.
- Banner/alert: `.banner.stale|warn`, `.warnbox`, `.status.failed|completed|running|stalled`.
- Guida: `.wbnav a[.active]` (stepper), `.nextbox` (call-out), `.sema`+`.dot`
  (`ok|partial|ready|todo`), `.dangerzone`, `.verdict-yes|no`.
- Form: `fieldset`/`legend`/`label`/`input`/`select` (focus ring accento), `input[type=checkbox]`.
- Vari: `.muted`/`.faint`, `.mono`/`code`, `.meta` (dl), `.timeline`, `.empty`, `.row`, `.cols`.

### Aggiungere una schermata workbench
Definire `{{define "screen_<nome>"}} {{template "wbHead" .}} {{template "wbNav" .}}
… {{template "wbFooter" .}} {{end}}` in `workbench_screens.html` — eredita
automaticamente design system, app bar, container. `wbFooter` chiude `.container`.

### Header con stato attivo
`{{template "appHeader" "dashboard"}}` su `/`; `{{template "appHeader" "workbench"}}`
sulle pagine workbench.

---

## Stato finale UI

- **Interamente in italiano** (prosa); grezzo solo il dato tecnico (Detail=diff
  valori, Type, code POL-*/AK-*).
- **Look moderno e coerente** su dashboard, lista sessioni e 7 schermate guidate.
- **Entry unico**: `/` è una landing che porta al percorso guidato; modalità avanzata
  (pipeline/checklist) accessibile sotto.
- Sessione reale `mig_20260704_1a4eaa2cc7d7` resta a `ready_for_cutover`, rende
  correttamente in IT + nuovo look. Produzione .193 intatta.

## Avvio UI (promemoria)
```
go build -o cpanel-self-migration ./cmd/cpanel-self-migration
./cpanel-self-migration ui --dir <cartella-artefatti> --listen 127.0.0.1:8422
# dashboard http://127.0.0.1:8422/ · sessioni guidate http://127.0.0.1:8422/workbench
```
Il workbench legge le sessioni da `~/.cpanel-self-migration/migrations/` (nome
canonico artifact `migration_checklist.json` nella cartella condivisa).

## Prossimi passi (invariati)
- **Finestra di cutover** — decisione utente (data/orario/variante sync DNS/ordine
  account, `CUTOVER_RUNBOOK.md` §7).
- **Nota amministrativa Orbit** — registrare via `create_intervention` le scritture
  sul sacrificale .78 quando il TOTP torna disponibile.
