# Task B4d-ii: Additive-only filter writer engine

| Field | Value |
|---|---|
| **ID** | `B4d-ii` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4d-i |
| **Branch** | `feat/b4d-ii-filter-writer-engine` |

**Origin:** second sub-task of the scope split of `B4d` (see
[B4d-email-filters-writer.md](B4d-email-filters-writer.md), split record).

**Goal:** Implement the per-scope email filters writer as an *additive-only* phase reusing
`execute_email_phase` and the B4d-i contract/fingerprint/rules (no `email_write.py`
change): fresh-read live per scope → decide (B4d-i rules) → an **upsert-guarded** single
`store_filter` reached only when the name is live-absent → live post-write verify by the
complete fingerprint → redacted compensation. Not wired into the runtime dispatch (B4e).

**Upsert danger.** `Email::store_filter` UPSERTS, so it is non-idempotent and dangerous:

- Introduce a guard **immediately before** the call; if the name appears between the
  fresh-read snapshot and the write, block.
- If a reliable fresh-read cannot be obtained, zero write.
- Never treat the op as idempotent merely because the name matches.
- Never send a source filter over an existing destination filter.

**Category behavior:**

- Reuse `execute_email_phase`; destination-only gateway; fresh-read per scope immediately
  before the decision.
- A single `store_filter` only on a live-absent name; **no** `DeleteFilter`; no auto-retry;
  timeout/ambiguous → fresh-read, never a second `store_filter`.
- Post-write verification via the complete fingerprint (not by name).
- `before_write` remains the B4e gate/fencing seam.
- Compensation metadata indicates a future controlled removal of **only the created
  filter**, with scope/name/fingerprint redacted; no automatic rollback.

**Testing Requirements (deterministic fake gateway, no real servers):** flag disabled;
source impossible/unsupported; same name+fingerprint → zero write; live-absent name →
one write + verify; same name/different fingerprint → blocked; destination-only preserved;
source incomplete → manual; destination partial → manual; mailbox missing → blocked; race
after snapshot → zero write; race immediately before `store_filter` → block; ambiguous
positive/negative; no second write; post-write fingerprint mismatch; zero DeleteFilter;
redacted compensation; no raw/payload/secret leak; B4a/B4b/B4c without regressions; ≥90%
coverage.

**Adversarial review:** unintended upsert; same-name collision ignored; order loss;
partial fingerprint; false-empty per mailbox; sensitive payload in logs; `store_filter`
retry; implicit `DeleteFilter`; verify-by-name-only; account/mailbox scope confused;
compensation that could delete a pre-existing filter.

**Acceptance Criteria:**

- [x] A filter is created only when its name is live-absent, guarded against the upsert
      race, and verified live by the complete fingerprint; a same-name different filter is
      never overwritten; a destination-only filter is never deleted; the source is never
      written.
- [x] No test, typecheck, Compose, or coverage regression.
- [x] Real behavior disabled by default and unreachable from the runtime until B4e.

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

