# SSH Host Identity Persistence

> Migration `0011` · tabella `endpoint_ssh_host_keys` · API `/api/endpoints/{id}/ssh-host-key`
> **Sola persistenza + API.** Nessuna connessione, nessun `ssh-keyscan`, nessun TOFU, nessun
> `known_hosts`, nessun `host.yaml`, nessun subprocess. Il runtime è un incremento successivo.

## Perché

Il motore Go verifica la host key SSH via TOFU su `~/.ssh/known_hosts` (`internal/sshx/hostkey.go`):
accetta la prima chiave vista, rifiuta un cambio. In un worker containerizzato `known_hosts` è
**effimero** → il TOFU degrada ad "accetta qualunque chiave a ogni run", che è esattamente ciò che un
man-in-the-middle sfrutta. Il pin dà alla piattaforma un'identità dell'host **durevole** da cui un
runtime futuro costruirà il `known_hosts`, così un cambio di host key diventa un rifiuto anziché un
"accetta di nuovo".

Questa PR memorizza solo il pin. Non lo usa.

## Formato di input accettato

Il client invia **solo** la chiave pubblica OpenSSH su **una riga**:

```
<algorithm> <base64>
```

Esempio: `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA…`

- **Il payload deve contenere esattamente due token.** Commenti, seconde chiavi e qualsiasi contenuto
  aggiuntivo sono **rifiutati** (non scartati): il parser di `cryptography` àncora solo all'inizio della
  riga e tratterebbe tutto ciò che segue come commento ignorato, così una seconda chiave o un payload
  arbitrario potrebbero viaggiare non visti — per questo si richiedono esattamente due token.
- Host, porta e fingerprint **non** sono campi del payload (`SshHostKeyUpsert` è `extra='forbid'`):
  sono decisi lato server. Un `host`/`port`/`fingerprint` inviato dal client è un **422**.
- La forma `ssh-keyscan` (`host algorithm base64`) è **rifiutata**: il token host iniziale non è un
  algoritmo valido, quindi il parser la respinge. Il client non fornisce l'host, e non deve poterne
  contrabbandare uno. (Scelta deliberata: accettare solo la chiave è la superficie più piccola e sicura.)
- Input multi-riga, vuoto, oltre `MAX_HOST_KEY` (8 KiB), non parsabile, materiale di **chiave privata**
  (marker `PRIVATE KEY-----`) o tipo **DSA `ssh-dss`** (deprecato/debole) → **rifiutati**.

## Parsing, canonicalizzazione, fingerprint

`packages/adapters/adapters/ssh_host_keys.py` (`parse_host_key`), puro, senza rete:

1. Parse **reale** con `cryptography.serialization.load_ssh_public_key` — non una regex: distingue una
   chiave valida da una ben formata ma falsa, e rifiuta un algoritmo incoerente col blob.
2. Ri-serializza nella forma canonica `algorithm base64` (senza commento) via
   `public_bytes(OpenSSH, OpenSSH)`. È questa forma che viene persistita.
3. `key_type` = l'algoritmo canonico (`ssh-ed25519`, `ssh-rsa`, `ecdsa-sha2-nistp256`, …).
4. Fingerprint = **standard OpenSSH SHA-256**:
   `SHA256:` + base64(`sha256(blob)`) **senza padding `=`**, dove `blob` è la parte base64 decodificata.
   Verificato contro `ssh-keygen -lf` con un known-answer vector nei test (non tautologico).
5. Gli errori sono **generici**: non riportano mai il materiale ricevuto.

## Integrità della riga persistita

I CHECK del DB sono **solo di formato** (non-blank, prefisso `SHA256:`): non possono dimostrare che la
fingerprint derivi realmente dalla chiave, né che il tipo o la forma siano coerenti. Una riga scritta
fuori dall'API può quindi avere coordinate corrette ma `public_key` invalida/non canonica, `key_type`
arbitrario o `fingerprint_sha256` formalmente valida ma **falsa**.

Una trust configuration è valida **soltanto** quando **tutte** queste condizioni sono vere:

