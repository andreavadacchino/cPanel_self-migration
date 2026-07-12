# Task B2b-ii: SSH stream session wiring and paramiko lifecycle

| Field | Value |
|---|---|
| **ID** | `B2b-ii` |
| **Status** | `[ ]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B2b-i |
| **Branch** | `feat/b2b-ii-ssh-stream-sessions` |

**Origin:** second half of the split of `B2b` (see
[B2b-ssh-streaming-backpressure.md](B2b-ssh-streaming-backpressure.md)). B2b-ii
wires the streaming contracts and `pump()` from
[`B2b-i`](B2b-i-ssh-stream-pump.md) onto the typed sessions and the real paramiko
transport.

**Goal:** Expose the streaming primitives with structural role separation and back
them with the real transport: `SourceReadSession.start_stdout(operation) ->
ByteSource` (source produces bytes only), `DestinationWriteSession.start_stdin(
operation) -> StdinSink` gated by the same `allow_writes` + verified-destination
authorization as B2a writes. Read and source sessions expose no stdin/write.
Implement the paramiko streaming execution (stdin write + stdout stream) without
network tests, keep streaming disabled by default, and do not wire dispatch, C1,
C2, C3, or the B5 writers.

**Scope (packages/adapters/adapters/ssh only):**

- `client.py` — `start_stdout` on read/source sessions (bytes only, no stdin) and
  `start_stdin` on the authorized destination write session; lifecycle and
  close/cancel integration with the pump.
- `paramiko_backend.py` — streaming execution: bounded stdout reads and stdin
  writes honouring channel flow control; exit status/signal.
- `fakes.py`, `tests/` — session-level fakes and structural/integration tests; docs.

**Testing Requirements (deterministic fakes, no real servers):**

- [ ] Source session exposes no stdin/write; destination read session exposes no stdin.
- [ ] Destination write session exposes only typed streaming operations, gated by
      `allow_writes` + verified destination.
- [ ] End-to-end source stdout → destination stdin via the session boundary.
- [ ] Cancellation during a streamed session closes both channels exactly once.
- [ ] New safety-critical code has at least 90% line coverage; no regression.

**Acceptance Criteria:**

- [ ] Source stays read-only; stdin is reachable only on an authorized, verified
      destination write session; secrets/content never appear anywhere.
- [ ] No new test, typecheck, Compose, or coverage regression.
- [ ] Real behavior remains disabled by default.

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
