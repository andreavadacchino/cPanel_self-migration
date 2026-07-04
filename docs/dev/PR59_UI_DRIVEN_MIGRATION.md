# PR 59 — UI-Driven Single-Account Migration

## Emendamento dell'invariante #58

PR #58 stabiliva: _"the UI NEVER executes apply/cutover/rollback"_.

Questo emendamento CONSAPEVOLE autorizza la Workbench a LANCIARE i passi
della migrazione come subprocess, con 4 guardrail non negoziabili:

| # | Guardrail | Meccanismo |
|---|-----------|------------|
| 1 | Solo subprocess del PROPRIO binario | `os.Executable()` + `exec.CommandContext` — pattern identico a `job.go:execRunner`. La UI non contiene MAI logica di migrazione (zero import di sshx/cpanel). |
| 2 | Conferma forte per ogni WRITE | Il POST richiede: (a) CSRF token, (b) campo `confirm_account` == nome esatto dell'account nella sessione. Mismatch → 403, zero esecuzione. Le azioni READ-ONLY (inventory, diff, plan, verify) bastano un click. |
| 3 | Tutto loopback-only | Binding 127.0.0.1, Host/Origin check, anti-DNS-rebinding — ereditato dal server esistente. |
| 4 | Timeline immutabile | Ogni esecuzione registra: comando, exit code, durata, artifact prodotti. Nessun artifact parziale allegato (attach solo su exit 0). |

**Cosa RESTA FUORI dalla UI**: cutover (TTL, synczone, sospensione .193) — sono azioni root/runbook, non comandi del tool.

### Note sicurezza (post review adversariale)

- **CSRF lifetime**: il token è per-start del server (come già in PR58). Per un tool loopback-only è accettabile: replay richiede accesso locale alla stessa macchina. Documentato qui come decisione consapevole.
- **Ordering enforcement**: la funzione `validateStrongConfirmation` DEVE essere chiamata PRIMA della costruzione dell'argv nel body della funzione. Il test AST verifica questo ordinamento (statement index della call < statement index della costruzione argv).
- **Artifact concurrent write**: il subprocess scrive nella working directory; `AttachArtifact` COPIA atomicamente (flock + temp + rename). Non c'è race perché attach avviene DOPO exit 0 del subprocess, con flock acquisito.

---

## Modello di esecuzione

Riuso ESATTO del pattern `job.go`:
- `StepRunner` iniettabile (tests usano fake, prod usa `execRunner`)
- Un solo job alla volta per sessione (`tryReserve`/`release`)
- Context discende da `BaseContext` (kill subprocess su SIGINT)
- `tailBuffer` per output (meta-refresh 2s per "streaming")
- Timeout hard (30 min)

