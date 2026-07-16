# SSH runtime resolver + workspace builder

Come la piattaforma trasforma una riga `endpoints` + il suo pin in un workspace
effimero che il motore Go potrà un giorno leggere. **Nessun subprocess, nessuna
rete, nessuna connessione SSH, nessun `ssh-keyscan`, nessun TOFU, nessun apply.**
Questo incremento costruisce file e li cancella; non autorizza nulla.

## Il confine

Tre componenti, deliberatamente separati:

| | Dove | Responsabilità | Vietato |
|---|---|---|---|
| **A. Loader** | `apps/worker/worker/ssh_runtime.py` | legge endpoint+pin sotto lock, prova il pin, produce il DTO | scrivere su disco |
| **B. Resolver** | `packages/adapters/adapters/ssh_runtime.py` | valida la coerenza della riga, decifra/risolve i segreti in memoria | DB, disco, rete |
| **C. Builder** | `packages/adapters/adapters/ssh_workspace.py` | crea directory/file, `known_hosts`, `host.yaml`, cleanup | DB, rete, subprocess |

La direzione degli import resta `worker → adapters` e `API → adapters`; **mai
`API → worker`**. Il loader vive nel worker perché usa `worker/db.py`; il
resolver e il builder vivono negli adapter perché il futuro executor li riuserà.

## Matrice dei metodi SSH

Il DB **non** garantisce la coerenza fra `ssh_auth_method`, `ssh_secret_source` e
le sei colonne dei segreti: le CHECK coprono l'enum, il range della porta e
«`none` è vuoto», e il bundle Pydantic che impone la coerenza gira **solo in
scrittura**. Una riga scritta fuori dall'API è quindi arbitraria. Il resolver la
rifiuta prima di decifrare qualsiasi cosa.

| `ssh_auth_method` | Richiede | Rifiuta |
|---|---|---|
| `none` | — | sempre: nessun workspace |
| `password` | username, porta 1-65535, sorgente valida, **esattamente una** password coerente con la sorgente | qualsiasi private key, qualsiasi passphrase |
| `private_key` | username, porta, sorgente valida, **esattamente una** chiave coerente con la sorgente; passphrase opzionale **con la stessa sorgente** | qualsiasi password |

Per ogni segreto: `direct` esige il ciphertext e **vieta** il ref; `ref` esige il
ref e **vieta** il ciphertext. Direct+ref insieme = riga ambigua → rifiutata, mai
risolta per precedenza. Valore risolto vuoto o solo-whitespace → rifiutato. Una
riga incoerente **non viene riparata né cancellata**: leggere non muta.

La presenza di una passphrase non è presa come prova che la chiave sia cifrata:
lo decide il parser (`load_private_key_or_raise`), qui, dove l'errore è generico
— non nello stderr del motore dopo l'avvio di un subprocess.

## Provider dei riferimenti

**Solo `env://`**, riusando `adapters/credentials.resolve_credential` **così
com'è**. Questa PR non aggiunge provider e **non allarga l'allowlist**.

Vincolo ereditato e reale: l'allowlist Sprint 2 esige un identificatore maiuscolo
che **contenga `CPANEL`** (`credentials.py:41-42`). Quindi `env://SOURCE_CPANEL_SSH_PASSWORD`
risolve, `env://SOURCE_SSH_PASSWORD` **no**. Nota che gli schemi accettati in
scrittura (`schemas.py:165`: `vault://`, `secretsmanager://`, `env://`, `ref://`)
sono più larghi di quelli risolvibili: `vault://` e `secretsmanager://` falliscono
qui con un errore esplicito «non implementato». È una divergenza **pre-esistente**
fra il write path e il resolver, documentata e non modificata in questo
incremento.

Esclusi esplicitamente: `file://`, Vault, AWS/GCP Secrets Manager, provider via
shell, HTTP, URL generiche, lookup arbitrario del filesystem.

## Lettura coerente

Il pin **non** è legato alle coordinate dell'endpoint da una foreign key — sono
mutabili, e una FK composita sarebbe fragile. Quindi «questo pin appartiene a
questo host e a questa porta» è vero **solo nel momento di una lettura coerente**.

