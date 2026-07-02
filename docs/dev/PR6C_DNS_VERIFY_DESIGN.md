# PR 6C — `dns verify` (micro-design)

Goal: prove, read-only, whether the DESTINATION account's DNS zones
match a `dns_import_plan.json` — per planned op — so an operator who
applied the plan by hand (the plan is "an exact worksheet for a manual
edit session", PR6A) or a future `dns apply` (6D) gets a machine
verdict instead of eyeballing zone files. This is the certification
step 6D's contract already references (PR6A, rollback point 5).

## CLI

```
cpanel-self-migration dns verify --plan dns_import_plan.json \
  [--config host.yaml] \
  [--source inventory_source.json] [--destination inventory_destination.json] \
  [--output-json dns_verify_report.json] [--output-md dns_verify_report.md] \
  [--fail-on-drift]
```

New `dns` namespace in the dispatch (before global flag parsing, like
`inventory`/`ui`). Side benefit: today `cpanel-self-migration dns
verify` silently ignores both tokens and, with a resolvable config,
starts a full migration dry-run — the new namespace turns that footgun
into a real command (and any unknown `dns` subcommand into usage +
exit 2).

Exit codes (house convention): 0 ok (report written, even with drift);
1 input/SSH-dial/write error; 2 flag error; 3 gated refusal — stale
plan (below) or `--fail-on-drift` with a non-clean verdict.

**Stale-plan gate**: when `--source`/`--destination` are given, the
file's sha256 (raw bytes, `fileSHA256`) must equal the plan's embedded
`source_sha256`/`destination_sha256`. A mismatch — or a plan carrying
no embedded hash to compare against — refuses the whole run (exit 3)
BEFORE any SSH. Without the flags the plan is trusted as-is (the live
zone, not the inventory, is the verify authority). The plan file must
have `mode == "dns-import-plan"` and `format_version == 1`.

## Connection: destination only

Verify re-fetches DESTINATION zones. The source server may already be
decommissioned when verify runs, so `sshx.DialBoth` (which always
dials the source first) is wrong here. New exported helper:

```go
// internal/sshx/pool.go
func DialDest(ctx context.Context, cfg config.Config, knownHostsPath string) (*Client, error)
```

Same TOFU/known-hosts path and pre-cancelled-context check as
`DialBoth`, requires `cfg.DestConfigured()`, dials only `cfg.Dest`.
`*sshx.Client` already satisfies `cpanel.Runner` — no wrapper. When
the plan has zero verifiable zones (all manual), verify never dials.

## Zone fetch: extracted from the collector, not duplicated

`collectDNS` already implements the real fetch semantics (UAPI
`DNS::parse_zone` → API2 `ZoneEdit::fetchzone_records` fallback →
unavailable-with-warning, records converted via `toDNSRecordEntries`).
That per-zone body is extracted behavior-preservingly into

```go
// internal/accountinventory
func FetchDNSZone(ctx context.Context, r cpanel.Runner, zone string) DNSZoneResult
```

used by both `collectDNS` (existing collector tests pin the refactor)
and the verify command. Fetch failure is never fatal: the zone lands
in the report as unavailable and gates the verdict (fail-safe: cannot
verify ⇒ not verified).

## Verify engine: pure, offline, same package as the plan

```go
// internal/accountinventory/dnsverify.go
func VerifyDNSPlan(plan DNSPlan, live map[string]DNSZoneResult) DNSVerifyReport
```

Reuses the plan's own comparison machinery (`groupRRSets`,
`planValue`, `valuesEqual`, `canonDNSName`, `isHostValidationName`) so
verify can never disagree with the plan about what "equal" means.
Values-only comparison, TTL never compared (plan rule). The desired
values of an `add`/`replace` op are derived from its `records`
(write-shaped `PlanRecord`): A/AAAA/CNAME → `Data[0]`, MX →
`Data[0]+"\x00"+Data[1]`, TXT → `strings.Join(Data, "")` (inverse of
`splitTXTSegments`). A malformed record (wrong `Data` arity) makes its
op `drift` with an explicit reason — fail-safe, never a silent pass.

### Per-op status

| Plan action | Live rrset | Status |
|---|---|---|
| add / replace | equals desired values | `applied` |
| add | absent (plan-time state) | `pending` |
| replace | equals plan-time `destination_values` | `pending` |
| add / replace | anything else | `drift` |
| skip (SOA, AutoSSL/DCV host-validation names) | — | `not_checked` |
| skip (value-equality, NS-equal, TXT-already-translated) | equals plan-time `destination_values` | `unchanged` |
| skip (checkable, as above) | differs | `drift` |
| manual | reported (current values shown) | `manual_review` |

Mapping to the 6A sketch ("applied/missing/unexpected"): missing =
`pending` on an `add`; unexpected = `drift`. `pending` is one status
for "the zone still matches plan time": legitimate before an apply,
a failure after one — the report counts it separately so both uses
read correctly.

A checkable skip op with no plan-time `destination_values` cannot
happen by construction (equality implies the rrset existed); if a
hand-edited plan produces one it degrades to `drift` with a reason,
fail-safe.

