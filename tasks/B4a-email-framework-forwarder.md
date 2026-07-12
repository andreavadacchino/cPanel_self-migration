# Task B4a: Email writer framework + forwarder

| Field | Value |
|---|---|
| **ID** | `B4a` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B1, B3c-ii |
| **Branch** | `feat/b4a-email-framework-forwarder` |

**Origin:** first sub-task of the per-capability split of `B4` (see
[B4-email-config-writers.md](B4-email-config-writers.md)). B4a establishes the
shared, reusable **email real-writer framework** and proves it end-to-end with the
simplest additive category — **forwarders** — behind a disabled-by-default flag.

**Goal:** Provide a typed, effect-isolated per-item write engine (fresh-read →
decide → gated create → live verify → redacted compensation) that every email
category will reuse, plus the forwarder rules and real writer. A forwarder is
created **only** when the exact `source→destination` pair is missing on the
destination; a differing pair is never replaced; verification is a fresh list read.
Real behavior stays disabled by default and the engine is not wired into the
runtime dispatch here (that is B4e).

**Current State:** `forwarder_writer.py` verifies only in-memory mock state; there
is no shared real-writer framework and no forwarder rules module.

**Scope (apps/api/app/modules/executions + inventory, ≤8 files / ≤500 changed lines):**

- `email_write.py` (new) — shared framework: the effectful `EmailGateway` protocol
  seam, the per-item `decide → gated create → verify` scaffold, an aggregated
  `EmailPhaseResult`, redacted per-step evidence events, and redaction helpers. No
  category-specific logic.
- `forwarder_rules.py` (new) — pure decision for a forwarder item over live
  evidence: `missing` (auto-candidate) / `match` (verified no-op) /
  `different`/`unknown`/`unreadable` (block or manual, never overwrite). Composite
  key `source→destination`.
- `forwarder_writer.py` (extend) — real additive phase reusing the framework +
  rules; keep the existing mock path intact.
- `app/core/config.py` — the forwarder real flag already exists
  (`forwarder_writer_mode`); add a double-gate property, keep disabled by default.
- tests + `README.md`.

**Common safety requirements (inherited by every B4x writer):**

- `DestinationWrite` only; the source stays structurally read-only.
- `authorize()` + lease + fencing before the phase and immediately before each
  write (via the `before_write` hook the framework calls; runtime re-validation is
  wired in B4e).
- Live fresh-read of the destination per item; only unambiguous
  `missing_on_destination` is an automatic candidate.
- `match` → verified no-op; `different`/`only_on_destination`/`unknown`/partial/
  unreadable → block or manual, never overwrite; no implicit delete.
- Non-idempotent writes are never auto-retried; a timeout/ambiguous response is
  resolved by a fresh read, never a blind second write; live post-write verify.
- Redacted compensation metadata; no token/password/sensitive payload in
  events/errors/audit; real flags separated, exact-match, disabled by default.

**Testing Requirements (deterministic fake gateway, no real servers):**

- [x] Flag disabled → no write. Source target rejected structurally (destination-only gateway).
- [x] Missing pair → create + live verify; match → zero writes.
- [x] Different / unknown / partial / unreadable → zero writes (block/manual).
- [x] Race after snapshot (pair appeared) resolved by fresh-read; ambiguous
      write with positive and with negative fresh-read; post-write mismatch fails.
- [x] Fencing lost before the write (before_write hook); stale confirmation/evidence
      re-validation is the B4e wiring seam.
- [x] Retry does not duplicate; compensation metadata present and redacted.
- [x] No secret/sensitive value in events/errors/audit; mock/dry-run unaffected.
- [x] New safety-critical code has at least 90% line coverage (email_write 99%, forwarder_rules 100%).

**Acceptance Criteria:**

- [x] Only a missing exact pair is created and verified; a differing pair is never
      replaced; the source is never written; secrets never appear in audit.
- [x] No new test, typecheck, Compose, or coverage regression.
- [x] Real behavior remains disabled by default and unreachable from the runtime
      until B4e wires it under the double gate.