Il loader prende il lock di riga dell'endpoint (`SELECT … FOR UPDATE`), lo stesso
che prendono `set_ssh_host_key`, `update_endpoint` e `set_ssh_credentials`, e
legge **entrambe** le righe dentro il lock. Un cambio di coordinate concorrente o
atterra prima (e il pin è già stato invalidato) o dopo (e lo snapshot era vero
quando è stato preso). Non può mai risultare «host nuovo + pin vecchio».

**Sotto il lock**: due SELECT, il confronto delle coordinate, la prova
crittografica del pin. **Non** la risoluzione delle credenziali: decifrare una
chiave privata esegue `bcrypt_pbkdf`, il cui costo lo sceglie chi ha generato la
chiave (`ssh-keygen -a 1000` = secondi, misurati), e tenere la riga per quel tempo
bloccherebbe i writer dell'API sulla stessa riga. Le colonne vengono copiate sotto
il lock e risolte fuori. Il workspace viene costruito dopo, per la stessa ragione.

SQLite ignora `FOR UPDATE`: la serializzazione è provata su PostgreSQL reale
(`worker/tests/test_ssh_runtime_pg.py`), con la contesa osservata via
`pg_stat_activity` — mai una sleep. Verificato per mutazione: togliendo
`.with_for_update()` i test diventano rossi.

## Riuso di `validate_persisted_host_key`

Resta **l'unica autorità crittografica** sul pin persistito. Il loader:

1. confronta `pin.host == endpoint.host` e `pin.port == endpoint.ssh_port` (la
   metà che richiede le righe, quindi del chiamante);
2. invoca `validate_persisted_host_key(public_key=…, key_type=…, fingerprint_sha256=…)`
   per il resto (parsabilità, forma canonica, tipo, fingerprint ricalcolata).

Le CHECK del DB sono solo di formato: non provano che la fingerprint derivi dalla
chiave. `SSH_HOST_IDENTITY.md` I11 impone esattamente questi due passi. L'API fa
lo stesso in `validate_ssh_host_key_pin`, ma su oggetti ORM — e il worker non
importa l'app FastAPI: la semantica è rispecchiata, l'autorità è condivisa.

| Stato del pin | Esito |
|---|---|
| assente | `SshHostIdentityError` |
| coordinate stale (host o porta) | `SshHostIdentityError` |
| chiave non parsabile / non canonica | `SshHostIdentityError` |
| `key_type` incoerente | `SshHostIdentityError` |
| fingerprint non derivata dalla chiave | `SshHostIdentityError` |

Verdetto uniforme di proposito: la risposta operativa è la stessa in tutti i casi
(ri-pinnare la chiave) e distinguere descriverebbe la riga al chiamante.

## Struttura del workspace

```
<runtime_root>/migration-ssh-XXXXXXXX/   0700   ← nome da mkdtemp (randomness di sistema)
├── host.yaml                            0600
├── source_key                           0600   ← solo in modalità private_key
├── dest_key                             0600   ← solo se anche la dest usa una chiave
└── .ssh/                                0700
    └── known_hosts                      0600
```

`known_hosts` sta in `<root>/.ssh/` perché **il motore non ha un campo di
configurazione per il known_hosts**: `internal/sshx/pool.go:49-58` lo deriva da
`os.UserHomeDir()` e ogni call site di produzione passa un path vuoto. Il futuro
executor punterà `HOME` alla root del workspace. È l'unico modo di consumarlo
**senza toccare il codice Go di produzione**.

Nomi file **costanti**, mai derivati da host, username, label o altre colonne. Il
nome della directory non contiene nulla della riga. I file sono creati con
`O_CREAT|O_EXCL|O_NOFOLLOW` + `fchmod`: `O_EXCL` impedisce di sovrascrivere un
file piantato, `O_NOFOLLOW` impedisce che un symlink rediriga la scrittura (una
chiave privata finirebbe dove punta il link), `fchmod` riporta a 0600 anche sotto
una umask **restrittiva** (una umask permissiva non può allargare 0600: azzera
soltanto).

Il **runtime root** è rifiutato (`WorkspaceSecurityError`) se è un symlink, non è
una directory, non è scrivibile, o è scrivibile oltre il proprietario senza
sticky bit — `/tmp` è `1777` e va bene; un `0770` senza sticky no, perché il
gruppo potrebbe sostituire l'albero fra creazione e scrittura.

