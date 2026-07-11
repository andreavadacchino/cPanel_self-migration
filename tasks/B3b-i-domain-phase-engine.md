# Task B3b-i: Real domain write phase engine

| Field | Value |
|---|---|
| **ID** | `B3b-i` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B3a |
| **Branch** | `feat/b3b-i-domain-phase-engine` |

**Origin:** first half of the split of `B3b` (see
[B3b-real-domain-writer-dispatch.md](B3b-real-domain-writer-dispatch.md)). B3b-i
delivers only the phase engine; the dispatch/actor wiring, flags, and
lease/fencing/authorize integration are the separate task
[`B3b-ii`](B3b-ii-domain-phase-dispatch.md).

**Goal:** A `real_domain_writer.py` phase engine, driven by an injected gateway,
that consumes the B3a adapter operations and pure rules to perform the additive
domains phase (resolve → fresh-read → decide → single create → verify), returns a
typed phase result with redacted compensation, and is **unreachable from the
runtime**.

**Scope:**

- `apps/api/app/modules/executions/real_domain_writer.py` — the phase engine.
- `apps/api/app/tests/test_real_domain_writer.py` — deterministic unit tests with
  a fake gateway.

**Implementation:**

1. `resolve_requested(...)` maps each `domains:<name>` step to a typed
   `RequestedDomain` (or `None` = manual) from the source evidence, reusing the
   B3a docroot rebasing shape.
2. `execute_domain_phase(run, requested_by_step, gateway, home, *, before_write)`
   runs each step: fresh-read → `decide_additive` → `already_present` (verified
   no-op) / `blocked` (fail-closed, zero write) / `unsupported` (manual) /
   `create`. `create` is the only path to a `DestinationWrite`, never
   auto-retried; an ambiguous/timeout outcome is resolved by a fresh read, not a
   second create. Post-write verification re-reads and trusts only an equivalent
   live record. Redacted per-step audit events and a typed `PhaseResult`.
3. `before_write` is an optional neutral hook the wiring (B3b-ii) supplies; the
   engine itself performs no authorize/lease/fencing and touches no DB session or
   state machine.

**Absolute constraints:**

- Engine unreachable from router, dispatch, and worker.
- No change to `dispatch.py`, actors, router, or runtime config.
- No real domain mode enabled; no lease/fencing/authorize in the engine.
- No real cPanel call; no `ExecutionRun`/`ExecutionAttempt` transition.
- No change to email, DNS, database, SSH, cron, FTP, or content.

**Testing Requirements:**

- [x] Valid and invalid (unresolved) requested step.
- [x] `already_present` → zero write; `blocked` → zero write; `unsupported` →
      zero write.
- [x] `create` → exactly one write and positive verification.
- [x] Ambiguous write + positive fresh-read → success, no second create.
- [x] Ambiguous write + negative fresh-read → failure.
- [x] Post-write mismatch → not verified.
- [x] Collision that appeared after the snapshot.
- [x] Result and compensation redacted; exceptions carry no secret.
- [x] The fake gateway proves no source is used.
- [x] B3a rules / B1 adapter compatibility; engine coverage ≥90%.

**Acceptance Criteria:**

- [x] Typed phase engine complete, gateway-injected, runtime-unreachable.
- [x] Single non-retried create; ambiguous handled by fresh-read; fail-closed on
      blocked/unsupported; no secret leak.
- [x] No API/worker/frontend/Compose regression.

## Completion Record

- **Data:** 2026-07-11
- **Riepilogo:** Prima metà dello split di B3b. Implementato il motore di fase
  `real_domain_writer.py`: `resolve_requested` (mapping puro source→`RequestedDomain`
  con rebasing docroot e `None` per main/unknown/foreign-home) ed
  `execute_domain_phase` (per-step: fresh-read → `decide_additive` di B3a →
  `already_present`/`blocked`/`unsupported`/`create`). La create è l'unico percorso
  verso una `DestinationWrite`, **mai ritentata**; un esito ambiguo/timeout è
  risolto da una fresh-read (mai una seconda create); verifica post-write per
  equivalenza (dominio+tipo+docroot) riusando `decide_additive → already_present`.
  Audit redatto per step, `PhaseResult` tipizzato, `compensation` redatto. Il
  motore è **DB-free e privo di authorize/lease/fencing/transizioni** (spostati in
  B3b-ii); unico seam è `before_write`. Nessun import runtime lo raggiunge.
- **File principali:** `apps/api/app/modules/executions/real_domain_writer.py`
  (227) + `apps/api/app/tests/test_real_domain_writer.py` (239). 2 file, 466 righe
  (< 500). Split documentato in `BACKLOG.md`, `B3b-real-domain-writer-dispatch.md`,
  `B3b-ii-domain-phase-dispatch.md`. Il wiring drafted è salvato come patch locale
  (`b3b-ii-wiring.patch`) per B3b-ii.
- **Test e comandi (tutti PASS):** engine `pytest test_real_domain_writer.py`
  **20 passed**, coverage **99%** (branch attiva); API **255 passed**; adapter
  **81 passed**; worker **18 passed**; `npm run build` OK; `docker compose config -q` OK.
  Verificato via grep che nessun file runtime (dispatch/router/actor/config)
  importa il motore.
- **Review:** review adversariale indipendente (python-reviewer) → REQUEST CHANGES
  con 1 Critical + 1 Medium + 3 Low. Risolti Critical+Medium con test:
  1. Critical — `_do_create`/`_planned` ora usano `decision.normalized_name` e
     `decision.normalized_docroot` (non i valori grezzi): una richiesta IDN/case/
     trailing-dot scrive e verifica il valore canonico (già corretto in autonomia
     prima della review; test `test_create_uses_normalized_name_and_docroot`).
  2. Medium — `planned` calcolato **una sola volta prima della write** e `_planned`
     reso non-sollevante (degrada a descrittore minimale): il logging di audit non
     è più un punto di crash post-mutazione.
  I 3 Low sono comportamenti intenzionali (applicazione parziale per-step demandata
  a B3b-ii; cattura di `CpanelError` coerente con la garanzia B1; il caso non-
  canonico è ora coperto).
- **Documentazione:** nessuna modifica a README/`.env.example` (i flag operativi e
  la doc del writer reale appartengono a B3b-ii, dove il motore diventa
  raggiungibile).
- **Limitazioni residue → B3b-ii:** doppio gate flag, authorize/WriteTarget/lease/
  fencing, integrazione dispatch/actor, selezione stato terminale (solo-domini/
  misto/only-unimplemented), checkpoint/transizioni, crash/retry, fencing-perso-
  dopo-write e test d'integrazione. Vedi [B3b-ii](B3b-ii-domain-phase-dispatch.md).

**Risk & Rollback:** The engine performs writes only through an injected gateway
and is not wired into any runtime path, so it cannot mutate a real destination.
Revert the PR if needed; nothing to compensate.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```
