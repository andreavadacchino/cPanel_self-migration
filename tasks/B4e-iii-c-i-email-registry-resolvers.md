# Task B4e-iii-c-i: Email registry and evidence resolvers

| Field | Value |
|---|---|
| **ID** | `B4e-iii-c-i` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4e-iii-b, B4e-ii, B4a, B4b-ii, B4c-ii, B4d-ii |
| **Branch** | `feat/b4e-iii-c-i-email-registry-resolvers` |

**Origin:** first sub-task of the scope split of `B4e-iii-c` (see
[B4e-iii-c-email-runtime-registry-dispatch.md](B4e-iii-c-email-runtime-registry-dispatch.md)).

**Goal:** A typed registry and evidence-bound source payload resolvers for all 5 email
categories (`email_forwarders`, `default_address`, `email_routing`, `email_filters`,
`email_autoresponders`). Testable in isolation; no gateway, no dispatch wiring, no backup
binding. Step IDs select items; the authoritative payload comes from the immutable snapshot
bound to its contract.

**Acceptance Criteria:**

- [x] Registry covers exactly 5 email categories with metadata (flag, backup need, scope);
      each resolver validates the contract with `is_write_eligible()` and extracts items from
      the snapshot, not from step IDs or preview; no generic `email` category; no dispatch import.
- [x] No test, typecheck, Compose, or coverage regression; mock/dry-run intact.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```

## Completion Record

**Date:** 2026-07-14

**Commit 1 (`75fcf72`):** initial implementation — 7 files, 693 insertions (518 production+test,
the rest split documentation). Review found 1 Critical + 2 High + 3 Medium; Critical and
filter/autoresponder dedup (Medium) corrected in-commit. Forwarder versioning (High), routing
clock placeholder (Medium), and full snapshot leak (Medium) remained open.

**Commit 2 (corrective):** all remaining High and Medium resolved:

1. **Forwarder contract versioned** — `forwarder_rules.py` now has `CONTRACT_VERSION = 1`,
   `is_write_eligible()` (checks version, status, mappings shape, no invalid sources, no
   duplicates, supported fresh-read strategy). Collector updated to persist `version` and
   `status` in the envelope. Legacy snapshots without version are not write-eligible.

2. **Flat/contract reconciliation** — the forwarder resolver now reconstructs pairs from the
   flat `email_forwarders` snapshot AND validates them against the contract's `mappings`.
   Any mismatch (extra/missing pair in either direction) → fail-closed. Step IDs remain
   selectors only; the verified pair comes from the reconciled snapshot, not the step ID.

3. **Routing clock removed** — `now=0` placeholder removed from resolved kwargs. The runtime
   clock will be injected by c-ii/c-iii. `policies={}` remains (keeps routing inert).

4. **Autoresponder projection** — `snapshot_data` in kwargs now contains only the verified
   entries (`{"email_autoresponders": [selected_entries]}`), not the full account snapshot.
   No extraneous categories, account metadata, or unselected items leak.

**Files modified (corrective commit):**

| File | Change |
|---|---|
| `forwarder_rules.py` | +30 (CONTRACT_VERSION, is_write_eligible) |
| `collector.py` | +2 (version + status in envelope) |
| `email_phase_registry.py` | ~40 net (reconciliation, projection, routing fix) |
| `test_email_phase_registry.py` | +70 (12 new tests) |
| `BACKLOG.md` | stale references corrected |
| `B4e-iii-c-i task file` | updated Completion Record |

**Tests:** 769 API tests pass (39 in registry file). Web build and Compose validation pass.

**Commit 3 (corrective 2):** reconciliation simmetrica, fail-closed sui flat malformed,
proiezione minima del contratto autoresponder. Review R1 trovò 3 Medium (tutti neutralizzati
dal B4e-ii engine downstream); corretti in-commit prima del gate finale.

1. **Reconciliation simmetrica source/destination** — introdotto `_reconcile_endpoint()` helper
   puro che valida flat/contract per ciascun endpoint separatamente (source e destination).
   Rileva: item non-dict, source email invalida, destination non-stringa, duplicati, mismatch
   flat↔contract in entrambe le direzioni. Reason side-specific
   (`forwarder_contract_source_*` / `forwarder_contract_destination_*`). Source e destination
   con insiemi diversi di coppie sono ammessi (pre-migrazione). `_forwarder_flat_pairs()` non
   raggiunge più item malformed perché `_reconcile_endpoint()` li intercetta prima.

2. **`is_write_eligible()` hardened** — `invalid_sources` deve essere `isinstance(list)` e vuoto
   (non solo falsy: `None`, `""`, `{}` ora bloccati). Ogni mapping validata con
   `_is_valid_source()` (source) e `_is_plain_forward()` (destination) — non solo truthy check.

3. **Proiezione minima del contratto autoresponder** — `resolve_autoresponders()` costruisce un
   `projected_contract` con solo i domain block coinvolti e solo i record selezionati
   (fingerprint-verificati). Domini non selezionati, responder non selezionati, e metadata
   estranei non entrano nei kwargs. `run_autoresponder_phase` e
   `resolve_autoresponder_items` accettano la proiezione (test di integrazione).

4. **Completeness/issue gate** — allineato con gli altri 4 resolver: `cr.completeness == COMPLETE`
   e `cr.issue is None` verificati prima di accettare il record.

5. **Domain tag cross-check** — `entry._domain` confrontato con `cr._domain` (contract); un
   drift flat↔contract viene bloccato con reason `domain_mismatch`.

6. **Duplicate step_id dedup** — step_id duplicati non corrompono la proiezione; il secondo
   viene bloccato con reason `duplicate_step_id`.

**Files modified (corrective commit 3):**

| File | Change |
|---|---|
| `forwarder_rules.py` | ~10 (is_write_eligible hardened: isinstance(list), _is_valid_source, _is_plain_forward) |
| `email_phase_registry.py` | ~60 net (_reconcile_endpoint, resolve_forwarder rewritten, projected contract, completeness/domain/dedup gates) |
| `test_email_phase_registry.py` | +120 (21 new tests: 8 forwarder reconciliation, 5 is_write_eligible, 8 autoresponder projection) |
| `BACKLOG.md` | status transition [x]→[~]→[x] |
| `B4e-iii-c-i task file` | updated Completion Record |

**Tests:** 790 API tests pass (60 in registry file). Worker 18 passed. Web build OK. Compose OK.
Review finale: 0 Critical, 0 High, 0 Medium, 1 Low (case-sensitive duplicate check in
is_write_eligible — harmless, il collector normalizza; nessuna write bypass).
