# Handoff — post sessione 2026-07-04

## Stato

PR #56 MERGED (`feat/dns-v2-replace-edit`).
Go-reviewer R1 → all fixed → R2 APPROVE.
Binary smoke su .78: 6/6 steps PASSED (replace→CLEAN→rollback→pending→cleanup).

## Cosa contiene #56

1. **DNS apply v2 (replace)**: le op `replace` non sono più skippate.
   Implementate come remove+add atomico in singola `mass_edit_zone` call.
   13 test totali (8 nuovi). Precondizioni: already_present, drift,
   growth drift, missing rrset, empty DestinationValues → refused.
   Verify-after: nuovi presenti E vecchi assenti. Rollback: ripristina
   dal backup, guard contro backup corrotto.

2. **Fleet coverage survey**: 15 aree not_collected censite su
   giorginisposi. Solo spamassassin è attivamente usato (template default).

3. **Doc hygiene**: COMMAND.md, DEVELOPMENT_STATE.md aggiornati.

## Residui minori (non bloccanti)

- `fwdopt=fail/blackhole` non byte-verificati
- `is_html=1`, `start/stop` espliciti mai live
- `synczone` non ancora byte-verificato live
- DKIM-aware plan classification (futuro)
- SpamAssassin collector/writer (se survey flotta lo richiede)
- Probe `_v2smoke TXT` accidentalmente aggiunto a .193 via Orbit (innocuo)

## Decisioni utente pendenti

1. **Survey flotta**: scegliere metodo di accesso per estendere a tutti
   gli account della campagna (read-only via Orbit)
2. **Campagna**: variante sync (C consigliata), data/finestra, ordine account

## Workflow

SOLO fork (`gh pr create --repo andreavadacchino/cPanel_self-migration`),
mai origin. TDD. go-reviewer + Docker. runner.go off-limits.
Peer NS standalone verificato ATTIVAMENTE prima di write DNS.
