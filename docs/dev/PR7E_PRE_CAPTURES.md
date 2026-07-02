# PR 7E-pre — email routing / default address / filters / redirects captures (results)

Read-only captures performed 2026-07-02 on the real server
(cPanel 110.0 build 131, server 194.76.118.193) via the established
Orbit method, on the two registered accounts: doctorbike.it (4 domains,
8 mailboxes) and italplant.com (main + 9 subdomains, main-account
mailbox only), plus a filters/redirects probe on principiadv.online.
Raw captures are regenerated on demand (see "How to re-capture"); per
repo convention they are not committed.

**Transfer method (improvement over the 6B caveat):** every capture is
saved to a server-side file first, relayed base64-encoded, decoded and
split locally, then verified by sending the LOCAL md5 list back to the
server for `md5sum -c`. A transcription error can only produce a false
FAILED (refetch), never a false OK. Result: **12/12 files OK** across
both accounts, every fixture-bearing capture byte-verified.

## Call availability on v110 build 131 (all UAPI, all read-only)

| Call | Status | Notes |
|---|---|---|
| `Email::list_mxs` (no args) | **WORKS** | returns every mail-routing domain in one call |
| `Email::list_default_address` (no args) | **WORKS** | returns ALL domains incl. subdomains — no per-domain loop needed |
| `Email::list_filters` (no args = account-level) | **WORKS** | `data:[]` when empty |
| `Email::list_filters account=<mailbox>` | **WORKS** | same shape, per-mailbox |
| `Email::list_filters_backups` | WORKS | `[{domain}]` per domain incl. subs; not used by 7E v0 |
| `Mime::list_redirects` | **WORKS** | harvests `.htaccess` — see fact 4 |

## Fact 1 — `Email::list_mxs`: the routing pair is real

Per-domain object shape (identical structure on both accounts):

```json
{"domain":"doctorbike.it","mxcheck":"local","detected":"local",
 "local":1,"remote":0,"secondary":0,"alwaysaccept":1,
 "status":1,"statusmsg":"Fetched MX List","mx":"doctorbike.it",
 "entries":[{"mx":"doctorbike.it","domain":"doctorbike.it",
             "priority":"0","entrycount":1,"row":"odd"}]}
```

- **doctorbike.it: `mxcheck:"local"`, `alwaysaccept:1`** — mail hosted
  on the box. **italplant.com: `mxcheck:"remote"`, `local:0`,
  `alwaysaccept:0`** — mail hosted elsewhere. The two registered
  accounts cover both routing modes with real data; fixtures need no
  synthetic routing case.
- **`priority` is a QUOTED STRING** (`"0"`) → `flexInt64`.
  `local`/`remote`/`secondary`/`alwaysaccept`/`status` arrived as bare
  ints here; per the general lesson, decode them flex anyway.
- **Only mail-routing domains are listed** (the main domain on both
  accounts). Subdomains do NOT appear in `list_mxs` even when they
  appear in `list_default_address` → the two sections have different
  domain universes; the diff must not treat a subdomain missing from
  routing as a removal.

## Fact 2 — `Email::list_default_address`: one call, all domains

```json
{"domain":"doctorbike.it","defaultaddress":"\":fail: No Such User Here\""}
```

- No-args form returns every domain **including subdomains** (4 on
  doctorbike, 10 on italplant) — the collector needs exactly one call.
- Both accounts carry the cPanel default `":fail: No Such User Here"`
  everywhere (note the embedded quotes: the JSON string value itself
  starts and ends with `"`). Forward-to-address, `:blackhole:` and
  pipe-to-program variants were NOT observed — treat the value as an
  opaque string, compare verbatim, never parse it into semantics beyond
  the well-known `:fail:`/`:blackhole:` prefixes.

## Fact 3 — `Email::list_filters`: empty everywhere reachable

`data: []` for: doctorbike account-level, doctorbike per-mailbox
(`account=info@…`), italplant account-level, principiadv account-level
(which also has zero filter files on disk — `ls $HOME/etc/*/filter*
$HOME/.filter` empty). The empty shape is byte-verified on both capture
accounts.

