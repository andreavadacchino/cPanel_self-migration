# Task B4d-i: Filter evidence contract, fingerprint and rules

| Field | Value |
|---|---|
| **ID** | `B4d-i` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4a |
| **Branch** | `feat/b4d-i-filter-contract` |

**Origin:** first sub-task of the scope split of `B4d` (see
[B4d-email-filters-writer.md](B4d-email-filters-writer.md), split record).

**Goal:** Provide the two-scope evidence contract, typed ops, a deterministic canonical
fingerprint over the *complete* filter payload, pure classification/completeness, and the
pure decision matrix for email filters — constructed and unit-tested but performing **no
write** and unreachable from the runtime. The additive-only engine is B4d-ii.

**Real observed shape.** Read `Email::list_filters` (UAPI) per scope (account-level =
`account=""`; mailbox = `account=local@domain`) → `[{filtername, enabled, rules, actions}]`.
Detail `Email::get_filter` (UAPI) → `{filtername, rules[], actions[]}`; each rule =
`{part, match, opt, val, number}` (`opt` observed null), each action =
`{action, dest, number}`. Write `Email::store_filter` (API2) **UPSERTS** existing state.

**Critical `get_filter` rule.** On a **non-existent** filter cPanel returns `status:1`
with a **TEMPLATE** (`filtername="Rule 1"`, one empty rule/action) — *not* an error.
Existence is therefore gated **only** on `list_filters`: `get_filter` is never an
existence check; the template is never a real filter; a detail name ≠ the enumerated name
is `ambiguous`; a template/empty/incoherent detail is `incomplete`/`ambiguous`, never a
valid filter; a detail failure makes the scope `partial`, never `empty`.

**Scope (≤8 files / ≤500 changed lines):**

- Typed SafeReads for `Email::list_filters` (per scope) and `Email::get_filter` (only after
  existence proven by the list); typed DestinationWrite for `Email::store_filter` (API2) —
  constructible/testable, runtime-unreachable. **No** `DeleteFilter` op exists.
- Versioned two-scope `email_filters` contract in the collector.
- Deterministic canonical fingerprint over the full ordered payload.
- Pure classification/completeness (`complete`/`incomplete`/`unsupported`).
- Pure decision matrix.
- `FILTER_WRITER_MODE` flag (exact-match, disabled by default, fail-closed validator).
- Collector, docs, tests. No engine, no dispatch, no write.

**Scope model:**

- Account-level represented explicitly, never with interchangeable values.
- Mailbox represented as a validated `local@domain`.
- The same name in different scopes is allowed and stays distinct.
- Duplicate scope+name: byte/semantically-equivalent complete detail → deterministic dedup
  only when provably safe; different → `ambiguous`.
- Mailbox scope not inventoried → issue.
- A mailbox detail failure never degrades the other scopes to `empty`.

**Canonical fingerprint:**

- Includes scope, name, filter position (when significant), and the complete rules/actions.
- Each rule includes exactly `part`, `match`, `opt` (incl. null), `val`, `number`.
- Each action includes exactly `action`, `dest`, `number`.
- Preserves rules order and actions order; no sorting.
- Distinguishes null, empty string, missing field, and zero.
- No normalization of regex, header, path, pipe, whitespace or quoting.
- Canonical serialization with an explicit schema/version tag; deterministic.
- Differs for any semantically-relevant change (reordering, differing condition/action).
- The complete payload stays in the protected contract, never in logs/audit/errors.

**Completeness/support:**

- A rule/action with missing fields → `incomplete`; an unknown type/operator →
  `unsupported`/manual (never silently dropped); an empty template → `incomplete`; a
  malformed payload → `failed`/`partial`.
- The collector keeps redacted issues; the validator never trusts the status string; a
  legacy snapshot is not write-eligible; a real zero-filter scope is distinct from a read
  failure.

**Decision matrix:**

- same scope+name+fingerprint → `already_present`;
- name live-absent and source complete/supported → `create`;
- same scope+name, different fingerprint → `blocked`;
- destination-only → preserve/no-op (never delete);
- source incomplete/unsupported → `manual`;
- destination scope partial/unreadable/ambiguous → `manual`;
- mailbox destination scope missing → `blocked`;
- no rename/reorder/replace/delete; no write in B4d-i.

**Testing Requirements:**

- [x] Account scope; mailbox scope; same name in different scopes.
- [x] List before get; get never called for a non-enumerated name.
- [x] Non-existent template rejected; detail name mismatch → ambiguous.
- [x] Detail failure → partial; mailbox failure never false-empty; real zero-filter scope.
- [x] Duplicate equivalent (dedup); duplicate conflicting (ambiguous).
- [x] Rules order preserved; actions order preserved.
- [x] Fingerprint changes when reversing rules; when reversing actions.
- [x] null vs missing; empty vs missing; zero vs "0" string distinguished.
- [x] Regex/whitespace/quoting preserved verbatim.
- [x] Incomplete payload; unknown operator; unknown action.
- [x] Legacy snapshot / unknown version; status succeeded with invalid payload.
- [x] Deterministic serialization.
- [x] same fingerprint → already_present; missing name → create; same name/different
      fingerprint → blocked.
- [x] Destination-only preserved; source unsupported → manual; destination partial →
      manual; mailbox missing → blocked.
- [x] StoreFilter unreachable; no DeleteFilter op exists.
- [x] Flag disabled/invalid; no raw/payload/secret leak.
- [x] Collector and B4a–B4c without regressions; ≥90% coverage on new safety-critical code.

**Adversarial review:** `get_filter` used as an existence check; template accepted;
account/mailbox scope confused; a fingerprint that sorts or drops fields; null/missing
collapsed; false-empty; same-name collision ignored; filter payload in logs; StoreFilter
reachable; a DeleteFilter accidentally present.

