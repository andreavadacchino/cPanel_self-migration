# PR 7E — inventory expansion wave 1 (micro-design)

Grounded in `PR7E_PRE_CAPTURES.md` (all shapes byte-verified on cPanel
110.0 build 131 except the non-empty filter item, which is docs-derived
with proof deferred). Two PRs:

- **7E-1 (this design, part A)** — cpanel layer + the four inventory
  sections + report rendering + contract-test coverage. Inventory-only:
  diff/policy/checklist do NOT change.
- **7E-2 (part B)** — diff + policy rules + checklist: the four areas
  stop being `not_inventoried`, plus the DKIM `CONFIRM_DNS_RECORD`
  action (7A smoke finding 3).

Section names reuse the identifiers the checklist already declares in
`checklistSectionOrder`: `email_routing`, `default_address`,
`email_filters`, `redirects`.

## Part A — cpanel layer (one file per feature, ftp.go template)

| File | Call | Notes from captures |
|---|---|---|
| `email_routing.go` | `Email::list_mxs` (no args) | `priority` quoted string → flexInt64; `local/remote/secondary/alwaysaccept/status` flex too |
| `email_default_address.go` | `Email::list_default_address` (no args) | one call covers ALL domains incl. subdomains; value kept verbatim (embedded quotes) |
| `email_filters.go` | `Email::list_filters` / `+ account=<mailbox>` | non-empty shape docs-derived → decode `rules`/`actions` as `[]json.RawMessage` and store COUNTS only; `filtername` string; `enabled` flexInt64. Shape surprises cannot break the decode |
| `mime_redirects.go` | `Mime::list_redirects` (no args) | `statuscode` null OR `"301"` string → flexInt64 (0 = none); `wildcard`/`matchwww` flex ints; keep raw `kind`/`type` |

All read-only; module/function as string literals (structural safety
test). Errors returned verbatim; never-fatal lives in the collector.

## Part A — accountinventory sections

Standard `ConfigSection` wrappers (`XxxSection{ConfigSection; Items}`),
initialized non-nil in `NewEmptyInventory`:

- `EmailRoutingEntry{Domain, Routing (mxcheck), Detected, AlwaysAccept,
  MXRecords []MXRecordEntry{Priority int64, Exchange string}}` — key
  facts preserved so 7E-2 can diff routing mode per domain.
- `DefaultAddressEntry{Domain, DefaultAddress string}` — value verbatim,
  compared as an opaque string.
- `EmailFilterEntry{Account, FilterName string, Enabled int64,
  RuleCount, ActionCount int}` — **counts only, no rule bodies**: rule
  contents may embed personal patterns and their shape is unproven;
  counts+names are all the checklist action needs. Collector calls
  account-level (`Account:""`) plus one call per real mailbox from
  `Email::list_pops` (the `Main Account` pseudo-entry — `email` without
  `@` — is skipped; account-level covers it). Per-mailbox loop follows
  the per-domain precedent of forwarders/autoresponders.
- `RedirectEntry{Domain, Source, Destination, Kind, Type string,
  StatusCode int64, Wildcard, MatchWWW bool}` — raw inventory; the
  CMS-noise classification (`kind=="rewrite" && type=="temporary" &&
  statuscode==0`) is POLICY logic (7E-2), not collector logic. DocRoot
  is dropped (adds nothing the checklist needs; one less path in
  artifacts).

Contract test: the four sections join the all-sections list, the stub
uapi harness serves the new fixtures, no-null-arrays and
available/method invariants apply unchanged.

Fixtures: `email_list_mxs_realserver.json` (real local+remote pair in
one envelope, mixed-account per ftp_list_realserver.json precedent),
`email_default_address_realserver.json`, `mime_redirects_realserver.json`
(the real 301 + two CMS rewrites), plus synthetic
`email_list_filters.json` (docs-derived non-empty, labeled) and the
byte-verified empty envelope.

## Part B — diff/policy/checklist (7E-2)

- `diffSectionNames` += the four names (appended after `cron`; policy
  iterates the same list). Adapters: key = `Domain` (routing, default
  address), `Account+"/"+FilterName` (filters), `Domain+" "+Source`
  (redirects).
- Policy v0 rules:
  - routing changed/removed → review (`POL-MAILROUTE-*`); added → info.
  - default address changed/removed → review; added → info.
  - filter removed → review "recreate on destination"; changed → review;
    added → info.
  - redirects: entries matching the CMS-noise predicate → info
    (`POL-REDIRECT-CMS-REMOVED`, they travel with webfiles); genuine
    redirect removed/changed → review. The predicate requires
    rewrite+temporary+no-status **AND a non-URL destination**: every CMS
    rewrite captured live targets a relative path
    (`%{ENV:REWRITEBASE}…`) while operator redirects always target an
    absolute URL, so an operator "temporary" redirect reporting no
    status code still classifies as genuine (round-1 review HIGH).
- Checklist: the four names move from `buildNotInventoriedSection` to
  `buildInventoriedSection` (+ `inventorySectionCount`). Section
  evaluators emit targeted actions only on real differences:
  `CONFIRM_EMAIL_ROUTING` (per-domain, routing mode differs),
  `MANUAL_CHECK_REQUIRED` (default address differs),
  `RECREATE_EMAIL_FILTERS` (new type; blocking — silent mail-handling
  change otherwise), `CONFIRM_REDIRECT` (new type; non-blocking —
  genuine redirects travel with `.htaccess` via webfiles). Zero items on
  both sides → section `ok`, no action: strictly better than today's
  blanket manual checks.
- **Acceptance-key churn is intended**: the four blanket
  `not_inventoried` actions disappear, so operator acceptances bound to
  their keys go stale — correct per the 7D key contract (the fact
  changed: real data replaced a blind check).
- DKIM (7A finding 3): in the dns section evaluator, a dns-plan
  `replace` op whose rrset name contains `._domainkey` (or starts with
  `default._domainkey`) emits a dedicated `CONFIRM_DNS_RECORD` action —
  non-blocking (the pending replace is already tracked as plan work;
  the action adds the old-key-vs-regenerated-key human decision instead
  of today's silence), `derived_from` `dns-plan:<zone>:TXT:<name>`.
- Goldens (`migration_checklist.md.golden`, diff/policy goldens) refresh
  with `UPDATE_GOLDEN=1` and get reviewed hunk by hunk.

## Invariants (pinned by tests)

1. No null arrays anywhere (contract test).
2. Sections degrade to `available:false` + `method:"unavailable"` +
   warning; never fatal.
3. Filter rule/action bodies never reach any artifact (counts only) —
   extends the redaction posture.
4. Read-only: no email/mime write verb appears as a code token
   (existing module-wide scans; `TestDNSAPICallsUseLiteralNames` already
   forces literal module/function names on every new RunUAPI call).
5. Determinism: every new list sorted (domain, then account/filtername,
   then source).

## Post-review hardening (round 1 findings)

1. `POL-SECTION-MISSING` (review): a diff artifact lacking an expected
   section key — e.g. produced by a pre-7E binary — can no longer read
   as ok downstream; the policy emits a review per missing section
   (closes a pre-existing silence that 7E would have widened).
2. The CMS-noise predicate requires a non-URL destination (above).
3. When the dns comparison is skipped but mail routing has data, the
   checklist warns explicitly that the MX exchangers behind the routing
   were never verified.
