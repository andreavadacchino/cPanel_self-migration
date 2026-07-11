# Task B3b-ii: Domain phase dispatch wiring

| Field | Value |
|---|---|
| **ID** | `B3b-ii` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B3b-i |
| **Branch** | `feat/b3b-ii-domain-phase-dispatch` |

**Origin:** second half of the split of `B3b` (see
[B3b-real-domain-writer-dispatch.md](B3b-real-domain-writer-dispatch.md)). B3b-ii
wires the B3b-i engine into the real dispatch/actor behind the double gate, adding
the lease/fencing/authorize integration and the terminal-state selection.

> A local patch with the drafted wiring (from the pre-split implementation) is
> saved in the session scratchpad as `b3b-ii-wiring.patch` (~242 lines touching
> `dispatch.py` and `config.py`); use it as a starting reference.

**Scope:**

- `apps/api/app/modules/executions/dispatch.py` — `_executable_categories`,
  gateway factory, `_run_domain_phase`, terminal-state selection in
  `worker_start`; keep the halt path for non-executable runs.
- `apps/api/app/core/config.py` — `domain_real_writer_enabled` double-gate property.
- `apps/api/app/tests/test_real_dispatch.py` — integration tests.
- `migration-platform/README.md`, `.env.example` — flags and operational docs.

**Implementation:**

1. Double gate: a real create is reachable only when `REAL_EXECUTION_MODE=enabled`
   AND `DOMAIN_WRITER_MODE=enabled`; both default disabled.
2. `authorize()`/`WriteTarget`/lease/fencing before the phase, before the
   fresh-read, immediately before each write (via the engine's `before_write`
   hook), and after the write before persisting (via `finalize_attempt`, which
   re-checks fencing).
3. Terminal-state selection: solo eligible domains → `succeeded`; only
   unimplemented categories → `halted`; mixed / manual-pending → `halted`
   (never `succeeded` while selected categories remain unexecuted); hard failure
   → `failed`; fenced-out after write → no success persisted.
4. `ExecutionAttempt` checkpoint + legal transitions; crash/retry does not
   duplicate the domain (fresh read → `already_present`).

**Testing Requirements:**

- [x] Real/domain flag disabled; source rejected as target.
- [x] Solo-domains run does not halt; only-unimplemented run halts; mixed run is
      not falsely fully successful.
- [x] Gate or lease expired before the write; fencing lost after the write → no
      success; stale confirmation/evidence between dispatch and phase.
- [x] Crash/retry idempotency; compensation metadata persisted and redacted; no
      secret in events; mock writer and dry-run do not regress.

**Acceptance Criteria:**

- [x] Real domain writer reachable only under both flags; source structurally
      unusable as a write target; a fenced-out worker cannot record success.
- [x] No new test, typecheck, Compose, or coverage regression.
- [x] Real behavior disabled by default.

