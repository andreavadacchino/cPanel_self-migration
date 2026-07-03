# DNS Apply v2 Replace — Binary Smoke on .78

Date: 2026-07-04. Binary smoke of the replace flow against the
sacrificial account giorginisposi@.78. All operations executed through
the compiled binary (not Orbit SSH).

## Prerequisite: peer NS standalone

The peer NS (136.144.242.119) is standalone for .78's zone. Verified
by PR6D smoke (non-propagation proven: `_6dpre` probe invisible on peer
after both add and remove). Consistent with this smoke: the replace
value ("smoke-after-replace") never appeared on the peer during the
smoke window.

## Smoke sequence

| Step | Command | Exit | Result |
|------|---------|------|--------|
| 1. Add probe | `dns apply --plan add_probe.json --yes-apply-writes` | 0 | TXT `_v2smoke` = "smoke-before-replace" added to .78 |
| 2. Replace | `dns apply --plan replace.json --yes-apply-writes` | 0 | TXT replaced: "smoke-before-replace" → "smoke-after-replace", 1 applied |
| 3. Verify | `dns verify --plan replace.json --fail-on-drift` | 0 | **CLEAN** — 1 applied, 0 pending, 0 drift |
| 4. Rollback | `dns apply --rollback backup.json --yes-apply-writes` | 0 | TXT restored: "smoke-after-replace" → "smoke-before-replace", 1 applied |
| 5. Verify post-rb | `dns verify --plan replace.json` | 0 | NOT CLEAN — 1 pending (correct: original value restored, replace undone) |
| 6. Cleanup | `dns apply --rollback probe_backup.json --yes-apply-writes` | 0 | Probe TXT removed from .78 |

All 6 steps: exit 0, report written before exit.

## Key findings

1. **Replace works end-to-end**: remove+add in single mass_edit_zone
   call, precondition check (plan-time dest value matched), verify-after
   (new value present, old value absent), all through the compiled binary.

2. **Rollback of replace works**: restores pre-apply value from backup,
   guarded by value match (current == written value).

3. **Backup-or-nothing**: backup written before every write step,
   bidirectionally paired with the report.

4. **Serial guard active**: each step uses a fresh serial from re-fetch.

5. **Non-propagation**: dig @peer for the probe during the smoke window
   shows only the .193 value (accidental Orbit add to source zone —
   see note below), never the .78 "smoke-after-replace" value.

## Note: accidental .193 probe

During setup, the Orbit `wordpress_run_remote_command` was incorrectly
assumed to target .78 — it targets .193 (where the WordPress site is
hosted). This added `_v2smoke TXT "smoke-before-replace"` to the .193
zone. The record is innocuous (no effect on services) and left in place
(source is read-only by project rules — the add was the mistake, a
remove would be a second violation). The binary smoke was corrected to
use `dns apply` directly (which connects to .78 per host.yaml).
