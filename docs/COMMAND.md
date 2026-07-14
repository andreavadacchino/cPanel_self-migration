# cPanel_self-migration — Command Reference

Quick reference. The **SOURCE is always read-only**: only ever read from, never
written to or modified, so your source data is never touched or at risk. All writes
go to the **DESTINATION**. Full details in [USAGE.md](USAGE.md).

```text
cpanel-self-migration [--apply|--apply-mirror|--dry-run] [--mail] [--file] [--db] [--domain DOMAIN] [--mailbox ADDR] [--full] [--verify-checksums] [--deep-verify] [--config PATH] [--log-level LEVEL] [--run-id ID] [--output-dir DIR] [--json-events] [--report-json]
```

## Flags

| Flag                      | Effect                                                              |
|---------------------------|--------------------------------------------------------------------|
| *(no flag)*               | Dry-run, all data kinds (mail + files + databases).                |
| `--dry-run`               | Explicit dry-run (default): analyze + compare, **no writes**.       |
| `--apply`                 | Perform the migration (**writes to DEST**).                         |
| `--apply-mirror`          | Like `--apply`, but MIRROR each mailbox: rename dest mailbox aside (`<user>-bak`) + re-copy all (removes dest-only mail). Files/DBs as `--apply`. |
| `--mail`                  | Select MAIL only (mailboxes: accounts + messages).                 |
| `--file`                  | Select WEBSITE FILES only (docroots / `public_html`).             |
| `--db`                    | Select DATABASES only (data + users + grants + config rewrite).   |
| `--domain DOMAIN`         | Narrow to ONE domain: its docroot + mailboxes (**never** databases). Composes with `--mail`/`--file`. |
| `--mailbox local@domain`  | Narrow to ONE mailbox (copy + verify). Implies mail only.          |
| `--full`                  | With `--apply`: force re-sync of every mailbox (mail only).        |
| `--force-sync`            | Alias of `--full`.                                                  |
| `--verify-checksums`      | With `--apply`: stricter mailbox skip (compare message-ID set); also enables the deep mail content check below. |
| `--deep-verify`           | With `--apply`: verify by CONTENT hash, not metadata — sha256 per web file + per mail message, exact DB row counts + same-version table checksum. Catches same-size corruption; slower (reads every byte on both sides). |
| `--config PATH`           | Path to `host.yaml` (default: `configs/host.yaml`). Each host authenticates with **either** `ssh_pass` **or** `ssh_key_path` (+ `ssh_key_passphrase` if encrypted), never both; a relative key path resolves against the config file's directory. |
| `--log-level info\|debug` | Verbosity (`debug` → diagnostics to stderr). Default `info`.       |
| `--run-id ID`             | Optional run identifier for structured output. Default: auto-generated `run-YYYYMMDD-HHMMSS`. |
| `--output-dir DIR`        | Output directory for all artifacts (default: CWD). Created if missing. |
| `--json-events`           | Write JSONL events to `<output-dir>/events.jsonl`. Does not suppress stdout. |
| `--report-json`           | Write JSON summary to `<output-dir>/report.json`. Does not suppress stdout. |
| `--account-inventory`     | Collect a read-only account inventory (domains, mailboxes, databases, DNS zones, cron jobs) and exit. No migration. |
| `--version`               | Print version and exit.                                            |
| `-h`, `--help`            | Show help and exit.                                                |

**Selectors (what KIND):** with none of `--mail`/`--file`/`--db`, **all** run. They combine freely (e.g. `--mail --db`).
**Narrowing (which DOMAIN/mailbox):** `--domain X` restricts the run to one domain (its docroot + mail, **never** databases); compose with `--mail`/`--file` (e.g. `--domain X --mail`). `--mailbox local@domain` restricts to one mailbox (mail only). The target is validated against the source; a missing domain/mailbox fails fast.
**Mutually exclusive:** `--apply` + `--dry-run`; `--apply-mirror` + `--dry-run`.
**Rejected (exit 2):** `--domain --db`; `--mailbox` with `--file`/`--db`/`--domain`. Both `--domain` and `--mailbox` require a **configured destination** (they scope a migration; source-only analysis covers the whole account).
`--full` and `--verify-checksums` affect the **mail** flow only and require `--apply`.
`--apply-mirror` implies the apply phase and changes the **mail** flow only; **do not** use it after switching the MX (it moves dest-only mail aside to `<user>-bak`).

