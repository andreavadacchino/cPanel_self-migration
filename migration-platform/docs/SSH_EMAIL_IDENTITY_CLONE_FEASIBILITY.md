# SSH-Assisted Email Identity Clone Feasibility

> Spike di analisi (read-only). Nessuna migrazione reale, nessun apply, nessuna
> lettura di hash reali, nessuna scrittura di `shadow`, nessuna copia di Maildir.
> Il vecchio motore Go è letto **solo come riferimento storico**. Tutte le
> citazioni sono `file:riga` verificate.

## Obiettivo

Rispondere con prove a: **possiamo riutilizzare nella nuova piattaforma
l'approccio del vecchio tool per preservare le password EMAIL copiando l'hash da
`~/etc/<domain>/shadow`, senza conoscere le password in chiaro?**

Risposta attesa e confermata: **sì per l'email, con condizioni e limiti** — e
solo dopo uno smoke reale che confermi il porting nella nuova piattaforma.

## Sintesi brutale

- Il vecchio tool **preservava davvero le password email**, ma **non** conosceva
  le password in chiaro: leggeva l'**hash** (campo 2 dello `shadow`) e lo
  ri-applicava sulla destinazione.
- **NON preservava tutte le credenziali.** MySQL era **password-from-config**
  (riuso/generazione di password in chiaro applicative, **non** clone di hash);
  FTP era **solo inventory**; API token / Team users / WebDisk / Directory
  Privacy **non** preservati.
- Il meccanismo email **non è token-only/UAPI**: richiede accesso
  **account-level/SSH** al filesystem dell'account (per leggere `~/etc/.../shadow`
  sul source e riscrivere lo `shadow` / creare la mailbox sul destination). Non
  richiede root né full backup né reseller restore.
- L'hash email **è materiale sensibile**: va trattato come segreto (mai
  persistito/loggato/esposto), anche se non è una password in chiaro.
- Trasformare "preserviamo le password email" in "preserviamo tutte le
  credenziali" sarebbe una **promessa falsa**. La promessa difendibile è solo
  quella email, e solo dopo smoke.

## Vecchio motore analizzato

File letti (read-only) nel worktree su `fork/main`, e cosa dimostrano:

| File | Cosa dimostra |
|---|---|
| `internal/migrate/collect.go` | Lettura read-only di `~/etc/<dom>/{passwd,shadow}` sul source; estrazione hash dal **campo 2** dello shadow; classificazione schema hash |
| `internal/cpanel/email.go` | Applicazione destinazione: `Email::add_pop password_hash=…` per mailbox nuove; **rewrite atomico del solo campo hash** nello shadow per mailbox esistenti; hash passato via `ENVIRON`, non in argv |
| `internal/migrate/apply_mailboxes.go` | Orchestrazione: `EnsureAccount(..., m.Hash)`, copia Maildir, verify; **failure-closed** se l'hash manca |
| `internal/migrate/data.go` | Avvio discovery credenziali DB da config applicative (`DiscoverAllCreds`) |
| `internal/migrate/apply_dbs.go` | MySQL: riuso password da config / generazione per orfani; create user/db/grants via UAPI; rewrite config destinazione |
| `internal/cpanel/mysql.go` | Solo `Mysql::list_users` (nessun hash); wrapper UAPI create_user/create_database/set_privileges/set_password |
| `internal/cpanel/ftp.go` | Solo `Ftp::list_ftp_with_disk` (inventory); `FTPAccountEntry` **senza** campo password |
| `internal/accountinventory/coverage.go` | Manifest dichiarativo: cosa è coperto vs non-collezionato/da-rigenerare (API token, Team, WebDisk, Directory Privacy) |

## Cosa preservava davvero

