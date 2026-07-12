# Task B4e-ii: Additive-only autoresponder writer engine

| Field | Value |
|---|---|
| **ID** | `B4e-ii` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4e-i |
| **Branch** | `feat/b4e-ii-autoresponder-writer-engine` |

**Origin:** second sub-task of the scope split of `B4e` (see
[B4e-autoresponder-dispatch.md](B4e-autoresponder-dispatch.md), split record).

**Goal:** Implement the per-address autoresponder writer as an *additive-only* phase reusing
`execute_email_phase` and the B4e-i contract/fingerprint/rules (no `email_write.py` change).
`Email::add_auto_responder` UPSERTS, so a write is reached only on a live-absent address —
proven by TWO distinct fresh reads (initial decision + a guard immediately before the write)
— then verified live by the complete fingerprint. Not wired into the runtime dispatch (B4e-iii).

**Naming:** the existing `autoresponder_writer.py` is the mock orchestration and must stay
intact (mock/dry-run unchanged); the real engine lands in a new module (e.g.
`real_autoresponder_writer.py`).

**Category behavior:**

- Reuse `execute_email_phase`; destination-only gateway; fresh-read per domain immediately
  before the decision.
- Two anti-upsert fresh reads: the initial decision read, and a guard (`list_auto_responders`
  in the same domain, absence by enumeration only — never `get_auto_responder`) immediately
  before the single `add_auto_responder`.
- A single create only on a live-absent address; no delete/rollback; no auto-retry;
  timeout/ambiguous → fresh list/detail, never a second create.
- Post-write verification via the complete fingerprint (not the body in logs).
- `before_write` remains the B4e-iii gate/fencing seam.
- Compensation metadata (redacted: scope/address/fingerprint, controlled future removal of the
  just-created responder, confirmation required) attached **only** for a create the gateway
  actually wrote and verified; never for `already_present` or a guard-skipped write, so it can
  never remove a pre-existing responder.

**Testing Requirements (deterministic fake gateway, no real servers):** flag disabled; source
impossible/unsupported; same address+fingerprint → zero write; live-absent address → guard +
one write + verify; same address different fingerprint → blocked; destination-only preserved;
source incomplete → manual; destination partial → manual; domain missing → blocked; race after
snapshot / immediately before the create → zero write; guard uses the same scope and never
`get_auto_responder` for absence; before_write failure skips guard+write; ambiguous
positive/negative; no second write; post-write fingerprint mismatch; no delete; redacted
compensation only for a verified create; no raw/body/secret leak; B4a–B4d without regressions;
≥90% coverage.

**Adversarial review:** unintended upsert; same-address collision ignored; body/subject leak;
verify-by-address-only; retry of the create; implicit delete; template accepted; compensation
able to remove a pre-existing responder; DestinationWrite payload in events.

**Acceptance Criteria:**

- [x] An autoresponder is created only when its address is live-absent, guarded against the
      upsert race, and verified live by the complete fingerprint; a same-address different
      responder is never overwritten; nothing is deleted; the source is never written.
- [x] No test, typecheck, Compose, or coverage regression.
- [x] Real behavior disabled by default and unreachable from the runtime until B4e-iii.

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

**Riepilogo implementazione.** Engine autoresponder *strettamente additivo* costruito e testato
senza scritture reali sul runtime. `real_autoresponder_writer.py` (nuovo) riusa
`execute_email_phase` (B4a) e consuma **solo** contratto/fingerprint/regole di B4e-i, **senza**
toccare `email_write.py`; il writer mock `autoresponder_writer.py` resta intatto. **Provenienza
payload:** il payload operativo completo (`from`/`subject`/`body`/`interval`/`is_html`/`charset`/
`start`/`stop`) è risolto **solo** dallo snapshot immutabile `data["email_autoresponders"]`
(`resolve_autoresponder_items`/`_resolve_source`) e **vincolato** al contratto — il fingerprint
ricostruito dallo snapshot deve coincidere con `record["fingerprint"]`, e dominio + local part
devono coincidere; assente/duplicato/detail-fallito/mismatch/completeness non-complete → `manual`/
`blocked`, zero write, nessun default inventato. Il payload completo vive **solo** in
`EmailItem.payload` (in memoria): planned_call/compensation/eventi portano soltanto dominio,
indirizzo, fingerprint e metadati non sensibili. **Anti-upsert:** `AutoresponderGateway.create`
esegue una seconda `list_auto_responders` di guardia (assenza per sola enumerazione, mai
`get_auto_responder`) immediatamente prima dell'unica `add_auto_responder`; indirizzo ricomparso o
lista unreadable/malformed → zero write. **Verify** per fingerprint completo su nuova lista+
dettaglio (template mai come successo); una sola write, mai retry; timeout/ambiguo → fresh-read.
**Compensation** redatta (`manual_remove_created_autoresponder`) solo per create realmente scritta
e verificata (`gateway.stored`), mai per `already_present` o write saltata.

**File principali.** `apps/api/app/modules/executions/real_autoresponder_writer.py` (nuovo, ~300
righe con docstring), `apps/api/app/tests/test_real_autoresponder_writer.py` (nuovo, 40 test),
`README.md` (sezione «Engine writer autoresponder additive-only (B4e-ii)»), `tasks/BACKLOG.md`
(B4e-ii `[x]`). **Nessuna** modifica a `email_write.py`, `autoresponder_rules.py`,
`autoresponder_writer.py` (mock), dispatch, actor, planner, readiness, config.

**Test e comandi eseguiti (esito).**
- Mirati B4e-ii: `pytest test_real_autoresponder_writer.py` → **40 passed**; coverage
  `real_autoresponder_writer.py` **96%** (righe residue = rami difensivi belt-and-suspenders).
- Intera suite API: **657 passed** (+40, nessuna regressione; mock/dry-run intatti).
- Worker (venv `migration-platform/.venv`, `DRAMATIQ_TESTING=1`): **18 passed**.
- Web `npm run build`: **OK**. `docker compose config -q`: **OK**.

**Esito review adversariale.** Coperti tutti i vettori richiesti: payload accettato dal client
(ignorato — solo snapshot, `test_payload_from_request_or_preview_is_ignored`); fingerprint non
rivalidato (ricostruito e confrontato in `_resolve_source` + verify post-write); detail come
existence check (guardia e assenza solo per enumerazione, `test_guard_uses_list_not_get_for_presence`);
seconda lista omessa (guardia reale, `list_calls == 3`); upsert su address ricomparso (race tests →
zero write); retry create (mai, single write su CpanelError); normalizzazione del body (verbatim,
`test_body_..._preserved`); template accettato (→ ambiguous/manual, verify per fingerprint);
payload sensibile persistito (redaction blob test; contratto senza from/subject/body); compensation
con contenuto (solo dominio/indirizzo/fingerprint); modifica prematura planner/dispatch (non in
`IMPLEMENTED_REAL_CATEGORIES`, categoria resta MANUAL). Nessuna modifica a B4e-iii o C3.

**Documentazione aggiornata.** `README.md` (nuova sezione B4e-ii); il flag `AUTORESPONDER_WRITER_MODE`
e `.env.example` erano già presenti da B4e-i.

**Limitazioni residue (per B4e-iii).** Cablaggio dispatch/registry/gate/fencing per-categoria e
promozione della categoria da `MANUAL` ad `AUTO` (split iii-a/b/c). L'engine resta irraggiungibile
dal runtime fino ad allora.
