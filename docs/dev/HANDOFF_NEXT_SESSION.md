# Prompt di avvio — prossima sessione (dopo PR #53)

Copia-incolla da qui in giù.

---

Stai lavorando sul tool Go **cpanel-self-migration**, directory
/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration.

Leggi PRIMA: DEVELOPMENT_STATE.md (#53), CUTOVER_RUNBOOK.md (runbook
ripetibile), HANDOFF qui sotto.

## Stato al 2026-07-03

**TUTTI i writer primitives sono LIVE-PROVEN** (session smoke-total):

| Writer | Primitiva | Live smoke |
|--------|-----------|-----------|
| Forwarder | AddForwarder | ✅ LIVE + rollback (#47) |
| Default address | SetDefaultAddress | ✅ LIVE (#47) |
| Autoresponder | AddAutoresponder | ✅ LIVE + rollback (#49) |
| Filter | StoreFilter/DeleteFilter | ✅ smoke-total |
| Routing | SetMXCheck (RunAPI2) | ✅ smoke-total |
| Cron | InstallCrontab | ✅ smoke-total |
| DNS | MassEditZoneAdd/Remove | ✅ smoke-total + non-propagation |

### Residui minori (non bloccanti)
- `fwdopt=fail/blackhole` non byte-verificati
- `is_html=1`, `start/stop` espliciti mai live
- Routing baseline era `auto` (non `local` come nei doc precedenti)

### Command file mancanti
I CLI subcommand per `dns apply`, `cron apply` e le sezioni
filtri/routing di `email apply` NON esistono ancora. Le primitive e
la logica di evaluate/rollback sono nel cpanel e accountinventory layer.
Il wiring CLI (flag, backup, report) è remaining work. Per giorginisposi
(singolo account) il throwaway è sufficiente.

## Obiettivo prossima sessione

**Primo cutover reale** — GATED sulle decisioni utente:

| Decisione | Stato |
|-----------|-------|
| Data campagna | **APERTA** |
| Ripristino ruolo sync DNS (variante A o B) | **APERTA** |
| Runbook CageFS per-account | Documentato in CUTOVER_RUNBOOK.md |

Senza queste decisioni, si può lavorare sui command file mancanti
(dns apply CLI, cron apply CLI, filtri/routing nel email apply CLI).

## Workflow (invariato)

SOLO fork, mai origin. TDD. go-reviewer + Docker. runner.go off-limits.
Peer NS standalone verificato ATTIVAMENTE prima di write DNS.
