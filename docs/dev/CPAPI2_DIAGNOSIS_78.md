# Diagnosi cpapi2 su .78 — 2026-07-03

## ✅ RISOLTO — 2026-07-03 (sessione root SSH)

`cpapi2` ora **funziona** per `giorginisposi` (read + write `setmxcheck` +
rollback verificati). Il debito **setmxcheck è CHIUSO**.

### Causa radice REALE (NON era la jailshell)

La shell di `giorginisposi` è `/bin/bash`, **non jailshell**. Il vero
isolamento è **CageFS** (Mode "Enable All"). Il binario
`/usr/local/cpanel/cpanel` **esiste** (ripristinato 03/07 04:02) ma
**CageFS non lo espone** nel filesystem virtualizzato dell'utente.
Diagnosi a strati (via `strace`):

1. `/usr/local/cpanel/cpanel` assente nello skeleton → `No such file`
   (la config `conf.d/cpanel.cfg` espone `bin/`, `*.pm`, `Cpanel/` ma
   NON il binario principale)
2. Esposto il binario → `EPERM` + "Locale needs to be compiled": i `.cdb`
   sono in `/var/cpanel/locale/`, non esposto in CageFS
3. Esposti i locale → `EPERM`: `/usr/local/cpanel/cpanel.lisc` (licenza)
   assente → il binario renderizza `licenseerror_cpanel.tmpl` ed esce 1
4. Esposta la licenza → **ancora EPERM**: mancano `cpsanitycheck.so`,
   `/var/cpanel/users/giorginisposi`, e **85 accessi file falliti** totali

**Conclusione:** il binario `cpanel` **non è progettato per girare da un
utente cageato** — scelta di design CloudLinux (le API utente passano da
`cpsrvd`). Esporre file uno per uno è un rabbit hole non manutenibile (si
rompe a ogni update cPanel).

### Fix applicato

`cagefsctl --disable giorginisposi` — l'utente esce da CageFS e vede il
filesystem reale. `cpapi2` funziona in modo robusto e **persistente**.
- Riduce l'isolamento CageFS del **solo** `giorginisposi` (accettabile su
  sacrificale), **reversibile** con `cagefsctl --enable giorginisposi`.
- Le esposizioni CageFS custom usate in diagnosi sono state **rimosse**
  (config CageFS standard ripristinata).

### Verifica (tutto PASS)
- `cpapi2 Email listmxs` → JSON valido
- `cpapi2 Email setmxcheck mxcheck=local` → `result:1`, verificato `local`;
  rollback a `auto` verificato
- `uapi Email list_mxs` → OK
- Peer NS cluster (`-dnsrole` autoritativo): `standalone` per entrambi
  (136.144.242.119, 185.17.106.73)

### Implicazioni per il tool
- **setmxcheck NON è più BLOCCATO**: `RunAPI2` via SSH `cpapi2` funziona
  su .78 per l'account migrato (CageFS disabilitato).
- ⚠️ **Generalizzazione**: il fix vale per QUESTO utente. Su un server di
  destinazione con CageFS attivo, ogni account migrato che deve usare
  `cpapi2` da SSH richiederebbe lo stesso `--disable`, OPPURE il fallback
  **HTTP JSON API (2083)** che è **CageFS-agnostico** (vedi sotto). Per il
  tool, l'HTTP JSON resta la strada più robusta e portabile.

---

## Sintesi (diagnosi storica — 2026-07-03 mattina, superata)

`cpapi2` non funziona dalla jailshell dell'utente `giorginisposi` su .78.

**Update 2026-07-03**: il binario `/usr/local/cpanel/cpanel` è stato
ripristinato dall'auto-update notturno (v11.110.0.133, 28 MB, 04:22 UTC).
Tuttavia la **jailshell** dell'utente non lo vede: `apitool` (3.1 MB, da
maggio) tenta `exec()` su `/usr/local/cpanel/cpanel` che è fuori dal
filesystem virtualizzato della jailshell. Serve root per aggiornare la
jailshell skeleton o cambiare la shell dell'utente.

