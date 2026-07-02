# Development State — cPanel Self-Migration (handoff)

Snapshot for starting a fresh development session. Last updated after
**PR 7B** (provenance chain — `chain_verified` end-to-end).

**PR numbering note:** the 6x series is the DNS track (6C = `dns verify`,
6D = `dns apply`, both not started); the 7x series is the migration
checklist / final verification track (7A = checklist v0, 7B =
provenance chain, both merged).

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
| 5C | Collector audit: email disk usage, autoresponder hardening | #8 |
| 5D | `--fail-on-blockers`: `inventory policy` exits 3 when blocked | #9 |
| 6A | DNS import/verifier micro-design (v2 post adversarial review) | #11 |
| 6B-pre | real-server DNS capability captures (mass_edit_zone OK on v110) | #12 |
| 6B | `inventory dns-plan`: offline DNS import plan builder | #13 |
| 7A | `inventory checklist`: operator migration checklist v0 | #16 |
| 7A-smoke | real-data smoke on doctorbike.it captures (`PR7A_REAL_SMOKE.md`) | #17 |
| 6B-fix | dns-plan: TXT already matching the ip-map translation → skip (cyclic-map safe, single-pass substitution) | #18 |
| 7B | provenance chain: diff/policy record input hashes, checklist verifies `chain_verified` | #19 |
| 7A-ssl-fix | checklist SSL: expired source cert groups → expected, RFC 6125 wildcard coverage | #21 |
| 7C | apply evidence: phase events (+per-item data), report.json `phases_completed`/`artifacts`, checklist `per_item` | #22 |
| 7D | operator acceptances: stable action keys, acceptances.json, `--acceptances` (gate clearing, fail-safe) | #23 |

## The full pipeline (all read-only / offline)

```
cpanel-self-migration --account-inventory   → inventory_source.json (+ _destination, report.md)
cpanel-self-migration inventory diff         → inventory_diff.json + .md
cpanel-self-migration inventory policy        → policy_report.json + .md
cpanel-self-migration inventory dns-plan      → dns_import_plan.json + .md
cpanel-self-migration inventory checklist     → migration_checklist.json + .md
```

The inventory has 11 sections: account, domains, mailboxes, databases,
forwarders, autoresponders, ftp, ssl, php, dns, cron. Diff compares them
deterministically; policy classifies each difference as
blocker/review/warning/info → overall `ready|review_required|blocked`.
None of the three commands connect to a server except
`--account-inventory` (which reads over SSH). `inventory policy
--fail-on-blockers` exits 3 when `overall_status` is `blocked` (reports
are still fully written first; `review_required` never gates), so the
pipeline can gate CI without JSON parsing.

`inventory checklist` (PR 7A) composes inventories + diff + policy
(+ optional dns-plan and `--apply` report.json) into the operator
migration checklist: per-area statuses, expected differences, manual
actions with IDs, and an overall
`BLOCKED|MANUAL_ACTION_REQUIRED|NOT_READY|READY_WITH_MANUAL_NOTES|READY_TO_CUTOVER`
rollup; `--fail-on-not-ready` exits 3 unless READY_*. Honesty invariants
(pinned by tests): `migrated_by_tool` never true without a successful
apply report; evidence is `per_item` when the report's
`phases_completed` proves both the migrate and the verify phase of the
flow completed (PR 7C), `run_level` otherwise; a dns-plan proves "expected" only via action `skip`;
non-inventoried areas (email routing, default address, filters,
redirects) and root-only areas (quota/package, server config) surface as
explicit sections instead of silently reading ok.

Provenance chain (PR 7B): `inventory diff` records
`source_sha256`/`destination_sha256`, `inventory policy` records
`input_diff_sha256` (raw file bytes); the checklist verifies every link
(diff→inventories, policy→diff, dns-plan→inventories) against the files
it composes. All match → `chain_verified: true`. Missing hashes
(pre-7B artifacts) → warning, no gating. A PROVEN mismatch → explicit
warning and any READY_* verdict capped to NOT_READY (the cap never
improves a worse verdict).

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
  `diff.go`+`diff_write.go` (PR4A), `policy.go`+`policy_write.go` (PR5A),
  `dnsplan.go`+`dnsplan_write.go` (PR6B),
  `checklist.go`+`checklist_types.go`+`checklist_write.go` (PR7A).
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
is intentionally never committed). **The Orbit gateway masks
emails/paths/IPs in command output and the masking corrupts JSON:
base64-encode every capture in transit (`uapi … | base64 -w0`), decode
locally, validate with a JSON parse** (learned in the 7A smoke,
`PR7A_REAL_SMOKE.md`). Diff/policy then run offline with the
real binary. Accounts must be registered in Orbit to be reachable;
`turtlebeachandora.com`/`fidopetstore.it` exist on the server but are NOT
in Orbit — `doctorbike.it` and `italplant.com` are and were used.

## Suggested next steps (not started)

- **PR 7C follow-up (optional)**: per-item Data for the web copy/verify
  and db/mail verify events — needs `applyWebFiles`/`verifyWebFiles`
  signature changes (8 test call sites) and `verify.go` (outside the 7C
  perimeter). The per-item lines already exist in
  `logs/migration_report.log`; the checklist upgrade does NOT depend on
  this.
- **PR 7E — inventory expansion wave 1** (capture-first like 6B-pre):
  email routing, default address, email filters, redirects.
- **Real-smoke refinements** (`PR7A_REAL_SMOKE.md`, findings 1 and 2
  already fixed — #18 and the SSL-expired/wildcard follow-up): (3)
  regenerated-DKIM reviews are silent — deserve a dedicated operator
  action (fits 7E).
- **PR 6C — `dns verify`** (read-only): re-fetch destination zones and
  compare against a plan; exit 3 on drift/mismatch. Reuses
  `internal/sshtest` for end-to-end tests.
- **PR 6D — `dns apply`**: the only writer. High risk — full backup +
  rollback protocol from the project CLAUDE.md, sacrificial-zone smoke
  first, and a live session for Orbit approvals. Contract in
  `PR6A_DNS_IMPORT_DESIGN.md`; write API facts in
  `PR6B_PRE_CAPTURES.md` (mass_edit_zone is line_index-addressed!).
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
