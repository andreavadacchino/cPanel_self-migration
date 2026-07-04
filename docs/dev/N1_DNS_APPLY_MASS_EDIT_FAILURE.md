# N1 — `dns apply` fallisce su .78 (`mass_edit_zone: The request failed`)

**Trovato**: dogfooding #2, 2026-07-04.
**Stato**: APERTO — bloccante per il ciclo UI-only end-to-end. Root cause nel
log errori WHM (root-only su .78): non diagnosticabile a livello utente.
**Account**: giorginisposi@.78 (dest, sacrificale). Source .193 read-only.

## Sintomo

Dalla UI (workbench → «Apply DNS», conferma forte) e da CLI, `dns apply`
produce:

```
summary: applied=0 skipped=6 manual=9 failed=3 refused_precondition=0
status_reason (per ognuno dei 3 op):
  DNS::mass_edit_zone: status=0 errors=[The request failed. (Error ID: <ID>)
  Ask your hosting provider to research this error in cPanel & WHM's main error log.]
```

- **Riproducibile 2/2**. L'Error ID cambia ad ogni tentativo (`m7sumx`,
  `qnrpvb`) → ogni chiamata è un fallimento server-side fresco, non un
  risultato cache-ato.
- **Atomico**: tutti e 3 gli op falliscono insieme, nessun apply parziale
  (la DKIM su .78 resta quella dest-rigenerata, invariata). Il tool crea il
  backup PRIMA (`dns_backup.json`, ~20 KB) e riporta l'errore con Error ID
  senza dichiarare falsa riuscita. **Il comportamento del tool è corretto.**

## I 3 op che il tool tenta (batch atomico unico)

Il writer usa `MassEditZoneBatch` (`internal/cpanel/dns_apply.go`): una SINGOLA
`DNS::mass_edit_zone` che porta insieme i `remove-N` (line_index dei replace) e
gli `add-M`. Per giorginisposi il piano (`dns_import_plan.json`) è:

| Azione | Record | Note |
|--------|--------|------|
| add | `_v2smoke.giorginisposi.it. TXT` | valore `smoke-before-replace` (presente anche nella zona SOURCE) |
| replace | `default._domainkey.giorginisposi.it. TXT` | DKIM — **multi-segmento** (2 char-string: 255 + 156) |
| replace | `giorginisposi.it. TXT` | SPF `v=spf1 ...` (single-string, 38 char) |

Il piano `replace` riscrive la DKIM **source** su .78 (design 7A/7E: source
authoritative). Il data DKIM nel piano è **correttamente segmentato** (255+156,
entrambi ≤255) → NON è un problema di char-string oversize.

## Diagnosi a livello utente (SSH giorginisposi@.78) — cosa FUNZIONA e cosa NO

Tutte via `uapi --output=json DNS ...`:

| Prova | Esito |
|-------|-------|
| `parse_zone` (read) | ✅ OK |
| `mass_edit_zone` senza modifiche | ✅ errore di validazione atteso ("provide at least one change") → transport OK |
| `mass_edit_zone add-0=<TXT single-string>` (serial reale) | ✅ **OK**, serial bump |
| `mass_edit_zone remove-0=<line_index>` | ✅ **OK**, serial bump |
| `mass_edit_zone remove-0=<L> add-0=<TXT single-string>` (batch indicizzato) | ✅ **OK** (batch semplice funziona) |
| **batch multi-op del tool** (2 remove di DKIM/SPF esistenti + 3 add, con DKIM 2-segmenti) | ❌ **FALLISCE** (Error ID) |

**Conclusione**: primitiva, serial, auth, batch-semplice funzionano. Il
fallimento è specifico del **batch multi-op con i `replace`**. Candidati
(non distinguibili senza il log root):
1. Il `replace` di un TXT **multi-segmento** (la DKIM): `parse_zone` la riporta
   come UN record a un solo `line_index`, ma nel file di zona potrebbe occupare
   più righe fisiche; rimuovere il solo `line_index` riportato potrebbe lasciare
   segmenti orfani → l'integrity check server-side rifiuta l'intero batch.
