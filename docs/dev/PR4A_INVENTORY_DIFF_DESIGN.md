# PR 4A — Inventory diff (read-only)

## Scope

Deterministic comparison of two already-produced inventory JSON files.
No server connection, no migration, no judgment: the diff says WHAT
differs, never whether that is safe or dangerous (policy engine is a
later, separate step).

## CLI

```
cpanel-self-migration inventory diff \
  --source ./inventory_source.json \
  --destination ./inventory_destination.json \
  [--output-json ./inventory_diff.json] \
  [--output-md ./inventory_diff.md]
```

Subcommand dispatch happens at the top of main() before the global flag
parsing (3 lines); everything else lives in new files.

Exit codes: `0` diff generated (with or without differences); `1`
missing/unreadable/invalid input; `2` flag usage error (tool
convention).

## Direction

`source → destination`: **removed** = present only in source,
**added** = present only in destination.

## Matching keys and compared fields

| Section        | Identity key                        | Changed-fields |
|----------------|-------------------------------------|----------------|
| domains        | name                                | type, document_root |
| mailboxes      | email                               | — (existence only; disk usage is volatile noise) |
| databases      | name                                | users (sorted set) |
| forwarders     | domain \| source \| destination     | — (key IS the content) |
| autoresponders | domain \| email                     | subject, interval |
| ftp            | login                               | type, dir |
| ssl            | domains (SAN list)                  | issuer, validation_type, is_self_signed, valid_until |
| php            | domain                              | version |
| dns            | zone, then (type \| name)           | canonical value-set (see below) |
| cron           | command_sha256                      | schedule/macro, enabled |

## DNS canonicalization

Records are grouped per zone by `(type, name)`. Each group's values are
canonicalized as `value ttl=N` (plus `prio=N` for MX), sorted — record
ORDER never produces a diff. Same `(type, name)` with a different
value-set → **changed** (source/destination show both sets). A
`(type, name)` present on one side only → added/removed. Zones
unavailable on either side → warning, records skipped (an unavailable
zone is not an empty zone).

## Cron

Jobs are grouped by `command_sha256` (never the raw command — which the
inventory does not even contain). One job per side with the same hash →
schedule/enabled compared as **changed**; multiple jobs per hash →
multiset of `sha|schedule` compared as added/removed. Markdown shows
only `command_redacted`.

## Unavailable / missing sections

ConfigSection-based sections (ftp/ssl/php/dns/cron) with
`available:false` on either side → per-section warning, comparison
skipped. A file missing a section entirely (older inventory) behaves
the same via the zero value. A file without `account.user`/`account.host`
is rejected as not-an-inventory (exit 1).

## Determinism

All lists sorted by key (then field). `generated_at` is the only
non-deterministic field and is set by the CLI, not the engine.

## Out of scope

Policy engine, risk scoring, safe/blocked verdicts, migration advice,
import/apply/fix, server connections, TOTP/Orbit.
