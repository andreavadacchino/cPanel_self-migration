# Task B3c-i: Domain inventory contract (collector)

| Field | Value |
|---|---|
| **ID** | `B3c-i` |
| **Status** | `[x]` |
| **Priority** | High |
| **Size** | M |
| **Dependencies** | B3b-ii |
| **Branch** | `feat/b3c-i-domain-inventory-contract` |

**Origin:** prima metà dello split di `B3c` (vedi
[B3c-rich-domain-inventory.md](B3c-rich-domain-inventory.md)). B3c-i produce e
persiste soltanto l'evidenza; l'integrazione readiness/gate e la prova
end-to-end sul writer sono `B3c-ii`.

**Goal:** Il collector produce e persiste nello snapshot l'envelope ricco
`domains_data` (via il contratto B3a `read_domains`/`parse_domains_data`) con uno
schema versionato e uno stato di riconciliazione fail-closed rispetto a
`DomainInfo::list_domains`, senza mai inventare valori e senza trasformare una
failure in elenco vuoto. Nessuna modifica a readiness, safety gate o writer.

**Scope:**

- `apps/api/app/modules/inventory/domain_contract.py` (nuovo, puro) +
  `apps/api/app/modules/inventory/collector.py` — `_collect_domains_contract`:
  dopo l'enumerazione `list_domains`, legge `DomainInfo::domains_data` (SafeRead)
  via l'adapter B3a, persiste l'envelope sotto la chiave dedicata
  `data["domains_contract"]` (**non** `domains_data`, che il writer
  `_source_domain_records` interpreta nella shape grezza) — schema `version`
  esplicito, record con dominio normalizzato + spelling raw + tipo + docroot +
  internal label + parent + ownership + metodo/provenienza, `null` + issue per i
  campi non verificabili — e una coverage entry `domains_contract` con stato
  `succeeded`/`partial`/`ambiguous`/`failed`/`unavailable`.
- `apps/api/app/tests/test_domain_inventory_contract.py` — unit test deterministici
  con client fake (nessun server reale, nessun writer invocato).
- `migration-platform/README.md` — tabella preflight domini, schema `domains_data`,
  significato `succeeded`/`partial`/`ambiguous`, compatibilità snapshot legacy.

**Implementation:**

1. Enumerazione invariata: `list_domains` resta la fonte dell'insieme atteso e
   della classificazione dei tipi.
2. Dettaglio via `DomainInfo::domains_data` (SafeRead), parsing con il B3a
   `parse_domains_data` in `DomainRecord`; nessuna re-implementazione del parsing.
3. Riconciliazione `list_domains` ↔ `domains_data`:
   - enumerato ma senza dettaglio → `partial` (record marcato non eleggibile);
   - dettaglio non enumerato → `ambiguous`/review;
   - duplicato o tipo conflittuale → `partial`/`ambiguous`;
   - docroot o internal label richiesti ma non verificabili → record non eleggibile.
4. Fail-closed: `domains_data` fallita/malformata → `unavailable`/`partial`, mai
   `[]`; legacy senza envelope → stato esplicito, non «vuoto verificato».
5. Persistenza con `version` di schema esplicita per la compatibilità futura.

**Absolute constraints:**

- Nessuna modifica a readiness, safety gate, planner, comparison, dispatch o writer.
- Solo `SafeRead`: la sorgente resta strutturalmente read-only.
- Nessun secret in `domains_data`, coverage, eventi o log.
- Nessuna nuova write/retry/dispatch/SSH/altra categoria.

**Testing Requirements:**

- [x] Account con main/addon/subdomain/alias completi → contratto `succeeded`.
- [x] Risultato cPanel moderno e legacy entrambi parsati.
- [x] `list_domains` e `domains_data` coerenti → `succeeded`.
- [x] Dominio enumerato ma dettaglio mancante → `partial`.
- [x] Dettaglio inatteso (non enumerato) → `ambiguous`/review.
- [x] Duplicati o tipi conflittuali → `ambiguous`.
- [x] Docroot mancante quando necessaria → record non eleggibile (`partial`).
- [x] Internal label mancante quando necessaria → record non eleggibile.
- [x] Parent domain incoerente → non eleggibile.
- [x] Chiamata `domains_data` fallita → `failed`, mai empty.
- [x] Risposta malformata → fail-closed (`failed`), mai empty.
- [x] Snapshot legacy senza envelope → `legacy`, non «vuoto».
- [x] Nessun writer invocato dal collector; nessun secret leak.
- [x] Collector/comparison esistenti senza regressioni.

**Acceptance Criteria:**

- [x] Envelope persistito con schema versionato e record ricchi fail-closed.
- [x] Riconciliazione `succeeded`/`partial`/`ambiguous`/`failed`/`unavailable`
      corretta e senza valori inventati.
- [x] Nessuna regressione test/coverage/Compose/web.

**Verification Commands:**

