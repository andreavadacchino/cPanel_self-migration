# PR 2B — email-config apply (design, v2 post adversarial review)

Date: 2026-07-03. v2: amended after the adversarial round — per-op
precondition guard (was: section-snapshot equality, which broke idempotent
convergence), explicit `manual` class for non-simple forwards (multi-target
comma-joined forwards EXIST in the fleet fixtures), report-driven rollback
spelled out with its loss degradation, prefix-matched system defaults,
unconditional per-op verify-after. The FIRST config writer of the tool. Scope: forwarders,
default (catch-all) address, autoresponders (with bodies), email filters,
email routing. Contract inherited from the DNS writer design
(`PR6A_DNS_IMPORT_DESIGN.md`) and adapted to a domain WITHOUT an atomic
serial-guarded write primitive. Motivation is REAL, not speculative: the
first real apply (Fase 0.2, `FASE0_2_FIRST_APPLY.md`) left exactly two
blocking email actions on the checklist — recreate the `info@ → gmail`
forwarder (MA-001) and check the catch-all (MA-006).

## House contract (unchanged, mandatory)

- Offline plan → dry-run by default → `--yes-apply-writes` → backup before
  the first write (backup-or-nothing) → idempotent apply → verify-after →
  conscious amendment of the safety tests.
- SOURCE is never written, in any phase, for any reason.
- **Never delete destination-only resources.** The only deletes the tool
  ever emits are the rollback inverses of its own creates.
- `manual` is terminal: "flagged but applied anyway" does not exist.
- Exit codes: 0 ok, 1 input/runtime error, 2 flags, 3 gated refusal.

## Command surface

- `inventory email-plan` — OFFLINE plan builder (new file set
  `emailplan*.go` in `internal/accountinventory`, mirroring `dnsplan*.go`).
  Consumes `inventory_source.json` + `inventory_destination.json` (NOT the
  lossy diff — 6B precedent), `--policy policy_report.json` as context
  (cross-referenced, never gating). Outputs `email_apply_plan.json` + `.md`,
  deterministic, embedding the sha256 of both inventories.
- `email apply` — the writer (new `email` namespace beside `dns`; connects
  to the DESTINATION only, `sshx.DialDest`). Flags: `--plan`, `--config`,
  `--yes-apply-writes`, `--rollback <backup-file>`, `--output-json/-md`
  (`email_apply_report.json`). Without `--yes-apply-writes`: fully offline
  print, exit 0, ZERO connections (house posture: the dry-run of a writer
  must be safe to run anywhere). Honesty note printed with it: the dry-run
  renders the PLAN-recorded destination state, not the live one — an op
  shown as `create` may resolve to `already_present` at apply; the live
  preview is `email verify`.
- `email verify` — read-only re-certification against a plan (list the
  touched sections on dest, report applied/missing/unexpected per op,
  `--fail-on-drift` → exit 3). Same stale-plan sha256 gate as `dns verify`.

## Plan ops and actions

One op per item, on canonicalized domain/address keys:

| Section | source has, dest missing | both, values differ | identical | unprovable/conflict |
|---|---|---|---|---|
| forwarders | `create` (single-address only) | n/a (a forwarder pair either exists or not) | `skip` | `manual` |
| default_address | `set` (only if dest still carries a FRESH-ACCOUNT default) | `manual` (dest was customized on purpose) | `skip` | `manual` |

**Only SINGLE-EMAIL-ADDRESS forwards are auto-created.** The collector keeps
the cPanel `forward` string verbatim, and the fleet fixtures prove it is not
always one address (`email_forwarders.json`: `"sales@company.com,
backup@company.com"`). Multi-target comma-joined forwards, pipes
(`| /script`), deliver-to-file paths, `:fail:`, `:blackhole:` and
deliver-to-account forms are classified `manual` with the raw value in the
reason — `add_forwarder fwdopt=fwd fwdemail=` cannot round-trip them.
Address split for the write: `Source` is `local@domain`; the op carries
`email=local`, `domain=domain` on the canonicalized domain key (same
canonicalization the inventory uses); a `Source` that does not split into
exactly one `@` → `manual`.

Fresh-account defaults recognized for `set`: the literal account username
and the `:fail:`/`:blackhole:` system forms matched by PREFIX (`:fail:` /
`:blackhole:` — the human-readable tail is locale-dependent;
`list_default_address` values are kept verbatim since 7E). Anything else on
the dest is somebody's decision → `manual`, terminal. DOCUMENTED ASSUMPTION:
this heuristic is safe because the campaign's destination accounts are
created FRESH by us; `:fail:`/username are also legitimate deliberate
choices, so pointing `email apply` at a pre-existing, human-configured
destination account makes the `set` classification unsafe — the plan MD
carries this warning verbatim.