- coordinate coerenti (`pin.host == endpoint.host`, `pin.port == endpoint.ssh_port`);
- `public_key` è una singola chiave OpenSSH pubblica **parsabile**;
- `public_key` è già nella **forma canonica** restituita dal parser;
- `key_type` coincide col tipo **derivato** dalla chiave canonica;
- `fingerprint_sha256` coincide con la fingerprint **ricalcolata** dalla chiave canonica.

Il validatore puro `validate_persisted_host_key(public_key, key_type, fingerprint_sha256)` (adapter)
ridereiva tutto tramite `parse_host_key` (unica autorità, nessuna duplicazione di parsing/fingerprint) e
solleva `InvalidPersistedHostKey` — generica, senza materiale, senza cause incatenata — su qualunque
incoerenza. `InvalidPersistedHostKey` è **distinta** da `InvalidHostKey` (input client invalido → 422):
è una riga già persistita ma non affidabile.

Conseguenze:

- la `GET` **fallisce closed con 404** su ogni incoerenza (coordinate o crittografia); verificare solo
  host e porta **non è sufficiente**;
- la riga corrotta **non** viene corretta, riscritta o cancellata automaticamente: resta disponibile per
  diagnosi amministrativa, invariata (`updated_at` incluso);
- il **futuro runtime SSH deve usare lo stesso validatore condiviso**: legge endpoint+pin in modo
  coerente (con lock), verifica host/porta, invoca `validate_persisted_host_key`, e materializza un
  `known_hosts` **solo dopo** il successo.

## Modello dati (`endpoint_ssh_host_keys`)

| Colonna | Note |
|---|---|
| `endpoint_id` | FK → `endpoints.id` `ON DELETE CASCADE`; **unique** (un solo pin per endpoint) |
| `host`, `port` | **snapshot server-side** delle coordinate SSH al momento del pin (`endpoint.host` + `endpoint.ssh_port`) |
| `key_type`, `public_key` | tipo e forma canonica `algorithm base64` |
| `fingerprint_sha256` | fingerprint OpenSSH, formato `SHA256:…` |
| `created_at`, `updated_at` | timestamp |

CHECK nominati (difesa in profondità, il runtime leggerà la riga come verità):
`ck_endpoint_ssh_host_key_port_range` (1–65535), `_host_nonblank`, `_key_type_nonblank`,
`_public_key_nonblank`, `_fingerprint_format` (`LIKE 'SHA256:_%'`), più
`uq_endpoint_ssh_host_key_endpoint`. **Non** è una FK composita verso le coordinate (fragile contro
coordinate mutabili): la coerenza è validata fail-closed nel service e va **ri-verificata dal runtime**.

## API

