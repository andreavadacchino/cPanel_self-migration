# Task B2b-ii: SSH stream session wiring and paramiko lifecycle

| Field | Value |
|---|---|
| **ID** | `B2b-ii` |
| **Status** | `[x]` |
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

- [x] Source session exposes no stdin/write; destination read session exposes no stdin.
- [x] Destination write session exposes only typed streaming operations, gated by
      `allow_writes` + verified destination.
- [x] End-to-end source stdout → destination stdin via the session boundary.
- [x] Cancellation during a streamed session closes both channels exactly once.
- [x] New safety-critical code has at least 90% line coverage; no regression.

**Acceptance Criteria:**

- [x] Source stays read-only; stdin is reachable only on an authorized, verified
      destination write session; secrets/content never appear anywhere.
- [x] No new test, typecheck, Compose, or coverage regression.
- [x] Real behavior remains disabled by default.

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

## Completion Record

- **Date:** 2026-07-12
- **Summary:** Wired the B2b-i `ByteSource`/`StdinSink` contracts onto the typed
  SSH sessions. Read/source sessions expose only `start_stdout`; only an
  explicitly authorized and verified destination-write session exposes
  `start_stdin`. `stream_between` starts both typed operations, closes the source
  if destination startup fails, and delegates bounded backpressure, cancellation,
  redacted evidence, and exactly-once channel cleanup to the proven pump.
- **Files changed:** `ssh/client.py`, `ssh/fakes.py`,
  `ssh/paramiko_backend.py`, `ssh/__init__.py`,
  `ssh/tests/test_ssh_stream_sessions.py`, `migration-platform/README.md`, and
  the task/backlog state files. Scope: 8 files and fewer than 500 changed lines.
- **Transport:** Paramiko now exposes incremental stdout reads and partial stdin
  writes, bounded stderr, EOF via `shutdown_write`, idempotent close, typed
  timeout/transport errors, and cached exit status. Host-key verification and
  authentication remain exclusively in the B2a connection path.
- **Tests:** targeted stream/session tests 32 passed; full SSH suite 82 passed
  with 99% branch coverage (`streaming.py` 100%, `client.py` 99%); complete
  adapter suite 163 passed; API suite passed; worker 18 passed; web TypeScript +
  Vite build passed; `docker compose config -q` passed; `git diff --check` clean.
- **Review:** Checked role leakage, startup cleanup, retry-after-start, short
  writes, incremental reads, EOF, bounded stderr, close/cancel lifecycle, shared
  connection ownership, host-key reuse, and secret/content redaction. One Medium
  issue was fixed: Paramiko exit status was read more than once; it is now cached
  and covered by a fake-channel regression test.
- **Documentation:** Updated the V2 README status table. No API, dispatch, writer,
  C1/C2/C3, or real-server behavior was added. Streaming remains unreachable
  until a later transfer task explicitly constructs authorized sessions.