Destination-only forwarders/addresses: listed as `informational`, never
touched (6A analogue of destination-only rrsets).

2B-1 implements forwarders + default_address ONLY (the two real blockers).
Autoresponders, filters and routing are planned as `manual` ops with
explicit reasons until their PR lands (see slicing) — the plan format
carries them from day one so the checklist picture stays complete.

## Write primitives (all via `RunUAPI`/`RunAPI2` with LITERAL names)

- `Email::add_forwarder` (UAPI) — create forwarder.
- `Email::set_default_address` (UAPI) — set catch-all.
- Rollback-only: `Email::delete_forwarder` (UAPI) for the tool's own adds;
  `set_default_address` back to the backup value.
- 2B-2: `Email::add_auto_responder` (body round-trip via
  `get_auto_responder`). 2B-3: `Email::store_filter` (round-trip via
  `get_filter`; REQUIRES resolving the open redaction decision — rules must
  reach the plan in clear), API2 `Email::setmxcheck` (routing, no UAPI
  equivalent).

Exact parameter names/response shapes are NOT trusted from memory: they are
byte-verified in **2B-pre** (below). The writers go through the generic
`RunUAPI` executor — NOT hand-built `RunScript` snippets — so the
module-wide `TestDNSAPICallsUseLiteralNames` structural guard covers them
for free (and no email-config value is a secret, so argv exposure of the
uapi CLI is a non-issue here).

## Freshness guard (the email analogue of the DNS serial)

Email config has no `serial`/optimistic lock. Compensation is a **PER-OP
precondition check** against a fresh re-list — NOT a whole-section snapshot
equality (v1 had snapshot semantics; the adversarial round showed they
break idempotent convergence: a re-run after a partial apply would abort
precisely because the destination moved TOWARD the plan's goal):

1. The PLAN records, per op, the destination precondition it assumed
   (forwarder absent; default_address currently `<value>`).
2. `email apply` RE-LISTS each touched section on the destination
   immediately before writing it, then evaluates EACH op against the fresh
   state:
   - precondition still holds → write;
   - the op's OUTCOME is already present (the exact planned forwarder pair;
     the exact planned default value) → record `already_present`, skip —
     this is what makes re-running a partially applied plan converge
     without duplicates;
   - anything else (e.g. the source address now carries a DIFFERENT forward
     than planned; the default changed to a third value) → record
     `refused_precondition` for THAT op, fail-closed, and continue with the
     remaining ops. No blanket section abort.
3. **Per-op verify-after is unconditional**: after each write the section is
   re-listed and the op is recorded `applied` ONLY if the outcome is
   observably present — so even if 2B-pre finds that `add_forwarder`
   duplicates on double-add, a write that raced a human in the TOCTOU
   window can never be mis-recorded.

The TOCTOU window between re-list and write is accepted and documented
(single-operator tool, seconds-wide window); its residual risk — a
duplicate created by racing a concurrent identical add — is bounded by the
verify-after and surfaced in the report.

## Backup and rollback

- Before the first write of a run: `email_backup_<account>_<ts>.json` with
  the verbatim fresh re-list of every section the plan touches (raw UAPI
  responses + normalized entries). **No backup file ⇒ no write.** The
  backup records the path of its paired report; the report records the
  path and sha256 of its backup (bidirectional pairing).
- `email apply --rollback <backup-file>` — **the paired REPORT is a
  required input** (located via the backup's recorded pairing; `--report`
  overrides). This diverges from the DNS rollback (derivable from
  backup+plan alone) for a structural reason: an email `create` can resolve
  to `already_present` at apply time, so the set of ops the tool ACTUALLY
  performed is only knowable from the report. Inverse ops are computed ONLY
  for ops recorded `applied` — `delete_forwarder` for own creates (the only
  deletes the tool ever emits; `already_present` ops are NEVER inverted),
  `set_default_address` back to the backup value for own sets — then
  re-verified. Rollback refuses (exit 3) if the current dest state diverges
  from the post-apply state for a touched item (a human changed it since:
  explicit resolution required).
- **Report-loss degradation (documented, fail-safe)**: without the report,
  forwarder rollback is MANUAL (deleting "present now but absent in backup"
  could destroy a forwarder a human added post-apply — never-delete wins);
  `set_default_address` rollback still works from the backup value alone.