| Verbo | Path | Semantica |
|---|---|---|
| `GET` | `/api/endpoints/{id}/ssh-host-key` | 404 se endpoint o pin assente; **fail-closed** su pin stale (vedi sotto) |
| `PUT` | `/api/endpoints/{id}/ssh-host-key` | pin/replace; 409 se SSH non configurato; 422 se chiave invalida |
| `DELETE` | `/api/endpoints/{id}/ssh-host-key` | 204; **idempotente** (204 anche se il pin è già assente, purché l'endpoint esista) |

`PUT`: valida/canonicalizza la chiave **prima** del lock (nessun crypto sotto lock), poi prende il lock
di riga sull'endpoint (`FOR UPDATE`), verifica che l'SSH sia configurato (`ssh_auth_method != none` **e**
`ssh_port != null`, altrimenti 409), **deriva host/porta dalla riga endpoint sotto lock**, fa l'upsert
dell'unico pin e committa in **una** transazione. Nessun probe.

## Invalidazione

Legata a `host + ssh_port`. Nella **stessa transazione** della modifica delle coordinate:

| Evento | Pin |
|---|---|
| `endpoint.host` cambia (`PATCH /api/endpoints/{id}`) | **eliminato** |
| `ssh_port` effettivo cambia (`PUT …/ssh-credentials`) | **eliminato** |
| `ssh_auth_method` → `none` | **eliminato** |
| cancellazione dell'endpoint | **eliminato** (FK CASCADE) |
| rotazione password / chiave privata / passphrase / sorgente / username SSH, **stessa porta** | **preservato** |
| cambio label / porta cPanel / username cPanel / token / `verify_tls` | **preservato** |

La host key è l'identità **del server**, indipendente da come ci autentichiamo: cambiare credenziale a
coordinate invariate non tocca il pin; cambiare il server (host) o la porta sì.

**GET fail-closed**: se il pin non supera il validatore completo — coordinate (`pin.host`/`pin.port` vs
endpoint) **o** integrità crittografica (chiave parsabile, canonica, tipo e fingerprint coerenti) — la
`GET` risponde **404** (uniforme: non distingue assente/stale/corrotto). La riga corrotta resta
invariata (vedi *Integrità della riga persistita*).

## Concorrenza

`set_ssh_host_key`, `update_endpoint` (quando può cambiare `host`) e `set_ssh_credentials` (quando può
cambiare `ssh_port`/`none`) **serializzano tutte sullo stesso lock di riga endpoint** (`FOR UPDATE`),
così nessun interleaving può lasciare un pin legato a coordinate che l'endpoint non ha più. Provato su
PostgreSQL reale (`test_endpoints_ssh_host_key_pg.py`, contention deterministica via `pg_stat_activity`,
mai `sleep` come sincronizzazione):

- **pin vs cambio host**: lo stato finale è `endpoint=nuovo host` con pin assente **oppure** pin sul
  nuovo host — mai pin sul vecchio host.
- **pin vs cambio porta SSH**: mai `endpoint port=2222` con pin `port=22`.
- **due PUT concorrenti**: una sola riga (last-writer-wins), nessun `IntegrityError` non gestito.

SQLite ignora `FOR UPDATE`: le proprietà concorrenti sono provate **solo** su Postgres.

## Threat model / invarianti

- **I1** un solo pin attivo per endpoint (unique DB).
- **I2–I4** host, porta e fingerprint sono **del server**: derivati/calcolati lato server sotto lock,
  mai forniti dal client.
- **I5–I7** cambio host / porta SSH / `none` invalidano il pin.
- **I8–I9** rotazione credenziale a coordinate invariate, e modifiche non-SSH dell'endpoint,
  **preservano** il pin.
- **I10** cancellazione endpoint → CASCADE.
- **I11** una riga incoerente scritta fuori dall'API **non** è affidabile: la `GET` la nasconde
  fail-closed, e **il runtime deve ri-verificare** sia le coordinate (`pin.host == endpoint.host`,
  `pin.port == endpoint.ssh_port`) sia l'integrità crittografica invocando lo **stesso validatore
  condiviso** `validate_persisted_host_key` prima di fidarsene. Le CHECK del DB sono solo di formato:
  non dimostrano che la fingerprint derivi dalla chiave.
- **I12** nessun errore/log ripete il materiale ricevuto (la host key è comunque pubblica, ma è input
  non fidato).

## Fuori scope (di questo modulo)

Nulla di tutto ciò è raggiungibile da questo codice: resta la persistenza e la sua API.

Costruzione di `known_hosts` dal pin, `host.yaml`, risoluzione dei ref SSH e decrypt dei segreti sono
ora implementati **altrove** — nel resolver e nel workspace builder, che riusano
`validate_persisted_host_key` esattamente come descritto in I11; vedi
[SSH_RUNTIME_WORKSPACE.md](SSH_RUNTIME_WORKSPACE.md). Restano fuori dalla piattaforma nel suo
complesso: subprocess del motore Go, TOFU, acquisizione automatica della host key, apply.

Una nota che il runtime ha reso concreta: il `public_key` è provato da `parse_host_key`, ma
**`endpoint.host` non ha un'autorità equivalente** — `_clean_host` toglie schema/userinfo/path/porta
e non vincola il charset. Un record `known_hosts` è delimitato da whitespace, quindi chi materializza
un `known_hosts` deve validare l'host (hostname bare o literal IP) prima di scriverlo, altrimenti un
host con uno spazio vi aggiunge un secondo record con una chiave arbitraria. Il builder lo fa.
