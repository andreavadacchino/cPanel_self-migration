# UI phase 2b — accept actions from the browser (micro-design)

Requirement: accept reviewed manual actions from the dashboard, not by
hand-editing acceptances.json. Closes the operator loop from phase 1's
read-only view.

## The loop (fully offline — no SSH)

1. The operator reads the current `migration_checklist.json`. Each
   acceptable, not-yet-accepted manual action shows an **Accept** form
   (reason + operator name, both required — 7D requires `accepted_by`).
2. `POST /accept {action_key, reason, operator}`:
   - reads the CURRENT `migration_checklist.json`, verifies the action
     with that key exists AND is `acceptable` (a blocking cron / MX
     confirm is refused, same rule as 7D);
   - computes the sha256 of that checklist file (the one the operator was
     looking at) — the acceptance's audit anchor;
   - upserts the entry into `acceptances.json` via a new library helper
     (dedup by key: re-accepting updates reason/author/date), stamping
     `checklist_file`/`checklist_sha256`;
   - regenerates ONLY the checklist step as a subprocess
     (`inventory checklist … --acceptances acceptances.json`), reusing
     the exact argv builder the run pipeline uses — the CLI stays the
     authority, and `loadAcceptancesFile`'s strict hash check passes
     because the on-disk checklist is still the one we hashed.
3. The dashboard refreshes; the action shows accepted and the verdict
   may relax (7D rollup).

Why regenerate immediately (not just write the file): the phase-1 view
would otherwise show a stale verdict until the next full run — the
"scomodo" the operator called out.

## Consistency of the sha binding across repeated accepts

Each accept stamps `checklist_sha256` = sha of the checklist CURRENTLY on
disk (before this regeneration). All prior entries are re-stamped to the
same current sha, so on the next accept the strict check still matches.
Action keys are content-derived (stable across regenerations), so an
already-accepted action re-matches and stays accepted — idempotent. If
the base checklist later changes for real (a full re-run), a vanished
action's key no longer matches → 7D's unmatched-key warning, acceptance
ignored (fail-safe, already implemented).

## Concurrency / safety

- `/accept` is a mutating POST → the existing gates apply (loopback Host,
  Origin, CSRF, framing headers, 64 KiB cap).
- Refuse with 409 while a full analysis job is running (they both write
  `migration_checklist.json`); serialize the acceptance write under the
  same config mutex family.
- The regeneration runs synchronously (checklist is fast + offline) with
  a short context descending from the base context.
- No new file-serving surface; `acceptances.json` is written atomically
  (temp + rename, 0600 not required — no secrets — but 0644 via the
  existing artifact writers' convention).

## New library code (accountinventory, unit-tested)

`MergeAcceptance(existing *AcceptanceFile, checklistFile, checklistSHA256
string, acc OperatorAcceptance) AcceptanceFile`: upsert by `ActionKey`,
set mode/format_version/file/sha. Pure; the webui handler marshals the
result and writes it. Keeps the acceptance-file shape owned by the
package that defines it.

## Testing (TDD)

- accountinventory: MergeAcceptance upsert/insert/sha-restamp, order
  stability.
- webui: accept writes a valid acceptances.json + regenerates the
  checklist (fake runner records the checklist argv incl. --acceptances);
  non-acceptable key → 4xx, no write; unknown key → 4xx; missing checklist
  → 4xx; 409 while a job runs; CSRF/Host still enforced; reason/operator
  required.
- Live smoke: real binary, real synthetic checklist, accept via the form,
  verify the action flips to accepted and summary.accepted increments.
