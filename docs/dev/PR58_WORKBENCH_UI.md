# PR 58 — Single Account Workbench UI

## What

Browser-based governance dashboard for a single migration session.
Extends the existing `cpanel-self-migration ui` webui.

## Why

After PR #57 (session model + CLI), the operator can manage sessions from
the terminal. But a visual dashboard makes the lifecycle *visible*: which
step are we at, what artifacts exist, what's the next action, and what's
the exact command to run.

The direction is: **first make a single migration impossible to lose
control of, then queue a hundred.**

## Design principle: governance, not execution

The UI **never** executes apply/cutover/rollback. It shows state, suggests
commands (copy-paste), and ingests the reports after manual execution.

This is the invariant from `ui_cmd.go` line 17: *"--apply stays
terminal-only."*

## Architecture

```
┌─────────────────────────────────────────────┐
│  Browser (127.0.0.1:8422)                   │
├─────────────────────────────────────────────┤
│  internal/webui (existing)                  │
│    + workbench routes (new)                 │
│    + workbench templates (new)              │
├─────────────────────────────────────────────┤
│  internal/workbench (PR #57)                │
│    Store → sessions/artifacts/timeline      │
├─────────────────────────────────────────────┤
│  ~/.cpanel-self-migration/migrations/       │
│    <session-id>/session.json + artifacts/   │
└─────────────────────────────────────────────┘
```

Same binary, same server, same security model (loopback, CSRF, no file
serving, anti-rebinding).

## Pages

| Route | Method | What |
|-------|--------|------|
| `/workbench` | GET | Sessions list |
| `/workbench/session/<id>` | GET | Session detail (all phases) |
| `/workbench/session/<id>/artifact/<idx>` | GET | Artifact viewer |
| `/workbench/session/<id>/status` | POST | Transition status |
| `/workbench/session/<id>/attach` | POST | Attach artifact |
| `/workbench/session/<id>/run-pipeline` | POST | Launch READ-ONLY pipeline |

## Apply Center (per-track: email/cron/dns)

For each track, the detail page shows:
- State derived from attached artifacts
- The EXACT terminal command (copy-paste ready)
- "Ingest report" button → attach the produced artifact

Example command suggestion:
```
cpanel-self-migration dns apply \
  --plan ~/.cpanel-self-migration/migrations/mig_20260704_abc123/artifacts/dns_plan_20260704_100000_a1b2c3d4.json \
  --config configs/host.yaml
```

## Security

- html/template everywhere (auto-escape)
- CSRF token on every POST
- Session ID validated via isCleanID before any filesystem access
- Artifact kind whitelist enforced
- No raw file serving
- No template.HTML from user input
- Safety test: no sshx/cpanel imports in UI package

## What's NOT in this PR

- Apply/cutover execution from browser
- Multi-account dashboard
- Campaign mode / batch queue
- SQLite migration
- Auth / multi-user
- New writers or collectors
- Changes to internal/migrate/runner.go

## Dependencies

- PR #57 (merged): internal/workbench package
- Existing: internal/webui (extended, not replaced)

## Pre-work required

The 5 LOW findings from PR #57 review need fixing first:
1. Sentinel errors (ErrSessionNotFound) for HTTP status distinction
2. List() returns warnings instead of printing to stderr
3. readSession asserts sess.ID == folderID
4. CLI display sanitization (control characters)
5. (deferred) Actor field in timeline
