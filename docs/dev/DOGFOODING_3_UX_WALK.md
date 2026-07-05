# Dogfooding #3 — Walk del percorso guidato (Workbench UX Redesign v1)

Data: 2026-07-05 · Metodo: walk in **browser reale** (Chrome), click veri, server
`cpanel-self-migration ui` locale (loopback). Nessuna scrittura sui server:
solo GET, governance locale (store isolato) e artifact sintetici/reali in lettura.

## Setup

- **Throwaway isolato**: `CPANEL_MIGRATION_HOME=/tmp/csm-walk-store`,
  `--dir /tmp/csm-walk-dir` (copia degli artifact reali di `dogfood_giorginisposi`
  per ricchezza dati), sessione `mig_20260705_…` avanzata via CLI
  `migration set-status`. Store e dir separati dai reali → zero contaminazione.
- **Sessione reale** (solo render read-only): `mig_20260704_1a4eaa2cc7d7`
  (`giorginisposi`, `ready_for_cutover`) sullo store reale,
  `--dir dogfood_giorginisposi`. Nessun click di scrittura, sessione non mutata.

Le 4 domande guida per ogni schermata: **dove sono / cosa manca / cosa rischio /
cosa faccio dopo**.

## Esito per schermata (throwaway)

| # | Schermata | Verificato | Esito |
|---|-----------|-----------|-------|
| 1 | **Panoramica** | nav 7-voci, badge stato IT, "PROSSIMA AZIONE", semafori per fase, Governance (CSRF), Cronologia | ✅ semafori corretti (Cutover grigio→blu al variare stato); prossima azione coerente con lo stato |
| 2 | **Preflight** | semafori pre-condizioni, cluster "da confermare manualmente", dettagli tecnici collassati | ✅ onesto (nessun preflight_report inventato); tutto IT |
| 3 | **Fotografia account** | 9 contatori sorgente/destinazione dalla checklist | ✅ dati reali (es. SSL 3→0), label IT |
| 4 | **Cosa verrà migrato** | ✅/🟡/⚪ da coverage manifest + join manual action | ✅ DNS=🟡 (conferma pendente), 33 aree tradotte, note IT |
| 5 | **Conferme operatore** | fieldset per azione, perché/rischio/come, form "Conferma fatto", badge "impedisce il cutover" | ✅ meccanismo corretto; chrome IT (contenuto motore EN, vedi Limiti) |
| 6 | **Applica e verifica** | 4 blocchi (contenuti/email/cron/DNS), stato "Applicata e verificata · backup disponibile", **DNS Danger Zone** | ✅ **gate danger zone confermato**: "Applica DNS" grigio/disabilitato senza attestazione → rosso/abilitato dopo la spunta (mai inviato) |
| 7 | **Chiusura** | verdetto "Posso spegnere il vecchio server?" | ✅ **NO** con lista esatta: blocker cutover + 8 conferme pendenti + 5 decisioni runbook (IT) |

### Validazione decisiva (riesame): no falso SÌ su status forzato

Sessione throwaway **forzata** a `ready_for_cutover` con checklist `OverallStatus=BLOCKED`:
la Chiusura ha continuato a rispondere **NO** (il verdetto deriva dagli artifact, non
dallo status forzabile). Confermato nel browser.

## Sessione reale `mig_20260704_1a4eaa2cc7d7` (read-only)

Chiusura renderizzata correttamente: **NO — non ancora**, con:
- Problemi che impediscono il cutover: `dns: POL-DNS-NS-CHANGED (zone giorginisposi.it NS …)`
- Conferme operatore mancanti: 8 A-record (`cpanel/cpcalendars/cpcontacts/ftp/
  giorginisposi.it/webdisk/webmail/whm`) da risolvere a mano
- Decisioni che restano a te (runbook §7): 5 voci in IT

È esattamente il "NO motivato corretto" atteso (cutover pendente + decisioni utente).
Sessione **non mutata** dal render.

## Friction trovate e trattamento

**Cosmetiche — corrette in questa stessa PR** (solo layer presentazione, giro
go-reviewer di conferma):

- **F1** badge stato mostrava l'enum grezzo (`ready_for_apply`) → ora mostra il
  label IT ("Pronto per il cutover").
- **F2** "Passo corrente: setup" (Step enum) → `stepLabel` ("Configurazione").
- **F3** Cronologia mostrava transizioni enum (`draft → preflight_required`) → ora
  `statusLabel` ("Bozza → Preflight richiesto"). La colonna Azione (`status_change`)
  resta come audit tecnico.
- **F4** `<select>` governance con testo-opzioni enum → testo IT (i `value` restano
  enum, necessari per il POST `/status`).
- **F5** note ⚪ di "Cosa verrà migrato" in inglese (da `coverage.go`) → tradotte via
  `coverageNoteIT` (17 aree). Guardia test anti-leak.

**Sostanziale — dichiarata, NON risolta in questa PR** (fuori scope UX):

- I **contenuti** delle azioni manuali (`Title`/`Detail`/`OperatorAction`, es.
  "Resolve the A record … by hand") sono stringhe **generate dal motore checklist**,
  in inglese — identiche a quelle della dashboard #61. Localizzarle richiede un
  intervento a livello motore (o un layer di traduzione dei contenuti dinamici),
  fuori dallo scope "SOLO presentazione". Coerente con lo scoping #61.
- Codici di riferimento tecnici lasciati intenzionalmente grezzi perché sono
  identificatori precisi: `POL-DNS-NS-CHANGED` (blocker), `CONFIRM_DNS_RECORD`
  (tipo azione), i `Kind` degli artifact sotto "Dettagli tecnici".

## Verdetto dogfooding #3

Il percorso guidato è **coerente e usabile**: in ogni schermata l'operatore sa dove
si trova, cosa manca, cosa rischia e cosa fare dopo. Il glossario è coerente (nessun
enum tecnico nella chrome dopo F1–F5). La danger zone DNS impedisce davvero l'apply
senza attestazione. La Chiusura risponde correttamente, e in particolare **non dà
falso SÌ** su uno status forzato. La sessione reale a `ready_for_cutover` rende il
NO motivato corretto.

Nota: gli screenshot delle schermate sono stati catturati durante il walk live
(mostrati in sessione); non sono stati committati come file binari nel repo — questo
report ne è l'evidenza scritta per-schermata.
