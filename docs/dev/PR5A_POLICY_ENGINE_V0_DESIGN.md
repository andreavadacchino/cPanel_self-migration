# PR 5A — Policy Engine v0

## Scope

Deterministic classification of an `inventory_diff.json` into a migration
risk report. The engine states whether each difference is blocking, needs
review, or is informational — it NEVER decides what to do about it, never
connects anywhere, never applies anything. No LLM, no scoring heuristics:
pure rule table.

## CLI

```
cpanel-self-migration inventory policy \
  --diff ./inventory_diff.json \
  [--output-json ./policy_report.json] \
  [--output-md ./policy_report.md]
```

Exit codes: `0` report generated (blockers are NOT a process error),
`1` invalid input, `2` unparsable flags.

## Severities and statuses

Finding severity: `info < warning < review < blocker`.
Overall status: any blocker → `blocked`; else any review → 
`review_required`; else → `ready` (warnings and info never gate).

## Rule table (v0)

| Rule ID | Trigger | Severity |
|---|---|---|
| POL-DOMAIN-MAIN-REMOVED | domains.removed with type `main` | blocker |
| POL-DOMAIN-REMOVED | domains.removed (other types) | review |
| POL-DOMAIN-TYPE-CHANGED | domains.changed field `type` | review |
| POL-DOMAIN-DOCROOT-CHANGED | domains.changed field `document_root` | info (paths differ across hosts by design) |
| POL-MAILBOX-REMOVED | mailboxes.removed | blocker |
| POL-MAILBOX-ADDED | mailboxes.added | info |
| POL-DB-REMOVED | databases.removed | blocker |
| POL-DB-ADDED | databases.added | info |
| POL-DB-USERS-CHANGED | databases.changed | review |
| POL-DNS-ZONE-REMOVED | dns.removed, zone-level | blocker (all its records, MX included, are gone) |
| POL-DNS-ZONE-ADDED | dns.added, zone-level | info |
| POL-DNS-MX-CHANGED / -REMOVED | dns record MX | blocker |
| POL-DNS-NS-CHANGED / -REMOVED | dns record NS | blocker |
| POL-DNS-MAIL-RECORD-ADDED | dns.added MX/NS | review (mail/delegation routing appears) |
| POL-DNS-RECORD-CHANGED | dns.changed other types (A/AAAA/CNAME/TXT incl. SPF/DKIM/DMARC) | review |
| POL-DNS-RECORD-REMOVED | dns.removed other types | review |
| POL-DNS-RECORD-ADDED | dns.added other types | info |
| POL-SSL-REMOVED | ssl.removed, at least one cert domain NOT in domains.removed | blocker |
| POL-SSL-REMOVED-WITH-DOMAIN | ssl.removed, all cert domains also removed | info |
| POL-SSL-CHANGED | ssl.changed (issuer/expiry/…) | review |
| POL-SSL-ADDED | ssl.added | info |
| POL-PHP-CHANGED | php.changed | review |
| POL-PHP-REMOVED | php.removed | review |
| POL-PHP-ADDED | php.added | info |
| POL-FTP-REMOVED | ftp.removed | review |
| POL-FTP-CHANGED | ftp.changed | review |
| POL-FTP-ADDED | ftp.added | info |
| POL-FORWARDER-REMOVED / POL-AUTORESPONDER-REMOVED | removed | review |
| POL-AUTORESPONDER-CHANGED | changed | review |
| POL-FORWARDER-ADDED / POL-AUTORESPONDER-ADDED | added | info |
| POL-CRON-ENABLED-REMOVED | cron.removed with `enabled=true` | blocker |
| POL-CRON-DISABLED-REMOVED | cron.removed with `enabled=false` | info |
| POL-CRON-SCHEDULE-CHANGED | cron.changed field `schedule` | review |
| POL-CRON-ENABLED-CHANGED | cron.changed field `enabled` | review |
| POL-CRON-ADDED | cron.added | info |
| POL-SECTION-UNAVAILABLE | any section warning mentioning skipped comparison | review (incomplete data can never be `ready`) |
| POL-DIFF-WARNING | other diff warnings (duplicate keys, …) | warning |

## Input contract

The diff DNS keys are parsed positionally (`zone <zone> [<TYPE> <name>]`)
— a format owned and tested by this repo. Cron removed/added entries
carry `… enabled=true|false` in Detail; the diff producer was aligned in
this PR (the whole-group branch previously omitted the flag the policy
needs). Skipped comparisons travel in the structured
`sections.*.skipped` field (added in this PR): the "incomplete data can
never be ready" gate branches on that field, never on warning prose.

Legacy note: a diff produced by a pre-PR5A binary lacks both the
`skipped` field and the cron `enabled=` flag. The engine fails CLOSED on
such files: every removed cron job classifies as an active-job blocker
(systematic false positives, never false negatives). Regenerate the diff
with the current binary for accurate cron findings.

The input file must have `mode == "inventory-diff"`; anything else is
rejected (exit 1).

## Determinism and safety

Findings sorted by severity (blocker→info), then section, id, detail.
All content comes from the diff, which is already redacted/truncated —
the policy never reconstructs raw commands or values. Markdown cells go
through mdCell (pipe-escape + rune-safe truncation).

## Out of scope

UI, dashboards, apply/import/fix, SSH, Orbit/TOTP, LLM scoring,
`internal/migrate/runner.go`.
