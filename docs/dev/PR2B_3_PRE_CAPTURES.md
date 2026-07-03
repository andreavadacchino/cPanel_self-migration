# PR 2B-3-pre — filter + routing primitives, byte-verified on the sacrificial dest

Date: 2026-07-03. All calls executed against the SACRIFICIAL destination
account `giorginisposi` on .78 (Fase 0.2 perimeter — writes legitimate by
construction, nothing resolves to it), from the dev Mac via the same
`sshx.DialDest` path the email commands use (throwaway harness, 5B/5C
precedent, never committed). Raw captures archived in
`~/Desktop/pADV/cPanel_self-migration-captures/fase0_2-giorginisposi/cap2b3pre/`
(rounds r5 and r6 — earlier rounds discovered the correct parameter names
through systematic error-driven probing).

## Byte-verified facts (the 2B-3 implementation contract)

### Filter primitives

1. **`Email::store_filter` parameter names**: `filtername`, `match_type`
   (is/and/or — the join between rules), `part1` (Exim match term, e.g.
   `$header_From:`, `$header_Subject:`, `$header_To:`), `match1`
   (comparison operator: `contains`, `is`, `begins`, `ends`), `val1`
   (value to compare against). Multi-rule: `part2/match2/val2`, etc.
   Actions: `action1` (type: `save`, `deliver`, `fail`, `finish`, `pipe`),
   `dest1` (destination for save/deliver/pipe, absent for fail/finish).
   Multi-action: `action2/dest2`, etc. Per-mailbox scope: add
   `account=local@domain`. **There is NO `opt1` parameter** — including it
   causes "Invalid filter match join" errors (round 4 discovery).
   **There is NO `action_dest1` or `action_number1`** — `dest1` is the
   correct destination parameter name (round 5 discovery).

2. **`Email::list_filters` response shape**: each filter carries
   `filtername` (string), `enabled` (bare int 1/0), `unescaped` (string
   `"1"`), `rules` (array of `{part, match, opt, val}` — `opt` is always
   `null`), `actions` (array of `{action, dest}` — `dest` is `null` for
   fail/finish). **No `number` field in list** (only in get_filter).
   **No `match_type` field** — the AND/OR join between rules is NOT
   returned by list_filters.

3. **`Email::get_filter filtername=`** returns a SINGLE filter with
   `filtername`, `rules` (array of `{part, match, opt, val, number}`),
   `actions` (array of `{action, dest, number}`). `number` is a bare int
   (1, 2, ...). `opt` is always `null`. **No `match_type`** — the
   AND/OR join is NOT returned by get_filter either.

4. **`get_filter` on a NON-EXISTENT filter returns status:1 with a
   TEMPLATE response**: `{"filtername":"Rule 1","rules":[{"number":1}],
   "actions":[{"number":1}]}`. NOT an error. Consumers must gate
   existence on `list_filters` first (same pattern as
   `get_auto_responder` on an absent address — 2B-2-pre fact 4).

5. **`store_filter` on an existing name UPSERTS**: count stays at 1,
   content is fully replaced (rules, actions, everything). ⚠️ Writing
   onto a filter that already exists DESTROYS the existing rules and
   actions: the apply guard must fail-closed unless the destination
   filter name does not exist or its content is identical (same
   never-overwrite posture as autoresponders — 2B-2-pre fact 7).

6. **Round-trip `get→reconstruct params→store→get` is byte-identical**
   for the rule/action fields: `part`, `match`, `val` round-trip
   verbatim; `action`, `dest` round-trip verbatim. Verified with both
   single-rule and multi-action filters.

7. **`dest` path normalization**: cPanel normalizes save destinations.
   A `store` call with `/home/user/mail/domain/folder` stores as
   `$home/mail/domain/folder` in `list_filters` and as
   `/domain/folder` in `get_filter`. The round-trip must compare
   against the VALUE RETURNED BY GET, not the value passed to store.

