# PR 69 — In-Flight Job Rehydration (Job Journal) — Design

Status: dev-ready spec · Date: 2026-07-06 · Base: `FRONTEND_FLIGHT_DIRECTOR_ROADMAP.md`
Scope: single-account migration UI · TDD obbligatorio · nessun writer/collector nuovo

## 1. Scopo

Rendere la UI **impossibile da perdere il controllo** durante un job in corso.
Oggi un refresh/sleep durante una migrazione lascia l'operatore cieco. Questa PR
introduce **un unico artefatto nuovo — il job journal (`job.json`)** — e riusa la
rehydration di stato completato che già esiste. È la fondazione di PR 70 (SSE): senza
identità di job persistita non c'è nulla a cui riattaccarsi.

## 2. Stato attuale (evidenza codice)

**Cosa esiste già e va RIUSATO:**

- `internal/webui/workbench_view.go:84` `readArtifactFacts(dir)` ricostruisce da
  disco a ogni GET: `HostYAMLPresent`, inventari, `PlanPresent`, `ApplyPresent`,
  `VerifyPresent/VerifyClean` (per dns/email/cron). Il refresh read-only **già
  sopravvive** (dogfooding #3). → la rehydration di stato *completato* è fatta.
- `internal/webui/job.go` `jobManager`: singolo slot di processo (`s.job`,
  `webui.go:153`), condiviso da `/run`, `/accept`, `/workbench/.../exec`
  (`webui.go:168-170`). `snapshot()` alimenta la dashboard (`webui.go:446`).
- `internal/workbench/store.go`: `session.json` atomico (0600), `Timeline`,
  `Artifacts` con SHA256. `SetStatus` registra ogni exec nel timeline.

**Il problema preciso (ciò che manca):**

- `internal/webui/workbench_exec.go:339-366` — l'exec gira **sincrono dentro la
  request HTTP**, tail **in-memory** (`tailBuffer`, 64 KiB), ctx su `ws.base` (non
  `r.Context()`). Conseguenza: se il browser si chiude, il subprocess di scrittura
  **continua** ma è **irriattaccabile** — nessun job-id, nessun progress su disco.
- `internal/webui/workbench_exec.go:333-334` — un retry becca
  `409 "an execution is already in progress"`. Lo slot è **anonimo**: il 409 non
  dice quale azione/sessione lo tiene, da quando, a che punto.
- Progress item-level esiste **solo** per `migrate_content` (`--json-events` →
  `events.jsonl`). DNS/email/cron/pipeline producono solo report finali.

## 3. Deliverable

Se troppo grande, splittare come da roadmap §14:

- **PR 69a — Job Journal (fondazione minima):** `job.json` + superficie dell'exec in
  corso + fine del 409 opaco. **Questa è la priorità assoluta.**
- **PR 69b — Setup wizard + credential decision:** wizard nuova migrazione,
  setup source/dest/account, decisione credenziali (§12 roadmap), backup detection.

SSE (PR 70) resta subito dopo, invariato.

## 4. Proposta schema `job.json` — DA RATIFICARE a inizio sessione

> Decisione **aperta** (roadmap §16 punto 7). Questo è uno **spunto da ratificare**,
> non uno schema bloccato. Il journal è per-sessione, vive in `<dir>/job.json`
> (stessa working dir degli artifact), scritto atomico 0600 come `session.json`.

```json
{
  "session_id": "mig_20260704_1a4eaa2cc7d7",
  "action": "migrate_content",
  "started_at": "2026-07-06T10:12:03Z",
  "updated_at": "2026-07-06T10:18:41Z",
  "state": "running",            // running | completed | failed | interrupted
  "phase": "apply_core",          // step corrente (riusa workbench.Step)
  "item": "info@example.com",    // opzionale, solo dove disponibile (events.jsonl)
  "items_done": 4,                // opzionale
  "items_total": 12,              // opzionale — mai percentuale se denom inaffidabile
  "error": "",                    // valorizzato in state=failed
  "tool_version": "0.0.0-..."
}
```

**Vincoli di sicurezza (roadmap §12):** il journal registra **solo** identità e
progresso. **Mai** credenziali, **mai** l'argv risolto se può contenere segreti.

## 5. Lifecycle & wiring

Punto di innesto: `handleExec` (`workbench_exec.go`) e `jobManager.start()`
(`job.go`). Il journal si aggancia allo **stesso slot** `tryReserve()`:

1. `tryReserve()` riesce → **prima** di lanciare il subprocess, scrivere
   `job.json` con `state=running, action, session_id, started_at, phase`.
2. Durante l'exec: aggiornare `job.json` sui confini di fase (e, per
   `migrate_content`, `item/items_done` leggendo `events.jsonl`).
