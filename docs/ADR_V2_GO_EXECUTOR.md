# ADR-001 — Migration Platform V2 è il control plane, il motore Go è l'executor

- **Stato:** Accettata
- **Data:** 2026-07-10
- **Contesto di codice:** `fork/main` = `5bd60c4`
- **Supersede:** nessuna. È il primo ADR del repository.
- **Vedi anche:** [`../migration-platform/docs/CURRENT_STATE.md`](../migration-platform/docs/CURRENT_STATE.md),
  [`JSON_EVENTS.md`](JSON_EVENTS.md)

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

### L'autenticazione SSH è solo a password, in due punti duplicati

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
`known_hosts` deterministico.

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

Non nella UI. In: contratto cross-language · supporto SSH key nel motore (due call site duplicati
più la validazione) · lifecycle dei subprocess · segreti temporanei · idempotenza · semantica di
retry · test su server reali · cleanup e rollback · compatibilità piattaforma ↔ binario.

**Il primo apply reale non parte finché questi elementi non sono coperti da test deterministici e
da uno smoke su account sacrificabile.**