**Risk & Rollback:** Main risk is an unintended destination mutation or false
verification. Keep both flags disabled, revert the PR if needed, and use only
recorded compensation steps; never compensate by mutating the source.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```

## Completion Record

- **Data:** 2026-07-11
- **Riepilogo:** Seconda metà dello split di B3b. Collegato il motore additivo di
  B3b-i (`real_domain_writer.py`) al dispatch/worker reale sotto **doppio gate**
  (`REAL_EXECUTION_MODE=enabled` + `DOMAIN_WRITER_MODE=enabled`, proprietà
  `settings.domain_real_writer_enabled`, entrambi `disabled` per default). In
  `worker_start`: `_executable_categories` (solo `domains`, e solo con doppio gate);
  `_build_domain_gateway` che costruisce il client cPanel **esclusivamente dalla
  destinazione** (`allow_destination_writes=True`, endpoint non-`destination`
  rifiutato); `_source_domain_records` che legge fail-closed l'envelope
  `domains_data` (assente nell'inventario attuale → ogni passo manuale/pending →
  `halted`, mai write fabbricata); `_run_domain_phase` che passa un hook
  `before_write` per rivalidare `authorize` (lease+fencing+evidenza) immediatamente
  prima di ogni create. Selezione stato terminale: solo-domini verificati (incl.
  `already_present`) → `succeeded`; `blocked`/create non verificata → `failed`;
  passo manuale o categoria non implementata presente → `halted` con
  `pending_categories`/`manual_pending` espliciti — mai `succeeded` con categorie
  selezionate non eseguite. `DOMAIN_WRITER_MODE` valida i valori fail-closed
  (`disabled`/`mock`/`enabled`; `real` ritirato → rifiutato al load).
- **File principali:** `dispatch.py` (+wiring), `core/config.py` (double-gate
  property + field_validator), `tests/test_real_dispatch.py` (+20 test B3b-ii),
  `README.md`, `.env.example`, più `BACKLOG.md`/questo task. 7 file. Diff ~600 righe:
  il core produzione+doc è ~280 (in linea col patch scratch di 242); l'eccedenza
  oltre 500 è interamente **test di sicurezza mandati dal task**, che le regole
  vietano di sacrificare.
- **Test e comandi (tutti PASS):** API **274 passed**, coverage `dispatch.py`
  **98%** / `config.py` **100%** (nessuna regressione vs baseline 91%); adapter
  **81 passed**; worker **18 passed** (via `.venv` di progetto con dramatiq 2.2.0);
  `npm run build` OK; `docker compose config -q` OK. Test chiave: doppio gate,
  flag disabled→halt, gateway solo-destinazione + rifiuto sorgente, solo-domini→
  succeeded, already_present→succeeded senza write, ambiguo→single-create,
  blocked/post-write-mismatch→failed, manuale/only-unimplemented/misto→halted,
  fencing stale pre/post-write, drift in `before_write`, write completata non
  orfana da drift di gate non-fencing, retry idempotente, cancellation, redazione
  checkpoint/compensation, no secret leak, gateway routing reale.
- **Review:** review adversariale indipendente (python-reviewer) → 1 **Critical** +
  2 **High** + 1 Medium. Risolti:
  1. Critical — la rivalidazione post-write usava `authorize()` senza `categories`,
     ri-controllando la capability di **tutte** le categorie del preview: in un run
     misto (o con conferma forte scaduta durante una fase reale lunga) una write
     dominio già eseguita e verificata poteva far sollevare l'authorize finale →
     nulla persistito → run bloccato per sempre in `running`. **Fix:** la
     rivalidazione post-write è ora **solo-fencing** (`assert_fencing_current`,
     task punto 8), disaccoppiata da categorie estranee/TTL conferma; test di
     regressione `test_completed_write_not_stranded_by_unrelated_gate_drift`.
  2. High (split-commit) — esito attempt e stato run erano persistiti da due commit
     separati (finestra di crash → attempt terminale ma run `running`). **Fix:** run
     e tentativo mutati prima e persistiti da **un unico commit** di
     `finalize_attempt` (atomico), sia sul ramo `failed` sia su `succeeded`/`halted`.
  3. High (test mancante) — nessun caso con categoria co-selezionata non idonea dopo
     una write riuscita. **Fix:** aggiunto il test di regressione sopra.
  Medium — assorbito dal fix Critical (post-write disaccoppiato dalla readiness/TTL).
  Nessun rilievo su source-read-only, duplicazione retry, segreti, transizioni.
- **Documentazione:** aggiornati `README.md` (sezione «Fase domini reale nel worker
  (B3b-ii)»: doppio gate, gateway solo-destinazione, rivalidazione a tre stadi,
  evidenza sorgente fail-closed, stato terminale, commit atomico, limitazione di
  recovery), nota writer mock e tabella flag; `.env.example` (valori ammessi di
  `DOMAIN_WRITER_MODE`).
- **Limitazioni residue:** (a) l'inventario attuale raccoglie solo la lista nomi di
  `list_domains`, non l'envelope ricco `domains_data`; finché non è arricchito, ogni
  passo dominio reale resta manuale/pending e il run si ferma in `halted` (mai una
  write) — arricchimento = task separato. (b) Recovery ereditata da A3: un crash del
  worker durante la fase lascia un tentativo `running` non riaccodabile (reconciliation
  esterna fuori scope); mitigata dalla rilettura fresca (un tentativo ripreso
  classifica il dominio già creato come `already_present`, mai duplicato).
