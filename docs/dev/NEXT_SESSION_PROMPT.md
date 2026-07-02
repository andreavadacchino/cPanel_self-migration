# Prompt per la prossima sessione di sviluppo

Copia il blocco qui sotto come primo messaggio della nuova sessione.

---

Stai lavorando sul tool Go **cpanel-self-migration** (migrazione read-only-source
tra due account cPanel via SSH user-level), directory
`/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration`.

Leggi PRIMA di toccare codice:
1. `docs/dev/DEVELOPMENT_STATE.md` — roadmap, mappa architettura, fatti reali del server (flexInt64/flexStringList, metodo smoke via Orbit con capture base64 + verifica md5 server-side fail-safe).
2. Linea DNS: `PR6A_DNS_IMPORT_DESIGN.md` (contratto 6D), `PR6B_PRE_CAPTURES.md` (fatti write-API: mass_edit_zone line_index-addressed, serial guard), `PR6C_DNS_VERIFY_DESIGN.md`.
3. Linea 7E: `PR7E_PRE_CAPTURES.md` (capture byte-verificate) e `PR7E_DESIGN.md` (incluso "Post-review hardening").
4. UI: `UI2A_CONNECTIONS_RUN_DESIGN.md`, `UI2B_ACCEPT_DESIGN.md`, `UI3_APPLY_MONITOR_DESIGN.md`.

Contesto in una riga: pipeline read-only completa (inventario SSH → diff → policy → dns-plan → checklist) con catena provenienza sha256, evidenza per_item (7C), accettazioni operatore (7D), dns verify (6C), UI web locale zero-JS monitor-only; da PR 7E le quattro aree ex-cieche (email_routing, default_address, email_filters, redirects) sono sezioni reali con azioni mirate. `--apply` e la futura scrittura DNS restano SOLO da terminale.

