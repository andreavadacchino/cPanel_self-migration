# Task B3a: Domain adapter and safety rules

| Field | Value |
|---|---|
| **ID** | `B3a` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B1 |
| **Branch** | `feat/b3a-domain-adapter-rules` |

**Origin:** first half of the split of the original `B3` (see
[B3-real-domain-writer.md](B3-real-domain-writer.md)). B3a delivers the typed
domain adapter operations and the pure safety rules; the real writer phase and
its dispatch/actor wiring are the separate task
[`B3b`](B3b-real-domain-writer-dispatch.md).

**Goal:** Add typed cPanel domain operations (read domains/types/internal
labels/docroot; additive `DestinationWrite` create for addon/subdomain/alias when
account-level supported; re-read/verify) plus pure domain rules (normalization,
type classification, collision detection, docroot safety), all fail-closed and
fully unit-tested with a deterministic fake transport. No dispatch integration.

**Scope:**

- `packages/adapters/adapters/cpanel/domains.py` — typed domain read/create/verify
  operations built on B1's `SafeRead`/`DestinationWrite`.
- `packages/adapters/adapters/cpanel/tests/test_domains.py` — adapter unit tests.
- `apps/api/app/modules/executions/domain_rules.py` — pure normalization,
  classification, collision detection, docroot validation, and typed
  fresh-read/decision/result models.
- `apps/api/app/tests/test_domain_rules.py` — rules unit tests.
- `migration-platform/README.md`, `.env.example` — documentation only if a flag
  or operational limit is introduced.

**Implementation:**

1. Typed domain operations in the adapter package: list current domains with
   type/internal-label/docroot; create addon, subdomain, alias/parked only when
   account-level supported; re-read a single domain for verification. Reads use
   `SafeRead`; creates use `DestinationWrite` (unreachable from dispatch in B3a).
2. Pure rules: case/trailing-dot/IDNA normalization; domain-type classification;
   collision detection (domain, internal addon/subdomain label, owned
   main/alias, overlapping/unsafe docroot, collision appeared after snapshot);
   docroot validation against traversal, symlink escape, foreign home, unsafe
   overlap. Typed fresh-read, additive-decision, and result models.
3. Fail-closed on partial/ambiguous/unsupported input. No automatic retry of a
   create. Modern/legacy response parsing compatible with B1.
4. Deterministic unit tests with a fake transport; no real cPanel contact.

**Constraints:**

- Do **not** integrate the writer into dispatch; do **not** modify
  `worker_start` or the real actors.
- `DestinationWrite` create primitives may be implemented and tested with a fake
  transport but must stay unreachable from dispatch.
- All real feature flags remain disabled; no writes flow through the runtime.
- No changes to email, database, DNS, FTP, cron, SSH, or content.

**Testing Requirements:**

- [x] Domain/type/internal-label/docroot read parsing (modern + legacy shapes).
- [x] Additive create ops for addon, subdomain, alias produce the correct typed
      `DestinationWrite` and never retry automatically.
- [x] Normalization: case, trailing dot, IDNA.
- [x] Collision detection: domain, internal label, ownership, docroot, and a
      collision that appeared after the snapshot.
- [x] Docroot traversal, symlink escape, foreign home, unsafe overlap blocked.
- [x] Partial/ambiguous/unsupported input fails closed.
- [x] No secret in results/errors; new safety-critical code ≥90% line coverage.

**Acceptance Criteria:**

- [x] Typed adapter and pure rules complete; collisions and unsafe docroot
      blocked; no automatic create retry; B1-compatible parsing; no secret leak.
- [x] Adapter tests and rules unit tests green.
- [x] No API/worker/frontend/Compose regression.

## Completion Record

- **Data:** 2026-07-11
- **Riepilogo:** Prima metà dello split di B3. Aggiunte operazioni tipizzate per i
  domini nell'adapter cPanel sopra il boundary B1 (`read_domains`,
  `read_single_domain`, `build_create`) e un modulo di regole pure
  (`domain_rules.py`) per normalizzazione IDNA/case/trailing-dot, validazione
  docroot e decisione additiva `create`/`already_present`/`blocked`/`unsupported`.
  Nessun wiring nel dispatch: le `DestinationWrite` di create restano irraggiungibili
  dal runtime e le write reali impossibili (gate B1 disabilitato per default).
- **File principali:** `packages/adapters/adapters/cpanel/domains.py` (+ test),
  `apps/api/app/modules/executions/domain_rules.py` (+ test), `README.md`. Split
  documentato in `BACKLOG.md`, `B3-real-domain-writer.md`,
  `B3b-real-domain-writer-dispatch.md`.
- **Test e comandi (tutti PASS):** adapter `pytest` **81 passed**
  (domains.py 97%, branch coverage attiva); rules `test_domain_rules.py`
  **44 passed** (domain_rules.py 99%); API **235 passed**; worker **18 passed**;
  `npm run build` OK; `docker compose config -q` OK. Budget: 5 file, 461 righe di
  produzione (< 500), entro il guardrail.
- **Review:** review adversariale indipendente (python-reviewer) → REQUEST CHANGES
  con 1 Critical + 2 High + 2 Medium + 1 Low, **tutti risolti** con test di
  regressione:
  1. Critical — `read_single_domain` non fa più fallback a `addon` su tipo
     sconosciuto: fail-closed.
  2. High — `normalize_domain` usa una **whitelist LDH** per label dopo l'IDNA,
     non una blacklist di caratteri.
  3. High — addon/subdomain senza docroot ora **bloccati** (`missing_docroot`),
     così l'overlap check non è più saltabile.
  4. Medium — la shape legacy "main come stringa" valida il tipo di
     `main_documentroot`.
  5. Medium — `build_create` ha una guardia boundary di difesa in profondità
     (rifiuta dominio/docroot non sicuri anche bypassando le regole).
  6. Low — docroot `""` normalizzato a `None`.
  Corretto inoltre in autonomia un difetto di correttezza pre-review: l'overlap
  docroot non blocca più l'annidamento legittimo di un addon sotto il
  `public_html` del dominio principale.
- **Documentazione:** nuova sottosezione README "Operazioni domini e regole di
  sicurezza (B3a)".
- **Limitazioni residue → B3b:** fase writer reale, fresh-read/verify,
  authorize/lease/fencing, compensation, checkpoint/attempt e wiring
  dispatch/actor sono fuori scope e restano in [B3b](B3b-real-domain-writer-dispatch.md).

**Risk & Rollback:** The create primitives exist but are unreachable from the
runtime and gated behind B1's disabled-by-default write path, so B3a cannot
mutate a destination. Revert the PR if needed; there is nothing to compensate.

**Verification Commands:**

```bash
cd packages/adapters && python -m pytest adapters/cpanel/tests
cd ../../apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```
