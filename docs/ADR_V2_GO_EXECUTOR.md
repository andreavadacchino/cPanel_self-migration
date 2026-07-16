# ADR-001 — Migration Platform V2 è il control plane, il motore Go è l'executor

- **Stato:** Accettata, con aggiornamento verificato (2026-07-16)
- **Data:** 2026-07-10
- **Contesto originario:** `fork/main` = `5bd60c4`
- **Stato verificato successivo:** `fork/main` = `dd7bb1d` + PR #108 (SSH key auth, mergiata
  `8e778b6`), #117 (host identity persistence), #118 (SSH runtime workspace, draft)
- **Supersede:** nessuna. È il primo ADR del repository.
- **Vedi anche:** [`../migration-platform/docs/CURRENT_STATE.md`](../migration-platform/docs/CURRENT_STATE.md),
  [`JSON_EVENTS.md`](JSON_EVENTS.md),
  [`../migration-platform/docs/SSH_RUNTIME_WORKSPACE.md`](../migration-platform/docs/SSH_RUNTIME_WORKSPACE.md)

---

## Aggiornamento verificato — 2026-07-16

Questo ADR è stato scritto contro `fork/main = 5bd60c4`. Incrementi successivi hanno reso **stale
la descrizione tecnica dell'autenticazione SSH** qui sotto, verificato leggendo il codice corrente
(non questo documento, non la memoria):

- **le decisioni architetturali (D1–D5) restano valide** — nulla in questo aggiornamento le tocca;
- la sezione «L'autenticazione SSH è solo a password» è **storica, superseded**: la PR #108
  (mergiata, `8e778b6`) ha implementato l'autenticazione a chiave privata esattamente nel modo che
  la sezione prescriveva. Oggi `internal/config` impone **`ssh_pass` XOR `ssh_key_path`**
  (`config.go`, `HostConfig.validate`: entrambi presenti = errore, entrambi assenti = errore) con
  `ssh_key_passphrase` opzionale e valido solo insieme a `ssh_key_path`. `internal/sshx/auth.go`
  costruisce l'`Authentication` una volta sola (`AuthFromHost` → `PrivateKeyAuth`/`PasswordAuth`,
  chiave letta e parsata una volta, `ssh.PublicKeys(signer)`), e **un unico builder**
  (`newClientConfig`) serve sia il dial iniziale (`retry.go`, `dialWithRetry`) sia il
  redial/self-heal (`client.go`, che riusa la stessa `Authentication` verbatim): il degrado
  silenzioso a password su reconnect, paventato sotto, è strutturalmente impossibile e coperto dai
  test di `internal/sshx` (dial, redial, chiave cifrata, passphrase errata);
- il timore «il worker deve fornire un `known_hosts` deterministico» è stato **risolto** dalle PR
  #117 + #118: il pin è persistito e validato crittograficamente, e il workspace effimero
  materializza `<root>/.ssh/known_hosts` dal pin (il motore lo deriva da `os.UserHomeDir()`,
  `internal/sshx/pool.go` — `host.yaml` non ha e non può avere un campo `known_hosts`: il parser è
  strict, `KnownFields(true)`). Nessun `ssh-keyscan`, nessun TOFU per acquisire il pin: resta il
  fallback del motore, non il percorso della piattaforma. **Distinzione dovuta**: la #118 risolve la
  *materializzazione* del pin, non la *provenienza affidabile* della host key — la procedura
  out-of-band con cui l'operatore acquisisce il pin resta responsabilità operativa;
- la sezione «Dove sta il lavoro vero» è aggiornata in coda: il supporto SSH key **non è più lavoro
  mancante**;
- **requisito per il prossimo incremento** (Executor packaging + compatibility handshake): il
  handshake non deve trattare «SSH» come una capability binaria generica. Deve poter distinguere
  almeno: password authentication, private-key authentication, supporto a chiave
  cifrata/passphrase, schema strict di `host.yaml`, `known_hosts` via `HOME`. Il formato definitivo
  delle capability si definisce lì, non qui.

Il testo originale sotto è preservato come contesto storico e non è stato riscritto.

---

## Contesto

Il repository contiene due architetture parallele che non condividono un solo file:

- il **motore Go** (`cmd/`, `internal/`), maturo, che esegue write reali su cPanel;
- la **Migration Platform V2** (`migration-platform/`), che oggi legge, confronta e pianifica,
  ma non esegue nulla.

