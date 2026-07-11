# Task B3c-ii: Rich domain readiness integration

| Field | Value |
|---|---|
| **ID** | `B3c-ii` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B3c-i |
| **Branch** | `feat/b3c-ii-domain-readiness-integration` |

**Origin:** seconda metà dello split di `B3c` (vedi
[B3c-rich-domain-inventory.md](B3c-rich-domain-inventory.md)). B3c-ii integra il
contratto ricco prodotto da B3c-i nella readiness/gate e dimostra che B3b-ii lo
consuma, **chiudendo la limitazione residua (a) di B3b-ii**.

**Goal:** Rendere la categoria `domains` `eligible_for_real_design` solo quando il
contratto `domains_data` è `succeeded` e coerente su **entrambi** gli endpoint;
partial/failed/legacy restano non eleggibili e il safety gate resta fail-closed.
Verificare che `_source_domain_records` di B3b-ii consuma il nuovo snapshot senza
adattamenti permissivi e che un passo dominio valido non è più `manual` per
assenza dell'envelope.

**Scope:**

- `apps/api/app/modules/readiness/engine.py` — ramo `domains` che diventa
  `eligible_for_real_design` su `domains_contract` `succeeded` su entrambi gli
  endpoint (pattern `*_contract_verified` esistente); `domains_contract` in
  `EVIDENCE_CATEGORIES`.
- (se strettamente necessario) `apps/api/app/modules/plans/engine.py` /
  `comparison/engine.py` — solo propagazione dei campi verificati; nessuna modifica
  a decisioni/writer.
- `apps/api/app/tests/` — readiness eligibilità solo su contratto completo; gate
  blocca contratto partial; round-trip snapshot; comparison/planner preservano i
  campi; **prova end-to-end B3b-ii**: dato uno snapshot con contratto `succeeded`,
  un passo dominio valido raggiunge `create`/`already_present` (non più `manual`).
- `migration-platform/README.md` — significato readiness/eligibility; rimozione
  della limitazione (a) di B3b-ii (dimostrata dai test); recovery → C4 resta.

**Implementation:**

1. Readiness: `domains` eleggibile solo con `domains_contract` `succeeded` su
   source **e** destination; ogni altro stato → non eleggibile (fail-closed).
2. Safety gate invariato: readiness non-eligible → gate fallisce → la fase domini
   non parte per un contratto partial/ambiguous/legacy.
3. **Bridge writer**: B3c-i persiste l'envelope sotto `data["domains_contract"]`
   (chiave dedicata, per non collidere con `data["domains_data"]` che
   `_source_domain_records` interpreta nella shape grezza). B3c-ii collega il
   writer a questa chiave (via `domain_contract.read_contract`/proiezione),
   fail-closed: solo un contratto `succeeded` fornisce record; partial/ambiguous/
   failed/legacy non autorizzano una write (comunque già bloccati dal gate).
4. Propagazione planner/preview solo se i campi verificati non arrivano già a
   B3b-ii dallo snapshot.

**Absolute constraints:**

- Nessuna modifica al writer B3b-ii / regole B3a / dispatch.
- Nessuna nuova write/retry/dispatch/SSH/altra categoria.
- Nessun contatto con server reali; nessun secret leak.

**Testing Requirements:**

- [x] Comparison/planner conservano i campi richiesti dal contratto (record proiettati
      da `project_records` con tipo/docroot/internal_label; nessuna modifica al planner).
- [x] Readiness eleggibile soltanto su contratto completo su entrambi gli endpoint.
- [x] Readiness NON eleggibile su partial/failed/legacy.
- [x] Safety gate blocca un contratto partial (fail-closed) — readiness non-eligible
      → gate rifiuta il dispatch.
- [x] Serializzazione e rilettura dello snapshot conservano il contratto.
- [x] B3b-ii riceve record ricchi: un passo valido non è più `manual` per assenza
      dell'envelope (end-to-end, gateway fake, nessun server reale).
- [x] Nessun secret leak; nessuna regressione readiness/comparison/gate/dispatch.

**Acceptance Criteria:**

- [x] `domains` eleggibile solo su contratto completo e coerente; partial/legacy
      fail-closed.
- [x] B3b-ii consuma i record persistiti completi senza adattamenti permissivi;
      un passo valido esegue create/already_present invece di `manual`.
- [x] Limitazione (a) di B3b-ii chiusa e documentata; recovery resta assegnata a C4.
- [x] Nessuna regressione test/coverage/Compose/web.

## Completion Record

