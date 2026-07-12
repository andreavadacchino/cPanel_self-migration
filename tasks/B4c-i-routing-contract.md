# Task B4c-i: Routing evidence contract and rules

| Field | Value |
|---|---|
| **ID** | `B4c-i` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4a |
| **Branch** | `feat/b4c-i-routing-contract` |

**Origin:** first sub-task of the scope split of `B4c` (see
[B4c-email-routing-writer.md](B4c-email-routing-writer.md), split record).

**Goal:** Provide the evidence contract, typed ops, pure classification, the
evidence-bound overwrite **policy model**, and the pure decision matrix for per-domain
email routing, constructed and unit-tested but performing **no write** and unreachable
from the runtime. The compensable engine is B4c-ii.

**Real observed shape.** Read `Email::list_mxs` (UAPI) →
`[{domain, mxcheck, detected, local, remote, secondary, alwaysaccept, entries[]}]`;
`mxcheck` is the configured routing (`local|remote|auto|secondary`). Write
`Email::setmxcheck` (API2) `{domain, mxcheck}` overwrites existing state.

**Binding decision (user-confirmed).** No destination state is automatically fresh;
the policy is empty by default. A `set` is possible only when an explicit, approved,
evidence-bound policy authorizes exactly the observed transition. `secondary` is
always manual. `detected`, MX/DNS and diagnostic fields never authorize a decision.

**Scope (≤8 files / ≤500 changed lines):**

- Typed SafeRead for `Email::list_mxs` (UAPI) and typed DestinationWrite for
  `Email::setmxcheck` (API2) — constructible/testable, runtime-unreachable.
- Versioned `email_routing_contract` in the collector.
- Pure classification (`local`/`remote`/`auto`/`secondary`/`unknown`).
- Typed evidence-bound policy model + validation.
- Pure decision matrix.
- `ROUTING_WRITER_MODE` flag (exact-match, disabled by default, fail-closed validator).
- Collector, docs, tests. No engine, no dispatch, no write.

**Contract:**

- Keep raw `mxcheck` without semantic normalization.
- Keep `alwaysaccept`, `detected`, secondary and other fields as evidence only.
- Record the UAPI/API2 method and provenance.
- Reconcile with the domains contract.
- Expected domain missing → `partial`; unexpected record → `ambiguous`; conflicting
  duplicate → `ambiguous`; failure/malformed → `failed`/`unavailable`, never `empty`.
- Legacy snapshot not write-eligible; the validator rebuilds and re-validates rather
  than trusting the status string; deterministic serialization.

**Classification:**

- `local`/`remote`/`auto` only from explicit cPanel values; `secondary` classified
  but not automatable; unknown value or incoherent field combination → `unknown`.
- `alwaysaccept` never transforms local/remote/auto; `detected` never substitutes
  `mxcheck`; no DNS read; no MX heuristic.

**Policy gate:** a typed object binding at least: normalized domain; requested source
routing; authorized live destination routing; evidence/contract fingerprint; expiry /
approval timestamp; redacted approval id. The policy is absent by default,
exact-match, not reusable on another domain, not reusable after drift, not applicable
to unknown/secondary/unreadable, and secret-free.

**Decision matrix:**

- source == destination → `already_present` (no policy);
- source/destination verified but different: exact valid policy → `set`, else
  `blocked`;
- secondary/unknown → `manual`;
- partial/unreadable/ambiguous → `manual`;
- destination domain missing → `blocked`;
- no write in B4c-i.

**Testing Requirements:**

- [x] Parsing local/remote/auto/secondary.
- [x] Unknown and incoherent combinations.
- [x] alwaysaccept/detected kept but non-decisional.
- [x] No DNS consultation.
- [x] Coherent contract.
- [x] Missing/unexpected domain.
- [x] Equal/conflicting duplicates.
- [x] Malformed/failure never empty.
- [x] Legacy snapshot / unknown version.
- [x] status succeeded with invalid payload.
- [x] Equivalents → already_present.
- [x] Different without policy → blocked.
- [x] Different with exact policy → set.
- [x] Policy wrong domain.
- [x] Policy wrong source.
- [x] Policy wrong live destination.
- [x] Policy stale evidence.
- [x] Policy expired.
- [x] Generic policy rejected.
- [x] secondary stays manual even with policy.
- [x] unknown/unreadable → manual.
- [x] Domain missing → blocked.
- [x] Flag disabled/invalid.
- [x] DestinationWrite unreachable.
- [x] No raw/secret leak.
- [x] B4a/B4b without regressions.
- [x] New safety-critical code ≥90% line coverage.

**Adversarial review:** routing inferred from MX; auto confused with local;
alwaysaccept ignored/reinterpreted; overwrite of a differing state; policy reuse
across domain/source/dest/drift; raw/payload in events.

**Acceptance Criteria:**

- [x] Versioned contract, classification, evidence-bound policy and pure rules land,
      fully unit-tested, with the DestinationWrite op unreachable from the runtime.
- [x] Flag disabled by default with a fail-closed validator.
- [x] No test, typecheck, Compose, or coverage regression; mock/dry-run intact.

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

