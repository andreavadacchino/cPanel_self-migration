# PR 6B-pre — DNS capability captures (results)

Read-only captures performed 2026-07-02 on the real server
(cPanel 110.0 build 131, server 194.76.118.193) via the established
Orbit method, on two accounts: doctorbike.it (zone: 64 lines) and
italplant.com (zone: 125 lines). Raw captures are regenerated on demand
(2 commands, see "How to re-capture"); per repo convention they are not
committed. Docs cross-checked against the official OpenAPI spec
(`api.docs.cpanel.net`, `/DNS/mass_edit_zone`).

Checklist status vs `PR6A_DNS_IMPORT_DESIGN.md`:

| Item | Status |
|---|---|
| (a) mass_edit_zone exists on v110 | **RESOLVED — YES** |
| (b) TXT >255 write format | **RESOLVED** (array of segments; final proof on sacrificial zone) |
| (c) stale-serial error shape | PARTIAL (docs confirm behavior; exact string needs sacrificial zone) |
| (d) atomicity on error | DEFERRED to sacrificial zone (docs imply batch semantics) |
| (e) user may write NS | DEFERRED (moot — we never write NS) |
| (f) name format round-trip | **RESOLVED read side**; write side has docs answer, proof on sacrificial zone |

## (a) `DNS::mass_edit_zone` exists on cPanel v110 build 131

Probe (`uapi --output=json DNS mass_edit_zone`, no arguments — fails at
parameter validation, touches nothing):

```json
{"apiversion":3,"module":"DNS","func":"mass_edit_zone",
 "result":{"metadata":{},"messages":null,"status":0,"warnings":null,
 "errors":["Provide the ‘zone’ argument."],"data":null}}
```

Function found, argument validation error — NOT "function not found".
The v1 assumption "parse_zone requires v136" was already proven wrong;
mass_edit_zone is likewise present on v110.

## Official signature (OpenAPI spec, confirmed)

- `zone` (required), `serial` (required, integer) — *"If this value
  does not match the zone's current state, the request fails"* →
  optimistic locking is documented behavior, not an assumption.
- `add[]` — serialized JSON objects `{dname, ttl, record_type, data[]}`.
- `edit[]` — same PLUS **`line_index` (0-based)**: edits are addressed
  by line, not by rrset.
- `remove[]` — array of line indexes.
- Response carries `data.new_serial` for consecutive edits.
- *"You cannot use this function to edit temporary domains"* — handle
  as an error case.

**Design impact (6B):** replace ops must resolve line indexes; the
"one fetch, two uses" rule of 6A becomes **one fetch, three uses** —
the same `parse_zone` response supplies (1) the backup, (2) the guard
serial, (3) the line indexes for `edit`/`remove`. Line indexes are only
valid against that exact serial, which the serial guard enforces
transitively.

## (b) TXT segments

Write format takes `data` as an **array of strings** (docs example:
`"data":["string1","string2"]`). Real reads show DKIM TXT split into
255-char segments (2 segments per key on both accounts). The writer
pre-splits >255-char TXT values into segments, mirroring the read
join. Whether the server would also accept one long string is
irrelevant — we write the format we can verify round-trip.

## (f) Real name format — the synthetic fixture was wrong

From both real zones (identical format on both accounts):

| What | Format | Example |
|---|---|---|
| apex owner name | absolute FQDN **with** trailing dot | `doctorbike.it.` |
| non-apex owner name | **relative, no** trailing dot | `www`, `mail`, `www.shop`, `default._domainkey`, `_acme-challenge.www` |
| CNAME/NS target, MX exchange | absolute FQDN with trailing dot | `doctorbike.it.`, `dkim2.mcsv.net.` |
| MX data | `[preference-as-string, exchange]` | `["0","doctorbike.it."]` |
| SOA | `record_type:"SOA"`, data `[mname, rname, serial, refresh, retry, expire, minimum]` all strings | serial at `data[2]`, e.g. `"2026061101"` |
| TTL | bare integer | `14400` |
| lines 0–2 | `comment`, `comment`, `control` (`$TTL 14400`) | first record at `line_index` 3 |

The committed synthetic fixture (`dns_parse_zone.json`) shows ALL owner
names as FQDN — **wrong for non-apex names**. It stays for decoder
coverage but must not be trusted for name-format behavior; 6B needs an
anonymized real-shape fixture. The docs' write examples use a relative
`dname` (`"dname":"example"`), matching the read format for non-apex
names; apex writes presumably use the FQDN form as read. Final
round-trip proof on the sacrificial zone.

## New facts the design should absorb in 6B

1. **Host-validation records must be skipped, not migrated.** Both real
   zones carry `_acme-challenge*` and `_cpanel-dcv-test-record` TXT
   records — transient, host-specific validation tokens (AutoSSL/DCV).
   Copying them to the destination is at best noise, at worst confuses
   DCV. 6B: skip-list on owner-name prefixes `_acme-challenge` and
   `_cpanel-dcv-test-record` (action `skip`, reason recorded).
2. **The SPF→manual rule will fire in practice.** Both accounts publish
   `v=spf1 +a +mx +ip4:194.76.118.193 ~all` — the source IP inside a
   TXT. Expected: those rrsets go `manual` per design; not a false
   positive, the operator genuinely must rewrite SPF for the new host.
3. **cPanel-generated service names are dense.** Dozens of
   `cpanel/webmail/whm/webdisk/cpcontacts/cpcalendars/ftp` A records
   (per subdomain, too). On a destination zone freshly created by
   cPanel these already exist pointing at the destination IP, so after
   ip-map translation they compare equal → `skip`. Fixtures should
   include this shape to keep plans quiet.
4. **No wildcard records** on either account — the wildcard fixture
   must be synthetic and labeled as such.
5. **Subdomain records live in the parent zone** (`shop`, `www.shop`,
   `noleggio`, …) — re-confirms design exclusion #4 from live data.
6. Sourcery follow-ups from PR #11 to fold into 6B: embed the effective
   `--ip-map` table verbatim in `dns_import_plan.json` (auditability);
   specify the relative→FQDN canonicalization rule explicitly (append
   zone origin to relative names; now grounded in the real format
   above).

## How to re-capture

Orbit session (TOTP), then per account:

```
uapi --output=json DNS mass_edit_zone            # existence probe (safe: fails at validation)
uapi --output=json DNS parse_zone zone=<zone>    # real zone, read-only
```

via `wordpress_run_remote_command` on the account's WordPress site_id.
Accounts must be registered in Orbit (doctorbike.it and italplant.com
are). Save one file per call into a local capture dir — never commit
raw captures; anonymize into `internal/testdata/*_realserver.json`
keeping format (relative vs absolute names, string numerics, segment
splits) intact.
