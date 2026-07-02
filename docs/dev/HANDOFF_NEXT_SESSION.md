# Prompt di avvio — prossima sessione di sviluppo (dopo PR #40–#45)

Copia-incolla da qui in giù.

---

Stai lavorando sul tool Go **cpanel-self-migration** (migrazione read-only-source
tra due account cPanel via SSH user-level password-auth; il binario gira dal Mac
dell'operatore come bridge SRC→relay→DEST), directory
/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration.

Leggi PRIMA di toccare qualsiasi cosa:
1. docs/dev/DEVELOPMENT_STATE.md — roadmap (ultimo merge: #45), mappa architettura, fatti reali dei server.
2. docs/dev/PR2B_EMAIL_APPLY_DESIGN.md — il design v2 (post review adversariale) del writer che DEVI implementare.
3. docs/dev/PR2B_PRE_CAPTURES.md — parametri e comportamenti verificati al byte delle primitive di scrittura.
4. docs/dev/FASE0_2_FIRST_APPLY.md — il primo apply reale + i gotcha del cluster DNS su .78.
5. docs/dev/MASTER_PLAN_COMPLETION.md — piano complessivo (0.3 SALTATO; modello cutover per-account in Fase 3).

## Stato al 2026-07-03 notte

Fase 0 CHIUSA, Fase 1A CHIUSA, design 2B congelato:
- **#40**: il motore ora supporta il layout 1:1 main→main stesso FQDN
  (carve-out classificatore + opt-in `AllowDestPublicHTMLRoot` nella guardia
  webfiles; backup path rifiuta il root anche col flag).
- **#41 / Fase 0.2**: primo `--apply` reale giorginisposi .193→.78, 14/14
  fasi verdi (mail 379 msg body-hash, web 12.521 entry/1.1 GB nel
  public_html root, db 32 tabelle + wp-config rewrite); sito vivo su .78
  via `curl --resolve`. Prima checklist con evidenza reale
  (`chain_verified`, per_item).
- **#42/#43 / 1A**: coverage manifest — 35 aree dichiarate (15 covered,
  2 root_only, 18 not_collected), puramente dichiarativo, lockstep test.
  Censimento 0.3 SALTATO (decisione utente: priorità 2A/2B già provate
  dall'evidenza reale).
- **#44**: design 2B v2 — email-config apply. Punti NON negoziabili:
  guardia di freschezza PER-OP (mai snapshot di sezione), forward
  non-single-address (virgole/pipe/script/:fail:/:blackhole:) → `manual`
  terminale, rollback guidato dal REPORT (input obbligatorio, degradazione
  documentata se perso), verify-after per-op incondizionato, dry-run
  offline con disclosure "plan-based", scritture SOLO via RunUAPI con nomi
  LITERAL, due nuovi safety test (glob emailplan + primo scan module-wide
  con allowlist per-file). Registrato anche il modello di cutover
  per-account (finestra sync, sospensione source OBBLIGATORIA, mai
  removeacct su nessun lato).
- **#45 / 2B-pre**: primitive verificate al byte su giorginisposi@.78:
  `Email::add_forwarder domain= email=<local> fwdopt=fwd fwdemail=` (doppio
  add identico DEDUPLICA), `Email::delete_forwarder address= forwarder=`,
  `Email::set_default_address domain= fwdopt= fwdemail=`; default fresco =
  username. I valori REALI di MA-001/MA-006 sono stati applicati: la
  pipeline rieseguita mostra le due azioni SPARITE per convergenza (6→4).

Stato infra: account dest `giorginisposi` su .78 completo (mail+web+db+
forwarder+catch-all); DNS in sicurezza (peer NS in `standalone`, backup
ruolo in /root su keliweb2; AutoSSL escluso; zona di produzione intatta,
serve ancora .193). `configs/host.yaml` (600, gitignorato) ha credenziali
VALIDE per entrambi i lati. Capture in
~/Desktop/pADV/cPanel_self-migration-captures/fase0_2-giorginisposi/
(dryrun1-3, apply1, pipeline, pipeline2, cap2bpre — gli scratchpad /tmp
NON sopravvivono ai riavvii). Load su .193 alto (~12-14 su 4 core): pesare
le letture. Sessione Orbit: serve TOTP nuovo; pending amministrativo:
aggiornare l'intervento Orbit con le scritture 2B-pre fatte via SSH
diretto a sessione scaduta.

## Obiettivo della sessione: 2B-1 — primo config writer

Implementare con TDD rigoroso, dal design #44 SENZA modificarlo (se trovi
un errore di design: fermati e discutilo):
1. `inventory email-plan` (offline, `emailplan*.go` in accountinventory,
   specchio di dnsplan*.go): ops create/set/skip/manual per forwarders +
   default_address, sha256 inventari embedded, policy come contesto mai
   gate, `--output-json/-md`.
2. `email apply` (namespace nuovo accanto a `dns`; SOLO dest,
   `sshx.DialDest`): dry-run default offline, `--yes-apply-writes`,
   backup-o-niente accoppiato bidirezionalmente al report, guardia
   freschezza per-op, `already_present`/`refused_precondition`/`applied`/
   `failed`, verify-after per-op, `--rollback` (report obbligatorio,
   delete_forwarder SOLO per i propri applied).
3. `email verify` (read-only, gate sha256 piano stantio,
   `--fail-on-drift` → exit 3).
4. Writer in `internal/cpanel/email_apply.go` via RunUAPI nomi literal;
   i due safety test nuovi; estensione `writeCalls` in
   checklist_safety_test.go.
5. Smoke reale sul banco di prova: reset su .78 (`delete_forwarder` +
   `set_default_address` fwdemail=giorginisposi? — NO: il reset del
   default su username va verificato, in dubbio lasciare lo stato attuale
   e testare solo il ramo already_present/skip) → email-plan → apply →
   verify → pipeline → checklist. Documentare in PR2B_1_SMOKE o simile.

Esci dalla sessione con: PR 2B-1 mergiata + smoke reale documentato +
riga roadmap + handoff aggiornato.

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
  vietate fino a PR 6D; le UNICHE scritture ammesse in questa sessione
  sono le email-config sul dest SACRIFICALE .78 via il nuovo writer.
- ⚠️ Mai removeacct/killdns su .78 o .193 (cluster DNS: delete propagata
  = dominio offline). Mai toccare il ruolo del peer DNS senza decisione
  esplicita dell'utente.

## Dopo la 2B-1 (ordine)

2B-2 (collector corpi autoresponder + create op), 2B-3 (regole filtri —
GATED sulla decisione redaction dell'utente — + routing API2 setmxcheck),
2A (cron apply), poi 6D (dns apply = lo switch per-account del cutover).
Decisioni aperte utente: postura redaction filtri; data di partenza
campagna; ripristino ruolo sync al primo cutover.

Analizza in modo investigativo; quando trovi una soluzione rimettila in
esame per assicurarti al 100% che sia corretta. NON supporre, NON
inventare, NON prendere scorciatoie, NON fare regressioni. Testa prima,
durante e dopo. Riusa l'implementazione esistente il più possibile. Usa
un team di agenti specializzati in parallelo e tutti gli strumenti
disponibili. Sii brutalmente onesto e sincero, ma critico e scettico.
Feedback non verboso.
