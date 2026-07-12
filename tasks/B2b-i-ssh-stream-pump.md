# Task B2b-i: SSH stream contracts, pump, fake

| Field | Value |
|---|---|
| **ID** | `B2b-i` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B2a |
| **Branch** | `feat/b2b-i-ssh-stream-pump` |

**Origin:** first half of the split of `B2b` (see
[B2b-ssh-streaming-backpressure.md](B2b-ssh-streaming-backpressure.md)). B2b-i
delivers the typed streaming contracts and the backpressured `pump()` engine with
its deterministic fake; the session role wiring and the paramiko transport move to
[`B2b-ii`](B2b-ii-ssh-stream-sessions.md).

**Goal:** Add a typed source→destination streaming engine that is genuinely
backpressured (write-before-next-read, one bounded chunk in flight, no unbounded
queue), memory-bounded independent of total size, cooperatively cancellable at
every phase, with distinct start/idle/total/close timeouts, bounded stderr from
both sides, exit code/signal from both sides, a transferred-byte count, a
redacted rate-limited progress callback, and a typed **partial** result carrying
the byte count and failure side. A started stream is never retried. No payload or
secret ever reaches a log, error, audit, or progress. No session/paramiko/dispatch
wiring here.

**Scope (packages/adapters/adapters/ssh only):**

- `streaming.py` — `ByteSource` / `StdinSink` protocols (source produces bytes only;
  sink receives stdin), `StreamOptions` (chunk size, high-water mark, stderr caps,
  timeouts, progress interval), `StreamResult` / `StreamFailureSide` /
  `StreamOutcome` / `StreamProgress`, and the `pump()` engine.
- `errors.py` — reuse `SshStreamInterruptedError`; add stream-start detail if needed.
- `fakes.py` — deterministic `FakeByteSource` / `FakeStdinSink` able to simulate
  chunking, a slow consumer, partial writes, blocking, cancellation, and disconnect.
- `__init__.py` — export the streaming contracts and `pump`.
- `tests/` — deterministic pump tests; docs (`README.md`).

**Implementation:**

1. Define the typed streaming contracts and result/partial states.
2. Implement `pump()`: read one bounded chunk, write it fully (handling short
   writes) before reading the next, so a slow sink naturally throttles the source;
   hold at most one chunk (measurable high-water mark). Check cancellation before
   start, before each read, during a blocked write, and while awaiting exit.
   Enforce distinct start/idle/total/close timeouts on a monotonic clock. Close
   both sides exactly once (even when the source fails), drain within a bounded
   grace. Return a typed partial result on timeout/cancel/interruption; never retry.
3. Keep the transferred bytes out of every log/error/audit/progress; redact secrets.

**Testing Requirements (deterministic fakes, no real servers):**

- [x] Small stream; stream much larger than the buffer; bounded high-water mark asserted.
- [x] Slow consumer applies backpressure; partial write completes the chunk without loss.
- [x] Source EOF closes destination stdin; source/destination exit non-zero surfaced.
- [x] Source disconnect; destination disconnect / broken pipe.
- [x] Cancellation before start, during read, during a blocked write, during exit wait.
- [x] Idle timeout; total timeout; close is idempotent; both channels closed once.
- [x] Partial result carries byte count and failure side; a partial stream is not retried.
- [x] Progress callback never receives payload; stderr bounded/truncated.
- [x] No secret/content in repr/error/audit; B2a command execution does not regress.
- [x] New safety-critical code has at least 90% line coverage (streaming.py 100%).

**Acceptance Criteria:**

- [x] Streaming never buffers the whole payload; a partial stream is not retried;
      cancellation closes streams; secrets/content never appear anywhere.
- [x] No new test, typecheck, Compose, or coverage regression.
- [x] Real behavior remains disabled by default (no transport wired here).

**Risk & Rollback:** Same as B2a/B2b — keep streaming disabled by default, revert
the module if needed, never compensate by mutating the source.

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

**Riepilogo implementazione.** Aggiunto il motore di streaming source→destination
`pump()` con i contratti tipizzati `ByteSource` (produce solo byte) / `StdinSink`
(riceve stdin), senza toccare le sessioni né il trasporto reale (rimandati a
B2b-ii). Proprietà:

