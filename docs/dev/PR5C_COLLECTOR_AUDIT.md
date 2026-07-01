# PR 5C — Collector real-server audit

## Goal

After the PR5B smoke test found 3 real-server parsing/redaction bugs
(FTP, SSL, cron), audit the REMAINING inventory collectors against the
actual cPanel 110 response shapes captured from doctorbike.it and
italplant.com.

## Method

Compare every Go type consumed by `Collect` against the real captured
JSON, field by field. Fix what the real data proves broken; harden what
matches the already-observed failure pattern.

## Findings

| Collector | Field | Real shape | Verdict |
|-----------|-------|-----------|---------|
| Email pops | `diskusedquota` | **does not exist**; disk is `_diskused` (bytes, quoted string) | BUG — mailbox disk always 0. Rebound to `_diskused` via flexInt64. |
| Autoresponders | interval/is_html/start/stop | not observed non-empty | Hardened to flexInt64 (preventive — same shape broke 3 collectors). |
| Forwarders | dest/forward | plain strings | OK |
| Databases | disk_usage/users | int / array | OK (already flexInt64 + []string) |
| Domains (list/data) | name/type/documentroot | strings | OK |
| DBUserEntry | `shortuser` | real is `short_user` | Mismatch, but NOT used by the inventory collector (`ListDBUsers` is unused). Left as-is; noted in DEVELOPMENT_STATE. |

## Fix

- `EmailAccountEntry.DiskUsedQuota (int64 "diskusedquota")` →
  `DiskUsedBytes (flexInt64 "_diskused")`. Verified end to end: all 8
  doctorbike mailboxes now report real usage instead of 0.
- `AutoresponderEntry` interval/is_html/start/stop → flexInt64
  (preventive hardening, declared as such).
- Synthetic `email_list_pops.json` rewritten to the real shape; new
  `email_list_pops_realserver.json` + `TestEmailRealServerDiskUsedBytes`.

## Not changed

The diff layer matches mailboxes by email existence only and ignores
disk usage (volatile), so the units change (effectively-0 → real bytes)
is inert for diff/policy. No policy rule touched.
