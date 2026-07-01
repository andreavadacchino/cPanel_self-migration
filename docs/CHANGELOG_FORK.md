# Changelog — Fork andreavadacchino/cPanel_self-migration

Work log for the Account Inventory + Config Migration extension of
`tis24dev/cPanel_self-migration`. All changes are on the fork
`andreavadacchino/cPanel_self-migration`, branch `main`.

Upstream baseline: `v2.2.1` (`f3c92ad`).

---

## Summary

| Metric | Value |
|--------|-------|
| Commits on top of v2.2.1 | 8 |
| Files changed | 35 |
| Lines added | +2,993 |
| Lines removed | -16 |
| New packages | 2 (`events`, `accountinventory`) |
| New UAPI wrappers | 7 |
| New test functions | 51 |
| Packages passing | 14/18 (4 macOS-only failures, identical to upstream) |

---

## PR 1 — JSON Events + Report Foundation

**Commits**: `c21be13`, `04ed358`

**New CLI flags**:
- `--run-id <id>` — optional run identifier (auto-generated if omitted)
- `--output-dir <dir>` — override artifact directory (default: CWD)
- `--json-events` — write JSONL events to `<output-dir>/events.jsonl`
- `--report-json` — write JSON summary to `<output-dir>/report.json`

**New package**: `internal/events/`

| File | Purpose |
|------|---------|
| `event.go` | Event model, Phase/Level/EventType constants, Emitter, RunID |
| `writer.go` | JSONL append-only writer (thread-safe, redacts Data secrets) |
| `redact.go` | Secret redaction (9 sensitive key patterns) |
| `report.go` | RunReport struct + WriteReport to JSON |
| `*_test.go` | 20 test functions |

**Modified files**:
- `cmd/cpanel-self-migration/main.go` — 4 new flags, EventWriter wiring, `buildRunReport()`
- `internal/migrate/runner.go` — `RunID` and `Events` fields in Options, `emitEvent` helper, emit calls at 12 pipeline phase boundaries
- `docs/COMMAND.md` — flag table and artifacts table updated
- `docs/JSON_EVENTS.md` — new user documentation

**Behavior**:
- Without new flags, behavior is identical to v2.2.1
- Events emitted from the real migration pipeline (not reconstructed from logs)
- No secrets in JSONL or JSON output
- `runner.go` changes are additive only (zero-value Emitter = no-op)

---

## PR 2 — Account Inventory Skeleton

**Commits**: `3482c4b`, `e64c535`, `15eb8c6`, `17772ee`

**New CLI flag**: `--account-inventory`

Collects a read-only inventory of a cPanel account and exits. Does NOT go
through `migrate.Run()` — has its own execution path in `main.go`.

**Mutually exclusive with**: `--apply`, `--apply-mirror`, `--mail`, `--file`,
`--db`, `--domain`, `--mailbox`.

**New package**: `internal/accountinventory/`

| File | Purpose |
|------|---------|
| `types.go` | NormalizedInventory, DomainEntry, MailboxEntry, DatabaseEntry, ForwarderEntry, AutoresponderEntry, FTP/SSL/PHP sections |
| `collector.go` | `Collect()` — orchestrates all UAPI calls per side |
| `write.go` | `WriteInventoryJSON()`, `WriteReport()` (markdown), `AggregateWarnings()` |
| `*_test.go` | 15+ test functions with fake Runner |

**New UAPI wrapper**: `internal/cpanel/email_accounts.go`
- `ListEmailAccounts` — UAPI `Email::list_pops_with_disk`

**Output files**:
- `inventory_source.json` — normalized JSON inventory of source account
- `inventory_destination.json` — same for destination (only if configured)
- `inventory_report.md` — human-readable markdown summary

**Error handling**:
- Fatal: config invalid, source unreachable, domain listing failure
- Warning (non-fatal): mailboxes, databases, docroots unavailable
- `runAccountInventory` returns error (no `os.Exit` inside — defers run correctly)

**Review fixes applied**:
- Run ID: single generation, honors `--run-id` CLI flag
- `--report-json`: supported in inventory path
- `report.json`: includes source/dest HostRef, timestamps, aggregated warnings
- Dead code removed (`sortEmailAccounts`)
- `strings.IndexByte` used instead of custom `findByte`

---

## PR 3A — Email Config Inventory

**Commit**: `c313e7e`

Adds forwarders and autoresponders to the account inventory. Iterates
over all domains from the existing domain inventory.

**New UAPI wrappers**: `internal/cpanel/email_config.go`
- `ListForwarders` — UAPI `Email::list_forwarders` (per domain)
- `ListAutoresponders` — UAPI `Email::list_auto_responders` (per domain)

**New inventory types**:
- `ForwarderEntry` — source, destination, domain
- `AutoresponderEntry` — email, domain, subject, interval

**Behavior**:
- Per-domain iteration: calls forwarders + autoresponders for each domain
- Per-domain failure produces warning, continues with other domains
- UAPI naming quirk: `dest` field = source address, `forward` = destination
- Markdown report includes Forwarders and Autoresponders sections

