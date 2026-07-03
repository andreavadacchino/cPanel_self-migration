# Runbook di cutover per-account — cpanel-self-migration

Sequenza ripetibile per ogni account della campagna di migrazione
.193 → .78. Ogni passo ha pre-condizioni e rollback. Il runbook è un
DOCUMENTO — nessun cutover reale senza le decisioni utente (§7).

## 1. Pre-check (T-48h)

### 1.1 Inventario fresco
```bash
cpanel-self-migration --account-inventory --config host.yaml
```
Produce `inventory_source.json` + `inventory_destination.json`.

### 1.2 Pipeline offline
```bash
cpanel-self-migration inventory diff
cpanel-self-migration inventory policy --fail-on-blockers
cpanel-self-migration inventory email-plan --source ... --destination ...
cpanel-self-migration inventory dns-plan --source ... --destination ... --ip-map <old>=<new>
cpanel-self-migration inventory checklist
```
Revisione manuale del checklist: tutti i blockers risolti, le azioni
manuali accettate (via `--acceptances`).

### 1.3 TTL abbassamento (T-48h)
Abbassare i TTL dei record A/AAAA/CNAME/MX della zona di produzione
a **300s** (5 min) almeno **48h prima** del cutover — il TTL originale
(tipicamente 14400s = 4h) deve scadere ovunque prima dello switch.
⚠️ Questo richiede accesso alla zona di PRODUZIONE su .193 (WHM o
file di zona). Il tool NON lo fa automaticamente.

**Rollback**: ripristinare i TTL originali.

## 2. Preparazione destinazione (T-4h)

### 2.1 Creazione account
```bash
# Manovra useclusteringdns (FASE0_2_FIRST_APPLY.md):
# 1. mv /var/cpanel/useclusteringdns /var/cpanel/useclusteringdns.bak
# 2. createacct <user> <domain> ...
# 3. mv /var/cpanel/useclusteringdns.bak /var/cpanel/useclusteringdns
```
⚠️ Finestra di ~30s senza clustering. Se il server ha altri account
in creazione contemporanea, coordinare.

### 2.2 CageFS eccezione
```bash
cagefsctl --disable <user>
```
Necessario per cpapi2 (setmxcheck). Reversibile con `--enable`.
Decisione registrata: runbook, non HTTP API.

### 2.3 Migrazione contenuti
```bash
cpanel-self-migration --apply --config host.yaml --yes-apply-writes
```
Migra mail, web files, database. Verificare il report.

**Rollback**: removeacct su .78 (⚠️ SOLO se il clustering è
momentaneamente disabilitato — altrimenti la zona viene cancellata
dalla produzione! Usare la stessa manovra useclusteringdns).

## 3. Apply configurazione

### 3.1 Email apply
```bash
cpanel-self-migration email apply --plan email_apply_plan.json --config host.yaml --yes-apply-writes
cpanel-self-migration email verify --plan email_apply_plan.json --config host.yaml --fail-on-drift
```
**Rollback**: `email apply --rollback <backup-file>`

### 3.2 Cron apply
```bash
# (command file non ancora implementato — usare le primitive Go
# o installare manualmente i cron job dall'inventario)
```
**Rollback**: `crontab -` con il crontab di backup.

### 3.3 DNS apply
```bash
# (command file non ancora implementato — usare le primitive Go
# MassEditZoneAdd con il piano dns_import_plan.json)
cpanel-self-migration dns verify --plan dns_import_plan.json --config host.yaml --fail-on-drift
```
**Rollback**: `MassEditZoneRemove` con i line_index ri-risolti.

## 4. Switch DNS (il punto di non ritorno soft)

### 4.1 ⚠️ DECISIONE UTENTE RICHIESTA: ruolo sync del peer DNS

Il peer NS (`136.144.242.119`, `185.17.106.73`) è attualmente in ruolo
**standalone**. Per propagare le zone modificate su .78 ai NS pubblici,
il ruolo deve tornare a **sync**.

**Variante A — sync PRIMA dello switch**:
- Pro: le zone su .78 si propagano automaticamente ai NS; il DNS
  switch è trasparente per i resolver.
- Contro: TUTTE le zone su .78 vengono sincronizzate (non solo quella
  dell'account migrato). Se ci sono zone di test/spazzatura su .78,
  vanno pulite prima.
- Rischio: una zona di produzione su .193 (non ancora migrata) potrebbe
  essere sovrascritta se ha una copia stale su .78.

**Variante B — sync DOPO lo switch per-account**:
- Pro: controllo per-account; solo le zone pronte vengono propagate.
- Contro: richiede un meccanismo per propagare selettivamente (cPanel
  non supporta sync per-zona — è tutto o niente).
- Rischio: la finestra tra il DNS switch e la propagazione lascia i
  resolver con i vecchi record.

**Raccomandazione dell'operatore**: variante A con pulizia preventiva
delle zone su .78 (rimuovere zone di test, verificare che ogni zona su
.78 sia allineata o assente). Ma la decisione è dell'utente.

### 4.2 Sospensione account su .193
```bash
whmapi1 suspendacct user=<user> reason="Migrated to .78"
```
⚠️ NON `removeacct` (distruggerebbe la zona di produzione nel cluster).

**Rollback**: `whmapi1 unsuspendacct user=<user>`

### 4.3 Verifiche post-switch
1. `dig @<NS pubblico> <domain> A` → IP di .78
2. `curl --resolve <domain>:443:<IP .78> https://<domain>/` → sito vivo
3. Test email: inviare + ricevere un messaggio
4. AutoSSL: `uapi SSL run_autossl_check` su .78

## 5. Rollback d'emergenza

### Criteri go/no-go (entro 1h dallo switch)
- Sito non raggiungibile da .78 → rollback
- Email non funzionante → rollback
- Errori AutoSSL non risolvibili → rollback

### Procedura rollback
1. Unsuspend su .193: `whmapi1 unsuspendacct user=<user>`
2. Ripristinare TTL originali sulla zona di produzione
3. Se sync attivato: ripristinare standalone
4. DNS apply rollback (remove le righe aggiunte)
5. Email apply rollback (--rollback <backup>)
6. Crontab restore dal backup

## 6. Post-cutover (T+24h)

1. Verificare che tutti i DNS si siano propagati (`dig` da più
   resolver pubblici)
2. Ripristinare i TTL originali (da 300s ai valori precedenti)
3. Monitorare email/sito per 48h
4. Solo dopo 48h puliti: considerare `removeacct` su .193 con la
   manovra useclusteringdns

## 7. Decisioni aperte (placeholder)

| Decisione | Stato | Chi decide |
|-----------|-------|-----------|
| Data di partenza campagna | **APERTA** | Utente |
| Finestra di cutover (orario) | **APERTA** | Utente |
| Ripristino ruolo sync DNS | **APERTA** — variante A o B | Utente |
| Ordine degli account | **APERTA** | Utente (suggerimento: giorginisposi primo, gli altri dopo conferma) |
| Pulizia zone spazzatura su .78 | **APERTA** | Utente |

## 8. Command file mancanti (work rimanente)

| Command | Stato | Workaround |
|---------|-------|-----------|
| `dns apply` | Primitive implementate, CLI mancante | Throwaway harness |
| `cron apply` | Primitive implementate, CLI mancante | Throwaway harness |
| `email apply` filtri/routing | Primitive implementate, CLI mancante | Throwaway harness |

Questi command file sono wiring (dispatch + flag + backup management)
attorno alle primitive già provate live. La priorità dipende dal numero
di account della campagna: per giorginisposi (singolo) il throwaway è
sufficiente; per 55 account serve il CLI.