2. La combinazione 2-remove + 3-add in una singola call su questa zona.
3. Un quirk d'ambiente su .78 (cluster / CageFS / integrità zona).

Nota ambiente: **CageFS attivo** su giorginisposi@.78 (`/var/cagefs`,
`/usr/share/cagefs-skeleton`), ma UAPI read/write singoli funzionano → CageFS
da solo non spiega il fallimento del batch.

## Cosa serve nella sessione ROOT dedicata (per chiudere N1)

Sul .78 come root, leggere il log per gli Error ID:

```bash
grep -E 'm7sumx|qnrpvb|<nuovo-ID>' /usr/local/cpanel/logs/error_log
# e i log DNS/zone:
tail -n 200 /usr/local/cpanel/logs/error_log
ls -la /var/cpanel/logs/ 2>/dev/null
```

Domande a cui rispondere:
1. L'errore è integrità-zona sul remove di un TXT multi-segmento (DKIM)?
   → **bug di prodotto**: il writer deve rimuovere TUTTE le righe fisiche del
     record multi-segmento, o splittare il replace in più call, o usare
     `edit-N` invece di remove+add.
2. È un rifiuto cluster/CageFS/permessi? → **quirk ambientale**, il tool è ok.

### Riproduzione mirata (senza toccare la DKIM reale)

Per isolare l'ipotesi (1) senza rischiare la DKIM di produzione: creare un TXT
throwaway **lungo** (>255 char → forza la segmentazione multi-riga), verificare
quanti `line_index` occupa in `parse_zone`, poi tentare un batch
`remove-0=<tutte le righe> add-0=<nuovo TXT lungo>`. Se fallisce come il tool →
confermato bug sul replace di TXT multi-segmento.

## Cluster DNS — standalone verificato (rule #4)

`.78` è membro del cluster DNS di produzione (peer `ns.hostnuoviclienti.*`).
Verifica ATTIVA a livello utente che gli edit su .78 **non propagano** (peer
standalone), fatta confrontando la DKIM su tre punti:

| Fonte | DKIM `p=` [40:90] |
|-------|-------------------|
| SOURCE .193 (inventory) | `AQEAy8TiOR74/IW9AuvwitvEYynRAfMHGflI8rUWH5ExOPCwps` |
| DEST .78 (rigenerata) | `AQEAtEYZ1SzVZDkME9oHNjFJQwNQ1yLQAP5xjyULp5gqpXgfgp` |
| **NS pubblici** (136.144.242.119) | `AQEAy8TiOR74/…` (**== SOURCE, ≠ DEST**) |

**PUBLIC == SOURCE ≠ DEST** → la DKIM divergente di .78 non è mai arrivata ai
NS pubblici → **.78 standalone confermato**. Corroborato dai serial SOA:
zona locale .78 = `2026070313`, NS pubblici = `2026070300` (locale avanti, non
propagato). Post-apply-fallito i NS pubblici restano invariati.

> Questo metodo (confronto DKIM/serial source-vs-dest-vs-public) è la verifica
> di standalone eseguibile SENZA root, utile ad ogni futuro `dns apply` su un
> membro del cluster finché la UI non offre un check nativo (vedi N2 in
> DOGFOODING_2_REPORT.md, mitigato con un warning in PR #62).

## Snippet di verifica (read-only, dalla dev Mac)

```bash
# DKIM servita dai NS pubblici (deve restare = SOURCE finché .78 è standalone)
dig +short TXT default._domainkey.giorginisposi.it @136.144.242.119

# serial locale su .78 vs pubblico
dig +short SOA giorginisposi.it @38.224.109.78   # locale (avanti)
dig +short SOA giorginisposi.it @ns.hostnuoviclienti.com  # pubblico
```
