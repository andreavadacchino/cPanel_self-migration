# Piano di completamento — migrazione account cPanel senza root

Data: 2026-07-02. Basato su analisi verificata di: motore upstream (v2.2.1),
layer inventory del fork (PR 1-37), documentazione di progetto, e superficie
API cPanel user-level verificata su api.docs.cpanel.net (spec 11.136, valida v110+).

## Obiettivo

Migrare un account cPanel completo tra due server **senza accesso root**:
automatizzare tutto ciò che le API user-level permettono; tutto il resto
diventa **dato storico** (dossier per account) + checklist operatore, così che
su una campagna da 100+ account nessun cron, record DNS o configurazione vada
perso o passi inosservato.

## Stato verificato (sintesi delle 4 analisi)

**Il motore upstream è maturo e va riusato, non riscritto.** Migra mail
(password hash preservati via `Email::add_pop password_hash=`), file web
(tar bridge con guardie di contenimento, backup `-bak`, verify a manifest +
digest), database (mysqldump|mysql, provisioning utenti/grant idempotente,
rewrite config per 8 CMS), creazione domini addon/sub (token API temporaneo
15min, riconciliazione contro `list_domains`). Test > codice sorgente in ogni
package, fault-injection, fail-closed sistematico. **MAI eseguito da noi su
server reali** — questo è il rischio n.1 dell'intero progetto.

**Limiti reali del motore:** rewrite config solo 8/24 CMS riconosciuti;
4 CMS non rilevati affatto (tra cui **PrestaShop 1.7+** — critico per i
nostri shop); `DB_HOST` mai riscritto; CHECKSUM TABLE cross-version degradato
a COUNT(*) (rilevante: source CentOS 7 / dest CloudLinux 9.8); copia SOLO i
docroot, non la home (~/.spamassassin, ~/.htpasswds, ~/.ssh esclusi).

**Il layer inventory del fork copre 14 sezioni** con diff/policy/dns-plan/
checklist/dns-verify/UI, catena provenienza sha256, accettazioni operatore.
**17 aree account NON coperte e oggi invisibili** (assenza silenziosa nella
checklist — lo stato `not_inventoried` è morto dopo PR 7E).

**Superficie API user-level (verificata sulla doc ufficiale):** ~27 aree su 32
automatizzabili. Scoperte chiave:
- `Ftp::add_ftp` accetta `pass_hash` (ma il file hash source `/etc/proftpd/<user>`
  è probabilmente root-only → password nuove)
- `SSL::show_key` esporta le chiavi private → migrazione SSL completa user-level
- Filtri email round-trippabili 1:1 (`get_filter` → `store_filter`)
- Autoresponder round-trippabili col body (`get_auto_responder` → `add_auto_responder`)
- Email routing SOLO via API2 `Email::setmxcheck` (nessun equivalente UAPI)
- Cron: meglio `crontab` via SSH (preserva MAILTO/env/commenti verbatim)
- Addon/parked: fragili — dipendono da tweak settings del server dest
  (controllabili: abbiamo root su .78) e dal dominio non già presente sul server

**Impossibile senza root (→ dossier storico, sempre):** creazione account,
package/quota/bandwidth, IP dedicato, rename dominio principale, hash password
MySQL esistenti, membri Mailman, segreti API token, NS/registrar, config
server-level. Nota di campagna: su .78 (destinazione) ABBIAMO root — la
creazione account dest può essere pre-provisionata via WHM fuori dal tool.

**Nessuna capacità multi-account:** config rigidamente a coppia singola
src/dest; nessun doc o codice affronta la campagna da 100+ account.

---

## FASE 0 — Verità (prima di ogni nuova riga di codice)

Il debito più pericoloso non è funzionalità mancante: è che il motore non ha
mai volato. Nessuna fase successiva parte finché la 0 non è chiusa.

