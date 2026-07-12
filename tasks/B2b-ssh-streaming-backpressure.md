# Task B2b: SSH streaming, cancellation, backpressure — SPLIT (retired)

| Field | Value |
|---|---|
| **ID** | `B2b` (ritirato) |
| **Status** | `[/]` split — non completare con questo ID |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B2a |
| **Branch** | `feat/b2b-ssh-streaming-backpressure` (non usare) |

> **Split.** L'implementazione completa di B2b è stata misurata a **~1080 righe su
> ~9 file** (streaming.py contratti+pump ~270, wiring `client.py` ~70, streaming
> paramiko ~70, fake source/sink ~150, ~460 di test, ~50 doc), oltre i guardrail 8
> file / 500 righe. Come previsto da questo stesso task ("misura prima di
> implementare; se necessario proponi un ulteriore split"), è stata suddivisa in:
>
> - [`B2b-i` — SSH stream contracts, pump, fake](B2b-i-ssh-stream-pump.md) (dep: B2a)
> - [`B2b-ii` — SSH stream session wiring and paramiko lifecycle](B2b-ii-ssh-stream-sessions.md) (dep: B2b-i)
>
> B2b-i è il minimo boundary testabile del motore di streaming: il `pump()` opera
> su protocolli `ByteSource`/`StdinSink` ed è verificato contro un fake
> deterministico, senza sessioni né rete. B2b-ii collega i ruoli sulle sessioni
> (`start_stdout`/`start_stdin` autorizzato) e il trasporto paramiko reale. Le
> dipendenze di trasferimento contenuti (C1/C2/C3) puntano a `B2b-ii`. L'ID `B2b` è
> ritirato e non riutilizzato. Il testo storico sottostante resta come riferimento.

---

**Origin:** second half of the split of the original `B2` (see
[B2-implement-ssh-adapter.md](B2-implement-ssh-adapter.md)). B2b builds on the
typed boundary, host-key security, and command execution delivered by
[`B2a`](B2a-ssh-command-execution.md).

**Goal:** Add authorized stdin streaming on the destination, a
source→destination stream with backpressure that never buffers the whole payload
in memory, stream interruption handling on either side, a no-retry rule for a
partially started stream, and safe cancellation/close races — all behind the
same disabled-by-default configuration.

**Scope (packages/adapters/adapters/ssh only):**

- `streaming.py` — bounded, backpressured pump for stdin and source→destination
  copy; chunked, never fully buffered; cooperative cancellation.
- `client.py` — expose the stdin/streaming primitive only on the authorized
  destination write session; source stays read-only.
- `errors.py` — reuse `SshStreamInterruptedError`; add stream-specific detail if needed.
- `fakes.py`, `tests/` — deterministic streaming fakes and tests; docs.

**Testing Requirements (deterministic fakes, no real servers):**

- [ ] Stream with backpressure (a slow consumer throttles the producer).
- [ ] Stream is not fully buffered in memory (bounded window asserted).
- [ ] Source interruption and destination interruption each fail closed.
- [ ] A partially started stream is never retried.
- [ ] Cancellation during a stream closes the channel/transport without a race.
- [ ] Close remains idempotent under a concurrent cancellation.
- [ ] New safety-critical code has at least 90% line coverage; no regression.

**Acceptance Criteria:**

- [ ] Streaming never buffers the whole payload; a partial stream is not retried;
      cancellation closes streams; secrets never appear anywhere.
- [ ] No new test, typecheck, Compose, or coverage regression.
- [ ] Real behavior remains disabled by default.

**Risk & Rollback:** Same as B2a — keep writes/streaming disabled by default,
revert the module if needed, never compensate by mutating the source.

**Verification Commands:**

```bash
cd packages/adapters && PYTHONPATH=. python -m pytest adapters/ssh/tests -q \
  --cov=adapters/ssh --cov-report=term-missing --cov-branch
cd ../../apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```
