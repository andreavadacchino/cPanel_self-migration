# PR 6A — DNS import/verifier: micro-design

Design-only PR. No code changes. This document fixes the decisions for
the first WRITE capability of the tool and splits it into phases so that
everything except the final apply stays read-only and testable offline.

Revision note: v2 after an adversarial architecture review. The v1
policy-status gate was self-deadlocking (NS records always differ
between hosts → every real migration is `blocked` → the plan could
never be applied); v1 also allowed unmapped A/AAAA values to reach
production as "review-flagged" verbatim copies. Both are redesigned
below.

## Goal

After a migration, make the destination account's DNS zones serve the
records the source served — translated where they must differ (server
IPs) — with a plan that is reviewable before anything is written, a
verifier that proves what happened, and a rollback that restores the
touched rrsets in the master zone within 60 seconds.

## Phasing

| Phase | Deliverable | Risk | Network |
|---|---|---|---|
| 6B-pre | read-only capability captures on the real server (list below) | none | read-only SSH |
| 6B | `inventory dns-plan`: offline plan builder | none | none |
| 6C | `dns verify`: read-only zone comparison against a plan | low | read-only SSH |
| 6D | `dns apply`: gated write + backup + rollback | HIGH | writes |

The capability captures are a **hard prerequisite of 6B**, not of 6D:
the plan's TXT encoding, name canonicalization and the atomicity
contract all depend on their answers. 6B and 6C merge like any other
PR. 6D additionally requires the full protocol of the project CLAUDE.md
(risk classification, pre-backup, <60s rollback, Orbit documentation)
and a live session for approvals.

## Inputs: inventories, not the diff

The plan builder consumes the two **inventory JSON files**
(`inventory_source.json`, `inventory_destination.json`), NOT
`inventory_diff.json`. The diff's DNS entries are display strings
(`displaySet` joins values with `"; "`) — lossy by design, fine for
humans and policy, unusable for reconstructing records. The inventories
carry full `DNSRecordEntry` data (type, name, ttl, value, priority,
exchange, txtdata…).

**The policy report is context, not a gate.** `dns-plan` accepts
`--policy policy_report.json` and cross-references POL-DNS-* findings
into the plan so the reviewer sees why each op exists — but plan
generation never refuses on `overall_status`. Rationale (the v1
deadlock): every real migration is `blocked` by POL-DNS-NS-CHANGED,
because source and destination nameservers necessarily differ — and NS
is precisely a record class this tool refuses to touch. Every DNS
blocker is either *fixed by the plan* (MX missing/changed → `add`/
`replace`) or *out of the plan's reach* (NS, SOA, zone missing →
`manual`). Gating plan generation on a status those findings dominate
would make the tool unrunnable, while providing no safety the plan
semantics don't already provide. Safety lives in the plan actions and
in the 6D gates instead (see below). Non-DNS blockers (mailboxes,
databases…) concern other migration steps and are surfaced in the plan
markdown header as context, nothing more.

## What the plan contains

`dns_import_plan.json` (+ `.md` for humans), deterministic ordering.
One entry per **rrset** (`zone`, `type`, `name`) — the same grouping the
diff uses, but on **canonicalized** names (see below) — with an action:

| Action | Meaning |
|---|---|
| `add` | rrset exists on source, missing on destination |
| `replace` | rrset on both, translated values differ (destination rewritten) |
| `manual` | the tool refuses to touch it; reason recorded (see exclusions) |
| `skip` | identical after translation — nothing to do |

`manual` is a **terminal, in-plan refusal**: 6D never applies a
`manual` op, there is no override flag, and the plan markdown lists
them first. "Flagged for review but applied anyway" does not exist in
this design.

Explicitly **NOT** planned:

1. **`delete` of destination-only rrsets — never.** Additive/corrective
   posture only. Destination-only records are listed in the plan as
   `informational`; no destructive op is ever generated. (A migration
   must not remove records someone added on the destination on
   purpose.) The ONLY deletes the tool ever writes are those computed
   by a rollback to undo its own `add`s.
2. **SOA — never touched.** Server-managed.
3. **NS — never touched.** Delegation is registrar/WHM territory; NS
   differences appear as `manual`.