---

## PR 3B — FTP, SSL, PHP Inventory

**Commit**: `1909ac1`

Adds FTP accounts, SSL certificates, and PHP vhost versions to the
account inventory.

**New UAPI wrappers**:

| File | Function | UAPI Call |
|------|----------|-----------|
| `internal/cpanel/ftp.go` | `ListFTPAccounts` | `Ftp::list_ftp_with_disk` |
| `internal/cpanel/ssl.go` | `ListSSLCerts` | `SSL::list_certs` |
| `internal/cpanel/php.go` | `ListPHPVersions` | `LangPHP::php_get_vhost_versions` |

**New inventory types with ConfigSection pattern**:

Each section uses a structured metadata wrapper:
```json
{
  "available": true,
  "method": "uapi",
  "source_function": "Ftp::list_ftp_with_disk",
  "warnings": [],
  "items": [...]
}
```

This distinguishes "no items found" (`available: true, items: []`) from
"API not available" (`available: false, items: [], warnings: [...]`).

Types: `FTPEntry`, `SSLEntry`, `PHPEntry`, `ConfigSection`, `FTPSection`,
`SSLSection`, `PHPSection`.

**Security**: SSL output contains only metadata (domains, issuer, dates,
validation type). No private keys, no certificate PEM material.

---

## Architecture

```
cmd/cpanel-self-migration/
  main.go                    # CLI entry point
    ├── --account-inventory  → runAccountInventory() [separate path]
    └── (default)            → migrate.Run()         [existing path]

internal/events/             # PR 1 — machine-readable output
  event.go                   # Event model + Emitter
  writer.go                  # JSONL writer (thread-safe, redacted)
  redact.go                  # Secret key detection
  report.go                  # RunReport JSON

internal/accountinventory/   # PR 2/3A/3B — read-only inventory
  types.go                   # All inventory types
  collector.go               # Collect() orchestrator
  write.go                   # JSON + markdown output

internal/cpanel/             # UAPI wrappers (extended)
  email_accounts.go          # Email::list_pops_with_disk      [PR 2]
  email_config.go            # Email::list_forwarders           [PR 3A]
                             # Email::list_auto_responders      [PR 3A]
  ftp.go                     # Ftp::list_ftp_with_disk          [PR 3B]
  ssl.go                     # SSL::list_certs                  [PR 3B]
  php.go                     # LangPHP::php_get_vhost_versions  [PR 3B]

internal/migrate/runner.go   # Modified only in PR 1 (Events field + emit calls)
```

---

## UAPI Coverage

| Module | Function | Wrapper | Direction | PR |
|--------|----------|---------|-----------|----|
| DomainInfo | list_domains | `ListDomains` | Read | existing |
| DomainInfo | domains_data | `ListDocroots` | Read | existing |
| Mysql | list_databases | `ListDatabases` | Read | existing |
| Mysql | list_users | `ListDBUsers` | Read | existing |
| Email | list_pops_with_disk | `ListEmailAccounts` | Read | PR 2 |
| Email | list_forwarders | `ListForwarders` | Read | PR 3A |
| Email | list_auto_responders | `ListAutoresponders` | Read | PR 3A |
| Ftp | list_ftp_with_disk | `ListFTPAccounts` | Read | PR 3B |
| SSL | list_certs | `ListSSLCerts` | Read | PR 3B |
| LangPHP | php_get_vhost_versions | `ListPHPVersions` | Read | PR 3B |

All new wrappers are **read-only**. No write operations added.

---

## Test Fixtures

| Fixture | UAPI Response |
|---------|---------------|
| `email_list_pops.json` | 3 mailbox accounts |
| `email_forwarders.json` | 2 forwarders (single + multi-dest) |
| `email_autoresponders.json` | 1 autoresponder |
| `ftp_list.json` | 2 FTP accounts (main + sub) |
| `ssl_list_certs.json` | 2 Let's Encrypt DV certificates |
| `php_vhost_versions.json` | 2 vhosts (ea-php81 + ea-php74) |

---

## Remaining Roadmap

| PR | Scope | Status |
|----|-------|--------|
| 3C | DNS inventory (UAPI `DNS::parse_zone` + API2 fallback) | Not started |
| 3D | Cron inventory (SSH `crontab -l`) | Not started |
| 4 | Policy Engine (deterministic classification) | Not started |
| 5 | Safe Config Importers | Not started |
| 6 | DNS Import/Verifier | Not started |
| 7 | Cron Approval Workflow | Not started |

---

## Known Issues / Follow-ups

1. `omitempty` on `disk_usage` fields drops legitimate zero values
2. `HostRef` serializes empty dest as `{"ip":"","user":""}` instead of omitting
3. `phase:""` on run-level events (`run_started`, `run_completed`)
4. Event `Data` redaction only covers `map[string]any` (not `map[string]string` or typed structs)
5. DNS UAPI `DNS::parse_zone` requires cPanel v136+ — server has v110, needs API2 fallback
6. Cron has no UAPI — SSH `crontab -l` only
