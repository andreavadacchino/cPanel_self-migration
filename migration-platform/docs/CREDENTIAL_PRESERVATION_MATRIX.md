# Credential Preservation Matrix

## Obiettivo

Consolidare in una singola matrice operativa/documentale lo stato reale della
preservazione credenziali nella nuova piattaforma, separando:

- ciò che è già verde e storicamente dimostrato
- ciò che è promettente ma richiede laboratorio dedicato
- ciò che non deve essere chiamato hash-preserving

Questo documento è docs-only. Non introduce apply, probe live, lettura hash
reale, scrittura shadow o modifiche allo smoke email.

## Sintesi brutale

- **Email = VERDE.** La preservazione password email è già dimostrata
  storicamente dal vecchio motore Go e modellata nella nuova piattaforma da
  [SSH_EMAIL_IDENTITY_CLONE_FEASIBILITY.md](./SSH_EMAIL_IDENTITY_CLONE_FEASIBILITY.md).
- **FTP = GIALLO / PROMETTENTE.** L'API di destinazione supporta `pass_hash`,
  ma la fonte live degli hash sul source con SSH account-level non è ancora
  confermata.
- **WebDisk = GIALLO / FRAGILE.** L'API di destinazione supporta
  `password_hash`, ma richiede anche `digest_auth_hash`; la fonte live resta da
  verificare e l'update hash di account esistenti non è chiarito.
- **MySQL Users = ROSSO per hash clone.** Le API ufficiali richiedono password
  in chiaro e non espongono hash leggibili.
- **Password account cPanel = ROSSO.** Non esiste un percorso account-level
  pulito per importare hash preservando la password esistente.
- **Subaccounts / UserManager = RISCHIO TRASVERSALE.** Email, FTP e WebDisk
  possono condividere una sola identità/password; promettere clone separato dei
  servizi senza audit rischia di spacchettare il modello utente reale.

## Stato attuale della roadmap

- `main` contiene già la fattibilità del remote full-account restore.
- `main` contiene già la fattibilità dello SSH-assisted email identity clone.
- `main` contiene già la strategia email-only hash-preserving.
- `main` **non** contiene apply reale.
- `main` **non** contiene lettura hash reale.
- `main` **non** contiene scrittura shadow.
- Lo sprint operativo successivo resta **SSH-Assisted Email Identity Clone
  Smoke**.
- Questa matrice non blocca e non sostituisce quello smoke.

## Classificazione finale

| Credenziale | Stato | Clone senza password in chiaro? | Metodo source | Metodo destination | Livello prova | Decisione |
|---|---|---:|---|---|---|---|
| Email | VERDE | Sì | `~/etc/<domain>/passwd` + `~/etc/<domain>/shadow` | `Email::add_pop password_hash` oppure rewrite controllato `shadow` | Storico + modellato | Resta il primo smoke operativo |
| FTP | GIALLO / PROMETTENTE | Potenzialmente sì | Live source da verificare; fallback backup `proftpdpasswd` | `Ftp::add_ftp pass_hash` | API destination confermata, source non confermato | Spike separato, fuori dallo smoke email |
| WebDisk | GIALLO / FRAGILE | Potenzialmente sì | Live source da verificare; fallback backup `digestshadow` | `WebDisk::addwebdisk password_hash` + `digest_auth_hash` | API destination confermata, source/update non confermati | Spike separato, fuori dallo smoke email |
| MySQL Users | ROSSO | No | Password da config applicative o reset | `Mysql::create_user` / `Mysql::set_password` con password in chiaro | API ufficiali chiare | Policy password-from-config / reset controllato |
| cPanel Account Password | ROSSO | No | Nessuna fonte account-level pulita per hash import | `UserManager::change_password` o WHM `passwd` con password in chiaro | API ufficiali chiare | Non promettere preservazione |
| Subaccounts / UserManager | RISCHIO TRASVERSALE | Dipende dal modello identità | Audit UserManager e servizi collegati | Da definire dopo audit | Deduzione forte da API | Audit prima di promettere FTP/WebDisk |

## Email

Email resta il solo caso verde.

- Source storico: `~/etc/<domain>/passwd` e `~/etc/<domain>/shadow`.
- Hash: campo 2 dello `shadow`.
- Destination: `Email::add_pop` accetta `password_hash`.
- Fallback storico per mailbox già esistente: rewrite controllato del solo
  campo hash in `shadow`.
