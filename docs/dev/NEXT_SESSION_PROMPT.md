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
2. Per la linea UI: `docs/dev/UI2A_CONNECTIONS_RUN_DESIGN.md` e
   `docs/dev/UI2B_ACCEPT_DESIGN.md`.
3. Per la checklist/accettazioni: `docs/dev/PR7A_REAL_SMOKE.md`,
   `docs/dev/PR7C_APPLY_EVIDENCE_DESIGN.md`, `docs/dev/PR7D_ACCEPTANCES_DESIGN.md`.

## Contesto in una riga

Pipeline read-only completa (inventario SSH → diff → policy → dns-plan →
checklist) con catena di provenienza sha256, evidenza `per_item` da report apply
(PR 7C), accettazioni operatore con chiavi stabili (PR 7D), **e una UI web locale
interattiva** (`cpanel-self-migration ui`) che copre l'intero flusso read-only dal
browser: configurare i server, lanciare l'analisi, accettare le azioni. La
migrazione vera (`--apply`) resta SOLO da terminale.

## Stato del fork (ultimo merge: PR #27, HEAD `69da8d4`)

Ultime 7 PR di questa sessione, tutte mergiate sul main del fork:

| PR | Contenuto |
|----|-----------|
| #21 | checklist SSL: gruppi cert scaduti sul source → expected, copertura wildcard RFC 6125 |
| #22 | PR 7C apply evidence: eventi di fase apply, `phases_completed`/`artifacts` in report.json, checklist `per_item` |
| #23 | PR 7D acceptances: chiavi azione stabili (`AK-<12hex>`), acceptances.json, `--acceptances` (gate clearing fail-safe) |
| #24 | UI fase 1: dashboard read-only (checklist + staleness + artefatti), loopback-only |
| #25 | UI fase 2a: form connessioni (host.yaml) + run-from-browser (pipeline come subprocess della CLI), gate CSRF/rebinding/clickjacking |
| #26 | UI 2a hardening: panic recovery nel job, signal shutdown (niente sottoprocessi orfani), doc/banner corretti, race save-config |
| #27 | UI fase 2b: accettare azioni dal browser (upsert acceptances.json + rigenerazione checklist immediata) |

## Workflow (OBBLIGATORIO — non negoziabile)

- Lavora SOLO sul fork `andreavadacchino/cPanel_self-migration`. Push su remote
  `fork`, MAI su `origin` (tis24dev, read-only). PR verso il main del fork, merge
  con `gh pr merge N --merge`.
- Branch nuovo per ogni PR: `git checkout main && git pull fork main && git checkout -b <branch>`.
- **TDD rigoroso**: fixture/scenario reale → test RED → fix minimo → GREEN → refactor.
- Per OGNI PR, prima del push lancia un Go reviewer (agent
  `everything-claude-code:go-reviewer`) con un prompt che gli fa ATTACCARE le
  proprietà critiche; correggi i finding reali PRIMA di aprire la PR. Storia di
  questa sessione: il reviewer ha trovato bug HIGH veri su #23 (under-acceptance
  silenziosa), #26 (panic + sottoprocesso orfano, in un SECONDO giro su main
  mergiato) e #27 (perdita storico accettazioni + TOCTOU run/accept). Non
  saltarlo mai, e per superfici security-critical valuta un secondo giro.
- **Sourcery è rate-limited a livello account** fino a ~09/07/2026 (limite
  settimanale 500k caratteri di diff): NON produce review, il check "pass" è solo
  formale. Gate sostitutivo usato per #22–#27: go-reviewer + verifica completa in
  Docker (sotto). Dichiaralo nel commento di merge.

## Verifiche finali di ogni PR

```
go test ./internal/webui/ ./internal/accountinventory/ ./cmd/... -race
go test ./...
go vet ./...
go build ./cmd/cpanel-self-migration
gofmt -l <file toccati>
```

I 4 package macOS noti (`dbmig`, `maildir`, `migrate`, `webfiles`) falliscono su
macOS SOLO perché usano bash/sed GNU-only — NON sono regressioni. Verifica con
`git diff main -- <pkg>` (deve essere vuoto). La CI del fork non gira: **replica
la suite completa in Docker** prima di ogni merge (stesso ambiente della CI):

```
docker run --rm -v "$PWD":/src -w /src -e GOFLAGS=-buildvcs=false -e CGO_ENABLED=1 \
  golang:1.25 bash -c "go test ./... && go vet ./... && go test -race ./internal/webui/ ./internal/accountinventory/ ./cmd/... && echo LINUX_ALL_GREEN"
```

Golden Markdown: refresh con `UPDATE_GOLDEN=1`.

## Perimetro protetto

