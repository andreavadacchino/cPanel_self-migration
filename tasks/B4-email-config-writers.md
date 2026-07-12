# Task B4: Real email configuration writers — SPLIT (retired)

| Field | Value |
|---|---|
| **ID** | `B4` (ritirato) |
| **Status** | `[/]` split — non completare con questo ID |
| **Priority** | High |
| **Size** | L |
| **Dependencies** | B1, B3c-ii |
| **Branch** | `feat/b4-email-config-writers` (non usare) |

> **Split.** L'implementazione completa di B4 è stata misurata a **~3200–3800 righe
> su ~25–30 file** (5 categorie con semantiche di sicurezza distinte: forwarder
> additivo/dedup, default-address che **sovrascrive** il catch-all, autoresponder e
> filtri **UPSERT**, routing MX; 3 categorie — routing, default-address, filtri —
> prive di evidence/flag), ~7× oltre i guardrail 8 file / 500 righe. Anche lo split a
> 3 suggerito dal task originale (B4a/B4b/B4c) resta ~1000–1530 righe per sotto-task.
> Su conferma esplicita dell'utente è stato suddiviso **per-capability** in 5
> sotto-task (ognuno ≈ una categoria testabile ≤~700 righe, dietro un flag reale
> exact-match disabled-by-default, non cablato finché il rispettivo wiring sicuro non
> è completo):
>
> - [`B4a` — Email writer framework + forwarder](B4a-email-framework-forwarder.md) (dep: B1, B3c-ii)
> - [`B4b` — Default address / catch-all writer](B4b-default-address-writer.md) (dep: B4a)
> - [`B4c` — Email routing writer](B4c-email-routing-writer.md) (dep: B4a)
> - [`B4d` — Email filters writer](B4d-email-filters-writer.md) (dep: B4a)
> - [`B4e` — Autoresponder writer + email dispatch integration](B4e-autoresponder-dispatch.md) (dep: B4a–B4d)
>
> `C3` (Mailbox content transfer) dipende ora da `B4e` (integrazione dispatch email
> finale). L'ID `B4` è ritirato e non riutilizzato. Il testo storico sottostante
> resta come riferimento.

---

**Goal:** Implement forwarder, autoresponder, routing, default-address, and filter writers with fresh reads, redacted payload handling, and verification.

**Current State:** Forwarder and autoresponder writers verify only in-memory mock state.

```text
apps/api/app/modules/executions/forwarder_writer.py
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

- [ ] missing items are created; different/existing items block or follow explicit policy; sensitive bodies stay out of audit.
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