4. **Zone create/delete — never.** Plans are computed only for zones
   present AND available on both sides (`DNSZoneResult.Available`).
   Zones missing on the destination appear as `manual` ("create the
   zone via WHM/park first, then re-run"). Subdomains have no zone of
   their own (real-server fact #5) and never appear.
5. **`:RAW` and unknown record types** — `manual`. We only write types
   we can round-trip: A, AAAA, CNAME, MX, TXT.
6. **Same-name cross-type conflicts** — `manual`. A CNAME cannot
   coexist with any other type at the same owner name; because the tool
   never deletes, an `add` could otherwise create exactly that invalid
   state (source `CNAME www` + destination `A www`). Any planned op at
   an owner name where source and destination disagree on
   CNAME-vs-non-CNAME is forced `manual`, both directions.

### Name canonicalization

DNS names are case-insensitive, and zone data mixes relative names with
trailing-dot FQDNs; writing a relative name a server re-qualifies
(`mail.example.com` → `mail.example.com.example.com.`) is a classic
production-breaker. Before comparison AND before write, owner names and
CNAME/MX targets are canonicalized: lowercase, absolute FQDN with
trailing dot. The exact format `parse_zone` returns and `mass_edit_zone`
expects is capture item (f); the writer emits whatever the capture
proves correct, but the *comparison* is always canonical. Wildcard
owners (`*`) flow through as ordinary names (fixture required).

### TTL rule

Action selection compares **values only, never TTL**: a TTL-only
difference is `skip` (avoids replace churn; note this deliberately
diverges from the inventory diff, where `canonicalDNSValue` folds TTL
into the compare key — the diff reports drift, the plan acts on
substance). Written records (`add`/`replace`) carry
`min(source TTL, 3600)`; the cap bounds how long a wrong record or a
rollback lives in resolver caches, and the plan notes each capped
value. Lowering TTLs ahead of a cutover remains an operator task, out
of scope.

## IP translation — the reason a blind copy is wrong

The source zone's A/AAAA records point at the **source server**. Copying
them verbatim would point the migrated domain back at the old host —
the exact wrong outcome.

```
inventory dns-plan --source s.json --destination d.json \
  --policy policy_report.json \
  --ip-map 194.76.118.193=38.224.109.78 [--ip-map v6old=v6new ...]
```

One rule, no exceptions: **an A/AAAA rrset is actionable only if every
one of its values has an `--ip-map` entry; otherwise the whole rrset is
`manual` ("unmapped address"), and manual is never applied.** There is
no "copy verbatim" path and no reliance on guessing which addresses
belong to the source server (v1 tried to string-match
`Account.Host`, which is an SSH hostname, not an IP — a guard that
would never fire). Records that legitimately must be copied unchanged
(external services, remote mail hosts) are authorized explicitly with
an identity mapping (`--ip-map 1.2.3.4=1.2.3.4`) — deliberate,
per-address, visible in the command line and in the plan.

TXT values are **never rewritten**. A TXT value containing any IP that
appears as an `--ip-map` key (e.g. SPF `ip4:` mechanisms) makes its
rrset `manual`: SPF authorizes *mail senders*, and the web-server IP
map is not a mail-sender map — auto-rewriting an authentication record
with the wrong mapping would silently break deliverability. The
operator fixes SPF by hand, once, knowingly.

## Write API (6D) — single primitive, no write fallback

**UAPI `DNS::mass_edit_zone`** is the only write primitive: atomic
multi-record add/edit/remove in one call, guarded by the zone `serial`
returned by `DNS::parse_zone` (optimistic locking: a concurrent edit
changes the serial and the call fails instead of clobbering).

There is **no API2 write fallback**. Per-record `ZoneEdit::*` writes are
line-number-addressed with no locking; a sequence that fails midway
leaves the zone in a third state that is neither pre- nor post-apply,
which is incompatible with the rollback contract. Consequence, stated
plainly: **if the capability captures show `mass_edit_zone` is missing
or non-atomic on the target server, 6D is not built for that server** —
the pipeline stops at the plan + verify (still useful: the plan is an
exact worksheet for a manual edit session).

### Read-only capability captures (6B prerequisite)

This server already taught us that module availability cannot be
assumed (UAPI Cron is missing on .193). Each item is a read-only probe
via the established Orbit capture method:

- (a) `DNS::mass_edit_zone` exists on cPanel v110 build 131 (probe:
      call with a missing required argument; distinguish "argument
      error" from "function not found". Capture the exact error shape).
- (b) How TXT data >255 chars (DKIM) must be submitted on write: one
      string (server splits) or pre-split segments. The collector joins
      segments on read (real-server fact #1); the writer must round-trip.
- (c) Stale-serial error shape from `mass_edit_zone` (distinguishes
      "re-fetch and retry" from "abort").
- (d) Whether the call is atomic on error (documented behavior vs
      observed error shape; atomicity itself is finally proven on the
      sacrificial zone in 6D smoke, since proving it requires a write).
- (e) Whether a cPanel user may write NS records at all (we never do —
      this only shapes error handling if a zone forces it).
- (f) Name format round-trip: what `parse_zone` returns and what
      `mass_edit_zone` expects (relative vs absolute, trailing dot,
      case), on A, CNAME, MX, TXT and wildcard records.

Every numeric field in write-path responses gets `flexInt64`; every
maybe-array string gets `flexStringList` (repo data rule).

## Backup and rollback (6D contract)

The contract below assumes captures (a)–(d) confirmed
`mass_edit_zone`; otherwise 6D does not exist (see above).

1. **One fetch, two uses.** Before writing a zone, a single
   `parse_zone` call yields BOTH the backup
   (`dns_backup_<zone>_<timestamp>.json`: raw output untouched +
   normalized records + the zone serial) AND the serial that guards the
   write. No gap between "state I saved" and "state I asserted"
   (TOCTOU). No backup file ⇒ no write, unconditionally.
2. **Apply is per-zone and atomic**: one `mass_edit_zone` call per
   zone, serial-guarded. Stale serial ⇒ abort that zone, report, leave
   the rest of the run untouched.
3. **Rollback is scope-limited and serial-guarded.**
   `dns apply --rollback <backup-file>` re-fetches the zone, computes
   the inverse ops **only for the rrsets named in the plan the backup
   belongs to** (restore replaced rrsets to backup values, delete
   rrsets the apply added — the only deletes the tool ever generates),
   and applies them in one serial-guarded `mass_edit_zone` call.
   Records outside the plan's rrsets are never touched, so a concurrent
   third-party edit elsewhere in the zone survives a rollback; a
   concurrent edit *to a planned rrset* changes the serial and aborts
   the rollback for explicit human resolution.
4. **The <60s target is scoped to the master zone file** (one read +
   one write). Secondary nameservers and resolver caches converge
   within record TTL — bounded by the 3600s write cap above. The
   guarantee is "the zone is restored", not "the world has re-resolved".
5. Post-apply, `dns verify` re-fetches the zone and reports
   `applied/missing/unexpected` per planned op. Non-zero exit on
   mismatch (gating pattern of `--fail-on-blockers`).

## CLI sketch

```
# 6B — offline, no network
cpanel-self-migration inventory dns-plan \
  --source inventory_source.json --destination inventory_destination.json \
  --policy policy_report.json \
  --ip-map OLD=NEW ... \
  [--output-json dns_import_plan.json] [--output-md dns_import_plan.md]

# 6C — read-only network (destination only)
cpanel-self-migration dns verify --plan dns_import_plan.json [--fail-on-drift]

# 6D — the only writer
cpanel-self-migration dns apply --plan dns_import_plan.json \
  --backup-dir ./dns-backups/ --yes-apply-writes
cpanel-self-migration dns apply --rollback ./dns-backups/dns_backup_Z_TS.json \
  --yes-apply-writes
```

Exit codes follow the house convention: 0 ok, 1 input/runtime error,
2 flags, 3 gated refusal (drift with `--fail-on-drift`, verify
mismatch, apply refusal).

6D gates, all mandatory: plan file embeds the SHA-256 of both inventory
files and `apply` re-hashes and refuses on mismatch (stale-plan
defense); `--yes-apply-writes` absent ⇒ dry-run print, exit 0, nothing
written (house posture); backup written before first write; `manual`
ops never applied; serial guard on every write.

## Testing strategy

- 6B is pure: table-driven tests over inventory fixtures, including the
  real-server captures already in `internal/testdata/`. Properties: a
  plan applied to the destination inventory in-memory yields rrsets
  equal to the translated source rrsets; zero `delete` ops; every
  A/AAAA op's values are fully mapped. Deliberate fixtures for the
  known traps: CNAME-vs-A same-name conflict, case/trailing-dot
  variants, wildcard owners, unmapped AAAA next to mapped A, SPF TXT
  containing a mapped IP, TTL-only difference.
- 6C reuses `internal/sshtest` with canned `parse_zone` responses.
- 6D: `sshtest` end-to-end (plan → apply → verify → rollback → verify
  restores backup), plus a **new** DNS write-verb safety test asserting
  that `dns-plan` and `dns verify` sources contain no write calls
  (`mass_edit_zone`, `ZoneEdit::add/edit/remove`). The existing
  `cron_safety_test.go` scans cron-specific literals only and does not
  cover DNS — this is new work, not an extension.
- Real-server smoke for 6D happens on a **sacrificial test zone** first
  (e.g. a dedicated test subdomain zone on principiadv.online created
  manually), never first-contact on a customer domain. The sacrificial
  smoke is also where mass_edit_zone atomicity-on-error (capture item
  d) is definitively proven.

## Out of scope

Registrar/nameserver changes, WHM zone creation, DNSSEC, zone file
templates, TXT/SPF auto-rewriting, `internal/migrate/runner.go`, any
UI. Also out of scope for 6B: fetching anything — it is offline by
definition.