- Contenuto: sync separato di `Maildir`.
- Verifica: login + contenuto mailbox.

Non serve ripetere qui tutta la prova già raccolta. Il riferimento normativo e
storico resta
[SSH_EMAIL_IDENTITY_CLONE_FEASIBILITY.md](./SSH_EMAIL_IDENTITY_CLONE_FEASIBILITY.md).

Fonti ufficiali rilevanti:

- `Email::add_pop` supporta `password_hash`:
  <https://api.docs.cpanel.net/specifications/cpanel.openapi/email-accounts/add_pop.md>
- La documentazione di migrazione manuale indica `mail` e `etc` come sorgenti
  dei dati email (`/home/user/mail` e `/home/user/etc` con `passwd`, `shadow`,
  `quota`):
  <https://docs.cpanel.net/knowledge-base/transfers-and-restores/how-to-manually-migrate-accounts-to-cpanel-from-unsupported-control-panels/>

## FTP

FTP è giallo/promettente, non verde.

Confermato:

- `Ftp::add_ftp` supporta `pass_hash`.
- `Ftp::list_ftp` elenca account e metadati operativi, ma non hash password.
- Il backup tarball include `proftpdpasswd`.

Non confermato:

- che l'utente cPanel con SSH account-level possa leggere live il materiale hash
  FTP necessario senza passare da backup
- che esista un percorso ufficiale elegante e stabile per leggere quegli hash
  via API o filesystem account-level

Conclusione operativa:

- `proftpdpasswd` da backup è un **fallback** plausibile, non un contratto
  stabile elegante.
- FTP **non** entra nello smoke email.
- FTP merita uno spike separato.

Smoke futuro FTP:

1. Creare account FTP test sul source.
2. Verificare se l'utente cPanel può leggere live materiale utile.
3. Se no, generare full backup e recuperare `proftpdpasswd`.
4. Creare account FTP sul destination con `pass_hash`.
5. Testare login FTP con la vecchia password.
6. Testare directory/home/quota.
7. Testare account già esistente sul destination.

Fonti ufficiali:

- `Ftp::add_ftp` con `pass_hash`:
  <https://api.docs.cpanel.net/specifications/cpanel.openapi/ftp-accounts/add_ftp.md>
- `Ftp::list_ftp`:
  <https://api.docs.cpanel.net/specifications/cpanel.openapi/ftp-accounts/list_ftp.md>
- `fullbackup_to_homedir`:
  <https://api.docs.cpanel.net/specifications/cpanel.openapi/backup/fullbackup_to_homedir.md>
- Contenuto backup tarball con `proftpdpasswd`:
  <https://docs.cpanel.net/knowledge-base/backup/backup-tarball-contents/>
- Documento di sospensione che cita i file FTP server-level
  `/etc/proftpd/passwd.vhosts` e `/etc/proftpd/user`; utile solo come indizio di
  collocazione, non come prova di leggibilità account-level:
  <https://docs.cpanel.net/knowledge-base/accounts/what-happens-when-you-suspend-an-account/>

## WebDisk

WebDisk è giallo/fragile, non verde.

Confermato:

- `WebDisk::addwebdisk` accetta `password_hash`.
- Se si usa `password_hash`, serve anche `digest_auth_hash`.
- `WebDisk::listwebdisks` elenca account e metadati (`hasdigest`, `homedir`,
  `perms`), ma non restituisce hash.
- Il backup tarball include `digestshadow`.

Non confermato:

- che il cPanel user possa leggere live il materiale necessario in
  `~/etc/webdav/shadow`
- che l'update di account WebDisk esistente via hash sia supportato o comunque
  chiaramente documentato
- come trattare in modo robusto account con digest disabilitato, enabledigest,
  permessi e identità già esistenti

Conclusione operativa:

- WebDisk va in spike separato dopo FTP o insieme a FTP.
- WebDisk non entra nello smoke email.

Smoke futuro WebDisk:

1. Creare WebDisk account test con digest attivo.
2. Verificare file live accessibili dal cPanel user.
3. Recuperare `password_hash` + `digest_auth_hash`.
4. Creare account sul destination con `addwebdisk`.
5. Verificare accesso WebDAV.
6. Verificare `hasdigest` / `perms` / `homedir`.
7. Verificare impatto Subaccounts.