**Differenza chiave**: il `jobManager` esistente orchestra una PIPELINE (passi sequenziali fissi). Il launcher PR59 esegue UNA SINGOLA azione alla volta (l'operatore sceglie quale), con tipo read-only o write.

**Job slot separato**: il launcher PR59 ha il proprio slot (`execSlot`) indipendente dal `jobManager` della pipeline `/run`. Motivazione: la pipeline read-only opera sulla webui dir; il launcher opera sulla sessione workbench. Sono directory diverse, file diversi, nessun conflitto. Ma ALL'INTERNO della sessione, un solo exec alla volta.

**tailBuffer esteso**: per il launcher PR59, il buffer è 64 KiB (vs 4 KiB della pipeline). Le operazioni di migrazione possono durare 20+ minuti con output rsync verbose — 4 KiB non basta per diagnosticare fallimenti.

**Cleanup su fallimento**: se il subprocess esce con codice != 0, il launcher NON allega artifact. I file parziali rimangono nella working directory (l'operatore può ispezionarli), ma non entrano nella sessione. Se il subprocess viene killato (timeout/SIGINT), stderr è nel tailBuffer e viene registrato nella timeline come last_error.

---

## Mappa step → subcommand → artifact → transizione

### Azioni read-only (click singolo + CSRF)

| Step sessione | Subcommand invocato | Artifact prodotto | Kind | Transizione auto |
|---|---|---|---|---|
| inventory | `--account-inventory --config host.yaml --output-dir DIR` | inventory_source.json, inventory_destination.json | `inventory_source`, `inventory_destination` | → `inventory_ready` |
| diff+policy+checklist | pipeline 4-step esistente (identica a `/run`) | diff, policy, checklist | `inventory_diff`, `policy_report`, `migration_checklist` | → `checklist_ready` |
| dns-plan | `inventory dns-plan --source SRC --destination DEST --output-json PLAN` | dns_import_plan.json | `dns_plan` | nessuna |
| email-plan | `inventory email-plan ...` | email_apply_plan.json | `email_plan` | nessuna |
| cron-plan | `inventory cron-plan ...` | cron_apply_plan.json | `cron_plan` | nessuna |
| dns verify | `dns verify --plan PLAN --config host.yaml --output-json REPORT` | dns_verify_report.json | `dns_verify_report` | vedi sotto |
| email verify | `email verify --plan PLAN --config host.yaml --output-json REPORT` | email_verify_report.json | `email_verify_report` | vedi sotto |
| cron verify | `cron verify --plan PLAN --config host.yaml --output-json REPORT` | cron_verify_report.json | `cron_verify_report` | vedi sotto |

### Azioni write (conferma forte: tipo account name + CSRF)

| Step sessione | Subcommand invocato | Flag critica | Artifact prodotto | Kind |
|---|---|---|---|---|
| migrate content | `--apply --config host.yaml --output-dir DIR [--mail] [--file] [--db] [--domain D]` | (nessuna extra) | report.json, events.jsonl | `apply_report`, `events_jsonl` |
| dns apply | `dns apply --plan PLAN --config host.yaml --yes-apply-writes --output-json REPORT` | `--yes-apply-writes` | dns_apply_report.json | `dns_apply_report` |
| email apply | `email apply --plan PLAN --config host.yaml --yes-apply-writes --output-json REPORT` | `--yes-apply-writes` | email_apply_report.json | `email_apply_report` |
| cron apply | `cron apply --plan PLAN --config host.yaml --yes-apply-writes --output-json REPORT` | `--yes-apply-writes` | cron_apply_report.json | `cron_apply_report` |
| dns rollback | `dns apply --rollback BACKUP --report REPORT --config host.yaml --yes-apply-writes` | `--yes-apply-writes` + `--rollback` | (sovrascrive report) | `dns_apply_report` |
| email rollback | `email apply --rollback BACKUP --report REPORT --config host.yaml --yes-apply-writes` | `--yes-apply-writes` + `--rollback` | (sovrascrive report) | `email_apply_report` |
| cron rollback | `cron apply --rollback BACKUP --config host.yaml --yes-apply-writes` | `--yes-apply-writes` | (sovrascrive report) | `cron_apply_report` |

### Note sul mapping (post review adversariale)

- **Content verify**: non esiste un subcommand "content verify" separato. La verifica dei contenuti è embedded nel `--apply` stesso (`--verify-checksums`, `--deep-verify`). Il checklist step valuta il migration report come evidenza. La verifica dei config track (dns/email/cron) è separata perché opera su piani deterministici.
- **Scope selection per migrate content**: la UI mostra un form con checkboxes `mail`/`file`/`db` e un campo opzionale `domain` (filtro). L'argv viene costruito dalla selezione: `--mail` se mail checked, etc. Se nessuno è selezionato → errore validation.
- **Re-apply idempotency**: è by design. L'operatore PUÒ ri-eseguire un apply (es. dopo aver fixato un problema). Il backup-or-nothing nel codice apply garantisce safety. Il report precedente viene sovrascritto nella working directory; nella sessione, artifact sono APPEND-only (immutabili) — entrambi i report restano nella storia.
- **Auto-transition failure UX**: se `ready_for_cutover` è tentata ma il session status è cambiato (es. CLI ha messo `blocked`), il flock + transition rules rifiutano silenziosamente. La UI mostra semplicemente lo stato corrente al prossimo refresh — nessun errore visibile, stato coerente.

### Transizione automatica a `ready_for_cutover`

Condizioni (TUTTE devono essere vere, verificate dagli artifact nella sessione):

1. Checklist presente senza blockers aperti (non accettati)
2. Apply reports per tutti i track (email/cron/dns) con status != `refused` pendenti
3. Verify reports per tutti i track con verdict = CLEAN

La transizione è tentata AUTOMATICAMENTE dopo ogni verify riuscito. Se i
criteri non sono provati dagli artifact, resta manuale (`set-status --force`).

---

## Struttura file

```
internal/webui/
  workbench.go            ← INVARIATO (governance pura, zero exec)
  workbench_exec.go       ← NUOVO: handler esecuzioni, conferma forte
  workbench_exec_test.go  ← NUOVO: TDD golden argv, conferma forte
  workbench_safety_test.go ← EMENDATO: allowlist dichiarata
  job.go                  ← INVARIATO (riusa StepRunner)
  accept.go              ← INVARIATO (acceptances via subprocess)
```

---

## Sicurezza: emendamento safety test

Il test `TestWorkbenchUINoApplyVerbs` verifica che `workbench.go` non
contenga `exec.Command` o `"--apply"`. Questo resta INVARIATO.

Il NUOVO file `workbench_exec.go`:
- PUÒ contenere `exec.Command` (via il `StepRunner` iniettato)
- PUÒ costruire argv con `"--apply"` e `"--yes-apply-writes"`
- MA deve passare un NUOVO test che verifica: ogni handler write ha la
  validazione `confirm_account == session.Name` sul percorso obbligato
  PRIMA di qualsiasi invocazione del runner

Il safety test emendato:
1. `TestWorkbenchUINoForbiddenImports` → INVARIATO (nessun import sshx/cpanel in nessun file webui)
2. `TestWorkbenchUINoApplyVerbs` → INVARIATO (scansiona SOLO `workbench.go`)
3. NUOVO `TestAllApplyVerbsRequireStrongConfirmation` → scansiona TUTTI i file .go non-test in webui/: qualsiasi file che contiene i letterali `--yes-apply-writes` o `"--apply"` (escluso workbench_safety_test.go) DEVE avere una chiamata a `validateStrongConfirmation` nello STESSO handler, PRIMA della costruzione argv (verificato via AST statement ordering). Questo previene il gaming via file helper indiretti.

---

## Cosa NON cambia

- `runner.go` (off-limits)
- `workbench.go` (governance pura)
- `job.go` / `accept.go` (pattern riusato, non modificato)
- `internal/workbench/` (store, types, status, artifacts — API riusata)
- Pipeline read-only della webui originale (`/run`)
- CLI (tutti i subcommand restano invocabili da terminale identicamente)

## Fuori scope

- Cutover dalla UI (resta runbook)
- Campagna/coda/multi-account
- SQLite
- Nuovi writer/collector
- Modifiche a runner.go
- SSE/WebSocket (meta-refresh basta)
