# PR 3C — DNS Inventory (read-only)

## Scope

Read-only DNS zone inventory for each domain in the account.

## API Calls

| Priority | API        | Function                       | Param    |
|----------|------------|--------------------------------|----------|
| 1 (try)  | UAPI       | `DNS::parse_zone`              | `zone`   |
| 2 (fall) | API2       | `ZoneEdit::fetchzone_records`  | `domain` |

UAPI requires cPanel ≥v136. Target server runs v110 → API2 is the working path.

## Data Flow

```
collectSide()
  └─ collectDNS(ctx, r, domains)
       └─ for each domain:
            ├─ cpanel.FetchDNSZoneUAPI(ctx, c, zone) → []DNSRecord
            │   fail? ──► cpanel.FetchDNSZoneAPI2(ctx, c, domain) → []DNSRecord
            │                fail? ──► warning (zone unavailable, continue)
            └─ append DNSZoneResult to section
```

## cpanel package additions

- `api.go`: `RunAPI2[T]`, `parseAPI2[T]`, `api2ArgsScript` (cpapi2 CLI)
- `types.go`: `api2Envelope[T]`
- `dns_zones.go`: `FetchDNSZoneUAPI`, `FetchDNSZoneAPI2`, raw/normalized types

## accountinventory additions

- `types.go`: `DNSRecordEntry`, `DNSZoneResult`, `DNSSection`
- `collector.go`: `collectDNS()`
- `write.go`: DNS report section
- `NormalizedInventory.DNS` field + `NewEmptyInventory` update

## Per-zone output schema

```json
{
  "available": true,
  "zone": "example.com",
  "method": "api2",
  "source_function": "ZoneEdit::fetchzone_records",
  "records": [...],
  "warnings": [],
  "errors": [],
  "raw_included": false
}
```

## Out of scope

cron, SSH `/var/named`, named-compilezone, DNS write/import/modify,
swap IP, mass edit, policy engine, UI, runner.go changes.
