# Task B4e-i: Autoresponder evidence contract and rules

| Field | Value |
|---|---|
| **ID** | `B4e-i` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4a |
| **Branch** | `feat/b4e-i-autoresponder-contract` |

**Origin:** first sub-task of the scope split of `B4e` (see
[B4e-autoresponder-dispatch.md](B4e-autoresponder-dispatch.md), split record).

**Goal:** Provide the per-domain evidence contract, typed ops, a deterministic canonical
fingerprint over the *complete* autoresponder payload, pure classification/completeness,
and the pure additive decision matrix for email autoresponders — constructed and
unit-tested but performing **no write** and unreachable from the runtime. The additive-only
engine is B4e-ii; the dispatch integration is B4e-iii.

**Real observed shape.** Read `Email::list_auto_responders` (UAPI) **per domain** →
addresses; detail `Email::get_auto_responder` (UAPI) per address →
`{email, from, subject, body, interval, is_html, charset, start, stop}`. Write
`Email::add_auto_responder` (`domain`+`email` local part; `from/subject/body/is_html/
interval/charset`; `start`/`stop` omitted when 0) **UPSERTS** existing state.

**Critical existence rule.** Existence is proven **only** by `list_auto_responders`;
`get_auto_responder` is never an existence check; a detail for a non-enumerated address, a
detail address mismatch, or a template/default response is `ambiguous`/`incomplete` — never
a valid responder. A detail failure makes the domain/scope `partial`; a list failure is
`failed`/`unavailable`, never `empty`; a real zero-responder domain stays distinct from an
unreadable one.

**Scope (≤8 files / ≤500 changed lines):**

- Typed SafeReads `list_auto_responders` (per domain) and `get_auto_responder` (only after
  the list proves the address); typed DestinationWrite `add_auto_responder` — constructible/
  testable, runtime-unreachable. No overwrite/upsert/delete path.
- Versioned `autoresponder_contract` (per-domain scope) in the collector.
- Deterministic canonical fingerprint over the full payload.
- Pure classification/completeness (`complete`/`incomplete`/`unsupported`).
- Pure additive decision matrix.
- `AUTORESPONDER_WRITER_MODE` flag (exact-match, disabled by default, fail-closed validator)
  + double-gate property.
- Collector, docs, tests. No engine, no dispatch, no write.

**Contract (protected payload per responder):** full address, domain + local part, `from`,
`subject`, `body`, `interval`, `is_html`, `charset`, `start`, `stop`, any extra returned
fields, method/provenance, completeness and issue. Never invent missing defaults; distinguish
missing field / null / empty string / numeric zero / `"0"` string / boolean. The complete
payload stays in the protected contract; `from`/`subject`/`body` never enter logs/audit/
events/errors/repr.

**Canonical fingerprint:** includes address/scope, `from`, `subject`, `body`, `interval`,
HTML mode, `charset`, `start`/`stop`, and any supported field; versioned canonical
serialization; deterministic; no normalization of body/subject/whitespace/HTML/charset; any
single-field change → different fingerprint; opaque hash auditable without exposing content.

**Completeness/support:** incomplete payload → `incomplete`/manual; unsupported field/mode →
`unsupported`/manual; duplicate same address: equivalent → deterministic dedup, different →
`ambiguous`; expected domain with no list → `partial`; unexpected record → `ambiguous`;
legacy snapshot / unknown version → not eligible; validator rebuilds and never trusts the
status string.

**Decision matrix:**

- same address+fingerprint → `already_present`;
- address live-absent and source complete/supported → `create`;
- same address, different fingerprint → `blocked`;
- source incomplete/unsupported → `manual`;
- destination partial/ambiguous → `manual`;
- destination domain missing → `blocked`;
- no overwrite/upsert/delete; no write in B4e-i.

**Pipeline boundary:** B4e-i may mark the autoresponder evidence contract as design-ready in
its own coverage/readiness assessment, but must **not** make it dispatchable, must not change
preview selection, and must not move the category from `MANUAL` to `AUTO` — that belongs to
B4e-iii-b.

**Testing Requirements:**

- [x] List per domain; real zero-responder domain; list failure never empty.
- [x] Detail only after enumeration; detail failure → partial; address mismatch → ambiguous.
- [x] Template/incomplete detail rejected.
- [x] Duplicate equivalent (dedup); duplicate conflicting (ambiguous).
- [x] from/subject/body preserved (in the fingerprint); whitespace/HTML preserved verbatim.
- [x] interval 0 vs "0" vs missing; null vs missing; start/stop; charset.
- [x] Fingerprint deterministic; changes for every field.
- [x] Sensitive payload absent from repr/error/audit (and from the persisted contract).
- [x] Coherent contract; malformed/legacy/unknown version not eligible; status not trusted.
- [x] same fingerprint → already_present; missing → create; same address different
      fingerprint → blocked.
- [x] incomplete → manual; destination partial → manual; domain missing → blocked.
- [x] add operation unreachable; flag disabled/invalid.
- [x] B4a–B4d without regressions; ≥90% coverage on new safety-critical code.

**Adversarial review:** detail used as existence check; false empty; sensitive payload in
messages; partial fingerprint; whitespace/body normalized; missing fields coerced to
defaults; upsert over an existing address; legacy snapshot promoted; category made AUTO
prematurely; DestinationWrite reachable.

