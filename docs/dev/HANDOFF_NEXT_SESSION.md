# Prompt di avvio — prossima sessione (PR 58 — Workbench UI)

Copia-incolla da qui in giù.

---

Stai lavorando sul tool Go **cpanel-self-migration**, directory
/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration.

Branch attivo: `feat/workbench-ui` (già creato da main con #57 merged).

Leggi PRIMA: DEVELOPMENT_STATE.md, internal/webui/webui.go (pattern da
specchiare), internal/workbench/ (il session model #57).

## Stato al 2026-07-04

### PR #57 — MERGED

`feat/workbench-session-model` merged come PR #57.
Introduce `internal/workbench`: session model, artifact registry, CLI.
Review adversariale a 3 agenti + hardening applicato:
- TOCTOU fix (open-before-lock, fd passthrough)
- os.CreateTemp per artifact (O_EXCL)
- Force reason ≥10 char
- os.Mkdir collision detection
- fsync su copyFromFD
- Cross-process limitation documentata

### PR #58 — IN CORSO (branch `feat/workbench-ui`)

Obiettivo: Single Account Workbench UI (governance, NON esecuzione).

## INVARIANTI NON NEGOZIABILI

1. **Apply è SOLO da terminale** — la UI mostra stato e PREPARA comandi
   copy-paste, mai esegue apply/cutover/rollback verso server
2. Loopback-only (127.0.0.1:8422, pattern webui esistente)
3. Offline-first: legge session store + artifact, esecuzioni solo
   pipeline READ-ONLY come subprocess (già supportato dalla webui)
4. Nessun test su server reale; niente nuovi writer/collector

## Scope PR 58

### A — Pre-lavoro (5 LOW della review #57, ora necessari per la UI)

1. **Sentinel errors**: `ErrSessionNotFound` ecc. nel package workbench
   (la UI ne ha bisogno per distinguere 404 da 500)
2. **List() warnings**: cambiare firma in `([]Session, []string, error)`
   — non scrivere su stderr dal package library
3. **readSession assertion**: `sess.ID == folderID` dopo parse (drift → error)
4. **Escaping**: html/template auto-escape copre la UI; nella CLI
   sanificare `--name` display (strip control chars)
5. Actor field: rimandato (single-operator)

### B — Pagine Workbench

1. Sessions list (stato, step, contatori)
2. Session detail (Overview → Preflight → Inventory → Diff/Policy/
   Checklist → Azioni manuali → Apply Center → Verify → Cutover → Archive)
3. Artifact viewer (kind, sha256, attached_at, render JSON read-only)

### C — Apply Center (invariante: NO esecuzione)

Per track (email/cron/dns) mostra:
- Stato derivato dagli artifact (plan? apply report? verify report?)
- Comando ESATTO copy-paste (con path della sessione)
- Bottone "ingest report" che allega artifact e aggiorna stato

### D — Governance dalla UI (solo scritture locali)

- Transizioni di stato (matrice #57, force con reason ≥10 char)
- Attach artifact via path locale (validazioni #57)
- Pipeline READ-ONLY come subprocess → output in artifact_dir → attach

### E — CLI

`cpanel-self-migration ui` retrocompatibile (`--dir` = modalità attuale).
Nuova modalità workbench (flag/route, stesso server).

## Pattern webui da specchiare

```go
// internal/webui/webui.go pattern:
- embed.FS per template
- html/template (auto-escape, MAI template.HTML da input)
- CSRF token per POST (rand 32 byte → hex)
- Anti-DNS-rebinding (Host + Origin check)
- Route fisse in route() — no ServeMux, no file serving
- Options{Dir, Runner, BaseContext}
- StepRunner interface per test
- jobManager per subprocess
```

La Workbench ESTENDE questo (stesso binario/server), non lo duplica.

## Test obbligatori

- Handler test (httptest): list, detail, transizione, attach, ingest
- Escaping: `<script>` e ANSI nel nome sessione → mai raw in HTML
- Safety test: no sshx/cpanel imports, no write verbs, no apply/cutover
  args nei subprocess
- Regressione: `--dir` mode funziona invariata
- Race detector pass
- Docker LINUX_ALL_GREEN

## Cutover #1 — giorginisposi

Fermo a P1 (TTL lowering su .193 da parte dell'utente).
Vedi CUTOVER_1_GIORGINISPOSI.md.

## Workflow (invariato)

SOLO fork (`--repo andreavadacchino/cPanel_self-migration`), mai origin.
TDD. go-reviewer + Docker. runner.go off-limits.
