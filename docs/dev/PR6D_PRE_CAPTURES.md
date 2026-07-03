# PR 6D-pre — DNS write primitives, byte-verified on the sacrificial dest

Date: 2026-07-03. All calls against giorginisposi zone on .78 (writes
legitimate by construction, peer NS standalone verified). Captures in
`~/Desktop/pADV/cPanel_self-migration-captures/fase0_2-giorginisposi/cap6dpre/`.

## Prerequisite: peer NS standalone verified

`dig +short @136.144.242.119 _6dpre.giorginisposi.it TXT` → empty
response before AND after writes. The peer NS is standalone —
zone changes on .78 do NOT propagate to the production NS.

## Byte-verified facts (the 6D implementation contract)

1. **`DNS::mass_edit_zone` parameter format**: array parameters use
   indexed keys, NOT JSON arrays:
   - `add-0=<JSON object>`, `add-1=<JSON object>`, etc. (NOT `add=[array]`)
   - `remove-0=<int>`, `remove-1=<int>`, etc. (NOT `remove=[array]`)
   - `zone=<zone>`, `serial=<int>` as plain key=value.
   Each `add-N` value is a JSON object:
   `{"dname":"<relative name>","ttl":<int>,"record_type":"<type>","data":["<value>"]}`

2. **`new_serial` is a string** in the response JSON:
   `"data":{"new_serial":"2026070300"}`.

3. **Stale-serial error (closes PR6B item (c))**: exact error string:
   ```
   The given serial number (<stale>) does not match the DNS zone's
   serial number (<current>). Refresh your view of the DNS zone,
   then resubmit.
   ```
   Status=0 with this error. The record is NOT created.

4. **SOA serial in `parse_zone` response**: `data_b64[2]` of the SOA
   record, **base64-encoded**. Must decode before using as the serial
   parameter for `mass_edit_zone`.

5. **Add → verify → remove → verify round-trip**:
   - Add: `add-0={"dname":"_6dpre2","ttl":300,"record_type":"TXT","data":["roundtrip-test"]}` → status:1, new_serial returned.
   - Verify: `FetchDNSZoneUAPI` finds the record at the expected line.
   - Remove: `remove-0=<line_index>` with fresh serial → status:1.
   - Final verify: record gone.

6. **Non-propagation proven**: `dig @136.144.242.119` for the probe
   record returns empty after both add and remove — the peer NS
   (standalone) does not receive zone changes from .78.

7. **Line index shifts after add**: the probe record was added at
   line_index=36 (end of zone). After remove of line 36, the zone
   returns to its original state. Line indexes must be re-resolved
   on a fresh `parse_zone` for every write operation.

## Consequences for 6D

- **Writer uses `add-0=`, `add-1=`, etc.** — NOT JSON arrays. This
  means `RunUAPI` with `map[string]string` works natively (each entry
  is a separate key-value pair).
- **Serial from `parse_zone`**: decode base64 from `data_b64[2]` of SOA.
- **Stale-serial detection**: check for the exact error string
  (or status=0 after a mass_edit_zone call).
- **Rollback `remove-0=`**: each removed line is a separate parameter.
  Line indexes must be from a fresh fetch (never cached).
