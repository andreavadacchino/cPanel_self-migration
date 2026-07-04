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

## Prossima sessione: DOGFOODING

Obiettivo: migrazione di test completa di **giorginisposi** condotta
INTERAMENTE dalla Workbench UI (connessioni → pipeline → acceptances →
apply → verify → ready_for_cutover), sul dest sacrificale .78.

### Sequenza operativa

1. `cpanel-self-migration ui --dir ./dogfood_giorginisposi`
2. Browser: http://127.0.0.1:8422
3. Configurare connessioni (form host.yaml) — source .193, dest .78
4. `/workbench` → creare sessione `migration init --name giorginisposi ...`
5. Eseguire pipeline read-only (inventory → diff → policy → checklist)
6. Gestire blockers/acceptances dalla UI
7. Eseguire `dns_apply`, `email_apply`, `cron_apply` dalla UI (con conferma)
8. Eseguire i 3 verify dalla UI
9. Verificare auto-transition a `ready_for_cutover`
10. Cutover page: seguire runbook (FUORI dalla UI)

### Cosa validare

- [ ] Pipeline read-only completa senza errori
- [ ] Artifact auto-allegati alla sessione
- [ ] Conferma forte funziona (errore se nome sbagliato)
- [ ] dns/email/cron apply riusciti con backup deterministici
- [ ] Verify CLEAN → auto-transition funziona
- [ ] Timeline registra tutto correttamente
- [ ] Rollback funziona (testare su un singolo track)

### Prerequisiti

- P1 (TTL lowering) completato dall'utente su .193
- Account dest su .78 già esistente (da PR precedenti)
- host.yaml con credenziali corrette

## Workflow

- Solo push a fork (`git push fork`)
- TDD
- go-reviewer adversariale multi-giro
- `runner.go` off-limits
