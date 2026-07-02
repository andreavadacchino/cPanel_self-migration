# Fase 0.2 — first real `--apply` (giorginisposi .193 → .78)

Date: 2026-07-02/03. The missing milestone of the whole project: the migration
engine had never been executed against real servers. This documents the first
real run end-to-end — account selection, the destination-side DNS-cluster
minefield, the two engine gaps the dry-run exposed (fixed in PR #40), the
apply itself, and every divergence observed (gold for Fase 5).

## Sacrificial account — selection and verified numbers

`giorginisposi` on .193 (194.76.118.193, "Intel Core i7", cPanel 110, CentOS 7,
861 days of uptime, load ~12-14 during the session). Verified via Orbit
(session reused, read-only) + direct SSH probe:

- disk 2.0 GB / 15 GB, 15 377 inodes; not suspended; plan principiadv_Professional
- 1 MySQL DB `giorginisposi_sito` (~25 MB), user `giorginisposi_db`
- 1 mailbox `info@giorginisposi.it` (~12 MB, 379 messages, **MD5 (weak)
  password hash**) + 1 forwarder → gmail
- crontab EMPTY (the "empty command" line Orbit showed was a parsing artifact)
- shell: jailshell; **only the main domain** — zero addon/sub/parked, so the
  apply exercises no domain creation
- source tooling: GNU tar 1.26, **MariaDB 10.5.26 client** (the "MySQL 5.x
  source" assumption was wrong in this direction), kernel 3.10 el7
- WordPress 6.6.5 + WPBakery + CF7 + EventON, no WooCommerce; site alive
  (apex 301 → www 200)

candidate `carrozzeriaberto` was discarded earlier (domain without DNS,
unverifiable from outside).

## Destination account creation — the DNS-cluster minefield

**Fact discovered: .78 (38.224.109.78, server157582, WHM 136) is a member of
the production DNS cluster** — peer `ns.hostnuoviclienti.com` (136.144.242.119)
with role **sync**, plus 185.17.106.73 (standalone). And
`ns.hostnuoviclienti.*` IS the public NS delegation of giorginisposi.it (and
of the fleet). Consequences, all verified live:

1. `whmapi1 createacct` on .78 **refuses** any domain whose zone exists in the
   cluster ("A DNS entry … already exists"). The operator's manual attempt and
   the first scripted attempt both failed on this. This will hit EVERY
   account of the campaign.
2. Creating the zone locally on .78 is a loaded gun: with the sync role, any
   zone save on .78 (AutoSSL DNS-DCV is the obvious nightly writer) pushes the
   local zone — pointing at .78, with a freshly generated DKIM — to the
   production NS. **DNS hijack of the live site.**
3. `killdns` cannot remove the zone afterwards: cPanel refuses to delete the
   zone of an account's primary domain ("still configured for HTTP use").
   A zone-less primary domain is NOT a supported cPanel state.
4. ⚠️ **CAMPAIGN-CRITICAL**: `removeacct` on .78 with clustering enabled
   propagates the zone DELETION to the production NS → outage for that
   domain. Any dest-account cleanup MUST temporarily disable clustering
   first (`/var/cpanel/useclusteringdns`).

**Maneuver used** (repeatable per campaign account, ~30 s window):
`mv /var/cpanel/useclusteringdns` aside (trap restores it on exit) →
`createacct` (zone created locally only) → restore flag. Then, defense in
depth for the weeks the account sits pre-cutover:

- peer 136.144.242.119 role flipped **sync → standalone** (original backed up
  at `/root/136.144.242.119-dnsrole.fase02.bak` on keliweb2): nothing on .78
  can push zones to production until the role is consciously restored. NOTE:
  PR 6D's `dns apply` cutover design assumes the sync path — the role must be
  re-evaluated in the Fase 3 session.
- AutoSSL excluded for giorginisposi.it/www/mail
  (`uapi SSL add_autossl_excluded_domains`).
- Production zone dig-verified intact (A → .193, SOA serial 2026051601
  unchanged) before and after; snapshots archived in
  `~/Desktop/pADV/cPanel_self-migration-captures/fase0_2-giorginisposi/`.

Side facts: thanksvirtualtour.it (the only other .78 account) has NO public NS
delegation, so the role flip harms nothing; `listzones` on .78 is
cluster-aware (returns the fleet's zones) while `/var/named` holds only 2
local zones — do not confuse the two views.

Dest account: `giorginisposi` / giorginisposi.it, shell /bin/bash, password
auth verified by direct SSH login. Both servers offer `password` in their
sshd auth methods (first time verified — the tool's password-auth design had
never been exercised).

## Dry-run 1 → the two engine gaps (fixed in PR #40)

`configs/host.yaml` (mode 600, gitignored — NOTE: only `/configs/host.yaml`
is gitignored, a stray root-level `host.yaml` is NOT). Dry-run 1 connected
fine (TOFU host keys, jailshell on source) and exposed:

**Gap 1 — domain type classifier.** The upstream engine only supports
migrating INTO a different account: `ExpectedDestinationType(Main) = Addon`,
plus an unconditional block when the destination domain is the dest account's
MAIN domain. Our 1:1 layout (dest account rebuilt with the SAME main domain)
classified as "destination domain type mismatch", which **blocked web files
AND database** (mail only warned): the apply would have migrated mail only.

**Gap 2 — webfiles containment guard.** Found by adversarially re-examining
the apply path after fixing gap 1 (the dry-run does NOT exercise apply-time
guards): `guard_dest_docroot` unconditionally refuses `~/public_html` itself
(exit 11) — at the `ValidateDestTargets` preflight and in every
empty/backup/extract script. Same-main-domain migration means the dest docroot
IS public_html, so `--apply` would have failed at preflight anyway.

Both fixed in **PR #40** (branch `fix/main-to-main-domain-type`, merged):
narrow same-FQDN main→main carve-out in the classifier +
`WebPlanItem.AllowDestPublicHTMLRoot` opt-in relaxing ONLY the exact-equality
refusal in the guard (`ALLOW_PUBLIC_HTML_ROOT=1`, per-item env);
`backupDestScript` refuses the root even with the flag (renaming public_html
aside would orphan the guard's own anchor). TDD RED→GREEN in Docker; two
adversarial go-reviewer rounds → APPROVE; gate = go-reviewer + Docker suite
(Sourcery rate-limited until ~2026-07-09).

Follow-ups recorded in the PR: symlinked-public_html anchor test (LOW,
pre-existing); backup path refuses empty-source main→main (revisit only if
the 0.3 census shows empty main docroots).

## Dry-run 2/3 — the correct plan

Identical before/after the guard change (the flag acts only at apply time):

- domains: `giorginisposi.it present on both` (no mismatch)
- mail: 1 mailbox TO MIGRATE (379 msg), MD5 hash to be preserved via
  `password_hash` — watch Dovecot on CL9.8 accepting MD5-crypt
- web: 12 473 files / 1.1 GB, `/home/giorginisposi/public_html` →
  `/home/giorginisposi/public_html`
- db: `giorginisposi_sito` will create (24.2 MB), user `giorginisposi_db`,
  password reused from wp-config, prefix rewrite is the identity
  (`giorginisposi_` → `giorginisposi_`)

## Apply — results (exit 0, 14/14 phases green, zero failures)

| Flow | Result | Verify |
|------|--------|--------|
| mail | `info@` created + **379 messages** (395 files incl. control) | per-folder count + UIDVALIDITY (V1684333191) + **message body hashes** ✓ |
| web  | **12 521 entries / 1.1 GB** into the public_html ROOT (the #40 guard opt-in held on real data) | full manifest (path+size+type+mode) + tree content fingerprint ✓ |
| db   | `giorginisposi_sito`: **32 tables, 24.7 MB**, user+grants provisioned, 1 wp-config rewritten | tables+objects+encoding+row counts+same-version checksum+object definitions ✓ |

End-to-end reality check (no cutover, `curl --resolve` onto .78): HTTP and
HTTPS answer with `X-Redirect-By: WordPress` (PHP + DB alive), the same 301
apex→www as production, and the www homepage renders the real content
("Andrea & Moira – 23.08.2023"). **The engine flew.**

## First real pipeline (inventory → diff → policy → dns-plan → checklist)

Real source + real post-apply destination + the REAL `report.json`:
`chain_verified: yes` across all 6 inputs — first checklist ever produced
with non-simulated evidence. The four migrated sections show
**"migrated by tool (per_item evidence)"**: the PR 7C/7D honesty invariants
work on real apply evidence.

Verdict: **BLOCKED** — 1 blocker, 16 reviews, 12 info; 6 manual actions
(3 blocking), 11 expected differences. Every single item is CORRECT for the
real state:

- **MA-001 blocking** — forwarder `info@ → gmail` not on dest (no writer
  until Fase 2B) → CREATE_ON_DESTINATION.
- **MA-003 blocking / POL-DNS-NS-CHANGED (the blocker)** — NS set differs:
  source zone carries com/net/org, the freshly created dest zone only
  com/net (WWWAcct filled NameServer1/2 only). Genuine registrar-territory
  item.
- **MA-006 blocking** — catch-all differs: source
  `andreavadacchino@gmail.com`, dest default `giorginisposi` (2B writer
  territory).
- **MA-004** — regenerated DKIM → non-blocking CONFIRM_DNS_RECORD (the 7E/#35
  logic firing on real data, exactly as designed).
- **MA-002** — PHP `ea-php80 → ea-php82` → CHECK_PHP_COMPATIBILITY.
- dns-plan (.193→.78): 0 add, 1 replace (DKIM), 1 manual (NS), **15 skip** —
  the ip-map translation classifies the dest A records already pointing at
  .78 as skip (6B-fix behavior confirmed on real data); SPF: 0 manual.
- cron / autoresponders / email_filters / redirects: not_applicable (true);
  email_routing / ftp: ok; quota/server config: explicit root-only sections.

## Divergences observed (Fase 5 / follow-up input)

1. **Engine gaps fixed in PR #40** (see above) — the 1:1 main→main layout was
   impossible before this session; THE structural finding of Fase 0.2.
2. **`createacct` vs DNS cluster** — every campaign account will need the
   clustering-off maneuver (or a decision to pre-create all dest zones
   another way); `removeacct` with clustering on would DELETE the production
   zone. Must become a documented runbook step (Fase 4 material).
3. **SSL section on a fresh dest account reports `unavailable`**
   (POL-SECTION-UNAVAILABLE review, "comparison skipped") instead of an
   honest "0 certificates". Cosmetic-ish but wrong shade of honesty: a fresh
   account has no certs, that is a comparable state. Collector follow-up.
4. **Analysis vs copy count**: analysis reported "12 473 files", the copy and
   manifest 12 521 entries (files + empty dirs). Not a bug — different units
   in the two messages — but the wording invites a false mismatch reading.
5. **Mailbox password hash is MD5 (weak)** from the CentOS 7 source,
   preserved by design. Whether Dovecot on CloudLinux 9.8 ACCEPTS MD5-crypt
   at login could not be tested (mailbox password unknown to us — by
   design); covered by the post-cutover send/receive check.
6. NOT hit in this run (single-domain vetrina): addon/sub creation flow,
   PrestaShop 1.7+ discovery, DB_HOST rewrite, cross-version CHECKSUM
   degradation. Still open engine risks for shop accounts.

## Post-session destination state

Account `giorginisposi` on .78 is a faithful pre-cutover mirror: site serves
locally, mail store populated, DB live. DNS: local zone on .78 (points to
.78, new DKIM), cluster peer role standalone, AutoSSL excluded — NOTHING
reaches the production NS. Production zone verified untouched after the full
session. Cleanup, if ever needed: disable clustering FIRST, then removeacct.

## Artifacts

All runs archived under
`~/Desktop/pADV/cPanel_self-migration-captures/fase0_2-giorginisposi/`:
`dryrun1/` (pre-fix, the blocking mismatch), `dryrun2-postfix/`,
`dryrun3-guard/`, `apply1/` (events.jsonl, report.json, logs/), plus the DNS
pre/post-state digs.
