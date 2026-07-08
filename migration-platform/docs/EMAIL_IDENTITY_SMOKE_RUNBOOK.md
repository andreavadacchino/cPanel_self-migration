# Email Identity Smoke Runbook

## Obiettivo

Validare in modo controllato la strategia **SSH-Assisted Email Identity Clone**
per una singola mailbox sacrificabile:

- leggere l'hash email dal source in `~/etc/<domain>/shadow`
- creare la mailbox sul destination con `password_hash`
- verificare che la **vecchia password** continui a funzionare

Questo runbook è **email-only**. Non copre FTP, WebDisk, MySQL, password
account cPanel o Subaccounts.

## Prerequisiti

- Source cPanel con accesso SSH **account-level** come utente cPanel.
- Destination cPanel che accetti `Email::add_pop password_hash`.
- Mailbox sacrificabile sul source.
- Password storica nota **solo all'operatore**.
- Finestra operativa in cui eventuali mailbox di test possano essere create e
  rimosse.
- Operatore autorizzato a usare account sacrificabili.

## Account sacrificabile source

Serve un account source in cui l'operatore possa:

- entrare via SSH come utente cPanel
- leggere `~/etc/<domain>/shadow`
- identificare la mailbox test
- confermare la vecchia password della mailbox

La mailbox deve essere sacrificabile. Nessun dato reale sensibile va usato per
lo smoke.

## Account sacrificabile destination

Serve un account destination in cui l'operatore possa:

- autenticarsi alle API cPanel con token o password
- creare una mailbox test separata
- verificare il login IMAP/POP/webmail
- rimuovere la mailbox a fine test

## Mailbox test

Preparare:

- `SMOKE_DOMAIN`
- `SMOKE_MAILBOX_USER` sul source
- `SMOKE_DEST_MAILBOX_USER` sul destination
- `SMOKE_MAILBOX_OLD_PASSWORD` nota solo all'operatore

Consigliato:

- mailbox source e destination dedicate
- nessun riuso di mailbox operative
- nessuna mailbox condivisa con utenti reali

## Variabili ambiente richieste

Segreti solo via env, mai in argv:

- `SOURCE_SSH_HOST`
- `SOURCE_SSH_PORT` opzionale, default `22`
- `SOURCE_SSH_USER`
- `SOURCE_SSH_KEY_PATH`
- `DEST_CPANEL_HOST`
- `DEST_CPANEL_USER`
- `DEST_CPANEL_TOKEN` oppure `DEST_CPANEL_PASSWORD`
- `SMOKE_DOMAIN`
- `SMOKE_MAILBOX_USER`
- `SMOKE_MAILBOX_OLD_PASSWORD`
- `SMOKE_DEST_MAILBOX_USER`

Opzionali:

- `DEST_IMAP_HOST` default: host di `DEST_CPANEL_HOST`
- `DEST_IMAP_PORT` default: `993`
- `SOURCE_MAILDIR_PATH`
- `DEST_MAILDIR_PATH`

Nota:

- `SOURCE_SSH_PASSWORD` può comparire come env dichiarata legacy, ma **non è
  supportata** dal live harness.
- Il live smoke richiede `SOURCE_SSH_KEY_PATH`.

## Modalità di lettura shadow source

Lo script live usa SSH account-level sul source e legge:

- `~/etc/<domain>/shadow`

La ricerca è per match esatto sul nome mailbox e usa il **campo 2** come hash.

Regole:

- mai stampare il contenuto dello shadow
- mai salvare l'hash su file
- mai loggare stdout/stderr raw delle operazioni
- fail closed se il file non è leggibile o se l'hash manca

## Modalità di creazione mailbox destination

Lo script live chiama:

- `Email::add_pop`

con:

- `email=<mailbox>`
- `domain=<domain>`
- `password_hash=<hash>`
- `quota=0`

L'hash non deve apparire in output. Se il destination rifiuta `password_hash`,
lo smoke è **FAIL**.

## Verifica login IMAP/POP/webmail

Lo smoke harness verifica **IMAP SSL** come prova minima.

Success criteria tecnico:

- login IMAP del destination riuscito con
  `SMOKE_DEST_MAILBOX_USER@SMOKE_DOMAIN`
- password usata: `SMOKE_MAILBOX_OLD_PASSWORD`

Verifiche manuali opzionali aggiuntive:

- POP
- webmail

## Verifica contenuto Maildir

Questo harness **non automatizza** il copy di Maildir.

È consentito:

- usare mailbox vuota e validare solo l'identità/password
- fare una verifica manuale separata del contenuto se l'operatore decide di
  simulare una Maildir minima fuori da questo script

Per questo sprint, il criterio minimo è il login con la vecchia password.

## Esecuzione dry-run

Comando:

```bash
python migration-platform/scripts/email_identity_smoke.py
```

In dry-run lo script fa solo:

- validazione env
- piano redatto
- nessuna connessione
- nessuna lettura shadow
- nessuna creazione mailbox

## Esecuzione live

Solo con doppio gate esplicito:

```bash
python migration-platform/scripts/email_identity_smoke.py \
  --live \
  --i-understand-this-uses-sacrificial-accounts
```

Se uno dei due flag manca:

- nessuna operazione live
- nessuna connessione
- nessuna lettura shadow
- nessuna scrittura destination

Se in live manca `SOURCE_SSH_KEY_PATH`:

- fail closed
- messaggio esplicito:
  `SOURCE_SSH_PASSWORD is not supported by this harness; use SOURCE_SSH_KEY_PATH`

## Rollback / cleanup

Cleanup minimo:

- eliminare la mailbox test sul destination
- chiudere sessioni IMAP aperte
- verificare che nessun log temporaneo contenga segreti

Se è stata fatta una simulazione Maildir manuale:

- rimuovere la Maildir di test
- ripristinare eventuali directory create per lo smoke

## Failure modes

- `source shadow not readable`
- `source mailbox hash missing`
- `source SSH read failed`
- `destination rejected password_hash`
- `imap login rejected old password`
- qualunque hash/password/token appare in output

## Criteri PASS

- mailbox destination creata
- login con vecchia password riuscito
- nessun hash in stdout/stderr/log/file/report
- cleanup possibile

## Criteri FAIL

- source shadow non leggibile
- hash mailbox assente
- destination non accetta `password_hash`
- login con vecchia password fallisce
- qualunque hash appare nei log/output

## Note operative

- Questo runbook non autorizza automaticamente uno smoke live.
- Se mancano account sacrificabili o credenziali autorizzate, fermarsi al
  dry-run.
- Non riportare mai risultati inventati. Se lo smoke reale non viene eseguito,
  dichiarare esplicitamente: **real-smoke non eseguito**.