- **0.1 Smoke reale 7E** (read-only, ~1h, TOTP Orbit): pipeline completa su
  capture fresche doctorbike/italplant. Le 15 rewrite CMS di doctorbike devono
  produrre expected differences; il routing remote di italplant deve restare pulito.
- **0.2 Primo run reale del motore** — IL milestone mancante. Prerequisiti:
  account sacrificale su .193 (o clone di un account piccolo), account dest
  su .78 creato via WHM, password cPanel di entrambi, shell abilitata.
  Sequenza: dry-run → `--apply` → verify → checklist con report.json reale.
  Criterio di uscita: report scritto delle divergenze osservate (aspettarsi
  sorprese su MySQL 5.x→MariaDB/MySQL8, GNU tools CentOS 7, jailshell).
- **0.3 Censimento di massa source-only** (riuso 100% dell'esistente):
  `--account-inventory` su tutti gli account dei server sorgente
  (.193=55, .205=67, .41=49). Output: matrice "quanti account usano
  forwarder / filtri / boxtrapper / git / passenger / webdisk / mailing
  list / team users". **Le priorità delle Fasi 1-2 si decidono con questi
  dati, non a intuito.** Richiede di risolvere l'accesso di massa (vedi 4D):
  con root sui source si possono iniettare chiavi SSH per-account.

## FASE 1 — Inventario completo (nessuna assenza silenziosa)

Pattern già rodato (7E: capture reali → collector → diff/policy/checklist).

- **1A Coverage manifest** (1 PR): la checklist elenca OGNI area nota al tool
  col suo stato di copertura (collected / not_collected / not_applicable /
  root_only). Risurrezione esplicita del principio "ciò che non vediamo deve
  dichiararsi", oggi perso.
- **1B Collector batch 1** (1-2 PR — alto valore): quota LIMITE mailbox,
  webdisk, mailing lists, SpamAssassin settings, contact info, chiavi SSH
  (metadati/fingerprint, mai le private), nomi API token,
  **body autoresponder** (`get_auto_responder`, prerequisito Fase 2B),
  **regole filtri complete** (`get_filter`, prerequisito 2B — oggi solo conteggi;
  rivedere la postura redaction: le regole servono per il round-trip).
- **1C Collector batch 2** (1-2 PR, solo se il censimento 0.3 li giustifica):
  directory privacy, hotlink/leech, MIME/handlers custom, git repos,
  passenger apps, BoxTrapper, alias come campo dedicato.

## FASE 2 — Config Apply: i writer user-level (cuore della richiesta)

Contratto comune OBBLIGATORIO per ogni writer (mutuato dal design 6D):
piano offline → dry-run default → `--yes-apply-writes` → backup pre-write →
apply idempotente → verify post-apply → emendamento CONSAPEVOLE dei safety
test (allowlist per file). Mai delete di risorse destination-only. Ordine
per dolore-se-dimenticato (dal censimento 0.3, salvo riordino):

- **2A cron apply** (1-2 PR): scrittura crontab via SSH sul dest con backup
  del crontab esistente, adattamento path `/home/<olduser>` → `/home/<newuser>`,
  verify rileggendo. Il caso simbolo del "dimenticarsi qualcosa".
- **2B email-config apply** (2-3 PR): forwarders, autoresponders (con body),
  default address, filtri (round-trip `get_filter`→`store_filter`),
  routing via API2 `setmxcheck`. Verify: ri-lettura e confronto col piano.
- **2C home-extras transfer** (1-2 PR): estensione del transfer (guardie
  webfiles riusate) per ~/.spamassassin/user_prefs, ~/.htpasswds (preserva le
  password della directory privacy!), ~/.ssh/authorized_keys (opt-in esplicito,
  azione di conferma in checklist).
- **2D FTP + webdisk** (1 PR): creazione account con password RIGENERATE,
  registrate nel dossier con flag `REGENERATED_PASSWORD` + azione checklist
  "comunicare nuove credenziali".
- **2E SSL custom + PHP** (1-2 PR): `show_cert`/`show_key`/cabundle →
  `install_ssl` SOLO per certificati non-AutoSSL (i Let's Encrypt li rifà
  AutoSSL, evitare rumore); `php_set_vhost_versions` con mapping preventivo
  delle versioni ea-php installate sul dest.
- **2F registrazioni** (1 PR, se censimento lo giustifica):
  `VersionControl::create` (adotta repo arrivati col transfer),
  `PassengerApps::register_application`, enable SpamAssassin, BoxTrapper config.

## FASE 3 — DNS apply (PR 6D)

Contratto già completo in PR6A_DNS_IMPORT_DESIGN.md (mass_edit_zone atomica
serial-guarded, mai delete, mai NS/SOA, backup-o-niente, rollback <60s).
Sessione dedicata con utente presente, zona sacrificale su principiadv.online,
protocollo CLAUDE.md completo. 6C (`dns verify --fail-on-drift`) è già il
certificatore post-apply.

## FASE 4 — Campagna 100+ account

- **4A accounts.yaml + runner di campagna** (1-2 PR): lista coppie
  src/dest, esecuzione SEQUENZIALE (mai parallela: carico sui server di
  produzione), directory artefatti per-account, resume per-account.
- **4B dashboard campagna** (1-2 PR): estensione UI — stato per account
  (pending / inventoried / applied / manual-open / cutover-done), azioni
  manuali aggregate cross-account.
- **4C dossier storico per account** (1 PR): archivio compresso di tutti gli
  artefatti + indice di campagna. È il "dato storico" richiesto: anche fra
  2 anni si deve poter rispondere "com'era configurato quell'account?".
- **4D credenziali di massa**: decisione operativa (non PR): iniezione chiavi
  SSH per-account via root sui source, o vault password. Da risolvere in 0.3.

## FASE 5 — Motore (solo se Fase 0 lo giustifica)

- **5A PrestaShop 1.7+ discovery+rewrite** (`app/config/parameters.php`) —
  priorità ALTA per i nostri shop (shop.doctorbike.it, bikers-chivasso).
- **5B** azione dedicata `DB_HOST` remoto; **5C** follow-up LOW già censiti.

---

## Cosa NON faremo (esplicito)

- Nessun writer per aree che il censimento 0.3 mostra inutilizzate.
- Mai migrazione automatica di: password esistenti MySQL/FTP/webdisk/team
  (rigenerate e registrate), membri Mailman, segreti token, NS/registrar.
- Mai parallelismo di campagna sui server di produzione.
- Mai scrittura sul SOURCE, in nessuna fase, per nessun motivo.

## Stima brutale e trade-off di campagna

Al ritmo tenuto finora (1-2 PR a sessione, review multi-giro): Fase 0 ≈ 2
sessioni, Fase 1 ≈ 2-3, Fase 2 ≈ 4-6, Fase 3 ≈ 2, Fase 4 ≈ 3-4.
**Totale ≈ 13-17 sessioni.**

Trade-off da decidere: .78 è pronto e la migrazione preme. Il minimo
indispensabile per migrare BENE è **Fase 0 + 1A/1B + 2A/2B + Fase 3**
(≈ 7-9 sessioni); 2C-2F e la Fase 4 completa sono automazione di comfort che
può procedere DURANTE la campagna, usando nel frattempo la checklist per le
parti non ancora automatizzate. Non ritardare la campagna per automatizzare
ciò che la checklist già rende sicuro — il tool esiste per non dimenticare
nulla, non per fare tutto da solo.

## Decisioni aperte (utente)

1. Quando eseguire 0.2 (primo apply reale) e su quale account sacrificale.
2. Meccanismo credenziali di massa per 0.3/campagna (chiavi iniettate vs vault).
3. Taglio minimo vs piano completo rispetto alla data di partenza campagna.
4. Postura redaction sulle regole filtri email (servono in chiaro per il
   round-trip 2B — oggi il collector salva solo conteggi per design).