| Area | Preservata? | Meccanismo | Evidenza codice | Limiti |
|---|---:|---|---|---|
| Email account password | **Sì** | Hash da `~/etc/<domain>/shadow` (campo 2) ri-applicato | `collect.go:188` `awk -F: '$1==u{print $2}'`; `email.go:174-175` `Email add_pop password_hash="$HASH"`; `email.go:30-32` rewrite hash shadow | Richiede SSH/account filesystem access; failure-closed se hash assente |
| Mail content | **Sì** | Maildir sync + verify | `apply_mailboxes.go:90` `maildir.Transfer{...}`; `:265-298` verify count/UIDVALIDITY + `--verify-checksums` | Verifica necessaria; copia separata dall'identità |
| MySQL user password | **Parziale** | Password **in chiaro** da config app; nuova se orfano | `apply_dbs.go:172` `password := it.Password`; `:173-182` `GeneratePassword` (crypto/rand); `cmsconfig.go:654` `DB_PASSWORD` | **NON** è clone di hash; user creato via UAPI |
| FTP password | **No** | Solo inventory | `ftp.go:21-22` `Ftp::list_ftp_with_disk`; `ftp.go:10-19` `FTPAccountEntry` senza campo password | Nessun path di apply/preservazione |
| API token | **No** | Segreti non recuperabili | `coverage.go:65-66` "secrets are never retrievable" | Rigenerare |
| Team users | **No** | Coverage legacy | `coverage.go:98-99` "passwords cannot be migrated" | Rigenerare |
| WebDisk | **No** | Coverage legacy | `coverage.go:100-101` "passwords would need regeneration" | Rigenerare |
| cPanel account password | **No** | Non trattata in questo percorso | — | Fuori scope |

## Meccanismo email hash-preserving

Passo per passo, con evidenza:

1. **Source read-only.** Lo script di analisi gira sul source in sola lettura,
   cammina `~/mail` e legge la metadata mail (`collect.go:214-217`, commento
   *"analyzeScript runs on the source (read-only)"*).
2. **Lettura `passwd`/`shadow`.** Per dominio: `passwd="$ETCROOT/$dom/passwd"`,
   `shadow="$ETCROOT/$dom/shadow"` (`collect.go:249-250`), lette riga per riga
   (`collect.go:259-284`). Commento: *"hashes from the source (read
   `~/etc/<dom>/{passwd,shadow}`). Read-only"* (`collect.go:109-110`).
3. **Estrazione hash (campo 2).** `field2_exact() { awk -F: -v u="$2"
   '$1==u{print $2; exit}' }` (`collect.go:188`) — l'hash è il **secondo campo**
   dello `shadow`, match **esatto** sul campo 1 (`collect.go:186`), mai regex.
   Lo schema è classificato (SHA-512/bcrypt/yescrypt/Argon2/MD5/EMPTY/…,
   `collect.go:101`). Nessuna password in chiaro viene mai letta — lo `shadow`
   contiene solo l'hash.
4. **Create/update mailbox destinazione.** `cpanel.EnsureAccount(ctx,
   pool.Dest, destDomain, m.User, m.Hash)` (`apply_mailboxes.go:157`).
5. **`password_hash` in `add_pop`.** Per una mailbox nuova:
   `uapi Email add_pop email=… domain=… password_hash="$HASH" quota=0`
   (`email.go:174-175`).
6. **Rewrite dello shadow su mailbox esistente.** Se `~/etc/<dom>/shadow`
   contiene già l'utente, si **riscrive solo il campo hash** in modo atomico
   (awk su file temporaneo poi rename), perché `add_pop` rifiuta i duplicati
   (`email.go:30-32`, `:104`, `SH="$HOME/etc/$DOM/shadow"` `:115`).
7. **Maildir sync separato.** La copia del contenuto è un passo distinto:
   `maildir.Transfer{Src, Dest, …}` (`apply_mailboxes.go:11,90`).
8. **Verify separato.** Integrità post-copia: fast-skip su conteggio +
   UIDVALIDITY, e con `--verify-checksums` confronto del message-set
   (`apply_mailboxes.go:77,233,265-298`).
