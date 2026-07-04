# Dogfooding #2 — ciclo UI-only di giorginisposi

**Data**: 2026-07-04
**Obiettivo**: verdetto finale di prodotto — un operatore può migrare un
account cPanel SOLO con la UI?
**Account**: giorginisposi .193 (source, PROD, read-only) → .78 (dest, sacrificale)
**Metodo**: click REALI nel browser via Chrome (nessun curl/script se non per
build, lancio `ui`, diagnosi bug). Sessione workbench `mig_20260704_1a4eaa2cc7d7`.
**Build**: da `main` @ `61f0dd0` (tool version `0.0.0-20260704150122-61f0dd02bac4`).

---

## VERDETTO

> **Un operatore può migrare un account cPanel SOLO con la UI: NO.**

Ma è un NO **profondamente diverso** da quello del report #1. Le 6 friction
strutturali del #1 sono **tutte chiuse e verificate con click reali**. Il ciclo
UI-only funziona end-to-end fino al `dns apply`, dove si ferma per **due**
motivi indipendenti — uno ambientale/da-diagnosticare, uno di design:

1. **[BLOCCANTE] `dns apply` fallisce** con errore server-side cPanel
   `DNS::mass_edit_zone: The request failed (Error ID …)` — **riproducibile
   2/2**, Error ID diverso ad ogni tentativo (m7sumx, qnrpvb). Senza un `dns
   apply` pulito, `dns verify` non è CLEAN → l'auto-transition a
   `ready_for_cutover` **correttamente** non scatta. Root cause nel log errori
   WHM (root-only): non diagnosticabile né risolvibile dalla UI.

2. **[GAP UI, HIGH per DNS clusterizzato]** La pre-condizione di sicurezza
   obbligatoria del `dns apply` (peer del cluster DNS su .78 **standalone**,
   rule #4) **non ha alcun affordance nella UI**. L'operatore deve verificarla
   fuori banda (dig ai NS pubblici / WHM root). Il passo di scrittura più
   pericoloso dipende da uno stato infrastrutturale invisibile al tool.

Tutto il resto del ciclo — connessioni, create, pipeline, plans, acceptances,
apply email/cron, verify, governance — è **UI-completo**.

---

## Timeline reale (per passo, dalla UI)

| # | Passo | Da UI? | Durata | Esito |
|---|-------|--------|--------|-------|
| 0 | build da main + lancio `ui --dir ./dogfood_giorginisposi` | terminale (atteso) | ~2s | OK |
| 1 | Dashboard: connessioni pre-caricate da host.yaml (src .193 / dest .78) | ✅ | — | OK, form funzionante |
| 2 | `/workbench` → **Create session** (form name/source/dest) | ✅ click | ~1s | `mig_20260704_1a4eaa2cc7d7` creata (draft) |
| 3 | **Run Pipeline** (SSH read-only .193 + .78) | ✅ click | ~20s (15:08:57→15:09:17) | 5 artifact **auto-allegati** con SHA256 |
| 4 | **DNS/Email/Cron Plan** (exec, offline) | ✅ 3 click | ~0s cad. | 3 plan auto-allegati (8 artifact) |
| 5 | **Acceptances** one-by-one sul dashboard | ✅ click | ~2s cad. | MA-001, MA-002, MA-013, MA-014 accettate |
| 6 | **Email Apply** (conferma forte `giorginisposi`) | ✅ click | ~3s | **applied=1** (routing→local) + backup, failed=0 |
| 7 | **Cron Apply** (conferma forte) | ✅ click | ~2s | 0 ops (no-op), failed=0 |
| 8 | **DNS Apply** (conferma forte) | ✅ click | ~8s | ❌ **failed=3** (m7sumx) — backup creato, nessun apply parziale |
| 9 | **Email/Cron Verify** | ✅ click | ~2s cad. | entrambi **clean=True** |
| 10 | **DNS Verify** | ✅ click | ~5s | **clean=False** (3 pending) |
| 11 | **Governance Set Status** draft→preflight_required | ✅ select+click | ~1s | transition matrix rispettata |
| 12 | **DNS Apply re-run** (verifica riproducibilità) | ✅ click | ~8s | ❌ **failed=3** (qnrpvb) — stesso errore |
| — | auto-transition → ready_for_cutover | — | — | **NON scattata** (dns non clean) — corretto |

---

## Confronto 1:1 con le 6 friction del report #1

| # | Friction #1 | Severità #1 | Stato in #2 | Evidenza |
|---|-------------|-------------|-------------|----------|
| **1** | No UI per creare sessione | HIGH | ✅ **CHIUSA** | Form "Create new migration" → session creata con click reale |
| **2** | Pipeline su pagina separata, no artifact attach | MEDIUM | ✅ **CHIUSA** | exec `run_pipeline` in-sessione, 5 artifact auto-allegati con SHA256 |
| **3** | `/run` non eseguito via browser (Origin) | LOW/test | ✅ **CHIUSA** | Ogni POST exec (pipeline/plan/apply/verify/accept) passa i gate via click reale nel browser nativo |
| **4** | No exec action per generare plans | HIGH | ✅ **CHIUSA** | DNS/Email/Cron Plan come exec actions, plan auto-allegati |
| **5** | Acceptances rigenerano chiavi (batch scriptato rotto) | MEDIUM | ✅ **CHIUSA (UI)** | One-by-one via click funziona: la pagina ricarica con chiavi fresche; provato su 3 famiglie (php-check, dns-confirm `blocking_cutover`, email-routing) |
| **6** | Blocker policy impedisce progressione (BLOCKED blocca tutto) | HIGH/DESIGN | ✅ **CHIUSA** | `apply_blocked=False` con `overall_status=BLOCKED`: l'Email Apply è andato a buon fine **con `POL-DNS-NS-CHANGED` presente**. Lo scoping PR61 funziona esattamente come progettato |

**Tutte e 6 le friction del #1 sono chiuse.** Il #1 era un "NO strutturale"
(mancavano pezzi di UI); il #2 è un "NO operativo" (la UI c'è tutta, ma il
`dns apply` inciampa su un errore server-side e su una pre-condizione fuori-UI).

