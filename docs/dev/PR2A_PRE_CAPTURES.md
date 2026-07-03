# PR 2A-pre — crontab write primitives, byte-verified on the sacrificial dest

Date: 2026-07-03. All calls executed against the SACRIFICIAL destination
account `giorginisposi` on .78 (writes legitimate by construction). Raw
captures archived in
`~/Desktop/pADV/cPanel_self-migration-captures/fase0_2-giorginisposi/cap2apre/`
(11 steps). The throwaway harness connected via `sshx.DialDest` and
used both raw `RunScript` and `cpanel.FetchCrontab` (the existing
read-only parser).

## Byte-verified facts (the 2A implementation contract)

1. **`crontab -` (pipe stdin) round-trips byte-identically.** A
   multiline crontab installed via `printf '%s' "$CONTENT" | crontab -`
   reads back via `crontab -l` with the exact same bytes (after trimming
   a trailing newline). RC=0 on success.

2. **Append (merge) works**: reading the current crontab, appending a
   line, and piping the result back to `crontab -` correctly adds the
   line. The new line is present in the subsequent `crontab -l`.

3. **Remove (rollback) works**: reading the current crontab, removing a
   line by content match, and piping the result back correctly removes
   only that line. The removed line is absent in the subsequent
   `crontab -l`.

4. **Special characters round-trip**: UTF-8 accented characters
   (`héllo wörld`), shell variables (`$HOME`), percent escapes
   (`+\%Y-\%m-\%d`), single and double quotes all survive the
   `printf → crontab - → crontab -l` round-trip verbatim.

5. **Empty crontab install**: `echo "" | crontab -` succeeds (RC=0)
   and leaves the user with no crontab. `crontab -l` after returns
   empty/no-crontab.

6. **`FetchCrontab` parser accuracy**: for the test crontab containing
   1 comment, 2 env lines, 1 schedule job, 1 @daily macro job, and
   1 comment-formatted disabled line, the parser correctly produces:
   2 jobs (schedule + macro), 2 env vars, 2 comments, 0 disabled jobs.
   The `# disabled: ...` line is classified as a comment because
   `disabled:` is not a valid cron field — this is CORRECT behavior
   (real disabled lines are `# 0 0 * * * command` format).

7. **Baseline on .78**: giorginisposi has NO crontab (empty). This is
   the clean-slate condition for the smoke test.

8. **`crontab -r` is accessible** but was NOT used in the probes (the
   cleanup was done via empty install). `crontab -r` remains FORBIDDEN
   in the tool's safety design.

## cpapi2 / UAPI Cron — NOT available

See `CPAPI2_DIAGNOSIS_78.md` for the full diagnosis. Summary:
- `cpapi2` CLI: broken (`/usr/local/cpanel/cpanel` missing).
- `uapi Cron`: module `Cpanel::API::Cron` fails to load.
- HTTP JSON API (port 2083): works but out of scope for 2A.
- **Consequence**: the cron writer MUST use `crontab -` via SSH.

## Consequences for 2A

- **Write primitive**: `printf '%s' "$CONTENT" | crontab -` with the
  content passed as an environment variable (shell-safe, no injection).
- **Read primitive**: `crontab -l` via the existing `FetchCrontab`.
- **Guard**: compare the full current crontab against the plan-time
  snapshot before every write (whole-crontab atomic guard).
- **Verify**: `crontab -l` after install, compare against expected.
- **Rollback**: restore the backup content via `crontab -`.
- **No crontab -r**: ever. Empty install cleans up.
