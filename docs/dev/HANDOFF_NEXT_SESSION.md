# Prompt di avvio — prossima sessione di sviluppo (dopo PR #50)

Copia-incolla da qui in giù.

---

Stai lavorando sul tool Go **cpanel-self-migration** (migrazione read-only-source
tra due account cPanel via SSH user-level password-auth; il binario gira dal Mac
dell'operatore come bridge SRC→relay→DEST), directory
/Users/andreavadacchino/Desktop/pADV/cPanel_self-migration.

Leggi PRIMA di toccare qualsiasi cosa:
1. docs/dev/DEVELOPMENT_STATE.md — roadmap (ultimo merge: #50), mappa architettura, fatti reali dei server.
2. docs/dev/PR2A_CRON_APPLY_DESIGN.md — design 2A congelato: crontab via SSH (non API), guard atomico, path adaptation.
3. docs/dev/PR2B_EMAIL_APPLY_DESIGN.md — design 2B congelato (pattern freshness-guard/safety ereditato da 2A).
4. docs/dev/CPAPI2_DIAGNOSIS_78.md — cpapi2 rotto su .78, UAPI Cron non caricabile; tutto via SSH.
5. docs/dev/PR2B_3_SMOKE.md + PR2B_3_PRE_CAPTURES.md — posture filtri, fatti byte-verificati.
6. docs/dev/FASE0_2_FIRST_APPLY.md — gotcha del cluster DNS su .78 (⚠️ regole sotto).
7. docs/dev/MASTER_PLAN_COMPLETION.md — piano complessivo e modello cutover per-account (Fase 3).

## Stato al 2026-07-03 (sessione 2A in corso)

Fase 0 CHIUSA, 1A CHIUSA, 2B COMPLETA (#47 + #49 + #50) — il tool ha
tre config writer email (forwarder, autoresponder, filtri) + routing plan,
provati sul server reale:

- **2B-1** (#47): forwarder + default_address — primo config writer.
- **2B-2** (#49): autoresponder collector + writer con rollback LIVE.
- **2B-3** (#50): filtri email (regole in chiaro, option A) + routing plan.
  Filtri: collector arricchito con Rules/Actions/RulesCollected; piano
  create/skip/manual (single-rule create, multi-rule MANUAL per match_type
  non round-trippable); StoreFilter/DeleteFilter writer; apply/verify/
  rollback estesi. Routing: planRouting produce set ops; SetMXCheck writer
  via RunAPI2 pronto ma NON smoke-testabile (cpapi2 rotto su .78).
  Gate: go-reviewer 2 giri APPROVE + Docker LINUX_ALL_GREEN ×2.

Stato infra: cpapi2 rotto su .78 (/usr/local/cpanel/cpanel mancante) +
UAPI Cron non caricabile → **TUTTO via SSH** (crontab -l/crontab -).
Account dest giorginisposi su .78 completo, 0 cron job, 0 filtri email.
DNS in sicurezza (peer NS standalone). Load .193 alto: mai toccarlo senza
motivo. host.yaml VALIDO per entrambi i lati.

## Obiettivo: 2A — cron apply

Design in PR2A_CRON_APPLY_DESIGN.md. Primitiva: SSH `crontab -` (replace
intero crontab). Sequenza:
1. Collector in chiaro: CommandClear + ValueClear + marcatore onestà
2. cron-plan offline: create/skip/manual con path adaptation
3. Writer + apply/verify/rollback via `crontab -`
4. Safety test + smoke su .78 + docs + PR

## Workflow (OBBLIGATORIO, invariato)

- SOLO fork andreavadacchino/cPanel_self-migration; push su fork, MAI su
  origin (tis24dev). PR verso il main del fork, merge `gh pr merge N --merge`.
- TDD rigoroso; go-reviewer multi-giro; Docker LINUX_ALL_GREEN prima del merge.
- runner.go off-limits; scritture DNS vietate fino a 6D; mai removeacct/killdns.
- Su .193 SOLO letture minime e motivate (load alto).

## Dopo la 2A (ordine)

6D (dns apply = lo switch per-account del cutover). Decisioni aperte:
data di partenza campagna; ripristino ruolo sync al primo cutover;
ticket Keliweb per cpapi2/UAPI Cron rotti su .78.