---

## Friction NUOVE trovate in #2

### N1 — [BLOCCANTE] `dns apply` fallisce (server-side mass_edit_zone)

- **Sintomo**: `dns apply` → `failed=3`, `DNS::mass_edit_zone: status=0
  errors=[The request failed. (Error ID: m7sumx / qnrpvb)]`. Riproducibile
  2/2, Error ID nuovo ad ogni tentativo (fallimento server-side fresco).
- **Op interessati**: `add TXT _v2smoke`, `replace TXT default._domainkey`
  (DKIM, 2 segmenti 255+156), `replace TXT giorginisposi.it.` (SPF). Il tool
  li invia in **una singola** `mass_edit_zone` batch (`MassEditZoneBatch`:
  remove-0/remove-1 + add-0/add-1/add-2) → fallimento atomico di tutti e 3.
- **Diagnosi a livello utente (via SSH, per isolare bug vs ambiente)**:
  - `uapi DNS parse_zone` (read) → OK
  - `mass_edit_zone` **add** singolo (`_dogfootest`) → **OK** (serial→bump)
  - `mass_edit_zone` **remove** singolo → **OK**
  - `mass_edit_zone` **batch** remove-0+add-0 (indicizzato, TXT 1-segmento) → **OK**
  - Il data DKIM nel piano è **correttamente segmentato** (255 + 156, ≤255) → non è oversize-string
  - Il batch specifico del tool (2 remove di record esistenti DKIM/SPF + 3 add, con DKIM 2-segmenti) → **FALLISCE**
- **Conclusione**: la primitiva funziona; il fallimento è specifico del
  **batch multi-op con i `replace`** (probabile: replace di un TXT
  multi-segmento come la DKIM, oppure la combinazione 2-remove+3-add).
  **Root cause definitiva nel log errori WHM (root-only)** — non ottenibile a
  livello utente (`giorginisposi`), non risolvibile dalla UI.
- **Comportamento del tool: CORRETTO** — backup creato prima
  (`dns_backup.json`, 20 KB), fallimento atomico (nessun apply parziale:
  la DKIM su .78 resta quella dest-rigenerata, invariata), errore riportato
  con Error ID, nessuna falsa riuscita.
- **Classificazione**: da confermare se **bug di prodotto** (batching dei
  replace su TXT multi-segmento) o **quirk ambientale .78** (cluster/CageFS/
  integrità zona). Serve il log WHM root → **decisione utente su come procedere**.

### N2 — [GAP UI, HIGH per destinazioni con DNS clusterizzato]

La pre-condizione di sicurezza del `dns apply` — **peer del cluster DNS su .78
standalone** (rule #4; altrimenti gli edit propagano ai NS di produzione) — non
è esposta né gateata dalla UI. In questa sessione l'ho verificata **fuori
banda** (terminale) con evidenza attiva:

- DKIM p[40:90]: SOURCE=`AQEAy8TiOR74…` · DEST .78=`AQEAtEYZ1Sz…` (rigenerata) ·
  **NS pubblici=`AQEAy8TiOR74…` (== SOURCE, ≠ DEST)** → la DKIM divergente di
  .78 **non è mai arrivata** ai NS pubblici → **.78 standalone confermato**.
- Serial locale .78 = `2026070313`, NS pubblici = `2026070300` → zona locale
  .78 avanti e non propagata (ulteriore conferma).
- Post-apply-fallito i NS pubblici restano invariati (nessuna propagazione).

Un operatore "UI-only" non ha modo di fare questa verifica dalla UI. Per un
tool che scrive DNS in un ambiente clusterizzato, è un gap reale sul passo più
rischioso.

### N3 — [MEDIUM/design] la governance status non avanza da sola

L'exec **non avanza mai** lo status della sessione (ogni exec registra un
`forced_status_change draft→draft`). L'auto-transition a `ready_for_cutover`
richiede `status==verification_required`, raggiungibile SOLO percorrendo a mano
la scala di governance (draft → … → verification_required = ~7 "Set Status")
dalla UI. È by-design (la governance è attestazione umana) e ogni hop è una
transizione legale del matrix, ma è un onere non ovvio e non documentato nella
sequenza attesa. Solo l'ultimo hop (verification_required→ready_for_cutover) è
automatico dopo i 3 verify CLEAN.