**Risk & Rollback:** Main risk is an unintended destination mutation or a false
verification. Keep the flag disabled, revert the module if needed, and never
compensate by mutating the source.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```

---

## Completion Record

**Data:** 2026-07-12

**Riepilogo implementazione.** Stabilito il framework condiviso per i writer email
reali e provato end-to-end con la categoria forwarder additiva, dietro flag
disabled-by-default e non cablato nel runtime (irraggiungibile fino a B4e).

- `email_write.py` (nuovo, framework): motore per-item `execute_email_phase`
  (fresh-read live → decisione di categoria → gated `create` → verifica live →
  compensation redatta). `create` è l'unico percorso a `DestinationWrite`, **mai
  ritentato**; esito ambiguo/timeout risolto da fresh-read, non da seconda scrittura
  cieca; `already_present`→no-op verificato; `blocked`→fail closed; `manual`→pending.
  `EmailGateway` è solo-destinazione (nessuna primitiva di scrittura sorgente). Hook
  `before_write` come seam per la rivalidazione gate+fencing (B4e). Eventi di audit
  redatti (solo label sicura, mai token/password/body/regole).
- `forwarder_rules.py` (nuovo, puro): chiave composta `sorgente→destinazione`;
  missing→create, match→already_present, forma non esprimibile (pipe/programma/
  `:fail:`)→blocked, sorgente invalida→blocked, evidenza illeggibile/ambigua→manual.
- `forwarder_writer.py` (esteso): `run_forwarder_phase` risolve gli item dai passi
  preview e invoca il motore con le regole forwarder; path mock preesistente intatto.
- `config.py`: property double-gate `forwarder_real_writer_enabled` + validator
  fail-closed su `FORWARDER_WRITER_MODE` (rifiuta valori ignoti allo startup).

**File principali.** `apps/api/app/modules/executions/email_write.py` (nuovo),
`forwarder_rules.py` (nuovo), `forwarder_writer.py` (esteso), `app/core/config.py`,
`app/tests/test_real_forwarder_writer.py` (nuovo, 18 test). Doc: `README.md`
(sezione framework B4a), `.env.example` (nota `FORWARDER_WRITER_MODE`). Task: `B4`
marcato split, `B4a`–`B4e` creati, `BACKLOG.md` aggiornato (grafo, downstream
`C3→B4e`).

**Test e comandi eseguiti (esito).**
- Mirati B4a: `pytest app/tests/test_real_forwarder_writer.py` → **18 passed**;
  coverage `email_write.py` **99%**, `forwarder_rules.py` **100%** (branch).
- Intera suite API: **351 passed** (+18, nessuna regressione; mock/dry-run intatti).
- Worker (venv): **18 passed**. Web `npm run build`: **OK**. `docker compose config
  -q`: **OK**.

**Esito review adversariale.** Verificati e coperti: overwrite (forwarder additivo,
`create` solo su coppia esatta mancante; coppia con destinazione diversa dalla stessa
sorgente è additiva, l'esistente non è toccato), race anti-upsert (decisione sulla
fresh-read live per item, non sullo snapshot; comparsa post-snapshot→no-op; ambiguo→
fresh-read senza retry), false verification (verifica = fresh-read + re-decide
`already_present`; mismatch/negativo→failed), fencing (hook `before_write` che
solleva→nessuna scrittura; rivalidazione runtime e commit atomico demandati a B4e),
secret leakage (nessun token/segreto in eventi/compensation; solo label). catch-all e
filtro differente sono di competenza di B4b/B4d. Nessun delete implicito; source
strutturalmente read-only (gateway solo-destinazione).

**Documentazione aggiornata.** `README.md` (framework email B4a + forwarder),
`.env.example` (`FORWARDER_WRITER_MODE`).

**Limitazioni residue (per B4b–B4e).** Default-address (overwrite+backup), routing MX,
filtri (UPSERT), autoresponder (UPSERT, body redatto) e l'integrazione dispatch runtime
(registrazione categorie in `IMPLEMENTED_REAL_CATEGORIES`, rivalidazione gate/fencing
per write, commit atomico) — il forwarder reale resta non cablato finché B4e non lo
collega dietro il doppio gate.