## Common commands

```sh
# Build
make build

# DRY-RUN (no changes)
./cpanel-self-migration                         # everything
./cpanel-self-migration --mail                  # mail only
./cpanel-self-migration --file                  # website files only
./cpanel-self-migration --db                    # databases only
./cpanel-self-migration --mail --db             # mail + databases

# APPLY (writes to DEST)
./cpanel-self-migration --apply                 # everything
./cpanel-self-migration --apply --file          # website files only
./cpanel-self-migration --apply --db            # databases only
./cpanel-self-migration --apply --mail          # mail only (also the final delta sync)
./cpanel-self-migration --apply --mail --full           # force re-sync every mailbox
./cpanel-self-migration --apply --mail --verify-checksums   # strict mailbox check
./cpanel-self-migration --apply-mirror --mail   # MIRROR mail: dest = exact copy of src (dest-only mail -> <user>-bak)

# DEEP VERIFY (content-hash integrity, slower — reads every byte on both sides)
./cpanel-self-migration --apply --deep-verify           # everything, verify by content hash
./cpanel-self-migration --apply --mail --deep-verify    # mail: per-message body hashes
./cpanel-self-migration --apply --file --deep-verify    # web files: sha256 per file
./cpanel-self-migration --apply --db --deep-verify      # databases: row counts + table checksum

# NARROW to one domain or one mailbox
./cpanel-self-migration --domain tissolution.it                 # dry-run: that domain's docroot + mail
./cpanel-self-migration --apply --domain tissolution.it         # apply: that domain's docroot + mail (no DB)
./cpanel-self-migration --apply --domain tissolution.it --mail  # only that domain's mailboxes
./cpanel-self-migration --apply --domain tissolution.it --file  # only that domain's docroot
./cpanel-self-migration --apply --mailbox info@tissolution.it   # only that one mailbox (copy + verify)

# Custom config / debug
./cpanel-self-migration --config /path/host.yaml
./cpanel-self-migration --apply --log-level debug 2> debug.txt
```

## Exit codes