Fonti ufficiali:

- `WebDisk::addwebdisk`:
  <https://api.docs.cpanel.net/cpanel-api-2/cpanel-api-2-modules-webdisk/cpanel-api-2-functions-webdisk-addwebdisk>
- `WebDisk::listwebdisks`:
  <https://api.docs.cpanel.net/cpanel-api-2/cpanel-api-2-modules-webdisk/cpanel-api-2-functions-webdisk-listwebdisks.md>
- `WebDisk::passwdwebdisk`:
  <https://api.docs.cpanel.net/cpanel-api-2/cpanel-api-2-modules-webdisk/cpanel-api-2-functions-webdisk-passwdwebdisk.md>
- Contenuto backup tarball con `digestshadow`:
  <https://docs.cpanel.net/knowledge-base/backup/backup-tarball-contents/>
- Documento di sospensione che cita `~/etc/webdav/shadow`; utile come indizio di
  collocazione, non come prova di leggibilità account-level:
  <https://docs.cpanel.net/knowledge-base/accounts/what-happens-when-you-suspend-an-account/>

## MySQL Users

MySQL users sono rossi per hash clone.

Confermato:

- `Mysql::create_user` richiede password in chiaro.
- `Mysql::set_password` richiede password in chiaro.
- `Mysql::list_users` non espone hash.
- `mysql.sql` nel backup contiene grants, non hash utenti.

Decisione:

- MySQL resta **password-from-config / reset controllato**.
- Non va chiamato hash-preserving.

## cPanel Account Password

La password dell'account cPanel è rossa.

Confermato:

- `UserManager::change_password` richiede `oldpass` e `newpass`.
- WHM `passwd` richiede la nuova password in chiaro.
- Il backup tarball include un file `shadow` dell'account, ma la stessa
  documentazione avverte di non usare il tarball come integration endpoint.

Decisione:

- Non promettere preservazione password account cPanel.
- Non presentare il backup tarball come percorso supportato di hash import.

## Subaccounts / UserManager

Subaccounts non sono un colore isolato: sono un rischio trasversale.

La documentazione `UserManager::create_user` conferma che una Subaccount può
abilitare email, FTP e Web Disk sotto la stessa identità e la stessa password.

Implicazione:

- clonare separatamente email, FTP e WebDisk può spacchettare un'identità unica
- prima di qualsiasi promessa FTP/WebDisk serve un audit UserManager sul source
- il lab FTP/WebDisk deve distinguere:
  - service accounts separati
  - Subaccounts unificate

Decisione:

- prima di promettere FTP/WebDisk, verificare se il source usa service accounts
  separati o Subaccounts unificati

## Matrice operativa

| Tipo credenziale | Dove leggere sul source | API/list ufficiale | Hash leggibile via API? | Apply hash-supported? | Richiede backup tarball? | Clone fedele? | Rischio | Smoke richiesto |
|---|---|---:|---:|---:|---:|---:|---|---|
| Email | `~/etc/<domain>/passwd` + `~/etc/<domain>/shadow` | Sì | No | Sì | No | Sì | Basso se SSH/account-level disponibile | Sì, smoke email |
| FTP | Da verificare live; fallback `proftpdpasswd` in backup | Sì | No | Sì | Probabile fallback | Non ancora dimostrato | Medio | Sì, lab dedicato |
| WebDisk | Da verificare live; indizio `~/etc/webdav/shadow`; fallback `digestshadow` in backup | Sì | No | Sì | Probabile fallback | Non ancora dimostrato | Medio-alto | Sì, lab dedicato |
| MySQL users | Config applicative / secret store / reset | Sì | No | No, solo password in chiaro | No | No | Medio operativo, basso documentale | No smoke hash; solo flow config/reset |
| cPanel account password | Nessun source account-level pulito per hash import | Sì | No | No | Backup non supportato come endpoint | No | Alto prodotto | No |
| Subaccounts / UserManager | Audit configurazione servizi | Sì | No | N/A | No | Dipende | Alto di modello identità | Audit prima del lab |

## Cosa è verde

- Email password preservation con accesso SSH/account-level.
- Uso di `Email::add_pop password_hash` lato destination.
- Preservazione contenuto mailbox via `Maildir` sync separato.
- Verifica finale tramite login e contenuto mailbox.