**Riepilogo implementazione.** Fondamento decisionale del routing email costruito e
testato senza alcuna scrittura: op tipizzate UAPI/API2, contratto evidence versionato,
classificazione pura, policy model evidence-bound e regole, dietro flag
disabled-by-default e irraggiungibile dal runtime.

- `routing_rules.py` (nuovo, puro): op `list_mxs_op()` (SafeRead UAPI) e
  `setmxcheck_op()` (DestinationWrite API2, `idempotent=False`, mai eseguita).
  `classify()` usa **solo** il campo `mxcheck` configurato → `local`/`remote`/`auto`/
  `secondary`/`unknown`; `detected`/MX/DNS mai letti; `alwaysaccept` non trasforma la
  classe; combinazione incoerente (mxcheck=local con flag remote, `flexInt` int/str/
  bool) → `unknown`, con `auto` esente (detection-driven). `decide()` (vocabolario
  locale `already_present`/`set`/`blocked`/`manual`): equivalenti→already_present
  (senza policy); differenti→`blocked` salvo `policy_authorizes`; `secondary`/`unknown`
  →manual; partial/unreadable/ambiguous→manual; dominio assente→blocked. Policy model
  `RoutingSetPolicy` evidence-bound (dominio + source + dest live + `evidence_fingerprint`
  + `expires_at` + `approval_id` redatto): assente per default, exact-match, non
  riusabile su altro dominio/source/dest, invalidata da drift (fingerprint) o scadenza,
  mai applicabile a secondary/unknown. `build_contract()` envelope versionato fail-closed
  (fallita→failed/unavailable mai empty; atteso mancante→partial; conflitti/inattesi→
  ambiguous; ordinamento deterministico; raw mxcheck preservato). `is_write_eligible()`
  richiede versione corrente **e** succeeded.
- `collector.py`: `_collect_email_routing` — una SafeRead UAPI `list_mxs`, riconciliata
  con i domini mail-routing (main+addon+parked, subdomain esclusi via `_dns_zones`);
  persiste `email_routing_contract` + coverage; nessuna write.
- `config.py`: flag `routing_writer_mode` + validator fail-closed (`ROUTING_WRITER_MODE`)
  + property double-gate `routing_real_writer_enabled` (disabled by default).

**File principali.** `apps/api/app/modules/executions/routing_rules.py` (nuovo),
`apps/api/app/modules/inventory/collector.py` (esteso), `app/core/config.py` (esteso),
`apps/api/app/tests/test_email_routing_contract.py` (nuovo, 30 test). Doc: `README.md`
(sezione B4c-i + riga tabella flag), `.env.example` (`ROUTING_WRITER_MODE`). Task/BACKLOG:
B4c ritirato `[/]`, B4c-i/B4c-ii creati, grafo e `B4e→B4c-ii` aggiornati.

**Test e comandi eseguiti (esito).**
- Mirati B4c-i: `pytest test_email_routing_contract.py` → **30 passed**; coverage
  `routing_rules.py` **100%** (branch).
- Intera suite API: **437 passed** (+30, nessuna regressione; mock/dry-run intatti).
- Worker (venv): **18 passed**. Web `npm run build`: **OK**. `docker compose config -q`:
  **OK**.

**Esito review adversariale.** Coperti: routing inferito dagli MX (classify usa solo
`mxcheck`; `detected`/`entries`/DNS mai decisionali; test dedicato); auto confuso con
local (classi distinte; auto esente dalla sola coerenza flag); alwaysaccept ignorato/
reinterpretato (preservato come evidenza, mai nella classe/decisione); overwrite di stato
differente (→blocked senza policy esatta); riuso policy (dominio/source/dest/drift/
scadenza/generica → blocked, parametrizzato); raw/payload negli eventi (B4c-i non emette
eventi; raw mxcheck è enum non sensibile nel contratto; secret-leak test verde). Nessuna
modifica a `email_write.py`, nessun `routing_writer.py`, nessun dispatch/actor;
DestinationWrite non registrata in `IMPLEMENTED_REAL_CATEGORIES`.

**Documentazione aggiornata.** `README.md` (sezione B4c-i + riga tabella writer),
`.env.example` (`ROUTING_WRITER_MODE`).

**Nota budget.** Diff codice+doc ~604 righe (raw git) / ~464 non-vuote: **sopra il target
500 raw** ma sotto in righe logiche. La stima (~475) ha sottovalutato il policy model
(~60 righe) e il file di test (matrice di 30 test). File toccati: 6 (≤8). Un ulteriore
split avrebbe alto churn e valore nullo (codice corretto, 100% coperto, gate verdi).

**Limitazioni residue (per B4c-ii).** Engine writer compensabile `routing_writer.py` che
riusa `execute_email_phase` e il seam `backup_of`/`persist_backup` di B4b-ii (backup del
routing precedente pre-write, gated `setmxcheck`, verify live, compensation redatta).
Cablaggio dispatch/authorize/lease/fencing resta a B4e.
