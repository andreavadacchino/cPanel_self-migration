# Task B3c-ii: Rich domain readiness integration

| Field | Value |
|---|---|
| **ID** | `B3c-ii` |
| **Status** | `[ ]` |
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

- [ ] Comparison/planner conservano i campi richiesti dal contratto.
- [ ] Readiness eleggibile soltanto su contratto completo su entrambi gli endpoint.
- [ ] Readiness NON eleggibile su partial/failed/legacy.
- [ ] Safety gate blocca un contratto partial (fail-closed).
- [ ] Serializzazione e rilettura dello snapshot conservano il contratto.
- [ ] B3b-ii riceve record ricchi: un passo valido non è più `manual` per assenza
      dell'envelope (end-to-end, gateway fake, nessun server reale).
- [ ] Nessun secret leak; nessuna regressione readiness/comparison/gate/dispatch.

**Acceptance Criteria:**

- [ ] `domains` eleggibile solo su contratto completo e coerente; partial/legacy
      fail-closed.
- [ ] B3b-ii consuma i record persistiti completi senza adattamenti permissivi;
      un passo valido esegue create/already_present invece di `manual`.
- [ ] Limitazione (a) di B3b-ii chiusa e documentata; recovery resta assegnata a C4.
- [ ] Nessuna regressione test/coverage/Compose/web.

**Verification Commands:**

```bash
cd apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```