```bash
cd packages/adapters && python -m pytest
cd ../../apps/api && PYTHONPATH=../../packages/adapters python -m pytest
cd ../worker && DRAMATIQ_TESTING=1 python -m pytest
cd ../web && npm run build
cd ../.. && docker compose config -q
```

## Completion Record

- **Data:** 2026-07-12
- **Riepilogo:** Prima metà dello split di B3c. Nuovo modulo puro
  `inventory/domain_contract.py` (`enumerated_types`, `enumeration_issues`,
  `reconcile`, `read_contract`) che riconcilia l'enumerazione `list_domains` col
  dettaglio `DomainInfo::domains_data` (letto via il SafeRead B3a `read_domains`)
  in un envelope versionato (`version=1`) con record ricchi (normalized/raw/type/
  docroot/internal_label/parent/account/method/complete/issues) e stato
  `succeeded`/`partial`/`ambiguous`/`failed`/`unavailable`, **senza mai inventare
  valori** (campo non verificabile = `null` + issue esplicita) e **senza mai
  trasformare una failure in elenco vuoto**. Il collector (`_collect_domains_contract`)
  invoca la riconciliazione e persiste l'envelope sotto la chiave dedicata
  `data["domains_contract"]`. Readiness, safety gate, planner e writer **non**
  toccati: la categoria `domains` resta `not_ready` (verificato da test).
- **File principali:** `inventory/domain_contract.py` (nuovo), `inventory/collector.py`
  (+`_collect_domains_contract`), `tests/test_domain_inventory_contract.py` (nuovo,
  28 test), `README.md`, più i doc di split (`B3c`/`B3c-i`/`B3c-ii`) e `BACKLOG.md`.
  8 file. Diff ~960 righe: la produzione pura è ~290 (collector 50 + logica modulo
  ~170 + README 70); l'eccedenza oltre 500 è **28 test di sicurezza mandati** (~320)
  + **doc di pianificazione dello split** (~280, richiesti nel commit) — non
  sacrificabili.
- **Test e comandi (tutti PASS):** API **302 passed**, coverage
  `domain_contract.py` **100%** (nessuna regressione vs baseline); adapter **81**;
  worker **18**; `npm run build` OK; `docker compose config -q` OK. Coperti tutti
  gli scenari obbligatori (tipi completi, moderno/legacy, coerente→succeeded,
  enumerato-senza-dettaglio→partial, dettaglio-non-enumerato/duplicati/tipo→
  ambiguous, docroot/label/parent mancanti→non-eleggibile, failure totale→failed
  mai-empty, malformato→failed, legacy→legacy, versione/stato ignoti→failed,
  serializzazione deterministica/round-trip, IDN/case, nome invalido, no-write,
  no-secret, non-eleggibilità del writer).
- **Review:** review adversariale indipendente (python-reviewer) → 1 **Critical** +
  1 **High** + 2 **Medium** risolti:
  1. Critical — l'envelope era persistito sotto `data["domains_data"]`, la stessa
     chiave che `dispatch._source_domain_records` (writer B3b-ii) legge nella shape
     grezza via `parse_domains_data`: sull'envelope ritornava `[]` **silenziosamente**
     (false-empty latente + docstring del writer non più vero). **Fix:** envelope
     persistito sotto la chiave dedicata `data["domains_contract"]`
     (`domain_contract.SNAPSHOT_KEY`); `data["domains_data"]` resta assente e il
     writer continua a fermarsi in `halted` (limitazione (a) invariata, chiusa da
     B3c-ii che farà il bridge). Test `test_does_not_collide_with_writer_raw_domains_data_key`.
  2. High — `read_contract` fidava lo `status` ma svuotava silenziosamente
     `records` se non-lista (corruzione). **Fix:** `records` non-lista → `failed`.
     Test `test_read_contract_corrupt_records_is_failed_not_silently_empty`.
  3. Medium — inconsistenze interne dell'enumerazione (nome non parseabile, stesso
     nome in più sezioni con tipo diverso) passavano silenziose. **Fix:**
     `enumeration_issues` le rileva e degrada a `ambiguous`. Test dedicati.
  4. Medium — un `list_domains` con shape non-dict veniva classificato «empty».
     **Fix:** enumerazione non-dict → `unavailable`.
- **Documentazione:** `README.md` — tabella preflight (riga «Contratto domini»),
  schema `domains_contract` versionato, significato succeeded/partial/ambiguous/
  failed/unavailable, compatibilità legacy, nota che B3c-i **non** chiude ancora la
  limitazione (a) di B3b-ii (compito di B3c-ii) e recovery → C4.
- **Limitazioni residue → B3c-ii:** readiness eligibility su contratto completo
  coerente, bridge writer (`_source_domain_records` → `domains_contract`), prova
  end-to-end che un passo dominio valido non è più `manual`. La limitazione (a) di
  B3b-ii resta aperta fino a B3c-ii; recovery tentativi `running` → C4.