La piattaforma deve diventare capace di **governare** migrazioni reali. La domanda è chi le esegue.

Un'analisi avversariale ha identificato lo scenario di fallimento concreto: si aggiunge un bottone
"Avvia migrazione"; l'API prende l'ultimo piano senza verificare che sia ancora coerente con gli
snapshot; il worker genera un `host.yaml` temporaneo ma il motore non supporta la chiave SSH usata
dall'infrastruttura reale; per aggirare il blocco si introduce una password temporanea; il
subprocess parte senza uno spec versionato; alcune fasi riescono e una fallisce; il modello dati
sa dire solo `succeeded` o `failed`, mai "metà destinazione è scritta"; l'operatore riesegue;
la destinazione diverge.

Ogni anello di questa catena è una decisione mancante. Questo ADR le prende.

## Decisione

```
Migration Platform V2  =  control plane          (stato, gate, audit, UI)
Dramatiq worker        =  lifecycle manager      (subprocess, credenziali, workspace)
Binario Go             =  execution engine       (unico che tocca i server)
PostgreSQL             =  durable state + audit  (fonte di verità)
Redis                  =  transport only         (nessuno stato)
```

### D1 — Il motore Go è l'unico executor. Nessun porting dei writer in Python.

I pacchetti che eseguono write reali (`internal/migrate`, `internal/dbmig`, `internal/maildir`,
`internal/webfiles`, i writer di `internal/cpanel` e `internal/accountinventory`) incorporano
correzioni maturate su cPanel reali. Riscriverli in Python significherebbe riscoprire quegli stessi
bug in produzione, su dati di clienti.

Conseguenze accettate, non implicite:

- il binario diventa una **dipendenza di runtime** della piattaforma;
- serve una **politica di compatibilità** fra versione della piattaforma e versione del binario;
- la compatibilità dell'executor è **verificata prima dell'avvio**, non scoperta a metà run;
- il worker invoca una **versione precisa** del binario, non "quello che c'è nel PATH".

### D2 — Il primo apply reale è bloccato finché non esiste un account sacrificabile.

Serve: un account dedicato con accesso SSH su **entrambi** i lati, una mailbox dedicata, una
password storica nota all'operatore, una destinazione inizialmente vuota, una procedura di cleanup
verificabile.

Finché non esiste, **la capability di apply non compare nella UI**. L'orizzonte eseguibile si ferma
al **dry-run governato**. Questo non blocca il lavoro su SSH-key e sul bridge: servono già al dry-run.

Nota operativa: `demobox@giorginisposi.it` **non** è un candidato — è una casella POP reale.

### D3 — La piattaforma resta localhost e mono-operatore fino all'hardening.

Non esiste autenticazione API. Finché non esistono auth, autorizzazione, protezione SSRF con host
allowlist e rate limiting, **la piattaforma non viene esposta fuori da localhost**.

Questo è ciò che rende accettabile posticipare l'hardening, non un'omissione.

### D4 — La WebUI Go (#84, #85) è congelata e fuori dalla roadmap attiva.

Sono evoluzioni di `internal/webui/`, non della piattaforma. Non toccano `migration-platform/`
(zero conflitti cross-track). Restano nel repository come lavoro recuperabile, ma **non devono
competere per diventare il control plane**: due control plane significano due verità sullo stato
di una migrazione.

### D5 — Il contratto Platform↔Executor è versionato ed esplicito.

Il worker non "chiama un comando". Scambia artefatti versionati.

- **Input** (platform → executor): spec JSON versionato.
- **Output** (executor → platform): eventi JSONL + report finale, versionati.
- I **segreti sono esclusi** dallo spec persistito, dagli eventi e dal report.
- Le credenziali sono risolte dal worker **a runtime**, mai persistite nello spec.
- **DNS è escluso dall'auto-run.** Resta inventariato, confrontato, mostrato come task manuale.
- **Una sola esecuzione mutante per migrazione** alla volta.

## Stato del contratto — cosa esiste già, cosa manca

Questo è il punto in cui l'analisi iniziale sovrastimava il lavoro. Verificato sul codice.

### Direzione executor → platform: **esiste già, va versionata**

