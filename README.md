<div align="center">

# cPanel_self-migration
Simple cPanel-to-cPanel hosting migration tool

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go](https://img.shields.io/badge/Go-1.25+-success.svg?logo=go)](https://go.dev/)
[![codecov](https://codecov.io/gh/tis24dev/cPanel_self-migration/branch/dev/graph/badge.svg?token=ZVBLmmYNsl)](https://codecov.io/gh/tis24dev/cPanel_self-migration)
[![Go Report Card](https://goreportcard.com/badge/github.com/tis24dev/cPanel_self-migration)](https://goreportcard.com/report/github.com/tis24dev/cPanel_self-migration)
[![GoSec](https://img.shields.io/github/actions/workflow/status/tis24dev/cPanel_self-migration/security-ultimate.yml?label=GoSec&logo=go)](https://github.com/tis24dev/cPanel_self-migration/actions/workflows/security-ultimate.yml)
[![CodeQL](https://img.shields.io/github/actions/workflow/status/tis24dev/cPanel_self-migration/codeql.yml?label=CodeQL&logo=github)](https://github.com/tis24dev/cPanel_self-migration/actions/workflows/codeql.yml)
[![Dependabot](https://img.shields.io/badge/Dependabot-enabled-success?logo=dependabot)](https://github.com/tis24dev/cPanel_self-migration/network/updates)
[![cPanel](https://img.shields.io/badge/cPanel-to--cPanel-FF6C2C.svg?logo=cpanel&logoColor=white)](https://cpanel.net/)
[![💖 Sponsor](https://img.shields.io/badge/Sponsor-GitHub%20Sponsors-pink?logo=github)](https://github.com/sponsors/tis24dev)
[![☕ Buy Me a Coffee](https://img.shields.io/badge/Buy%20Me%20a%20Coffee-tis24dev-yellow?logo=buymeacoffee)](https://github.com/sponsors/tis24dev)
[![💸 Donate](https://img.shields.io/badge/Donate-PayPal-blue?logo=paypal)](https://paypal.me/DNoventa)
</div>


Migrates a cPanel account — **email mailboxes, website files, and MySQL
databases**, plus the domains they need — between two cPanel accounts using only
user-level SSH (no root/WHM). **The SOURCE host is always read-only; all writes
target the DESTINATION.** This machine acts as a bridge: SRC → (Go pipe) → DEST.

## Installation

```sh
curl -fsSL https://raw.githubusercontent.com/tis24dev/cPanel_self-migration/main/install.sh | bash
```

🔒 **No root/sudo required.** Installs the latest **signed** release (ECDSA P-256 + SHA256 verified) under your user prefix (`~/.local` by default), and symlinks it into `~/.local/bin`; re-runs replace the binary but never touch your `configs/host.yaml`. (For a system-wide install: `PREFIX=/usr/local sudo bash install.sh`.) Setup & usage: **[docs/USAGE.md](docs/USAGE.md)**.

## Design highlights

- **Robustness**: cPanel JSON parsed with `encoding/json` + typed structs.
  Password hashes (`$6$…`) and other params are passed as SSH session
  environment variables, never interpolated into a command line.
- **Native SSH**: `golang.org/x/crypto/ssh`. Passwords live only in memory
  (never visible in `ps`). One reused connection per host. Host keys follow
  `accept-new` (unknown → trusted and recorded; changed → rejected).
- **Tests**: an extensive unit/golden test suite covering batching, domain
  mapping, account idempotency, JSON parsing, the plan/analysis renderers
  (golden, byte-for-byte against committed reference output), and a real local
  `tar`-bridge round-trip.
- **Maildir transfer = streaming tarball**: `tar -c` on the read-only source is
  piped through this process into `tar -x` on the destination — one continuous
  stream, no per-file round-trips, idempotent. Message files extract with
  `--keep-newer-files` (already-delivered mail is never clobbered); the Dovecot
  control files extract with `--overwrite` so the source's `dovecot-uidlist`
  always lands. Split into ≤500 MB batches with per-batch retries; `dovecot.index*`
  excluded, `dovecot-uidlist` preserved (UIDs/UIDVALIDITY survive).

## Usage

**Build from source:**

```sh
cp configs/host_template.yaml configs/host.yaml   # fill in real SRC/DEST credentials
make build                                         # -> ./cpanel-self-migration

./cpanel-self-migration                # dry-run: analyze + compare SRC/DEST (mail + files + databases)
./cpanel-self-migration --apply        # migrate everything: domains + mailboxes + website files + databases
./cpanel-self-migration --apply --db   # migrate only one kind (any of --mail / --file / --db; combine freely)
./cpanel-self-migration --apply --full --verify-checksums  # mail-only knobs: force re-sync / strict skip
./cpanel-self-migration --apply --deep-verify  # verify by CONTENT hash (sha256 per file/message, exact DB row counts) — slower, catches same-size corruption
./cpanel-self-migration --apply-mirror --mail  # MIRROR mail: dest = exact copy of src (dest-only mail -> <user>-bak; NOT after MX switch)
./cpanel-self-migration --config /path/to/host.yaml
./cpanel-self-migration --version      # print the build version
```

`host.yaml` is loaded from `configs/host.yaml` by default (next to the binary
or in the current directory); override with `--config`.

Default mode is **dry-run** and writes nothing to either server.

📖 Full command guide (dry-run, `--apply`, `--apply-mirror`, `--full`,
`--verify-checksums`, interruption, exit codes, cutover procedure):
**[docs/USAGE.md](docs/USAGE.md)**.

## Phases

1. **Connect** to source and destination.
2. **Analyze & compare (read-only)** the selected data SRC ↔ DEST — mailboxes
   (`~/mail`), website docroots, and MySQL databases — classifying each item as
   IDENTICAL / DIFFERS / TO MIGRATE → `logs/*_analysis.log`.
3. **--apply: create domains** — SRC domains missing on DEST are created as
   addon/subdomains via a temporary API token that is always revoked.
4. **--apply: migrate mailboxes** — idempotent account create/update preserving
   the password hash; fast-skip when count + UIDVALIDITY already match; maildir
   tar-streamed in batches. (`--apply-mirror` instead renames the dest mailbox
   aside to `<user>-bak` and re-copies in full, for an exact source mirror.)
5. **--apply: copy website files** — each docroot mirrored to DEST via the tar
   bridge (the destination docroot is emptied first, within a safety guard; an
   empty source backs the destination up aside instead of wiping it).
6. **--apply: migrate databases** — dumped and loaded via `mysqldump | mysql`,
   remapping the account prefix, then each site's config is rewritten to the new DB.
7. **--apply: verify** each flow (mailbox count + UIDVALIDITY, docroot file count
   + bytes, database table/object counts) → `logs/migration_report.log`.

Only the steps for the selected flows (`--mail` / `--file` / `--db`; all if none)
run. Full step list and order: **[docs/USAGE.md](docs/USAGE.md)**.

## Layout

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

## CI / CD (`.github/`)

GitHub Actions workflows:

- **codecov.yml** — tests + coverage upload (push to `main`/`dev` and PRs).
- **race.yml** — `go test -race` on every push/PR.
- **security-ultimate.yml** — Staticcheck, govulncheck, and GoSec (SARIF); also
  runs weekly.
- **codeql.yml** — GitHub CodeQL analysis for Go (push/PR on `main`/`dev` +
  weekly).
- **dependency-review.yml** — license/vulnerability gate on dependency PRs.
- **dependabot.yml** + **dependabot-automerge.yml** — weekly gomod/actions
  updates, auto-approve+merge for patch/minor, manual review for major.
- **release-intake.yml** / **release-guard.yml** / **post-merge-release.yml** —
  governed release flow: pushing a `vX.Y.Z` tag on `dev` HEAD opens a `dev→main`
  release PR; `release-guard` validates it; on squash-merge `post-merge-release`
  realigns `dev` onto the squash commit (lease-protected) and re-applies the tag
  there, which triggers `release.yml`. Needs a `RELEASE_BOT_TOKEN_CPANEL` PAT secret.
- **release.yml** + **.goreleaser.yml** — on a `vX.Y.Z` tag that is on `main`
  (gated): build, archives, SHA256SUMS, CycloneDX SBOM, GitHub release, and
  build-provenance attestation.
- **autotag.yml** — semantic auto-tagging from commit messages (disabled by
  default; remove `if: false` to enable).

## Tests

```sh
make test        # unit + golden + in-process integration (no network, no cPanel)
make test-race   # the same suite under the race detector
```

`make test` runs everything: pure-Go unit and golden tests plus **integration
tests** that drive the real SSH transport and the real remote shell commands
(`tar`/`find`/`mysqldump`/`awk`) against an **in-process SSH server** in a temp
`HOME` — no live cPanel host is needed. Those tests skip automatically where
`bash`, `tar`, or `mysql` is unavailable. CI runs the same suite (codecov for
coverage, a separate workflow under `-race`).

A golden test compares the `mail_analysis.log` output byte-for-byte against a
committed reference artifact (timestamp line normalized), guarding the
read/parse/render path against regressions.