## Verify-after and reporting

Per-op verify inside the apply run: re-list after each section's writes,
classify each planned op `applied / already_present / failed / refused`.
`email_apply_report.json` records per-op results + timings + the backup
file path + plan sha256. Any `failed` ⇒ process exit 1 (after the report is
fully written — reports are never sacrificed to the exit code).

Checklist integration comes FOR FREE: after a successful apply, re-running
the pipeline (inventory dest → diff → policy → checklist) makes the
`*-REMOVED`/`*-CHANGED` findings disappear, so MA-001/MA-006-class actions
vanish with real evidence instead of being "cleared". No checklist code
change is required for 2B-1; extending `checklistMigratable`/evidence to
email sections is deliberately deferred until the apply report carries
per-item email evidence worth binding (candidate 2B-2/2B-3 work).

## Safety-test amendments (conscious, reviewed)

Two DISTINCT new tests (the v1 doc conflated them), both on the proven
`WalkDir`+tokenizer pattern; grep confirms no existing file contains the
email write verbs, so both are clean at introduction:

- NEW `internal/accountinventory/emailplan_safety_test.go` — package-glob
  over `emailplan*.go` (mirror of `dnsplan_safety_test.go`): no write verbs
  (`add_forwarder`, `set_default_address`, `delete_forwarder`,
  `add_auto_responder`, `store_filter`, `setmxcheck`), no connecting
  imports — the plan builder is offline by construction.
- NEW `TestNoEmailWritePatternsModuleWide` (in
  `internal/cpanel/email_apply_safety_test.go`) — module-wide scan for the
  same verbs, skipping ONLY the allowlisted writer files:
  `internal/cpanel/email_apply.go` + the `email apply`/`email verify`
  command files. NOTE: this is the FIRST per-file allowlist in a
  module-wide scan (the DNS scan has no allowlist yet — 6D will introduce
  its own the same way).
- `internal/accountinventory/checklist_safety_test.go`: extend `writeCalls`
  with the email write verbs (the checklist stays offline).
- The existing DNS/cron forbidden-verb scans do not name email verbs — no
  amendment there. New `RunUAPI` write calls are automatically covered by
  the structural `TestDNSAPICallsUseLiteralNames` (literal module/function
  names, module-wide).
- `internal/migrate/runner.go` remains off-limits: `email apply` is a
  standalone subcommand, NOT a phase of the migration flow.

## 2B-pre — real round-trip captures (first step, needs the user)

Byte-verify on the SACRIFICIAL dest account (giorginisposi@.78 — writes are
legitimate there, nothing resolves to it):

1. `uapi Email add_forwarder domain=… email=… fwdopt=fwd fwdemail=…` — real
   param names, response shape, duplicate-add behavior (the idempotency
   design depends on whether cPanel dedupes or duplicates).
2. `uapi Email set_default_address …` — param names, accepted forms
   (address vs `:fail:` vs `:blackhole:`), response.
3. `uapi Email delete_forwarder …` — rollback primitive.
4. Round-trip proof: `list_forwarders`/`list_default_address` after each
   write; then CLEAN UP (delete the test forwarder, restore the default
   address) and re-list to prove the account is back to its pre-capture
   state. Bonus: applying the REAL MA-001/MA-006 values manually during the
   capture is allowed (they are the migration's intended end state) — in
   that case the pipeline re-run must show the two actions gone.
5. Archive under `~/Desktop/pADV/cPanel_self-migration-captures/` +
   `PR2B_PRE_CAPTURES.md` (byte-verified, 7E-pre style).

## Slicing

- **2B-pre** captures + doc (needs ~15 min on .78, no TOTP — direct SSH).
- **2B-1** `inventory email-plan` + `email apply`/`email verify` for
  forwarders + default_address (TDD; sshtest end-to-end
  plan→apply→verify→rollback; real smoke on giorginisposi .78).
- **2B-2** autoresponder bodies collector (`get_auto_responder`, closes the
  1A `autoresponder_bodies` not_collected line) + autoresponder create op.
- **2B-3** filter rules collector + `store_filter` round-trip (GATED on the
  user's redaction decision — rules must be stored in clear in the plan) +
  routing via API2 `setmxcheck`.

## Out of scope (explicit)

- Deleting/renaming anything destination-only, ever.
- Mailbox creation/passwords (the migrate flow owns mailboxes).
- MX **DNS records** (that is the dns track); `setmxcheck` only flips the
  local/remote routing flag, and only in 2B-3.
- Any UI work; any `internal/migrate/runner.go` change.