## Fatti

1. **`/usr/local/cpanel/bin/cpapi2`** è un symlink → `apitool` (binario
   3.4 MB, presente, `file` lo vede come binario). `--help` elenca tutti
   i moduli. Ma ogni invocazione di una funzione fallisce con:
   ```
   Failed to execute /usr/local/cpanel/cpanel: No such file or directory
   at bin/apitool.pl line 278.
   ```

2. **`/usr/local/cpanel/cpanel` non esiste.** Normalmente questo è il
   binario principale di cPanel (il "cpanel daemon wrapper"). La sua
   assenza indica un'installazione cPanel danneggiata o incompleta.

3. **`/usr/local/cpanel/version` non è leggibile** (errore di permesso o
   file mancante). Non possiamo verificare la versione cPanel.

4. **UAPI modulo `Cron` fallisce al caricamento**:
   ```
   Failed to load module "Cpanel::API::Cron":
   Cpanel::Exception::ModuleLoadError
   ```
   Conseguenza: né `uapi Cron list_cron` né `uapi Cron add_line`
   funzionano. Il modulo Perl `Cpanel::API::Cron` non è installato o le
   sue dipendenze sono rotte.

5. **`crontab -l` e `crontab -` (SSH) funzionano normalmente.** Il
   crontab utente è l'unica primitiva cron disponibile.

## Impatto sul tool

| Primitiva | Via API | Via SSH | Stato |
|-----------|---------|---------|-------|
| Leggere cron | ❌ UAPI Cron | ✅ `crontab -l` | OK (già implementato) |
| Scrivere cron | ❌ cpapi2/UAPI | ✅ `crontab -` | Usare SSH |
| setmxcheck (routing) | ❌ cpapi2 Email | ❌ nessuna via SSH | **BLOCCATO** |

### Cron apply (2A)

Il writer cron DEVE usare `crontab -` via SSH. Non c'è alternativa API.
⚠️ Semantica distruttiva: `crontab -` SOSTITUISCE l'intero crontab.
Il writer deve: leggere il crontab corrente → aggiungere/rimuovere le
righe pianificate → installare il crontab modificato → verificare.

### Routing (setmxcheck)

Resta BLOCCATO finché cpapi2 non viene riparato. Non esiste alternativa
SSH/UAPI. Al cutover, il routing deve essere impostato a mano da chi ha
accesso WHM/root. L'azione manuale è già nel checklist.

## Workaround scoperto: HTTP JSON API (porta 2083)

L'API HTTP di cPanel (`https://127.0.0.1:2083`) funziona perfettamente
per TUTTE le chiamate API2, incluso `Email::setmxcheck`. Richiede
autenticazione cookie-jar (login + token + cookies). Testato:
`Email::listpops`, `ZoneEdit::fetchzone_records`, `Email::setmxcheck`
— tutti rispondono correttamente via HTTP anche se il CLI è rotto.

**Per la 2A**: il workaround HTTP NON serve — il cron si fa via SSH.
**Per setmxcheck (routing)**: il workaround HTTP è un'alternativa al
ticket Keliweb. Implementarlo richiederebbe un fallback HTTP in
`RunAPI2` (il pattern esiste già parzialmente in `addon.go`). Fuori
scope 2A ma candidato per una PR dedicata se il ticket non risolve.

## Raccomandazione

1. **Ticket Keliweb**: segnalare che `/usr/local/cpanel/cpanel` manca
   su .78 e il modulo `Cpanel::API::Cron` non si carica. Chiedere la
   reinstallazione. Alternativa: `check_cpanel_pkgs --fix` come root.
2. **Non bloccare 2A**: il cron apply via SSH `crontab -` funziona ed è
   la primitiva più affidabile.
3. **setmxcheck**: due opzioni — (a) ticket Keliweb per riparare cpapi2,
   (b) implementare fallback HTTP in RunAPI2 (PR separata).
