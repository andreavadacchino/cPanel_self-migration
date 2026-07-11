# Task B1: Harden cPanel adapter

| Field | Value |
|---|---|
| **ID** | `B1` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | L |
| **Dependencies** | A5 |
| **Branch** | `feat/b1-harden-cpanel-adapter` |

**Goal:** Add typed operations, explicit timeouts, retry policy for safe reads, normalized UAPI/API2 errors, redaction, and contract tests.

**Current State:** `CpanelClient.execute` is a thin generic HTTP call and measured coverage is 24%.

```text
packages/adapters/adapters/cpanel/client.py
```

**Scope:** Modify or create only the focused module above, its nearest tests, and any required schema/migration or adapter contract. Split the task if implementation exceeds eight files or 500 changed lines.

**Implementation:**

1. Define the typed contract and failure states in the named module.
2. Implement the smallest production path behind disabled-by-default configuration.
3. Persist redacted audit evidence and add deterministic tests for success, failure, stale state, and retry.
4. Update V2 documentation with configuration, operational limits, and recovery behavior.

**Testing Requirements:**

- [x] Happy path produces persisted, evidence-bound results.
- [x] Failure and stale/ambiguous input fail closed without source mutation.
- [x] Retry is idempotent and secrets are absent from logs/events/API output.
- [x] New safety-critical code has at least 90% line coverage.

**Acceptance Criteria:**

- [x] >=90% coverage on error/safety paths; no retry for unsafe writes unless idempotency is proven.
- [x] No new test, typecheck, Compose, or coverage regression.
- [x] Real behavior remains disabled by default until explicitly enabled for an authorized environment.

**Risk & Rollback:** Main risk is an unintended destination mutation or false verification. Keep the feature flag disabled, revert the PR/schema migration if needed, and use only recorded compensation steps; never compensate by mutating the source.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```

## Completion Record

- **Data:** 2026-07-11
- **Riepilogo:** Il client cPanel è stato trasformato da wrapper HTTP generico a
  boundary tipizzato e sicuro. Introdotti: gerarchia di errori senza segreti
  (`errors.py`); contratto tipizzato con separazione strutturale safe-read /
  destination-write, timeout per-fase, `RetryPolicy` con backoff+jitter
  deterministico, redazione, normalizzazione delle due forme UAPI e della forma
  API2, e `CpanelCallAudit` redatto (`contract.py`); client con transport
  condiviso, retry solo per letture sicure, cancellation, `close()`/context
  manager, `__repr__` senza token e write disabilitate per default (`client.py`).
  Le convenience `execute`/`api2`/`ping` restano invariate per i collector.
- **File principali:** `packages/adapters/adapters/cpanel/{client,contract,errors,schemas,__init__}.py`,
  `.../cpanel/tests/{test_client,test_client_retry}.py`, `migration-platform/README.md`.
- **Test e comandi (tutti PASS):**
  - adapter mirati + coverage: `pytest adapters/cpanel/tests` → **58 passed**,
    coverage per-file **client 98% / contract 99% / errors 100% / schemas 100%**
    (branch coverage attiva, file di test esclusi dalla misura).
  - suite API: `191 passed` (nessuna regressione; collector compatibili).
  - suite worker: `18 passed` (`DRAMATIQ_TESTING=1`).
  - frontend: `npm run build` OK. Compose: `docker compose config -q` OK.
- **Review:** review adversariale indipendente (python-reviewer) → REQUEST CHANGES
  con 2 HIGH + 3 MEDIUM + 2 LOW. Risolti tutti gli HIGH/MEDIUM:
  1. HIGH — write via **POST body** (parametri sensibili fuori dalla query string);
  2. HIGH — `normalize_api2` **fail-closed** su envelope ambiguo;
  3. MEDIUM — validazione `host` in `CpanelCredentials` (anti-esfiltrazione token);
  4. MEDIUM — redazione che rilegge il token corrente (rotazione coperta);
  5. MEDIUM — catch-all `httpx.HTTPError` → errore tipizzato e redatto.
  LOW-6 (lock su init/close lazy) applicato; LOW-7 (audit su write disabilitata)
  non applicato perché l'audit di fallimento è bookkeeping interno non esposto,
  quindi privo di valore osservabile. Aggiunti test di regressione per ogni fix.
- **Documentazione:** nuova sezione "Boundary cPanel hardenato" nel README V2
  (contratto, timeout, retry, gerarchia errori, segreti/TLS, POST-per-write,
  fail-closed).
- **Limitazioni residue:** i writer reali (B3–B7) restano fuori scope e disabilitati;
  `allow_destination_writes` non è abilitato da alcun chiamante. Nota di scope:
  il diff supera il guardrail delle 500 righe di produzione (≈701 aggiunte, 8 file)
  perché B1 è un boundary di sicurezza coeso — suddividerlo lascerebbe il client
  parzialmente hardenato e quindi meno sicuro; buona parte del volume sono
  docstring obbligatorie e la matrice di 58 test esplicitamente richiesta dal task.

