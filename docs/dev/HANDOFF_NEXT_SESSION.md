# Prompt di avvio — prossima sessione

Copia-incolla da qui in giù.

---

Stai lavorando sul tool Go **cpanel-self-migration**, directory
/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration.

Leggi PRIMA: DEVELOPMENT_STATE.md, COMMAND.md.

## Stato al 2026-07-04

### PR #57 — MERGED (workbench session model)
### PR #58 — MERGED (workbench UI)

La governance operativa single-account è completa:
- Session model: 14 stati, 12 step, transizioni validate
- Artifact registry: 17 kind, SHA256, atomic copy
- CLI: `migration init/list/show/set-status/attach-artifact/archive`
- UI: `/workbench` (list) + `/workbench/session/<id>` (detail + governance)
- Invariante: l'apply è SOLO da terminale (safety test lo certifica)
- Sentinel errors per HTTP status distinction
- XSS/CSRF/path-traversal testati

### Cutover #1 — giorginisposi

Fermo a P1 (TTL lowering su .193 da parte dell'utente).
Vedi CUTOVER_1_GIORGINISPOSI.md.

### Prossimi passi

1. **Cutover completion** — quando l'utente edita i TTL
2. **Campagna** — dopo il primo cutover completo, orchestrare la flotta
3. **SpamAssassin collector** — se il fleet survey lo richiede

## Tool state

Binario: `go build -o /tmp/cpanel-self-migration ./cmd/cpanel-self-migration/`
Config: `configs/host.yaml` (src=.193, dest=.78)
UI: `cpanel-self-migration ui` → http://127.0.0.1:8422/workbench

## Workflow (invariato)

SOLO fork (`--repo andreavadacchino/cPanel_self-migration`), mai origin.
TDD. go-reviewer + Docker. runner.go off-limits.
