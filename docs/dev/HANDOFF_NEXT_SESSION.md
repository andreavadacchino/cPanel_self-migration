# Prompt di avvio — prossima sessione

Copia-incolla da qui in giù.

---

Stai lavorando sul tool Go **cpanel-self-migration**, directory
/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration.

Leggi PRIMA: DEVELOPMENT_STATE.md, COMMAND.md, PR61_BLOCKER_SCOPING.md.

## Stato al 2026-07-04 (fine giornata)

### PR mergiate oggi

| PR | Contenuto | Gate |
|---|---|---|
| #59 | UI-driven migration exec (10 azioni, conferma forte, artifact attach, auto-transition) | R1→R2→R3 APPROVE, Docker ×2 |
| #60 | HTML exec forms + dogfooding #1 report | Template-only, test pass |
| #61 | UI-complete cycle (create session, pipeline, plans, blocker scoping) | R1→R2 APPROVE, Docker ×2 |

### Stato architetturale

La Workbench è ora un prodotto UI-complete per il ciclo single-account:
- **Create**: POST /workbench/create (form nel browser)
- **Pipeline**: exec action `run_pipeline` (4-step, 5 artifact auto-attach)
- **Plans**: exec actions `dns_plan`, `email_plan`, `cron_plan`
- **Acceptances**: form per-azione su dashboard `/`
- **Apply**: exec actions `dns_apply`, `email_apply`, `cron_apply` + `migrate_content` (conferma forte)
- **Verify**: exec actions `dns_verify`, `email_verify`, `cron_verify` (click singolo)
- **Rollback**: exec actions `dns_rollback`, `email_rollback`, `cron_rollback` (doppia conferma)
- **Auto-transition**: `ready_for_cutover` automatico quando tutti e 3 verify CLEAN
- **Blocker scoping**: apply gateato solo da `blocks_apply` (2 regole); `blocks_cutover` (8 regole) non impedisce apply
- **Governance**: shared jobManager lock (no race con /run e /accept)

### Invarianti emendati

1. **#58 → #59**: workbench.go resta governance-only; workbench_exec.go PUÒ exec con conferma forte (AST-enforced)
2. **#61**: blocker scoping — `blocks_cutover` non impedisce apply ma impedisce `ready_for_cutover`

### Dogfooding #1 (eseguito, report in DOGFOODING_1_REPORT.md)

Verdetto: ciclo NON completabile UI-only. 6 gap trovate, 4 HIGH fixate in #61.
Le 2 non-fix dichiarate:
- FRICTION #3: `/run` click via browser automation fallisce (Origin header) — da verificare con click umano
- FRICTION #5: batch-accept scriptato non supportato (flusso one-by-one è di prodotto)

## Prossima sessione: DOGFOODING #2

### Obiettivo

Ripetere il ciclo INTERAMENTE dalla UI con le gap chiuse. Build da main
(che include #59+#60+#61), poi: create session → pipeline → plans →
acceptances → apply → verify → ready_for_cutover AUTOMATICO. STOP.

### Regole

- **Tutto dalla UI** — terminale = finding
- **Letture .193 autorizzate** (inventory + delta); catturare load prima
- **Scritture SOLO su sacrificale .78**; peer standalone verificato prima di DNS apply
- **NIENTE cutover/TTL** — zona produzione intoccabile
- **NIENTE --force** per far passare transizioni; se non scatta = BUG

### Sequenza attesa

1. `cpanel-self-migration ui --dir ./dogfood_giorginisposi` (terminale — atteso)
2. Browser → `/workbench` → "Create session" (form: name=giorginisposi)
3. Browser → session detail → "Run Pipeline" (exec)
4. Browser → "DNS Plan" + "Email Plan" + "Cron Plan" (exec)
5. Browser → dashboard `/` → accept blockers one-by-one (click reale!)
6. Browser → session detail → "DNS Apply" (conferma forte: digitare "giorginisposi")
7. Ripetere per email + cron
8. Browser → "DNS Verify" + "Email Verify" + "Cron Verify"
9. Osservare auto-transition a `ready_for_cutover`
10. STOP — sessione resta ready_for_cutover per il cutover futuro

### Deliverable

- DOGFOODING_2_REPORT.md con confronto vs #1
- Verdetto finale: "UI-only completabile: SÌ/NO"
- Se SÌ → prossimo: cutover reale (data utente)
- Se NO → PR di fix, poi dogfooding #3

### Prerequisiti tecnici

- `dogfood_giorginisposi/` directory con host.yaml (già presente da #1)
- Account giorginisposi@.78 esistente
- `go build ./cmd/cpanel-self-migration/` da main aggiornato

## Workflow (promemoria)

- Solo push a fork (`git push fork`)
- TDD
- go-reviewer multi-giro fino APPROVE PULITO
- Docker LINUX_ALL_GREEN eseguito (non promesso)
- Gate nel body PRIMA di chiedere merge
- `runner.go` off-limits
