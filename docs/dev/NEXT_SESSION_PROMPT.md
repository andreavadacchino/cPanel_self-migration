# Prompt per la prossima sessione di sviluppo

Copia il blocco qui sotto come primo messaggio della nuova sessione.

---

Stai lavorando sul tool Go **cpanel-self-migration** (migrazione read-only-source
tra due account cPanel via SSH user-level), directory
`/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration`.

## Leggi PRIMA di toccare codice

1. `docs/dev/DEVELOPMENT_STATE.md` — roadmap, mappa architettura, i fatti reali
   del server (formati che rompono le fixture sintetiche: ogni campo numerico
   cPanel può arrivare come stringa quotata o float → `flexInt64`; ogni "stringa"
   può essere un array → `flexStringList`), convenzioni test, metodo smoke via
   Orbit (capture SEMPRE in base64: il gateway maschera e corrompe il JSON).
2. Per la linea DNS: `docs/dev/PR6A_DNS_IMPORT_DESIGN.md` (contratto 6D),
   `docs/dev/PR6B_PRE_CAPTURES.md` (fatti write-API reali),
   `docs/dev/PR6C_DNS_VERIFY_DESIGN.md` (verify + safety test).
3. Per la linea UI: `docs/dev/UI2A_CONNECTIONS_RUN_DESIGN.md`,
   `docs/dev/UI2B_ACCEPT_DESIGN.md`, `docs/dev/UI3_APPLY_MONITOR_DESIGN.md`.
4. Per checklist/accettazioni: `docs/dev/PR7A_REAL_SMOKE.md`,
   `docs/dev/PR7C_APPLY_EVIDENCE_DESIGN.md`, `docs/dev/PR7D_ACCEPTANCES_DESIGN.md`.

## Contesto in una riga

Pipeline read-only completa (inventario SSH → diff → policy → dns-plan →
checklist) con catena di provenienza sha256, evidenza `per_item` da report apply
(7C), accettazioni operatore (7D), **`dns verify`** (6C: certificazione read-only
delle zone destination contro il piano), e una UI web locale zero-JS che copre
l'intero flusso read-only dal browser **più il monitor live di un apply da
terminale** (UI-3). La migrazione vera (`--apply`) e la futura scrittura DNS
restano SOLO da terminale.

## Stato del fork (ultimo merge: PR #30, main `6a9dfed`)

| PR | Contenuto |
|----|-----------|
| #29 | **PR 6C `dns verify`**: namespace `dns` nel dispatch (subcommand ignoti → exit 2, prima cadevano silenziosamente in un dry-run di migrazione), engine puro `VerifyDNSPlan` (stati applied/unchanged/pending/drift/manual_review/not_checked, fail-safe su piani malformati), gate stale-plan sha256 (exit 3 prima di ogni SSH), `--fail-on-drift` (exit 3 se non clean), `sshx.DialDest` (solo destination — il source può essere già dismesso), `FetchDNSZone` estratto behavior-preserving dal collector, report json+md con golden. Semantica gate: manual OPS non gatano mai (NS differisce in ogni migrazione reale — deadlock 6A-v1), manual ZONES gatano sempre (piano stantio ≠ falso verde). Safety: scan module-wide token-based dei verbi di scrittura DNS + **test strutturale AST** (ogni `RunUAPI`/`RunAPI2` deve passare modulo/funzione come string literal — la concatenazione `"mass_"+"edit_zone"` fa fallire la suite). |
| #30 | **UI-3 apply/run monitor**: la dashboard legge `events.jsonl` (scritto da `--apply --json-events` da terminale, O_APPEND → segmentazione sull'ultimo `run_started`) e mostra l'ultimo run fase-per-fase con l'evidenza per-item 7C. Monitor-only (la UI non lancia mai apply), zero-JS (meta-refresh esteso, NIENTE SSE: richiederebbe JS), parser fail-safe (tail 2 MiB con io.LimitReader, riga parziale=write in-flight, garbage contato, 50 fasi max, 8 errori, 10 item names), stall detection (>10 min senza eventi o ts futuro oltre 30s di skew → pannello ambra + stop refresh). |

Lezione di sessione (paga da 4 PR di fila): il go-reviewer al **primo giro** su #30
ha trovato 2 HIGH veri (stalled renderizzato verde; ts futuro → refresh infinito);
il **secondo giro sui fix** ha trovato 2 MEDIUM introdotti DAI fix (conteggio
overflow per eventi anziché fasi; zero tolleranza skew NTP → falso stall su run
sani); terzo giro APPROVE. Su superfici critiche i giri di review sono iterativi
finché non escono puliti — mai fermarsi al primo APPROVE mancato.

## Workflow (OBBLIGATORIO — non negoziabile)

- Lavora SOLO sul fork `andreavadacchino/cPanel_self-migration`. Push su remote
  `fork`, MAI su `origin` (tis24dev, read-only). PR verso il main del fork, merge
  con `gh pr merge N --merge`.
- Branch nuovo per ogni PR: `git checkout main && git pull fork main && git checkout -b <branch>`.
- **TDD rigoroso**: fixture/scenario reale → test RED → fix minimo → GREEN → refactor.
  I safety test vanno validati col metodo test-del-test (canary iniettato → fail → rimosso).
- Per OGNI PR, prima del push lancia il Go reviewer (agent
  `everything-claude-code:go-reviewer`) con un prompt che gli fa ATTACCARE le
  proprietà critiche con controesempi concreti; correggi i finding reali e
  RIMANDA i fix allo stesso reviewer (SendMessage) finché non dà APPROVE.
- **Sourcery è rate-limited a livello account** fino a ~09/07/2026: il check
  "pass" è solo formale. Gate sostitutivo usato per #22–#30: go-reviewer
  multi-giro + suite completa in Docker. Dichiaralo nel commento di merge.