9. **Failure-closed.** Se l'hash è assente: *"no password hash found on source;
   account/password not applied"* → esito **unverified**, l'account/password
   **non** viene applicato (`apply_mailboxes.go:138-142`).

Nota sicurezza dal vecchio codice: l'hash viene passato all'awk via
`ENVIRON["HASH"]`, **non** in argv (`email.go:42-43,91-93`) — segnale che già
allora l'hash era trattato come materiale da non esporre nella command line.

## Perché questo non è token-only

- **token_only/UAPI inventory non vede lo `shadow`.** Il layer UAPI legge
  domini/email/DB via API, ma **non** ha accesso al filesystem `~/etc/<dom>/shadow`
  dove vivono gli hash. Con solo un API token gli hash sono irraggiungibili.
- **SSH/account access vede il filesystem dell'account.** Solo l'accesso
  shell/file-level come utente cPanel permette di leggere `passwd`/`shadow` e di
  riscrivere lo `shadow`/creare la mailbox sul destination.
- **Non serve root.** È accesso a livello di account, non di server.
- **Non serve full backup.** Si copia solo ciò che serve (identità + Maildir),
  granulare, senza archivio enorme né spazio extra sul source.
- **Ma serve accesso shell/file-level** su entrambi i lati (source per leggere,
  destination per scrivere shadow/mailbox e Maildir).

## Nuova strategia proposta

Nome: **SSH-Assisted Email Identity Clone**

È una strategia **separata** dalle quattro già modellate (api_rebuild,
restore_assisted_config_clone, full_account_restore, root_transfer). Non è
token-only, non è full backup, non è reseller restore, non è root transfer.

## Capability richieste

- `can_ssh_source_account` — **prerequisito esplicito**
- `can_ssh_destination_account` — **prerequisito esplicito**
- `can_read_source_mail_shadow`
- `can_read_source_mail_passwd`
- `can_create_destination_mailbox_with_password_hash`
- `can_update_destination_mail_shadow_hash`
- `can_copy_maildir`
- `can_verify_maildir`
- `can_redact_hashes_everywhere`

> **`can_ssh_source_account` e `can_ssh_destination_account` sono prerequisiti
> espliciti, NON impliciti.** La strategia è SSH/account-level: il modello
> (`recommend_email_identity_strategy`) **rifiuta** (cade su `api_rebuild` /
> `unavailable`) se anche uno solo dei due è assente, prima ancora di valutare la
> leggibilità dello `shadow` o la scrivibilità della destinazione. Non basta che
> lo `shadow` sia "readable": serve accesso shell/file-level dichiarato su
> **entrambi** i lati.

## Cosa NON promette

- **non** preserva le password FTP
- **non** preserva gli hash MySQL
- **non** preserva gli API token
- **non** preserva i Team users
- **non** preserva WebDisk
- **non** preserva la password dell'account cPanel
- **non** preserva "tutte le credenziali"

## Perché non estenderlo subito a tutte le password

- **Email è un caso speciale:** l'hash è leggibile dal filesystem
  (`~/etc/<dom>/shadow`) **e** la scrittura destinazione (add_pop
  `password_hash` / rewrite shadow) era già provata nel vecchio motore.
- **MySQL non funziona allo stesso modo:** il vecchio tool **non** clonava gli
  hash; scopriva la password in chiaro dalla config applicativa e altrimenti la
  **generava** (`apply_dbs.go:172-182`). È re-provisioning, non preservazione.
- **FTP richiede uno spike separato:** oggi è solo inventory, nessun campo
  password (`ftp.go:10-19`).
- **WebDisk/Team/API token** restano da rigenerare o da analizzare a parte
  (`coverage.go:65-101`).

## Sicurezza

