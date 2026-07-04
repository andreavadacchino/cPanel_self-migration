# Prompt di avvio — prossima sessione

Copia-incolla da qui in giù.

---

Stai lavorando sul tool Go **cpanel-self-migration**, directory
/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration.

Leggi PRIMA: DEVELOPMENT_STATE.md, COMMAND.md.

## Stato al 2026-07-04 (sera)

### PR #57 — MERGED (workbench session model)
### PR #58 — MERGED (workbench UI)
### PR #59 — MERGED (UI-driven migration execution)

La Workbench ora PUÒ ESEGUIRE i passi della migrazione:
- 10 azioni: dns/email/cron × apply/verify/rollback + migrate_content
- Conferma forte (digitare nome account) per operazioni write
- Doppia conferma per rollback
- Artifact auto-allegati su exit 0
- Auto-transition a `ready_for_cutover` quando tutti e 3 i verify CLEAN
- Timeline registra ogni esecuzione (comando, durata, esito)
- Safety: workbench.go resta governance-only (AST-enforced)

### Invariante emendato (#59)

L'invariante "apply terminal-only" di #58 è stato CONSAPEVOLMENTE emendato:
la UI PUÒ lanciare subprocess (stessa exec.CommandContext del pipeline
read-only), MA solo con conferma forte. Il safety test ora verifica:
1. workbench.go non ha exec/apply verbs (INVARIATO)
2. OGNI file con --yes-apply-writes chiama validateStrongConfirmation
3. AST ordering: conferma PRIMA di buildArgv nel handler

### Cutover #1 — giorginisposi

Fermo a P1 (TTL lowering su .193 da parte dell'utente).
Vedi CUTOVER_1_GIORGINISPOSI.md.

### PR #60 — MERGED (exec forms + dogfooding report)

Aggiunti form HTML per le 10 azioni exec nel workbench. Dogfooding #1
completato: verdetto NEGATIVO — il ciclo NON è completabile solo dalla UI.
6 gap strutturali documentate in DOGFOODING_1_REPORT.md.

## Prossima sessione: scelta

Due opzioni (decidere all'inizio della sessione):

### Opzione A: Chiudere le gap UI (PR piccola)

Colmare i 4 gap principali per rendere il ciclo UI-only:
1. Form creazione sessione in `/workbench`
2. Exec actions per plans (dns_plan, email_plan, cron_plan)
3. Exec action "run_pipeline" (pipeline + artifact attach)
4. Decoupling checklist BLOCKED da exec gate

Poi dogfooding #2 end-to-end.

### Opzione B: Cutover reale (modalità hybrid)

Usare il tool com'è (UI governance + terminale exec) per completare il
cutover di giorginisposi. Il tool è funzionalmente completo per un
operatore che accetta il terminale per pipeline/plans.

Prerequisiti per B:
- P1 (TTL lowering) da completare dall'utente su WHM .193
- 4h di attesa post-TTL
- Eseguire il runbook Variante C (docs/dev/CUTOVER_RUNBOOK.md)

## Workflow

- Solo push a fork (`git push fork`)
- TDD
- go-reviewer adversariale multi-giro
- `runner.go` off-limits
