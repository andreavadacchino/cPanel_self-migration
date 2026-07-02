# Prompt di avvio — prossima sessione di sviluppo (dopo PR #40–#47)

Copia-incolla da qui in giù.

---

Stai lavorando sul tool Go **cpanel-self-migration** (migrazione read-only-source
tra due account cPanel via SSH user-level password-auth; il binario gira dal Mac
dell'operatore come bridge SRC→relay→DEST), directory
/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration.

Leggi PRIMA di toccare qualsiasi cosa:
1. docs/dev/DEVELOPMENT_STATE.md — roadmap (ultimo merge: #47), mappa architettura, fatti reali dei server.
2. docs/dev/PR2B_EMAIL_APPLY_DESIGN.md — il design 2B congelato; la sezione slicing definisce 2B-2 (autoresponder) e 2B-3 (filtri+routing).
3. docs/dev/PR2B_1_SMOKE.md — lo smoke reale del primo writer + i residui dichiarati (fail/blackhole non byte-verificati).
4. docs/dev/PR2B_PRE_CAPTURES.md — primitive byte-verificate + lista not-probed (add_auto_responder, store_filter, setmxcheck, multi-target ADD).
5. docs/dev/FASE0_2_FIRST_APPLY.md — gotcha del cluster DNS su .78 (⚠️ regole sotto).
6. docs/dev/MASTER_PLAN_COMPLETION.md — piano complessivo e modello cutover per-account (Fase 3).

## Stato al 2026-07-03 (sessione 2B-1 chiusa)

Fase 0 CHIUSA, 1A CHIUSA, **2B-1 MERGIATA (#47)** — il tool ha il suo primo
config writer, provato sul server reale:

- **`inventory email-plan`** (offline): ops create/set/skip/manual per
  forwarders + default address; autoresponders/filtri/routing portati nel
  piano come skip/manual finché 2B-2/2B-3 non atterrano; euristica
  fresh-default a prefisso (:fail:/:blackhole:, locale-safe); assunzione
  documentata verbatim nel MD; sha256 inventari embedded; policy solo contesto.
- **`email apply`** (namespace `email` accanto a `dns`, SOLO dest via
  `sshx.DialDest`): dry-run offline default (zero connessioni),
  `--yes-apply-writes`, backup-or-nothing accoppiato bidirezionalmente al
  report (raw UAPI + entries normalizzate), guardia freshness PER-OP
  outcome-first (already_present/refused_precondition, mai abort di sezione),
  verify-after incondizionato, exit 1 failed / 3 refused.
  `--rollback <backup>`: inversi SOLO per gli applied propri (already_present
  MAI invertito), rifiuto su stato divergente, degradazione report-loss
  dietro `--accept-report-loss`, dry-run offline.
- **`email verify`**: read-only, gate sha256 piano stantio, `--fail-on-drift`
  → exit 3; statuses dnsverify-style + untracked informational.
- **Safety**: emailplan_safety_test (glob), TestNoEmailWritePatternsModuleWide
  (PRIMA allowlist per-file: solo email_apply.go + email_apply_cmd.go),
  writeCalls checklist estesi, RunUAPIRaw aggiunto e coperto dal guard
  strutturale TestDNSAPICallsUseLiteralNames.
- **Smoke reale su .78** (PR2B_1_SMOKE.md, artefatti in
  `~/Desktop/pADV/cPanel_self-migration-captures/fase0_2-giorginisposi/smoke2b1/`):
  piano = esattamente MA-001/MA-006; convergenza live (2 already_present,
  NESSUN backup creato); write reale → 1 applied + backup + verify-after;
  verify finale CLEAN; rollback dry-run inverte solo la propria create.
  Restore bare-username byte-verificato (finding HIGH del go-review, chiuso
  con round-trip reale). Stato post-smoke = pre-smoke (MA-001/MA-006 vivi).
- **Gate**: go-reviewer adversariale 2 giri (round 1 REQUEST CHANGES → fix →
  round 2 APPROVE, zero violazioni invarianti); Docker LINUX_ALL_GREEN ×2.
  Sourcery rate-limited fino a ~09/07/2026 → gate sostitutivo dichiarato in PR.

Stato infra (invariato): account dest `giorginisposi` su .78 completo
(mail+web+db+forwarder+catch-all); DNS in sicurezza (peer NS in `standalone`,
backup ruolo in /root su keliweb2; AutoSSL escluso; zona di produzione
intatta, serve ancora .193). `configs/host.yaml` (600, gitignorato) ha
credenziali VALIDE per entrambi i lati. Load su .193 alto (~12-14 su 4 core):
pesare le letture — lo smoke 2B-1 non ha MAI toccato .193 (piani costruiti
dalle capture pipeline/pipeline2). Sessione Orbit: serve TOTP nuovo; pending
amministrativo: aggiornare l'intervento Orbit con le scritture 2B-pre + 2B-1
fatte via SSH diretto (delete/re-add forwarder, round-trip default) a
sessione scaduta.

## Obiettivo della sessione: 2B-2 — autoresponder

Dal design congelato (sezione slicing), con TDD rigoroso:
1. **2B-2-pre** (serve l'utente, ~15 min su .78): byte-verifica di
   `Email::add_auto_responder` (param names, body round-trip, is_html/
   interval/start/stop shapes) e `Email::get_auto_responder` (il body!).
   Creare un autoresponder di prova su giorginisposi@.78, round-trip,
   pulizia. Archivio in captures + doc (pattern 2B-pre).
2. **Collector body autoresponder** (`get_auto_responder`): chiude la riga
   1A `autoresponder_bodies` not_collected; estende l'inventory (nuova
   sezione o arricchimento AutoresponderEntry — decidere rispetto al
   lockstep coverage test) + diff/policy se serve.
3. **Op `create` autoresponder in email-plan** (sostituisce il manual
   incondizionato di 2B-1: con i body collezionati il confronto diventa
   provabile → skip onesto quando identico) + writer `add_auto_responder`
   in email_apply.go (emendamento CONSAPEVOLE dell'allowlist? NO — il verb
   è già nella forbidden list e email_apply.go è già allowlisted) + ramo
   apply/verify/rollback (l'inverso di una create autoresponder è
   delete_auto_responder — da byte-verificare in 2B-2-pre).
4. Smoke reale su .78 + doc + roadmap + handoff.

Residui 2B-1 da tenere d'occhio (non bloccanti, dichiarati):
- `set_default_address fwdopt=fail/blackhole` non byte-verificati (primo
  account reale che li usa: verificare prima).
- Rollback live mai eseguito sul server reale (coperto da sshtest E2E).

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

## Dopo la 2B-2 (ordine)

2B-3 (regole filtri — GATED sulla decisione redaction dell'utente — +
routing API2 setmxcheck), 2A (cron apply), poi 6D (dns apply = lo switch
per-account del cutover). Decisioni aperte utente: postura redaction
filtri; data di partenza campagna; ripristino ruolo sync al primo cutover.

Analizza in modo investigativo; quando trovi una soluzione rimettila in
esame per assicurarti al 100% che sia corretta. NON supporre, NON
inventare, NON prendere scorciatoie, NON fare regressioni. Testa prima,
durante e dopo. Riusa l'implementazione esistente il più possibile. Usa
un team di agenti specializzati in parallelo e tutti gli strumenti
disponibili. Sii brutalmente onesto e sincero, ma critico e scettico.
Feedback non verboso.
