# Task B4e: Autoresponder writer + email dispatch integration

| Field | Value |
|---|---|
| **ID** | `B4e` |
| **Status** | `[/]` (retired — split into B4e-i / B4e-ii / B4e-iii) |
| **Priority** | High |
| **Size** | L |
| **Dependencies** | B4a, B4b-ii, B4c-ii, B4d-ii |
| **Branch** | `feat/b4e-autoresponder-dispatch` |

> **Split record (2026-07-12).** Misurato a **~2465 righe su ~18 file** (~5× il budget 500).
> Analisi fattuale: `default_address`/`email_routing` non esistono come categoria in
> comparison/plan/preview/readiness (solo contratti evidence per-dominio); l'autoresponder
> è `MANUAL` (escluso dal preview); **nessuno store backup durevole** esiste (`persist_backup`
> è solo callback nei test) mentre default-address/routing lo richiedono (backup-or-nothing);
> interfacce engine non uniformi; l'actor A3 non riprende un attempt `running` (recovery = C4);
> nessuno stato `partial` (`halted` modella il successo parziale). Su conferma dell'utente,
> suddiviso al confine **contract/rules → engine → dispatch**:
>
> - [`B4e-i` — Autoresponder evidence contract and rules](B4e-i-autoresponder-contract.md) (dep: B4a).
> - [`B4e-ii` — Additive-only autoresponder writer engine](B4e-ii-autoresponder-writer-engine.md) (dep: B4e-i).
> - [`B4e-iii` — Email phases pipeline and dispatch integration](B4e-iii-email-dispatch-integration.md)
>   (dep: B4e-ii, B4a, B4b-ii, B4c-ii, B4d-ii) — aggregatore, ulteriore split previsto in
>   **iii-a** (durable backup store), **iii-b** (pipeline integration), **iii-c** (runtime registry
>   + dispatch), da formalizzare dopo B4e-ii.
>
> `C3` dipende ora da `B4e-iii`. L'ID `B4e` è ritirato e non riutilizzato per l'implementazione.

**Origin:** final sub-task of the per-capability split of `B4` (see
[B4-email-config-writers.md](B4-email-config-writers.md)). Adds the autoresponder
writer and wires every completed email category into the real runtime dispatch.

**Goal:** Implement the autoresponder writer and integrate the email categories
into `dispatch.py` so a run whose email steps are all verified actually executes,
while any unimplemented/manual step keeps the run `halted`/`failed` (never a false
`succeeded`). `Email::add_auto_responder` **UPSERTS**, so the writer needs a strict
anti-upsert fresh-read immediately before the write and a fingerprint-based verify.

**Scope:** `autoresponder_rules.py` + real `autoresponder_writer.py` (reusing the
B4a framework), `dispatch.py` integration (register only genuinely-completed email
categories in `IMPLEMENTED_REAL_CATEGORIES`, re-validate gate + fencing per write,
atomic run/attempt commit), the `autoresponder_writer_mode` double-gate property,
tests, and docs.

**Autoresponder behavior:**

- Requires the full source payload (subject/body/interval) but the audit stores a
  **redacted fingerprint**, never the body.
- Anti-upsert fresh-read immediately before the write; if the responder appeared
  after the snapshot → block; an equivalent responder already present → verified
  no-op; a different responder → block.
- Verify via fingerprint (not body in logs).

**Dispatch integration:**

- Register only categories with a completed, verified real writer; unimplemented
  categories stay pending/halted.
- No `succeeded` while any manual/unverified email step remains.
- Re-validate the safety gate and fencing before each write; atomic run/attempt
  commit.

**Testing Requirements, Acceptance Criteria, Risk & Rollback, Verification
Commands:** inherit the common B4a set (flag disabled, source rejected,
missing→create+verify, match→no-op, different/unknown/partial→block, race after
snapshot, ambiguous positive/negative, post-write mismatch, fencing lost
before/after, stale evidence, retry no-duplication, compensation metadata, full
redaction incl. autoresponder body, mock/dry-run intact, ≥90% coverage), plus
runtime-integration tests (real actor runs a valid email phase; a run with
unimplemented categories stays halted; no false success on mixed runs). Behind the
double gate `autoresponder_writer_mode` + `REAL_EXECUTION_MODE`, disabled by default.
