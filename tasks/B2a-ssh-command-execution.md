# Task B2a: SSH contract, host-key security, command execution

| Field | Value |
|---|---|
| **ID** | `B2a` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | A5 |
| **Branch** | `feat/b2a-ssh-command-execution` |

**Origin:** first half of the split of the original `B2` (see
[B2-implement-ssh-adapter.md](B2-implement-ssh-adapter.md)). B2a delivers the
typed SSH boundary and host-key-verified command execution; the streaming,
stdin, and backpressure concerns move to [`B2b`](B2b-ssh-streaming-backpressure.md).

**Goal:** Replace the `SshClient.run` stub with a secure, typed, testable SSH
command-execution boundary: host-key-verified connect, typed command building
(no arbitrary shell), bounded stdout/stderr with truncation, connect/command/idle
timeouts, cooperative cancellation, exit status/signal, connect-only retry, a
redacted secret-free audit, and a **structural** source-read-only /
destination-read / destination-write session separation. Real writes and any
network access stay disabled by default; no streaming yet.

**Current State:** `SshClient.run` always raises `NotImplementedError`
(`packages/adapters/adapters/ssh/__init__.py`).

**Scope (packages/adapters/adapters/ssh only):**

- `errors.py` — typed, secret-free error hierarchy (auth, host-key unknown/changed,
  connect/timeout, command timeout, command rejected, non-zero exit, cancelled,
  transport, write-not-authorized).
- `contract.py` — typed models (endpoint, credentials with secret redaction,
  timeouts, output limits, retry policy, session role), typed `command()` builder,
  `redact()`, `CommandResult`, redacted `SshCommandAudit`.
- `hostkeys.py` — persistent known-hosts store, fingerprint, and a policy that
  supports strict (default) and explicit accept-new, and **always** rejects a
  changed key. No silent auto-add.
- `client.py` — backend protocol, connect (verify host key *before* auth),
  `SshReadSession` (source-read and destination-read: no write/upload/stdin
  primitive) and `SshWriteSession` (destination-only, writes disabled by default).
- `fakes.py` — deterministic in-memory backend for tests (no real network).
- `__init__.py` — re-exports; keep a back-compatible `SshClient` symbol.
- `tests/` — deterministic unit tests; `README.md`, `.env.example` docs.

**Implementation:**

1. Define the typed contract and failure states.
2. Build the smallest production path behind disabled-by-default configuration;
   argv is quoted with `shlex` so no unvalidated shell interpolation is possible.
3. Verify the host key against a persistent store before authenticating; changed
   key always fails; accept-new records audibly.
4. Execute a command with separated, bounded stdout/stderr (truncation flagged),
   connect/command/idle timeouts, cooperative cancellation that closes the
   channel/transport, and exit status/signal in a redacted, evidence-bound result.
5. Retry only on connect (idempotent, before any command); never retry a command.
6. Keep secrets out of repr, errors, logs, results, and audit.

**Testing Requirements (deterministic fakes, no real servers):**

- [x] Source session exposes no write primitive; destination read vs write separated.
- [x] Known host key accepted; unknown host rejected by default; explicit accept-new
      records the key; changed host key rejected.
- [x] Password/key never present in repr/error/audit/result.
- [x] Connect timeout, command timeout, idle timeout each classified.
- [x] Cancellation before and during a command closes the channel.
- [x] stdout/stderr separated; output bounded with `truncated` flag.
- [x] Non-zero exit and remote signal surfaced; command builder rejects invalid
      input; no shell injection reaches the wire command.
- [x] Connect retry is idempotent; a command is never retried.
- [x] Close is idempotent; the fake backend is deterministic.
- [x] New safety-critical code has at least 90% line coverage (client.py 99%, overall 99%).
- [x] Existing API/worker suites do not regress.

**Acceptance Criteria:**

- [x] Changed host keys fail; cancellation closes streams; secrets never appear in
      commands/events/audit.
- [x] No new test, typecheck, Compose, or coverage regression.
- [x] Real behavior (network, writes) remains disabled by default until explicitly
      enabled for an authorized environment.

**Risk & Rollback:** Main risk is an unintended destination mutation or a false
verification. Keep writes disabled by default, revert the module if needed, and
never compensate by mutating the source.

**Verification Commands:**

```bash
cd packages/adapters && PYTHONPATH=. python -m pytest adapters/ssh/tests -q \
  --cov=adapters/ssh --cov-report=term-missing --cov-branch
cd ../../apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```

---

## Completion Record

**Data:** 2026-07-12

