# PR 6D — DNS apply smoke report

Date: 2026-07-03.

## DNS smoke end-to-end on .78

| Step | Result |
|------|--------|
| Peer NS standalone (both 136.144.242.119, 185.17.106.73) | verified via dig |
| Baseline serial | 2026070303 |
| `MassEditZoneAdd` (TXT `_smoke-total`, TTL=300) | new_serial=2026070304 |
| Verify: record present at line 36 | OK |
| Non-propagation: dig @136.144.242.119 | empty (standalone confirmed) |
| `MassEditZoneRemove` (line_index from fresh fetch) | OK |
| Verify: record gone | OK |
| **DNS SMOKE (harness)** | **PASSED** |

## Writer primitives fully live-proven

The DNS writer primitives (`MassEditZoneAdd`, `MassEditZoneRemove`,
`ExtractSOASerial`, `FetchDNSZoneRaw`) have now been exercised
end-to-end on the sacrificial .78 zone. The `IsStaleSerialError`
detection was proven in the 6D-pre session.

## Binary-level smoke (supersedes harness)

Date: 2026-07-03 (same session, after CLI wiring PR).

The harness-level smoke is superseded by the binary-level smoke below.
The CLI subcommands (`dns apply`, `dns verify`) now exist and have been
exercised end-to-end against the real .78 zone via the compiled binary.

| Step | Command | Result |
|------|---------|--------|
| Peer standalone guard | dig @136.144.242.119 SOA/TXT | empty for .78 zone (standalone confirmed) |
| Apply TXT `_binary-smoke-test` | `dns apply --plan ... --yes-apply-writes` | 1 applied, 0 failed |
| Verify CLEAN | `dns verify --plan ... --fail-on-drift` | CLEAN — 1 applied |
| Non-propagation | dig @136.144.242.119 `_binary-smoke-test` TXT | empty (standalone confirmed) |
| Rollback LIVE | `dns apply --rollback backup.json --yes-apply-writes` | 1 applied (record removed) |
| Verify pre-smoke | `dns verify --plan ...` | NOT CLEAN — 1 pending (state restored) |
| **DNS BINARY SMOKE** | | **PASSED** |