**The non-empty item shape is NOT captured** — no reachable production
account has filters, and creating a throwaway filter on a live mailbox
is a write outside this capture session's read-only perimeter. Per the
6B-pre precedent (items c/d), the item shape (`filtername`, `rules[]`,
`actions[]`, `enabled`) is taken from the official docs with **final
proof deferred**; the 7E collector must decode it flex + fail-safe and
the checklist action for filters is count/name-based
(`RECREATE_EMAIL_FILTERS`), never rule-replication, so a shape surprise
cannot corrupt a migration decision.

## Fact 4 — `Mime::list_redirects` harvests `.htaccess`: CMS noise dominates

This call does not read an operator-curated redirect store; it parses
`.htaccess` RewriteRules. Observed:

- **doctorbike: 15 entries, ALL PrestaShop image-rewrite noise**
  (`kind:"rewrite"`, `type:"temporary"`, `statuscode:null`, regex
  sourceurl, `%{ENV:REWRITEBASE}` destination) on the noleggio/shop2
  subdomain docroots. principiadv probe: same pattern (19 entries across
  two PrestaShop docroots). Zero operator redirects on either.
- **italplant: exactly 1 REAL operator redirect** —
  wilco-uk.italplant.com → `https://wilco.italplant.com/`,
  `type:"permanent"`, **`statuscode:"301"` (QUOTED STRING)** →
  `flexInt64`; `wildcard:1`, `matchwww:1` (bare ints) with parallel
  `wildcard_text`/`matchwww_text` (`"checked"`/`""`) display fields.

**Design impact (7E):** redirects live in `.htaccess` files, which the
webfiles migration already copies verbatim — so the inventory must NOT
tell the operator to recreate CMS rewrites. Classification rule:
`kind:"rewrite"` + `type:"temporary"` + `statuscode:null` ⇒
CMS-generated, informational only; `type:"permanent"` (or a concrete
statuscode) ⇒ genuine redirect worth an operator confirmation (its
correctness after the move depends on both docroots and DNS).

## Fact 5 — reminders that resurfaced

- `Email::list_pops` includes the main-account pseudo-mailbox
  (`login:"Main Account"`, `email:"doctorbike"` — no `@domain`). Any 7E
  per-mailbox filter enumeration must skip or special-case it.
- Key order inside every object is randomized per invocation (already
  known from 6B) — fixtures must never assert byte order.

## Carried into 7E from the 7A smoke

Finding 3 of `PR7A_REAL_SMOKE.md`: regenerated DKIM keys produce plan
`replace` ops and policy reviews but no dedicated operator action. 7E
adds a `CONFIRM_DNS_RECORD`-class checklist action for DKIM rrsets whose
destination value differs from the source (old key vs regenerated key is
a human decision).

## How to re-capture

Orbit session (TOTP), then per account via `wordpress_run_remote_command`
on the WordPress site_id:

```
D=$HOME/.cap7e; rm -rf "$D"; mkdir -p "$D"; cd "$D" || exit 1
uapi --output=json Email list_mxs             > 01_list_mxs.json 2>&1
uapi --output=json Email list_default_address > 02_default_address_noargs.json 2>&1
uapi --output=json Email list_filters         > 03_list_filters_account.json 2>&1
uapi --output=json Mime  list_redirects       > 04_list_redirects.json 2>&1
uapi --output=json Email list_filters_backups > 05_list_filters_backups.json 2>&1
uapi --output=json Email list_pops            > 06_list_pops.json 2>&1
md5sum *.json | base64 -w0; echo
```

Bulk fetch: `cd $HOME/.cap7e && { for f in *.json; do printf 'FILE:%s\n'
"$f"; cat "$f"; printf '\nENDFILE\n'; done; } | base64 -w0` — decode and
split locally, then verify by piping the LOCAL md5 list back into
`md5sum -c -` on the server (fail-safe direction). Remove `$HOME/.cap7e`
when done. Never commit raw captures; anonymize into
`internal/testdata/*_realserver.json` keeping format (string numerics,
embedded quotes in `defaultaddress`, null-vs-string `statuscode`)
intact.