### N4 — [MEDIUM] `run_pipeline` genera il checklist PRIMA del DNS plan

`pipelineSteps` esegue inventory→diff→policy→**checklist**, senza uno step
`dns-plan`. `checklistStep` aggiunge `--dns-plan` **solo se il file esiste**.
Al momento del `run_pipeline` il dns-plan non esiste ancora → il checklist
iniziale **sotto-riporta** le azioni DNS: 6 manual action mostrate. Dopo aver
generato i plan, la **prima acceptance** rigenera il checklist (ora col
dns-plan presente) e le azioni **saltano a 14**. Confusione UX (la lista
raddoppia a metà flusso) e, se l'operatore non genera i plan / non accetta
nulla, procede con un checklist che omette 8 CONFIRM_DNS_RECORD. Non bloccante
(`apply_blocked` corretto in entrambi i casi). Fix suggerito: aggiungere lo
step `dns-plan` a `pipelineSteps` PRIMA del checklist, oppure rigenerare il
checklist dopo i plan.

---

## Cosa funziona (UI-only, verificato con click reali)

1. **Connessioni** — dashboard pre-carica host.yaml; form funzionante
2. **Create session** — form workbench → sessione draft con artifact dir dedicata
3. **Run Pipeline** — SSH read-only a .193+.78, 5 artifact auto-allegati (SHA256)
4. **Plans** — DNS/Email/Cron via exec, auto-allegati
5. **Acceptances** — one-by-one, tutte le famiglie incl. `blocking_cutover`
   (accettare un CONFIRM = confermarlo: semantica corretta; `overall_status`
   resta BLOCKED perché il *policy blocker* è un layer separato — governance corretta)
6. **Email Apply** — write reale con conferma forte, **con blocker presente**
   (prova chiave della chiusura di FRICTION #6), backup, verify-after
7. **Cron Apply** — no-op gestito correttamente
8. **Email/Cron Verify** — clean=True
9. **Governance Set Status** — transition matrix rispettata
10. **Gate auto-transition** — withholding corretto (non scatta senza i 3 clean:
    "criteri veri", rule #5 rispettata — nessuna transizione falsa)
11. **Safety** — conferma forte (digitare il nome), atomicità apply, backup,
    nessun apply parziale sul fallimento DNS

## Ambiente (rule #3 / #4)

- **.193 (source, PROD)** pre-lettura: load `25.95 / 22.78 / 22.04` su 16 cpu,
  uptime **863 giorni** (CentOS 7 EOL, VPS Keliweb, 55 account). Load elevato
  (~1.6/core) ma **stabile** su tutte e 3 le finestre (nessun runaway), stesso
  stato sotto cui il pipeline aveva già girato oggi. Inventory read-only =
  impatto trascurabile. Proceduto, annotato.
- **.78 (dest, sacrificale)** health: load `0.57`, uptime 2gg — sano.
- **Scritture solo su .78**: email routing set (applicato), DNS (fallito,
  nessun parziale). Probe di diagnosi `_dogfootest*` creati e **ripuliti**
  (zona verificata pulita a fine sessione). `_v2smoke` NON presente su .78.

---

## Prossimo passo (handoff)

Il verdetto è NO, quindi: **PR di chiusura delle friction residue**, in ordine
di priorità:

1. **[BLOCCANTE] N1 — `dns apply` mass_edit_zone**: serve il log errori WHM
   root su .78 per l'Error ID (m7sumx/qnrpvb) → **decisione utente**: aprire una
   sessione superadmin/root per leggere il log e stabilire bug-di-prodotto
   (batching replace TXT multi-segmento) vs quirk ambientale. Se prodotto → fix
   TDD (probabile: split del batch replace, o rimozione di TUTTE le righe
   fisiche di un record multi-segmento).
2. **N2 — gate/segnale UI per il cluster standalone**: almeno un warning
   pre-`dns apply` ("verifica che il peer DNS su .78 sia standalone") o un check
   automatico se ottenibile a livello utente.
3. **N4 — ordering pipeline**: `dns-plan` prima del checklist in `pipelineSteps`.
4. **N3 — governance**: valutare se l'exec debba proporre/auto-avanzare lo
   status (o almeno documentare il walk a verification_required).

La sessione `mig_20260704_1a4eaa2cc7d7` resta a `preflight_required` (NON
archiviata). NON ha raggiunto `ready_for_cutover`: **nessun cutover, zona
produzione intatta** (A record giorginisposi.it pubblico = 194.76.118.193,
invariato).
