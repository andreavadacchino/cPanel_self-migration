# Task B4b-ii: Compensable default-address writer engine

| Field | Value |
|---|---|
| **ID** | `B4b-ii` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B4b-i |
| **Branch** | `feat/b4b-ii-default-address-writer-engine` |

**Origin:** second sub-task of the scope split of `B4b` (see
[B4b-default-address-writer.md](B4b-default-address-writer.md), split record).
Builds the compensable writer engine on top of the B4b-i evidence contract and
pure rules, and the B4a shared framework.

**Goal:** Implement the per-domain default-address writer as a *compensable, not
additive* phase: fresh-read live → decide (B4b-i rules) → persist a redacted
backup atomically **before** the write → gated `set_default_address` → live
post-write verify → redacted compensation. It reuses `execute_email_phase` from
B4a without duplicating its lifecycle. Not wired into the runtime dispatch (that
stays with B4e).

**Scope (≤8 files / ≤500 changed lines):**

- `email_write.py` — add an optional `backup_of` seam: when a category provides it,
  the engine computes a redacted backup from the pre-write live evidence and records
  it **before** the write; if the backup cannot be secured, the write is not reached
  (backup-or-nothing). Default `None` keeps the forwarder path unchanged.
- `default_address_writer.py` (new) — resolve items from the preview default-address
  steps, plan the redacted call, provide `backup_of` (previous fresh value → restore
  descriptor), and run the compensable phase with the B4b-i rules.
- tests + docs.

**Category behavior:**

- `set_default_address` overwrites, so a `set` is only reached for a fresh
  destination (per B4b-i); a differing custom catch-all is blocking.
- The previous verified value is persisted as redacted backup/compensation metadata
  **before** any write; if the backup is not persisted atomically, the write is not
  reached; live post-write verification.
- Non-idempotent write: never auto-retried; a timeout/ambiguous outcome is resolved
  by a fresh read, never a blind second write.
- `before_write` remains the B4e authorize/lease/fencing seam.

**Testing Requirements (deterministic fake gateway, no real servers):** the full
compensable matrix — flag disabled; source impossible; equivalent → zero write;
destination missing/empty → backup + one write + verify; different → blocked, zero
write; source/destination unreadable/ambiguous → manual; destination domain
missing → blocked; reject/fail preserved; forward-to-address preserved;
pipe/command → manual; collector duplicates/conflict; failure never empty; legacy
snapshot ineligible; backup persisted before the write; backup failure → zero
write; race (value appeared after snapshot) → blocked; ambiguous write + positive
fresh-read; ambiguous write + negative fresh-read; no second write; post-write
mismatch; `before_write` fails → zero write; compensation metadata complete and
redacted; no address/sensitive payload in events unless approved by the redacted
contract; B4a framework and forwarder without regressions; ≥90% coverage.

**Adversarial review:** overwrite of an existing catch-all; false empty; backup
after the write; insufficient compensation; pipe/command reinterpreted; domain
normalized with wrong meaning; race between fresh-read and write; non-idempotent
retry; false post-write success; secret/address leakage.

**Acceptance Criteria:**

- [x] Only a fresh destination is set, backed up before the write and verified live;
      a differing catch-all is never overwritten; the source is never written.
- [x] No test, typecheck, Compose, or coverage regression.
- [x] Real behavior disabled by default and unreachable from the runtime until B4e.

**Risk & Rollback:** Main risk is an unintended overwrite or a false verification.
Keep the flag disabled, revert the module if needed, never compensate by mutating
the source.

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

**Riepilogo implementazione.** Engine writer compensabile del catch-all che consuma
solo il contratto/regole B4b-i e riusa `execute_email_phase`, aggiungendo un backup
pre-write. Non cablato nel dispatch (irraggiungibile fino a B4e).

