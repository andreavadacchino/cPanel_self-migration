# Sessione 2026-07-06 — webui: In-Flight Job Rehydration Journal (GitHub PR #70)

Report completo della sessione. Implementata e **mergiata** la fondazione tecnica
della fase frontend **Flight Director** (roadmap doc: fase «PR 69 — Setup/Rehydration
Foundation»). Consegnata come **GitHub PR #70** (merge commit `5e11575`, 2026-07-06).

Principio guida rispettato: *prima rendi impossibile perdere il controllo, poi rendi
l'interfaccia bella*. **NON un redesign**: costruito **solo** il pezzo mancante
(`job.json`) e riusato tutto il resto. **SSE non introdotta** (rimandata).

Riferimenti: `PR69_JOB_JOURNAL_DESIGN.md` (spec/contratto), `FRONTEND_FLIGHT_DIRECTOR_ROADMAP.md`
(§6/§7/§12/§14/§16), `HANDOFF_NEXT_SESSION.md` (stato post-merge).

---

## 1. Obiettivo e vincoli

Implementare l'**In-Flight Job Rehydration** (Job Journal): rendere la UI workbench
impossibile da perdere il controllo mentre un exec di scrittura è in corso, e
sopravvivere a refresh / sleep / restart del processo `ui`.

Vincoli non negoziabili rispettati:
- Branch dedicato dal main; **mai commit su main**; push solo a `fork`; PR verso
  `andreavadacchino/cPanel_self-migration`.
- `runner.go` off-limits; nessun writer/collector nuovo; nessun motore delta;
  nessuna automazione cutover/DNS; **nessun SSE** (PR 70 roadmap).
- go-reviewer multi-giro fino APPROVE PULITO; Docker LINUX_ALL_GREEN **eseguito**.
- TDD: test §10 della spec PRIMA del codice.

---

## 2. Analisi investigativa — ground truth (scoperta chiave)

Ispezione del codice reale PRIMA di scrivere. Esistono **due** livelli di rehydration
già funzionanti, non uno:

1. **`readArtifactFacts`** (`workbench_view.go:84`) → stato **completato** (plan/apply/
   verify letti da disco a ogni GET; il refresh read-only già sopravvive — dogfooding #3).
2. **`loadRunMonitor`** (`monitor.go:126`) → progress **per-fase live** da `events.jsonl`,
   **già** con stati running/completed/failed + stall detection, **già** wired nel
   dashboard. Copre `migrate_content` (l'unico action con `--json-events`).

**Il gap reale** non era il progress item-level (già coperto dal monitor), ma lo
**strato identità**: lo slot single-writer (`jobManager`, `job.go`) è **anonimo**.
Conseguenze verificate:
- `dns_apply`/`email_apply`/`cron_apply`/`run_pipeline` non producono `events.jsonl` →
  in-flight **totalmente invisibile**;
- **409 opaco** in tutti e 3 i chiamanti dello slot: `/run` (webui.go:377),
  `/accept` (accept.go:47-48), `/exec` (workbench_exec.go:333);
- nessun recovery su restart.

Questa scoperta ha ridotto lo scope: `job.json` deve portare **solo identità+fase**,
non duplicare il progress item-level.

---

## 3. Decisioni ratificate (roadmap §16.7 / §16.8)

**1. Schema `job.json` — lean.** Confermato con l'utente. Memorizzare
`item/items_done/items_total` sarebbe ridondante (già dal monitor) e richiederebbe
polling o toccare i writer (viola i non-goal). Schema finale, `<dir>/job.json`,
atomico 0600 come `store.writeSession`:

```json
{
  "session_id": "mig_...", "action": "migrate content",
  "started_at": "RFC3339", "updated_at": "RFC3339",
  "state": "running|completed|failed|interrupted",
  "phase": "migrate content", "error": "", "tool_version": "..."
}
```

TTL: nessuna (un journal per working dir, sovrascritto dall'exec successivo). Il
dettaglio item di `migrate_content` si compone al view-layer riusando `loadRunMonitor`.

**2. Progress granularity — opzione B.** Phase-level dal journal; item-level solo per
`migrate_content` (riuso monitor). **Nessun writer toccato.** Opzione A (estendere
`--json-events`) rinviata a una PR SSE futura.

---

## 4. Implementazione (TDD RED→GREEN)

Nuovo file `internal/webui/job_journal.go` + test `job_journal_test.go`. Modifiche
chirurgiche agli altri file webui. **Nessun** file fuori scope toccato.

### 4.1 Persistenza atomica
- `writeJobJournal` — temp + `Write` + `Sync` + `Chmod(0600)` + `Close` + `Rename`
  (stesso pattern di `store.writeSession`); cleanup su ogni branch d'errore.
- `readJobJournal` — fail-soft: file mancante/corrotto → `(nil, false)`.

### 4.2 Lifecycle in `handleExec`
- `tryReserve()` fallito → `writeBusy409` (stato leggibile, non più il 409 nudo).
- `startJobJournal` scrive `running` **prima** di lanciare il subprocess.
- Il terminale (`completed|failed` + `error`) è scritto in un **defer accoppiato a
  `release()`** → copre ogni return path (attach/timeline error inclusi) e, entro un
  processo vivo, un refresh non vede mai `running` con slot libero.

### 4.3 Fine del 409 opaco — tutti e 3 i chiamanti
`writeBusy409(w, dir, job)` su `/run` (webui.go), `/accept` (accept.go), `/exec`
(workbench_exec.go). `busyMessage` legge il journal (accurato per l'exec) e, se lo
slot è tenuto dall'analisi read-only (stato in-memory, non su disco), ripiega sullo
snapshot del `jobManager`.

### 4.4 Recovery + reconcile
- `recoverJobJournal` allo startup (`New`): lo slot in-memory è libero per
  costruzione → un journal ancora `running` è residuo di una `ui` uccisa mid-exec →
  persistito `interrupted`.
- `reconcileJobJournal` al read-time nel view-model: `running` + slot libero →
  presentato `interrupted`, **senza scrivere** durante la GET (belt-and-suspenders per
  path di panic). Wiring: `workbenchServer.jobBusy = s.job.running` (nil-safe).

### 4.5 Backup detection (§8)
`readArtifactFacts` esteso con `areaFacts.BackupPresent` (`<area>_backup.json`). I
template `screen_applica` mostrano il pulsante **Rollback** solo se il backup esiste.

### 4.6 Superficie su refresh + live auto-refresh
- Banner del job (running/interrupted) su ogni schermata via `wbNav`.
- `workbenchView.JobLive` (= journal `running` **dopo** reconcile) → `<meta
  http-equiv="refresh" content="2">` in `wbHead` (riuso del pattern dashboard
  `index.html:6`) sulle 7 schermate. Interrupted/completed/failed sono terminali e
  **non** refreshano. Backstop anti-refresh-forever: `execTimeout` (30m) +
  `recoverJobJournal`.

### 4.7 Anti-leak (roadmap §12)
Il journal ha solo campi identità+fase; `error` = `execErr.Error()` (errore runner
wrappato "name: exit status N", mai argv/segreti). Testato anche sul **failure path**
(dove `error` è popolato).

---

## 5. Test (§10 della spec) — tutti verdi

`job_journal_test.go`:
- schema write/read + **0600**; read fail-soft;
- `running` scritto **prima** del subprocess (gate sul runner);
- `completed` su successo / `failed` (+`error`) su fallimento;
- **409 leggibile** (cita l'action, non più opaco);
- refresh end-to-end: la pagina renderizzata mostra il job running;
- recovery `running`→`interrupted` allo startup;
- reconcile read-time (running+busy resta running; running+free → interrupted);
- **JobLive** guida l'auto-refresh (unit) + render (meta presente durante l'exec,
  assente dopo il completamento);
- backup detection (facts) + gating rollback (render);
- **anti-leak** su failure path.

I test con goroutine usano il pattern `done := make(chan struct{})` + `<-done` per
il join prima del ritorno (evita race col cleanup di `t.TempDir()`).

---

## 6. Review adversariale — go-reviewer multi-giro

**Giro 1 → BLOCK.** Trovati due problemi reali (non stilistici):
1. **BLOCKING — goroutine leak nei test**: `go doWorkbenchReq(...)` senza join → race
   col cleanup di `TempDir` (`unlinkat ... directory not empty`), ~13% flaky senza
   `-race`, 5/5 con `-race`. Il mio primo `-race` era passato **per fortuna**
   (intermittente). Fix: join con `done` channel su 3 test (incl. uno aggiunto dopo
   il giro 1).
2. **MEDIUM — anti-leak non testato sul failure path**: `error` (unico campo free-text)
   non era esercitato. Fix: `TestJobJournalAntiLeak` ora forza `fr.fail` e verifica i
   byte del journal (incl. `error`).
3. Minor: `startJobJournal` ritornava il suo arg (no-op) → rimosso.

**Giro 2 → APPROVE PULITO.** Verificato indipendentemente: 20/20 run plain, `-race`
x10, entrambi i fix chiusi.

**Delta live-refresh → APPROVE** (verifica indipendente 15/15 senza flake).

Osservazione non-blocking accettata: il meta-refresh cancella input di form in corso —
ma mentre un exec gira lo slot è busy, quindi non se ne può lanciare un altro (409):
ininfluente qui. Stesso tradeoff già accettato per il dashboard.

---

## 7. Gate eseguiti (reali, non dichiarati)

Sul HEAD di merge:
- **Docker LINUX_ALL_GREEN** (`golang:1.25`): `go test ./...` = **0**, `go vet ./...` = **0**.
- Race locale `internal/webui` + `internal/workbench`: **verde** (count ripetuti).
- `gofmt`: **file di questa PR clean**. Nota onesta: `gofmt -l .` segnala ~11 file
  **preesistenti su main** (non nel diff, probabile gofmt go1.25 su codice più
  vecchio) → fuori scope, non toccati.
- Sanity post-merge su `main`: build OK + `internal/webui` verde.
- CI GitHub Actions non gira su questa PR fork (solo "Sourcery review" skipped) → i
  gate sono stati eseguiti manualmente + Docker.

**Failure macOS preesistenti** (`internal/dbmig`: `sed` BSD vs GNU, guard `HOME` su
tmp): confermate rosse su `main` pulito (worktree separato), **verdi su Linux**. Non
correlate a questa PR.

---

## 8. Scope boundary ratificato

`job.json` è scritto **solo** da `/exec` (il path di scrittura distruttiva, oggetto
reale della rehydration). `/run` (analisi read-only async, stato in-memory + fallback
409 dallo snapshot) e `/accept` (ricomposizione checklist offline) **non** scrivono il
journal — ma restano non-opachi sul 409. Scelta minimale voluta per non toccare
`job.go`/il pipeline core. Documentata nel body PR come decisione, non svista.

---

## 9. Chiusura PR #70

- Titolo corretto da "PR 69 —" a **"PR 70 — In-Flight Job Rehydration Journal"** +
  nota nel body: la GitHub PR è #70; il "69" è la **fase di roadmap documentale**.
- **Mergiata** (merge commit `5e11575`, mergedAt 2026-07-06T11:06:25Z), `main` locale
  aggiornato, branch `feat/webui-job-journal` eliminato (locale + fork).
- Handoff/roadmap aggiornati: #70 completata; decisioni §16.7/§16.8 ratificate; SSE
  **rimandata** (da rivalutare solo dopo dogfooding reale su migrazione lunga).

---

## 10. Prossima direzione (NON iniziata)

**Setup Flow / New Migration Wizard (69b)** — semplificare il flusso operatore:
nuova migrazione → sorgente → destinazione → account → cosa migrare → preflight.

**NON** iniziare codice SSE: gran parte del suo valore (reconnect, phase progress,
stati) è già coperto da `job.json` + `loadRunMonitor` + meta-refresh; l'incremento
reale è solo UX (no flicker, log-tail live) a costo di complessità non banale
(endpoint streaming long-lived, gate su GET persistente, EventSource, reconnect).
