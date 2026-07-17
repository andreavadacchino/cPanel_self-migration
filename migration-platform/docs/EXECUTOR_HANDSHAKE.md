# Executor compatibility handshake

Come la piattaforma decide che un binario Go **preciso** può essere lanciato —
prima di lanciarlo (ADR-001: «la compatibilità dell'executor è verificata prima
dell'avvio, non scoperta a metà run»; aggiornamento verificato 2026-07-16: le
capability SSH sono fatti distinti, mai un booleano generico).

La catena di questo incremento, e dove si ferma:

```
source binary (non fidato)
→ copia privata verificata
→ artifact immutabile
→ bounded capabilities runner
→ compatibility decision
→ cleanup
→ STOP
```

**Nessun lancio di migrazioni.** Nessun actor, nessun dispatch, nessun comando
`execute`, nessuna execution/attempt, nessuna ingestione di eventi, nessuna
connessione SSH, nessuna migration Alembic. L'unico subprocess è l'handshake:
`<artifact> capabilities`, che stampa un documento e termina.

## Il documento: `executor-capabilities-v1`

Quarto documento del contratto cross-language (dopo spec/event/result), stesso
corpus condiviso (`testdata/execution-contract/manifest.json`), stessa coppia
di validatori: `internal/executioncontract/capabilities.go` e
`domain/execution_contract.py` (`parse_capabilities`). Schema descrittivo in
`schemas/executor-capabilities-v1.json`.

```json
{
  "format_version": 1,
  "executor_version": "…",            ← build version del binario, MAI la versione documento
  "contract": { "spec": [1], "event": [1], "result": [1] },
  "ssh": {
    "password": true,
    "private_key": true,
    "encrypted_private_key": true,
    "strict_host_config": true,
    "known_hosts_via_home": true
  }
}
```

A differenza di event/result, questo documento è **strict a ogni livello**: i
suoi nomi di campo contengono per natura sottostringhe sensibili (`password`,
`private_key`), quindi il redaction-walk degli output non può applicarsi — il
vocabolario chiuso e interamente tipizzato è ciò che garantisce che nessun
campo extra possa veicolare un segreto. Evolverlo significa un nuovo
`format_version`, mai una chiave aggiunta in silenzio.

Il produttore Go (`NewCapabilities`) dichiara **verità di codice, non
aspirazioni**: `password`/`private_key`/`encrypted_private_key` derivano dallo
XOR `ssh_pass`/`ssh_key_path` di `internal/config` e da `internal/sshx/auth.go`;
`strict_host_config` da `KnownFields(true)`; `known_hosts_via_home` da
`pool.go` che deriva il path da `os.UserHomeDir()`. `MarshalCapabilities`
ri-valida il proprio output: un documento invalido è inemettibile per
costruzione, e i byte emessi per una versione fissa sono la golden fixture del
corpus.

## Il subcomando: `cpanel-self-migration capabilities`

Stampa il documento su stdout ed esce. Nessun config letto, nessuna scrittura,
nessuna rete: la piattaforma lo esegue **prima** di aver deciso che il binario
è lanciabile. Argomenti extra = exit 2. Output deterministico byte-per-byte.

## Il deployment path è input, non identità

**Il difetto che questo design rimuove.** Hashare un file e poi passare il suo
*path* a `subprocess` fa **rivalutare il path al kernel**: fra il verdetto e
l'exec, la directory di deploy può restituire un file diverso. I byte
verificati e i byte eseguiti diventano due domande diverse. Rifiutare un
symlink al momento della verifica non tocca il problema: la sostituzione
avviene *dopo*. Provato per esecuzione, non per argomento — sul codice
precedente:

```
assert 'replaced-B' == 'trusted-A'   ← os.replace dopo l'identificazione
assert 'edited-B'  == 'trusted-A'    ← riscrittura in-place dopo l'identificazione
```

Quindi il source viene trattato come **input**: aperto una volta, copiato in una
directory privata mentre viene hashato, e solo quella copia viene mai eseguita.

