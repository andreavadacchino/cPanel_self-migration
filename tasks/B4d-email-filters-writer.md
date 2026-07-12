# Task B4d: Email filters writer

| Field | Value |
|---|---|
| **ID** | `B4d` |
| **Status** | `[/]` (retired — split into B4d-i / B4d-ii) |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4a |
| **Branch** | `feat/b4d-email-filters-writer` |

> **Split record (2026-07-12).** Misurato a **~1365 righe su ~7 file** (`filter_rules.py`
> ~300, `filter_writer.py` ~170, `config.py` ~15, `test_filter_rules.py` ~380,
> `test_real_filter_writer.py` ~450, README + `.env.example` ~50) — oltre ~2,7× il budget
> 500 righe/PR; il solo codice di produzione (~485) è già al limite senza test/doc. Nulla
> è riutilizzabile (nessuna op tipizzata Python filtri, nessun contratto versionato con
> fingerprint — il collector attuale è una lista piatta). Su conferma dell'utente,
> suddiviso al confine **evidence/rules → additive-only engine**:
>
> - [`B4d-i` — Filter evidence contract, fingerprint and rules](B4d-i-filter-contract.md)
>   (dep: B4a).
> - [`B4d-ii` — Additive-only filter writer engine](B4d-ii-filter-writer-engine.md)
>   (dep: B4d-i).
>
> `B4e` dipende ora da `B4d-ii`. L'ID `B4d` è ritirato e non riutilizzato.

**Origin:** per-capability split of `B4` (see
[B4-email-config-writers.md](B4-email-config-writers.md)). Builds on the B4a
framework. The filter evidence contract (`email_filters`) already exists in the
collector.

**Goal:** Implement the email filters writer. `Email::store_filter` **UPSERTS**, so
the writer must re-read live immediately before the write and treat a same-name
filter with different content as blocking. Filter order, name, and complete
rules/actions must be preserved; a destination-only filter is never deleted.

**Scope:** a `filter_rules.py` decision module, a `filter_writer.py` real phase
reusing the B4a framework, a new `filter_writer_mode` flag (disabled by default),
tests, and docs.

**Category behavior:**

- Preserve order, name, and the complete rules/actions payload.
- Same name + different content → **block** (no overwrite via the upsert).
- Never delete a destination-only filter (no implicit delete).
- Sensitive filter payloads (rules/addresses) excluded from the audit; verify by a
  content fingerprint, not by logging the rules.

**Testing Requirements, Acceptance Criteria, Risk & Rollback, Verification
Commands:** inherit the common B4a set (flag disabled, source rejected,
missing→create+verify, match→no-op, different/unknown/partial→block, race after
snapshot, ambiguous positive/negative, post-write mismatch, fencing lost
before/after, stale evidence, retry no-duplication, compensation metadata, full
redaction incl. filter payload, mock/dry-run intact, ≥90% coverage), specialized to
the upsert + order/name-preservation semantics above. Not wired into runtime
dispatch until B4e.
