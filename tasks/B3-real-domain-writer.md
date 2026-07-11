# Task B3: Real domain writer — SPLIT (retired)

| Field | Value |
|---|---|
| **ID** | `B3` (ritirato) |
| **Status** | `[/]` split — non completare con questo ID |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B1 |
| **Branch** | `feat/b3-real-domain-writer` (non usare) |

> **Split.** L'implementazione completa di B3 come specificata (20 requisiti + 26
> test: operazioni adapter, regole di dominio pure, fase writer reale e wiring
> dispatch) supera in modo netto i guardrail di 8 file / 500 righe per PR. Come
> imposto dalla regola 2 del `TASK_EXECUTION_PROMPT.md`, B3 è stato suddiviso in
> due sotto-task documentati e tracciabili:
>
> - [`B3a` — Domain adapter and safety rules](B3a-domain-adapter-rules.md) (dep: B1)
> - [`B3b` — Real domain writer phase and dispatch wiring](B3b-real-domain-writer-dispatch.md) (dep: B3a)
>
> Le dipendenze downstream che puntavano a `B3` ora puntano a `B3b` (il writer
> reale effettivo): B4, B5, B6, B7 e C1. L'ID `B3` è ritirato e non riutilizzato.
> Il testo storico sottostante è conservato solo come riferimento.

---

**Goal:** Implement additive domain creation via a typed adapter with collision detection, fresh-read, post-write verification, and compensation metadata.

**Current State:** The domain writer explicitly refuses every non-mock destination.

```text
apps/api/app/modules/executions/domain_writer.py
```

**Scope:** Modify or create only the focused module above, its nearest tests, and any required schema/migration or adapter contract. Split the task if implementation exceeds eight files or 500 changed lines.

**Implementation:**

1. Define the typed contract and failure states in the named module.
2. Implement the smallest production path behind disabled-by-default configuration.
3. Persist redacted audit evidence and add deterministic tests for success, failure, stale state, and retry.
4. Update V2 documentation with configuration, operational limits, and recovery behavior.

**Testing Requirements:**

- [ ] Happy path produces persisted, evidence-bound results.
- [ ] Failure and stale/ambiguous input fail closed without source mutation.
- [ ] Retry is idempotent and secrets are absent from logs/events/API output.
- [ ] New safety-critical code has at least 90% line coverage.

**Acceptance Criteria:**

- [ ] existing domain is idempotent; ambiguous collision blocks; source is never called with a writer.
- [ ] No new test, typecheck, Compose, or coverage regression.
- [ ] Real behavior remains disabled by default until explicitly enabled for an authorized environment.

**Risk & Rollback:** Main risk is an unintended destination mutation or false verification. Keep the feature flag disabled, revert the PR/schema migration if needed, and use only recorded compensation steps; never compensate by mutating the source.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```

