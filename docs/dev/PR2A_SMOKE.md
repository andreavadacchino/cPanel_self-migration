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

## What would a full smoke require

A source account WITH cron jobs (giorginisposi on .193 has cron jobs per
the real Fase 0.2 inventory). Reading .193 would provide the source cron
data; the plan would produce create ops with path adaptation; the apply
would install them on .78 via `crontab -`; verify + rollback would clean
up. This is achievable in a dedicated session with explicit permission to
read .193 once.

## Conclusion

The 2A code is unit-tested, go-reviewed (adversarial), Docker-verified,
and the write primitive is individually byte-proven. The zero-op pipeline
path is validated. The full write-path smoke requires source cron data
from .193.
