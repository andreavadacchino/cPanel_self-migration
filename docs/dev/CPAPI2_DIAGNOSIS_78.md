# Diagnosi cpapi2 su .78 — 2026-07-03

## Sintesi

`cpapi2` e UAPI modulo `Cron` sono entrambi non funzionanti su .78
(Keliweb VPS dest, cPanel). **Non risolvibile a livello utente.** Serve un
ticket Keliweb o un intervento root.

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
