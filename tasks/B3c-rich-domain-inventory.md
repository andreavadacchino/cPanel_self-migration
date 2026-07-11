# Task B3c: Rich domain inventory contract (split parent)

| Field | Value |
|---|---|
| **ID** | `B3c` (ritirato — suddiviso) |
| **Status** | `[/]` split |
| **Priority** | High |
| **Size** | L (misurato) |
| **Dependencies** | B3b-ii |

**Motivazione:** B3b-ii è correttamente cablato, ma l'inventario persistito
contiene soltanto `DomainInfo::list_domains` (lista nomi). Il writer reale
(`real_domain_writer.py`, via `_source_domain_records`) richiede l'envelope ricco
`domains_data` con tipo, docroot e internal label; oggi quindi ogni passo dominio
reale finisce `manual/pending` e nessun dominio può essere creato. B3c rimuove
questo blocco **senza modificare il writer** — è la limitazione residua (a) di
B3b-ii. La limitazione crash/recovery resta assegnata a **C4** (non a B3c).

**Obiettivo:** Persistire negli inventory snapshot un contratto domini ricco,
completo e fail-closed, consumabile direttamente da B3b-ii senza inventare tipo,
docroot, owner o internal label, e rendere la categoria `domains` eleggibile alla
scrittura reale solo quando il contratto è completo e coerente su entrambi gli
endpoint.

## Split (guardrail 8 file / 500 righe)

Stima a task unico ≈ 580 righe / 8–9 file (collector ~120, readiness ~6, ~20
test obbligatori ~300, README ~40, task/backlog ~110), oltre entrambi i limiti e
guidata dai test di sicurezza mandati (non sacrificabili). Suddiviso in due
sotto-task lungo il confine **produci l'evidenza** / **fidati dell'evidenza**:

- **[B3c-i](B3c-i-domain-inventory-contract.md) — Domain inventory contract
  (collector):** il collector produce e persiste l'envelope ricco `domains_data`
  con schema versionato + stato di riconciliazione (`succeeded`/`partial`/
  `ambiguous`) rispetto a `list_domains`, fail-closed (una failure non diventa
  elenco vuoto), sorgente strettamente read-only (solo `SafeRead`), compatibile
  con gli snapshot legacy. **Non** tocca readiness, gate o writer.
- **[B3c-ii](B3c-ii-domain-readiness-integration.md) — Rich domain readiness
  integration:** readiness rende `domains` `eligible_for_real_design` solo con
  contratto `succeeded` su **entrambi** gli endpoint (partial/failed/legacy →
  non eleggibili, gate fail-closed invariato); verifica che B3b-ii consumi il
  contratto senza adattamenti permissivi; prova end-to-end che un passo dominio
  valido non è più `manual` per assenza dell'envelope. **Chiude la limitazione
  (a) di B3b-ii** (solo qui, quando il writer riceve davvero record completi).

Gli ID `B3c` non viene riusato per implementazione: il lavoro vive in `B3c-i`
(dip. `B3b-ii`) e `B3c-ii` (dip. `B3c-i`). Le categorie downstream
(`B4`/`B5`/`B6`/`B7`/`C1`) dipendono da **`B3c-ii`** (contratto integrato e
consumato), non più da `B3b-ii`.

## Requisiti trasversali (ripartizione)

- **B3c-i** (produce l'evidenza): enumerazione `list_domains` invariata; dettaglio
  via `DomainInfo::domains_data` SafeRead (adapter B3a); envelope `domains_data`
  versionato con record ricchi (dominio normalizzato, raw, tipo, docroot, internal
  label, parent, ownership, metodo/provenienza, completezza/issue); nessun valore
  inventato (`null` + issue); riconciliazione list↔detail
  (partial/ambiguous/failed/unavailable) fail-closed, mai elenco vuoto; schema
  versionato + legacy leggibile non promosso.
- **B3c-ii** (fidati dell'evidenza): readiness `eligible_for_real_design` solo su
  contratto `succeeded` coerente su source+destination (partial/failed/legacy non
  eleggibili, gate fail-closed); `_source_domain_records` consuma lo snapshot senza
  adattamenti permissivi; propagazione planner/preview solo se necessaria; prova
  end-to-end che un passo dominio valido non è più `manual`.
- **Entrambi**: nessuna nuova write/retry/dispatch/SSH/altra categoria; nessun
  server reale nei test; sorgente read-only (solo `SafeRead`); nessun secret leak.

## Documentazione (ripartita)

README: tabella preflight domini + schema `domains_data` + significato
`succeeded`/`partial`/`ambiguous` + compatibilità legacy *(B3c-i)*; rimozione
della limitazione (a) di B3b-ii solo quando dimostrata dai test, mantenendo
documentata la limitazione recovery → C4 *(B3c-ii)*.
