# Design — traduzione IT dei contenuti azioni manuali (Title / OperatorAction)

Stato: PROPOSTA (pre-implementazione). Sessione 2026-07-05.

## Problema

Le azioni manuali del checklist (`ManualAction`) sono l'ultimo residuo EN della
UI, visibili identiche su **dashboard #61** (`index.html`) e **workbench #66**
(`workbench_screens.html`, schermate Conferme e Chiusura). Composte dal motore
in `internal/accountinventory/checklist.go` (+ `dnsplan.go` per le op DNS
manuali).

## Cosa mostra davvero la UI (verificato nei template)

| Campo | Dashboard #61 | Workbench #66 | Natura |
|---|---|---|---|
| `Title` | riga 165 | riga 71, 183 | prosa statica + coda dinamica (nome/ref) |
| `OperatorAction` | riga 165 | riga 73, 183 | **prosa statica pura** (una frase per call-site) |
| `Detail` | non mostrato | riga 72 ("Perché serve:") | **diff di valori** `sorgente → dest` (dato tecnico) |
| `Type` | riga 164 | riga 74 | codice tassonomia (`CONFIRM_DNS_RECORD`…) |
| `Section`, `Key`, `ID` | sì | sì | identificatori |

Ground truth dalla sessione reale `mig_20260704_1a4eaa2cc7d7` (6 azioni): ogni
`Detail` è un diff di valori puro (`ea-php80 → ea-php82`, `v=spf1 … → …`,
`routing: local → auto`). Nessuna prosa da tradurre nei `Detail` di questa
sessione; gli unici `Detail` in prosa nel codice sono i `op.Reason` del dns-plan
(es. "NS/delegation is registrar/WHM territory — never written").

## Decisione di design

**Layer di presentazione, non alla sorgente.** Si aggiungono due funzioni pure
di traduzione (`manualTitleIT`, `manualActionIT`) e si registrano nel `FuncMap`
dei template, **esattamente come il pattern esistente** `statusLabelIT` /
`stepLabelIT` / `overallLabelIT` / `sectionLabelIT` (già in `workbench_view.go`).
I template chiamano `{{.Title | manualTitleIT}}` e `{{.OperatorAction | manualActionIT}}`.

### Perché NON alla sorgente (motore) — decisivo

1. **Chiavi di acceptance.** `manualActionKey = sha256(type\0section\0title\0detail)`
   (`checklist.go:1068`). Tradurre `title`/`detail` **cambia ogni `AK-*`** →
   le acceptances salvate smettono di combaciare (fail-safe → azioni riappaiono
   pending → verdetto regredisce). Viola il vincolo "nessuna regressione".
2. **La UI legge l'artifact JSON congelato** (`os.ReadFile` +
   `json.Unmarshal`, `workbench_view.go:110`, `webui.go:473`), **non rigenera**.
   - Tradurre alla sorgente NON tocca gli artifact già scritti → la sessione
     reale resterebbe EN comunque, a meno di rigenerare (che cambia le chiavi).
   - Tradurre a presentazione **ri-renderizza il JSON congelato ad ogni view** →
     la sessione reale `ready_for_cutover` diventa IT **subito**, senza rigenerare
     nulla, senza toccare chiavi/acceptances.
3. **Zero churn su golden test / JSON / .md.** Le stringhe del motore, l'artifact
   JSON e il `migration_checklist.md` restano invariati byte-identici.

### Correzione onesta all'ipotesi "mappa per Type"

La consegna suggeriva "mappa IT per Type + interpolazione". **Non regge**: un
Type ha più titoli/operator diversi per finding (es. `CREATE_ON_DESTINATION` ha
4 titoli distinti). La chiave reale è la **frase specifica del finding**, non il
Type. Quindi il traduttore mappa la **prosa statica** (prefisso/suffisso), non il
Type, e lascia verbatim la coda dinamica (nome/ref/valore).

## Scope della traduzione