8. **`Email::delete_filter filtername=` [account=]** works (status:1).
   Non-existent filter → status:0 + error `Filter "name" not found.`
   This is the rollback primitive for the tool's own applied creates.

9. **Per-mailbox scope is isolated**: a per-mailbox filter
   (`account=local@domain`) does NOT appear in the account-level list
   (and vice versa). Both `store_filter` and `delete_filter` accept the
   `account` parameter for per-mailbox operations.

10. **`match_type` (AND/OR join) is NOT round-trippable**: neither
    `list_filters` nor `get_filter` returns the `match_type` that was
    used to create the filter. Consequence: a filter with 2+ rules
    cannot be faithfully re-created because the tool cannot determine
    whether the source's rules were joined with AND or OR. **Single-rule
    filters are safe** (match_type is irrelevant with 1 rule).
    **Multi-rule filters must be classified MANUAL** — the writer
    defaults `match_type=is` for single-rule creates (safe default).

### Routing primitives

11. **API2 `Email::setmxcheck` is BROKEN on .78**: `cpapi2` is a
    symlink to `apitool` which depends on `/usr/local/cpanel/cpanel`
    (missing on this server). Error: `Failed to execute
    /usr/local/cpanel/cpanel: No such file or directory`. No UAPI
    equivalent exists (`uapi Email setmxcheck` → "function not found";
    `uapi Email set_mail_routing` → "function not found").

12. **`Email::list_mxs` (UAPI, read-only) works fine**: routing is
    readable but NOT writable on .78. Baseline:
    `giorginisposi.it mxcheck=local detected=local`.

## Consequences for 2B-3

### Filter implementation (proceeds)

- **Parameters for `store_filter`**: `filtername`, `match_type`,
  `action{N}`, `dest{N}`, `part{N}`, `match{N}`, `val{N}`, optional
  `account` for per-mailbox scope. NO `opt{N}`, NO `action_dest{N}`,
  NO `action_number{N}`.
- **Collector**: extend `EmailFilterEntry` with `Rules`/`Actions` raw
  JSON (the existing `[]json.RawMessage` from `cpanel.EmailFilterEntry`
  already carries them). Add `RulesCollected bool` honesty marker.
  `match_type` is NOT collectable — document this.
- **Plan**: single-rule filters → create/skip/manual based on rule+action
  equality. Multi-rule filters → MANUAL (match_type not round-trippable,
  fact 10). Content comparison: `part`, `match`, `val` for rules;
  `action`, `dest` for actions. `opt` is always null (ignored).
- **Writer**: `store_filter` via `RunUAPI` with literal names; `dest`
  parameter only when non-null. Re-check pre-write (upsert is
  destructive, fact 5). Verify-after via `get_filter`.
- **Rollback**: `delete_filter` guarded by content equivalence (fact 8).
- **`delete_filter`** added to all three forbidden-verb scans.

### Routing implementation (BLOCKED on .78)

- **The `setmxcheck` writer CANNOT be smoke-tested on .78** (fact 11).
  The routing ops remain MANUAL in the plan with an updated reason
  referencing this finding. The writer code CAN be implemented and
  unit-tested, but the live smoke must wait for a server where cpapi2
  works (or a cPanel update on .78 that restores the binary).
- **Alternative**: routing is already compared at plan time (skip if
  identical, manual if different). On the real campaign, the source and
  dest are both `local` for giorginisposi.it (fact 12), so no write is
  needed.

## Not probed (out of 2B-3 scope, unchanged from 2B-2-pre)

- Multi-target forward ADD behavior and `set_default_address`
  `fwdopt=fail`/`fwdopt=blackhole` (2B-1 residuals).
- Filter behavior with non-existent per-mailbox account (the plan
  already fails those into manual before any write).
- `match_type` values beyond `is`, `and`, `or` (no documentation found;
  the tool uses `is` as the safe default for single-rule creates).