**Acceptance Criteria:**

- [x] Versioned per-domain contract, canonical fingerprint, classification and pure additive
      rules land, fully unit-tested, with the DestinationWrite op unreachable from the runtime.
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

**Riepilogo implementazione.** Fondamento decisionale degli autoresponder costruito e testato
senza alcuna scrittura. `autoresponder_rules.py` (nuovo, puro): op tipizzate SafeRead
`list_auto_responders_op(domain)` e `get_auto_responder_op(email)` (valida **solo** dopo che la
lista prova l'esistenza), DestinationWrite `add_auto_responder_op` (UPSERT, `idempotent=False`,
mai eseguita, `start`/`stop` omessi se 0/None, **nessun** delete); **canonical fingerprint**
deterministico order-stable sul payload completo (known fields in ordine fisso + extra ordinati,
`json.dumps` sort_keys=False + sha256 → distingue null/empty/missing/zero/`"0"`/bool; hash opaco
`afpv1:` senza raw); `redacted_metadata` (solo interval/is_html/charset/start/stop);
`classify_completeness` (`complete`/`incomplete`/`unsupported`, modalità HTML sconosciuta →
unsupported, tenuta); `build_contract` (envelope versionato per-dominio fail-closed: list failure
→ failed/unavailable mai empty, detail failure → partial, address-mismatch/duplicato conflittuale
→ ambiguous, duplicato equivalente → dedup, worst-of-domains); `decide` (matrice additiva);
`is_write_eligible` (versione corrente **e** tutti i domini succeeded/empty, mai fidandosi dello
status). **Redazione chiave:** il contratto memorizza **solo** fingerprint opaco + metadata non
sensibili — `from`/`subject`/`body` non entrano mai nel contratto persistito, log, eventi, errori
o repr (il fingerprint li copre senza esporli).

**File principali.** `apps/api/app/modules/executions/autoresponder_rules.py` (nuovo, 339 righe),
`apps/api/app/modules/inventory/collector.py` (`_assess_autoresponder_contract` sostituito da
`_read_autoresponder_domain`/`_collect_autoresponder_contract` versionato; flat
`email_autoresponders` intatto), `apps/api/app/core/config.py` (validator
`autoresponder_writer_mode` + property double-gate `autoresponder_real_writer_enabled`),
`apps/api/app/modules/readiness/engine.py` (eligibility autoresponder ora su
`{succeeded, empty}`, evidenza design-ready — **non** dispatchabile), `test_autoresponder_inventory.py`
(aggiornato alla shape versionata + redazione), `app/tests/test_email_autoresponder_contract.py`
(nuovo, 57 test). Doc: `README.md` (sezione B4e-i). Task/BACKLOG: B4e ritirato `[/]`, creati
B4e-i/ii/iii, `C3→B4e-iii`, grafo aggiornato.

**Test e comandi eseguiti (esito).**
- Mirati B4e-i: `pytest test_email_autoresponder_contract.py` → **57 passed**; coverage
  `autoresponder_rules.py` **100%**; `test_autoresponder_inventory.py` → **3 passed**.
- Intera suite API: **617 passed** (+57, nessuna regressione; mock/dry-run intatti). Worker
  (venv root): **18 passed**. Web `npm run build`: **OK**. `docker compose config -q`: **OK**.

**Esito review adversariale.** Coperti: detail come existence check (solo nomi enumerati;
`test_detail_only_after_enumeration`, collector `get_calls == [ADDR]`); false empty (list failure
→ failed/unavailable, `test_one_domain_failure_never_false_empties_others`); payload sensibile nei
messaggi (fingerprint opaco, contratto senza from/subject/body, redazione asserita in 3 test);
fingerprint parziale (copre ogni campo + extra); whitespace/body normalizzati
(`test_fingerprint_preserves_body_whitespace_and_html_verbatim`); campi mancanti convertiti in
default (distingue null/empty/missing/zero); upsert sopra esistente (decide same-addr-diff-fp →
blocked; B4e-i non scrive); snapshot legacy promosso (`is_write_eligible` rifiuta); categoria resa
AUTO prematuramente (plans `MANUAL` **non** toccato, preview invariato, readiness solo design-ready);
DestinationWrite raggiungibile (non in `IMPLEMENTED_REAL_CATEGORIES`). Nessuna modifica a
`email_write.py`, dispatch, actor, mock writer.

**Documentazione aggiornata.** `README.md` (sezione «Contratto evidence autoresponder…»); il flag
`AUTORESPONDER_WRITER_MODE` e `.env.example` erano già presenti.

**Nota budget.** Codice di produzione = `autoresponder_rules.py` 339 + `config.py` ~25 +
`collector.py` ~45 + `readiness.py` ~1 = **~410 righe** (< 500). Test 376 (nuovo) + edit al test
inventory. File toccati: 6 (rules, test, config, collector, readiness, inventory-test) — ≤8.

**Limitazioni residue (per B4e-ii).** Engine additive-only `real_autoresponder_writer.py` (il mock
resta intatto) che riusa `execute_email_phase`, due fresh-read anti-upsert (guardia adiacente),
verify per fingerprint, compensation redatta. Cablaggio dispatch resta a B4e-iii (con lo split
iii-a/b/c da formalizzare dopo B4e-ii).