- **Tradotti**: `Title`, `OperatorAction`.
- **Verbatim (non tradotti)**: `Detail` (diff di valori = dato tecnico, coerente
  con "codici di riferimento tecnici restano grezzi"), `Type`, `Section`, `Key`,
  `ID`, e le code dinamiche dei titoli (nomi dominio, record, valori).
- **Fuori scope**: l'artifact `migration_checklist.md` (file scaricabile, non
  chrome UI) e il JSON restano EN — coerente con il non-toccare-la-sorgente.
- **`op.Reason` in prosa dentro `Detail`** (solo dns-plan manuale): resta EN,
  perché `Detail` non è tradotto per policy. È un dettaglio-evidenza tecnico e
  raro; documentato come residuo consapevole (evita parsing fragile del campo dati).

## Implementazione

- Nuovo file `internal/webui/manualaction_it.go` — sole funzioni pure, zero I/O.
- `manualActionIT(op string) string`: **mappa exact-match** EN→IT (map + fallback
  al raw). Le varianti con `noun` interpolato (`Recreate the <noun> on the
  destination…`) sono **enumerate** per i 5 nomi noti (mailbox/database/forwarder/
  autoresponder/FTP account) → nessuna regola, solo voci statiche.
- `manualTitleIT(t string) string`: lista **ordinata** di regole
  `{enPrefix, enMid, enSuffix} → {itPrefix, itMid, itSuffix}`:
  - statiche pure (nessuna coda): match esatto;
  - prefisso + coda: `HasPrefix(enPrefix)` → `itPrefix + coda`;
  - "Recreate <noun> <ref> on the destination": mappa noun→IT + ref verbatim;
  - "Resolve/Review the <T> record <name> by hand": `enMid=" record "`,
    `enSuffix=" by hand"` → `"Risolvi/Rivedi a mano il record " + T + " " + name`.
  - fallback: raw (EN) se nessuna regola combacia.
- Registrazione: aggiungere le 2 funzioni al `funcMap` di `workbench.go:26` e
  aggiungere `.Funcs(funcMap)` (con le 2 funzioni) al parse di `index.html`
  (`webui.go:126`). Struct e chiavi restano intatte.

## Tabella pattern → IT (bozza, rifinita in TDD)

### OperatorAction (exact-match)
| EN | IT |
|---|---|
| Test the site against the destination PHP configuration before cutover. | Testa il sito con la configurazione PHP della destinazione prima del cutover. |
| NS records differ; confirm the intended delegation at the registrar/WHM level. | I record NS differiscono; conferma la delega voluta a livello di registrar/WHM. |
| TXT records often bind external services (SPF/DKIM/verification); confirm the destination value is intended. | I record TXT spesso legano servizi esterni (SPF/DKIM/verifiche); conferma che il valore sulla destinazione sia quello voluto. |
| Email Routing differs between source and destination; a wrong local/remote value silently breaks delivery. | L'instradamento email differisce tra sorgente e destinazione; un valore local/remote errato interrompe silenziosamente la consegna. |
| … (elenco completo ~28 voci in TDD) | … |

### Title (regole)
| EN pattern | IT |
|---|---|
| `Check PHP compatibility for <ref>` | `Verifica la compatibilità PHP per <ref>` |
| `Confirm delegation (NS) for <ref>` | `Conferma la delega (NS) per <ref>` |
| `Confirm mail routing for <ref>` | `Conferma l'instradamento email per <ref>` |
| `Confirm mail routing (MX) for <ref>` | `Conferma l'instradamento email (MX) per <ref>` |
| `Verify the changed TXT record <ref>` | `Verifica il record TXT modificato <ref>` |
| `Create the main domain on the destination` | `Crea il dominio principale sulla destinazione` |
| `Recreate <noun> <ref> on the destination` | `Ricrea <noun-IT> <ref> sulla destinazione` |
| `Resolve the <T> record <name> by hand` | `Risolvi a mano il record <T> <name>` |
| … (elenco completo ~25 regole in TDD) | … |

