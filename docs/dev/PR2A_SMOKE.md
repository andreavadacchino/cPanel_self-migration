# PR 2A — cron apply smoke report

Date: 2026-07-03.

## Smoke posture

The sacrificial account `giorginisposi` on .78 has **no crontab** (empty —
2A-pre fact 7). The source account on .193 was NOT read during this
session (load constraint — the 2B sessions never touched .193).

Consequence: the cron plan for giorginisposi produces **zero ops** (no
source cron jobs → nothing to create/skip). The smoke therefore validates
the pipeline's ZERO-OP path (plan generation with empty cron sections),
not the write path.

## Write primitive evidence

The write primitive (`printf '%s' "$CONTENT" | crontab -`) was
byte-verified in the 2A-pre capture round (11 steps on .78):

| Test | Result |
|------|--------|
| Install → readback byte-identical | ✅ |
| Append line (merge pattern) | ✅ |
| Remove line (rollback pattern) | ✅ |
| UTF-8, quotes, $, % round-trip | ✅ |
| Empty install (cleanup) | ✅ |
| FetchCrontab parser accuracy | ✅ |

Full details in `PR2A_PRE_CAPTURES.md`.

## Cron write-path smoke — NOT achievable with this account

**Update 2026-07-03 (6D session)**: a fresh `crontab -l` read of
giorginisposi@.193 (authorized single read) returned **0 cron jobs**.
This confirms FASE0_2_FIRST_APPLY.md line 19: "crontab EMPTY (the
'empty command' line Orbit showed was a parsing artifact)". The earlier
statement in this doc ("giorginisposi on .193 has cron jobs per the real
Fase 0.2 inventory") was WRONG — corrected here.

The cron write-path smoke (plan with path adaptation → apply → verify →
rollback LIVE) requires a source account WITH cron jobs. Neither
giorginisposi@.193 nor any other account in this campaign has cron
data to migrate. The writer is unit-tested, go-reviewed, and the
individual primitives are byte-verified (PR2A_PRE_CAPTURES.md). The
end-to-end write-path remains untested — declared as an honest residual.

## Update 2026-07-03 (smoke-total session): cron primitive LIVE-PROVEN ✅

Smoke: `InstallCrontab` installed `0 3 * * * /bin/true # smoke-total-cron`
on .78 (empty crontab baseline). Verified present via `ReadCrontabRaw`.
Rollback: reinstalled without the line. Verified removed. The cron
write primitive is **LIVE-PROVEN** via throwaway harness.

The end-to-end tool pipeline (plan → apply → verify → rollback via
the CLI binary) is NOT tested because the `cron apply` command file
does not exist yet. The primitive behavior is proven.

## Conclusion

The 2A code is unit-tested, go-reviewed (adversarial), Docker-verified,
write primitive LIVE-PROVEN (smoke-total session). The `cron apply` CLI
command file is remaining work.
