# cPanel_self-migration — Command Reference

Quick reference. SOURCE is always **read-only**; all writes go to DESTINATION.
Full details in [USAGE.md](USAGE.md).

```text
cpanel-self-migration [--apply|--apply-mirror|--dry-run] [--mail] [--file] [--db] [--full] [--verify-checksums] [--deep-verify] [--config PATH] [--log-level LEVEL]
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
| `--full`                  | With `--apply`: force re-sync of every mailbox (mail only).        |
| `--force-sync`            | Alias of `--full`.                                                  |
| `--verify-checksums`      | With `--apply`: stricter mailbox skip (compare message-ID set); also enables the deep mail content check below. |
| `--deep-verify`           | With `--apply`: verify by CONTENT hash, not metadata — sha256 per web file + per mail message, exact DB row counts + same-version table checksum. Catches same-size corruption; slower (reads every byte on both sides). |
| `--config PATH`           | Path to `host.yaml` (default: `configs/host.yaml`).               |
| `--log-level info\|debug` | Verbosity (`debug` → diagnostics to stderr). Default `info`.       |
| `--version`               | Print version and exit.                                            |
| `-h`, `--help`            | Show help and exit.                                                |

**Selectors:** with none of `--mail`/`--file`/`--db`, **all** run. They combine freely (e.g. `--mail --db`).
**Mutually exclusive:** `--apply` + `--dry-run`; `--apply-mirror` + `--dry-run`.
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
