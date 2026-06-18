# cPanel_self-migration — Command Reference

Quick reference. The **SOURCE is always read-only**: only ever read from, never
written to or modified, so your source data is never touched or at risk. All writes
go to the **DESTINATION**. Full details in [USAGE.md](USAGE.md).

```text
cpanel-self-migration [--apply|--apply-mirror|--dry-run] [--mail] [--file] [--db] [--domain DOMAIN] [--mailbox ADDR] [--full] [--verify-checksums] [--deep-verify] [--config PATH] [--log-level LEVEL]
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

| File                        | When            |
|-----------------------------|-----------------|
| `mail_analysis.log`         | `--mail`        |
| `web_analysis.log`          | `--file`        |
| `db_analysis.log`           | `--db`          |
| `migration_report.log`      | `--apply`       |
