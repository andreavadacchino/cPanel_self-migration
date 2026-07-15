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
<algorithm> <base64> [commento-ignorato]
```

Esempio: `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA…`

- Il commento (se presente) viene **scartato** dalla canonicalizzazione.
- Host, porta e fingerprint **non** sono campi del payload (`SshHostKeyUpsert` è `extra='forbid'`):
  sono decisi lato server. Un `host`/`port`/`fingerprint` inviato dal client è un **422**.
- La forma `ssh-keyscan` (`host algorithm base64`) è **rifiutata**: il token host iniziale non è un
  algoritmo valido, quindi il parser la respinge. Il client non fornisce l'host, e non deve poterne
  contrabbandare uno. (Scelta deliberata: accettare solo la chiave è la superficie più piccola e sicura.)
- Input multi-riga, vuoto, oltre `MAX_HOST_KEY` (8 KiB), non parsabile, o materiale di **chiave
  privata** (marker `PRIVATE KEY-----`) → **rifiutati**.

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

**GET fail-closed**: se il pin esiste ma `pin.host != endpoint.host` o `pin.port != endpoint.ssh_port`
(coordinate mosse fuori dall'API, o riga scritta manualmente), la `GET` risponde **404** — non presenta
mai una riga stale come identità valida.

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
  fail-closed, e **il runtime deve ri-verificare** `pin.host == endpoint.host` e
  `pin.port == endpoint.ssh_port` prima di fidarsene.
- **I12** nessun errore/log ripete il materiale ricevuto (la host key è comunque pubblica, ma è input
  non fidato).

## Fuori scope (runtime, PR successiva)

Costruzione di `known_hosts` dal pin, `host.yaml`, risoluzione dei ref SSH, decrypt dei segreti,
subprocess del motore Go, TOFU, apply. Nulla di tutto ciò è raggiungibile da questo codice.