- **Backpressure reale, memoria bounded**: il pump scrive un chunk per intero prima
  di leggere il successivo, tenendo in memoria un solo chunk (`chunk_size` =
  high-water mark) → memoria massima indipendente dalla dimensione totale, nessuna
  coda non limitata. Short write gestite (il chunk è completato senza perdita).
- **Cancellazione cooperativa** verificata prima dello start, durante read, durante
  una write bloccata e nell'attesa exit; **timeout distinti** start/idle/total/close
  su clock monotòno iniettabile; ogni timeout/cancel/interruzione restituisce un
  `StreamResult` **parziale tipizzato** con byte trasferiti e lato del guasto.
- **Nessun retry**: un flusso iniziato non viene mai ritentato dal pump.
- **Chiusura una sola volta** di entrambi i canali (destinazione drenata anche se la
  sorgente fallisce) tramite guard idempotente in `finally`.
- **Gestione**: source/destination exit non-zero (COMPLETED+failure_side),
  disconnect sorgente/destinazione, broken pipe, EOF (chiude stdin destinazione),
  stderr bounded/troncato di entrambi i lati, exit code/signal, conteggio byte.
- **Progress callback** rate-limited e **senza payload** (solo contatori); dati
  trasferiti e segreti mai in log/errori/audit/progress (`redact`), campi bytes con
  `repr=False`.
- **Fake deterministico** (`FakeByteSource`/`FakeStdinSink`/`FakeClock`) che simula
  chunking, consumer lento, partial write, blocchi, cancel, disconnect e attese exit.

**File principali.** `packages/adapters/adapters/ssh/streaming.py` (nuovo, contratti
+ `pump`), `fakes.py` (aggiunti `FakeClock`, `FakeByteSource`, `FakeStdinSink`,
`SourceStep`), `__init__.py` (export streaming), `tests/test_ssh_streaming.py`
(nuovo, 25 test). Doc: `README.md` (sezione B2b-i + riga stato). Task: `B2b` marcato
split, `B2b-i`/`B2b-ii` creati, `BACKLOG.md` aggiornato (grafo `B2a→B2b-i→B2b-ii`,
downstream `C1/C2/C3→B2b-ii`).

**Test e comandi eseguiti (esito).**
- SSH mirati + branch coverage: `PYTHONPATH=. python -m pytest adapters/ssh/tests
  --cov=adapters/ssh --cov-branch` → **74 passed**, coverage **99%**
  (streaming.py **100%**, client.py 99%, contract 99%, hostkeys 98%).
- Intera suite adapter: **155 passed** (81 cPanel + 74 SSH), nessuna regressione.
- API: **333 passed**. Worker (venv): **18 passed**. Web `npm run build`: **OK**.
  Compose `docker compose config -q`: **OK**.

**Esito review adversariale.** Verificati e coperti da test: buffering non bounded
(un solo chunk, high-water mark asserito su payload 1 MB), deadlock source/dest
(modello sincrono write-before-read, timeout a limitare gli stalli), backpressure
reale (consumer lento → molte write prima della read successiva), short write
(chunk completato senza perdita), retry dopo avvio remoto (mai ritentato,
`read_calls` asserito), channel leak / doppia close (`close_count == 1`, guard
idempotente), race cancellazione (checkpoint cooperativi a ogni fase; hardening
concorrente delegato a B2b-ii), timeout che interrompe I/O (budget per-call +
boundary check), payload nei log (audit/progress solo contatori; messaggi generici
redatti), source write exposure (il protocollo `ByteSource` non ha primitiva di
write/stdin). La separazione strutturale dei ruoli sulle sessioni e il trasporto
paramiko sono in **B2b-ii**.

**Documentazione aggiornata.** `README.md` (motore di streaming B2b-i + riga stato).

**Limitazioni residue (spostate in B2b-ii).** Wiring dei ruoli sulle sessioni
(`SourceReadSession.start_stdout`, `DestinationWriteSession.start_stdin`
autorizzato), backend paramiko di streaming (stdin write + stdout stream con flow
control reale), test strutturali (source/dest-read senza stdin; dest-write solo
operazioni tipizzate) e integrazione end-to-end via sessione; hardening della race
concorrente cancel()/close() a livello di lifecycle di sessione.
