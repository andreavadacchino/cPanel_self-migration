# PR 7A — real-data smoke of `inventory checklist` (doctorbike.it)

Date: 2026-07-02. Read-only captures taken via Orbit on the real account
`doctorbike` (cPanel 110.0 build 131, server 194.76.118.193), replayed
through the REAL `Collect()` with the throwaway capture-replay harness
(never committed — see DEVELOPMENT_STATE.md). The destination inventory
simulates a realistic "apply done, pre-cutover" state on 38.224.109.78:
A/SPF records regenerated with the new IP, DKIM regenerated with new
keys, Mailchimp CNAMEs (`k2/k3._domainkey`) and `_dmarc` NOT recreated,
AutoSSL reissued per-vhost certs, cron NOT recreated, forwarder and
sub-FTP accounts NOT recreated, mail/files/db migrated (simulated
successful apply `report.json`).

Capture note: the Orbit gateway masks emails/paths/IPs in command
output and the masking corrupts JSON; captures must be base64-encoded
in transit (`uapi … | base64 -w0`).

## Source inventory (real)

4 domains (main + 3 subs), 8 mailboxes, 4 databases, 1 forwarder
(admin@ → support@keliweb.com), 6 FTP accounts, 14 SSL certs (incl. 3
wildcard generations, several expired), 4 PHP vhosts (2× ea-php80, 2×
ea-php74), 1 DNS zone with 61 records (4 SPF, 4 DKIM, DMARC, Mailchimp
k2/k3 CNAMEs, 6 acme/DCV validation records), 6 enabled cron jobs
(`token=`/`secure=` correctly redacted by the collector).

## Pipeline results

| Stage | Output |
|-------|--------|
| `inventory diff` | 1 added, 25 removed, 45 changed |
| `inventory policy` | **blocked** — 7 blockers, **63 reviews**, 10 warnings, 1 info |
| `inventory dns-plan` (ip-map .193→.78) | 3 add, 4 replace, 4 manual, 49 skip |
| `inventory checklist` | **BLOCKED** — 2 blocked sections, 23 manual actions (15 blocking), **45 expected differences** |

The headline: the checklist absorbed the policy noise. 63 raw reviews
collapsed into 45 expected differences (A-records already translated,
proven by plan `skip`) plus targeted actions. Every blocking item was
real work: 6 cron recreations (1 with `/home/` path adaptation), the
Keliweb forwarder, the wildcard certificate, email routing/catch-all/
filter checks. FTP sub-accounts correctly listed non-blocking. Evidence
honesty held: mail/files/db/domains showed `run_level` only because a
successful apply report was provided.

## Findings (refinement candidates, in priority order)

1. **SPF false positive when the destination is already correct** — all
   4 `UPDATE_SPF` blocking actions were false positives: dest SPF was
   already exactly the ip-map translation of the source
   (`+ip4:38.224.109.78`), but `dnsplan.go` `classify()` sends any TXT
   containing a mapped source address to `manual` without comparing the
   destination first. Fix belongs in the plan: if the destination rrset
   equals the source rrset after ip-map string substitution → `skip`.
   (Same false positive family as the identity-map case seen in the 6B
   smoke.) **FIXED** in the follow-up PR: `classify()` now skips when
   the destination already matches the translation; re-running this
   smoke gives 0 manual DNS ops and 11 blocking actions, all legitimate.
2. **Expired source certificates still gate** — the source carries
   several EXPIRED certs; when their domain grouping is missing on the
   destination they surface as removed. The valid-coverage downgrade
   rescued the per-vhost ones, but expired wildcard generations kept an
   SSL blocker alive (`*.doctorbike.it` is never literally covered by
   per-vhost AutoSSL certs). Candidates: treat certs already expired on
   the SOURCE as `not_applicable`; match wildcard coverage semantically.
   **FIXED** in the follow-up PR: a removed group whose source entries
   are ALL provably expired at Now downgrades to an expected difference
   (fail-safe: unknown expiry or one still-valid generation keeps the
   blocker), and `domainCovered` now matches RFC 6125-style wildcards
   (a valid destination `*.base` cert covers exactly one extra label —
   never the base domain, multi-label subdomains, or a lost wildcard
   "covered" by per-host certs).
3. **DKIM-changed reviews are silent** — regenerated DKIM keys produce
   plan `replace` ops (pending work) and policy reviews, but no
   dedicated operator action. Real ambiguity (old key vs regenerated
   key) deserves a `CONFIRM_DNS_RECORD`-class action in 7E.

## Verdict

Usable on a real account. 15 blocking actions of which 11 legitimate,
4 known-family SPF false positives (fail-safe direction — they cost a
manual glance, they cannot cause a false READY). The `not_inventoried`
sections did their job: email routing / catch-all / filters surfaced as
explicit blocking checks instead of silence.