Stato del fork (ultimo merge: #35, main 7feaf89):
- #32 — dispatch: `inventory` senza/con subcommand ignoto → exit 2 + usage (prima cadeva nel flusso migrazione); test E2E via TestMain re-exec del vero main().
- #33 — 7E-pre: capture reali (routing local doctorbike / remote italplant; default address copre i subdomini in una chiamata; filtri vuoti ovunque, shape non-vuota docs-derived; Mime::list_redirects = harvest .htaccess, rumore CMS dominante, 1 solo 301 vero). Metodo transfer migliorato: md5 LOCALI verificati dal server con `md5sum -c` (corruzione trascrizione → falso FAILED, mai falso OK).
- #34 — 7E-1 collectors: 4 chiamate UAPI read-only + 4 sezioni inventario. Filtri: SOLO conteggi (rules/actions restano json.RawMessage nel layer cpanel, mai negli artefatti). Tie-break deterministici completi (l'ordine array del backend Perl non è stabile). Warning scope-ridotto quando la lista mailbox fallisce ma list_filters riesce.
- #35 — 7E-2 pipeline: diff (routing confronta SOLO routing+always_accept; detail redirect = `kind/type/status → destination` come canale di classificazione), policy (POL-MAILROUTE/DEFAULTADDR/EMAILFILTER via evalSimple; evalRedirects separa rumore CMS→info da redirect veri→review), checklist (azioni per-item: CONFIRM_EMAIL_ROUTING blocking, MANUAL_CHECK_REQUIRED default address blocking, RECREATE_EMAIL_FILTERS nuovo tipo blocking acceptable, CONFIRM_REDIRECT nuovo tipo non-blocking; rewrite CMS rimossi = expected difference). DKIM: plan replace su TXT `._domainkey` → azione CONFIRM_DNS_RECORD non-blocking (finding 3 smoke 7A chiuso). buildNotInventoriedSection rimossa (stato not_inventoried resta nello schema per artefatti vecchi).

Hardening post-review #35 (2 HIGH riprodotti empiricamente dal reviewer, corretti e pinnati):
1. POL-SECTION-MISSING (review) per ogni sezione attesa assente dal diff — un artefatto pre-7E non può più nascondere una divergenza dietro SectionOK/READY_TO_CUTOVER (chiuso buco PREESISTENTE su tutte le sezioni). Test: TestChecklistStaleDiffMissingSectionNeverReadsOK.
2. Predicato CMS = rewrite+temporary+no-status E destinazione NON-URL (tutte le 34 rewrite CMS catturate puntano a path relativi `%{ENV:REWRITEBASE}`; i redirect operatore puntano sempre a URL assolute). Un temporary operatore senza statuscode resta genuino.
3. Warning "MX exchangers non verificati" scopato alle zone che ospitano domini di routing (dnsSkipTouchesRouting).

Lezioni di sessione (valgono oro):
- Il go-reviewer multi-giro ha trovato HIGH veri anche su #35 (giro 1: 2 HIGH con repro empirici — falso READY_TO_CUTOVER da diff stantio, falso negativo CMS; giro 2 APPROVE + 1 MEDIUM sul fix; giro 3 APPROVE). Su superfici critiche NON fermarsi al primo APPROVE: i giri continuano finché non escono puliti, e i fix tornano allo STESSO reviewer.
- Metodo capture: MAI trascrivere indici md5 a mano (un carattere corrotto passa inosservato); trascrivere solo i blob contenuto e far verificare gli md5 LOCALI al server con `md5sum -c` — la direzione fail-safe è l'unica accettabile.

Workflow (OBBLIGATORIO):
- SOLO fork andreavadacchino/cPanel_self-migration; push su `fork`, MAI su origin (tis24dev). PR verso il main del fork, merge con `gh pr merge N --merge`.
- Branch nuovo per PR: `git checkout main && git pull fork main && git checkout -b <branch>`.
- TDD rigoroso: scenario reale → RED → fix minimo → GREEN.
- Per OGNI PR con codice: go-reviewer (agent everything-claude-code:go-reviewer) con prompt che gli fa ATTACCARE le proprietà critiche con controesempi concreti; multi-giro fino ad APPROVE via SendMessage. Storico HIGH veri: #23, #26, #27, #30, #35.
- Sourcery rate-limited fino a ~09/07/2026: gate sostitutivo = go-reviewer multi-giro + suite Docker. Dichiararlo nel commento di merge.

Verifiche finali di ogni PR:
go test ./internal/webui/ ./internal/accountinventory/ ./cmd/... ./internal/sshx/ ./internal/cpanel/ -race
go test ./...   &&   go vet ./...   &&   go build ./cmd/cpanel-self-migration
gofmt -l SOLO sui file toccati (violazioni PREESISTENTI su main in vari file, tra cui main.go, diff_write_test.go, dnsplan_test.go, dnsplan_write_test.go — verificate col confronto su main, NON riformattarle).
I 4 package macOS noti (dbmig, maildir, migrate, webfiles) falliscono su macOS solo per bash/sed GNU — verificare `git diff main -- <pkg>` vuoto. La CI del fork non gira: replica la suite in Docker prima di ogni merge:
docker run --rm -v "$PWD":/src -w /src -e GOFLAGS=-buildvcs=false -e CGO_ENABLED=1 golang:1.25 bash -c "go test ./... && go vet ./... && go test -race ./internal/webui/ ./internal/accountinventory/ ./cmd/... && echo LINUX_ALL_GREEN"
Golden: UPDATE_GOLDEN=1, poi rileggere il diff hunk per hunk.

Perimetro protetto: `internal/migrate/runner.go` off-limits. Scritture sui server VIETATE fino a PR 6D. La UI non apre SSH e non lancia --apply; dns verify apre SSH READ-ONLY verso il solo destination. I safety test DNS (lexical module-wide + strutturale literal-names in dns_safety_test.go) sono il lucchetto di 6D: 6D dovrà emendarli CONSAPEVOLMENTE con una allowlist per i propri file.

Prossimi obiettivi (proponi tu quale, motiva, aspetta la mia conferma prima di scrivere codice):
1. **PR 6D — dns apply**: il PRIMO comando che scrive. L'unico obiettivo sostanziale rimasto. Alto rischio — sessione dedicata con l'utente presente: protocollo completo del CLAUDE.md (backup, rollback <60s), zona sacrificale su principiadv.online, approvazioni Orbit live (o yolo esplicito). Contratto: mass_edit_zone atomica serial-guarded, mai delete di record destination, mai NS/SOA, backup-file-o-niente-write. 6C fornisce già la certificazione post-apply (`dns verify --fail-on-drift`).
2. **Smoke reale 7E** (~1h, read-only, richiede TOTP Orbit): rigirare la pipeline completa su capture fresche doctorbike/italplant per validare le nuove sezioni su dati veri (le 15 rewrite CMS di doctorbike devono produrre expected differences, zero azioni finte; il routing remote di italplant deve restare pulito).
3. Follow-up LOW dei reviewer #34/#35 (riempitivi): chiavi diff NUL-framed invece di separatori spazio/slash; esenzione CMS anche per redirect Changed (oggi solo Removed, asimmetrica); azione dedicata per filtri -CHANGED.
4. UI rifiniture (basso valore, zero blocchi): revoca accettazione dal browser, persistenza nome operatore, download artefatti.

Consiglio: la 2 è il primo passo naturale della prossima sessione con TOTP (validare 7E su dati reali prima di costruirci sopra); la 1 va pianificata come sessione dedicata con te presente dall'inizio alla fine.

Analizza in modo investigativo; quando trovi una soluzione rimettila in esame per assicurarti al 100% che sia corretta. NON supporre, NON inventare, NON prendere scorciatoie, NON fare regressioni. Testa prima, durante e dopo. Riusa l'implementazione esistente il più possibile. Usa un team di agenti specializzati in parallelo e tutti gli strumenti disponibili. Sii brutalmente onesto e sincero, ma critico e scettico. Feedback non verboso.