| Artefatto | Dove | Nota |
|---|---|---|
| Evento JSONL | `internal/events/event.go:57-67` | `run_id`, `ts`, `level`, `phase`, `event`, `message`, `source`, `destination`, `data` |
| Writer append-only | `internal/events/writer.go` | **redige i segreti** (`redactData`) |
| Report finale | `internal/events/report.go:27-41` | `exit_status`, `phases_completed`, `warnings`, `errors`, `artifacts` |
| Flag CLI | `--json-events`, `--report-json`, `--run-id`, `--output-dir` | già pensati per un orchestratore esterno |
| Documentazione | [`JSON_EVENTS.md`](JSON_EVENTS.md) | il formato è già descritto |

Il commento di package di `internal/events` dichiara esplicitamente che il JSONL serve a un
consumatore esterno senza sostituire l'output human-readable.

**Il gap reale è ristretto:** né `events.jsonl` né `report.json` portano un campo di versione dello
schema. `RunReport.Version` è la **build version del binario** (da `internal/version`, via ldflags),
non la versione del formato.

Il repository ha già una convenzione per questo: `format_version` (intero), dichiarato dai report di
`internal/accountinventory` — `cronapply.go:35`, `emailapply.go:373`, `dnsverify.go:68`,
`emailverify.go:55` — e valorizzato dai loro produttori (es. `checklist.go:117`).
**Si adotta quella**, non se ne inventa una nuova.

→ `execution-event-v1.json` e `execution-result-v1.json` sono **derivati** dalle struct esistenti,
non progettati da zero.

### Direzione platform → executor: **da definire**

Oggi il contratto de-facto è `host.yaml` (`internal/config/config.go:44-50`): chiavi `src`, `dest`,
`databases`. È già parsato in modo **strict** — `dec.KnownFields(true)` fa fallire una chiave
sconosciuta invece di ignorarla, il che è la proprietà giusta per un contratto.

→ `execution-spec-v1.json` è **nuovo**. Deve contenere solo riferimenti e dati non segreti:
`plan_id`, `source_snapshot_id`, `destination_snapshot_id`, `comparison_report_id`, `mode`, `scope`.

## Vincoli sul motore Go

### L'autenticazione SSH è solo a password, in due punti duplicati — **[storico: superseded, vedi «Aggiornamento verificato — 2026-07-16»]**

> Vero su `5bd60c4`, falso oggi: la PR #108 ha implementato la chiave privata nel modo qui
> prescritto (entrambi i call site, un solo builder, test sul redial). Il testo resta come contesto.

`ssh.Password` è l'unico `ssh.AuthMethod` costruito, e lo è **due volte, verbatim**:

- `internal/sshx/retry.go:27-33` — dial iniziale
- `internal/sshx/client.go:123-131` — **redial / self-heal**

`ssh_pass` è obbligatorio in `internal/config/config.go:171-188`.

> **Aggiornare solo il dial produce un motore che sembra supportare le chiavi finché la connessione
> non cade, e poi degrada silenziosamente a password.** Entrambi i call site vanno modificati, e il
> test deve coprire il redial.

L'estensione (`SSHKeyPath`, `SSHKeyPass`, `HostKeySHA256`) mantiene il supporto password senza
regressioni; `validate()` passa da "password obbligatoria" a "password **oppure** chiave".

### L'host key è già verificata

`internal/sshx/hostkey.go:31-86` implementa **TOFU accept-new** su `~/.ssh/known_hosts`, e rifiuta
con `ErrHostKeyChanged` se la chiave cambia. `InsecureIgnoreHostKey` compare solo nei `_test.go`.

