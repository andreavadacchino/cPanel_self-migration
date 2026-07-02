# UI phase 2b — accept manual actions from the browser (micro-design)

Reconstructed after the fact: this file was committed empty with PR
#27; the content below documents what #27 (plus its go-reviewer
hardening) actually shipped, from the merged implementation
(`internal/webui/accept.go`, `job.go`, `templates/index.html`).

Requirement (operator): the dashboard lists manual actions with their
stable keys (`AK-…`, PR 7D); accepting one today means hand-editing
`acceptances.json` and re-running `inventory checklist`. Phase 2b does
both from the browser.

## Flow (`POST /accept`, handler `saveAccept`)

1. Form fields `action_key`, `reason`, `operator` — all required
   (422 otherwise). Same CSRF/Host/Origin gates as every POST.
2. **Mutual exclusion with /run**: the whole critical section claims
   the jobManager's single slot (`tryReserve`/`release`). A concurrent
   analysis run → 409, and vice versa — the two writers of
   `migration_checklist.json` can never overlap (go-reviewer TOCTOU
   finding on the first cut of #27).
3. Read `migration_checklist.json`; the key must resolve to a current
   action and the action must be `acceptable` — the engine's "must be
   resolved, not accepted" rule is enforced in the UI too (422).
4. Upsert via `accountinventory.MergeAcceptance` into
   `acceptances.json`, bound to the sha256 of the exact checklist
   bytes just read (7D strict-hash anchor). An existing but unparsable
   `acceptances.json` REFUSES the operation instead of starting fresh —
   never erase the audit trail (go-reviewer HIGH on #27). Write is
   temp+rename atomic.
5. Regenerate the checklist synchronously (offline `checklistStep`,
   2-minute timeout, context descends from the job base context so
   shutdown kills it); redirect to `/` — the accept is visible on the
   next page load, zero JS.

## Invariants

- The UI never edits checklist JSON directly: the CLI subprocess
  remains the single authority (`inventory checklist --acceptances`).
- Acceptance semantics (stable keys, fail-safe self-invalidation on
  changed facts, non-acceptable actions) live entirely in 7D engine
  code; the UI is a thin, validated writer of the acceptance file.
- `--apply` and anything that touches servers stay terminal-only.

## Testing (as merged)

Handler tests: missing fields, unknown key, non-acceptable action,
busy slot 409, corrupt acceptances refusal, atomic write, successful
accept regenerates and redirects; race-mode run of the webui package
covers the reserve/release exclusion.