## Verifiche finali di ogni PR

```
go test ./internal/webui/ ./internal/accountinventory/ ./cmd/... ./internal/sshx/ ./internal/cpanel/ -race
go test ./...   &&   go vet ./...   &&   go build ./cmd/cpanel-self-migration
gofmt -l <file toccati>   # ATTENZIONE: main ha violazioni gofmt PREESISTENTI in 7 file — formatta solo i TUOI file
```

I 4 package macOS noti (`dbmig`, `maildir`, `migrate`, `webfiles`) falliscono su
macOS SOLO per bash/sed GNU-only — verifica `git diff main -- <pkg>` vuoto. La CI
del fork non gira: **replica la suite in Docker prima di ogni merge**:

```
docker run --rm -v "$PWD":/src -w /src -e GOFLAGS=-buildvcs=false -e CGO_ENABLED=1 \
  golang:1.25 bash -c "go test ./... && go vet ./... && go test -race ./internal/webui/ ./internal/accountinventory/ ./cmd/... && echo LINUX_ALL_GREEN"
```

Golden Markdown: refresh con `UPDATE_GOLDEN=1`.

## Perimetro protetto

- `internal/migrate/runner.go` off-limits (unica eccezione già spesa: la riga
  `opts.RunID = runID` di PR 7C).
- Scritture sui server VIETATE fino a PR 6D. La UI non apre SSH e non lancia
  `--apply`; `dns verify` apre SSH READ-ONLY verso il solo destination.
- I safety test DNS (`internal/cpanel/dns_safety_test.go`: lexical module-wide +
  strutturale literal-names) sono il lucchetto di 6D: 6D dovrà emendarli
  CONSAPEVOLMENTE con una allowlist per i propri file.

## Mappa rapida del codice nuovo

- `internal/accountinventory/dnsverify.go` — `VerifyDNSPlan` (puro; riusa
  `planValue`/`groupRRSets`/`canonDNSName` del piano: verify non può divergere
  dal piano su cosa significhi "uguale") + `dnsverify_write.go` (json+md golden).
- `internal/accountinventory/collector.go` — `FetchDNSZone` esportata (UAPI→API2
  →unavailable, mai fatale), usata da collector e da `dns verify`.
- `internal/sshx/pool.go` — `DialDest` + `hostKeyCallback` condiviso con `DialBoth`.
- `cmd/cpanel-self-migration/dns_verify_cmd.go` — comando; dispatch namespace
  `dns` in `main.go`. Exit: 0 ok, 1 input/SSH, 2 flag, 3 gated (stale-plan o
  `--fail-on-drift` non clean). E2E con `sshtest` + stub `uapi` in PATH.
- `internal/webui/monitor.go` — `loadRunMonitor` (tail-bounded) +
  `parseRunMonitor` (puro, `now` iniettato) + pannello in `templates/index.html`
  (classe `.status.stalled` ambra); `page.Monitor`/`MonitorLive` in `webui.go`.

## Prossimi obiettivi (proponi tu quale, motiva, aspetta conferma)

1. **PR 7E — inventario esteso** (capture-first come 6B-pre): email routing,
   default address, filtri email, redirect + finding 3 dello smoke 7A (DKIM
   rigenerati silenziosi → azione operatore `CONFIRM_DNS_RECORD`). Riduce le
   azioni manuali cieche della checklist. **RICHIEDE sessione Orbit con TOTP**
   per le capture reali (account registrati: doctorbike.it, italplant.com).
2. **PR 6D — `dns apply`**: il PRIMO comando che scrive. Alto rischio — sessione
   dedicata, protocollo completo del CLAUDE.md (backup, rollback <60s, zona
   sacrificale su principiadv.online, approvazioni Orbit live). Contratto:
   mass_edit_zone atomica serial-guarded, mai delete di record destination, mai
   NS/SOA, backup-file-o-niente-write. 6C fornisce già la certificazione
   post-apply (`dns verify --fail-on-drift`) e i safety test da emendare.
3. **Quick-fix dispatch** (30 min): `inventory <subcommand ignoto>` cade ancora
   silenziosamente nel flusso migrazione (stessa classe di footgun che il
   namespace `dns` ora rifiuta con exit 2) — finding out-of-scope del reviewer
   su #29, annotato in DEVELOPMENT_STATE.md.
4. **UI rifiniture** (basso valore, zero blocchi): revoca accettazione dal
   browser, persistenza nome operatore, download artefatti.

Consiglio: 1 e 2 sono i due obiettivi sostanziali rimasti e richiedono ENTRAMBI
l'utente in sessione (TOTP / approvazioni): pianificali come sessioni dedicate.
La 3 è il riempitivo perfetto se c'è tempo residuo. Follow-up minori annotati in
DEVELOPMENT_STATE.md (§ "Follow-ups from the 6C go-review").

## Come provare la UI adesso

```
go build ./cmd/cpanel-self-migration
./cpanel-self-migration ui --dir <dir-artefatti>   # http://127.0.0.1:8422/
```

Per vedere il monitor: nella dir un `events.jsonl` (anche sintetico: righe JSONL
di `events.Event`; vedi `internal/webui/monitor_test.go` per lo shape, o lancia
un dry-run con `--json-events`). Senza file la dashboard mostra l'hint.

## Regole di lavoro

Analizza in modo investigativo; quando trovi una soluzione rimettila in esame per
assicurarti al 100% che sia corretta. NON supporre, NON inventare, NON prendere
scorciatoie, NON fare regressioni. Testa prima, durante e dopo. Riusa
l'implementazione esistente il più possibile. Usa agenti specializzati in
parallelo e tutti gli strumenti disponibili. Sii brutalmente onesto, critico e
scettico. Feedback non verboso.
