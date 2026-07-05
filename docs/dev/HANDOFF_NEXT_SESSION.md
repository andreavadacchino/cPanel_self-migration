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

### Prossimo passo

Proposta **UX guidata** (valutata: adottare con 5 correzioni — la PR parte DOPO
questo verdetto): "dove sei / cosa manca / cosa rischi / cosa fare dopo" sopra la
governance esistente (#57/#59/#61), traduzione IT solo lato UI con enum motore
intatti, schermata covered/not_collected/root_only (coverage.go), DNS danger zone
che evolve il warning N2 (#62) in un check/attestazione della pre-condizione
standalone. Niente feature-di-motore mascherate da UX; nessuno scoring inventato.

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

### PR in coda (richiesta utente 2026-07-04): traduzione webui in italiano

L'utente ha chiesto **"tradurre tutto in italiano"** = webui (dashboard +
workbench) **e** deliverable, con timing "dopo il dogfooding". Deliverable già
in IT. Da fare: tradurre i template `internal/webui/templates/index.html`,
`workbench_list.html`, `workbench_detail.html` (label, bottoni, messaggi) da EN
a IT. Fork-only, `--repo` esplicito, TDD, go-reviewer multi-giro fino APPROVE
PULITO, Docker LINUX_ALL_GREEN eseguito, gate nel body prima del merge, handoff.

## Workflow (promemoria)

- Solo push a fork (`git push fork`); PR con `gh pr create --repo andreavadacchino/cPanel_self-migration`
- TDD; go-reviewer multi-giro fino APPROVE PULITO; Docker LINUX_ALL_GREEN eseguito (non promesso)
- Gate dichiarato NEL BODY prima di chiedere il merge
- `runner.go` off-limits
- Scritture reali SOLO su sacrificale .78; letture .193 (prod) con load-check prima; MAI --force per transizioni