**Acceptance Criteria:**

- [x] Versioned two-scope contract, canonical fingerprint, classification and pure rules
      land, fully unit-tested, with the DestinationWrite op unreachable from the runtime.
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

**Riepilogo implementazione.** Fondamento decisionale dei filtri email costruito e testato
senza alcuna scrittura. `filter_rules.py` (nuovo, puro) fornisce: op tipizzate SafeRead
`list_filters_op(account)` (scope account = account assente; mailbox = `account=local@domain`)
e `get_filter_op(name, account)` (valida **solo** dopo che la lista ha provato l'esistenza),
DestinationWrite `store_filter_op` (API2, `idempotent=False`, mai eseguita, **nessuna**
`delete_filter_op`); un **canonical fingerprint** deterministico e order-preserving
(`canonical_filter` mantiene esattamente le chiavi presenti per ogni rule
`part`/`match`/`opt`/`val`/`number` e action `action`/`dest`/`number`, ordine di rules/actions
preservato, nessun sorting, nessuna normalizzazione; `json.dumps` con `sort_keys=False` +
sha256 → distingue null/empty/missing/zero; hash opaco senza raw); `classify_completeness`
(`complete`/`incomplete`/`unsupported`, operatore/azione sconosciuti tenuti mai scartati,
template vuoto → incomplete); `build_contract` (envelope versionato a due scope, fail-closed:
list failure → `failed`/`unavailable` mai `empty`, detail failure → `partial`, template/
name-mismatch → `ambiguous`, duplicato equivalente → dedup, conflittuale → `ambiguous`,
status complessivo = peggiore degli scope così che account `succeeded` non nasconde mailbox
`partial`); `decide` (matrice additive-only: same fingerprint → already_present, nome
live-assente + source supportata → create, stesso nome fingerprint diverso → blocked,
scope mailbox assente → blocked, source/destination non affidabili → manual, mai delete/
rename/reorder); `is_write_eligible` (versione corrente **e** tutti gli scope succeeded/empty,
mai fidandosi dello status string). Fatto critico gestito: `get_filter` su filtro inesistente
ritorna un TEMPLATE (`filtername="Rule 1"`) → l'esistenza è gateata **solo** su `list_filters`,
un nome del dettaglio ≠ nome enumerato è `ambiguous`.

**File principali.** `apps/api/app/modules/executions/filter_rules.py` (nuovo, 360 righe),
`apps/api/app/modules/inventory/collector.py` (+`_read_filter_scope`/`_collect_email_filters_contract`,
persiste `email_filters_contract` senza toccare il flat `email_filters`),
`apps/api/app/core/config.py` (flag `filter_writer_mode` + validator fail-closed +
property double-gate `filter_real_writer_enabled`),
`apps/api/app/tests/test_email_filter_contract.py` (nuovo, 56 test). Doc: `README.md`
(sezione B4d-i + riga tabella flag), `.env.example` (`FILTER_WRITER_MODE`). Task/BACKLOG:
B4d ritirato `[/]`, B4d-i/B4d-ii creati, grafo e `B4e→B4d-ii` aggiornati.

**Test e comandi eseguiti (esito).**
- Mirati B4d-i: `pytest test_email_filter_contract.py` → **56 passed**; coverage
  `filter_rules.py` **100%**; righe nuove del collector coperte.
- Intera suite API: **532 passed** (+56, nessuna regressione; mock/dry-run intatti).
- Worker (venv root): **18 passed**. Web `npm run build`: **OK**. `docker compose config -q`:
  **OK**.

**Esito review adversariale.** Coperti: `get_filter` come existence check (solo nomi
enumerati dalla lista; test `get_not_called_for_non_enumerated`, `read_filter_scope` con
duplicati/malformati); template accettato (name mismatch → incomplete/ambiguous); scope
account/mailbox confusi (scope è parte di identità/fingerprint; stesso nome scope diversi
distinto); fingerprint che ordina o perde campi (order-preserving, chiavi presenti, no
sorting; test reverse rules/actions e null/empty/missing/zero); false-empty (list failure →
failed/unavailable, detail → partial, mailbox non degrada gli altri scope); collisione
omonima (conflittuale → ambiguous, decide same-name-diff-fp → blocked); payload nei log
(B4d-i non emette eventi; fingerprint opaco; decide reason senza payload); StoreFilter
raggiungibile (non in `IMPLEMENTED_REAL_CATEGORIES`); DeleteFilter presente (asserito
assente). Nessuna modifica a `email_write.py`, dispatch, actor.

**Documentazione aggiornata.** `README.md` (sezione «Contratto evidence filtri email,
fingerprint e regole (B4d-i)» + riga tabella flag), `.env.example` (`FILTER_WRITER_MODE`).

**Nota budget.** Codice di produzione = `filter_rules.py` 360 + `config.py` ~21 +
`collector.py` ~54 = **~435 righe** (< 500). File di test 475 (matrice di 56 test
obbligatori). File toccati: 6 (≤8). Totale codice+doc oltre 500 raw ma allineato al
precedente B4c-i, con codice di produzione sotto budget.

**Limitazioni residue (per B4d-ii).** Engine writer additive-only `filter_writer.py` che
riusa `execute_email_phase`, gateway destination-only, fresh-read per scope,
**upsert-guard** immediatamente prima della `store_filter` (nome live-assente; nome comparso
tra snapshot e write → block; fresh-read inaffidabile → zero write), nessun `DeleteFilter`,
verify via fingerprint completo, compensation redatta. Cablaggio dispatch/authorize/lease/
fencing resta a B4e.
