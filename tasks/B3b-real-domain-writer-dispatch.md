# Task B3b: Real domain writer phase and dispatch wiring — SPLIT (retired)

| Field | Value |
|---|---|
| **ID** | `B3b` (ritirato) |
| **Status** | `[/]` split — non completare con questo ID |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B3a |
| **Branch** | `feat/b3b-real-domain-writer-dispatch` (non usare) |

> **Split.** L'implementazione completa di B3b è stata misurata a **~660 righe**
> (produzione ~384: `real_domain_writer.py` 209 + wiring `dispatch.py`/`config.py`
> ~175; più ~250 di test e ~30 di doc), oltre il guardrail di 500 righe. Come
> imposto dal task, l'implementazione è stata fermata e B3b suddiviso in:
>
> - [`B3b-i` — Real domain write phase engine](B3b-i-domain-phase-engine.md) (dep: B3a)
> - [`B3b-ii` — Domain phase dispatch wiring](B3b-ii-domain-phase-dispatch.md) (dep: B3b-i)
>
> Le dipendenze downstream che puntavano a `B3b` ora puntano a `B3b-ii` (il wiring
> che abilita il writer reale end-to-end): B4, B5, B6, B7 e C1. L'ID `B3b` è
> ritirato e non riutilizzato. Il testo storico sottostante resta come riferimento.

---

**Origin:** second half of the split of the original `B3` (see
[B3-real-domain-writer.md](B3-real-domain-writer.md)). B3b consumes the typed
adapter operations and pure rules delivered by
[`B3a`](B3a-domain-adapter-rules.md) and turns them into a complete real write
phase wired into the Wave A runtime.

**Goal:** Implement the additive, destination-only real domain writer phase and
wire it into the real dispatch/actor so a run whose only phase is an eligible
`domains` phase actually executes instead of always halting — behind the
double gate `DOMAIN_WRITER_MODE=enabled` **and** `REAL_EXECUTION_MODE=enabled`.

**Scope:**

- `apps/api/app/modules/executions/domain_writer.py` — real phase orchestration
  (keeps the existing mock path and dry-run behavior intact).
- `apps/api/app/modules/executions/dispatch.py` — populate `_real_phases` and run
  the domain phase inside `worker_start`; halt only for not-implemented
  categories.
- `apps/api/app/tests/test_domain_writer.py`, `test_real_dispatch.py` — real-path
  integration tests.
- `migration-platform/README.md`, `.env.example`, operational docs.

**Implementation:**

1. `authorize()`/lease/fencing integration before the phase and immediately
   before each write; re-read live destination state before the mutation.
2. Idempotent additive decision using B3a rules: equivalent existing domain →
   verified `already_present` no-op; different type/owner/label/docroot →
   fail-closed block; unsupported type → manual/not-supported.
3. Real create via B3a `DestinationWrite`; post-write fresh-read verification
   (type + domain + docroot must match). Ambiguous/temporary write → fresh-read
   before deciding; never auto-retry a non-idempotent create.
4. Redacted planned-call/result audit and compensation metadata sufficient for a
   future controlled manual removal (no destructive rollback in B3b).
5. Legal A2 transitions and checkpoint/attempt; a fenced-out or stale worker
   records no success. Re-validate safety gate and fencing before the phase,
   before the write, and before persisting the result.
6. Keep `DOMAIN_WRITER_MODE` and `REAL_EXECUTION_MODE` disabled by default.

**Testing Requirements (integration, deterministic fake transport):**

- [ ] Source endpoint rejected as target; master/domain writer switch disabled.
- [ ] Missing domain created and verified; equivalent present → idempotent no-op;
      present-but-different → block.
- [ ] Collision appeared after snapshot; ambiguous write + positive fresh-read →
      success; ambiguous write + negative fresh-read → failure without retry;
      post-write verification failure.
- [ ] Lease expired / stale fencing before the write; fencing lost after the
      write but before commit; confirmation/evidence went stale between dispatch
      and phase.
- [ ] Real actor runs a valid domains phase and does not halt; a run with
      not-implemented categories stays `halted` without writes.
- [ ] Retry/crash does not duplicate the domain; compensation metadata present
      and redacted; no secret in events/exceptions/audit; mock writer and dry-run
      do not regress.

**Acceptance Criteria:**

- [ ] Existing domain is idempotent; ambiguous collision blocks; source is never
      called with a writer; a fenced-out worker cannot record success.
- [ ] No new test, typecheck, Compose, or coverage regression.
- [ ] Real behavior remains disabled by default until both gates are explicitly
      enabled for an authorized environment.

**Risk & Rollback:** Main risk is an unintended destination mutation or false
verification. Keep both flags disabled, revert the PR/schema migration if needed,
and use only recorded compensation steps; never compensate by mutating the source.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```
