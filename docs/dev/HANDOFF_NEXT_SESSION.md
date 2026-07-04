# Prompt di avvio — prossima sessione

Stai lavorando sul tool Go **cpanel-self-migration**, directory
/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration.

Leggi PRIMA: docs/dev/DOGFOODING_2_REPORT.md, docs/dev/DEVELOPMENT_STATE.md,
docs/dev/PR61_BLOCKER_SCOPING.md.

## Stato al 2026-07-04 (fine giornata) — dopo Dogfooding #2

### Verdetto #2: **UI-only completabile = NO** (ma NO "operativo", non strutturale)

Le **6 friction del #1 sono tutte chiuse** e verificate con click reali nel
browser (create, pipeline+auto-attach, plans, acceptances, apply con blocker
presente). Il ciclo UI-only funziona end-to-end **fino al `dns apply`**, dove si
ferma. Dettaglio completo in `DOGFOODING_2_REPORT.md`.

Sessione dogfooding #2: `mig_20260704_1a4eaa2cc7d7`, a `preflight_required`,
NON archiviata, NON arrivata a ready_for_cutover. Zona produzione intatta
(A giorginisposi.it pubblico = .193).

### Friction residue (da chiudere, in ordine di priorità)

1. **[BLOCCANTE] N1 — `dns apply` fallisce**: `DNS::mass_edit_zone: The request
   failed (Error ID m7sumx/qnrpvb)`, riproducibile 2/2. Isolato a livello utente:
   add/remove/batch-semplice singoli funzionano; fallisce il **batch multi-op con
   i replace** (probabile DKIM TXT multi-segmento o combinazione 2-remove+3-add).
   **Root cause nel log WHM root-only su .78** → serve decisione utente su
   sessione root per leggerlo e stabilire bug-prodotto vs quirk-ambiente. Il tool
   gestisce il fallimento correttamente (backup, atomico, nessun apply parziale).

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
