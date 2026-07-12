# Task B4b-i: Default-address evidence contract and rules

| Field | Value |
|---|---|
| **ID** | `B4b-i` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4a |
| **Branch** | `feat/b4b-i-default-address-contract` |

**Origin:** first sub-task of the scope split of `B4b` (see
[B4b-default-address-writer.md](B4b-default-address-writer.md), split record).
B4b-i establishes the evidence contract, the typed adapter ops, and the pure
decision rules for the per-domain default (catch-all) address, without any
compensable writer engine.

**Goal:** Provide everything needed to *decide* a default-address change safely,
constructed and unit-testable, but structurally unreachable from the runtime: a
SafeRead op for `Email::list_default_address`, a typed DestinationWrite op for
`Email::set_default_address`, a versioned `default_address_contract` in the
collector, pure opaque-form classification, and the pure decision rules. No write
is ever performed here; the compensable engine is B4b-ii.

**Real observed shape (byte-verified Go reference).**
`Email::list_default_address` → `[{"domain","defaultaddress"}]` in a single
account-level read. The fresh cPanel default is the literal
`":fail: No Such User Here"` (embedded double quotes) and is compared as an
**opaque string**. `Email::set_default_address domain= fwdopt= [fwdemail=|failmsgs=]`
is account-level and **overwrites**; `fwdopt` derives from the value shape.

**Scope (≤8 files / ≤500 changed lines):**

- SafeRead op builder for `Email::list_default_address` (account-level).
- Typed DestinationWrite op builder for `Email::set_default_address` — constructible
  and testable (`fwdopt` derivation: `:fail:`→fail[+failmsgs], `:blackhole:`→
  blackhole, else `fwd`+fwdemail), but never reached from the runtime.
- `default_address_contract` in the collector: versioned, fail-closed evidence.
- Pure opaque-form classification: `fail` / `blackhole` / `account_default` /
  `address` / `other`, with source/destination usernames bound explicitly to the
  evidence (never inferred from the value alone).
- Pure decision rules: `already_present` / `set` / `blocked` / `manual` (no write).
- `DEFAULT_ADDRESS_WRITER_MODE` flag: exact-match, disabled by default, fail-closed
  validator + double-gate property.
- Docs and tests.

**Parsing rules:**

1. Always keep the raw value byte-/string-faithful.
2. Never strip quotes, the message after `:fail:`, spaces, or punctuation.
3. Classification never mutates the value.
4. `:fail:` with a message stays class `fail` but preserves the full raw.
5. `:blackhole:` stays `blackhole`.
6. A value equal to the account username is `account_default`.
7. A plain forward is `address` only via an explicit, unambiguous parser.
8. Pipe, program, path, complex list, unexpected quoting, or unknown form → `other`.
9. No permissive heuristic address validation.
10. Do not place the full raw in log/error/audit when it may carry sensitive data.

**Collector contract:**

- One account-level SafeRead unless a stronger need is proven.
- Per-domain record: protected raw, class, username evidence, provenance,
  completeness, issues.
- Reconcile with the verified domains inventory.
- Duplicate domain or conflicting values → `ambiguous`.
- Expected domain with no record → `partial`.
- Unexpected record → `ambiguous`/review.
- Malformed response → `failed`/`unavailable`.
- A failed call is never `empty`.
- Zero successful records is distinguishable from `unreadable`.
- Legacy snapshot readable but not write-eligible.
- Validator does not trust the `status` string alone.
- Deterministic ordering and serialization.

**Pure decision matrix:**

- source and destination raw equivalent → `already_present`;
- source verified and destination fresh → `set`;
- fresh destination means exclusively `fail`, `blackhole`, or `account_default`
  verified against the destination username;
- destination custom/address/other → `blocked`;
- source `other` → `manual`;
- source or destination partial/unreadable/ambiguous → `manual`;
- domain missing on the destination → `blocked`;
- source missing → `manual`;
- no overwrite is performed in B4b-i.

**Constraints:**

- No `email_write.py` change.
- No writer engine.
- No runtime backup or compensation.
- No dispatch/actor.
- No real calls.
- No change to B4c/B4d/B4e.
- DestinationWrite stays unreachable.
- Mock/dry-run unchanged.

**Testing Requirements:**

- [x] Real payload `":fail: No Such User Here"` with quotes preserved.
- [x] Blackhole.
- [x] account_default source/destination with differing usernames.
- [x] Simple address.
- [x] Pipe/program/path/quoted/unknown → other.
- [x] Raw preserved verbatim.
- [x] Coherent list.
- [x] Expected domain missing.
- [x] Unexpected record.
- [x] Equal duplicates.
- [x] Conflicting duplicates.
- [x] Modern/legacy response.
- [x] Malformed response.
- [x] Failure never empty.
- [x] Zero successful records distinct from failure.
- [x] Legacy snapshot.
- [x] Unknown version.
- [x] status succeeded with invalid payload.
- [x] Deterministic serialization.
- [x] Equivalents → already_present.
- [x] destination fail/blackhole/account_default → set.
- [x] destination custom → blocked.
- [x] source other → manual.
- [x] destination domain missing → blocked.
- [x] evidence unreadable/ambiguous → manual.
- [x] Flag disabled and invalid value.
- [x] DestinationWrite unreachable from the runtime.
- [x] No secret/raw leak.
- [x] Collector and B4a without regressions.
- [x] New safety-critical code ≥90% line coverage.

