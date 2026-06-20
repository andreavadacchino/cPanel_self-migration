<div align="center">

# cPanel Self-Migration

### Move an entire cPanel account to a new host yourself, in one command.

**Email, websites, and databases, plus the domains they need.**
**No root. No WHM. The old host is never touched.**

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go](https://img.shields.io/badge/Go-1.25+-success.svg?logo=go)](https://go.dev/)
[![codecov](https://codecov.io/gh/tis24dev/cPanel_self-migration/graph/badge.svg?branch=main)](https://codecov.io/gh/tis24dev/cPanel_self-migration)
[![Go Report Card](https://goreportcard.com/badge/github.com/tis24dev/cPanel_self-migration)](https://goreportcard.com/report/github.com/tis24dev/cPanel_self-migration)
[![GoSec](https://img.shields.io/github/actions/workflow/status/tis24dev/cPanel_self-migration/security-ultimate.yml?label=GoSec&logo=go)](https://github.com/tis24dev/cPanel_self-migration/actions/workflows/security-ultimate.yml)
[![CodeQL](https://img.shields.io/github/actions/workflow/status/tis24dev/cPanel_self-migration/codeql.yml?label=CodeQL&logo=github)](https://github.com/tis24dev/cPanel_self-migration/actions/workflows/codeql.yml)
[![Dependabot](https://img.shields.io/badge/Dependabot-enabled-success?logo=dependabot)](https://github.com/tis24dev/cPanel_self-migration/network/updates)
[![cPanel](https://img.shields.io/badge/cPanel-to--cPanel-FF6C2C.svg?logo=cpanel&logoColor=white)](https://cpanel.net/)
[![Sponsor](https://img.shields.io/badge/Sponsor-GitHub%20Sponsors-pink?logo=github)](https://github.com/sponsors/tis24dev)
[![Buy Me a Coffee](https://img.shields.io/badge/Buy%20Me%20a%20Coffee-tis24dev-yellow?logo=buymeacoffee)](https://github.com/sponsors/tis24dev)
[![Donate](https://img.shields.io/badge/Donate-PayPal-blue?logo=paypal)](https://paypal.me/DNoventa)

</div>

---
[Quick start](#quick-start) · [Why](#why) · [Safety](#safety--trust) · [Docs](docs/USAGE.md)
---

## Why

Moving away from a cPanel host should not require root access, WHM access, a paid
migration ticket, or a weekend of manual `rsync`, mailbox repair, SQL dumps, and
DNS guesswork.

cPanel Self-Migration gives account owners, freelancers, and small hosting teams a
repeatable way to move an account with only normal cPanel SSH credentials. It
previews first, writes only to the destination, and verifies the result after the
copy.

| Built for | What it means |
|-----------|---------------|
| **Account owners** | Move your own cPanel account without waiting on support. |
| **Agencies** | Repeat the same migration process across many small client accounts. |
| **Host changes** | Copy mail, sites, databases, and missing domains into a new cPanel account. |
| **Low-risk cutovers** | Dry-run first, apply later, verify before DNS cutover. |

## What it migrates

| Area | What is handled |
|------|-----------------|
| **Email** | Mailboxes, folders, messages, password hashes, UIDs, and UIDVALIDITY. |
| **Websites** | Full docroots streamed from source to destination. |
| **Databases** | MySQL data, users, grants, and supported CMS config rewrites. |
| **Domains** | Missing addon domains and subdomains created on the destination. |

Run everything together, migrate only one area with `--mail`, `--file`, or
`--db`, or narrow to a single domain (`--domain`) or a single mailbox
(`--mailbox`).

> **Your old host is never at risk.** The SOURCE account is opened strictly
> **read-only**: the tool only reads from it and never writes to, deletes from, or
> modifies anything there. Every change is made on the DESTINATION, so even an
> interrupted or failed run cannot alter or endanger your source data.

## Built for safe migrations

- **Source is read-only.** The old host is only ever read from, never written to,
  deleted from, or modified, so your source data is never at risk; all writes go to
  the new host.
- **Dry-run by default.** Running `cpanel-self-migration` with no flags previews
  the move and writes nothing.
- **Designed for retries.** If a transfer is interrupted, run it again. Matching
  data is skipped and unfinished work continues.
- **Verified after copy.** Mailbox state, file trees, and database contents are
  checked after migration. Use `--deep-verify` for content-hash verification.
- **No server agent.** The tool runs from your machine and connects over SSH to
  each cPanel account.

## Quick start

**1. Install**

```sh
curl -fsSL https://raw.githubusercontent.com/tis24dev/cPanel_self-migration/main/install.sh | bash
```

The installer downloads the latest signed release under `~/.local` and adds it to
your `PATH`.

**2. Configure**

The installer already created your config from the template (as mode `600`), next
to the binary:

```text
~/.local/share/cPanel_self-migration/configs/host.yaml
```

Open it and fill in your source and destination cPanel SSH accounts:

```yaml
src:                       # SOURCE, read-only
  ip: "192.0.2.10"
  port: 22
  ssh_user: "your_cpanel_user"
  ssh_pass: "********"
  timeout: "10s"

dest:                      # DESTINATION, receives all writes
  ip: "198.51.100.20"
  port: 22
  ssh_user: "your_cpanel_user"
  ssh_pass: "********"
  timeout: "10s"
```

The binary finds this config next to itself, so you can run it from any directory.
(Building from source instead? Copy `configs/host_template.yaml` to
`configs/host.yaml` and `chmod 600` it.)

**3. Preview**

See what would move without changing either account:

```sh
cpanel-self-migration
```

**4. Apply**

When the preview looks right, migrate to the destination:

```sh
cpanel-self-migration --apply
```

For byte-level verification, run:

```sh
cpanel-self-migration --apply --deep-verify
```

## Safety & trust

The tool is built around a simple rule: the source account is never the write
target. The migration is a one-way flow from source to destination.

- **Signed releases.** Release artifacts are signed with ECDSA P-256 + SHA256 and
  ship with a CycloneDX SBOM.
- **Secret-aware logging.** Normal logs avoid secret values, and debug response
  logging redacts secret-shaped fields.
- **Host-key protection.** Unknown SSH host keys are accepted once and recorded;
  changed keys are rejected.
- **CI coverage.** The project runs unit tests, integration tests, race tests,
  CodeQL, govulncheck, GoSec, and dependency review in GitHub Actions.

## Common workflows

```sh
cpanel-self-migration --apply --mail
cpanel-self-migration --apply --file
cpanel-self-migration --apply --db
cpanel-self-migration --apply --domain example.com         # one domain (docroot + mail, no DB)
cpanel-self-migration --apply --mailbox user@example.com   # one mailbox (copy + verify)
cpanel-self-migration --apply --deep-verify
cpanel-self-migration --apply-mirror --mail
cpanel-self-migration --config /path/to/host.yaml
cpanel-self-migration --version
and more
```

Full flag reference: **[docs/COMMAND.md](docs/COMMAND.md)**.
Cutover procedure and exit codes: **[docs/USAGE.md](docs/USAGE.md)**.

---

<details>
<summary><b>What happens, step by step</b></summary>

<br>

1. **Connect** to source and destination.
2. **Analyze and compare** the selected data source-to-destination, classifying
   mailboxes, website docroots, and MySQL databases as
   IDENTICAL / DIFFERS / TO MIGRATE into `logs/*_analysis.log`.
3. **`--apply`: create domains.** SRC domains missing on DEST are created as
   addon / subdomains via a temporary API token that is always revoked.
4. **`--apply`: migrate mailboxes.** Idempotent account create/update preserving
   the password hash; fast-skip when count + UIDVALIDITY already match; maildir
   tar-streamed in batches. (`--apply-mirror` instead renames the dest mailbox
   aside to `<user>-bak` and re-copies in full, for an exact source mirror.)
5. **`--apply`: copy website files.** Each docroot mirrored to DEST via the tar
   bridge (the destination docroot is emptied first, within a safety guard; an
   empty source backs the destination up aside instead of wiping it).
6. **`--apply`: migrate databases.** Dumped and loaded via `mysqldump | mysql`,
   remapping the account prefix, then each site's config is rewritten to the new DB.
7. **`--apply`: verify** each flow (mailbox count + UIDVALIDITY, docroot file count
   + bytes, database table/object counts) into `logs/migration_report.log`.

Only the selected flows run. With none of `--mail`, `--file`, or `--db`, all flows
run. `--domain DOMAIN` narrows the run to a single domain (its docroot + mail,
never databases; compose with `--mail`/`--file`), and `--mailbox local@domain`
narrows it to a single mailbox (mail only).

</details>

<details>
<summary><b>Under the hood</b></summary>

<br>

- **Native SSH.** Built on `golang.org/x/crypto/ssh`; no daemon or agent is
  installed on either server.
- **Streaming transfers.** Web and mail data move through tar streams instead of
  per-file round trips.
- **cPanel-aware operations.** Domains, mail accounts, MySQL users, and API tokens
  are handled through cPanel user-level interfaces.
- **CMS config rewrite.** Supported site configs are rewritten to destination DB
  names, users, and passwords after database migration.
- **Reports.** Analysis and migration reports are written under `logs/`.

</details>

<details>
<summary><b>Build and test</b></summary>

<br>

Build from source:

```sh
cp configs/host_template.yaml configs/host.yaml
make build
```

Run the local test suite:

```sh
make test
make test-race
```

The tests include pure Go unit tests, golden report tests, and in-process SSH
integration tests. No live cPanel host is required.

Project layout:

```text
configs/            host.yaml (gitignored) + host_template.yaml
cmd/cpanel-self-migration   CLI: flags, signal context, wiring
internal/config     host.yaml loader (validated)
internal/sshx       native SSH: client, pool, accept-new host keys, streaming bridge
internal/cpanel     typed UAPI/api2 calls (RunUAPI[T]), domains/email/token/addon/mysql
internal/model      Domain/Mailbox types + pure mapping (ActionFor, HashScheme)
internal/maildir    mail: batch split, box stats, delta, tar-streaming transfer
internal/webfiles   website files: docroot gather, plan, tar-streaming transfer
internal/dbmig      databases: discovery, plan, mysqldump|mysql bridge, config rewrite
internal/wpconfig   wp-config.php parse/rewrite (DB credentials)
internal/phpserialize  minimal PHP unserialize (Softaculous registry)
internal/validate   permissive path/identifier sanity checks
internal/logx       progress logger (steps, colors, progress bar, debug)
internal/report     analysis + migration_report renderers (mail / web / db)
internal/migrate    orchestration of the migration phases
internal/version    build metadata (injected via ldflags at release)
```

</details>

<details>
<summary><b>CI/CD and releases</b></summary>

<br>

The repository uses GitHub Actions for tests, coverage, race testing, CodeQL,
govulncheck, GoSec, dependency review, Dependabot, and the governed release flow.
Releases are built with GoReleaser and published with checksums, SBOM, signature,
and provenance metadata.

</details>

---

## Documentation

- **[docs/USAGE.md](docs/USAGE.md)**: full setup, modes, cutover procedure, exit codes
- **[docs/COMMAND.md](docs/COMMAND.md)**: complete flag reference

## 💖 Support

If this tool saved you a migration headache, consider supporting its development


Released under the [MIT License](https://opensource.org/licenses/MIT).
