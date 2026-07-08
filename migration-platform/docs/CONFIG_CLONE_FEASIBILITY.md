# Config Clone / Credential Preservation Feasibility

> Spike investigativo (read-only). Nessuna migrazione reale, nessun restore
> automatico, nessun backup generato. Questo documento risponde a una domanda
> precisa e distingue **ciò che è confermato dalla documentazione ufficiale**,
> **ciò che è solo dedotto**, e **ciò che richiede uno smoke reale**.

## Obiettivo

Con accesso a **password cPanel** del source (e/o **WHM reseller** sulla
destinazione), è possibile **preservare le credenziali esistenti**
(email / FTP / MySQL) **senza conoscerle in chiaro**, usando il meccanismo
ufficiale di **backup/restore** cPanel — invece di ricreare gli account via API
(che impone password nuove)?

In particolare: è fattibile una modalità **Restore-Assisted Config Clone**
(backup ufficiale → eventualmente senza homedir → restore reseller → verifica
credenziali)?

## Risposta in una riga

**Sì, ma non nel modo che sembrava più comodo.** Le credenziali possono viaggiare
**solo dentro un archivio di backup ufficiale ripristinato** sulla destinazione;
l'idea di **saltare l'homedir** per snellire l'archivio **non è disponibile con
accesso a livello di account** (richiede root) e per l'email è **auto-contraddittoria**
(gli hash delle password email stanno *dentro* l'homedir). Il percorso più pulito
realmente supportato è il **remote pull di WHM** (`Transfer from Remote cPanel
Account`), che si autentica con username+password del cPanel source e sposta
l'intero account. Ogni affermazione di "password preservate" resta **da
confermare con smoke reale** finché non testata.

---

## Contesto operativo

La piattaforma oggi (fino a PR #99) parla **cPanel UAPI read-only** con un
**API token** sulla porta **2083** (`AuthType.TOKEN`, cifrato Fernet a riposo).
Il client (`adapters/cpanel/client.py`) è deliberatamente **solo lettura**
(*"there is no write helper"*). Le capability sono tutte `can_read_*`.

Definiamo quattro **access profile** in ordine crescente di potere:

| Profilo | Cosa possediamo | Cosa parla |
|---|---|---|
| `token_only` | API token cPanel | UAPI/API2 read-only (attuale piattaforma) |
| `token_plus_cpanel_password` | token + **password cPanel** dell'account | UAPI + interfaccia Backup + può essere *pullato* da remoto |
| `whm_reseller` | account reseller su WHM destinazione | può ricevere/ripristinare account (se ACL abilitate) |
| `root_whm` | root/WHM | tutto: `pkgacct`, Transfer Tool, restore completo |

## Problema da risolvere

Preservare le credenziali **senza conoscerle in chiaro**. Vietato: leggere/estrarre
hash, copiare file interni (`shadow`, `proftpdpasswd`, `digestshadow`), fare
parsing manuale del tarball. L'unico meccanismo lecito è **far fare a cPanel**
backup + restore ufficiale e poi **verificare** con la nostra piattaforma
(inventory/comparison) e con login di prova.

---

## Strategie analizzate

### 1. API Rebuild Mode (baseline attuale)

- **Cosa può fare:** legge l'inventory del source via UAPI e ricrea gli oggetti
  sulla destinazione tramite API ufficiali (domini, email, DB, utenti MySQL, …).
- **Cosa NON può fare:** **non può preservare alcuna password**. Ogni account
  email/FTP e ogni utente MySQL vanno creati con una **password nuova**
  (fornita dall'operatore). Le API di creazione richiedono una password in
  input; non esiste "crea con l'hash esistente" lecito.
- **Impatto password:** **nessuna preservazione** → `operator_supplied_only`
  (o `unavailable` con solo token). Per migrazioni massive significa cambiare
  le password a molti clienti → disservizio. È esattamente ciò che vogliamo
  evitare.

### 2. Full Backup Restore

- **Cosa può fare:** genera un **backup completo dell'account** e lo ripristina
  sulla destinazione. L'archivio contiene i dati dell'account **inclusi gli hash
  delle credenziali** (email/FTP/MySQL) → il restore li **ripristina**.
- **Requisiti:** un modo per generare il backup sul source (password cPanel →
  UAPI `Backup::fullbackup_to_*`, oppure root `pkgacct`) **e** una destinazione
  che sappia **ripristinare** un archivio cPanel (reseller abilitato o root).
- **Limiti spazio:** l'archivio **include l'homedir** (posta + `public_html` +
  dump DB) → può essere enorme; serve spazio di staging sia sul source sia sulla
  destinazione.

### 3. Remote Backup / Backup Shuttle

Con la **password cPanel** l'account può spedire il proprio backup altrove
(UAPI Backup module):

- `Backup::fullbackup_to_ftp` — FTP
- `Backup::fullbackup_to_scp_with_password` — SCP con password
- `Backup::fullbackup_to_scp_with_key` — SCP con chiave
- `Backup::fullbackup_to_homedir` — nell'homedir dell'account

**Limiti (confermati):** **nessuna** di queste funzioni accetta un parametro per
**escludere l'homedir** → a livello di account produci **solo un full backup**.

### 4. homedir=skip / Config Clone

- **Cosa promette:** un archivio "config-only" piccolo (niente posta/web/dati),
  con sola configurazione + credenziali, da restorare velocemente.
- **Cosa NON sappiamo / cosa sappiamo adesso:**
  - **Skip-homedir esiste solo a livello root** (`pkgacct --skiphomedir`), **non**
    nel modulo UAPI Backup account-level → **non disponibile** con token/password.
  - **Auto-contraddizione per l'email:** gli **hash delle password email** stanno
    **dentro** l'homedir (`~/etc/<domain>/shadow`) → saltando l'homedir si
    **perdono** le credenziali email. Il "config clone" **non** preserva le
    password email a meno di trasportare separatamente la config mail.
- **Rischio:** costruire una promessa ("clone leggero che preserva le password")
  che per l'email è **falsa**. **Sconsigliato** come scorciatoia di preservazione.

### 5. Transfer from Remote cPanel Account (remote pull) — *il candidato migliore*

- **Requisiti:** WHM sulla destinazione (root **oppure** reseller con
  l'interfaccia abilitata); sul source basta **username + password del cPanel**.
- **Reseller/root:** il **Transfer Tool** (multi-account) **richiede root**; la
  singola **"Transfer or Restore a cPanel Account"** può essere **abilitata ai
  reseller**.
- **Ipotesi di preservazione credenziali:** sposta l'**intero account** (homedir
  inclusa) con gli hash intatti → email/FTP/MySQL **plausibilmente preservati**.
  L'unica eccezione **ufficialmente documentata** come non trasferita è la **2FA**.
- **Costo:** trasferisce l'homedir (nessuno snellimento). La preservazione resta
  **da confermare con smoke reale** (soprattutto hash MySQL, daemon FTP diversi,
  versioni cPanel diverse).

---

## Tabella capability

| Capability | `token_only` | `token_plus_cpanel_password` | `whm_reseller` | `root_whm` | Note |
|---|---|---|---|---|---|
| Leggere inventory (UAPI RO) | ✅ | ✅ | ✅ | ✅ | Piattaforma attuale |
| Generare full backup account | ⚠️ tecnicamente via UAPI, ma non lo facciamo | ✅ UAPI `Backup::fullbackup_*` | ✅ | ✅ `pkgacct` | Operazione stateful: non auto-eseguita |
| Backup remoto (FTP/SCP) | ⚠️ | ✅ | ✅ | ✅ | Password cPanel sufficiente |
| **Skip homedir** (config-only) | ❌ | ❌ (no param UAPI) | ❌ a livello account | ✅ `pkgacct --skiphomedir` | Solo root; e perde hash mail |
| Ricevere/ripristinare archivio | ❌ | ❌ | ✅ se ACL abilitate | ✅ | Serve WHM sulla destinazione |
| Transfer Tool (multi-account) | ❌ | ❌ | ❌ (root-only) | ✅ | Doc: richiede root/sudo/su |
| Remote pull (auth con pwd source) | ❌ | ✅ come *sorgente* del pull | ✅ come destinazione | ✅ | Il path più pulito |

Legenda: ✅ supportato · ⚠️ possibile ma non usato/consigliato · ❌ non disponibile.

## Credential Preservation Matrix

| Categoria | API rebuild | Backup homedir=include | Backup homedir=skip | Remote cPanel transfer | Note |
|---|---|---|---|---|---|
| **Password account email** | ❌ nuova password | 🟡 possibile (hash in `~/etc/<dom>/shadow`) — smoke | ❌ **persa** (homedir esclusa) | 🟡 possibile — smoke | Skip-homedir auto-contraddittorio |
| **Password account FTP** | ❌ nuova password | 🟡 possibile — smoke | ⚠️ dipende (config in `~/etc`) | 🟡 possibile — smoke | Daemon FTP diversi = rischio hash |
| **Password utente MySQL** | ❌ nuova password | 🟡 possibile (dump `mysql/` + grant) | 🟡 possibile (dump fuori da homedir) | 🟡 possibile — smoke | **Fedeltà hash = rischio più alto** |
| **GRANT MySQL** | ⚠️ ricreabili via API | 🟡 possibile — smoke | 🟡 possibile — smoke | 🟡 possibile — smoke | |
| **Password account cPanel (sistema)** | ❌ nuova | 🟡 nel meta account — smoke | 🟡 nel meta account — smoke | 🟡 possibile — smoke | Fuori scope preservazione end-user |
| **Secret API/app (dentro i file)** | ❌ | ✅ se homedir inclusa | ❌ persi | ✅ se homedir inclusa | Vivono nei file dell'homedir |
| **2FA** | ❌ | ❌ **non trasferita (ufficiale)** | ❌ | ❌ **non trasferita (ufficiale)** | Gli utenti devono ri-registrarsi |

Legenda: ✅ atteso preservato · 🟡 plausibile ma **da confermare con smoke** · ⚠️ condizionato · ❌ non preservato.

---

## Cosa è confermato da documentazione ufficiale

1. **Full backup con sola password cPanel** via UAPI `Backup::fullbackup_to_homedir`
   / `_to_ftp` / `_to_scp_with_password` / `_to_scp_with_key`, endpoint
   `https://host:2083/execute/Backup/fullbackup_to_*`. Titolo ufficiale
   dell'operazione: *"Back up cPanel account to home directory"*.
2. **Skip-homedir esiste solo a livello root** via `pkgacct --skiphomedir`
   (anche `--skipmail`), pensato per "trasferire /home con un altro metodo".
   `--userbackup` produce file compatibili con "Transfer or Restore a cPanel
   Account".
3. **Il Transfer Tool (multi-account) richiede root**; la singola **"Transfer or
   Restore a cPanel Account"** può essere **abilitata ai reseller**.
4. **Remote pull** ("Transfer from Remote cPanel Account") si autentica con
   **Remote username + Remote password = credenziali del cPanel source**; la
   destinazione richiede WHM.
5. **Solo la 2FA è documentata come NON trasferita** dal restore/transfer — il che
   implica che il resto (credenziali incluse) è pensato per essere trasferito.
6. **Requisiti di spazio:** la directory di staging deve contenere il file di
   backup più grande da ripristinare; WHM ha un controllo "Check the Available
   Disk Space".

Fonti ufficiali (vedi elenco in fondo).

## Cosa è solo deduzione

- Che le password **email/FTP/MySQL** e i **GRANT** sopravvivano al restore
  perché i loro hash stanno dentro l'archivio (dedotto da: struttura archivio di
  fonti community + la singola eccezione documentata = 2FA). **Non** c'è una
  frase ufficiale che lo dica testualmente.
- La **struttura del tarball** e i **path esatti** dei file credenziali
  (`~/etc/<domain>/shadow`, `mysql/*.create`, proftpd/pureftpd, `cp/`, `dnszones/`,
  `sslcerts/`, …) provengono da fonti community, non da una pagina ufficiale
  corrente.
- La regola "~2× lo spazio del più grande account" è da KB di hosting provider,
  non da cPanel.

## Cosa richiede smoke reale

- **Il conflitto centrale del "Config Clone":** skip-homedir richiede **root**;
  con la sola password cPanel via API produci **solo full backup**. "Saltare
  l'homedir per evitare l'archivio enorme" **non è ottenibile** con accesso a
  livello di account.
- **Interazione mail + skip-homedir:** verificare se `pkgacct --skiphomedir`
  emette comunque `~/etc` (config mail) o se le password email si perdono.
- **Fedeltà 1:1 degli hash utente MySQL** attraverso restore (versioni cPanel /
  auth plugin diversi). **Rischio più alto.**
- **ACL reseller minime** effettive per ripristinare un account completo, e
  limiti di numero account/pacchetto.
- **Login reali** post-restore con le vecchie password (email/FTP/MySQL).

---

## Cosa NON dobbiamo fare

- ❌ **Parsing manuale del tarball** di backup.
- ❌ **Copiare/leggere** file interni: `shadow`, `proftpdpasswd`, `digestshadow`,
  `passwd`, dump grant.
- ❌ **Estrarre/manipolare hash** o password interne.
- ❌ Usare il tarball come **integration endpoint stabile**.
- ❌ Salvare password cPanel/email/FTP/MySQL **in chiaro** o gli hash.
- ❌ Generare **backup reali** in modo automatico nei test; niente full backup
  senza flag di smoke esplicito.
- ❌ Dichiarare "password preservate" **senza smoke reale**.

Il principio: **facciamo fare a cPanel** backup + restore ufficiale; noi solo
**orchestriamo e verifichiamo** (inventory/comparison + login di prova). Mai
toccare il materiale credenziale.

---

## Smoke test plan

**Prerequisito:** account **sacrificabile**, credenziali fuori repo (solo env
locali non committati), autorizzazione esplicita. Coppia reale suggerita:
`.193` (source) → `.78` (destination).

Account di prova con oggetti noti:
- 1 account email con password nota
- 1 account FTP con password nota
- 1 database + 1 utente MySQL con password nota
- 1 forwarder, 1 autoresponder, 1 cron semplice, piccolo `public_html` dummy

Sequenza:

| # | Passo | Cosa verifica |
|---|---|---|
| A | Full backup remoto **con homedir** (UAPI `Backup::fullbackup_to_scp_*`) | Generazione backup con password cPanel |
| B | Backup **senza homedir** (`pkgacct --skiphomedir`, richiede root) | Cosa resta/cade senza homedir (mail!) |
| C | Restore su destinazione/reseller ("Transfer or Restore a cPanel Account") | Ripristino reseller/root |
| D | Inventory post-restore (piattaforma) | Oggetti ricreati |
| E | Comparison post-restore (piattaforma) | Config allineata source↔dest |
| F | Login **email** con la **vecchia** password | Preservazione credenziali email |
| G | Login **FTP** con la **vecchia** password | Preservazione credenziali FTP |
| H | Connessione **MySQL** con la **vecchia** password | Preservazione credenziali MySQL |

Confronto atteso: **A** (full) dovrebbe preservare F/G/H; **B** (skip-homedir)
dovrebbe **fallire F** (email) e mettere alla prova G. Se F fallisce con B → il
"config clone" **non** è un percorso di preservazione email.

> **Real-smoke non eseguito in questo spike:** account/credenziali sacrificabili
> non disponibili in-repo e nessuna autorizzazione esplicita fornita. Vedi PR.

---

## Decisione consigliata

1. **Baseline invariata:** la strategia di default resta **API rebuild**
   (read + ricrea). È onesta: **non preserva** le password → l'operatore le
   reimposta/fornisce. Nessuna falsa promessa.
2. **Strategia avanzata opt-in:** **remote-pull full-account restore** (D11)
   quando la destinazione ha WHM (root o reseller abilitato) e possediamo la
   **password cPanel** del source. È l'**unico** percorso ufficialmente
   supportato che **plausibilmente** preserva email/FTP/MySQL, al costo del
   trasferimento dell'homedir. **Gated dietro smoke reale.**
3. **Non perseguire** il "config clone via skip-homedir" come scorciatoia di
   preservazione credenziali: richiede root sul source, **perde** le password
   email e per l'email è auto-contraddittorio. Se serve snellire l'homedir, è
   un'ottimizzazione di **trasferimento dati** (es. rsync separato), **non** una
   scorciatoia sulle credenziali.
4. **Mai** parsing tarball / copia hash / manipolazione file interni.

## Impatto sulla roadmap

Questo spike **non** modifica il Migration Plan. In futuro, il modello puro
`domain/migration_strategy.py` (aggiunto qui) alimenterà due nuove sezioni del
piano, calcolate da un futuro **capability probe** (backup/restore) senza
generare nulla:

```json
{
  "credential_preservation": {
    "email_accounts": "possible_requires_smoke",
    "ftp_accounts": "possible_requires_smoke",
    "mysql_users": "possible_requires_smoke",
    "recommended_strategy": "restore_assisted_config_clone"
  },
  "strategy_recommendation": { "recommended_strategy": "full_account_restore", "reason": "…" }
}
```

Punti di aggancio identificati nel codice:

- **Access profile:** oggi `AuthType` (endpoints/models.py) ha già lo slot
  `PASSWORD_REF` non cablato → base per `token_plus_cpanel_password`.
- **Capability probe backup/restore:** `CapabilityReport` (adapters/inventory.py)
  ha solo `can_read_*`; andranno aggiunti campi `can_generate_full_backup`,
  `can_skip_homedir`, `can_restore_cpanel_account`, `has_whm_reseller` — popolati
  da un probe **read-only** (mai generando backup).
- **Strategy recommendation nel piano:** `build_migration_plan`
  (domain/migration_plan.py) è il punto dove innestare
  `sections.strategy_recommendation` / `sections.credential_preservation`,
  usando `recommend_strategy(capabilities)`.

---

## Modello di dominio aggiunto

`packages/domain/domain/migration_strategy.py` — puro, senza DB/rete/FastAPI:

- `AccessProfile` = { `token_only`, `token_plus_cpanel_password`, `whm_reseller`, `root_whm` }
- `MigrationStrategy` = { `api_rebuild`, `restore_assisted_config_clone`, `full_account_restore`, `root_transfer`, `hybrid`, `unknown` }
- `CredentialPreservation` = { `unavailable`, `operator_supplied_only`, `possible_requires_restore`, `possible_requires_smoke`, `confirmed_by_smoke`, `not_supported` }
- `recommend_strategy(capabilities: dict) -> dict` → `{recommended_strategy, credential_preservation, reason}`

**Regola d'onestà del modello:** pre-smoke il verdetto più forte è
`possible_requires_smoke`; solo uno smoke eseguito può promuovere a
`confirmed_by_smoke` (promozione **non** fatta dal modello).

---

## Fonti ufficiali consultate

- The pkgacct Script — https://docs.cpanel.net/whm/scripts/the-pkgacct-script/ (`--skiphomedir`, `--skipmail`, `--userbackup`)
- Transfer or Restore a cPanel Account — https://docs.cpanel.net/whm/transfers/transfer-or-restore-a-cpanel-account/ (remote pull con pwd account; reseller; solo 2FA non trasferita)
- Transfer Tool — https://docs.cpanel.net/whm/transfers/transfer-tool/ (richiede root/sudo/su)
- Backup Configuration — https://docs.cpanel.net/whm/backup/backup-configuration/ (cosa viene salvato; staging/spazio disco)
- Edit Reseller Nameservers and Privileges — https://docs.cpanel.net/whm/resellers/edit-reseller-nameservers-and-privileges/ (ACL: `create-acct`, `file-restore`)
- UAPI Backup::fullbackup_to_homedir — https://api.docs.cpanel.net/openapi/cpanel/operation/fullbackup_to_homedir/ (titolo operazione confermato; SPA JS, corpo non renderizzato)

**Fonti non ufficiali** (usate solo come deduzione, da verificare): mirror CLI di
terze parti (cpanel-cli.readthedocs.io) e KB di hosting provider/community per la
struttura del tarball e i requisiti di spazio. **Non** una pagina ufficiale
corrente enumera l'albero del tarball → i path dei file credenziali sono
**dedotti**, da verificare estraendo un archivio reale.
