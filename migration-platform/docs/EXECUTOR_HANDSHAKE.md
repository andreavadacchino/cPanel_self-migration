# Executor compatibility handshake

Come la piattaforma decide che un binario Go **preciso** può essere lanciato —
prima di lanciarlo (ADR-001: «la compatibilità dell'executor è verificata prima
dell'avvio, non scoperta a metà run»; aggiornamento verificato 2026-07-16: le
capability SSH sono fatti distinti, mai un booleano generico).

**Questo incremento non lancia alcuna migrazione.** Nessun actor, nessun
dispatch, nessuna connessione SSH, nessuna migration Alembic. L'unico
subprocess che esiste è il handshake stesso: `<binario> capabilities`, che
stampa un documento e termina.

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
corpus (pattern `generated_hostyaml`) — il validatore Python prova di accettare
esattamente ciò che il produttore Go emette.

## Il subcomando: `cpanel-self-migration capabilities`

Stampa il documento su stdout ed esce. Nessun config letto, nessuna scrittura,
nessuna rete: la piattaforma lo esegue **prima** di aver deciso che il binario
è lanciabile, quindi il comando non deve avere alcun effetto. Argomenti extra
= exit 2 (un errore d'uso non deve mai sembrare un handshake riuscito).
Output deterministico byte-per-byte.

## La metà piattaforma: `adapters/executor_handshake.py`

Tre passi deliberatamente separati, ognuno fail-closed:

| Passo | Funzione | Rifiuta |
|---|---|---|
| **Identità** | `identify_executor_binary(path, expected_sha256=…)` | pin non esadecimale, file assente, **symlink**, non-regular, non eseguibile, digest diverso |
| **Handshake** | `run_capabilities_handshake(identity)` | timeout, exit ≠ 0, risposta > 64 KiB, documento invalido, exec fallita |
| **Decisione** | `ensure_compatible(caps, require_…)` | versione contratto assente da una lista, `strict_host_config`/`known_hosts_via_home` mancanti, capability SSH richiesta assente |

Il pin è **obbligatorio**: senza `expected_sha256` non esiste «binario
preciso», solo un lookup in PATH con più passaggi (ADR-001 D1). Un symlink è
rifiutato anche con digest corretto: il digest pinna il *contenuto*, ma è il
*path* a essere eseguito, e un path che qualcuno può ri-puntare non è un
binario pinnato.

Il subprocess del handshake è delimitato su ogni lato: argv puro (mai shell),
stdin chiuso, **ambiente spogliato** (`env={}` — l'ambiente del worker porta
legittimamente segreti `*_CPANEL_*` per i ref `env://`, e nulla di ciò deve
raggiungere il figlio), timeout, tetto sulla dimensione della risposta. Gli
errori nominano il path, un exit code o il campo del contratto violato — mai
stdout/stderr (testo influenzabile dal binario), mai un digest, mai un valore.

`strict_host_config` e `known_hosts_via_home` sono **sempre** richiesti: il
workspace scrive `host.yaml` contando sul fatto che un campo sconosciuto sia un
errore duro, e il trust pinnato esiste solo perché il motore legge
`HOME/.ssh/known_hosts`. Le tre capability di autenticazione sono richieste
**per run**: è il chiamante a dichiarare cosa servono le credenziali risolte.

## Cosa questo incremento NON fa

Niente lancio di migrazioni, niente actor Dramatiq, niente dispatch delle
execution `pending`, niente ingestione eventi, niente packaging pipeline
(GoReleaser/ldflags restano com'erano: un build locale riporta `0.0.0-dev`),
niente wiring di env/setting del worker (il path e il pin del binario entrano
nella configurazione del worker nell'incremento che lancia il subprocess),
niente rete, niente migration.

**Una verifica è una dichiarazione sul momento in cui è girata.** Il futuro
executor dovrà identificare il binario, completare il handshake, caricare
snapshot freschi e costruire un workspace nuovo **nella stessa sequenza**
immediatamente precedente al subprocess. Né l'identità del binario né il
workspace si «promuovono» da un run precedente.

## Prossimo passo

Dry-run actor + lifecycle del subprocess: dispatch di una execution `pending`,
sequenza identify → handshake → fresh snapshot → fresh workspace → `execute
--spec` → ingestione `events.jsonl`/`report.json` → terminalizzazione →
cleanup, con recovery provato.