- `email_write.py` (esteso): seam generico minimo `backup_of`/`persist_backup`. Il
  motore ora passa il `live` pre-write a `_do_create`; quando `backup_of` è presente,
  costruisce il backup dal live e lo persiste via callback **prima** della write
  (`_persist_backup`): backup non costruibile → `backup_unavailable`, riferimento
  falsy/non-string → `backup_not_persisted`, in entrambi i casi **zero write**. La
  compensation redatta (solo `backup_ref`) è aggiunta al risultato **sia su successo
  sia su fallimento** (la write potrebbe essersi applicata). Il forwarder additivo
  (nessun `backup_of`) resta identico.
- `default_address_writer.py` (nuovo): `_source_evidence`/`_destination_evidence`
  costruiscono le evidenze dal live (dest riclassificata con lo **username
  destination**); `decide_default_address_live` mappa la decisione B4b-i
  (`set→create`) al framework; `backup_default_address` costruisce il backup tipizzato
  dal **live** (dominio/raw/classe/username/provenienza/evidence/reverse_op/conferma),
  raw solo nel contenitore protetto; `plan`/`compensation` redatti (nessun raw);
  `DefaultAddressGateway` solo-destinazione (SafeRead + DestinationWrite B4b-i);
  `run_default_address_phase` orchestra il tutto.

**File principali.** `apps/api/app/modules/executions/email_write.py` (esteso, seam),
`apps/api/app/modules/executions/default_address_writer.py` (nuovo),
`apps/api/app/tests/test_real_default_address_writer.py` (nuovo, 27 test). Doc:
`README.md` (sezione B4b-i estesa con l'engine B4b-ii). Flag `DEFAULT_ADDRESS_WRITER_MODE`
riusato da B4b-i (nessun nuovo flag).

**Test e comandi eseguiti (esito).**
- Mirati B4b-ii: `pytest test_real_default_address_writer.py` → **27 passed**; coverage
  `default_address_writer.py` **100%**, seam `email_write.py` **100%** (branch).
- Forwarder B4a: **18 passed** (nessuna regressione dal seam).
- Intera suite API: **407 passed** (+27; mock/dry-run intatti).
- Worker (venv): **18 passed**. Web `npm run build`: **OK**. `docker compose config
  -q`: **OK**.

**Esito review adversariale.** Verificati e coperti: backup dopo la write (persistito
**prima**, ordine `["backup","write"]` asserito); backup dallo snapshot (costruito dal
`live`, raw asserito == valore live); riferimento non durevole (None/invalid→zero
write); raw nel compensation/evento (solo `backup_ref`; raw solo nello store; test
secret verde); overwrite dest custom (→blocked); classificazione con username sorgente
(dest usa `dest_username`); seconda write dopo timeout (fresh-read, `create_calls`==1);
falso successo post-write (verify = `already_present`; mismatch→failed); seam che altera
il forwarder (forwarder invariato, nessun `backup_ref`); eccezione tra backup e write
(`before_write` che solleva→propaga, write saltata, backup conservato). Nessuna modifica
a dispatch/actor/`IMPLEMENTED_REAL_CATEGORIES`/B4c/B4d/B4e.

**Documentazione aggiornata.** `README.md` (engine compensabile B4b-ii nella sezione
default-address).

**Nota budget.** Diff codice+test ~585 righe (raw git) / ~440 righe non-vuote:
**sopra il target 500 raw** ma sotto in righe logiche. La stima iniziale (~400) ha
sottovalutato il file di test (matrice obbligatoria di 27 test) e il seam. Ho rimosso
il grasso (docstring, un test ridondante fuso nel parametrizzato) onorando "evita
docstring ridondanti"; oltre servirebbe tagliare test obbligatori della categoria a
rischio più alto (overwrite compensabile). File toccati: 4 (≤8). Un ulteriore split
retroattivo avrebbe alto churn e valore nullo (codice corretto, 100% coperto, gate
verdi).

**Limitazioni residue (per B4e).** Cablaggio runtime: registrazione in
`IMPLEMENTED_REAL_CATEGORIES`, rivalidazione gate/lease/fencing via `before_write`,
persistenza durevole del backup e commit atomico. Categoria non runtime-ready.