Live rrsets of actionable types (A/AAAA/CNAME/MX/TXT) that appear in
neither the zone's ops nor its plan-time `informational` list are
reported as `untracked` (informational, non-gating): they postdate the
plan and deserve a glance, but the additive posture means the tool has
no opinion on them.

### Gate (the `clean` verdict)

```
clean = pending == 0 && drift == 0 && unavailable_zones == 0 && manual_zones == 0
```

- **Manual OPS never gate.** NS is `manual` in essentially every real
  migration; gating on it would make `--fail-on-drift` permanently
  red — the exact self-deadlock the 6A v2 redesign removed from the
  plan gate. Manual ops have their resolution home in the checklist +
  operator acceptances (7D).
- **Manual ZONES do gate.** All three manual-zone reasons ("zone
  missing on destination", "unavailable on source/destination") mean
  the plan computed NO ops for that zone — its migration state is
  unknown, and a verify that ignores it would print a false green on a
  fresh migration where every zone is still missing. Stale/incomplete
  plan ⇒ re-run the pipeline; verify cannot upgrade it.

`--fail-on-drift` absent: report written, summary printed, exit 0
(house pattern: reporting never gates by default). Present: exit 3
unless `clean`.

## Report

`dns_verify_report.json` + `.md` (writer mirrors `dnsplan_write.go`,
golden-tested markdown):

```
DNSVerifyReport{
  mode: "dns-verify", format_version: 1, generated_at,
  plan_file, plan_sha256,            // provenance of the consumed plan
  zones: [ { zone, available, method, fetch_error?,
             ops: [ { action, type, name, status, reason?,
                      expected_values?, observed_values? } ],
             untracked: [ {type, name, values} ] } ],
  manual_zones: [ {zone, reason} ],  // passthrough from the plan
  summary: { applied, unchanged, pending, drift, manual_review,
             not_checked, untracked, unavailable_zones, manual_zones },
  clean: bool
}
```

`plan_sha256` = sha256 of the plan file's raw bytes — the provenance
hook a future checklist input can verify (PR7B pattern). Ordering is
deterministic everywhere (plan order for ops; sorted untracked).

Known honesty caveat (documented, accepted): if the plan was built
from UAPI-shaped TXT data and verify falls back to API2 (or vice
versa), value shapes could theoretically differ → `drift`. Fail-safe
direction: costs a manual glance, can never fabricate `applied`.

## Safety: DNS write-verb scan goes module-wide

`internal/cpanel/dns_safety_test.go` scans only `internal/cpanel/`;
the plan/verify sources live in `internal/accountinventory/` and
`cmd/`. New test (same file, cron-style `filepath.WalkDir` from the
module root, non-test `.go` files only): no source file may contain
`mass_edit_zone`, `swap_ip_in_zones`, `add_zone_record`,
`edit_zone_record`, `remove_zone_record`, `/var/named`. PR 6D will
have to consciously amend this test to introduce its writer — that is
the point.

The scan is token-based (go/scanner over string literals and
identifiers; comments are exempt — design docs and plan comments
legitimately NAME the write API). A lexical scan is defeated by
runtime concatenation (`"mass_"+"edit_zone"` — go-reviewer finding),
so a structural companion test closes that hole: every
`RunUAPI`/`RunAPI2` call in the module must pass its module and
function names as plain string literals; a dynamically built name
fails regardless of its value. Accepted residual limit: a writer that
bypasses those entry points entirely (hand-built `uapi …` script via
`Runner.RunScript`) is human-review territory.

## Testing (TDD)

- Engine (offline, `internal/accountinventory`): table-driven over
  plan+live fixtures — add applied/pending/drift; replace
  applied/pending/drift; skip unchanged/drift; SOA + `_acme-challenge`
  not_checked; manual reported non-gating; manual zones gating;
  untracked detection; TXT joined-segment equality (DKIM-length
  values); MX priority in the compare; case/trailing-dot
  canonicalization between plan names and live names; malformed
  PlanRecord → drift; zone missing from `live` map → unavailable.
- `FetchDNSZone` extraction: existing `TestCollectDNS*` pin the
  collector; direct unit tests reuse the `fakeRunner` +
  `dns_parse_zone.json` / `dns_fetchzone_records.json` fixtures.
- `sshx.DialDest`: `sshtest.NewExecServer` connect + not-configured
  error + pre-cancelled context.
- cmd e2e (`sshtest` + PATH stubs, `inventory_contract_test.go`
  pattern): crafted plan vs fixture-served zone → statuses + exit
  codes (0 / 3 with `--fail-on-drift`); stale-plan sha256 refusal;
  bad plan file (mode/format_version) → 1; flag errors → 2; an
  integration flow `dns-plan → verify` on the same inventories.
- Goldens refreshed via `UPDATE_GOLDEN=1`.

## Out of scope

Any write (6D), zone creation, WHM, rollback, UI wiring, TTL
verification, `internal/migrate/runner.go` (untouched).