## Test / non-regressione

1. **Unit (RED→GREEN)**: `manualaction_it_test.go` con tutti i pattern → IT
   attesi; casi di coda dinamica preservata; fallback su stringa ignota = raw.
2. **Drift-guard**: test che esegue il **builder reale** su una fixture che
   emette il massimo dei tipi di azione, raccoglie ogni `Title`/`OperatorAction`
   prodotto e asserisce che il traduttore li riconosca tutti (nessun fallback
   EN silenzioso). Se il motore cambia una stringa, il test fallisce.
3. **Golden invariati**: `checklist_test.go`, `checklist_write_test.go`, i .md
   golden e il JSON NON cambiano (nessun byte). Verifica: `go test ./...` verde.
4. **Manuale**: la sessione reale `mig_20260704` renderizza IT su Conferme /
   Chiusura senza rigenerare l'artifact.

## Riesame opus — modifiche adottate (2026-07-05)

Verifiche: A VERO, B VERO, D VERO (con nota: `Detail` **è** mostrato nel
workbench riga 72, ma trattato come dato → resta EN, residuo consapevole),
E VERO. C **parzialmente falso**: i `Detail` in prosa non sono solo `op.Reason`
del dns-plan ma anche `policy.go:355` ("certificate entry carries no domain list
— verify manually") e `:111`. Restano tutti verbatim per policy (Detail non
tradotto), elencati come residui consapevoli.

1. **Registrazione index.html (regressione da evitare).** `webui.go:428` usa
   `s.tpl.Execute(w, p)` sul template root. Passare a `template.New("")` lo
   renderebbe **bianco**. Fix corretto: `template.New("index.html").Funcs(fm).
   ParseFS(templatesFS, "templates/index.html")` (root name = file). Test di
   render end-to-end della dashboard deve restare verde.
2. **Firma robusta.** Le due funzioni prendono l'intera `ManualAction`
   (`{{manualTitleIT .}}`, `{{manualActionIT .}}`), così `a.Type` disambigua i
   prefissi condivisi ("Recreate " → CreateOnDestination vs EmailFilters vs Cron)
   senza dipendere dall'ordine. Regole titoli: **exact-static prima, poi prefissi
   longest-first, poi famiglia "by hand" (`mid=" record "`, `suffix=" by hand"`)
   che collassa T20/T22/T23**.
3. **Drift-guard bidirezionale + coverage-completo.** (a) golden inventory
   hard-coded (29 title + 31 operator, sotto) → assert ognuno tradotto (≠ raw);
   (b) assert che ogni EN del golden esista `strings.Contains` in `checklist.go`
   (pin golden↔sorgente: un edit motore a una stringa nota fa fallire il test);
   (c) test sul builder reale (artifact giorginisposi) → ogni Title/Operator
   emesso è riconosciuto. Limite onesto residuo: un **nuovo** tipo di azione con
   nuova stringa non è colto in automatico (fallback raw EN visibile a schermo +
   gate go-reviewer).
4. **Sorgente stringhe.** TUTTI i Title/OperatorAction letterali sono in
   `checklist.go` (incluse le op DNS manuali, `addPlanManualAction` :748-774).
   `dnsplan.go` produce solo `op.Reason` (→ Detail). Doc corretto.

### Inventario contratto (dal riesame)
29 Title concreti (25 pattern, T3 ×5 noun) + 31 OperatorAction concrete (27
stringhe, O3 ×5 noun). Tutte le OperatorAction sono statiche pure → mappa
exact-match al 100%. Solo i Title hanno code dinamiche. Elenco completo con
file:riga incollato nel test `manualaction_it_test.go` come golden.

## Gate

TDD; go-reviewer (opus) multi-giro fino APPROVE pulito; Docker LINUX_ALL_GREEN;
gate dichiarato nel body PR; fork-only `--repo andreavadacchino/cPanel_self-migration`;
handoff post-merge.
