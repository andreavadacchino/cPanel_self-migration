# Frontend Flight Director Roadmap

Status: proposal / product direction
Date: 2026-07-06
Scope: single-account migration UI, not Campaign Mode

## 1. Purpose

This document consolidates the product and UX direction for the next frontend phase of `cPanel_self-migration`.

The tool is no longer just a technical CLI. It now has a strong migration core, account inventory, diff, policy, checklist, apply/verify writers, workbench sessions, artifact registry, and a local web UI.

The next problem is not adding another writer.

The next problem is making the UI safe, understandable, resilient, and usable by agencies/freelancers that need to migrate a cPanel account without root access.

The UI must answer five operator questions at all times:

1. Where am I in the migration?
2. What is happening now?
3. What is missing or risky?
4. What do I need to do next?
5. Can I cut over safely?

## 2. Product thesis

The product should not expose the internal implementation model as the primary user experience.

Internal concepts that must remain available for audit:

- artifacts
- policy findings
- checklist JSON
- acceptances
- apply reports
- verify reports
- events.jsonl
- status transitions

But the primary UI should be migration-first:

- New migration
- Source server
- Destination server
- Source/destination cPanel account
- What to migrate
- Preflight
- Start migration
- Live progress
- Comparative checklist
- Manual tasks
- Final verify
- Cutover gateway
- Archive/report

Principle:

> Simple for the operator, auditable for the tool.

## 3. Current state

Recent frontend/workbench milestones:

- PR #57 introduced `internal/workbench`, migration sessions, artifact registry, and the `migration` CLI namespace.
- PR #58 added the single-account workbench UI.
- PR #59 allowed the UI to launch migration steps as subprocesses with guardrails: strong confirmation, CSRF, loopback-only, and timeline recording.
- PR #61 closed dogfooding gaps for a UI-driven cycle.
- PR #62 localized the UI in Italian, fixed pipeline ordering so `dns-plan` is generated before checklist, and added DNS apply warning.
- PR #63 fixed cPanel UAPI value encoding where `+`/`%` could corrupt TXT/DKIM/SPF records.
- PR #64 fixed DNS apex handling for `mass_edit_zone` by using FQDN instead of `@`.
- PR #65 documented a UI-only dogfooding walk to `ready_for_cutover`.
- PR #66 redesigned the workbench detail page into seven guided screens.
- PR #67 translated manual action presentation to Italian without mutating checklist JSON or acceptance keys.
- PR #68 added a shared design system and modern landing/workbench presentation.

The UI is now stronger than a simple dashboard, but it is still too close to the engineering model. Operators still risk losing context across long-running jobs, manual actions, verify phases, and cutover decisions.

## 4. Why the current UI still needs work

The current UI exposes too much of the internal model:

- session
- artifact
- policy
- checklist
- acceptance
- status governance
- transition
- verify report
- apply report
- pipeline

These are valid backend/audit concepts, but they should not be the operator's main mental model.

An agency/freelancer operator expects a guided migration flow, similar in simplicity to migration plugins or WHM Transfer Tool-style workflows, while still respecting the no-root/account-level limitations of this tool.

The risk is not only confusion. The real risk is false confidence.

A green UI must mean that the migration is safe according to fresh evidence, not merely that an operator clicked confirmations.

## 5. UX direction: Flight Director pattern

Adopt a Flight Director layout instead of a long single-page document or loosely connected screens.

### 5.1 Persistent global header

Always visible:

- Migration name
- Source server/account
- Destination server/account
- Main domain
- Current phase
- Global status
- Risk badge
- Next recommended action

Example statuses:

- Setup required
- Preflight required
- Ready to migrate content
- Copy in progress
- Waiting for operator input
- Verification required
- Ready for cutover
- Cutover completed
- Failed / needs attention

### 5.2 Left timeline / stepper

Migration phases:

1. Setup
2. Preflight
3. Content migration
4. Email configuration
5. Cron
6. DNS
7. Final verify
8. Cutover gateway
9. Archive

The current phase should be highlighted automatically.

The operator may revisit previous phases to inspect historical logs, snapshots, or reports.

### 5.3 Main stage

The main area shows only the contextually relevant task for the current phase.

Examples:

- During preflight: show source/destination connectivity, account discovery, UAPI/SSH status, risks.
- During copy: show progress, current item, elapsed time, live logs.
- During comparative check: show source vs destination status by area.
- During manual task phase: show actionable tasks with source values and copy buttons.
- During cutover: show final decision, blockers, quarantine guidance, and report export.

### 5.4 Technical details drawer

Artifacts and raw reports remain available, but behind an advanced section:

- inventory_source.json
- inventory_destination.json
- inventory_diff.json
- policy_report.json
- migration_checklist.json
- dns_import_plan.json
- apply/verify reports
- events.jsonl

The primary UI should not require opening them.

## 6. Rehydration-first rule

Live UI is not enough.

Long migrations may outlive the browser session. The laptop may sleep. The browser may be refreshed. The connection to SSE may drop.

Therefore the UI must be able to rehydrate state from persistent sources before reconnecting to any live stream.

Canonical rehydration sources:

- session.json
- timeline entries
- events.jsonl
- report.json
- *_apply_report.json
- *_verify_report.json
- artifact registry
- migration_checklist.json

Expected flow:

1. User opens or refreshes the page.
2. UI reconstructs the current state from persisted session/artifacts/timeline.
3. If a job is active, UI attaches to the live stream.
4. If live stream fails, UI remains usable with last known state.
5. Refreshing the page never loses the migration context.

SSE should be used as a live transport, not as the source of truth.

## 7. Live progress and logs

Use Server-Sent Events for server-to-browser updates.

Suggested endpoint:

```text
GET /workbench/session/<id>/events
```

Progress must avoid fake precision.

Prefer:

- current phase
- current item
- completed items / total items
- elapsed time
- warnings/errors
- live log tail

Avoid claiming exact percentages unless the denominator is reliable.

Example UI copy:

```text
Migration in progress — mailbox 4 of 12

✓ Connected to source
✓ Connected to destination
✓ Preflight completed
→ Copying info@example.com — 128 MB / 430 MB
□ Copy web files
□ Import databases
□ Final verification
```

## 8. Manual actions as verifiable tasks

Do not expose acceptance as a primary UX concept.

Turn manual actions into operator tasks.

Suggested task states:

- To do
- Done by operator, not automatically verifiable
- Automatically verified
- Ignored with reason
- Not applicable
- Blocking

Where possible, manual tasks should have a `Verify now` action.

Example DNS task:

```text
DNS TXT record missing

Source:
Type: TXT
Name: @
Value: v=spf1 include:spf.protection.outlook.com -all

Destination:
Missing

Recommended action:
Create this TXT record on destination.

[Copy value] [Verify now] [Mark as done manually] [Not applicable]
```

Example cron task:

```text
Cron job requires path adaptation

Source command:
*/5 * * * * /usr/local/bin/php /home/olduser/public_html/cron.php

Suggested destination command:
*/5 * * * * /usr/local/bin/php /home/newuser/public_html/cron.php

[Copy adapted command] [Mark as created] [Ignore with reason]
```

Audit acceptances should still be saved behind the scenes using existing acceptance keys/hashes.

## 9. DNS and routing policy

DNS must never be silently coupled to content migration.

Recommended rule:

- Content migration may include files, DB, mail/Maildir.
- Email config and cron may be separate controlled phases.
- DNS must always be a separate phase with explicit warning, backup, verify-after, and operator confirmation.
- Cutover must be separate from DNS apply.

DNS can remain automatable where the existing DNS plan/apply/verify contract proves it safe, but the UI must treat it as a danger zone.

Manual DNS should remain available as a safer option:

- generate DNS copy map
- show source values
- show destination current values
- allow copy-to-clipboard
- verify destination after manual change

## 10. Final sync and delta risk

The UI must account for data changes during long migrations.

Potential deltas:

- new email arriving on source
- uploaded files during copy
- DB writes during migration
- WooCommerce orders or CMS content changes

Recommended UX:

- Add a `Final sync` phase before cutover.
- Email: allow delta sync where supported.
- Files: allow delta sync where supported.
- DB: warn strongly for dynamic sites; recommend maintenance/freeze window.
- DNS: not a sync phase, but a cutover/switch concern.

The UI must not imply that a migration is safe if the source may have changed after the last snapshot.

## 11. Idempotency and restart semantics

If a migration stops at 60%, the UI must not offer an ambiguous `Start migration` action.

It must explain what actions are available:

- Resume
- Retry failed items
- Re-run selected phase
- Re-run full phase
- Final sync
- Rollback supported changes
- Archive failed attempt

Each action must explain what it will overwrite, skip, verify, or preserve.

This is essential for operator trust.

## 12. Credentials and setup

The UI should simplify credentials without weakening security.

Open decision:

- Are credentials temporary per migration?
- Are server profiles saved?
- Are secrets persisted or only kept in memory?
- Is `host.yaml` generated by the UI?
- Can the UI use cPanel tokens instead of passwords?

Recommended initial rule:

- Avoid persistent passwords by default.
- Prefer token/password in memory for current job when possible.
- If writing `host.yaml`, create it with mode `0600` and document its lifecycle.
- Never copy credentials into artifacts or reports.
- Redact secrets in logs and UI.
- Saved profiles should initially store non-secret connection metadata only.

## 13. Green criteria

A migration is not green because copying finished.

A migration is green only when operational evidence and decision evidence agree.

### 13.1 Operational green

Required:

- selected content phases completed successfully
- no unhandled cPanel/UAPI errors
- no partial apply without explanation
- file/mail/db verification clean or explicitly degraded with reason
- DNS/email/cron verify reports clean where those phases were selected
- reports/artifacts generated and attached

### 13.2 Decision green

Required:

- comparative checklist has zero unresolved blockers
- all blocking manual tasks are automatically verified or explicitly confirmed with reason
- final verify is fresh
- source snapshot is not stale beyond a configured threshold
- DNS/cutover risks acknowledged

### 13.3 Cutover green

`Can I cut over?` may be green only when:

- operational green is true
- decision green is true
- final sync/verify has completed
- DNS/routing decisions are explicit

### 13.4 Shutdown green

`Can I shut down the old server?` should not become green immediately after cutover.

Default recommendation:

- show `observe/quarantine` state
- require post-cutover checks
- recommend waiting a configured window, e.g. 48-72 hours, unless operator overrides with reason

## 14. Roadmap

### PR 69 — Setup Flow + Rehydration Foundation

Goal: ensure the UI never loses migration context.

Scope:

- wizard for new migration
- source/destination/account setup
- safe credential handling decision for initial implementation
- rehydration endpoint/view-model from session + artifacts + timeline
- current job state model
- clearer empty/error states
- no redesign beyond what is needed for setup/rehydration

Out of scope:

- campaign mode
- queue
- new writers
- new collectors
- full Flight Director design
- cutover automation

### PR 70 — Live Job Engine: SSE + Progress/Log History

Goal: make long-running jobs observable and reconnectable.

Scope:

- SSE endpoint
- live log stream
- historical log tail
- progress by phase/item
- reconnect after refresh
- interrupted/failed/completed states

### PR 71 — Flight Director UI

Goal: replace engineering-oriented workbench detail with contextual migration control.

Scope:

- persistent header
- left timeline
- contextual main stage
- next recommended action
- risk badge
- visible separation between content migration, config phases, DNS, verify, cutover

### PR 72 — Comparative Checklist UI

Goal: show source vs destination in operator language.

Scope:

- source/destination comparison by area
- migrated/missing/different/manual states
- semaphores
- drill-down details
- technical artifact links behind advanced drawer

### PR 73 — Manual Actions as Verifiable Tasks

Goal: turn manual actions into practical operator work.

Scope:

- source values visible
- destination current values visible
- copy-to-clipboard controls
- recommended action text
- verify-now where possible
- manual confirmation fallback
- acceptance saved behind the scenes

### PR 74 — Final Sync + Cutover Gateway

Goal: prevent false-ready cutover.

Scope:

- final sync phase
- DB dynamic-site warning
- fresh final verify
- cutover decision screen
- old-server quarantine/observe state

### PR 75 — Final Report / Archive

Goal: produce a clean closeout artifact.

Scope:

- final HTML/PDF-style report
- migration summary
- migrated areas
- manual tasks confirmed
- unresolved notes
- post-cutover recommendations
- archived session view

## 15. Explicit non-goals

Do not implement in this frontend phase:

- Campaign Mode
- multi-account queue
- parallel migrations
- root/WHM operations
- new migration writers
- new inventory collectors
- blind `migrate everything` button
- automatic cutover
- automatic old-server shutdown decision
- hidden DNS writes

## 16. Open decisions

Before implementation, decide:

1. Does `Start migration` include only file/db/mail, or also email config and cron?
2. Should DNS be automatable from the UI, or should the first product UX prefer manual DNS copy map + verify?
3. Are credentials temporary only, or can profiles persist secrets?
4. What is the stale threshold for source snapshots before cutover?
5. What does resume mean after an interrupted migration?
6. What is the recommended observation/quarantine period before declaring the old server dismissible?

## 17. Recommended next step

Start with PR 69: Setup Flow + Rehydration Foundation.

Do not start with another visual redesign.

The next frontend increment must prove that the UI can survive refreshes, long-running jobs, interrupted operations, and operator re-entry without losing the migration state.

If this foundation is weak, every later UI improvement will be cosmetic and fragile.

Core principle for PR 69:

> First make the UI impossible to lose control of. Then make it beautiful.
