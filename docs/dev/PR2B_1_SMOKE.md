# PR 2B-1 — real smoke of the first config writer (giorginisposi @ .78)

Date: 2026-07-03. First real-server run of `inventory email-plan` /
`email apply` / `email verify` against the SACRIFICIAL destination
account `giorginisposi` on .78 (Fase 0.2 perimeter — writes legitimate by
construction, nothing resolves to it). All commands ran from the dev Mac
with the branch binary and `configs/host.yaml`; the source host was NEVER
dialed (all email commands are `sshx.DialDest`-only), so the loaded .193
was untouched. Artifacts archived in
`~/Desktop/pADV/cPanel_self-migration-captures/fase0_2-giorginisposi/smoke2b1/`.

## Test bench

Two REAL inventory pairs from the Fase 0.2 captures:

- `pipeline/` — post-apply, PRE-2B-pre: dest has **no forwarder** and the
  fresh default `giorginisposi` (the state that produced MA-001/MA-006).
- `pipeline2/` — post-2B-pre: dest already carries the real values
  (`info@ → gmail` forwarder, gmail catch-all).

## Results (all as expected, no surprises)

1. **`inventory email-plan` on the pre pair** → `1 create, 1 set, 1 skip
   (routing), 0 manual` — exactly the two Fase 0.2 blockers, nothing
   invented. Plan embeds both inventory sha256; MD carries the
   fresh-default assumption verbatim.
2. **`inventory email-plan` on the fresh pair** → `0 create, 0 set,
   3 skip` — the plan itself converges to zero actionable ops on real
   data.
3. **`email apply` dry-run (no `--yes-apply-writes`)** → offline preview
   with the plan-based disclosure, exit 0, zero connections (run without
   any reachable config).
4. **`email verify` on the pre plan (live .78, read-only)** → `CLEAN — 2
   applied, 1 not_checked (routing)`, stale-plan sha256 gate green with
   `--source/--destination` pointed at the pre inventories: the 2B-pre
   manual writes are recognized as the plan's desired outcomes.
5. **`email apply --yes-apply-writes` on the pre plan, live state
   untouched** → `2 already_present, 1 skipped, 0 applied`, **no backup
   file** (`backup_note`: no write decided), exit 0. The outcome-first
   convergence branch works on the real server: re-running a satisfied
   plan writes nothing and creates no artifacts to roll back.
6. **Reset**: `Email::delete_forwarder` removed the real forwarder (via
   an uncommitted throwaway harness, 5B/5C precedent; the catch-all was
   deliberately NOT reset — resetting a default to the bare username is
   an unverified write shape, per the session brief).
7. **`email apply --yes-apply-writes` again** → `1 applied (forwarder
   re-created + per-op verify-after observed it live), 1 already_present
   (default), 1 skipped`, exit 0. **Pre-write backup written first** with
   the verbatim raw UAPI responses + normalized entries showing the
   pre-write state (zero forwarders), bidirectionally paired: backup
   records the report path, report records backup path + sha256.
8. **`email verify --fail-on-drift`** → `CLEAN — 2 applied`, exit 0.
9. **`email apply --rollback <backup>` dry-run (offline)** → computes
   exactly ONE inverse op: remove the tool's own applied create. The
   `already_present` default is NOT inverted (never-delete /
   invert-own-applied-only, as designed). The live rollback execution was
   deliberately NOT run: the applied forwarder IS the intended end state
   (MA-001), and the rollback write path is E2E-locked by the sshtest
   suite (including the diverged-state refusal and the
   `--accept-report-loss` degradation).

## Post-smoke destination state

Identical to pre-smoke: `info@giorginisposi.it → andreavadacchino@gmail.com`
forwarder live, catch-all `andreavadacchino@gmail.com` — MA-001/MA-006
remain satisfied; the pipeline2 convergence evidence (6→4 manual actions)
still holds.

10. **Bare-username default RESTORE byte-verified** (go-review round 1,
    finding 1 — HIGH): the rollback of a `default_address`/`set` op on a
    fresh account restores the backup value, which IS the bare account
    username (2B-pre finding 4); that exact write shape
    (`set_default_address fwdopt=fwd fwdemail=giorginisposi`) had never
    been exercised against real cPanel. Round-trip run on the bench:
    gmail → username-restore → re-list shows **`giorginisposi` verbatim,
    byte-identical to the fresh-account default** → gmail restored →
    re-list confirms. The account ended in its pre-harness state. Raw
    responses in `smoke2b1/cap-username-restore.txt`; the writer comment
    now states exactly what is and is not verified.

## Not exercised on the real server (documented residuals)

- Live rollback execution (dry-run only — see 9); covered by sshtest E2E.
  Its default-restore write shape IS byte-verified (see 10).
- `refused_precondition` on real data (would require racing a human);
  covered by sshtest E2E with a mutated stub state.
- `set_default_address` with `fwdopt=fail/blackhole` (no :fail: source in
  this bench; parameter derivation unit-locked, `failmsgs` shape from the
  cPanel docs — now listed in the `PR2B_PRE_CAPTURES.md` not-probed set;
  byte-verify before the first account that needs it. The writer's
  verify-after re-list bounds a wrong write; list round-trip, not
  delivery behavior, is what verification means here).

## Artifacts

`smoke2b1/`: `email_apply_plan_pre.{json,md}`, `email_apply_plan_fresh.{json,md}`,
`email_verify_pre.{json,md}`, `email_apply_run1.{json,md}` (convergence run),
`email_backup_smoke.json`, `email_apply_run2.{json,md}` (applied run),
`email_verify_final.{json,md}`.