## `known_hosts`

Costruito **esclusivamente dal pin già validato**. La chiave è
`validate_persisted_host_key(...).public_key`: la chiave stessa, mai la
fingerprint, mai riassemblata da pezzi.

Formato, rispecchia `golang.org/x/crypto/ssh/knownhosts.Normalize` (il motore
dialoga `net.JoinHostPort(ip, port)` e cerca il risultato in questo file; una
divergenza non fallisce, **manca la entry e ricade nel TOFU**):

| Coordinate | Riga |
|---|---|
| `host.example.com:22` | `host.example.com ssh-ed25519 AAAA…` |
| `host.example.com:2222` | `[host.example.com]:2222 ssh-ed25519 AAAA…` |
| `203.0.113.10:22` | `203.0.113.10 ssh-ed25519 AAAA…` |
| `2001:db8::1:22` | `2001:db8::1 ssh-ed25519 AAAA…` (senza parentesi) |
| `2001:db8::1:2222` | `[2001:db8::1]:2222 ssh-ed25519 AAAA…` |

Una riga per host configurato, newline finale, nessun commento, nessuna seconda
chiave, nessun hashing del nome host (il motore non lo richiede).

### L'host è validato, non creduto

**Un record `known_hosts` è delimitato da whitespace, e `knownhosts.parseLine`
tratta tutto ciò che segue il blob come un commento.** Un host contenente uno
spazio non corrompeva il file: vi **aggiungeva un secondo record ben formato**
con la chiave di un attaccante, degradando la chiave vera a commento di quel
record. Entrambi i record soddisfano poi il lookup — l'esatto contrario del
pinning. `_clean_host` dell'API toglie schema/userinfo/path/porta ma **non
vincola il charset**, e un host ostile sulla *destination* finisce nello stesso
file condiviso della *source*, che viene dialata per prima.

Il builder quindi **valida l'host**: hostname bare o literal IP, altrimenti
`WorkspaceSecurityError` e nessun workspace. Lo stesso valore validato finisce in
`host.yaml` (`ip:`), così i due file non possono divergere. La chiave pubblica non
ha bisogno di questo controllo: `parse_host_key` ha già provato che è una singola
riga a due token.

## `host.yaml`

Lo schema è quello che `internal/config` (unica autorità) consuma davvero —
`KnownFields(true)`: un campo in più è un **errore di parsing**, non un warning.
Il parser **non applica default**, quindi `port` e `timeout` vanno sempre emessi.

```yaml
src:
  ip: 203.0.113.10          # obbligatorio
  port: 22                  # obbligatorio, 1-65535, nessun default
  ssh_user: srcuser         # obbligatorio
  ssh_key_path: /…/source_key   # XOR con ssh_pass
  ssh_key_passphrase: …     # solo insieme a ssh_key_path
  timeout: 30s              # obbligatorio, > 0, duration string
dest: {…}                   # tutto-o-niente: completo, oppure blocco assente
```

`ssh_pass` **XOR** `ssh_key_path`: entrambi = errore, nessuno dei due = errore.
`dest` è tutto-o-niente perché `destIntended()` interpreta *qualsiasi* campo
popolato come intenzione e poi pretende il blocco intero; un `dest` parziale
fallisce rumorosamente. Un `dest` assente è una valida analisi source-only.

### Segreti nel file: il limite, dichiarato

Ordine di preferenza applicato:

1. **chiave privata → file privato nel workspace** (`ssh_key_path`). È ciò che
   facciamo: il materiale non entra mai in `host.yaml`.
2. variabili d'ambiente dedicate — **non supportate** dal parser;
3. secret file separati — **non supportati** dal parser;
4. **plaintext in `host.yaml` — inevitabile per `ssh_pass` e `ssh_key_passphrase`**:
   `internal/config` li dichiara `string` e non ha alcuna forma file/env/ref.

Quindi password e passphrase sono scritte **in chiaro** in `host.yaml`. Mitigazioni:
`0600`, esclusivamente dentro il workspace, mai loggato, mai in un'eccezione, mai
copiato in un artifact, cancellato dal cleanup. **Allargare quel transport
significa cambiare il parser Go: è un incremento separato e deliberato, non
qualcosa da introdurre qui.**