## Cosa è giallo

- FTP: destination hash apply confermato, source hash acquisition da confermare.
- WebDisk: destination hash apply confermato ma fragile per doppio hash e update
  non chiarito.
- Subaccounts: modello identità condivisa da auditare prima di qualsiasi
  promessa.

## Cosa è rosso

- MySQL user hash clone.
- cPanel account password preservation.
- Qualsiasi claim del tipo "preserviamo tutte le password" o "preserviamo tutte
  le credenziali".

## Perché FTP/WebDisk non devono entrare nello smoke email

- Lo smoke email ha già una base storica e documentale separata.
- FTP e WebDisk non hanno ancora la stessa prova sul **source path**.
- FTP può richiedere fallback da backup tarball, che è esplicitamente non
  raccomandato come integration endpoint stabile.
- WebDisk aggiunge complessità digest (`digest_auth_hash`) e possibile coupling
  con Subaccounts.
- Mescolare questi temi nello smoke email trasformerebbe una verifica verde in
  una promessa multiprotocollo non ancora supportata.

## Piano futuro FTP Lab

Obiettivo: verificare se esiste un percorso davvero difendibile di
preservazione FTP senza password in chiaro.

Passi:

1. Creare account FTP test sul source.
2. Verificare leggibilità live con SSH account-level.
3. Se live fallisce, generare backup con `fullbackup_to_homedir`.
4. Estrarre `proftpdpasswd`.
5. Applicare `pass_hash` sul destination.
6. Verificare login, home, quota e caso account preesistente.
7. Documentare se il risultato è supportabile come prodotto o solo come
   fallback fragile.

## Piano futuro WebDisk Lab

Obiettivo: verificare se WebDisk è preservabile in modo coerente e ripetibile.

Passi:

1. Creare account WebDisk test con digest attivo.
2. Verificare leggibilità live del materiale hash dal source.
3. Recuperare `password_hash` e `digest_auth_hash`.
4. Applicare `addwebdisk` sul destination.
5. Verificare login WebDAV.
6. Verificare `hasdigest`, `enabledigest`, `perms`, `homedir`.
7. Verificare collisioni/effetti su Subaccounts.

## Policy MySQL

- Recuperare la password da `wp-config` / config applicative / registry.
- Creare o allineare user / db / grants.
- Riscrivere la config sul destination.
- Se la password non è recuperabile: generare nuova password e aggiornare la
  config.
- Non chiamare questo percorso hash clone.

## Policy cPanel Password

- Non promettere preservazione password account cPanel.
- Se serve accesso utente finale, prevedere reset controllato o handoff di
  cambio password.
- Non costruire messaging commerciale sull'idea di hash import dell'account
  principale.

## Rischi prodotto

- Confondere il verde email con un verde generale credenziali.
- Vendere FTP/WebDisk come già preservati quando oggi sono solo candidati da
  laboratorio.
- Vendere MySQL come hash-preserving quando il modello reale è
  password-from-config / reset.
- Rompere identità utente se una Subaccount condivisa viene spacchettata in tre
  servizi clonati separatamente.
- Basarsi su backup tarball come integration contract stabile nonostante la
  documentazione cPanel lo sconsigli.

## Copy prodotto consentita

- "Password email preservabili con accesso SSH/account-level, dopo smoke
  positivo."
- "FTP/WebDisk sono candidati a preservazione hash, ma richiedono laboratorio
  dedicato."
- "MySQL usa password da config applicative o reset controllato, non hash
  clone."

## Copy prodotto vietata

- "Preserviamo tutte le password."
- "Preserviamo tutte le credenziali."
- "FTP/WebDisk già preservati."
- "MySQL password hash clone."
- "cPanel account password preservata."

## Raccomandazione finale

La linea difendibile oggi è netta:

- **Email** resta verde, storico, e primo smoke operativo.
- **FTP** resta giallo/promettente e va in lab futuro separato.
- **WebDisk** resta giallo/fragile e va in lab futuro separato.
- **MySQL** resta rosso per hash clone e segue policy password-from-config.
- **cPanel password** resta rossa e non va promessa.
- **Subaccounts** vanno auditate prima di qualsiasi estensione del claim
  FTP/WebDisk.

Questa matrice esiste proprio per impedire che la capability email venga
estesa falsamente a tutte le credenziali.
