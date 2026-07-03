# Prompt di avvio — prossima sessione di sviluppo (dopo PR #52)

Copia-incolla da qui in giù.

---

Stai lavorando sul tool Go **cpanel-self-migration**, directory
/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration.

Leggi PRIMA:
1. docs/dev/DEVELOPMENT_STATE.md — roadmap (ultimo merge: #52)
2. docs/dev/PR6A_DNS_IMPORT_DESIGN.md + PR6D_DNS_APPLY_DESIGN.md — design DNS
3. docs/dev/PR6D_PRE_CAPTURES.md — fatti DNS byte-verificati
4. docs/dev/CPAPI2_DIAGNOSIS_78.md — cpapi2 risolto (CageFS disable)
5. docs/dev/FASE0_2_FIRST_APPLY.md — regole cluster DNS

## Stato al 2026-07-03

**Tutti i writer sono implementati**: email (forwarder, autoresponder,
filtri, routing), cron, DNS. Il tool ha il codepath completo per la
migrazione per-account.

### Writer implementati

| Writer | PR | Live smoke | Debito |
|--------|-----|-----------|--------|
| Forwarder (add) | #47 | ✅ LIVE | — |
| Default address (set) | #47 | ✅ LIVE | fwdopt=fail/blackhole non byte-verificati |
| Autoresponder (create) | #49 | ✅ LIVE + rollback LIVE | is_html=1/start-stop mai live |
| Filter (store) | #50 | Primitive byte-verificate | Write-path mai end-to-end (0 filtri su source) |
| Routing (setmxcheck) | #50 | Primitiva CLI verificata (root) | Tool codepath mai esercitato (routing skip) |
| Cron (crontab -) | #51 | Primitive byte-verificate | Write-path mai end-to-end (0 cron su source) |
| DNS (mass_edit_zone) | #52 | Primitive byte-verificate | Smoke end-to-end nella prossima sessione |

### Infra

- cpapi2 FUNZIONANTE (CageFS disabled per giorginisposi su .78)
- Peer NS standalone (entrambi: 136.144.242.119, 185.17.106.73)
- host.yaml VALIDO per entrambi i lati
- AutoSSL escluso, zona produzione intatta

## Obiettivo prossima sessione

### Opzione A — Smoke DNS end-to-end + primo cutover

Se l'utente è pronto per il cutover:
1. Smoke DNS end-to-end: plan → apply (add su zona sacrificale) →
   dns verify --fail-on-drift → rollback LIVE → verify = pre-smoke
2. Primo cutover reale (Fase 3): switch DNS di giorginisposi.it
   da .193 a .78 (richiede ripristino ruolo sync del peer DNS)

### Opzione B — Smoke DNS + hardening

Se non pronto per il cutover:
1. Smoke DNS end-to-end (come sopra)
2. Hardening residui: fwdopt=fail/blackhole, is_html/start-stop

## Decisioni aperte (utente)

1. **Data campagna**: quando iniziare il cutover reale
2. **Ripristino ruolo sync DNS**: il peer NS è standalone — al cutover
   serve sync per propagare le zone. Quando ripristinare?
3. **Runbook CageFS**: disabilitare CageFS per ogni account migrato
   durante la finestra di migrazione (per cpapi2 setmxcheck)

## Workflow (invariato)

SOLO fork andreavadacchino/…, mai origin. TDD, go-reviewer, Docker.
runner.go off-limits. Peer NS verificato ATTIVAMENTE prima di write.
Mai removeacct/killdns. Mai toccare zona produzione.
