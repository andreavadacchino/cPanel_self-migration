# Task B2b: SSH streaming, cancellation, backpressure

| Field | Value |
|---|---|
| **ID** | `B2b` |
| **Status** | `[ ]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B2a |
| **Branch** | `feat/b2b-ssh-streaming-backpressure` |

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
