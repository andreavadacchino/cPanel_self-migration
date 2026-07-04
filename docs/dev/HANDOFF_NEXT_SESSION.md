# Prompt di avvio — prossima sessione

Copia-incolla da qui in giù.

---

Stai lavorando sul tool Go **cpanel-self-migration**, directory
/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration.

Leggi PRIMA: DEVELOPMENT_STATE.md, COMMAND.md (sezione `migration`).

## Stato al 2026-07-04

### Ultimo merge: feat/workbench-session-model

PR merged: **Migration Session Model and Artifact Registry**.

Introduce `internal/workbench`:
- 14 statuses, 12 steps, transition matrix con force+reason
- Artifact registry: 17 kind conosciuti, copia atomica + SHA256
- Timeline eventi con tool_version
- JSON file storage (atomic write-temp+fsync+rename), 0700/0600
- Zero import sshx/cpanel/config (safety test)
- `migration` CLI namespace (init/list/show/set-status/attach-artifact/archive)
- Test completi: unit, permission, determinism, CLI dispatch, safety

### Cutover #1 — giorginisposi

Fermo a P1 (TTL lowering su .193 da parte dell'utente).
Vedi CUTOVER_1_GIORGINISPOSI.md per il diario del cutover.

### Prossimo passo: PR 58 — Single Account Workbench UI

La UI userà le migration sessions per costruire un dashboard con:
- Overview (lista sessioni, status badge)
- Preflight / Inventory / Checklist views
- Apply Center (trigger da UI → CLI subprocess)
- Verify / Cutover / Archive

Prerequisiti soddisfatti:
- Modello dati: ✅ (Session struct, status enum, steps, artifacts)
- CLI: ✅ (tutti i subcommand operativi)
- Storage: ✅ (JSON file-based, testato)
- Safety: ✅ (offline, no credentials)

Pattern UI da seguire: `cmd/cpanel-self-migration/ui_cmd.go` + `internal/webui/`.
La nuova UI può leggere le session via il package `workbench.Store`.

## Tool state

Binario: `go build -o /tmp/cpanel-self-migration ./cmd/cpanel-self-migration/`
Config: `configs/host.yaml` (src=.193, dest=.78)

## Workflow (invariato)

SOLO fork (`--repo andreavadacchino/cPanel_self-migration`), mai origin.
TDD. go-reviewer + Docker. runner.go off-limits.
Peer NS standalone verificato ATTIVAMENTE prima di write DNS.

## Regole assolute

- Mai removeacct/killdns. Mai toccare ruoli peer o useclusteringdns.
- Mai ripristinare sync (Variante C — standalone per tutta la campagna).
- Zone di TUTTI gli altri account INTOCCABILI.
- Su .193: letture + delta sync + sospensione — nient'altro.