**Gli hash email sono segreti.** Anche se non sono password in chiaro, sono
materiale sensibile (permettono l'autenticazione). Regole:

- mai salvare hash in DB persistente
- mai esporre hash in API response
- mai loggare hash
- mai metterli in eventi/job progress/report
- mai usarli come fingerprint reversibile/visibile o come contenuto scaricabile
- usare solo memoria transitoria/job-local quando si implementerà l'apply
- redazione obbligatoria ovunque
- **failure closed** se l'hash è assente (come il vecchio motore:
  `apply_mailboxes.go:138-142` → unverified, nessun apply)

Il modello dominio rispecchia questa regola: `recommend_email_identity_strategy`
**rifiuta** la strategia (`api_rebuild` / `unavailable`) se
`can_redact_hashes_everywhere` non è garantito.

## Impatto sulla nuova piattaforma

Come aggiungere in futuro (roadmap, non in questa PR):

- **Access profile `ssh_account_access`** — aggiunto in `AccessProfile` (dominio).
- **Capability probe read-only** — verificare *senza leggere gli hash reali* se
  `~/etc/<dom>/shadow` è leggibile e se il destination accetta `add_pop
  password_hash` / rewrite shadow. Il probe non deve stampare né persistere hash.
- **Plan section `email_identity`** — nuova sezione nel Migration Plan che
  riporta la raccomandazione email (senza hash).
- **Strategy recommendation** — `recommend_email_identity_strategy` alimenta la
  sezione.
- **Worker apply separato** — un futuro worker che, solo in memoria/job-local,
  legge l'hash, lo applica e lo scarta a fine job. Mai in DB/eventi/log.
- **Secret redaction tests** — test che verificano che nessun hash finisca in
  DB/snapshot/API/log/report.

## Smoke test plan futuro

Account **sacrificabile** (credenziali fuori repo, autorizzazione esplicita):

- 1 mailbox con password nota
- messaggi nel Maildir
- destination vuoto
- clone dell'hash (identità)
- login IMAP/POP/webmail con la **vecchia** password
- verify del contenuto mailbox
- **negative test**: hash assente → deve fallire closed (unverified, nessun apply)
- **negative test**: mailbox già esistente sul destination → rewrite del solo
  campo hash, nessun duplicato

> Real-smoke **non eseguito** in questo spike: è modelling/documentation-only,
> nessun account sacrificabile né autorizzazione.

## Decisione consigliata

- **Adottare la strategia SSH-Assisted Email Identity Clone come strategia
  separata e opt-in**, limitata all'**email**, con `access_profile =
  ssh_account_access`.
- **Gate di sicurezza obbligatorio:** rifiutare finché la redazione degli hash
  non è garantita end-to-end.
- **Prossimo passo = smoke controllato** su account sacrificabile, **non** apply
  in produzione.
- **Non estendere** la promessa a FTP/MySQL/API token/WebDisk: per MySQL vale il
  modello password-from-config del vecchio tool (re-provisioning), non un clone
  di hash.

## Roadmap proposta

1. (Questa PR) Documento + modello dominio + test — **nessun apply**.
2. Capability probe read-only (nessuna lettura hash reale).
3. Smoke controllato su account sacrificabile (email only).
4. Se lo smoke conferma: worker apply email-only con hash transitori + redaction
   test, dietro conferma esplicita dell'operatore.
5. Spike separati per FTP / Directory Privacy (eventuale).

## Out of scope futuro/separato

- **FTP credential preservation feasibility** — forse investigabile, non
  dimostrato; spike separato.
- **Directory Privacy / `.htpasswds` feasibility** — forse file-based, fuori
  scope (`coverage.go:76-77`).
- **WebDisk credential feasibility** — password da rigenerare
  (`coverage.go:100-101`).
- **Team users** — non preservati (`coverage.go:98-99`).
- **API token** — segreti non recuperabili (`coverage.go:65-66`).
- **MySQL hash clone** — non fatto dal vecchio tool; resta password-from-config.