| Code | Meaning                                  |
|------|------------------------------------------|
| `0`  | Success — everything migrated and verified clean |
| `1`  | A hard error, OR an `--apply` that finished with failures / unresolved divergences (see [USAGE.md §16](USAGE.md#16-exit-codes)) |
| `2`  | Flag misuse (e.g. `--apply` + `--dry-run`)|
| `130`| Interrupted (Ctrl-C)                     |

## Artifacts (under `logs/`)

| File                        | When               |
|-----------------------------|--------------------|
| `logs/mail_analysis.log`    | `--mail`           |
| `logs/web_analysis.log`     | `--file`           |
| `logs/db_analysis.log`      | `--db`             |
| `logs/migration_report.log` | `--apply`          |
| `events.jsonl`              | `--json-events`           |
| `report.json`               | `--report-json`           |
| `inventory_source.json`     | `--account-inventory`     |
| `inventory_destination.json`| `--account-inventory` (if dest configured) |
| `inventory_report.md`       | `--account-inventory`     |
| `inventory_diff.json`       | `inventory diff`          |
| `inventory_diff.md`         | `inventory diff`          |
| `policy_report.json` / `.md`| `inventory policy`        |
| `dns_import_plan.json` / `.md` | `inventory dns-plan`   |
| `dns_verify_report.json` / `.md` | `dns verify`         |
| `dns_apply_report.json` / `.md` | `dns apply`           |
| `dns_backup_*.json`          | `dns apply` (pre-write) |
| `cron_apply_plan.json` / `.md` | `inventory cron-plan` |
| `cron_apply_report.json` / `.md` | `cron apply`         |
| `cron_backup_*.json`         | `cron apply` (pre-write) |
| `migration_checklist.json` / `.md` | `inventory checklist` |

## Subcommand: `inventory diff`

Deterministic, fully offline comparison of two inventory JSON files
produced by `--account-inventory`. Never connects to any server; it only
states WHAT differs (source → destination), with no judgment about
safety.

```bash
cpanel-self-migration inventory diff \
  --source ./inventory_source.json \
  --destination ./inventory_destination.json \
  [--output-json ./inventory_diff.json] \
  [--output-md ./inventory_diff.md]
```

Compares all 10 sections (domains, mailboxes, databases, forwarders,
autoresponders, ftp, ssl, php, dns, cron). DNS records are compared
order-insensitively per zone; cron jobs are matched by their redacted
command hash — the raw command is never reconstructed. Sections marked
`available:false` on either side are skipped with a warning.

The diff records the SHA-256 of the raw bytes of both input files
(`source_sha256`/`destination_sha256`): the checklist verifies the
provenance chain against them.

Exit codes: `0` diff generated (differences are NOT an error), `1`
missing/invalid input or write failure, `2` flag usage error.

## Subcommand: `inventory policy`

Deterministic classification of an `inventory_diff.json` into a
migration-readiness report (`policy_report.json` + `policy_report.md`).
Fully offline: it states whether each difference is a blocker, needs
review, or is informational — it never decides what to do about it.

```bash
cpanel-self-migration inventory policy \
  --diff ./inventory_diff.json \
  [--output-json ./policy_report.json] \
  [--output-md ./policy_report.md] \
  [--fail-on-blockers]
```

Overall status: any blocker → `blocked`; any review → `review_required`;
otherwise `ready`. Blockers include: removed mailboxes/databases, the
main domain or a whole DNS zone missing, MX/NS records changed or
removed, certificates missing for still-present domains, and active cron
jobs missing on the destination. The full rule table lives in
`docs/dev/PR5A_POLICY_ENGINE_V0_DESIGN.md`.

Exit codes: `0` report generated (blockers are findings, not process
errors), `1` missing/invalid input or write failure, `2` flag usage
error, `3` `--fail-on-blockers` was set and the overall status is
`blocked`. The reports are always fully written before the gating exit;
`review_required` never gates. Without the flag the exit stays `0`
regardless of status, so existing consumers are unaffected.

`--fail-on-blockers` makes the pipeline usable as a CI / pre-migration
gate without parsing JSON:

```bash
cpanel-self-migration inventory policy --diff ./inventory_diff.json --fail-on-blockers \
  && echo "migration can proceed"
```

The report records the SHA-256 of the raw bytes of the consumed diff
(`input_diff_sha256`): the checklist verifies the provenance chain
against it.

## Subcommand: `inventory dns-plan`

Fully offline builder of the DNS import plan
(`dns_import_plan.json` + `.md`): what a future gated apply would write
into the DESTINATION account's zones. It consumes the two inventory
files (NOT the diff, which is lossy for DNS records); the policy report
is optional **context** — findings are cross-referenced into the plan,
but a `blocked` status never prevents plan generation (NS always
differs between hosts, so gating on the status would block every real
migration). Design: `docs/dev/PR6A_DNS_IMPORT_DESIGN.md`.

```bash
cpanel-self-migration inventory dns-plan \
  --source ./inventory_source.json \
  --destination ./inventory_destination.json \
  [--policy ./policy_report.json] \
  --ip-map 194.76.118.193=38.224.109.78 [--ip-map OLD=NEW ...] \
  [--output-json ./dns_import_plan.json] \
  [--output-md ./dns_import_plan.md]
```

Plan actions per rrset (zone, type, name — canonicalized lowercase
absolute FQDNs): `add` (missing on destination), `replace` (values
differ after translation), `skip` (equal, TTL-only drift, SOA,
host-validation records `_acme-challenge*`/`_cpanel-dcv-test-record`),
`manual` (never applied, no override: NS/delegation, unsupported record
types, CNAME cross-type conflicts, A/AAAA with any un-mapped address,
TXT containing a mapped source IP — e.g. SPF — unless the destination
already carries exactly the ip-map translation, which is a `skip`).
Destination-only rrsets are listed as informational and **never
deleted**.

Safety rules: every A/AAAA value must have an `--ip-map` entry
(identity `X=X` authorizes a verbatim copy); written TTLs are capped at
3600; the plan embeds the SHA-256 of both inventory files and the
effective ip-map for auditability.

Exit codes: `0` plan generated, `1` missing/invalid input (including
malformed `--ip-map` values) or write failure, `2` flag usage error.

## Subcommand: `dns verify`

Read-only verification of the DESTINATION zones against a
`dns_import_plan.json`: re-fetches each plan zone over SSH (destination
only — the source is never dialed, it may already be decommissioned)
with the collector's own fetch (UAPI `DNS::parse_zone`, API2 fallback)
and reports, per planned op, whether the live zone matches the plan.
Use it to certify a manual DNS edit session done from the plan
worksheet, or (future 6D) a `dns apply`. Design:
`docs/dev/PR6C_DNS_VERIFY_DESIGN.md`.

```bash
cpanel-self-migration dns verify \
  --plan ./dns_import_plan.json \
  [--config ./host.yaml] \
  [--source ./inventory_source.json] \
  [--destination ./inventory_destination.json] \
  [--output-json ./dns_verify_report.json] \
  [--output-md ./dns_verify_report.md] \
  [--fail-on-drift]
```

Per-op statuses: `applied` (add/replace landed), `unchanged` (checkable
skip still matches), `pending` (zone still in the plan-time state),
`drift` (matches neither), `manual_review` (manual ops, reported only),
`not_checked` (SOA / host-validation skips). Live rrsets that postdate
the plan are listed as `untracked` (informational). The `clean` verdict
gates on pending + drift + unavailable zones + **manual zones** (a plan
that computed no ops for a zone cannot be verified — re-run the
pipeline); manual ops and untracked rrsets never gate.

Stale-plan gate: with `--source`/`--destination`, the file hashes must
match the plan's embedded `source_sha256`/`destination_sha256`, or the
whole run is refused (exit `3`) before any SSH.

Exit codes: `0` verify ran and reports were written (even with drift),
`1` invalid input / config / SSH dial / write failure, `2` flag usage
error, `3` gated refusal (stale plan, or `--fail-on-drift` with a
verdict that is not clean).

## Subcommand: `dns apply`

Applies a `dns_import_plan.json` to the DESTINATION account via
`DNS::mass_edit_zone` with serial guard (optimistic locking). Default is
fully offline dry-run (no connections, no writes). Design:
`docs/dev/PR6D_DNS_APPLY_DESIGN.md`, `docs/dev/PR_DNSV2_REPLACE_DESIGN.md`.

```bash
cpanel-self-migration dns apply \
  --plan ./dns_import_plan.json \
  [--config ./host.yaml] \
  [--yes-apply-writes] \
  [--backup ./dns_backup.json] \
  [--output-json ./dns_apply_report.json] \
  [--output-md ./dns_apply_report.md]

# Rollback:
cpanel-self-migration dns apply \
  --rollback ./dns_backup_YYYYMMDD-HHMMSS.json \
  [--report ./dns_apply_report.json | --accept-report-loss] \
  [--yes-apply-writes] \
  [--config ./host.yaml] \
  [--output-json ./dns_rollback_report.json]
```

Operations: `add` (missing rrsets) and `replace` (rrsets with different
values — implemented as atomic remove+add in a single `mass_edit_zone`
call). `skip` and `manual` ops are never written.

Replace preconditions (per-op): if the live zone already has the desired
values → `skipped` (already_present). If the live zone still has the
plan-time destination values → proceed. If a third value is observed →
`refused_precondition` (drift).

Safety contract: backup-or-nothing before the first write; per-op
verify-after (re-fetch zone, match by type+name+data); reports written
before exit.

Rollback: report-driven inverse — `add` ops are removed by line index;
`replace` ops are restored to their pre-apply values (from the backup).
Degraded rollback (`--accept-report-loss`): ALL ops become MANUAL.

Exit codes: `0` ok, `1` failure (report still written), `2` flags,
`3` gated refusal (stale serial, drift).

## Subcommand: `cron apply`

Applies a `cron_apply_plan.json` to the DESTINATION account via SSH
`crontab -` (pipe stdin). Default is fully offline dry-run.

```bash
cpanel-self-migration cron apply \
  --plan ./cron_apply_plan.json \
  [--config ./host.yaml] \
  [--yes-apply-writes] \
  [--backup ./cron_backup.json] \
  [--output-json ./cron_apply_report.json] \
  [--output-md ./cron_apply_report.md]

# Rollback:
cpanel-self-migration cron apply \
  --rollback ./cron_backup_YYYYMMDD-HHMMSS.json \
  [--yes-apply-writes] \
  [--config ./host.yaml]
```

Exit codes: `0` ok, `1` failure, `2` flags.

## Subcommand: `cron verify`

Read-only verification of the DESTINATION crontab against a
`cron_apply_plan.json`.

```bash
cpanel-self-migration cron verify \
  --plan ./cron_apply_plan.json \
  [--config ./host.yaml] \
  [--fail-on-drift]
```

Exit codes: `0` verify ran, `1` failure, `2` flags, `3` `--fail-on-drift`
with drift detected.

## Subcommand: `inventory cron-plan`

Fully offline builder of the cron apply plan. Compares source and
destination crontabs and produces per-job create/skip/manual decisions.

```bash
cpanel-self-migration inventory cron-plan \
  --source ./inventory_source.json \
  --destination ./inventory_destination.json \
  [--output-json ./cron_apply_plan.json] \
  [--output-md ./cron_apply_plan.md]
```

Exit codes: `0` plan generated, `1` failure, `2` flags.

## Subcommand: `inventory checklist`

Fully offline composition of the pipeline's artifacts into the
operator-facing **migration checklist**
(`migration_checklist.json` + `.md`): per account area, what the tool
migrated (with evidence), what it did not migrate, what differs but is
expected, what requires manual action, and what blocks shutting down the
old server. It never connects to any server.

```bash
cpanel-self-migration inventory checklist \
  --source ./inventory_source.json \
  --destination ./inventory_destination.json \
  --diff ./inventory_diff.json \
  --policy ./policy_report.json \
  [--dns-plan ./dns_import_plan.json] \
  [--migration-report ./report.json] \
  [--acceptances ./acceptances.json] \
  [--output-json ./migration_checklist.json] \
  [--output-md ./migration_checklist.md] \
  [--fail-on-not-ready]
```

### Operator acceptances (`--acceptances`)

`acceptances.json` lets the operator formally accept reviewed manual
actions so they stop gating the verdict — attributably and fail-safe:

```json
{
  "mode": "operator-acceptances",
  "format_version": 1,
  "checklist_file": "migration_checklist.json",
  "checklist_sha256": "<sha256 of the reviewed checklist file>",
  "acceptances": [
    {
      "action_key": "AK-650e9068dc67",
      "action_id": "MA-004",
      "reason": "confirmed with the customer",
      "accepted_by": "andrea",
      "accepted_at": "2026-07-02T10:00:00Z"
    }
  ]
}
```

- Acceptances bind to the action's stable `key` (shown in both reports),
  NOT to the positional `MA-nnn` id. If the underlying finding changes,
  the key changes and the acceptance stops matching: the action
  resurfaces un-accepted, with a warning.
- `checklist_sha256` records which checklist the operator reviewed; when
  `checklist_file` is present its hash is verified strictly and a
  mismatch rejects the whole file (warning, nothing accepted).
- Non-acceptable actions (`acceptable: false` — an external MX to
  confirm, a lost active cron job) can never be accepted: they must be
  resolved.
- An accepted action no longer counts toward `MANUAL_ACTION_REQUIRED`;
  sections list it in `accepted_by_operator` and the summary counts it.

Overall status: `BLOCKED` (unresolved blockers) →
`MANUAL_ACTION_REQUIRED` (at least one cutover-blocking manual action) →
`NOT_READY` (a core area — mailboxes, databases, web files — has data on
the source and no migration evidence) → `READY_WITH_MANUAL_NOTES` (only
non-blocking notes/reviews/expected differences remain) →
`READY_TO_CUTOVER`.

Honesty rules:

- `migrated_by_tool` is **never** true without evidence. Evidence comes
  only from a `report.json` of a **successful `--apply` run**
  (`--migration-report`). It is labeled `per_item` when the report's
  `phases_completed` proves BOTH the migrate and the verify phase of that
  section's flow completed (`migrate_mail`+`verify_mail`,
  `copy_files`+`verify_files`, `migrate_db`+`verify_db`; domains need
  `create_domains` only) — the verify phases are per-item integrity
  passes whose failures make the run non-success. Otherwise (including
  every pre-7C report without `phases_completed`) it is `run_level`.
  Without the report the status is "unknown", even when both inventories
  look identical.
- A DNS plan (`--dns-plan`) proves a DNS difference is expected **only**
  when the destination already matches the desired translation (plan
  action `skip`). Pending plan work (`add`/`replace`) is still work.
- `email_routing`, `default_address`, `email_filters` and `redirects`
  are real inventoried sections (PR 7E): actions are generated only on
  actual differences (a routing-mode change or a lost filter is
  blocking; a genuine redirect difference is a non-blocking
  confirmation; CMS-generated `.htaccess` rewrites are recognized as
  expected — they travel with the web files). Root-only areas
  (`quota_package`, `server_level_config`) remain
  `not_accessible_without_root`.

Section statuses: `ok`, `expected_difference`, `manual_required`,
`review_required`, `blocked`, `not_migrated_by_tool`,
`not_accessible_without_root`, `not_applicable` (`not_inventoried`
remains in the schema for artifacts produced by older builds).
Expected differences recognized: regenerated SOA, docroot layout,
A/AAAA already translated per the DNS plan, a certificate that
differs but is currently valid for the same domains, and CMS-generated
rewrites missing on a destination whose web files are not synced yet.
A regenerated DKIM key (dns-plan `replace` on a `_domainkey` TXT) now
raises a dedicated non-blocking `CONFIRM_DNS_RECORD` action.

Manual actions carry a stable ID (`MA-001`…), a type
(`RECREATE_CRON`, `ADAPT_CRON_PATH`, `CONFIRM_MX_EXTERNAL`,
`CONFIRM_DNS_RECORD`, `UPDATE_SPF`, `REISSUE_SSL`,
`CHECK_PHP_COMPATIBILITY`, `CREATE_ON_DESTINATION`,
`VERIFY_EXTERNAL_SERVICE`, `CONFIRM_EMAIL_ROUTING`,
`MANUAL_CHECK_REQUIRED`, `ACCEPT_EXPECTED_DIFFERENCE`), and a
`blocking_cutover` flag; the Markdown report lists the blocking ones
under "Before shutting down the old server".

The checklist embeds the SHA-256 of every input file and verifies the
**provenance chain** (PR 7B): the hashes the diff, the policy report and
the DNS plan record about their OWN inputs must match the files being
composed. All links match → `chain_verified: true`. Hashes missing
(artifacts from older builds) → `false` with a "not verifiable" warning,
no gating. A hash **mismatch** (an artifact generated from different
files) → `false`, an explicit warning, and any `READY_*` verdict is
capped to `NOT_READY` — a composition proven inconsistent can never
read as ready.

Exit codes: `0` checklist generated (manual actions and blockers are
findings, not process errors), `1` missing/invalid input or write
failure, `2` flag usage error, `3` `--fail-on-not-ready` was set and the
overall status is neither `READY_TO_CUTOVER` nor
`READY_WITH_MANUAL_NOTES`. The reports are always fully written before
the gating exit.

## Subcommand: `ui`

A LOCAL web workstation over the pipeline artifacts: the operator
configures the servers, launches the read-only analysis and reads the
results in a browser — the terminal is only needed for the migration
itself. It renders the migration checklist (verdict, sections, manual
actions with their stable acceptance keys, warnings) plus an artifact
presence table, and re-hashes every input the checklist records — a
mismatch renders a dominant **STALE** banner.

From the page you can:

- **save the server connections** (source and destination IP, port, SSH
  user/password) → written to `host.yaml` in the artifact directory
  (0600, local only; blank password fields keep the stored ones; the
  file is validated by the same `config.Load` the CLI uses);
- **run the read-only analysis**: the UI spawns the tool's own binary
  through the pipeline (account inventory over SSH — the only connecting
  step, source read-only by construction — then diff → policy →
  checklist, picking up `acceptances.json`/`dns_import_plan.json`/apply
  `report.json` when present). One run at a time; the page auto-refreshes
  with per-step progress and output tails. `--apply` stays terminal-only.
- **accept a reviewed manual action** inline (name + reason): the UI
  upserts `acceptances.json` (bound to the current checklist's sha256)
  and regenerates the checklist immediately, so the verdict updates
  without a full re-run. Non-acceptable actions (lost active cron, MX to
  confirm) are refused — they must be resolved.
- **monitor a migration run** (UI phase 3, monitor-only): when the
  terminal run was started with `--json-events`, the dashboard tails
  `events.jsonl` and shows the LAST run — phase by phase, with the
  per-item apply evidence (failed/unverified mailboxes, migrated
  databases, divergence counts) — auto-refreshing while the run is
  live. A run with no terminal event and no events for over 10 minutes
  is shown as **stalled** and stops refreshing. The UI never launches
  `--apply`: it only reads the file.

```bash
cpanel-self-migration ui [--dir ./run-artifacts] [--listen 127.0.0.1:8422]
# then open http://127.0.0.1:8422/
```

Safety, by construction:

- binds to **loopback only** (`127.0.0.1`, `::1` or `localhost`); every
  request also passes an anti-DNS-rebinding **Host gate**, an **Origin
  check**, and mutating POSTs require the per-start **CSRF token**;
- the UI process never opens SSH itself and never mutates servers: the
  analysis runs as a subprocess of the CLI, which remains the single
  authority for every step;
- it serves rendered pages only — no raw-file serving, no other routes;
- no readiness logic is re-implemented in the UI: it displays decisions
  the offline pipeline already computed.

Possible next refinements: revoking an acceptance from the browser,
operator-name persistence, artifact downloads.

## Subcommand: `migration`

Session governance for single-account migrations. Fully offline — never
connects to any server. Manages the lifecycle state and artifact trail of
one migration from creation to archive.

```bash
# Create a new migration session
cpanel-self-migration migration init \
  --name giorginisposi \
  --source-profile old193 \
  --destination-profile new78

# List all sessions (ordered by created_at)
cpanel-self-migration migration list [--json]

# Show session details
cpanel-self-migration migration show <session-id> [--json]

# Transition to a new status (validated against the transition matrix)
cpanel-self-migration migration set-status <session-id> --status checklist_ready
# Force an illegal transition (requires reason, recorded in timeline)
cpanel-self-migration migration set-status <session-id> --status blocked --force --reason "external dependency"

# Attach an artifact (copies into session dir, computes SHA256)
cpanel-self-migration migration attach-artifact <session-id> \
  --kind migration_checklist --path ./migration_checklist.json

# Archive a completed/blocked/failed session
cpanel-self-migration migration archive <session-id>
```

**Storage**: `~/.cpanel-self-migration/migrations/` (override with
`--home` flag or `CPANEL_MIGRATION_HOME` env). Directories 0700, files
0600. All writes are atomic (write-temp + fsync + rename).

**Statuses**: `draft` → `preflight_required` → `inventory_ready` →
`checklist_ready` → `[manual_actions_required]` → `ready_for_apply` →
`apply_in_progress` → `apply_done` → `verification_required` →
`ready_for_cutover` → `cutover_done` → `archived`. `blocked`/`failed`
reachable from any active state. `archived` is terminal.

**Artifact kinds** (whitelist): `inventory_source`, `inventory_destination`,
`inventory_diff`, `policy_report`, `dns_plan`, `migration_checklist`,
`acceptances`, `apply_report`, `dns_apply_report`, `dns_verify_report`,
`email_plan`, `email_apply_report`, `email_verify_report`, `cron_plan`,
`cron_apply_report`, `cron_verify_report`, `events_jsonl`. Unknown kind →
exit 2.

Exit codes: `0` success, `1` operational error (session not found,
transition not allowed), `2` usage error (bad flags, unknown kind/status).

## Subcommand: `execute`

The **platform → executor bridge** (ADR-001 §D5). Consumes an
`execution-spec-v1` JSON document and runs a **governed dry-run** — it
never writes to the destination. It emits the versioned executor →
platform output: `execution-event-v1` lines to `events.jsonl` and a final
`execution-result-v1` `report.json`.

```bash
cpanel-self-migration execute \
  --spec ./execution-spec.json \
  [--config ./host.yaml] \
  [--output-dir ./out]
```

The spec carries only **non-secret references** (`plan_id`,
`source_snapshot_id`, `destination_snapshot_id`, `comparison_report_id`,
`mode`, `scope`); the connection details and credentials come from
`host.yaml`, resolved at run time and never taken from the spec. `mode`
must be `dry_run` and `scope` must select at least one of
`mail`/`files`/`databases` — both enforced by the contract parser; an
invalid spec is rejected before any connection.

`Apply` is always false: this command cannot write to the destination
regardless of the spec.

**One execution = one workspace.** `--output-dir` must not already contain
`events.jsonl` or `report.json`: the bridge refuses a directory a previous
execution used, and writes nothing. The orchestrator gives every run a
directory of its own — including a retry, which is a new run. Without this
the two artifacts would disagree after a retry: `events.jsonl` is opened in
append mode, so a second run's events would be interleaved with the first's
under the same `run_id` (every line still a valid `execution-event-v1`, so
no consumer could detect it), while `report.json` is truncated and would
describe only the last attempt.

Exit codes: `0` dry-run completed, `1` input/runtime failure (the
`report.json` is still written when the run reached it), `2` flag/usage
error, `130` interrupted — the signal contract the platform maps to the
`interrupted` status: on `SIGINT`/`SIGTERM` the run stops and still writes
a `report.json` whose `exit_status` is `interrupted`.

An exit of `1` alone does not say whether the spec was rejected or the run
failed. Given a **fresh** workspace, the artifacts do: a rejected spec or an
unreadable config fail before anything is written, so the directory stays
empty; a run that genuinely failed leaves a `report.json` carrying its
`errors`. The one case that breaks the rule is a workspace that was already
used — there a `report.json` is present that *this* run did not write, which
is the second reason every run gets a directory of its own.