| | |
|---|---|
| `ExecutorDeployment` | i tre input espliciti: `source_path`, `expected_sha256`, `runtime_root`. Dati puri: nessuna lettura di variabili d'ambiente qui |
| `VerifiedExecutorArtifact` | `root`, `executable_path`, `source_path` (solo audit), `sha256`. **Nulla esegue `source_path`** |
| `prepare_verified_executor(deployment)` | context manager: stage, yield, cleanup su ogni uscita |

### Perché una copia e non l'exec-by-descriptor

`fexecve` / `/proc/self/fd` è **solo Linux** (qui si sviluppa anche su macOS),
non funziona con lo shebang di uno script, e spingerebbe il lifetime del
descriptor dentro ogni chiamante futuro (`pass_fds` nell'actor). Una copia
privata è portabile, e l'artifact è un path reale che l'executor futuro può
ricevere come qualsiasi altro. Verificato: una copia privata `0500` del binario
reale **esegue**.

### Copia e hash in un solo passaggio

| Passo | Come |
|---|---|
| apertura source | `os.open(O_RDONLY \| O_NOFOLLOW)` — una sola volta, mai riaperto |
| controlli | `fstat` **sullo stesso descriptor** (regular file, bit eseguibili). Mai `os.access` sul path: ri-risolverebbe il path e descriverebbe un file che non stiamo copiando |
| root | `mkdtemp(prefix="executor-")` in un `runtime_root` verificato, `0700`. Nome dal sistema: non contiene digest, source, versione né altro |
| destination | `os.open(O_CREAT \| O_EXCL \| O_WRONLY \| O_NOFOLLOW, 0600)` |
| copia | a chunk, con `sha256.update(chunk)` **e** `write_all(chunk)` nello stesso passaggio |
| write parziali | `os.write` può scrivere meno di quanto chiesto: un write corto lascerebbe un artifact troncato sotto il digest del contenuto intero. Il loop scrive **ogni** byte |
| finale | `fsync` → confronto digest constant-time → `fchmod 0500` → chiusura |

Il source **può cambiare durante la copia**, e non è un problema: il digest
descrive esattamente i byte finiti nell'artifact, che sono esattamente i byte
che verranno eseguiti. Mismatch → artifact eliminato, runtime root vuoto.
Nessuna fiducia in `mtime`.

L'artifact finale è `0500` in una root `0700`: **un artifact scrivibile è un
artifact che può smettere di essere ciò che è stato verificato**.

### Cleanup

Semantica **replicata** da quella del workspace #118, non importata: la #118 non
è ancora canonica su `main`, e copiarne il codice specularmente sarebbe
speculativo.

| Stato | Esito |
|---|---|
| root già assente | successo idempotente |
| root rimossa | successo |
| root diventata symlink | `ExecutorArtifactCleanupError`, link **non seguito** |
| `PermissionError` / I/O / mount | `ExecutorArtifactCleanupError` |
| `rmtree` termina ma la root esiste | `ExecutorArtifactCleanupError` |
| errore nel body + cleanup fallito | **entrambi** in un `ExceptionGroup` |

Mai `ignore_errors=True`. Gli errori nominano il path amministrativo, mai un
digest, mai il contenuto.

## Il bounded runner

`subprocess.run(capture_output=True)` legge fino a EOF e *poi* si può giudicare
la dimensione: un verdetto su un buffer che esiste già, non un limite di
memoria. `adapters/bounded_process.py` legge invece **incrementalmente** e
ferma il figlio nel momento in cui supera il limite.

| Requisito | Come |
|---|---|
| lettura incrementale | `Popen` + `selectors` + `os.read`, deadline monotona |
| buffer massimo | `max_stdout_bytes + 1`: ogni read chiede esattamente quanto è ancora consentito **più un byte**, quello che prova l'overflow (e non viene mai restituito) |
| overflow / timeout | il processo viene **fermato**, non solo giudicato |
| process group | `start_new_session=True`, poi SIGTERM → grace → SIGKILL sul **gruppo**: un helper generato dal figlio non può sopravvivere tenendo la pipe |
| reaping | il figlio è sempre `wait`-ato **prima** che un errore lasci il modulo: un'eccezione con uno zombie dietro è un leak, non un fallimento |
| stderr | `DEVNULL`. Una pipe non letta bloccherebbe il figlio una volta pieno il buffer del kernel, rendendo il timeout l'unica uscita |
| env | default vuoto: chi vuole l'ambiente del parent deve dirlo — il verso giusto per un worker che porta segreti |
| shell / PATH | `shell=False`, e `argv[0]` **deve essere assoluto**: nessun lookup |

Errori tipizzati (`ProcessStartError`, `ProcessTimeoutError`,
`ProcessOutputLimitError`, `ProcessTerminationError`), programmabili per tipo,
mai per parsing di stringhe, e **senza output del figlio**.

Provato per mutazione: togliendo il clamp, il flood infinito (`yes`) diventa
`ProcessTimeoutError` invece di `ProcessOutputLimitError` — il test non è
basato sul tempo, è basato sul fatto che `yes` non finisce mai.

## L'invariante che l'executor futuro eredita

**L'artifact che supera l'handshake è l'artifact che `execute` deve eseguire.**

```python
with prepare_verified_executor(deployment) as artifact:
    capabilities = run_capabilities_handshake(artifact)
    ensure_compatible(capabilities, ...)
    # fresh snapshot
    # fresh SSH workspace
    # futuro:
    # run_execute(artifact, ...)   ← lo STESSO artifact, mai il source
```

Sono **vietati** entrambi questi schemi, e il type boundary è ciò che li rende
difficili: `run_capabilities_handshake` accetta un `VerifiedExecutorArtifact` e
nient'altro — niente `Path | str`.

```
hash del source → handshake sull'artifact → execute del SOURCE PATH   ✗
handshake sull'artifact A → ricopia → execute dell'artifact B         ✗
```

Questa PR fornisce l'API e **si ferma prima** di `run_execute`.

## Stato onesto del packaging

| Implementato | Ancora mancante |
|---|---|
| capabilities contract (4° documento, corpus 70/70) | build/release pipeline (GoReleaser) |
| emitter Go + parser Python | versione di release affidabile |
| immutable verified artifact | digest pubblicato |
| bounded handshake runner | configurazione worker (path + pin) |
| compatibility decision | distribuzione nel container |
| | dry-run dispatch / actor |

**Il packaging operativo NON è completo.** Nessun ldflag nuovo, nessuna
release, nessun download automatico. `ExecutorDeployment` rende espliciti i tre
input, ma **non** legge variabili d'ambiente: i setting del worker arriveranno
con l'incremento che ha un consumatore reale, cioè quello che lancia il
subprocess.

### `executor_version` non è ancora affidabile — misurato, non supposto

Senza ldflag, `version.String()` (`internal/version/version.go`) ricade su
`debug.ReadBuildInfo()`, e il risultato **dipende dall'ambiente di build**:

| Ambiente | `executor_version` |
|---|---|
| build locale (`Main.Version` = `(devel)`) | `0.0.0-dev` |
| CI (Go risolve una pseudo-version di modulo) | `0.0.0-20260717110738-21779e0d3d27` |

Osservato davvero: un test che confrontava l'output del binario byte-per-byte
con la golden passava in locale e **falliva in CI** proprio su questo campo.
Non è rumore da aggirare: è la dimostrazione che **manca una versione di
release affidabile**, ed è il primo motivo per cui il packaging resta lavoro
residuo. Finché non c'è una pipeline che stampiglia la versione,
`executor_version` è un fatto di build, non un'identità.

Conseguenza per i test: il confronto **byte-per-byte** con la golden vive dove
la versione è un input controllato — `TestMarshalCapabilitiesMatchesTheSharedGolden`
(Go) la fissa a `0.0.0-dev`. Il test sul binario reale confronta il documento
**a meno di `executor_version`**, che è l'unico campo che oggi non può essere
pinnato.

## Prossimo passo

Dry-run actor + lifecycle del subprocess: dispatch di una execution `pending`,
sequenza `prepare_verified_executor` → handshake → fresh snapshot → fresh
workspace → `execute --spec` sullo **stesso artifact** → ingestione
`events.jsonl`/`report.json` → terminalizzazione → cleanup, con recovery
provato.
