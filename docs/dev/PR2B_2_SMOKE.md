# PR 2B-2 — real smoke of the autoresponder writer (giorginisposi @ .78)

Date: 2026-07-03. First real-server run of the autoresponder create path of
`inventory email-plan` / `email apply` / `email verify` — and the FIRST
LIVE ROLLBACK EXECUTION ever performed against real cPanel. All commands
ran from the dev Mac with the branch binary (post go-review round 1 fixes)
and `configs/host.yaml`; every email command is `sshx.DialDest`-only, so
the loaded .193 was never touched. Artifacts archived in
`~/Desktop/pADV/cPanel_self-migration-captures/fase0_2-giorginisposi/smoke2b2/`.

## Test bench

The real `pipeline2/` inventory pair (post-2B-pre state: `info@ → gmail`
forwarder and gmail catch-all live on the dest). Neither side of the real
bench pair carries an autoresponder, so the SOURCE inventory was
bench-augmented (documented here, file `inventory_source_bench.json`) with
ONE realistic entry — `assenza-smoke2b2@giorginisposi.it`, multiline body
with accents/blank line/trailing newline, `body_collected: true` — to
exercise the writer end-to-end. The destination side is 100% real. The
smoke address is neutral (nothing resolves to it; the domain's MX still
points at production .193).

## Results (all as expected)

1. **`inventory email-plan`** → `1 create (autoresponder), 3 skip
   (forwarder, default, routing), 0 manual` — the two 2B-1 sections skip
   on the REAL live values, the autoresponder classifies create with the
   full content payload embedded. Plan sha256 gates green.
2. **`email apply` dry-run** → offline, renders
   `create autoresponder assenza-smoke2b2@… (subject "Assenza — smoke 2B-2 àèì")`,
   exit 0, zero connections.
3. **`email verify` pre-apply (live, read-only)** → `2 unchanged,
   1 pending (the autoresponder), 1 not_checked (routing)` — NOT clean,
   correctly: the pending create gates.
4. **`email apply --yes-apply-writes`** → `1 applied, 3 skipped`, exit 0.
   Pre-write backup written first (`email_backup_smoke2b2.json`: raw
   list + per-address raw gets + normalized entries, bidirectionally
   paired with the report). The write path exercised on real cPanel: batch
   snapshot → guard → **fresh per-address re-check immediately before the
   write** (go-review round 1 HIGH fix) → add → unconditional verify-after.
5. **`email verify --fail-on-drift` post-apply** → **CLEAN**, exit 0; the
   autoresponder op verifies `applied` with the full content equivalence:
   the multiline accented body round-trips byte-identical through
   add → get on real cPanel (2B-2-pre fact 5 confirmed via the writer).
6. **Convergence re-run** → `1 already_present, 3 skipped, 0 applied`,
   **no backup file** (`backup_note`: no write decided), exit 0.
7. **`--rollback` dry-run** → exactly ONE inverse: remove the tool's own
   applied autoresponder create. Fully offline.
8. **LIVE rollback (`--rollback … --yes-apply-writes`)** → `1 applied`,
   exit 0: content-equality pre-check passed (the live autoresponder still
   carried exactly what the tool applied), delete executed, verify-after
   observed it gone. **This closes the 2B-1 residual "live rollback never
   executed on the real server"** for the delete-inverse path.
9. **Final verify** → autoresponder back to `pending` (absent),
   forwarder/default still `unchanged` (the MA-001/MA-006 values are
   intact), zero untracked items.

## Post-smoke destination state

Identical to pre-smoke: `info@ → gmail` forwarder and gmail catch-all
live, NO autoresponder. Nothing to clean up.

## Not exercised on the real server (documented residuals)

- `is_html=1` and explicit `start`/`stop` through the WRITER (the raw
  uapi shapes are byte-verified in 2B-2-pre step 9/10; the writer's
  parameter derivation is unit-locked).
- The mid-run race refusal on real cPanel (structurally impossible to
  stage single-operator; E2E-locked by the stub race hook in
  `TestEmailApplyCmdAutoresponderMidRunRaceIsRefused`).
- 2B-1 carry-overs unchanged: `set_default_address fwdopt=fail/blackhole`
  (byte-verify before the first account that needs it);
  `refused_precondition` on real data.

## Artifacts

`smoke2b2/`: `inventory_source_bench.json`, `email_apply_plan.{json,md}`,
`email_apply_dryrun.txt`, `email_verify_pre.{json,md}`,
`email_backup_smoke2b2.json`, `email_apply_run1.{json,md}` (applied run),
`email_verify_post.{json,md}` (CLEAN), `email_apply_run2.{json,md}`
(convergence), `email_rollback_dryrun.txt`,
`email_rollback_report.{json,md}` (live rollback),
`email_verify_final.{json,md}`.
