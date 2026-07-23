# Execution bridge — run and ingest a dry-run (M3 core)

> Stato: `feat/platform-v2-execution-bridge`, basato su `fork/main` = `dd7bb1d`.
> Roadmap: [`EXECUTION_ROADMAP.md`](EXECUTION_ROADMAP.md) §M3 · ADR: [`../../docs/ADR_V2_GO_EXECUTOR.md`](../../docs/ADR_V2_GO_EXECUTOR.md)

## Cosa fa

`apps/worker/worker/execution_bridge.py` è il **cuore run+ingest** di un dry-run:
il pezzo che fa *partire ed esitare* un'esecuzione, oggi inesistente. Dato un
percorso all'executor **già verificato** e una workspace SSH **già costruita**
altrove, il motore:

1. **materializza** lo `execution-spec-v1` canonico nella directory di output del
   run, con permessi privati (`0600`, `O_EXCL`), a partire da uno spec **già
   ancorato** (`materialize_spec` rivalida i byte esatti: uno spec invalido non
   viene mai scritto né passato all'executor);
2. **lancia** `‹executor› execute --spec … --config host.yaml --output-dir …` nel
   **proprio process group** (`start_new_session=True`), con un **ambiente
   spogliato** che il chiamante controlla per intero e un **timeout** la cui
   scadenza reap-a l'intero gruppo (`SIGTERM` → grace → `SIGKILL`);
3. **ingerisce e valida** `events.jsonl` riga per riga e `report.json` contro il
   contratto (`domain.execution_contract`), correlando ogni documento al `run_id`
   dell'esecuzione;
4. **classifica** l'esito terminale.

## L'invariante `partial`

Un dry-run non scrive nulla, quindi nessuna interruzione può lasciare la
destinazione a metà: l'esito è **solo** `succeeded`, `failed` o `interrupted`,
**mai** `partial`. È codificato (`_EXIT_STATUS_TO_TERMINAL` non contiene
`partial`) e presidiato da un test (`test_a_dry_run_never_terminalizes_as_partial`).

Mappatura:

| Condizione | `terminal_status` |
|---|---|
| exit 0 + `report.exit_status == success` | `succeeded` |
| `report.exit_status == failed` | `failed` |
| `report.exit_status == interrupted` | `interrupted` |
| timeout → gruppo reap-ato | `interrupted` (nessun report) |

## Confine — cosa NON fa (per design, in questo incremento)

- **Nessun DB, nessuna Session, nessun lease.** Il motore prende `path`, non righe.
  L'acquisizione/rilascio dell'attempt+lease (`apps/api/.../executions/attempts.py`)
  e la terminalizzazione durevole restano dell'**actor** che avvolgerà questo motore.
- **Nessun dispatch.** Non c'è `.send()` né actor Dramatiq registrato: nessuna
  execution viene ancora accodata.
- **Nessuna risoluzione credenziali, nessuna workspace, nessun handshake.** Il
  motore riceve un `executable_path` (che l'actor otterrà da #119) e un
  `host_config_path` (che l'actor otterrà da #118). Non li costruisce e non li
  parsa: `host.yaml` è passato a `--config` e mai letto qui.
- **Nessuno smoke live.** Nessun SSH reale: la suite gira contro un *fake
  executor* che parla lo stesso contratto. Lo smoke reale resta gated per roadmap
  (serve account sacrificabile + accesso SSH bilaterale).

## Sicurezza

- **Nessun segreto** è letto, loggato o restituito. Lo spec porta solo
  riferimenti; le credenziali vivono in `host.yaml` (mai parsato qui); i
  validatori di contratto rifiutano un documento che ne abbia trapelato uno.
- **Ambiente non ereditato.** `run_execution(env=…)` è l'ambiente **completo** del
  subprocess: il motore non fonde mai l'ambiente del worker, così un segreto che
  il worker detiene non può raggiungere l'executor (verificato:
  `test_the_subprocess_environment_is_exactly_what_the_engine_is_given`).
- **One execution = one artifact set.** Una output directory che già contiene
  `events.jsonl`/`report.json` di un run precedente è **rifiutata** prima del
  lancio (stessa difesa del bridge Go), non interlacciata.

## Prossimo passo (fuori da questo incremento)

L'**actor dry-run**: `pending → queued → running` atomico, ricarico da PostgreSQL,
ricontrollo freschezza pre-start, `acquire_attempt` → risoluzione SSH (#118) →
handshake executor (#119) → `run_execution` (questo motore) → terminalizzazione
via `finish_attempt` → cleanup. Richiede la risoluzione del *seam* worker↔attempts
(il lifecycle lease vive in `apps/api` su `Session` ORM; il worker è Core-only).

## Gate eseguiti

| Gate | Esito |
|---|---|
| `pytest` worker (`DRAMATIQ_TESTING=1`) | 30 passed (16 base + 14 nuovi) |
| Go / contratto | non toccati (zero file Go nel diff) |
| Frontend | non toccato |
| Smoke live SSH | **non eseguito** — gated (nessuna credenziale/binario reale in sessione) |
