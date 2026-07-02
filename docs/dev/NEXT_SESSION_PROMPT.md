# Prompt per la prossima sessione di sviluppo

Copia il blocco qui sotto come primo messaggio della nuova sessione.

---

Stai lavorando sul tool Go **cpanel-self-migration** (migrazione read-only-source
tra due account cPanel via SSH user-level password-auth; il binario gira dal Mac
dell'operatore come bridge SRC→relay→DEST), directory
`/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration`.

Leggi PRIMA di toccare qualsiasi cosa:
1. `docs/dev/MASTER_PLAN_COMPLETION.md` — il piano di completamento (Fasi 0-5) con le decisioni aperte.
2. `docs/dev/DEVELOPMENT_STATE.md` — roadmap (ultimo merge: **#38**), mappa architettura, fatti reali del server.
3. `docs/dev/PR7E_REAL_SMOKE.md` — metodo replay offline e risultati dello smoke 7E.

## Stato al 2026-07-02 sera

**Fase 0.1 CHIUSA (PR #38 mergiata):** smoke 7E passato al 100% in replay
offline (zero contatto server, zero TOTP): 20 rewrite CMS → expected
differences senza azioni finte; blocking 11→8 (i 3 check ciechi sostituiti
dalla logica per-item); DKIM → 4 CONFIRM_DNS_RECORD non bloccanti; SPF ancora
0 manual; guardia dest-stantia regge (4× POL-SECTION-UNAVAILABLE, mai ok
silenzioso); italplant routing remote pulito + il 301 genuino → esattamente
una CONFIRM_REDIRECT non bloccante. Bonus: le 11 sezioni pre-7E sono
multiset-identiche al source del 7A → zero drift dei collector da #32-#35.

**Capture archiviate** (gli scratchpad /private/tmp NON sopravvivono ai
riavvii) in `~/Desktop/pADV/cPanel_self-migration-captures/`:
`doctorbike-full-setA` (autoritativo, zona DNS 61 record; setB scartato,
parse_zone parziale 19 record), `cap7e/{doctorbike,italplant}`,
`7a-artifacts` (incl. destination simulata .78 e report.json apply),
`italplant-scaffold` (list_domains/domains_data SINTETICI, solo scaffold).
Harness di replay: ricostruirlo al bisogno come `cmd/replay-smoke/main.go`
(mai committato — vedi PR7E_REAL_SMOKE.md per il contratto: Runner che
serve le capture per module::func + ARG_0).

**Analisi a 4 agenti completata (in MASTER_PLAN_COMPLETION.md):** motore
upstream maturo (test>codice, fail-closed, fault-sim) ma MAI eseguito da noi;
17 aree account non inventariate e oggi silenziose; ~27/32 aree
automatizzabili user-level (SSL::show_key esporta le chiavi private; filtri
email round-trip 1:1; routing solo API2 setmxcheck; cron meglio via crontab
SSH); nessuna capacità multi-account (config a coppia singola). Limiti motore:
CMS rewrite 8/24, PrestaShop 1.7+ NON rilevato, DB_HOST mai riscritto,
CHECKSUM cross-version degradato (rilevante: CentOS7→CL9.8).

## Obiettivo della sessione: FASE 0.2 — primo `--apply` reale

Il milestone mancante dell'intero progetto. Account sacrificale SCELTO:
**giorginisposi** (giorginisposi.it su .193 = 194.76.118.193). Verificato da
fuori: WordPress 6.6.5 + WPBakery + CF7 + EventON, **niente WooCommerce**,
sito vetrina vivo (apex 301 → www 200) — caso ideale: wp-config rewrite è il
percorso più maturo del motore. Il candidato `carrozzeriaberto` è stato
scartato (dominio senza DNS, non verificabile da fuori).
NON ancora verificato (interrotto): numeri via Orbit.

Sequenza:
1. **Sessione Orbit** (chiedi TOTP; l'utente ha già usato "524932 yolo" il
   02/07 ~20:54 UTC, sessione 2h con YOLO — probabilmente scaduta: chiedine
   uno nuovo). Poi verifica giorginisposi: `whm_list_servers` →
   `whm_list_accounts` (search user/domain) per username esatto, disco, plan,
   suspended; `superadmin_find_site` → cpanel_list_databases /
   list_email_accounts / list_cron_jobs / list_forwarders. Vuoi: disco
   contenuto (<5-10GB), almeno 1 DB + qualche mailbox (senò il test prova
   poco), shell abilitata.
2. **Prerequisiti dall'utente**: password cPanel di giorginisposi su .193
   (o reset da WHM); creazione account destinazione su .78 via WHM (root
   disponibile: `ssh keliweb2`, WHM 136) con stesso dominio, shell abilitata.
3. **host.yaml** (mode 600, `configs/host.yaml` accanto al binario): src=
   .193/giorginisposi, dest=.78/nuovo account. Il tool gira dal Mac,
   password-auth, TOFU host-key. `make build`.
4. **Dry-run PRIMA** (`./cpanel-self-migration --json-events --report-json`):
   analizza+confronta, zero scritture. Esamina logs/mail_analysis.log,
   web_analysis.log, db_analysis.log. Aspettati sorprese ambiente
   (CentOS7 source: versioni tar/mysqldump, jailshell, GTID assente=MariaDB?).
5. **`--apply`** con l'utente presente, poi le verify del tool; poi pipeline
   inventario completa (source+dest reali) → diff → policy → checklist con
   il report.json REALE: prima checklist con evidenza vera non simulata.
6. **Documentare**: `docs/dev/FASE0_2_FIRST_APPLY.md` stile PR7A_REAL_SMOKE
   (divergenze osservate = oro per la Fase 5), riga roadmap, PR docs sul
   fork; `create_intervention` su Orbit (site_id WordPress, MAI cPanel).

Classificazione rischio (CLAUDE.md Server-VPS): **medio** — source
strettamente read-only per costruzione; le scritture vanno SOLO sull'account
destinazione nuovo e vuoto su .78 dove nessun DNS punta; NESSUN cutover in
questo test. Rollback: l'account dest si butta e si ricrea.

## Workflow (OBBLIGATORIO, invariato)

- SOLO fork andreavadacchino/cPanel_self-migration; push su `fork`, MAI su
  origin (tis24dev). PR verso il main del fork, merge `gh pr merge N --merge`
  (attendi che Sourcery/mergeability si sblocchi: subito dopo il push la PR
  può risultare UNSTABLE per qualche secondo).
- Branch nuovo per PR: `git checkout main && git pull fork main && git checkout -b <branch>`.
- TDD rigoroso per OGNI modifica al codice; go-reviewer multi-giro
  (everything-claude-code:go-reviewer) fino ad APPROVE pulito; Sourcery
  rate-limited fino a ~09/07/2026 → gate sostitutivo = go-reviewer + suite
  Docker, dichiararlo nel commento di merge.
- Verifiche: `go test ./... && go vet ./... && go build ./cmd/cpanel-self-migration`;
  i 4 package macOS noti (dbmig, maildir, migrate, webfiles) falliscono su
  macOS solo per bash/GNU — `git diff main -- <pkg>` deve essere vuoto; suite
  completa in Docker prima di ogni merge:
  `docker run --rm -v "$PWD":/src -w /src -e GOFLAGS=-buildvcs=false -e CGO_ENABLED=1 golang:1.25 bash -c "go test ./... && go vet ./... && echo LINUX_ALL_GREEN"`.
- Perimetro protetto: `internal/migrate/runner.go` off-limits; scritture DNS
  vietate fino a PR 6D; la Fase 0.2 NON modifica codice (solo esecuzione +
  docs), salvo bug bloccanti trovati dal dry-run (in tal caso: PR separata
  con TDD).

## Dopo la 0.2 (ordine dal MASTER_PLAN)

0.3 censimento di massa (inventory source-only su tutti gli account dei
server sorgente — decide le priorità di Fase 1-2 con dati), poi 1A coverage
manifest, 1B collector batch 1. Decisioni aperte da sciogliere con l'utente:
meccanismo credenziali di massa, taglio minimo vs piano completo, postura
redaction sulle regole filtri (servono in chiaro per il round-trip 2B).

Analizza in modo investigativo; quando trovi una soluzione rimettila in esame
per assicurarti al 100% che sia corretta. NON supporre, NON inventare, NON
prendere scorciatoie, NON fare regressioni. Testa prima, durante e dopo.
Riusa l'implementazione esistente il più possibile. Usa un team di agenti
specializzati in parallelo e tutti gli strumenti disponibili. Sii brutalmente
onesto e sincero, ma critico e scettico. Feedback non verboso.