3. `defer release()` → scrivere `state=completed|failed` con `updated_at`/`error`.
4. **Crash-recovery:** all'avvio della `ui` (o al primo GET), se `job.json` è
   `state=running` ma lo slot in-memory è libero → l'exec è morto con il processo
   precedente → marcare `state=interrupted` (il subprocess figlio non sopravvive alla
   morte del padre `ui`; se il padre vive ma il browser no, lo slot resta occupato e
   `state=running` è corretto).

## 6. View-model di rehydration

`readArtifactFacts(dir)` **+** `job.json` **+** `session.Timeline` = view-model.
Regola: `job.json` ha priorità per il "cosa sta succedendo ORA"; gli artifact per il
"cosa è già fatto". Il render non deve mai mostrare item-level per una fase che ha solo
verità per-fase (roadmap §7).

## 7. Fine del 409 opaco

Quando `tryReserve()` fallisce, leggere `job.json` e restituire uno stato leggibile
invece di `409 "an execution is already in progress"`:

> «`migrate_content` in corso dalle 10:12 (fase apply_core, mailbox 4/12). Attendi il
> completamento o riapri per seguirne l'avanzamento.»

Vale per tutti e 3 i chiamanti dello slot (`/run`, `/accept`, `/exec`).

## 8. Backup detection per Rollback (§11 roadmap)

`readArtifactFacts` deve esporre `Backup{Dns,Email,Cron}Present`
(`fileExists(<dir>/<x>_backup.json)`). Il pulsante **Rollback** di una fase è offerto
**solo** se il backup esiste. Nessun rollback promesso senza backup.

## 9. Decisioni da ratificare a inizio sessione

1. **Schema `job.json`** (§4) — confermare campi/path/TTL.
2. **Progress granularity** (roadmap §7): estendere `--json-events` a tutte le fasi
   (A) o accettare progress per-fase per DNS/email/cron/pipeline (B)?
3. `Start migration` include email+cron o solo file/db/mail? (roadmap §16.1)
4. Credenziali: temporanee o profili persistenti? (roadmap §16.3) — 69b.

## 10. Criteri TDD (lista test minima)

- `job.json` scritto con `state=running` **prima** dell'avvio del subprocess.
- `job.json` → `completed` su successo, `failed` (+`error`) su fallimento.
- Refresh durante un exec attivo: il view-model mostra lo stato da `job.json` (non
  pagina morta, non 409 opaco).
- `tryReserve` fallito → risposta leggibile che cita action + started_at (non il 409 nudo).
- Recovery: `job.json` `state=running` + slot libero all'avvio → `interrupted`.
- Backup detection: Rollback offerto **solo** con `<x>_backup.json` presente.
- **Anti-leak (obbligatorio):** `job.json` non contiene mai stringhe di credenziali
  (guardia test come `manualaction_it` anti-leak).
- **Reuse (regressione):** `readArtifactFacts` resta l'unica fonte dello stato
  completato — nessuna duplicazione della logica di lettura artifact.
- Atomicità: `job.json` scritto write-temp+rename 0600 (stesso pattern `writeSession`).

## 11. Non-goal / off-limits

- Nessun writer/collector nuovo; nessun motore delta (roadmap §15).
- Nessun redesign oltre setup/rehydration (Flight Director è PR 71).
- Nessuna automazione cutover/DNS.
- `runner.go` **off-limits** (regola di progetto).
- Solo push a `fork`; PR con `--repo andreavadacchino/cPanel_self-migration`.

## 12. Definition of Done (gate)

- [x] Schema `job.json` ratificato e implementato (atomico, 0600, per-sessione).
      Ratifica: schema **lean** (solo identità+fase; item-level riusato da
      `loadRunMonitor`, non duplicato — §16.7). Granularity **opzione B** (§16.8).
- [x] Exec in corso sempre superficiale su refresh; 409 opaco eliminato ovunque
      (`writeBusy409` su `/run`, `/accept`, `/exec`). `job.json` scritto solo da
      `/exec` (path di scrittura); `/run`+`/accept` usano il fallback in-memory.
- [x] `readArtifactFacts` riusato, non riscritto (test di regressione verdi).
- [x] Recovery `running`→`interrupted` all'avvio processo (`recoverJobJournal`)
      + reconcile read-time nel view-model (`reconcileJobJournal`, no-write su GET).
- [x] Backup detection collega Rollback all'esistenza del backup
      (`areaFacts.BackupPresent`, gating nel template `screen_applica`).
- [x] Guardia anti-leak credenziali su `job.json` (testata anche sul failure path).
- [x] go-reviewer multi-giro fino APPROVE PULITO; Docker LINUX_ALL_GREEN eseguito.
- [x] Gate dichiarato nel body PR prima del merge.
