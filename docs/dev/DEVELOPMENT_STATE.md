# Development State — cPanel Self-Migration (handoff)

Snapshot for starting a fresh development session. Last updated after
**PR 5C** (collector real-server audit).

## What this tool is

A CLI (`cpanel-self-migration`) that migrates email, website files and
MySQL databases between two cPanel accounts over **user-level SSH only**.
The SOURCE host is always read-only; all writes target the DESTINATION;
default mode is dry-run. Module path
`github.com/tis24dev/cPanel_self-migration`, Go 1.25.

**Fork workflow (important):** work only on the fork
`andreavadacchino/cPanel_self-migration`. `origin` = `tis24dev` (no push
access). `fork` = `andreavadacchino` (push here). PRs target the fork's
own `main`; Sourcery reviews each PR; merge with `gh pr merge N --merge`.

## Roadmap so far (all merged to fork main)

| PR | What | Merge |
|----|------|-------|
| 1 | JSON events + report foundation (`--json-events`, `--report-json`) | — |
| 2 | Read-only account inventory skeleton (`--account-inventory`) | — |
| 3A | Email config inventory (forwarders, autoresponders) | — |
| 3B | FTP + SSL + PHP inventory collectors | — |
| 3C | DNS zone inventory (UAPI `DNS::parse_zone` + API2 fallback) | #1, #2 |
| 3D | Cron inventory (SSH `crontab -l`, redacted) | #3 |
| 4A | Offline `inventory diff` subcommand | #4 (contract test), #5 |
| 5A | Policy engine v0 (`inventory policy`) | #6 |
| 5B | Real-server hardening: cron `secure=` leak, FTP/SSL parsing | #7 |
| 5C | Collector audit: email disk usage, autoresponder hardening | (open) |

## The full pipeline (all read-only / offline)

```
cpanel-self-migration --account-inventory   → inventory_source.json (+ _destination, report.md)
cpanel-self-migration inventory diff         → inventory_diff.json + .md
cpanel-self-migration inventory policy        → policy_report.json + .md
```

The inventory has 11 sections: account, domains, mailboxes, databases,
forwarders, autoresponders, ftp, ssl, php, dns, cron. Diff compares them
deterministically; policy classifies each difference as
blocker/review/warning/info → overall `ready|review_required|blocked`.
None of the three commands connect to a server except
`--account-inventory` (which reads over SSH).

## Architecture map

- `cmd/cpanel-self-migration/main.go` — flags + subcommand dispatch
  (`inventory diff|policy` handled before global flag parsing).
- `internal/cpanel/` — cPanel API layer. `Runner` interface is the SSH
  seam. `RunUAPI[T]`/`parseUAPI[T]` (UAPI), `RunAPI2[T]`/`parseAPI2[T]`
  (cpapi2 CLI). Per-feature files: `domains.go`, `email*.go`, `ftp.go`,
  `ssl.go`, `php.go`, `mysql.go`, `dns_zones.go`, `cron.go`, `token.go`,
  `addon.go`. Flexible decoders in `types.go`: `flexInt64` (number OR
  quoted string OR float→trunc), `flexStringList` (string OR array).
- `internal/accountinventory/` — `Collect()` orchestrates all collectors;
  `types.go` (normalized schema), `collector.go`, `write.go` (report),
  `diff.go`+`diff_write.go` (PR4A), `policy.go`+`policy_write.go` (PR5A).
- `internal/migrate/runner.go` — the migration orchestrator. **Off-limits
  to the inventory/diff/policy line of work** (do not modify).
- `internal/sshx/` — real SSH transport; `internal/sshtest/` — in-process
  SSH exec server for end-to-end tests without a real daemon.

## Hard-won real-server facts (cPanel 110.0 build 131, server .193)

These broke synthetic-fixture assumptions and cost real bugs — respect
them when adding collectors:

1. **`DNS::parse_zone` DOES work on v110** (the "requires v136" note was
   wrong). API2 `ZoneEdit::fetchzone_records` fallback still needed for
   other builds. API2 returns numeric fields as **quoted strings**
   (`ttl:"14400"`, `preference:"0"`). DNS TXT (DKIM) is split into
   255-char `data_b64` segments — must be RFC1035-joined.
