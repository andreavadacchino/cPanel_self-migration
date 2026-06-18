# cPanel_self-migration — Usage Guide

A tool to migrate **email mailboxes, website files, and MySQL databases** (plus
the domains they need) between two cPanel accounts using only user-level SSH (no
root/WHM).

> ⚠️ **Golden rule:** the **SOURCE (SRC) host is ALWAYS read-only**, in every
> mode. All writes go **only** to the **DESTINATION (DEST)** host. This machine
> acts as a bridge: data flows `SRC → (relay) → DEST`. The source is only ever
> read from, never written to, deleted from, or modified, so **your source data is
> never touched or put at risk**, even if a run is interrupted or fails.

---

## Table of contents

1. [Configuration](#1-configuration)
2. [What gets migrated: `--mail` / `--file` / `--db`](#2-what-gets-migrated---mail----file----db)
3. [The pipeline (steps)](#3-the-pipeline-steps)
4. [DRY-RUN mode (default)](#4-dry-run-mode-default)
5. [The `--apply` command](#5-the---apply-command)
6. [Mail flow details](#6-mail-flow-details)
7. [Website-file flow details](#7-website-file-flow-details)
8. [Database flow details](#8-database-flow-details)
9. [The `--full` / `--force-sync` flag](#9-the---full----force-sync-flag)
10. [The `--verify-checksums` and `--deep-verify` flags](#10-the---verify-checksums-and---deep-verify-flags)
11. [The `--apply-mirror` flag](#11-the---apply-mirror-flag)
12. [Other flags](#12-other-flags)
13. [Optional `databases:` config section](#13-optional-databases-config-section)
14. [Generated artifacts](#14-generated-artifacts)
15. [Interruption (Ctrl-C)](#15-interruption-ctrl-c)
16. [Exit codes](#16-exit-codes)
17. [Recommended cutover procedure](#17-recommended-cutover-procedure)
18. [Security notes (secret handling)](#18-security-notes-secret-handling)

---

## 1. Configuration

Configuration lives in `configs/host.yaml` (it is **gitignored**: it holds
credentials and must never be committed).

```sh
cp configs/host_template.yaml configs/host.yaml   # then fill in real values
make build                                         # produces ./cpanel-self-migration
```

Structure of `host.yaml`:

```yaml
src:                       # SOURCE — read-only
  ip: "192.168.1.100"
  port: 22
  ssh_user: "your_cpanel_user"
  ssh_pass: "********"
  timeout: "10s"

dest:                      # DESTINATION — receives all writes
  ip: "192.168.1.200"
  port: 22
  ssh_user: "your_cpanel_user"
  ssh_pass: "********"
  timeout: "10s"

# OPTIONAL — only needed in rare cases, see section 13.
# databases:
#   - name: "srcuser_orphandb"
#     user: "srcuser_someuser"
#     password: "the_db_password"
```

The file is auto-discovered at `configs/host.yaml` (next to the binary or in the
current directory); override the path with `--config`.

If the `dest` section is **entirely** absent, the tool only runs the **SOURCE
analysis** and stops. A **partially-filled** `dest` (some fields set, others
missing — e.g. a forgotten `ssh_pass`) is treated as a mistake and **rejected
with a clear error**, rather than being silently ignored (which would run a
source-only analysis with no migration and no warning).

> The cPanel **account password** (the `ssh_pass`) doubles as the MySQL
> credential the tool uses to dump databases on the SOURCE (the account user is
> a MySQL user that can read all the account's databases). No per-database
> passwords are required for the dump.

---

## 2. What gets migrated: `--mail` / `--file` / `--db`

The tool migrates three independent kinds of data. You choose which with three
selector flags:

| Flag      | Migrates                                                        |
|-----------|----------------------------------------------------------------|
| `--mail`  | Email **mailboxes** (accounts + messages, `~/mail`).           |
| `--file`  | **Website files** (document roots / `public_html`).            |
| `--db`    | **MySQL databases** (data + users + grants + wp-config rewrite).|

**With none of them set, ALL three run** (the default). Combining them is valid:
`--mail --db` runs mail + databases and skips website files. Setting all three
is equivalent to setting none.

Domain/account creation on the destination is **shared**: it runs (in `--apply`)
whenever *any* of the three flows is selected, because mailboxes, docroots and
database users all need the destination domain/account to exist first.

Examples:

```sh
./cpanel-self-migration                  # dry-run, everything
./cpanel-self-migration --file           # dry-run, website files only
./cpanel-self-migration --apply --db     # apply, databases only
./cpanel-self-migration --apply --mail --file   # apply, mail + files, no DB
```

### What is NOT migrated (out of scope)

The tool migrates **only** mailboxes (`~/mail`), the discovered website document
roots, and the MySQL databases it can enumerate. A clean run (exit 0, "integrity
check passed") certifies **only those three flows** — it does **not** mean the
whole cPanel account was copied.

The following account data is **neither migrated nor verified** — the tool never
enumerates it, so it contributes nothing to the result and a green run says
nothing about it:

- **cron jobs**;
- **email forwarders, filters, and autoresponders**;
- **mailman / mailing lists**;
- **DNS zone records** (the source's zone is not read or recreated);
- **SSL/TLS certificates** (and AutoSSL state);
- **FTP accounts**;
- **files above a document root** — anything in the home directory outside the
  migrated docroots (e.g. `~/ssl`, `~/.htpasswd` files above `public_html`,
  arbitrary home files);
- **subdomain / addon-domain docroots** that cPanel does **not** return in its
  `domains_data` inventory (so they are never discovered as a docroot);
- **system-excluded paths** pruned on both sides by design (e.g. `cgi-bin`,
  `.ftpquota`).

> ℹ️ These must be migrated and checked **manually**. Plan for them before
> cutover — a successful migration here is **not** a complete account transfer.

**What IS covered (do not double-migrate):** databases with **no** `wp-config`
(or other app-config) reference — "orphan" databases — **are** discovered from
the MySQL inventory and migrated/verified like any other DB. Only the classes
listed above are out of scope.

### Narrowing to one domain or one mailbox: `--domain` / `--mailbox`

`--mail`/`--file`/`--db` choose **which kinds** of data run. Two optional filters
choose **which domain or mailbox**:

| Filter                   | Restricts the run to                  | Notes                                            |
|--------------------------|---------------------------------------|--------------------------------------------------|
| `--domain DOMAIN`        | one domain: its **docroot + mailboxes** | Composes with `--mail`/`--file`. **Never** migrates databases. |
| `--mailbox local@domain` | one **mailbox** (copy + verify)       | Implies **mail only**.                           |

`--domain` defaults to docroot + mail with no kind flag. Compose it to go narrower:

| Command              | Effect                          |
|----------------------|---------------------------------|
| `--domain X`         | X's docroot + mailboxes         |
| `--domain X --mail`  | only X's mailboxes              |
| `--domain X --file`  | only X's docroot                |
| `--mailbox a@X`      | only the mailbox `a@X` (mail only) |

The target is validated against the **source** inventory: a `--domain` absent
from the source, or a `--mailbox` that is not an active source mailbox, fails
fast and lists what is available. Destination domain/account creation still runs
for the targeted domain when it is missing, so a single domain can be migrated
end-to-end onto a fresh account.

**Rejected up front (exit 2):**

- `--domain --db` — cPanel databases are account-wide and only loosely tied to a
  domain (by the `wp-config.php` location), so `--domain` deliberately never
  touches them. Migrate databases without `--domain`.
- `--mailbox` together with `--file`, `--db`, or `--domain` — `--mailbox` is
  mail-only and already names its own domain.
- `--domain` / `--mailbox` with **no destination configured** — these filters
  scope a *migration*; source-only analysis always covers the whole account.

> **Note:** the mail-analysis artifact (`mail_analysis.log`) and the on-screen `~/mail
> scan` line are an account-wide source audit and are **not** narrowed by these
> filters; the compare/apply/verify steps and their counts **are**.

Examples:

```sh
./cpanel-self-migration --domain tissolution.it                 # dry-run: docroot + mail for one domain
./cpanel-self-migration --apply --domain tissolution.it         # apply: docroot + mail (no DB)
./cpanel-self-migration --apply --domain tissolution.it --mail  # only that domain's mailboxes
./cpanel-self-migration --apply --mailbox info@tissolution.it   # only one mailbox (copy + verify)
```

---

## 3. The pipeline (steps)

The pipeline is a fixed sequence; only the steps for the **selected** flows run,
and the `[n/N]` counter counts **only the active steps** (so a `--file`-only
dry-run shows `[1/3]…[3/3]`, not gaps). The order is always the same.

**Read-only steps (always run, in both dry-run and apply):**

| Step | Name                                   | Active when |
|------|----------------------------------------|-------------|
| 1    | Connecting to source and destination   | always      |
| 2    | Analyzing the SOURCE mailboxes (~/mail) | `--mail`    |
| 3    | Comparing mailboxes SRC ↔ DEST          | `--mail`    |
| 4    | Analyzing the SOURCE web files          | `--file`    |
| 5    | Comparing web files SRC ↔ DEST          | `--file`    |
| 6    | Analyzing the SOURCE databases          | `--db`      |
| 7    | Comparing databases SRC ↔ DEST          | `--db`      |

**Write steps (only with `--apply`):**

| Step | Name                                   | Writes? | Active when                |
|------|----------------------------------------|---------|----------------------------|
| 8    | Creating missing destination domains   | **DEST**| `--mail` or `--file` or `--db` |
| 9    | Migrating active mailboxes              | **DEST**| `--mail`                   |
| 10   | Verifying mailbox integrity            | no      | `--mail`                   |
| 11   | Copying website files                  | **DEST**| `--file`                   |
| 12   | Verifying website files                | no      | `--file`                   |
| 13   | Migrating databases                    | **DEST**| `--db`                     |
| 14   | Verifying databases                    | no      | `--db`                     |

**Apply execution order:** connect → (read-only analyses/compares) → **create
domains** (once, shared) → mail (migrate + verify) → files (copy + verify) →
**databases (migrate + verify)**. Databases come *after* files on purpose: the
files carry each site's config (e.g. `wp-config.php`), which the database step
then rewrites to point at the new database name.

Examples of the counter:

```text
no flags, dry-run     → [1/7]   (connect + mail×2 + file×2 + db×2)
no flags, --apply     → [1/14]  (the 7 above + domains + mail×2 + file×2 + db×2)
--mail, dry-run       → [1/3]
--db, --apply         → [1/5]   (connect + db×2 + domains + db migrate/verify)
```

---

## 4. DRY-RUN mode (default)

```sh
./cpanel-self-migration
# or, explicitly:
./cpanel-self-migration --dry-run
```

This is the **default** mode and **writes nothing** to either server. It runs
the read-only steps for the selected flows:

- **Mail** (`--mail`): analyzes `~/mail` (domains, mailboxes, `ACTIVE`/`ORPHAN`,
  password scheme → `logs/mail_analysis.log`), then compares SRC ↔ DEST per
  mailbox:
  - `= IDENTICAL` (green) — same message count and same UIDVALIDITY;
  - `~ DIFFERS` (red) — present on both but different counts;
  - `+ TO MIGRATE` (yellow) — missing or empty on DEST.
- **Files** (`--file`): reads each document root's size + file count on the
  SOURCE (read-only) → `logs/web_analysis.log`, and shows the
  `src → dest` docroot mapping with sizes (`ready` / `empty` / `absent`).
- **Databases** (`--db`): lists the SOURCE databases and their owners, maps each
  name to its destination-prefixed name, classifies it (`linked` / `shared` /
  `orphan`) → `logs/db_analysis.log`, and reports which already exist on DEST.

The write steps do not run. **Always run a dry-run before an `--apply`** to see
exactly what would change.

---

## 5. The `--apply` command

```sh
./cpanel-self-migration --apply              # everything
./cpanel-self-migration --apply --file       # only website files
```

Performs the real migration: it **writes to DEST** (never to SRC). After the
read-only steps it runs, for the selected flows:

- **Create missing domains** (shared) — SRC domains absent on DEST are created
  preserving their type (addon / subdomain), via a **temporary API token** that
  is **always revoked** at the end, even on error or interruption.
- **Mail** — see [section 6](#6-mail-flow-details).
- **Files** — see [section 7](#7-website-file-flow-details).
- **Databases** — see [section 8](#8-database-flow-details).

Per-item outcomes are written to `logs/migration_report.log` (one shared report
for all flows).

**Idempotency:** re-running `--apply` is safe and is the intended way to do a
**final sync** shortly before cutover (mailboxes already aligned are skipped;
databases and files are re-migrated as configured — see each flow below).

---

## 6. Mail flow details

For each active mailbox, idempotently:

1. create/update the account on DEST, preserving the `$6$` password hash;
2. **fast-skip**: if the mailbox is already aligned (same message count **and**
   same UIDVALIDITY), it is **skipped** without re-copying;
3. otherwise copy the maildir via **tar streaming** (`SRC tar -c | relay | DEST
   tar -x`), with a live **progress bar** (percentage, bytes, batch). Large
   maildirs are split into 500 MB batches with automatic retries.

IMAP folders whose names contain **spaces** (e.g. `Sent Items`, `Junk E-mail`)
are migrated correctly, and the Dovecot `dovecot-uidlist` (which carries the
UIDVALIDITY) is preserved even for large mailboxes split across several batches —
so IMAP clients keep their state instead of re-downloading everything.

Then an **integrity check** re-compares SRC ↔ DEST **per folder** (the INBOX root
and each `.Subfolder`, on message count + UIDVALIDITY) and reports `consistent` /
`divergent` per mailbox. Comparing each folder separately means a shortfall in one
folder offset by a surplus in another can no longer net to zero and pass. A mailbox
that fails to copy, or that is still divergent here, makes the run exit non-zero
(see [section 16](#16-exit-codes)).

See also [`--full`](#9-the---full----force-sync-flag) and
[`--verify-checksums`](#10-the---verify-checksums-and---deep-verify-flags), which tune the
fast-skip, and [`--deep-verify`](#deep-verification---deep-verify), which hashes every
message body to catch same-name corruption.

---

## 7. Website-file flow details

> **Semantics: MIGRATION, not sync.** Before copying a docroot, the destination
> docroot is **emptied** so it ends up an exact mirror of the source. (Exception:
> an **empty source** never wipes the destination — its existing content is backed
> up aside instead; see the empty-source note below.)

- Document roots are read from the authoritative cPanel API
  (`DomainInfo::domains_data`) on **both** sides — paths are never guessed,
  because the two accounts can lay docroots out differently (e.g. addons in
  dedicated home dirs on one side vs under `public_html/` on the other).
- The destination docroot is emptied **once**, within a **hard safety guard**
  that refuses to touch `~/public_html` itself, any path containing `..`, any
  symlink-resolved path outside `~/public_html/`, or duplicate resolved
  destination targets. The guard resolves `$HOME` and the docroot on the DEST
  shell before mutation.
- The cPanel system entries `cgi-bin` and `.ftpquota` are **never** copied or
  deleted. `.well-known` is user-served site content and is copied/deleted like
  other user content.
- Files are streamed via the same **tar bridge** (`SRC tar -c | relay | DEST
  tar -x`), in 500 MB batches with retries, preserving **empty directories**.
  Files and directories whose names contain **spaces** (or other unusual
  characters, e.g. an upload named `my photo.jpg`) are copied correctly.
- A **source docroot that is empty** (no regular files) never **wipes** the
  destination. Any existing destination content is **renamed aside** to
  `<docroot>-bak[.N]` and a fresh empty docroot is left — the destination is
  mirrored to the empty source **without losing data** (recover from the `-bak`
  directory if the empty source was a transient bad read). A destination docroot
  that resolves to the account web root (`public_html` itself) is a hard guard
  failure and is not mutated.
- **Verification** re-reads each copied docroot on DEST and compares file count
  + bytes against the source. A docroot that fails to copy, or that is still
  divergent here, makes the run exit non-zero (see [section 16](#16-exit-codes)).

---

## 8. Database flow details

Migrates MySQL databases as a **migration** (the destination database is created
fresh and loaded from the source), handling the cPanel account-prefix rename.

**Dump (SOURCE, read-only).** Each database is dumped with
`mysqldump --no-tablespaces --single-transaction --quick --routines --events`
authenticated as the cPanel account user. `--single-transaction` takes a
consistent snapshot with **no locks and no writes** on the source;
`--no-tablespaces` is required for a non-root cPanel user; `--routines` and
`--events` make sure stored procedures/functions and scheduled events come across
too (both are OFF by default in mysqldump — triggers are already included). The
dump streams straight into the destination (`SRC mysqldump | relay | DEST mysql`)
— nothing is spilled to local disk.

On import, the `DEFINER=` clause mysqldump attaches to each
routine/trigger/event/view — naming the *source* MySQL user, which does not exist
on the destination — is **stripped**, so the non-root destination user can create
those objects without an `ERROR 1227 … SUPER` failure. The strip touches only
mysqldump's own version-comment lines, never row data.

**Prefix remap.** cPanel prefixes database and user names with the account name.
The tool maps **both** the database name and its user from the SOURCE prefix to
the DESTINATION prefix, e.g.:

| Source                | Destination           |
|-----------------------|-----------------------|
| `srcacct_wp694`       | `destacct_wp694`      |
| user `srcacct_u1`     | user `destacct_u1`    |

**On the destination** it creates the user, the database, and the grant, then
imports the data. (On a destination running MariaDB the account user often has
no direct MySQL login, so import/verify authenticate as the **per-database user**
the tool just created.)

**Credential discovery (read-only, layered).** To reuse each site's existing DB
password and to know which config files to rewrite, the tool looks, in order:

1. the **Softaculous registry** (`~/.softaculous/installations.php`) — a
   CMS-agnostic list of every installed app and its DB credentials (it even
   retains entries whose site files were later removed);
2. each site's **config file** under its docroot. Recognized apps (selected by
   file *content*, since many share `config.php`): WordPress, Joomla, Drupal,
   Concrete CMS, TYPO3, PrestaShop, OpenCart, AbanteCart, CubeCart, Magento,
   phpBB, MyBB, SMF, MediaWiki, Moodle, Chamilo, Dolibarr, SuiteCRM, MantisBT,
   Piwigo, Coppermine, Nextcloud, LimeSurvey, Matomo, Laravel (`.env`);
3. a **database-name grep** across the docroot files for any database still
   missing a password;
4. the optional [`databases:` config section](#13-optional-databases-config-section).

**Config rewrite (DESTINATION only).** After the data is loaded, the config file
that referenced the database is updated on the destination to point at the new
prefixed name/user/password (preserving table prefix, salts, and all other
settings). A database shared by several installs has **all** of its configs
handled. WordPress (`wp-config.php`), Joomla (`configuration.php`), Drupal
(`settings.php`), Moodle (`config.php`), Magento (`app/etc/env.php`), PrestaShop,
OpenCart/AbanteCart and Laravel (`.env`) are rewritten automatically; for the other
recognized CMSes the rewrite is not yet automated, so the run prints a clear
**MANUAL** line naming the config file and the destination database/user to set by
hand — the data has migrated, only that file still points at the old name.

After each rewrite the destination config is re-read and the cutover is checked two
ways: the value/host check (the planned name/user/password landed AND the DB host is
local), and — for `define()`-based configs (WordPress, PrestaShop, OpenCart) — a
PHP-free structural check that the rewrite acted on the `define()` PHP actually uses.
When the value check fails (e.g. a never-rewritten remote DB host) the config is a hard
**MANUAL** failure. When the structural check cannot *prove* the cutover (the constant is
`define()`d more than once, the live definition is a non-literal expression, or a
heredoc/inline-HTML/computed-name decoy may shadow it) the config is reported
**`[db config UNVERIFIED]`** — a soft note at the default tier (the data migrated and the
value/host check passed) and a **hard failure under `--deep-verify`**, never a silent clean
pass. A config whose DB constant lives in a separate `include`d file is beyond this
structural check and still needs a manual confirmation.

**Orphan databases** (no site references them) are still migrated; if a password
is found (e.g. via Softaculous) it is reused, otherwise a strong one is
generated. There is no config to rewrite for them.

**Verification** compares, SRC vs DEST, the base-table count **and** the counts
of the non-table objects (stored routines, events, triggers, views), so an object
that failed to import is reported rather than silently lost.

> **Scheduled events need the event scheduler.** Events are imported, but MySQL
> only *runs* them when the server's global `event_scheduler` is `ON` — a setting
> a non-root cPanel user cannot change. If a migrated database relies on events,
> ask the destination host to enable it (`SET GLOBAL event_scheduler = ON;`, or
> the `event_scheduler=ON` line in `my.cnf`). The tool prints a reminder whenever
> it migrates any events.

---

## 9. The `--full` / `--force-sync` flag

```sh
./cpanel-self-migration --apply --full
./cpanel-self-migration --apply --force-sync   # identical alias
```

Works **with `--apply`**, **mail flow only**. It disables the mailbox
**fast-skip**: every mailbox is re-synced even if it already looks aligned.

- **When:** if you suspect the count+UIDVALIDITY comparison is unreliable for a
  mailbox and want to force the tar pass (still idempotent — already-present
  messages are not overwritten).
- **Cost:** much slower (re-streams every maildir).
- On its own (without `--apply`) it has no effect. It does **not** affect the
  file or database flows.

---

## 10. The `--verify-checksums` and `--deep-verify` flags

```sh
./cpanel-self-migration --apply --verify-checksums
```

Makes the mailbox **fast-skip stricter** (**mail flow only**). When SRC and DEST
have the same message count and UIDVALIDITY, it additionally compares the
**exact set of message IDs** before skipping:

- IDs match → safe skip;
- IDs differ (same count, different messages) → it copies instead of skipping.

- **When:** maximum precision for the "same count but different content" edge
  case.
- **Cost:** one extra file-list read per side, only on mailboxes that *would*
  have been skipped — much cheaper than `--full`.

`--full` and `--verify-checksums` are independent: `--full` ignores the
fast-skip entirely; `--verify-checksums` makes it more precise.

### Deep verification (`--deep-verify`)

```sh
./cpanel-self-migration --apply --deep-verify
```

By default the post-`--apply` integrity checks compare **metadata** (fast and
default-on):

- **mail** — per-folder message count + UIDVALIDITY, **plus a per-mailbox message-body
  check** (sha256 of every message body, keyed by stable folder-aware identity so a
  flag change or a `new/`->`cur/` move is not a false diff). So a same-count body
  corruption or a cross-folder swap is caught at the default tier and fails the run as
  `CONTENT`. The default rolls the result to one mailbox verdict (run `--deep-verify`
  to name the diverging message(s)). A body that cannot be hashed (`sha256sum` missing,
  an unreadable message, or a mailbox above the content-check cap) is reported as **not
  byte-verified** (a soft note; count + UIDVALIDITY still verified), never a green
  content-OK; a DEST AHEAD mailbox's bodies stay `--deep-verify`'s job;
- **web files** — a per-path manifest (relpath + size + type + symlink target +
  mode), which also surfaces a symlink that failed to copy, **plus a streaming tree
  content fingerprint** (one sha256 folding every file body and symlink target on each
  side). So a same-name/same-size **content** corruption (a transfer that wrote the
  right number of bytes wrongly) is caught even at the default tier and fails the run
  as a `DIFF`. The fingerprint is *tree*-granular: it proves the docroot diverges but
  does not name the file (run `--deep-verify` to localize it). If the host lacks
  `sha256sum`/`sort -z`, a file body is unreadable, or the docroot exceeds the
  content-hash size cap, the content is reported as **not byte-verified** (a soft note;
  metadata still verified), never a green content-OK. A docroot too large for a per-path
  manifest (above the entry cap) is verified the same way — by the streaming tree content
  fingerprint layered on a count+bytes+namelist match — so a body corruption is caught
  above the cap too; only above the *byte* cap does it drop to a names/sizes/types soft
  note. A docroot whose source contains
  paths the tool cannot represent (a tab, control byte, or `..` in the filename) is
  reported `UNVERIFIED`, not `OK` (re-running will not help; the source path must be
  renamed);
- **databases** — table/object **sets** (not only counts) + charset/collation
  (the charset/collation comparison catches the classic *mojibake* import, where
  data dumped as `utf8mb4` lands as `latin1`). If the charset/collation cannot be
  read on either side, the database is reported `UNVERIFIED`, not `OK`.

`--deep-verify` upgrades these to **content** verification — it reads every byte
on both sides, so it is opt-in and slower:

- **web files** — a **sha256 per file** (the per-path version of the default tree
  fingerprint), which additionally names the exact diverging file(s);
- **mail** — the same **sha256 per message body** as the default check, but it NAMES
  each diverging message (corrupted or missing) instead of one mailbox verdict, and an
  unreadable body becomes a hard `UNVERIFIED` rather than a soft note;
- **databases** — exact table-set comparison, **exact per-table row counts**, and,
  when both servers run the *same* version, a `CHECKSUM TABLE` content hash. Missing
  or extra tables, row-count drift, and checksum drift are hard failures; if the
  requested content check cannot prove equality, the database is reported as
  `UNVERIFIED`, not `OK` — including a base table whose `CHECKSUM TABLE` returns
  `NULL` (e.g. a corrupt, locked, or otherwise unhashable table), which is never
  passed silently. `AUTO_INCREMENT` drift remains informational.

A database above an internal size cap, or a docroot above the **byte** cap, falls back
to the metadata check with a `DEEP-SKIPPED`/soft note (never silent). A docroot above the
**entry** (per-path manifest) cap still gets the streaming tree **content** fingerprint —
it loses only per-file localization, not body verification. `--verify-checksums` implies
the deep **mail** content check too.

---

## 11. The `--apply-mirror` flag

```sh
./cpanel-self-migration --apply-mirror          # all flows; mail is mirrored
./cpanel-self-migration --apply-mirror --mail   # mail only
```

`--apply-mirror` runs the same write phase as `--apply` (it **implies** it; the
two are not combined), but it changes the **mail flow** to make each destination
mailbox an **EXACT mirror** of the source instead of merging into it:

- the destination mailbox `~/mail/<domain>/<user>` is **renamed aside** to the
  first free `<user>-bak[.N]` (within a hard path guard), then re-created and
  **fully re-copied** from the source;
- so mail that exists **only on the destination** (e.g. a `Trash` folder, or
  messages deleted on the source) is moved out of the live mailbox — exactly the
  **DEST AHEAD** divergence that a normal `--apply` reports but cannot remove
  (see [section 5](#5-the---apply-command)).

The old mail is **recoverable**: it stays in `<user>-bak[.N]` on the destination
until you delete it. Files and databases are unaffected — under `--apply-mirror`
they behave exactly as under `--apply`.

- **When:** to deliberately reset a mailbox to match the source (a botched or
  half-synced mailbox), or to clear a reported DEST AHEAD.
- **Cost:** a full re-copy of every selected mailbox (no fast-skip, no delta),
  plus a `-bak` copy left on the destination per run — clean those up afterwards.
- **Safety:** the SOURCE stays read-only; the rename and re-copy touch the DEST
  only. The tool prints a prominent warning at the start of an `--apply-mirror`
  run. `--apply-mirror` is mutually exclusive with `--dry-run`. The tool records
  the source mailbox's message count **before** renaming the live destination
  aside and re-checks the destination **after** the copy: if a mailbox proven to
  hold mail is emptied and the copy brings **nothing** back (the source was
  removed or fully emptied around mirror time), that mailbox is **FAILED**
  (non-zero exit) rather than reported as a clean sync — the prior live mail is
  preserved in `<user>-bak`, so investigate and recover from there.

> ⚠️ **Do NOT use `--apply-mirror` after switching the MX to the new server.**
> Once the destination is receiving live mail, a mirror run would move that
> freshly-delivered mail aside to `-bak` (it is recoverable there, but it
> disappears from the user's live mailbox). For the normal final sync before
> cutover, use plain `--apply --mail` (see
> [section 17](#17-recommended-cutover-procedure)).
>
> Note: mailboxes/accounts that exist **only** on the destination are not touched
> — `--apply-mirror` mirrors the *source's* mailboxes, it does not delete extra
> destination accounts.

---

## 12. Other flags

| Flag                 | Effect                                                   |
|----------------------|----------------------------------------------------------|
| `--mail`             | Migrate mail only (see [section 2](#2-what-gets-migrated---mail----file----db)). |
| `--file`             | Migrate website files only.                              |
| `--db`               | Migrate databases only.                                  |
| `--domain DOMAIN`    | Narrow the run to one domain (docroot + mail, never DB); see [section 2](#2-what-gets-migrated---mail----file----db). |
| `--mailbox ADDR`     | Narrow the run to one mailbox `local@domain` (mail only). |
| `--apply`            | Perform the migration (writes to DEST).                  |
| `--apply-mirror`     | Like `--apply`, but MIRROR each mailbox (see [section 11](#11-the---apply-mirror-flag)). |
| `--dry-run`          | Explicit dry-run (the default).                          |
| `--full` / `--force-sync` | Force re-sync of every mailbox (with `--apply`).    |
| `--verify-checksums` | Stricter mailbox fast-skip (with `--apply`); also enables deep mail content verify. |
| `--deep-verify`      | Content-hash verification (sha256 per file/message, exact DB row counts); see [section 10](#10-the---verify-checksums-and---deep-verify-flags). |
| `--config PATH`      | Path to `host.yaml` (default: `configs/host.yaml`).      |
| `--log-level LEVEL`  | Verbosity: `info` (default) or `debug`.                  |
| `--version`          | Print the binary version and exit.                       |
| `-h` / `--help`      | Show the short help and exit.                            |

**Constraints:** `--apply` and `--dry-run` are mutually exclusive (error if used
together). The `--mail` / `--file` / `--db` selectors can be combined freely.
`--domain` narrows to one domain (compose with `--mail`/`--file`; rejected with
`--db`); `--mailbox` narrows to one mailbox (mail only; rejected with
`--file`/`--db`/`--domain`). Both `--domain` and `--mailbox` require a configured
destination.

### `--log-level debug`

```sh
./cpanel-self-migration --apply --log-level debug 2> debug.txt
```

Turns on verbose diagnostics, printed to **stderr** (so they never corrupt the
log files on stdout). Debug traces include:

- every SSH session opened/closed, with the live count of concurrent sessions
  per host (useful to spot server `MaxSessions` saturation);
- per-mailbox / per-docroot transfer details (file counts, computed delta, each
  batch attempt and its outcome);
- database discovery (which config/registry each credential came from — never
  the password itself) and the dump/import bridge;
- network errors (e.g. connection resets) at the exact point they occur.

For deeper, opt-in diagnostics — including the redacted **raw UAPI response**
debug (`CPSM_DEBUG_RAW_UAPI`) used to inspect how a specific cPanel build shapes
its responses — see [DEBUGGING.md](DEBUGGING.md).

---

## 13. Optional `databases:` config section

In the common case you do **not** need this. The tool discovers DB credentials
automatically (account user can dump everything; site configs / Softaculous
supply per-site passwords to reuse). Add this section only when that is
insufficient — for example an **orphan** database (no config anywhere) whose
**data** you still want migrated, or to force a specific user/password.

```yaml
databases:
  - name: "srcuser_orphandb"     # SOURCE database name
    user: "srcuser_someuser"     # optional: MySQL user to associate
    password: "the_db_password"  # optional: reused on the destination
```

Entries are keyed by the **source** database name and override/fill the
automatically-discovered values.

---

## 14. Generated artifacts

Log files are written under a **`logs/`** directory (gitignored):

| File                        | When                | Contents                                        |
|-----------------------------|---------------------|-------------------------------------------------|
| `logs/mail_analysis.log`    | with `--mail`       | SRC mail analysis: domains, mailboxes, ACTIVE/ORPHAN, scheme. |
| `logs/web_analysis.log`     | with `--file`       | SRC docroot analysis: per-domain path, size, file count, status. |
| `logs/db_analysis.log`      | with `--db`         | SRC database analysis: name remap, owner, link state, sizes. |
| `logs/migration_report.log` | only with `--apply` | Per-item outcome (mailboxes, docroots, databases) + integrity checks. |

The live SRC ↔ DEST comparisons are printed to the screen; the analysis `*.log`
files are the persistent record.

---

## 15. Interruption (Ctrl-C)

During an `--apply` you can interrupt at any time with **Ctrl-C**:

- The tool stops **immediately**, even mid-way through a multi-gigabyte copy or a
  database dump (it closes the SSH sessions and unblocks the in-flight transfer).
- It reports how many items were processed and **does not run** the final
  integrity check for the interrupted flow.
- A **second Ctrl-C** force-quits immediately, in case something is still stuck.
- The SRC is never touched anyway (only reads: `tar -c`, `mysqldump
  --single-transaction`, `ls`/`cat`/`find`/`uapi --output`).
- A half-copied mailbox simply has *fewer* files; a later `--apply` resumes it.
  A half-loaded database should be re-run with `--apply --db` (the import
  recreates it).

---

## 16. Exit codes

| Code  | Meaning                                                |
|-------|--------------------------------------------------------|
| `0`   | Success — everything selected migrated **and verified clean**. |
| `1`   | A hard error, **or** an `--apply` that finished with failures (see below). |
| `2`   | Flag misuse (e.g. `--apply` together with `--dry-run`).|
| `130` | Interrupted by the user (Ctrl-C).                      |

`1` covers both a hard error (invalid config, SSH/cPanel failure) **and** an
`--apply` that ran to completion but did **not** fully succeed: one or more
mailboxes/docroots/databases failed to migrate, a domain failed to create (so its
dependent data was skipped), or the post-migration verification still found a
**real** divergence (a mailbox missing messages or with a mismatched UIDVALIDITY,
a docroot with a different file count, a database with a differing table/object
count). So a `0` exit can be trusted by automation as "fully migrated and
verified". The per-item detail is on screen and in `logs/migration_report.log`.
A benign **DEST AHEAD** (extra mail that exists only on the destination, e.g.
Trash) is reported but does **not** force a non-zero exit, since a normal
re-run cannot remove it. To deliberately remove it and make the mailbox an exact
copy of the source, use [`--apply-mirror`](#11-the---apply-mirror-flag) (but not
after the MX has been switched to the new server).

---

## 17. Recommended cutover procedure

1. **Dry-run** to see the full state (mail, files, databases):
   ```sh
   ./cpanel-self-migration
   ```
2. **First full migration**:
   ```sh
   ./cpanel-self-migration --apply
   ```
   (or migrate one kind at a time the first time, e.g. `--apply --file`, then
   `--apply --db`, to review each in isolation.)
3. (Optional) **strict mail verification** if you have doubts about content:
   ```sh
   ./cpanel-self-migration --apply --mail --verify-checksums
   ```
4. Shortly **before switching the MX/DNS**, run a **final delta sync** for mail
   (unchanged mailboxes are skipped):
   ```sh
   ./cpanel-self-migration --apply --mail
   ```
5. Switch MX / DNS / DKIM (manual step, outside this tool).

> Tip: always run the dry-run first and read the on-screen comparison plus the
> `logs/*_analysis.log` files to confirm what `--apply` would do.

> The final sync (step 4) uses plain `--apply --mail` on purpose: it **merges**
> into the destination and never removes mail. Use
> [`--apply-mirror`](#11-the---apply-mirror-flag) only for a deliberate reset
> **before** step 5 (e.g. to clear a DEST AHEAD or a botched mailbox), **never
> after** the MX switch — once the new server is receiving live mail, a mirror
> run would move that mail aside to `-bak`.

## 18. Security notes (secret handling)

The tool handles two kinds of secret on the destination host: **MySQL user
passwords** (created/aligned so the rewritten CMS config can connect) and
**mailbox password hashes** (replicated so existing mail clients keep working).
How they are transported is designed to keep them out of places other local users
could read:

- **Over SSH** — every secret travels out-of-band: as an SSH **environment
  variable**, or (if the server rejects `Setenv`) as a single-quote-escaped
  `export` line read from the script's **stdin**. Either way it is never on the
  remote command line / argv.
- **`mysqldump` / `mysql`** — the DB password is passed via `MYSQL_PWD` (the
  client reads it from the environment), so it is never on those tools' argv.
- **Shadow-file rewrite** — the mailbox hash is read by `awk` from `ENVIRON`, not
  from a `-v` argument, so it is never on `awk`'s argv. The rewritten shadow temp
  file is created `umask 077` (owner-only).
- **Addon-domain creation** — uses a short-lived, immediately-revoked API token
  read by `curl` from `--config -` (stdin), so the token is never on `curl`'s argv.

**Residual exposure (by design, bounded).** A few writes must go through the
`uapi` command-line tool — `Mysql::create_user` / `Mysql::set_password` (MySQL
password) and the `Email::add_pop` create fallback (mailbox `password_hash`). The
`uapi` CLI accepts parameters **only on its command line** (it has no stdin /
`--input` mode), so for the duration of each such call the value is visible in
`/proc/<uapi-pid>/cmdline` on the destination host. This is bounded by:

- the **short window** (one UAPI call), and
- **`/proc` isolation** — a standard cPanel host runs `hidepid`/CageFS, so a
  co-tenant account cannot see another account's processes at all; the entry is
  readable only by the same account (which already owns these credentials) and by
  `root` (the host administrator).

There is no argv-free way to pass a parameter through the `uapi` CLI. If you are
migrating onto a host **without** `/proc` isolation **and** with untrusted local
co-tenants, treat these credentials as potentially observable during the apply
window and rotate the affected MySQL password afterward.