- **Data:** 2026-07-12
- **Riepilogo:** Seconda metà dello split di B3c. Integra il contratto ricco B3c-i
  nella readiness/gate e collega il writer B3b-ii, chiudendo la limitazione (a).
  Nuovo validator puro `domain_contract.verify_contract` che **non si fida della
  stringa `status`**: per un envelope dichiarato `succeeded` ricostruisce i record
  (`_rebuild_records`) e ri-esegue `reconcile` contro l'enumerazione `list_domains`
  persistita, restando eleggibile solo se la re-derivazione indipendente dà
  `succeeded`; `ContractEvaluation` + gap reason stabili e redatti (`absent`,
  `unsupported_version`, `read_failed`, `partial`, `ambiguous`, `unavailable`,
  `incomplete_record`, `incoherent`) + `project_records`. La readiness rende
  `domains` `eligible_for_real_design` solo con contratto valido su **entrambi**
  gli endpoint (gap code `domains_contract_<source|destination>_<reason>`);
  `domains_contract` aggiunto a `EVIDENCE_CATEGORIES`. Il bridge writer
  `_source_domain_records` legge **esclusivamente** `data["domains_contract"]` via
  `verify_contract`/`project_records` (mai `domains_data`/`list_domains`/euristiche)
  e ri-valida a runtime (TOCTOU): contratto invalido → `ConflictError` esplicito
  fail-closed, mai `[]`. Il safety gate resta invariato e riusa il risultato
  readiness evidence-bound (nessuna validazione duplicata). Nessuna modifica a
  planner/comparison (il writer legge la sorgente d'evidenza direttamente).
- **File principali:** `inventory/domain_contract.py` (+`verify_contract`,
  `project_records`, `_rebuild_records`, `ContractEvaluation`, gap reasons),
  `readiness/engine.py` (`_domains_gaps` + `domains_contract` in EVIDENCE),
  `executions/dispatch.py` (`_source_domain_records` bridge fail-closed),
  test `test_real_dispatch.py` / `test_writer_readiness.py` /
  `test_domain_inventory_contract.py`; docs `README.md`, `B3b-ii`, `B3c-ii`,
  `BACKLOG.md`. 6 file code/test (~477 righe, di cui ~326 test mandati) + docs.
- **Test e comandi (tutti PASS):** API **333 passed**; coverage
  `domain_contract.py` **100%**, `readiness/engine.py` 99%, `dispatch.py` 98%;
  adapter **81**; worker **18** (venv con dramatiq 2.2.0); `npm run build` OK;
  `docker compose config -q` OK. Coperti tutti gli scenari obbligatori: source+dest
  succeeded→eligible, source/dest partial, ambiguous/failed/unavailable, assente,
  legacy, versione sconosciuta, succeeded-ma-malformato, succeeded-ma-coverage-
  incoerente, record incompleto addon/subdomain, issue bloccante in succeeded,
  readiness obsoleto, bridge legge `domains_contract`, nessun fallback a
  `domains_data`, invalid→errore esplicito (non lista vuota), valido→DomainRecord
  completi, passo `missing_on_destination` valido non più `manual`, actor reale con
  fake gateway, contratto invalidato dopo dispatch blocca prima della write,
  partial/legacy non raggiunge DestinationWrite, gate blocca readiness non-eligible,
  nessun secret leak, mock/dry-run/collector senza regressioni.
- **Review:** review adversariale autonoma sui rischi richiesti (trust della sola
  stringa `status`, fallback silenziosi, snapshot legacy promossi, schema version
  non verificata, mismatch coverage/payload, TOCTOU readiness↔worker, perdita
  tipo/docroot/label, falso empty/eligible). Un difetto trovato e corretto in fase
  di test: `_rebuild_records` rifiutava la `tuple` di record → `project_records`
  restituiva `[]` silenzioso (falso empty); corretto ad accettare list/tuple e
  coperto. Nessun rilievo residuo.
- **Documentazione:** `README.md` — nuova sezione «Readiness e bridge del contratto
  domini (B3c-ii)», nota B3c-i→B3c-ii aggiornata, sezione worker B3b-ii aggiornata
  (bridge fail-closed, limitazione (a) chiusa); `B3b-ii` limitazione (a) marcata
  CHIUSA; recovery `running` documentata come assegnata a **C4**.
- **Limitazioni residue → C4:** recovery dei tentativi `running` dopo crash del
  worker durante la fase (reconciliation esterna), invariata rispetto a B3b-ii.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```