**Riepilogo implementazione.** Sostituito lo stub `SshClient.run` con un boundary
SSH tipizzato e testabile per l'esecuzione comandi verificata su host key, senza
alcuno streaming (rimandato a B2b) e senza collegamento al dispatch/writer.
Proprietà chiave:

- Separazione strutturale dei ruoli: `open_source_read_session` /
  `open_destination_read_session` restituiscono `SshReadSession` (nessuna primitiva
  write/stdin); `open_destination_write_session` restituisce `SshWriteSession` con
  `run_write` **disabilitato per default** (`allow_writes=False`) e che richiede
  destinazione verificata.
- Command builder tipizzato `command(program, *args)`: programma su whitelist,
  argomenti citati con `shlex.join` → nessuna shell injection, nessun entry point a
  stringa grezza.
- Verifica host key **prima** dell'auth su `KnownHostsStore` persistente: strict
  (default) rifiuta host sconosciuti, `accept_new` (override audibile) registra solo
  host nuovi, host key cambiata sempre rifiutata; fingerprint `SHA256:` nell'audit.
- Segreti (`repr=False`) esclusi da repr/errori/risultati/audit via `redact`.
- Output separato e limitato con flag `truncated` (memoria bounded); timeout
  connect/command/idle; cancellazione cooperativa che chiude il canale; `close`
  idempotente; retry solo su connect (comando mai ritentato).
- Backend di trasporto reale `paramiko_backend.py` (paramiko, import lazy, low-level
  `Transport` per verify-before-auth), escluso dalla coverage perché richiede rete;
  tutta la logica di policy vive in `client.py`, coperta da un fake deterministico.

**File principali.** Produzione (in `packages/adapters/adapters/ssh/`): `errors.py`,
`contract.py`, `hostkeys.py`, `client.py`, `paramiko_backend.py`, `fakes.py`,
`__init__.py`. Test: `tests/test_ssh_contract.py`, `tests/test_ssh_client.py`. Config:
`packages/adapters/pyproject.toml` (dep `paramiko>=3.4`, `[tool.coverage.run]` con
omit del backend paramiko). Doc: `README.md` (sezione B2a + tabella stato),
`.env.example` (policy host key, timeout, limiti, nota credenziali). Task: `B2`
marcato split, `B2a`/`B2b` creati, `BACKLOG.md` aggiornato (grafo + downstream
`B5→B2a`, `C1/C2/C3→B2b`).

**Test e comandi eseguiti (esito).**
- SSH adapter mirati + branch coverage: `PYTHONPATH=. python -m pytest
  adapters/ssh/tests --cov=adapters/ssh --cov-branch` → **49 passed**, coverage
  **99%** (client.py 99%, contract 100%, hostkeys 99%, errors 100%).
- Intera suite adapter: **130 passed** (81 cPanel + 49 SSH), nessuna regressione.
- API: `PYTHONPATH=../../packages/adapters python -m pytest` → **333 passed**.
- Worker (venv con dramatiq): `DRAMATIQ_TESTING=1 python -m pytest` → **18 passed**.
- Web: `npm run build` → **build OK** (tsc + Vite).
- Compose: `docker compose config -q` → **OK**.
- Coverage combinata app+adapter (collezionando anche i test adapter): **96%**,
  sopra il baseline 91% → nessuna regressione. (La run API-only mostra ssh a 0%
  perché i test ssh vivono nel package adapters e non sono raccolti da `app/tests`,
  come già avviene per cPanel; la coverage reale ssh è 99% via suite dedicata.)

**Esito review adversariale.** Verificati e coperti da test: command injection
(argv+shlex, nessuna stringa grezza), host-key bypass (verify-before-auth, changed
sempre rifiutata, no auto-add silenzioso, `authenticated=False` su rifiuto),
source-write (sorgente senza primitiva write, `run()` rifiuta comandi write),
secret leakage (repr/errori/audit/result redatti), retry dopo write parziale
(comando mai ritentato, `run_count==1`), output illimitato (bounded+truncated),
processi/channel non chiusi (`execution.close`/`session.close`/`handshake.close` su
ogni percorso, idempotenti), timeout mancanti (connect/command/idle passati al
transport). Note: lo streaming e l'hardening delle race cancellation/close in
streaming sono di competenza di **B2b**; la verifica live della destinazione prima
delle scritture reali è demandata ai writer (B5/B6/B7) — in B2a le scritture restano
disabilitate per default.

**Documentazione aggiornata.** `README.md`, `.env.example`, file task/BACKLOG come
sopra.

**Limitazioni residue (spostate in B2b).** stdin streaming autorizzato, stream
source→destination con backpressure, non-buffering integrale, interruzione stream,
no-retry su stream parziale, race cancellation/close in streaming.
