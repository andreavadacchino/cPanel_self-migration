# Task B2: Implement SSH adapter — SPLIT (retired)

| Field | Value |
|---|---|
| **ID** | `B2` (ritirato) |
| **Status** | `[/]` split — non completare con questo ID |
| **Priority** | High |
| **Size** | L |
| **Dependencies** | A5 |
| **Branch** | `feat/b2-implement-ssh-adapter` (non usare) |

> **Split.** L'implementazione completa di B2 è stata misurata a **~1100 righe di
> produzione + ~600 di test su 9+ file** (errors, contract tipizzato, command
> builder, host-key policy, client paramiko, streaming/backpressure, fake
> transport, ~25 test), oltre i guardrail 8 file / 500 righe e il limite di 400
> righe per file. Come previsto da questo stesso task ("misura prima di
> implementare; se necessario proponi lo split"), l'implementazione è stata
> fermata e B2 suddiviso in:
>
> - [`B2a` — SSH contract, host-key security, command execution](B2a-ssh-command-execution.md) (dep: A5)
> - [`B2b` — SSH streaming, cancellation, backpressure](B2b-ssh-streaming-backpressure.md) (dep: B2a)
>
> B2a è il minimo boundary coerente e testabile per l'esecuzione comandi
> verificata host-key (una divisione più fine produrrebbe PR intermedie non
> testabili). Le dipendenze downstream su trasferimento contenuti (C1/C2/C3)
> puntano a `B2b`; i writer basati su comandi (B5) possono partire da `B2a`. L'ID
> `B2` è ritirato e non riutilizzato. Il testo storico sottostante resta come
> riferimento.

---

**Goal:** Implement host-key-verified SSH commands and streaming with timeouts, cancellation, bounded output, and read/write target policy.

**Current State:** `SshClient.run` always raises `NotImplementedError`.

```text
packages/adapters/adapters/ssh/__init__.py
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

- [ ] changed host keys fail; cancellation closes streams; secrets never appear in commands/events.
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