**Adversarial review:**

- false empty; raw strip/normalization; permissive address classification;
  account_default compared against the wrong username; fail/blackhole mistaken for a
  custom value; silent duplicates; legacy snapshot promoted; set allowed toward a
  custom destination; raw payload in logs.

**Acceptance Criteria:**

- [x] Versioned evidence contract and pure rules land, fully unit-tested, with the
      DestinationWrite op unreachable from the runtime.
- [x] Flag disabled by default with a fail-closed validator.
- [x] No test, typecheck, Compose, or coverage regression; mock/dry-run intact.

**Risk & Rollback:** The rules perform no write, so the main risk is a
misclassification that a later writer would trust. Keep the flag disabled and the
op unreachable; revert the modules if needed; never mutate the source.

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

**Riepilogo implementazione.** Fondamento decisionale del catch-all costruito e
testato senza alcuna scrittura: op tipizzate, contratto evidence versionato,
classificazione opaca e regole pure, dietro flag disabled-by-default e
irraggiungibile dal runtime.

- `default_address_rules.py` (nuovo, puro): op `list_default_address_op()`
  (SafeRead account-level) e `set_default_address_op()` (DestinationWrite tipizzata,
  `fwdopt` derivato — `:fail:`→fail[+failmsgs], `:blackhole:`→blackhole,
  else `fwd`+`fwdemail`; `idempotent=False`; costruibile/testabile ma mai eseguita).
  `classify()` non muta mai il raw (solo whitespace locale per shape detection):
  `fail`/`blackhole`/`account_default` (== username, legato all'evidenza)/`address`
  (parser esplicito: un `@`, dominio dotted, nessun sentinel iniziale)/`other`
  (pipe/programma/path/quoting inatteso). `decide()` (vocabolario locale
  `already_present`/`set`/`blocked`/`manual`, nessuna dipendenza da `email_write.py`):
  equivalenti→already_present; dest fresca + sorgente round-trippabile→set; dest
  customizzata→blocked; dominio assente→blocked; sorgente other/mancante o evidenza
  illeggibile/ambigua→manual. `build_contract()` envelope versionato fail-closed
  (lettura fallita→failed/unavailable mai empty; dominio verificato senza record→
  partial; duplicati conflittuali/record inattesi→ambiguous; ordinamento
  deterministico; raw byte-faithful). `is_write_eligible()` richiede versione corrente
  **e** stato succeeded (snapshot legacy leggibile ma inerte).
- `collector.py`: `_collect_default_address` — una SafeRead account-level,
  riconciliata con l'enumerazione domini verificata, delega alla logica pura; persiste
  `data['default_address_contract']` + coverage; nessuna write.
- `config.py`: flag `default_address_writer_mode` + validator fail-closed
  (`DEFAULT_ADDRESS_WRITER_MODE`) + property double-gate
  `default_address_real_writer_enabled` (disabled by default).

**File principali.** `apps/api/app/modules/executions/default_address_rules.py`
(nuovo), `apps/api/app/modules/inventory/collector.py` (esteso), `app/core/config.py`
(esteso), `apps/api/app/tests/test_default_address_contract.py` (nuovo, 29 test). Doc:
`README.md` (sezione B4b-i + tabella flag), `.env.example`
(`DEFAULT_ADDRESS_WRITER_MODE`). Task/BACKLOG: B4b ritirato `[/]`, B4b-i/B4b-ii creati,
grafo e dipendenza `B4e→B4b-ii` aggiornati.

**Test e comandi eseguiti (esito).**
- Mirati B4b-i: `pytest test_default_address_contract.py` → **29 passed**; coverage
  `default_address_rules.py` **100%**.
- Intera suite API: **380 passed** (+29, nessuna regressione; mock/dry-run intatti).
- Worker (venv): **18 passed**. Web `npm run build`: **OK**. `docker compose config
  -q`: **OK**.

**Esito review adversariale.** Verificati e coperti: false-empty (lettura fallita→
failed, domini illeggibili→unavailable, mai empty); strip/normalizzazione del raw
(raw verbatim; classify muta solo copia locale); classificazione permissiva di address
(parser esplicito; `user@localhost`→other); account_default vs username sbagliato
(classe legata allo username fornito per lato); fail/blackhole scambiati per custom
(prefix match, classi fresche); duplicati silenziosi (uguali→un record; conflittuali→
ambiguous raw None); snapshot legacy promosso (`is_write_eligible` richiede versione);
set verso dest custom (→blocked, mai overwrite); raw nei log (B4b-i non emette
eventi; il raw vive solo nel contratto-evidenza, come `forwarder_contract`; secret-leak
test verde). Nessuna modifica a `email_write.py`, nessun engine/dispatch/chiamata reale;
DestinationWrite non registrata in `IMPLEMENTED_REAL_CATEGORIES`.

**Documentazione aggiornata.** `README.md` (sezione B4b-i + riga tabella writer),
`.env.example` (`DEFAULT_ADDRESS_WRITER_MODE`).

**Limitazioni residue (per B4b-ii).** Engine writer compensabile: seam `backup_of` in
`email_write.py` (backup redatto pre-write atomico, backup-fallito→zero-write),
`default_address_writer.py` (fresh-read→decide→backup→gated `set`→verify live→
compensation) che riusa `execute_email_phase`. Cablaggio dispatch/authorize/lease/
fencing resta a B4e.
