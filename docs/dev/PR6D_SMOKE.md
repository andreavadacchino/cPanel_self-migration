# PR 6D — DNS apply smoke report

Date: 2026-07-03.

## DNS smoke end-to-end on .78

| Step | Result |
|------|--------|
| Peer NS standalone (both 136.144.242.119, 185.17.106.73) | ✅ verified via dig |
| Baseline serial | 2026070303 |
| `MassEditZoneAdd` (TXT `_smoke-total`, TTL=300) | ✅ new_serial=2026070304 |
| Verify: record present at line 36 | ✅ |
| Non-propagation: dig @136.144.242.119 | ✅ empty (standalone confirmed) |
| `MassEditZoneRemove` (line_index from fresh fetch) | ✅ |
| Verify: record gone | ✅ |
| **DNS SMOKE** | **PASSED** |

## Writer primitives fully live-proven

The DNS writer primitives (`MassEditZoneAdd`, `MassEditZoneRemove`,
`ExtractSOASerial`, `FetchDNSZoneRaw`) have now been exercised
end-to-end on the sacrificial .78 zone. The `IsStaleSerialError`
detection was proven in the 6D-pre session.

## Note: command file not yet implemented

The smoke used a throwaway harness calling the Go primitives directly.
The `dns apply` CLI subcommand (command file) does not exist yet — it
will wire the primitives into the CLI dispatch with flags, backup
management, and report writing. The primitive behavior is proven;
the CLI wiring is the remaining work.
