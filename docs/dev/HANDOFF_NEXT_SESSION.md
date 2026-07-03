# Prompt di avvio — prossima sessione (cutover giorginisposi P2-P6)

Copia-incolla da qui in giù.

---

Stai lavorando sul tool Go **cpanel-self-migration**, directory
/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration.

Leggi PRIMA: CUTOVER_1_GIORGINISPOSI.md (il diario del cutover in corso),
CUTOVER_RUNBOOK.md (runbook ripetibile con §4.1 Variante C).

## Stato al 2026-07-03

### Cutover #1 — IN CORSO, fermo a P1

| Fase | Stato |
|------|-------|
| P0 Preflight | COMPLETATO — peer standalone, SPF/DKIM verificati |
| GATE 1 | APPROVATO — ordine operativo confermato |
| P1 TTL lowering | **IN ATTESA** — utente deve editare 4 TTL via WHM su .193 |
| P2-P6 | Non iniziati — partono 4h dopo l'edit TTL |

### Decisioni prese

- **Variante C**: peer standalone, `synczone` per-account
- **Primo account**: giorginisposi
- **TTL**: opzione 1 (abbassare adesso, switch quando pronto dopo 4h)

### Check pre-switch già completati

| Check | Risultato |
|-------|-----------|
| SPF su .78 | `ip4:38.224.109.78` (corretto) |
| DKIM su .78 | chiave propria di .78 (piano replace → skipped_v1, corretto) |
| Peer standalone | entrambi confermati (SOA 2026051601) |
| CageFS .78 | disabilitato per giorginisposi |

### Azione utente richiesta per sbloccare P1

Editare su WHM (.193) → DNS Zone Manager → giorginisposi.it:
A apex, CNAME www, MX, TXT/SPF → TTL da 14400 a 300.
NON toccare NS/SOA. Dopo l'edit: serial SOA deve bumpare da 2026051601.

### Prossimi passi (dopo 4h dall'edit TTL)

P2: sync contenuti finale (delta mail/web/db)
P3: apply config binario + verify CLEAN + conferma SPF/DKIM
P4: `dnscluster synczone giorginisposi.it` (LO SWITCH)
P5: .193 attivo → delta maildir → sospensione
P6: documentazione

## Tool state

PR #54 merged. 7 writer binary-proven. Pipeline CLI completa.
Binario: `go build -o /tmp/cpanel-self-migration ./cmd/cpanel-self-migration/`
Config: `configs/host.yaml` (src=.193, dest=.78)

## Workflow (invariato)

SOLO fork (`--repo andreavadacchino/cPanel_self-migration`), mai origin.
TDD. go-reviewer + Docker. runner.go off-limits.
Peer NS standalone verificato ATTIVAMENTE prima di write DNS.

## Regole assolute

- Mai removeacct/killdns. Mai toccare ruoli peer o useclusteringdns.
- Mai ripristinare sync (Variante C — standalone per tutta la campagna).
- Zona produzione toccabile SOLO: TTL (P1) e synczone (P4).
- Zone di TUTTI gli altri account INTOCCABILI.
- Su .193: letture + delta sync + sospensione — nient'altro.
