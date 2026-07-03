# Prompt di avvio — prossima sessione di sviluppo (dopo PR #40–#49)

Copia-incolla da qui in giù.

---

Stai lavorando sul tool Go **cpanel-self-migration** (migrazione read-only-source
tra due account cPanel via SSH user-level password-auth; il binario gira dal Mac
dell'operatore come bridge SRC→relay→DEST), directory
/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration.

Leggi PRIMA di toccare qualsiasi cosa:
1. docs/dev/DEVELOPMENT_STATE.md — roadmap (ultimo merge: #49), mappa architettura, fatti reali dei server.
2. docs/dev/PR2B_EMAIL_APPLY_DESIGN.md — il design 2B congelato (emendato 2B-2: re-check pre-write per i writer distruttivi); la sezione slicing definisce 2B-3 (filtri+routing, l'ultima fetta email).
3. docs/dev/PR2B_2_SMOKE.md + PR2B_1_SMOKE.md — gli smoke reali dei due writer + i residui dichiarati.
4. docs/dev/PR2B_2_PRE_CAPTURES.md + PR2B_PRE_CAPTURES.md — primitive byte-verificate; not-probed rimasti: store_filter/get_filter round-trip, API2 setmxcheck, multi-target ADD, set_default_address fwdopt=fail/blackhole.
5. docs/dev/FASE0_2_FIRST_APPLY.md — gotcha del cluster DNS su .78 (⚠️ regole sotto).
6. docs/dev/MASTER_PLAN_COMPLETION.md — piano complessivo e modello cutover per-account (Fase 3).

## Stato al 2026-07-03 (sessione 2B-2 chiusa)

Fase 0 CHIUSA, 1A CHIUSA, 2B-1 MERGIATA (#47), **2B-2 MERGIATA (#49)** —
il tool ha il secondo config writer (autoresponder), provato sul server reale:

- **Collector body autoresponder** (`Email::get_auto_responder` per ogni
  indirizzo listato): chiude la riga 1A `autoresponder_bodies`
  not_collected; `AutoresponderEntry` arricchita (from/body/is_html/start/
  stop/charset + marcatore onestà `body_collected`); fixato il bug latente
  `email+"@"+domain` (le righe reali di list portano l'indirizzo COMPLETO e
  nessun campo domain — l'interval inventariato era sempre 0); fallimento
  get per-indirizzo → warning + body_collected=false, mai sezione persa;
  collection forwarders/autoresponders ora indipendenti per dominio.
- **`inventory email-plan`**: autoresponder ora create/skip/manual
  PROVABILI sui body (equality con normalizzazione ensure-trailing-newline
  byte-verificata; charset case-insensitive); manual terminale per:
  body non collezionato su uno dei lati (re-run inventory), contenuto
  DIVERSO sul dest (add_auto_responder UPSERTA → mai sovrascrivere),
  dominio mancante, indirizzo malformato. Le op skip portano il payload
  per il verify. Dest-only → informational.
- **`email apply`**: le create autoresponder passano la guardia batch PIÙ
  un re-check mirato per-indirizzo IMMEDIATAMENTE prima della scrittura
  (go-review round 1 HIGH: lo snapshot batch è stantio per le op tardive
  del loop e l'upsert è distruttivo); stesso re-check generalizzato al
  `set_default_address` (round 2 HIGH: sovrascrive il catch-all — un
  valore umano raced a metà run sarebbe stato distrutto irrecuperabilmente);
  i forwarder restano col solo guard batch (add additivo + dedupato).
  Verify-after incondizionato; backup con raw list + raw get per-indirizzo.
- **Rollback**: inverso di una create autoresponder = delete_auto_responder
  guardato da equivalenza contenuto (divergenza → refused); degraded senza
  report = MANUAL. **Primo rollback LIVE mai eseguito sul server reale**
  (chiude il residuo 2B-1).
- **`email verify`**: autoresponder escono da not_checked
  (applied/pending/drift/unavailable + untracked).
- **Safety**: `delete_auto_responder` aggiunto consapevolmente a TUTTE e
  tre le liste verbi vietati; allowlist invariata (email_apply.go +
  email_apply_cmd.go).
- **Smoke reale su .78** (PR2B_2_SMOKE.md, artefatti in
  `~/Desktop/pADV/cPanel_self-migration-captures/fase0_2-giorginisposi/smoke2b2/`):
  piano = 1 create AR + 3 skip reali; apply → 1 applied + backup; verify
  CLEAN (body multilinea accentato round-trip byte-identico); convergenza
  already_present senza backup; rollback dry-run → 1 inverso; **rollback
  LIVE → 1 applied**; verify finale = stato pre-smoke (MA-001/MA-006
  intatti). Eseguito due volte (post round 1 e col binario finale).
- **Gate**: go-reviewer adversariale 3 giri (R1 REQUEST CHANGES 1 HIGH +
  4 minori → fix; R2 REQUEST CHANGES 1 HIGH default-set + 1 LOW perf →
  fix; R3 APPROVE, zero findings); Docker LINUX_ALL_GREEN ×3. Sourcery
  rate-limited fino a ~09/07/2026 → gate sostitutivo dichiarato in PR.

Residui dichiarati (non bloccanti):
- `set_default_address fwdopt=fail/blackhole` non byte-verificati (primo
  account reale che li usa: verificare prima).
- is_html=1 e start/stop espliciti mai passati dal WRITER live (shapes raw
  byte-verificate in 2B-2-pre; derivazione parametri unit-locked).
- Follow-up round 3 (pre-esistente 2B-1): il rollback DEGRADED
  (`--accept-report-loss`) emette default_restore senza ExpectedCurrent →
  il divergence check è saltato su quel percorso esplicitamente opt-in.

Stato infra (invariato): account dest `giorginisposi` su .78 completo
(mail+web+db+forwarder+catch-all, NESSUN autoresponder); DNS in sicurezza
(peer NS in `standalone`, backup ruolo in /root su keliweb2; AutoSSL
escluso; zona di produzione intatta, serve ancora .193). `configs/host.yaml`
(600, gitignorato) ha credenziali VALIDE per entrambi i lati. Load su .193
alto: pesare le letture — le sessioni 2B non hanno MAI toccato .193.
Pending amministrativo Orbit (TOTP scaduto): registrare via
create_intervention le scritture 2B-pre/2B-1/2B-2-pre/2B-2 fatte via SSH
diretto sul sacrificale (forwarder round-trip, default round-trip,
autoresponder create+delete di probe e di smoke).

## Obiettivo della prossima sessione: 2B-3 — filtri email + routing

Dal design congelato (sezione slicing), con TDD rigoroso. ⚠️ GATED sulla
**decisione redaction dell'utente**: le regole filtro devono stare in
CHIARO nel piano/inventario per il round-trip `get_filter`→`store_filter`
(oggi il collector salva solo conteggi by design). Chiedere PRIMA di
iniziare. Poi:
1. **2B-3-pre** (~15 min su .78, harness throwaway): byte-verify di
   `Email::get_filter`/`store_filter` (shape delle regole!) e di API2
   `Email::setmxcheck` (routing local/remote round-trip via
   list_mxs). Pattern 2B-pre, archivio + doc.
2. Collector regole filtri (chiude la riga 1A `email_filter_rules`) —
   postura redaction secondo la decisione utente.
3. Op filtri nel piano + writer `store_filter` + apply/verify/rollback
   (inverso di una create filter = delete_filter — byte-verificare).
4. Routing: op `set` + writer API2 `setmxcheck` (nessun equivalente UAPI).
5. Smoke reale su .78 + doc + roadmap + handoff.

## Workflow (OBBLIGATORIO, invariato)

- SOLO fork andreavadacchino/cPanel_self-migration; push su fork, MAI su
  origin (tis24dev). PR verso il main del fork, merge con
  `gh pr merge N --merge` (post-push la PR può risultare UNSTABLE qualche
  secondo: attendi).
- Branch nuovo per PR: git checkout main && git pull fork main && git checkout -b <branch>.
- TDD rigoroso per OGNI modifica; go-reviewer multi-giro fino ad APPROVE
  pulito; Sourcery rate-limited fino a ~09/07/2026 → gate sostitutivo =
  go-reviewer + suite Docker, dichiararlo nella PR.
- Verifiche: go test ./... && go vet ./... && go build
  ./cmd/cpanel-self-migration; i 4 package macOS noti (dbmig, maildir,
  migrate, webfiles) falliscono su macOS solo per bash/GNU — git diff
  main -- <pkg> vuoto se non toccati; suite in Docker prima di ogni merge:
  docker run --rm -v "$PWD":/src -w /src -e GOFLAGS=-buildvcs=false -e CGO_ENABLED=1 golang:1.25 bash -c "go test ./... && go vet ./... && echo LINUX_ALL_GREEN".
- Perimetro protetto: internal/migrate/runner.go off-limits; scritture DNS
  vietate fino a PR 6D; le UNICHE scritture ammesse sono le email-config
  sul dest SACRIFICALE .78 via il writer (o harness throwaway MAI
  committato, precedente 5B/5C, per reset/byte-verify sul banco).
- ⚠️ Mai removeacct/killdns su .78 o .193 (cluster DNS: delete propagata
  = dominio offline). Mai toccare il ruolo del peer DNS senza decisione
  esplicita dell'utente.

## Dopo la 2B-3 (ordine)

2A (cron apply), poi 6D (dns apply = lo switch per-account del cutover).
Decisioni aperte utente: postura redaction filtri (GATE della 2B-3); data
di partenza campagna; ripristino ruolo sync al primo cutover.

Analizza in modo investigativo; quando trovi una soluzione rimettila in
esame per assicurarti al 100% che sia corretta. NON supporre, NON
inventare, NON prendere scorciatoie, NON fare regressioni. Testa prima,
durante e dopo. Riusa l'implementazione esistente il più possibile. Usa
un team di agenti specializzati in parallelo (modello opus/sonnet) e
tutti gli strumenti disponibili. Sii brutalmente onesto e sincero, ma
critico e scettico. Feedback non verboso.
