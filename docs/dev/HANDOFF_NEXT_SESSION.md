# Handoff — post sessione 2026-07-04

## Stato

PR #56 aperta (`feat/dns-v2-replace-edit`), NON merged.
Gate pre-merge: go-reviewer R2, binary smoke su .78, Docker LINUX_ALL_GREEN.

## Cosa contiene #56

1. **DNS apply v2 (replace)**: le op `replace` non sono più skippate.
   Implementate come remove+add atomico in singola `mass_edit_zone` call.
   13 test totali (8 nuovi), go-reviewer R1 findings tutti fixati.

2. **Fleet coverage survey**: 15 aree not_collected censite su
   giorginisposi. Solo spamassassin è attivamente usato (template default).

3. **Doc hygiene**: COMMAND.md con dns apply, cron apply, cron verify,
   inventory cron-plan. DEVELOPMENT_STATE.md aggiornato.

## Residui minori (non bloccanti)

- `fwdopt=fail/blackhole` non byte-verificati
- `is_html=1`, `start/stop` espliciti mai live
- `synczone` non ancora byte-verificato live
- DKIM-aware plan classification (futuro)
- SpamAssassin collector/writer (se survey flotta lo richiede)

## Decisioni utente pendenti

1. **Smoke su .78**: autorizzare perturbazione record per validare replace live
2. **Survey flotta**: scegliere metodo di accesso per estendere il survey
3. **Campagna**: variante sync (C consigliata), data/finestra, ordine account

## Workflow

SOLO fork (`gh pr create --repo andreavadacchino/cPanel_self-migration`),
mai origin. TDD. go-reviewer + Docker. runner.go off-limits.
Peer NS standalone verificato ATTIVAMENTE prima di write DNS.
