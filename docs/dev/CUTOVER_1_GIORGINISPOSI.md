# Cutover #1 — giorginisposi.it (.193 → .78)

Data inizio preflight: 2026-07-03. **Cutover RIMANDATO** per decisione
utente (sessione 2026-07-04). Lo snapshot DNS pre-switch e il riesame
ordine-MX restano validi come evidenza per quando si eseguirà il cutover.

## Decisioni utente (vincolanti)

| Decisione | Scelta |
|-----------|--------|
| Variante ruolo sync DNS | **C** — peer standalone per tutta la campagna, `synczone` per-account |
| Primo account | giorginisposi (l'unico interamente provato) |
| TTL strategy | Opzione 1 — abbassare adesso, switch quando pronto dopo 4h |

## Tool state (PR #54, merged 2026-07-03)

Tutti i 7 writer sono binary-proven (esercitati end-to-end attraverso il
binario compilato contro .78):

| Command | Writer | Binary smoke |
|---------|--------|-------------|
| `email apply` | Forwarder, Default, Autoresponder, Filter, Routing | PASS |
| `email verify` | tutte e 5 le sezioni | PASS |
| `dns apply` | MassEditZoneAdd (v1: solo add) | PASS + rollback + non-propagazione |
| `dns verify` | per-op verify + fail-on-drift | PASS |
| `cron apply` | InstallCrontab (merge + install) | PASS + rollback |
| `cron verify` | per-line verify | PASS |
| `inventory cron-plan` | piano offline | PASS |

Pipeline completa: `--account-inventory` → `inventory *-plan` → `* apply`
→ `* verify --fail-on-drift` → `dnscluster synczone` → rollback pronto.

### Residui non bloccanti (aggiornati post #56)

- `replace` DNS ops: **v2 implementato e binary-proven** (PR #56, smoke 2026-07-04)
- `fwdopt=fail/blackhole`, `is_html=1`, `start/stop`: mai byte-verificati
- `synczone`: mai eseguito live (il primo uso reale = lo switch P4)

## P0 — Preflight (completato 2026-07-03 ~15:10 UTC)

### Peer standalone — verificato

| Peer | IP | SOA serial | Risultato |
|------|----|------------|-----------|
| ns.hostnuoviclienti.com | 136.144.242.119 | 2026051601 | standalone confermato |
| ns.hostnuoviclienti.net | 185.17.106.73 | 2026051601 | standalone confermato |

### DNS produzione snapshot (NS pubblici, pre-cutover)

| Record | Valore | TTL |
|--------|--------|-----|
| A apex | 194.76.118.193 | 14400 (4h) |
| CNAME www | giorginisposi.it. | 14400 |
| MX | 0 giorginisposi.it. | 14400 |
| TXT/SPF | `v=spf1 +a +mx +ip4:194.76.118.193 ~all` | 14400 |
| DKIM default._domainkey | chiave RSA 2048 (.193) | — |
| DMARC _dmarc | **assente** | — |
| NS | 3x hostnuoviclienti.{com,net,org} | 86400 (24h) |
| SOA serial | 2026051601 | 86400 |

### Check pre-switch (SPF/DKIM)

| Check | Risultato | Dettaglio |
|-------|-----------|-----------|
| SPF su .78 | CORRETTO | `ip4:38.224.109.78` (ip-map tradotta, piano = skip) |
| DKIM su .78 | CORRETTO | chiave propria di .78 (diversa da .193, piano = replace → skipped_v1) |
| DMARC | assente | proposta: `p=none` post-cutover (decisione utente, non bloccante) |

### CageFS .78

Disabilitato per giorginisposi (confermato: binary smoke SetMXCheck via
cpapi2 ha funzionato).

### synczone read-only

`/usr/local/cpanel/scripts/dnscluster synczone <zone>` — esistenza
confermata via `--help`. Azioni disponibili: `synczone` (push a tutti i
peer), `synczonelocal` (pull al server locale). Script root-level.
Mai eseguito live.

### Riesame adversariale runbook sotto Variante C

**Problema identificato**: ordine switch/sospensione vs finestra MX.
Con TTL A/MX a 14400s (4h), dopo synczone i resolver consegnano ancora
mail a .193 per un massimo di 4h. Sospensione immediata → mail BOUNCE.

**Ordine proposto e approvato**:
1. P1: TTL 14400→300s (WHM su .193)
2. Attesa 4h (scarico cache)
3. P2: Sync contenuti finale
4. P3: Apply config + verify CLEAN
5. P4: synczone (lo switch)
6. P5: .193 ATTIVO durante propagazione (300s = 5min finestra)
7. Delta maildir finale da .193
8. Sospensione .193

**Rischio residuo con TTL 300s**: finestra 5 minuti, catturabile con
delta maildir finale. Rischio trascurabile.

**Rischio senza abbassamento**: finestra 4h. Inaccettabile per la mail.

### GATE 1 — APPROVATO

Ordine operativo confermato. Decisione TTL: opzione 1 (abbassare adesso).

## P1 — Abbassamento TTL (RIMANDATO)

**Stato: RIMANDATO** — per decisione utente, il cutover non è in programma
in questa sessione. Le informazioni sotto restano valide per quando si
eseguirà P1.

Record da editare su WHM (.193) → DNS Zone Manager → giorginisposi.it:

| # | Tipo | Name | TTL attuale → nuovo |
|---|------|------|---------------------|
| 1 | A | giorginisposi.it. | 14400 → **300** |
| 2 | CNAME | www.giorginisposi.it. | 14400 → **300** |
| 3 | MX | giorginisposi.it. | 14400 → **300** |
| 4 | TXT | giorginisposi.it. (SPF) | 14400 → **300** |

NON toccare: NS (86400), SOA (86400) — i nameserver non cambiano.

**Post-edit**: verificare bump serial SOA da 2026051601 su NS pubblici.
**Timer**: lo switch (P4) non prima di 4h dopo l'edit.

**Stato**: in attesa dell'edit manuale dell'utente.

## P2-P6 — Da eseguire dopo il timer TTL

Documentazione in tempo reale durante l'esecuzione.

### P2 — Sync contenuti finale
Delta mail/web/db verso .78 col migrate flow. Report del delta.

### P3 — Apply config dal binario
```bash
# Email (forwarder/default/autoresponder/filtri/routing)
cpanel-self-migration email apply --plan email_apply_plan.json --yes-apply-writes --config host.yaml
cpanel-self-migration email verify --plan email_apply_plan.json --config host.yaml --fail-on-drift

# Cron
cpanel-self-migration cron apply --plan cron_apply_plan.json --yes-apply-writes --config host.yaml
cpanel-self-migration cron verify --plan cron_apply_plan.json --config host.yaml --fail-on-drift

# DNS (zona su .78)
cpanel-self-migration dns apply --plan dns_import_plan.json --yes-apply-writes --config host.yaml
cpanel-self-migration dns verify --plan dns_import_plan.json --config host.yaml --fail-on-drift
```
Tutto deve essere CLEAN.

### P4 — Lo switch (synczone strumentato)
```bash
# (a) Capture pre-switch (dig NS pubblico)
dig @ns.hostnuoviclienti.com giorginisposi.it A MX TXT SOA +short

# (b) Backup raw zona su .78 (già nel dns apply backup)

# (c) synczone (root su .78)
/usr/local/cpanel/scripts/dnscluster synczone giorginisposi.it

# (d) Dig-verify immediata
dig @ns.hostnuoviclienti.com giorginisposi.it A +short  # deve essere IP .78
dig @ns.hostnuoviclienti.com giorginisposi.it MX +short

# (e) Curl + test mail
curl --resolve giorginisposi.it:443:38.224.109.78 https://giorginisposi.it/
```
Se dig-verify diverge → ROLLBACK (repush backup + synczone).

### P5 — Coda post-propagazione
Monitor propagazione (1.1.1.1, 8.8.8.8). Delta maildir finale.
Sospensione .193: `whmapi1 suspendacct user=giorginisposi reason="Migrated to .78"`.

### P6 — Documentazione
Questo file completato con timeline reale e lezioni apprese.
