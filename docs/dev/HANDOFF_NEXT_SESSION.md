# Prompt di avvio — prossima sessione

Stai lavorando sul tool Go **cpanel-self-migration**, directory
/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration.

Leggi PRIMA: docs/dev/DOGFOODING_2_REPORT.md, docs/dev/DEVELOPMENT_STATE.md,
docs/dev/PR61_BLOCKER_SCOPING.md.

## Stato al 2026-07-05 — Dogfooding #2 SBLOCCATO (N1 risolto)

### Verdetto #2 aggiornato: **UI-only completabile = SÌ** (con riserva N2 documentata)

Il blocco N1 è **risolto alla radice** e il ciclo UI-only è stato completato fino
a `ready_for_cutover` **dalla UI con click reali**. Dettaglio in
`DOGFOODING_2_REPORT.md` (sezione VERDETTO AGGIORNATO 2026-07-05) e
`DNS_MASS_EDIT_DIAGNOSIS_78.md`.

- **N1 causa radice**: `mass_edit_zone` rifiuta `dname="@"` per l'apex → fix
  `dnsCanonToRelative` apex→FQDN (**PR #64, merged**). Co-bug encoding `+`→spazio
  (**PR #63, merged**). Entrambi in `main`.
- **`dns apply` reale post-fix**: `3 applied, 0 failed`, `dns verify` CLEAN,
  DKIM/SPF SOURCE coi `+` intatti.
- **Walk governance UI** (2026-07-05, click reali, MAI `--force`): 6 hop
  `preflight_required`→…→`verification_required`, poi Verifica DNS (lettura) →
  **auto-transition a `ready_for_cutover` scattata da sola** (rule #5 ok).

Sessione dogfooding #2: `mig_20260704_1a4eaa2cc7d7`, ora a **`ready_for_cutover`**,
NON archiviata, **nessun cutover eseguito**, nessun TTL toccato. Zona produzione
intatta (A giorginisposi.it pubblico = .193).

### Prossimo passo — aggiornato 2026-07-05 (post #66)

**UX guidata: FATTA e merged (PR #66).** Workbench redesign in 7 schermate
(Panoramica/Preflight/Fotografia/Cosa verrà migrato/Conferme/Applica/Chiusura),
SOLO presentazione, enum motore intatti. Validata con walk in browser reale
(dogfooding #3, `DOGFOODING_3_UX_WALK.md`): guida corretta per stato, DNS danger
zone che blocca l'apply senza attestazione, Chiusura senza falso SÌ su status
forzato, sessione reale `mig_20260704` che rende il NO motivato corretto.

Quadro attuale: **tool completo, UI product-grade, sessione reale a
`ready_for_cutover`.** Restano in agenda solo:

1. **Finestra di cutover** (decisione utente): data campagna, orario, ruolo sync
   DNS (variante A/B/C), ordine account — vedi `CUTOVER_RUNBOOK.md` §7. Il tool
   non le può prendere; sono le 5 voci mostrate nella schermata Chiusura.
2. **Nota amministrativa mai chiusa**: registrare via `create_intervention` su
   Orbit le scritture fatte sul sacrificale .78 in queste settimane, **quando il
   TOTP torna disponibile**.

~~Limite noto: i contenuti delle azioni manuali restano in inglese.~~
**CHIUSO in #67 (merged):** Title/OperatorAction ora tradotti in IT a livello di
presentazione (`manualTitleIT`/`manualActionIT`, pattern `statusLabelIT`), NON
alla sorgente (le chiavi acceptance `AK-*` sono `sha256` su title/detail →
intoccabili; la UI ri-renderizza l'artifact congelato, quindi la sessione reale
è già in IT). Restano volutamente grezzi: `Detail` (diff di valori tecnici) e
`Type`. La UI è ora **interamente in italiano**. Dettaglio: `MANUAL_ACTIONS_IT_DESIGN.md`.

### Friction residue (da chiudere, in ordine di priorità)

1. **[BLOCCANTE] N1 — `dns apply` fallisce**: `DNS::mass_edit_zone: The request
   failed (Error ID m7sumx/qnrpvb)`, riproducibile 2/2. Isolato a livello utente:
   add/remove/batch-semplice singoli funzionano; fallisce il **batch multi-op con
   i replace** (probabile DKIM TXT multi-segmento o combinazione 2-remove+3-add).
   **Root cause nel log WHM root-only su .78** → serve decisione utente su
   sessione root per leggerlo e stabilire bug-prodotto vs quirk-ambiente. Il tool
   gestisce il fallimento correttamente (backup, atomico, nessun apply parziale).
   **Diagnosi completa + snippet di riproduzione: `N1_DNS_APPLY_MASS_EDIT_FAILURE.md`.**

2. **N2 [HIGH per DNS cluster]** — nessun affordance UI per la pre-condizione
   "peer DNS .78 standalone" (rule #4). Verificata fuori-banda con dig (DKIM
   pubblica == source ≠ dest → standalone confermato). Aggiungere warning/check.

3. **N4 [MEDIUM]** — `pipelineSteps` genera il checklist PRIMA del dns-plan →
   checklist iniziale sotto-riporta le azioni DNS (6→14 dopo la 1ª acceptance).
   Fix: step `dns-plan` prima del checklist, o rigenerare il checklist dopo i plan.

4. **N3 [MEDIUM/design]** — l'exec non avanza mai lo status; per l'auto-transition
   serve percorrere a mano la scala governance a `verification_required`.

### Traduzione webui in italiano — COMPLETATA (#62 + #66)

`index.html`/`workbench_list.html`/`workbench_detail.html` tradotti in #62; il
redesign #66 ha completato la traduzione della chrome del workbench (status/step/
overall/coverage note, 33 aree) e aggiunto le 6 nuove schermate; **#67 ha tradotto
i contenuti delle azioni manuali (Title/OperatorAction)** a livello presentazione.
**Nessun residuo EN nella UI** (restano grezzi solo i dati tecnici: Detail=diff
valori, Type, code POL-*/AK-*).

## Workflow (promemoria)

- Solo push a fork (`git push fork`); PR con `gh pr create --repo andreavadacchino/cPanel_self-migration`
- TDD; go-reviewer multi-giro fino APPROVE PULITO; Docker LINUX_ALL_GREEN eseguito (non promesso)
- Gate dichiarato NEL BODY prima di chiedere il merge
- `runner.go` off-limits
- Scritture reali SOLO su sacrificale .78; letture .193 (prod) con load-check prima; MAI --force per transizioni