2. **FTP `diskused`** = quoted string `"57632.08"` on some accounts, bare
   float `13558.40` on others → use `flexInt64`.
3. **SSL `domains`** = an **array** (SAN list), not a string → `flexStringList`.
4. **Email `list_pops_with_disk`** has NO `diskusedquota`; disk is in
   `_diskused` (bytes, quoted string).
5. **Subdomains have no DNS zone of their own** — `parse_zone` on a
   subdomain returns "You do not control a DNS zone". The collector skips
   `Type=="sub"` for DNS (correct).
6. **Cron redaction must cover `secure=`** as well as `token=` — real
   PrestaShop cron jobs authenticate with `secure=<token>`.

**General lesson:** any cPanel numeric field can arrive as a quoted
string or float; default to `flexInt64` for informational numbers and
`flexStringList` for maybe-array strings. Synthetic fixtures repeatedly
hid these — validate new collectors against real captures.

## Testing conventions

- TDD throughout: fixture → RED test → fix → GREEN.
- Real-server-shape fixtures live in `internal/testdata/*_realserver.json`
  with tests in `internal/cpanel/realserver_test.go`.
- Safety tests assert read-only invariants (`dns_safety_test.go`,
  `cron_safety_test.go` — no write verbs; module-wide source scan).
- Determinism: every diff/policy output list is fully sorted.
- Redaction: secrets are masked before storage; hashes are computed over
  the REDACTED text (no brute-force oracle).
- Markdown cells go through `mdCell` (pipe-escape + CR/LF collapse +
  rune-safe truncation).
- **Known-failing on macOS (NOT regressions):** `internal/dbmig`,
  `internal/maildir`, `internal/migrate`, `internal/webfiles` — they run
  bash/sed scripts that need GNU tools / bash≥4. Always diff them against
  `main` to confirm zero changes before blaming your PR.

Verify commands:
```
go test ./internal/cpanel/ ./internal/accountinventory/ ./cmd/...
go test ./...
go vet ./...
go build ./cmd/cpanel-self-migration
```

## Smoke-testing against the real server

Direct SSH from the dev Mac is refused (keys rejected for
`onlinerincipiadv`). To exercise the real `Collect` code on real data:
capture cPanel responses **read-only via Orbit** (`superadmin_start_session`
with a TOTP, then `wordpress_run_remote_command` running `uapi …` /
`cpapi2 …` / `crontab -l`), save one file per API call into a capture
dir, and replay them through `accountinventory.Collect` with a small
throwaway `Runner` test (see git history of PR5B/5C for the harness — it
is intentionally never committed). Diff/policy then run offline with the
real binary. Accounts must be registered in Orbit to be reachable;
`turtlebeachandora.com`/`fidopetstore.it` exist on the server but are NOT
in Orbit — `doctorbike.it` and `italplant.com` are and were used.

## Suggested next steps (not started)

- **PR 6 — DNS import/verifier** (roadmap): the write side of DNS,
  gated behind the policy report. High risk — needs the full backup +
  rollback protocol from the project CLAUDE.md.
- **`--fail-on-blockers`** flag for CI gating on the policy status
  (currently exit is always 0; consumers must parse `overall_status`).
- **Policy rule refinement / configurable rules** — only if real usage
  shows the v0 rule table is too aggressive; the smoke test did not show
  false positives (the 24 blockers were legitimate for two *different*
  accounts).
- **DBUserEntry `shortuser` vs `short_user`** — the type binds
  `json:"shortuser"` but real cPanel uses `short_user`; harmless today
  because the inventory collector never reads `ShortUser` (only
  `ListDatabases`), but fix if `ListDBUsers` ever feeds the inventory.

## Operational context (from project CLAUDE.md)

Real production infra managed by Principi S.r.l. Uptime > security >
functionality > optimization. Server .193 = Keliweb VPS (Intel i7 4-core,
55 cPanel accounts, cPanel v110, CentOS 7 EOL). Every server intervention
must classify risk, back up first (medium/high), define a <60s rollback,
and be documented via Orbit `create_intervention`. The inventory/diff/
policy line of work is **read-only** and low-risk by construction.
