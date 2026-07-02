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
| `--config PATH`           | Path to `host.yaml` (default: `configs/host.yaml`).               |
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
  [--output-json ./migration_checklist.json] \
  [--output-md ./migration_checklist.md] \
  [--fail-on-not-ready]
```

Overall status: `BLOCKED` (unresolved blockers) →
`MANUAL_ACTION_REQUIRED` (at least one cutover-blocking manual action) →
`NOT_READY` (a core area — mailboxes, databases, web files — has data on
the source and no migration evidence) → `READY_WITH_MANUAL_NOTES` (only
non-blocking notes/reviews/expected differences remain) →
`READY_TO_CUTOVER`.

Honesty rules:

- `migrated_by_tool` is **never** true without evidence. Evidence comes
  only from a `report.json` of a **successful `--apply` run**
  (`--migration-report`), and is explicitly labeled `run_level` — the
  apply flow does not emit per-item events yet. Without the report the
  status is "unknown", even when both inventories look identical.
- A DNS plan (`--dns-plan`) proves a DNS difference is expected **only**
  when the destination already matches the desired translation (plan
  action `skip`). Pending plan work (`add`/`replace`) is still work.
- Areas the inventory cannot see are reported as their own sections
  instead of silently reading as ok: `email_routing`, `default_address`,
  `email_filters`, `redirects` are `not_inventoried` (with explicit
  manual checks); `quota_package` and `server_level_config` are
  `not_accessible_without_root`.

Section statuses: `ok`, `expected_difference`, `manual_required`,
`review_required`, `blocked`, `not_migrated_by_tool`,
`not_inventoried`, `not_accessible_without_root`, `not_applicable`.
Expected differences recognized in v0: regenerated SOA, docroot layout,
A/AAAA already translated per the DNS plan, and a certificate that
differs but is currently valid for the same domains.

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