### Il contratto è provato, non assunto

Le fixture in `internal/config/testdata/generated_hostyaml/` sono l'output
**byte-per-byte** di `render_host_config`. Il lato Python
(`test_ssh_workspace_contract.py`) fallisce se il builder smette di produrle; il
lato Go (`internal/config/generated_hostyaml_test.go`) le passa a `config.Load`.
PyYAML che accetta il proprio output non prova niente sul parser Go.

Il test Go copre password, chiave, chiave+passphrase, porta non standard,
src+dest con metodi misti, e due proprietà su cui il builder fa affidamento:
ogni campo emesso è *load-bearing* (rimuoverne uno fa rifiutare `Load` → il parser
non ha default) e un campo in più è rifiutato (→ il builder non può inventarsi un
campo `known_hosts` che il motore non ha). `config.Load` non fa I/O oltre alla
lettura del file: non dialoga, non legge la chiave, non avvia migrazioni.

Gira sotto il check required **`Go race detector`** (`go test -race ./...`, tutto
il modulo); il job `platform-go-contract` è limitato a `internal/executioncontract`.

## Lifecycle e cleanup

```python
with build_ssh_workspace(source, destination, runtime_root=…) as ws:
    ...  # ws.host_config_path, ws.known_hosts_path, ws.source_key_path
# qui il workspace non esiste più
```

Context manager di proposito: il workspace contiene una chiave privata decifrata
e una password in chiaro, quindi «mi sono dimenticato il cleanup» non deve essere
raggiungibile dall'uso ordinario. Il `finally` copre **ogni** uscita: ritorno,
eccezione del chiamante, fallimento a metà build (che ha **già** scritto una
chiave). `cleanup()` è idempotente, sopravvive a un workspace già rimosso, e non
tocca il runtime root. `rmtree` non può uscire dal workspace: la root è una
directory reale creata da noi sotto un root verificato, e `rmtree` non segue una
root symlinkata.

## Limiti di zeroization

**Le stringhe Python non sono azzerabili in modo garantito.** Nessuna promessa
diversa qui. La mitigazione è limitarne durata, copie, `repr`, log e persistenza:
i segreti vivono in `SshCredentials` con `repr=False`, la classe non espone alcun
serializzatore (niente `to_dict`/`model_dump` che qualcuno possa passare a un
logger), gli errori nominano un *campo* mai un valore e tagliano la causa con
`from None`. **Il confine forte di questa PR è il filesystem: privato, effimero,
cancellato in modo deterministico.**

Questo conta anche per la CI: `platform-v2-gates.yml` carica `junit-*.xml`, e
pytest serializza il testo dei fallimenti nell'XML. Un segreto in un'eccezione è
un segreto in un artifact. I test asseriscono su presenza/permessi/assenza, mai
sui byte di un segreto, e diversi verificano `sentinel not in str(exc)` e
`exc.__cause__ is None`.

## Cosa questo incremento NON fa

Nessun subprocess. Nessuna rete. Nessuna connessione SSH. Nessun `ssh-keyscan`.
Nessun TOFU. Nessuna acquisizione automatica della host key. Nessun actor
Dramatiq. Nessun packaging o selezione del binario. Nessun compatibility
handshake. Nessuna ingestione eventi. Nessun aggiornamento di execution/attempt,
lease o heartbeat. Nessun apply. Nessuna UI. Nessuna migration Alembic (le
colonne `ssh_*` e la tabella del pin esistono già da `0009`/`0011`: era la
metadata del worker a esserne cieca).

**Lo snapshot non autorizza l'avvio.** Registra che il pin era coerente *quando è
stato letto*. L'executor che un giorno avvierà il subprocess dovrà, immediatamente
prima del lancio: rileggere endpoint e pin, confrontare le coordinate e gli anchor
(`host`, `port`, `fingerprint_sha256` — già nel DTO; non serve un timestamp),
rivalidare il pin con lo stesso `validate_persisted_host_key`, e **rifiutare uno
snapshot diventato stale**. Quella fase non è implementata qui.

## Prossimo passo

**Executor packaging + compatibility handshake** — binario Go identificato per
digest/versione, allowlist di contratto, avvio rifiutato prima del subprocess se
incompatibile.