Il pinning esplicito resta desiderabile perché **in un worker containerizzato `~/.ssh/known_hosts`
è effimero**, e il TOFU degrada a "accetta qualunque chiave al primo run". Il worker deve fornire un
`known_hosts` deterministico. *(Aggiornamento 2026-07-16: fatto — pin persistito da #117, workspace
effimero che lo materializza da #118; vedi la sezione in testa.)*

Side-effect da conoscere: al primo contatto il motore **crea** `~/.ssh/known_hosts` sull'host dove gira.

### Il dry-run è già il default

Il flusso principale è dry-run salvo `--apply` / `--apply-mirror`. I writer (`dns|cron|email apply`)
richiedono `--yes-apply-writes` e di default fanno preview offline a **zero connessioni**.
Exit code: `0` ok, `1` errore, `2` uso, `130` interrotto.

Invariante del motore, da preservare: **il SOURCE è sempre read-only; tutte le write vanno sul
DESTINATION.** L'utente SSH è l'utente-account cPanel, non root.

## Modello dati dell'esecuzione

`jobs` non va sovraccaricata. `JobStatus` oggi è `pending queued running succeeded failed`: non sa
esprimere `partial`, ed è esattamente l'informazione che serve dopo un fallimento a metà.

Tabella dedicata `migration_executions`, ancorata **immutabilmente** al piano e agli snapshot che
l'operatore ha visto:

```
id · migration_id · job_id · plan_id
source_snapshot_id · destination_snapshot_id · comparison_report_id
mode (dry_run|apply|verify|rollback) · scope JSON · status
executor_version · spec_version · spec_sha256
artifact_manifest JSON · result_summary JSON
error_code · error_summary · created_at · started_at · finished_at
```

Stati: `pending queued running succeeded failed partial cancel_requested cancelled interrupted`.

## Freshness: un piano non è eseguibile per sempre

- Un nuovo snapshot o una nuova comparison rendono il piano precedente **stale**.
- Un piano `blocked` non genera un'esecuzione `apply`.
- Un piano con unknown critici richiede una decisione esplicita.
- Lo scope è **congelato** quando parte la prima write.
- **L'API ricalcola tutti i gate lato server. La UI non è una barriera di sicurezza.**

## Alternative scartate

**Riscrivere i writer in Python.** Perde anni di correzioni verificate su server reali. Nessuna
delle write (Maildir, dump/import MySQL, riscrittura config CMS, mirror docroot) è banale.

**Trattare "job asincrono" come "esecuzione governata".** Oggi mancano cancellazione, interruzione,
esito parziale, manifest degli artifact e identità della versione dell'executor. Un actor Dramatiq
che lancia un subprocess non è un contratto esecutivo.

**Modellare token cPanel e accesso SSH come una sola credenziale.** Sono capability distinte e
possono essere disponibili indipendentemente: `cpanel_api_access`, `ssh_account_access`,
`imap_verification_access`.

**Iniziare l'apply dall'intero account.** Il raggio d'errore è troppo ampio per validare
un'integrazione appena nata. Si parte da una singola mailbox sacrificabile.

## Conseguenze

- Gli artifact di esecuzione cresceranno rapidamente su disco: serve una retention policy.
- Retry e resume diventano **contratto pubblico**: cambiarne la semantica dopo costa caro.
- Più credenziali supportate = più responsabilità di rotazione, redazione, cleanup.
- Dopo la prima write reale, ogni stato verde della UI ha conseguenze operative: i fallback
  ottimistici non sono più accettabili.
- Il **Campaign Mode** resta bloccato finché una singola migrazione non è recuperabile dopo crash,
  refresh e riavvio del worker.

## Dove sta il lavoro vero

Non nella UI. Elenco originale: contratto cross-language · supporto SSH key nel motore · lifecycle
dei subprocess · segreti temporanei · idempotenza · semantica di retry · test su server reali ·
cleanup e rollback · compatibilità piattaforma ↔ binario.

**Aggiornamento verificato 2026-07-16** — fatti da allora: supporto SSH key nel motore (#108),
contratto `host.yaml` provato cross-language e host identity persistita (#117), resolver dei
segreti + workspace effimero con `known_hosts` dal pin (#118). Il lavoro residuo reale è:

- packaging deterministico del binario e identificazione versione/digest;
- compatibility handshake (con le capability distinte richieste in testa) e avvio fail-closed;
- fresh snapshot + fresh secret resolution + **fresh workspace** immediatamente prima del
  subprocess (mai riuso di un workspace precedente);
- lifecycle del subprocess, ingestione eventi e report, cleanup, recovery;
- smoke reale su account sacrificabile (D2 resta bloccante).

Nulla di questo è implementato oggi: nessun subprocess, nessun actor dry-run, nessun handshake,
nessuna ingestione eventi, nessuna terminalizzazione delle execution, nessun apply, nessuno smoke.
Il prossimo incremento resta **Executor packaging + compatibility handshake**.

**Il primo apply reale non parte finché questi elementi non sono coperti da test deterministici e
da uno smoke su account sacrificabile.**
