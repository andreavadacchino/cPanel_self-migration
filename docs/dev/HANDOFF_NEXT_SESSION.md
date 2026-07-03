# Prompt di avvio — prossima sessione (dopo PR cli-wiring-binary-smoke)

Copia-incolla da qui in giù.

---

Stai lavorando sul tool Go **cpanel-self-migration**, directory
/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration.

Leggi PRIMA: DEVELOPMENT_STATE.md, CUTOVER_RUNBOOK.md (runbook
ripetibile con §4.1 aggiornato), HANDOFF qui sotto.

## Stato al 2026-07-03

**TUTTI i writer sono BINARY-PROVEN** (esercitati end-to-end attraverso
il binario compilato contro .78, non più solo tramite harness throwaway):

| Writer | CLI command | Binary smoke |
|--------|-------------|-------------|
| DNS | `dns apply` + `dns verify` | apply→CLEAN→rollback→pending + non-propagazione peer |
| Routing | `email apply` (SetMXCheck) | auto→local→CLEAN→rollback(auto) |
| Filter | `email apply` (StoreFilter/DeleteFilter) | apply→CLEAN→rollback→0 filtri |
| Cron | `cron apply` + `cron verify` | apply→CLEAN→rollback→crontab vuoto |
| Forwarder | `email apply` (AddForwarder) | LIVE + rollback (#47) |
| Default addr | `email apply` (SetDefaultAddress) | LIVE (#47) |
| Autoresponder | `email apply` (AddAutoresponder) | LIVE + rollback (#49) |

### Scoperta Passo 4: per-zone sync ESISTE

Il claim "cPanel non supporta sync per-zona" è stato **SMENTITO**.
`/usr/local/cpanel/scripts/dnscluster synczone <zone>` propaga UNA
singola zona a tutti i peer del cluster (script root-level).
Variante C aggiunta al runbook §4.1 — richiede byte-verify di
`synczone` prima del primo cutover reale.

### Residui minori (non bloccanti)
- `fwdopt=fail/blackhole` non byte-verificati
- `is_html=1`, `start/stop` espliciti mai live
- `replace` DNS ops (v1: skipped, futuro PR)
- `synczone` non ancora byte-verificato live (solo help/esistenza)

## Obiettivo prossima sessione

**Primo cutover reale** — GATED su:

| Decisione | Stato |
|-----------|-------|
| Data campagna | **APERTA** |
| Variante ruolo sync DNS (A/B/C) | **APERTA** — dato Variante C disponibile |
| Ordine account | **APERTA** (suggerimento: giorginisposi primo) |
| Byte-verify `synczone` | **RICHIESTO** se Variante C scelta |

## Workflow (invariato)

SOLO fork, mai origin. TDD. go-reviewer + Docker. runner.go off-limits.
Peer NS standalone verificato ATTIVAMENTE prima di write DNS.