**Riepilogo implementazione.** Engine filtri **strettamente additivo** implementato in
`filter_writer.py` (nuovo) riusando `execute_email_phase` (B4a) e consumando solo B4d-i,
**senza toccare** `email_write.py`. Poiché `store_filter` è un **UPSERT**, la write è
raggiunta solo su nome live-assente provato da **due fresh-read distinte**: la `read_live`
iniziale (list + `get_filter` per i nomi enumerati, template-safe) dietro la decisione, e una
seconda `list_filters` di **guardia** eseguita **dentro** `FilterGateway.create`,
immediatamente adiacente a `store_filter`. L'ordine imposto dal framework (`before_write` →
`gateway.create`) produce esattamente la sequenza richiesta: read-live → decide →
`before_write` → fresh-list guard → unica `store_filter`; **nessuna modifica al framework**
è stata necessaria (la guardia dentro `create` colloca `before_write` prima della guardia).
La guardia (`name_absent`) prova l'assenza **solo per enumerazione** (mai `get_filter`),
riusa lo stesso scope (`self._account`), non riusa la prima lista; nome ricomparso o lista
unreadable/malformed → zero write. Decider (`decide_filter_live`) mappa
`FilterDecision → WriteAction` (`create → create`): source incompleta/unsupported/non-verified
→ manual; scope mailbox assente → blocked; destination unreadable/ambiguous → manual; nome
presente con fingerprint uguale → already_present, diverso → blocked. Verify tramite
fingerprint **completo** su nuova lista (enumerazione → `get_filter` → confronto). Una sola
`store_filter`, mai retry; timeout/ambiguo → fresh-read, mai seconda write. `stored` registra
il tentativo **prima** della write (un timeout ambiguo può comunque applicare); la
compensation redatta (`manual_remove_created_filter`, scope + nome + fingerprint + conferma)
è attaccata **solo** quando il gateway ha scritto (`step_id in gateway.stored`) **e** la
rilettura ha verificato — mai per `already_present` né per una write saltata dalla guardia,
così non può rimuovere un filtro preesistente. Nessun `DeleteFilter`.

**File principali.** `apps/api/app/modules/executions/filter_writer.py` (nuovo, 208 righe),
`apps/api/app/tests/test_real_filter_writer.py` (nuovo, 28 test). Doc: `README.md` (sezione
«Engine writer filtri additive-only (B4d-ii)»). Task/BACKLOG aggiornati. Nessuna modifica a
`email_write.py`, `filter_rules.py`, `collector.py`, `config.py`, `dispatch.py`, actor o
`IMPLEMENTED_REAL_CATEGORIES`.

**Test e comandi eseguiti (esito).**
- Mirati B4d-ii: `pytest test_real_filter_writer.py` → **28 passed**; coverage
  `filter_writer.py` **100%**.
- Intera suite API: **560 passed** (+28; nessuna regressione; mock/dry-run intatti). Worker
  (venv root): **18 passed**. Web `npm run build`: **OK**. `docker compose config -q`: **OK**.

**Esito review adversariale.** Coperti: finestra TOCTOU (guardia dentro `create`, unica
`stored.add` non-fallibile prima della write); `get_filter` per assenza
(`test_guard_uses_list_not_get_filter_for_presence`, `name_absent` solo enumerazione);
seconda lista omessa (list_calls==3: read-live/guard/verify); scope diverso nella guardia
(list_accounts tutti uguali); upsert sopra omonimo (same-name-diff-fp → blocked; race →
guard aborts); retry StoreFilter (single store_call, ambiguo senza retry); verifica solo per
nome (fingerprint completo; post-write absent/mismatch/template → failed); ordine payload
perso (val1/val2, dest1/dest2 asseriti); template accettato (detail mismatch → manual,
verify template → failed); compensation capace di rimuovere preesistente
(`test_race_same_fingerprint_appears_at_guard_no_write_no_false_compensation`,
already_present senza comp); DeleteFilter introdotta (asserito assente); payload leak
(`test_no_filter_payload_in_events_or_result`).

**Documentazione aggiornata.** `README.md` (sezione B4d-ii). Il flag `FILTER_WRITER_MODE` e
`.env.example` erano già presenti da B4d-i.

**Nota budget.** Codice di produzione `filter_writer.py` = 208 righe (< 500). File di test
363 righe (matrice di 28 test). File toccati: 4 (writer, test, README, task/backlog) — ≤8.

**Limitazioni residue (per B4e).** Cablaggio nel dispatch runtime (authorize/lease/fencing
via `before_write`, aggiunta a `IMPLEMENTED_REAL_CATEGORIES`, resolve degli item dal
contratto B4d-i con `scope_present` dall'inventario mailbox destination) resta a B4e,
insieme all'autoresponder writer e all'integrazione dispatch email.
