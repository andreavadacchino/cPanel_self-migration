# Handoff → Sprint 3 (Comparison)

Contesto per la prossima sessione sulla Migration Platform V2.

## Stato attuale (fine Sprint 2)

- **PR #89** aperta sul fork `andreavadacchino/cPanel_self-migration`, base `main`,
  `MERGEABLE`/`CLEAN`. Branch `feat/platform-v2-sprint-2-cpanel-readonly`,
  commit `6226047` (+ commit docs). Tutti i gate locali verdi, review R1→R2
  chiuse (vedi `SPRINT_2_CPANEL_READONLY.md` §14). **Merge = decisione utente.**
- Sprint 0 (PR #86) e Sprint 1 (PR #87) già mergiati in `fork/main` (`2ba9f9e`).

## Topologia remote (IMPORTANTE — non confondere)

- `origin` = `tis24dev/cPanel_self-migration` → **upstream Go legacy** ("Release v2.x"). NON è la platform.
- `fork` = `andreavadacchino/cPanel_self-migration` → **la platform**. Il "main" di riferimento è **`fork/main`**.
- Attenzione: `git diff main...HEAD` con `main` locale (stale) dà falsi positivi coi file Go. Usare `fork/main`.

## Setup ambiente (worktree isolato)

```bash
git worktree add -b feat/platform-v2-sprint-3-... \
  ~/worktrees/cPanel_self-migration/platform-v2-sprint-3 fork/main
# venv FUORI dal repo (lo scope gate vuole solo file sotto migration-platform/):
python3 -m venv <scratchpad>/venv-s3
<venv>/bin/pip install -e packages/domain -e packages/adapters \
  -e "apps/api[test]" -e "apps/worker[test]"
```
Il branch legacy `feat/operator-landing-prune` (checkout principale, con modifiche
Go non committate) **non va mai toccato/switchato**.

## Cosa esiste già e va RIUSATO in Sprint 3

- **Inventory snapshot** normalizzati per source/destination in `inventory_snapshots`
  (API: `GET /api/migrations/{id}/inventory`). Il Comparison lavora su questi.
- `packages/domain/domain/comparison.py` (modello reference Pydantic, da Sprint 0).
- Pattern job/worker: aggiungere un `JobType.COMPARISON` (già nell'enum) con un
  actor `run_comparison` (stesso confine producer/consumer del preflight).
- `packages/adapters` NON serve per il comparison (è puro delta sui dati già letti).

## Regole non negoziabili (invariate)

1. Solo file sotto `migration-platform/`. Nessun Go legacy toccato.
2. Postgres fonte di verità, Redis solo trasporto. Nessun BackgroundTasks/Celery/RQ.
3. Nessun segreto in DB/snapshot/response/log. `auth_ref` mai in response.
4. Gate reali (mai dichiarare verde senza eseguire): scope, `docker compose config`,
   pytest api+worker, `npm run build`, alembic up/down/up, smoke Docker mock.
5. PR sempre sul **fork**, base `main`. Review adversariale + R2 prima del merge.

## Debiti/rischi da affrontare (in ordine)

1. **Sicurezza (prima di uscire dal localhost)**: autenticazione API su tutte le
   route + validazione `host` (blocco range privati/loopback) + binding
   per-endpoint del nome variabile env. Vedi `SPRINT_2_CPANEL_READONLY.md` §7.4.
2. `worker/db.get_engine()` singleton lazy non sincronizzato (lock o init all'import).
3. `test-connection` sincrono → valutare stato `testing` persistito + async.
4. DNS read: nessuna funzione UAPI account-level read-only verificata (resta assente).

## Idea di scope Sprint 3 (Comparison)

- Job `comparison`: legge gli ultimi snapshot source+destination, calcola il delta
  per categoria (domini/email/db/cron/ssl presenti solo a sorgente, solo a
  destinazione, in entrambi), salva un `comparison_result` (nuova tabella + Alembic
  `0004`), API `GET /api/migrations/{id}/comparison`, UI riepilogo delta.
- **Non** implementare ancora il migration plan esecutivo né azioni write.
