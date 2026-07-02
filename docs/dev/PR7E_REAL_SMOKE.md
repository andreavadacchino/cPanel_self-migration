# PR 7E — real-data smoke of the four new inventory sections

Date: 2026-07-02. Goal: validate the 7E sections (email_routing,
default_address, email_filters, redirects) end-to-end on REAL server data
after the #34/#35 merges, before building anything on top of them.

## Method — replay, no new server contact

No new Orbit session was needed: the same-day read-only captures from the
7A smoke and the 7E-pre session were still on disk and were replayed
through the REAL `accountinventory.Collect` with a throwaway
capture-replay harness (`cmd/replay-smoke`, never committed — project
convention). The captures are archived outside the repo in
`~/Desktop/pADV/cPanel_self-migration-captures/`:

- `doctorbike-full-setA/` — full 11-section capture (2026-07-02 08:43,
  the one the 7A smoke used; 61-record DNS zone). A second set from
  2026-07-01 was discarded: its `DNS::parse_zone` capture held only 19
  records (partial fetch).
- `cap7e/{doctorbike,italplant}/` — the four 7E calls (byte-verified in
  `PR7E_PRE_CAPTURES.md`).
- `7a-artifacts/` — the 7A smoke pipeline artifacts, including the
  simulated ".78 apply done, pre-cutover" destination and `report.json`.

Simulation choices: the 7A destination inventory was extended with the
four 7E sections in the post-migration state (routing/default address
identical to source, filters empty, **redirects empty** — i.e. the
harvest before the web-files copy, which exercises the CMS-rewrite
classification on all Removed entries). `Email::list_filters` per-mailbox
calls were served the account-level empty response (faithful: filters are
empty everywhere on the real server, 7E-pre).

## Regression guarantee

The post-7E collector reproduces the 7A collection **exactly**: all 11
pre-7E sections of the regenerated `inventory_source.json` are
multiset-identical to the 7A smoke's source artifact. Zero drift from
#32–#35 on existing collectors.

## Run A — doctorbike (full pipeline, real source, simulated dest)

| Stage | Result |
|-------|--------|
| `inventory diff` | 14 sections — 1 added, 45 removed, 45 changed |
| `inventory policy` | **blocked** — 7 blockers, 63 reviews, 10 warnings, 21 info |
| `inventory dns-plan` (.193→.78) | 3 add, 4 replace, **0 manual**, 53 skip |
| `inventory checklist` (with 7A report.json) | **BLOCKED** — 19 manual actions (**8 blocking**), 69 expected differences, `chain_verified: true` |

Deltas vs the 7A smoke are exactly the 7E design:

- removed 25→45 and info 1→21: the **20** PrestaShop `.htaccess` rewrites
  (10 noleggio + 10 shop2), every one classified
  `POL-REDIRECT-CMS-REMOVED` (info) and absorbed as expected differences
  (49→69, delta = the 20 redirects, nothing else changed).
- reviews unchanged at 63: **zero** spurious findings from the four new
  sections when source and destination match.
- blocking actions 11→8: the three blanket manual checks (email routing /
  catch-all / filters) are gone, replaced by per-item logic that found
  nothing to do — `email_routing` **ok**, `default_address` **ok**,
  `email_filters` **not_applicable**, `redirects` **expected_difference**.
  Zero `CONFIRM_EMAIL_ROUTING` / `RECREATE_EMAIL_FILTERS` /
  `CONFIRM_REDIRECT` actions. The remaining 8 blocking are the same
  legitimate work as 7A: 6 cron (5 RECREATE + 1 ADAPT_CRON_PATH), the
  Keliweb forwarder, the wildcard certificate.
- DKIM (7A finding 3, closed by #35): the 4 dns-plan `replace` ops on
  `default._domainkey*` now raise 4 non-blocking `CONFIRM_DNS_RECORD`
  actions.
- SPF regression check: still **0 manual DNS ops** (the 6B-fix holds —
  dest SPF already carrying the translated IP classifies as skip).

## Run B — stale-destination guard (negative test)

Same source, but the ORIGINAL 7A destination artifact (pre-7E schema, no
7E sections): all four sections come out **review_required** (never a
silent ok), driven by 4 `POL-SECTION-UNAVAILABLE` findings (63→67
reviews); verdict stays BLOCKED. The #35 round-1 hardening
(POL-SECTION-MISSING / no-silence-on-missing-sections) holds on real
artifacts.

## Run C — italplant (section-scoped, remote routing + genuine redirect)

italplant has no full-inventory capture; a minimal synthetic
`list_domains`/`domains_data` scaffold (domains derived from the real
`list_default_address` capture: main + 9 language subdomains) enabled
`Collect`, with all non-7E sections degrading to warnings as designed.
The four 7E sections are 100% real data. Destination = same 7E state,
redirects empty.

- `email_routing` → **ok**: the `remote` routing (mxcheck remote,
  alwaysaccept 0) round-trips cleanly, zero actions.
- `default_address` (10 domains) → **ok**; `email_filters` →
  **not_applicable**.
- The single genuine operator redirect (`wilco-uk.italplant.com/ →
  https://wilco.italplant.com/`, permanent 301, absolute-URL destination)
  is correctly NOT treated as CMS noise: `POL-REDIRECT-REMOVED` (review)
  and exactly one non-blocking `CONFIRM_REDIRECT` action.
- Policy total: 6 reviews = 5 unavailable degraded sections + the 1
  genuine redirect. Verdict NOT_READY (no apply evidence supplied) —
  evidence honesty behaves.

## Verdict

**All 7E smoke criteria pass.** No code changes required. Two
documentation corrections recorded here: (1) `PR7E_PRE_CAPTURES.md` says
"doctorbike = 15 entries"; the byte-verified capture actually holds 20
(10 + 10; shop.doctorbike.it contributes none — its PrestaShop
`.htaccess` evidently exposes no harvestable RewriteRules). (2) the 7A
smoke doc quotes 45 expected differences; the archived artifact carries
49.

## Notes for the next steps

- The replay method (saved captures + throwaway harness) makes inventory
  smokes repeatable offline; keep archiving capture sets per account.
- The italplant scaffold trick (synthetic domain listing, real section
  captures) is acceptable for section-scoped validation only — a full
  italplant pipeline run would need a real full capture (~10 min with a
  TOTP session).
