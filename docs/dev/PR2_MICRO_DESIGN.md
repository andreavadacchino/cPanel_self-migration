# PR 2 — Account Inventory Skeleton — Micro-Design

## Scope

Add `--account-inventory` flag for read-only account inventory collection.
Collects domains, docroots, mailboxes, and databases from source (and
optionally destination). Outputs `inventory.json` and `inventory_report.md`.

Zero writes to any server. Zero changes to existing migration behavior.

## New CLI flag

```
--account-inventory   collect account inventory (read-only) and exit
```

Mutually exclusive with `--apply` and `--apply-mirror`.
Compatible with `--config`, `--output-dir`, `--run-id`, `--json-events`,
`--report-json`.

## Files to CREATE

```
internal/accountinventory/types.go           # NormalizedInventory, DomainEntry, MailboxEntry, etc.
internal/accountinventory/collector.go       # Collect(ctx, src, dest Runner) function
internal/accountinventory/write.go           # WriteJSON, WriteReport
internal/accountinventory/types_test.go      # Type serialization tests
internal/accountinventory/collector_test.go  # Collector tests with fake Runner
internal/accountinventory/write_test.go      # Output format tests
internal/cpanel/email_list.go               # ListEmailAccounts wrapper (UAPI Email::list_pops_with_disk)
internal/cpanel/email_list_test.go          # parseUAPI fixture test
internal/testdata/email_list_pops.json      # UAPI fixture
```

## Files to MODIFY

```
cmd/cpanel-self-migration/main.go           # Add --account-inventory flag + wiring
internal/migrate/runner.go                  # NOT MODIFIED (inventory is a separate path)
```

## Key design decision: separate path, not a runner phase

`--account-inventory` does NOT go through `migrate.Run()`. It creates its own
execution path in `main.go` that:
1. Loads config
2. Dials SSH to source (+ dest if configured)
3. Calls `accountinventory.Collect(ctx, src, dest)`
4. Writes output files
5. Exits

This avoids coupling inventory to the migration pipeline and keeps the runner
untouched.

## Reuse map

| Data needed | Existing function | Package |
|-------------|-------------------|---------|
| Domains (name + type) | `cpanel.ListDomains(ctx, Runner)` | cpanel |
| Docroots (domain + path) | `cpanel.ListDocroots(ctx, Runner)` | cpanel |
| Databases | `cpanel.ListDatabases(ctx, Runner)` | cpanel |
| DB users | `cpanel.ListDBUsers(ctx, Runner)` | cpanel |
| Mailboxes | **NEW** `cpanel.ListEmailAccounts(ctx, Runner)` | cpanel |

### Why new ListEmailAccounts instead of reusing collectMailboxes

`collectMailboxes` (in `migrate/collect.go`) is:
- unexported
- collects password hashes (not needed for inventory)
- runs a complex shell script
- coupled to migration concerns

For inventory we only need "which mailboxes exist + disk usage". UAPI
`Email::list_pops_with_disk` provides exactly that — a ~10 line wrapper
following the existing `ListDatabases` pattern.

## NormalizedInventory schema

```go
type NormalizedInventory struct {
    Account  AccountInfo      `json:"account"`
    Domains  []DomainEntry    `json:"domains"`
    Mailboxes []MailboxEntry  `json:"mailboxes"`
    Databases []DatabaseEntry `json:"databases"`
    Warnings []string         `json:"warnings"`
}

type AccountInfo struct {
    User        string `json:"user"`
    Host        string `json:"host"`
    CollectedAt string `json:"collected_at"`
    Side        string `json:"side"` // "source" or "destination"
}

type DomainEntry struct {
    Name         string `json:"name"`
    Type         string `json:"type"`          // main/addon/sub/parked
    DocumentRoot string `json:"document_root"` // from ListDocroots
}

type MailboxEntry struct {
    Email     string `json:"email"`      // user@domain
    Domain    string `json:"domain"`
    User      string `json:"user"`
    DiskUsage int64  `json:"disk_usage"` // bytes, from UAPI
}

type DatabaseEntry struct {
    Name      string   `json:"name"`
    DiskUsage int64    `json:"disk_usage"`
    Users     []string `json:"users"`
}
```

## Output files

When `--account-inventory` is used:

```
<output-dir>/
  inventory_source.json        # NormalizedInventory for source
  inventory_destination.json   # NormalizedInventory for dest (if configured)
  inventory_report.md          # Human-readable summary
  events.jsonl                 # (if --json-events)
  report.json                  # (if --report-json)
```

## Collector flow

```
Collect(ctx, srcRunner, destRunner, srcInfo, destInfo)
  ├── ListDomains(ctx, src)
  ├── ListDocroots(ctx, src)
  ├── ListEmailAccounts(ctx, src)
  ├── ListDatabases(ctx, src)
  ├── ListDBUsers(ctx, src)
  ├── merge into NormalizedInventory (source)
  ├── if dest configured:
  │   ├── same 5 calls on dest
  │   └── merge into NormalizedInventory (dest)
  └── return (srcInventory, destInventory, warnings)
```

Each UAPI call failure produces a warning, not a fatal error.
The inventory continues with partial data.

## Hook points in main.go

```go
if *accountInventory {
    // validate: mutually exclusive with --apply/--apply-mirror
    // dial SSH
    // collect inventory
    // write output
    // emit events if --json-events
    // write report if --report-json
    // exit
}
// ... existing migration path unchanged ...
```

## Risks

| Risk | Severity | Mitigation |
|------|----------|------------|
| UAPI Email::list_pops_with_disk unavailable | LOW | Warning + empty mailbox list |
| No mutation of runner.go | NONE | Separate path |
| Config KnownFields | NONE | No YAML changes |
| Test regression | LOW | New package, no existing code modified except main.go |

## TDD sequence

1. Write ListEmailAccounts fixture test (RED)
2. Implement ListEmailAccounts wrapper (GREEN)
3. Write NormalizedInventory type tests (RED)
4. Implement types (GREEN)
5. Write Collector tests with fake Runner (RED)
6. Implement Collector (GREEN)
7. Write output format tests (RED)
8. Implement WriteJSON + WriteReport (GREEN)
9. Wire into main.go
10. Build + test full suite
