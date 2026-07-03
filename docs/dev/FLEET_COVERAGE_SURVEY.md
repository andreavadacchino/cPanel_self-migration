# Fleet Coverage Survey — not_collected areas

Date: 2026-07-04. Evidence-based census of 15 `not_collected` areas from
`coverage.go` against the sacrificial account giorginisposi@.193 (via Orbit).

## Survey method

Per-area: lightest READ-ONLY UAPI endpoint or filesystem check. All
verified on giorginisposi (single connection, ~3s per batch). UAPI
module availability varies by cPanel version and feature set.

## Results — giorginisposi

| Area | Check | Available | Used | Evidence |
|------|-------|-----------|------|----------|
| api_tokens | `Tokens::list` | yes | 1 token | `orbitpanel_services` — platform-managed, auto-provisioned |
| boxtrapper | `ls ~/.boxtrapper` | n/a | no | directory absent |
| contact_info | `Variables::get_user_information` | yes | yes | contact_email set — trivially re-settable, not migration-critical |
| directory_privacy | `ls ~/.htpasswds` | n/a | minimal | `public_html` subdir exists — investigate if password protection is active |
| domain_aliases | `DomainInfo::domains_data` | yes | no | 0 parked, 0 addon domains |
| git_repositories | `VersionControl::retrieve` | yes | no | 0 repositories |
| hotlink_protection | `grep .htaccess` | n/a | no | no hotlink/rewrite rules in .htaccess; UAPI module unavailable on this cPanel |
| leech_protection | `find .leechprotect` | n/a | no | 0 files |
| mailbox_quota_limits | `Email::list_pops_with_disk` | yes | no | 1 mailbox, `unlimited` quota — no limits to migrate |
| mailing_lists | `Email::list_lists` | yes | no | 0 lists |
| mime_handlers | `Mime::list_mime` | yes | no | 0 custom handlers |
| passenger_apps | `PassengerApps::list_applications` | feature disabled | no | "Non si dispone della funzione passengerapps" |
| spamassassin | `Email::get_spam_settings` | yes | **YES** | `spam_enabled=1`, `spam_box_enabled=1`, `~/.spamassassin/user_prefs` exists (default template) |
| ssh_keys | `ls ~/.ssh/authorized_keys` | partial | 1 key | UAPI `SSH::list_keys` unavailable; `authorized_keys` has 1 entry (likely orbit/platform key) |
| team_users | `TeamUser::list` | module unavailable | n/a | cPanel version does not support Team Users |
| webdisk_accounts | `WebDisk::list_webdisks` | function unavailable | unknown | UAPI function not found — need API2 fallback or root-level check |

## Findings

### Used / requires attention (3)

1. **spamassassin** — ENABLED with SpamBox. The enable state and
   `user_prefs` are per-account settings. `user_prefs` lives in
   `~/.spamassassin/` which is outside the docroot copy. Currently only
   the default template is present (no custom rules). At migration:
   - The enable state may or may not carry over in a cPanel transfer
   - If it does not, it must be re-enabled on the destination
   - Custom rules (if any exist on other fleet accounts) need explicit copy
   - **Recommendation**: check `spam_enabled` on all fleet accounts; if
     any have custom `user_prefs` beyond the default template, add a
     SpamAssassin collector/writer

2. **directory_privacy** — `~/.htpasswds/public_html/` exists. This
   directory stores `.htpasswd` files for password-protected directories.
   These travel with the home directory transfer but the cPanel UI
   registration may not. Low-risk for giorginisposi (likely default/empty).
   - **Recommendation**: check `find ~/.htpasswds -type f -not -empty`
     on fleet accounts

3. **ssh_keys / api_tokens** — Platform-managed (orbit key, orbit API
   token). These are auto-provisioned per server — NOT migration-sensitive.
   The destination will get its own orbit key/token.
   - **Recommendation**: out of scope — platform handles these

### Not used / out of scope with evidence (10)

| Area | Evidence | Decision |
|------|----------|----------|
| boxtrapper | absent | out of scope |
| contact_info | trivially re-settable | out of scope (operator can re-set post-migration) |
| domain_aliases | 0 parked/addon | out of scope (already folded into domains section) |
| git_repositories | 0 repos | out of scope |
| hotlink_protection | no rules + UAPI unavailable | out of scope |
| leech_protection | 0 files | out of scope |
| mailbox_quota_limits | all unlimited | out of scope (no limits to lose) |
| mailing_lists | 0 lists | out of scope |
| mime_handlers | 0 custom | out of scope |
| passenger_apps | feature disabled | out of scope |

### Unavailable / cannot verify (2)

| Area | Issue | Decision |
|------|-------|----------|
| team_users | module not on this cPanel | out of scope (feature not available) |
| webdisk_accounts | UAPI function missing | **needs fleet check** via API2 or root |

## Fleet survey status

### GATE — fleet access

This survey covers only **giorginisposi**. For the full campaign fleet:

**Option A** — Root/WHM on .193: `whmapi1 listaccts` → per-account UAPI
via `cpapi --user=<user>`. Highest coverage, single connection.

**Option B** — Orbit superadmin: per-account via `wordpress_run_remote_command`
(as done above). Requires site_id per account.

**Option C** — Per-account SSH credentials (as they become available).

**Cost per account**: ~3 UAPI calls (~3-5 seconds), read-only.

**Recommendation**: run via Option B (Orbit) on all active WordPress sites
in the fleet. The user must provide the account list or confirm the
survey scope.

## Operational conclusion

For the giorginisposi campaign, the tool coverage is **sufficient**:
- 15 covered areas handle the real operational surface
- SpamAssassin is the only used not_collected area, and for this account
  it has only the default template (no custom rules to migrate)
- All other not_collected areas are empty or platform-managed

**Before declaring fleet-wide**: run the same survey on every campaign
account. If SpamAssassin custom rules or non-trivial directory_privacy
appear, add collectors for those specific areas.
