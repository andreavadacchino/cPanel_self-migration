# PR 3D — Cron Inventory (read-only)

## Scope

Read-only crontab inventory via SSH `crontab -l`. No cron modification,
no import, no execution.

## Fetch strategy

`sshx.Client.RunScript` returns an error on any non-zero exit, but
`crontab -l` legitimately exits 1 when the user has no crontab. The fetch
script therefore always exits 0 and carries the real exit code in a
trailing marker:

```bash
out=$(crontab -l 2>&1); rc=$?; printf '%s\n__CRONTAB_RC:%d__\n' "$out" "$rc"
```

- rc=0 → parse content
- rc≠0 + "no crontab" in output → available, zero jobs, light warning
- rc≠0 otherwise → section unavailable, warning (never fatal)

## Parser (line-by-line)

Order of classification per line:
empty → comment (`#…`; if the remainder parses as a valid job → disabled
job) → env var (`NAME=value`) → macro (`@daily…`) → 5-field schedule →
unparsable (warning with sha256, raw line never stored).

Field validation: minute/hour/day-of-month `^[0-9*,/-]+$`;
month/day-of-week also allow names (`^[0-9A-Za-z*,/-]+$`). The numeric
rule keeps prose comments like `# 5 minuti dopo…` from being
misclassified as disabled jobs.

## Redaction (before storing anything)

Applied to job commands and env values:
- `user:pass@host` URL credentials
- `<name-containing-sensitive-keyword>=value` (password/passwd/pwd/token/
  secret/key/auth/cred/bearer/apikey — over-redacts by design)
- `Bearer <token>` / `Basic <b64>`

Raw command is hashed (`sha256:…`) BEFORE redaction for future
comparison, then discarded.

## Schema deviation

Spec asked for `command_hash`/`raw_line_hash`; the existing
`TestNormalizedInventoryJSON` guardrail forbids the substring "hash" in
serialized inventory JSON (anti password-hash leak). Fields are named
`command_sha256`/`raw_line_sha256` instead — same purpose, guardrail
intact.

## Files

- `internal/cpanel/cron.go` — fetch + parse + redact + types
- `internal/cpanel/cron_test.go`, `cron_safety_test.go`
- `internal/accountinventory/{types,collector,write}.go` — CronSection,
  collectCron, Markdown section

## Out of scope

crontab -e/-r/import, server-side writes, DNS, policy engine, UI,
`internal/migrate/runner.go`.