- `internal/migrate/runner.go` — off-limits alla linea inventory/UI. Unica
  eccezione già spesa: PR 7C tocca il call-site minimo di `runApply`
  (`opts.RunID = runID`).
- Scritture sui server VIETATE fino a PR 6D (protocollo dedicato: backup,
  rollback <60s, zona sacrificale, Orbit). La UI non apre SSH: lancia la CLI come
  subprocess; `--apply` resta terminal-only.

## Architettura UI (per orientarti veloce)

- `internal/webui/webui.go` — handler, gate di sicurezza (`route`,
  `requestIsLocal` anti-rebinding, `post` CSRF), config form (`saveConfig`,
  `writeValidatedConfig` — validazione delegata a `config.Load`), dashboard
  (`buildPage`, `staleInputs`).
- `internal/webui/job.go` — `jobManager` (uno slot singolo `busy` sotto mutex;
  `tryReserve`/`release` per la mutua esclusione run↔accept), `pipelineSteps` e
  `checklistStep(dir)` (condiviso), `execRunner` (subprocess della CLI),
  `tailBuffer`.
- `internal/webui/accept.go` — `saveAccept` (UI 2b): legge la checklist, verifica
  `Acceptable`, upsert via `accountinventory.MergeAcceptance`, rigenera la
  checklist; `writeJSONAtomic`.
- `internal/webui/templates/index.html` — pagina unica, zero JS (meta-refresh
  durante un run), form connessioni + run + accept inline.
- `cmd/cpanel-self-migration/ui_cmd.go` — sottocomando `ui`, signal handling che
  cancella il base context (uccide il subprocess) e drena via `srv.Shutdown`.

## Prossimi obiettivi (proponi tu quale, motiva, aspetta conferma)

**Lato UI:**
1. **UI fase 3 — monitor live apply**: seguire `events.jsonl` via SSE durante un
   `--apply` mostrando fase/item in tempo reale (gli eventi per-item ci sono già
   da PR 7C). ATTENZIONE: l'apply è l'operazione che scrive sui server — decidere
   se la UI lo LANCIA (con conferme forti) o solo lo MONITORA mentre gira da
   terminale. Il monitor-only è a rischio molto più basso e allineato al modello
   attuale ("la UI non muta i server").
2. **UI rifiniture**: revoca di un'accettazione dal browser (oggi si edita il
   file); persistenza del nome operatore; download degli artefatti.

**Lato pipeline (nessuna UI):**
3. **PR 6C — dns verify** (read-only): ri-fetch delle zone destination e confronto
   con un `dns_import_plan.json`, exit 3 su drift; il piano può essere rifiutato se
   gli input non corrispondono agli sha256 embedded. Prerequisito logico di 6D.
   Riusa `internal/sshtest` e `dns_zones.go`.
4. **PR 7E — inventario esteso** (capture-first come 6B-pre): email routing,
   default address, filtri, redirect + finding 3 dello smoke (DKIM rigenerati
   silenziosi → azione operatore dedicata). Riduce le azioni manuali cieche della
   checklist. Richiede capture reali via Orbit (sessione con TOTP).
5. **PR 6D — dns apply**: il PRIMO comando che scrive. Alto rischio, sessione
   dedicata, protocollo completo del CLAUDE.md di progetto.

Consiglio: la **3 (dns verify)** è il quick-win read-only a maggior valore
strutturale; la **1 monitor-only** è il completamento naturale della UI a basso
rischio; la **4** sblocca la checklist ma costa una sessione di capture Orbit.

## Come provare la UI adesso (demo con dati sintetici)

```
go build ./cmd/cpanel-self-migration
mkdir -p ~/Desktop/pADV/demo-run
./cpanel-self-migration ui --dir ~/Desktop/pADV/demo-run   # http://127.0.0.1:8422/
```

Directory vuota = stato vuoto. Per popolarla servono gli artefatti di una run
(inventari → diff → policy → checklist). In sessione precedente è stata usata una
demo sintetica generando due inventari con un piccolo main() throwaway che importa
`internal/accountinventory` (va eseguito DA DENTRO il repo con `go run ./tmp_gen`,
non da un file esterno: il package è internal), poi i tre subcomandi
`inventory diff|policy|checklist`.

## Regole di lavoro

Analizza in modo investigativo; quando trovi una soluzione rimettila in esame per
assicurarti al 100% che sia corretta. NON supporre, NON inventare, NON prendere
scorciatoie, NON fare regressioni. Testa prima, durante e dopo. Riusa
l'implementazione esistente il più possibile. Usa agenti specializzati in
parallelo e tutti gli strumenti disponibili. Sii brutalmente onesto, critico e
scettico. Feedback non verboso.
