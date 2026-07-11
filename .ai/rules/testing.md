# Testing Conventions

## Baseline

- API: 117 tests passing; measured API+adapter coverage 91%.
- cPanel client coverage: 24%.
- Worker tests require the worker dependencies; the host environment currently lacks `dramatiq`.
- Frontend has build/typecheck but no automated test suite.

## Requirements

- Add a regression test for every bug.
- Test happy path, stale evidence, failed reads, retry, cancellation, redaction, and source-read-only invariants for real execution work.
- Mock network boundaries in unit tests; add separately marked sandbox cPanel integration tests.
- Never treat an operator confirmation as verification evidence.

## Do / Don't

| Do | Don't |
|---|---|
| Assert persisted state and events | Assert only HTTP status codes |
| Test crash/retry boundaries | Assume actors run exactly once |

