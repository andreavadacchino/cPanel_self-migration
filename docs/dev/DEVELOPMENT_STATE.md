# Development State — cPanel Self-Migration (handoff)

Snapshot for starting a fresh development session. Last updated after
**UI-3** (apply/run monitor from events.jsonl, monitor-only).

**PR numbering note:** the 6x series is the DNS track (6C = `dns verify`,
6D = `dns apply` — both merged); the 7x series is the migration checklist
/ final verification track.

## What this tool is

A CLI (`cpanel-self-migration`) that migrates email, website files and
MySQL databases between two cPanel accounts over **user-level SSH only**.
The SOURCE host is always read-only; all writes target the DESTINATION;
default mode is dry-run. Module path
`github.com/tis24dev/cPanel_self-migration`, Go 1.25.

**Fork workflow (important):** work only on the fork
`andreavadacchino/cPanel_self-migration`. `origin` = `tis24dev` (no push
access). `fork` = `andreavadacchino` (push here). PRs target the fork's
own `main`; Sourcery reviews each PR; merge with `gh pr merge N --merge`.

## Roadmap so far (all merged to fork main)

| PR | What | Merge |
|----|------|-------|
| 1 | JSON events + report foundation (`--json-events`, `--report-json`) | — |
| 2 | Read-only account inventory skeleton (`--account-inventory`) | — |
| 3A | Email config inventory (forwarders, autoresponders) | — |
| 3B | FTP + SSL + PHP inventory collectors | — |
| 3C | DNS zone inventory (UAPI `DNS::parse_zone` + API2 fallback) | #1, #2 |
| 3D | Cron inventory (SSH `crontab -l`, redacted) | #3 |
| 4A | Offline `inventory diff` subcommand | #4 (contract test), #5 |
| 5A | Policy engine v0 (`inventory policy`) | #6 |
| 5B | Real-server hardening: cron `secure=` leak, FTP/SSL parsing | #7 |
| 5C | Collector audit: email disk usage, autoresponder hardening | #8 |
| 5D | `--fail-on-blockers`: `inventory policy` exits 3 when blocked | #9 |
| 6A | DNS import/verifier micro-design (v2 post adversarial review) | #11 |
| 6B-pre | real-server DNS capability captures (mass_edit_zone OK on v110) | #12 |
| 6B | `inventory dns-plan`: offline DNS import plan builder | #13 |
| 7A | `inventory checklist`: operator migration checklist v0 | #16 |
| 7A-smoke | real-data smoke on doctorbike.it captures (`PR7A_REAL_SMOKE.md`) | #17 |
| 6B-fix | dns-plan: TXT already matching the ip-map translation → skip (cyclic-map safe, single-pass substitution) | #18 |
| 7B | provenance chain: diff/policy record input hashes, checklist verifies `chain_verified` | #19 |
| 7A-ssl-fix | checklist SSL: expired source cert groups → expected, RFC 6125 wildcard coverage | #21 |
| 7C | apply evidence: phase events (+per-item data), report.json `phases_completed`/`artifacts`, checklist `per_item` | #22 |
| 7D | operator acceptances: stable action keys, acceptances.json, `--acceptances` (gate clearing, fail-safe) | #23 |
| UI-1 | `ui` subcommand: local read-only dashboard (checklist + staleness + artifacts), loopback-only | #24 |
| UI-2a | connections form (host.yaml) + run-from-browser (CLI subprocess pipeline), CSRF/rebinding gates | #25, #26 |
| UI-2b | accept manual actions from the browser (acceptances.json upsert + checklist regen) | #27 |
| 6C | `dns verify`: read-only per-op verification of destination zones against a dns plan (`--fail-on-drift`, stale-plan sha256 gate, `sshx.DialDest`, structural literal-names safety test) | #29 |
| UI-3 | apply/run monitor: dashboard tails events.jsonl (monitor-only, zero-JS, stall detection, bounded parse/render) | #30 |
| fix | dispatch: `inventory` missing/unknown subcommand → exit 2 + usage (was: silent fall-through to the migration flow); E2E dispatch tests via TestMain re-exec | #32 |
| 7E-pre | real-server captures for email routing / default address / filters / redirects (`PR7E_PRE_CAPTURES.md`: list_mxs local+remote pair, list_default_address covers subdomains, filters empty everywhere, Mime::list_redirects = .htaccess harvest with CMS noise) | #33 |
| 7E-1 | inventory sections email_routing / default_address / email_filters / redirects (4 read-only UAPI calls, filter bodies never in artifacts, deterministic tie-breaks, narrowed-scope warning); diff/policy/checklist unchanged until 7E-2 | #34 |
| 7E-2 | diff/policy/checklist wiring for the four 7E sections (per-item actions replace the blanket not_inventoried checks, CMS rewrite recognition, RECREATE_EMAIL_FILTERS + CONFIRM_REDIRECT action types) + DKIM CONFIRM_DNS_RECORD on plan replace (7A finding 3) | #35 |
| 7E-smoke | real-data smoke of the four 7E sections via offline capture replay (`PR7E_REAL_SMOKE.md`): all criteria pass — 20 CMS rewrites → expected differences, zero fake actions, blocking 11→8, DKIM CONFIRM_DNS_RECORD ×4, SPF still 0 manual, stale-dest guard holds, italplant remote routing clean + genuine 301 → one non-blocking CONFIRM_REDIRECT; 11 pre-7E sections multiset-identical to the 7A source (zero collector drift). Captures archived in `~/Desktop/pADV/cPanel_self-migration-captures/` | #38 |
| main→main | support the 1:1 same-domain migration layout (Fase 0.2 blocker): classifier carve-out `sameNameMainToMain` + webfiles guard per-item opt-in `AllowDestPublicHTMLRoot`/`ALLOW_PUBLIC_HTML_ROOT=1` (backup path still refuses the root even with the flag). Found by the first real dry-run; 2× adversarial go-review APPROVE; Docker LINUX_ALL_GREEN (gate: Sourcery rate-limited) | #40 |
| Fase 0.2 | **first real `--apply`** (giorginisposi .193→.78): 14/14 phases green — mail 379 msg (body-hash verified), web 12 521 entries/1.1 GB into the public_html root, db 32 tables + wp-config rewrite; site serves on .78 (`curl --resolve`). First checklist with REAL apply evidence: `chain_verified`, per_item evidence on all 4 migrated sections, BLOCKED with 6 genuine manual actions (forwarder, NS, catch-all, DKIM, PHP). Full story + campaign gotchas (DNS cluster on .78!) in `FASE0_2_FIRST_APPLY.md` | #41 |
| 1A | coverage manifest: static registry (`coverage.go`) of every known area — 15 covered, 2 root_only, **18 not_collected** (17 after 2B-3) — embedded as declarative `coverage_manifest` + `## Coverage` MD table; lockstep test pins registry↔sections; zero effect on verdict/actions (byte-identical on the real giorginisposi artifacts). Replaces the SKIPPED 0.3 census as the campaign safety net (decision: user, 2026-07-03 — 2A/2B priorities already proven by the real 0.2/7A evidence) | #42 |
| 2B-pre | email write primitives byte-verified on the sacrificial .78 account (`PR2B_PRE_CAPTURES.md`): add_forwarder/delete_forwarder/set_default_address params confirmed, double-add DEDUPES (idempotent-safe), fresh default = account username; REAL MA-001/MA-006 values applied → pipeline re-run shows both actions gone by real convergence (6→4 manual actions), 7D AK keys stable | #45 |
| 2B-1 | **the first config writer**: `inventory email-plan` (offline plan: create/set/skip/manual for forwarders + default address, 2B-2/2B-3 sections carried as manual, fresh-default heuristic prefix-matched) + `email apply` (new `email` namespace, DEST-only via `sshx.DialDest`; offline dry-run default, backup-or-nothing bidirectionally paired to the report, per-op freshness guard with outcome-first convergence, unconditional verify-after, report-driven `--rollback` incl. `--accept-report-loss` degradation) + `email verify` (read-only, stale-plan sha256 gate, `--fail-on-drift`). Writers via RunUAPI literal names; `RunUAPIRaw` added (+ literal-names guard extended); module-wide email verb scan with the FIRST per-file allowlist. Real smoke on .78 (`PR2B_1_SMOKE.md`): plan = exactly MA-001/MA-006, live convergence (2 already_present, no backup), real applied write + backup + verify CLEAN, rollback dry-run inverts only the own applied create | #47 |
| 2B-2 | **autoresponder collector + writer**: body collector via `get_auto_responder` (closes the 1A `autoresponder_bodies` not_collected line; fixes the latent `email+"@"+domain` concatenation bug — real list rows carry the FULL address and no domain field; `BodyCollected` honesty marker, per-address failure degrades to a warning) + plan create/skip/manual now provable on bodies (trailing-newline-normalized equality per the byte-verified storage semantics; differing dest content = terminal manual because `add_auto_responder` UPSERTS) + writer/apply/verify/rollback (fresh per-address re-check IMMEDIATELY before each write — go-review HIGH: the batch snapshot alone left a destructive race window; rollback inverse = delete guarded by content equality). `delete_auto_responder` consciously added to all three forbidden-verb scans. 2B-2-pre byte-verify in `PR2B_2_PRE_CAPTURES.md` (26 probes, upsert/ensure-trailing-newline/absent-get facts). Real smoke on .78 (`PR2B_2_SMOKE.md`): applied + verify CLEAN (accented multiline body round-trips byte-identical) + convergence + **first LIVE rollback ever executed** (closes the 2B-1 residual) — post-smoke state = pre-smoke | #49 |
| 2B-3 | **email filter rules collector + writer + routing plan** (closes the 2B email track): filter rules in clear (option A, user gate decision) — `EmailFilterEntry` enriched with `Rules`/`Actions` typed fields + `RulesCollected` honesty marker (pattern: autoresponder `BodyCollected`); collector calls `get_filter` per listed filter with graceful degradation; `email_filter_rules` exits `not_collected` in coverage.go; `FilterRule`/`FilterAction` typed structs. `planFilters` replaces the manual stub: single-rule create/skip/manual based on rule+action equality; multi-rule → MANUAL (`match_type` AND/OR not round-trippable — 2B-3-pre fact 10); different dest content → MANUAL (upsert, fact 5). `StoreFilter`/`DeleteFilter` writers via `RunUAPI` (byte-verified param names). `planRouting` now produces `set` ops; `SetMXCheck` writer via `RunAPI2` ready but smoke-blocked (cpapi2 broken on .78 — fact 11). Apply/verify/rollback extended for filters + routing. `delete_filter` added to all 3 forbidden-verb scans. 2B-3-pre in `PR2B_3_PRE_CAPTURES.md` (29 probes across 6 rounds). Gate: go-reviewer adversarial R1 (1 HIGH + 3 MEDIUM → fixed) → R2 APPROVE; Docker LINUX_ALL_GREEN ×2; Sourcery rate-limited → gate: go-reviewer + Docker | #50 |
| 2A | **cron collector in clear + offline plan + writer + safety** (+ bonifica 2B-3): cron commands in clear (option A) — `CommandClear`/`CommandCollected` on CronJobEntry, `ValueClear`/`ValueCollected` on CronEnvEntry; display invariant (redacted only in diff/policy/checklist). `cronplan.go` offline plan builder: create/skip/manual per job with `/home/<srcuser>/` → `/home/<destuser>/` path adaptation; disabled → MANUAL; different schedule → MANUAL; env lines with same pattern. `InstallCrontab` writer via SSH `crontab -` (pipe stdin, env var for content, no injection); `ReadCrontabRaw` single SSH call. Safety: `cronplan_safety_test.go` (offline guard), `cron_safety_test.go` (allowlist + unconditional `crontab -r` ban), checklist writeCalls extended. Bonifica: PR2B_3_SMOKE.md (posture), CPAPI2_DIAGNOSIS_78.md (cpapi2 broken, HTTP workaround found), HANDOFF rewritten. 2A-pre in `PR2A_PRE_CAPTURES.md` (11 probes on .78: round-trip byte-identical, append/remove/UTF-8 OK). Gate: go-reviewer R1 (1C + 3H + 5M → fixed) → R2 APPROVE; Docker LINUX_ALL_GREEN ×2 | #51 |
| 6D | **DNS apply writer — the ONLY DNS writer** (closes the DNS track): `MassEditZoneAdd`/`MassEditZoneRemove` via `RunUAPI` with indexed param format (`add-0=<JSON>`, `remove-0=<int>` — 6D-pre fact 1); `ExtractSOASerial` (base64 decode from parse_zone SOA); `IsStaleSerialError` detection (exact string from 6D-pre fact 3); `FetchDNSZoneRaw` for backup. First DNS allowlist in `dns_safety_test.go` + `TestDNSWriteAllowlistFilesExist` guard + `mass_edit_zone` in checklist writeCalls. cpapi2 fixed via CageFS disable (root session: cause was CageFS isolation, not jailshell; `cagefsctl --disable giorginisposi`). 6D-pre in `PR6D_PRE_CAPTURES.md` (add/remove round-trip, stale-serial error, non-propagation, peer standalone). Gate: go-reviewer R1 (0 HIGH, 2 MEDIUM → fixed); Docker LINUX_ALL_GREEN | #52 |
| smoke-total | **ALL writer primitives LIVE-PROVEN** + cutover runbook: DNS (add→verify→non-propagation→remove on .78), routing SetMXCheck via RunAPI2, cron InstallCrontab, filter StoreFilter+DeleteFilter. CUTOVER_RUNBOOK.md: per-account repeatable sequence with pre-conditions, rollback for every step | #53 |
| cli-wiring | **CLI wiring for all writer primitives**: `dns apply` (forward + rollback + verify-after), `cron apply` (+ cron verify + inventory cron-plan), email filter/routing sections of `email apply`. All 7 writers binary-proven. Pipeline CLI complete | #54 |
| dns-v2 | **DNS apply v2**: `replace` strategy (remove+add atomic within a single `mass_edit_zone` call) for existing records that need updating (DKIM regeneration, SPF edits). `edit` strategy as fallback discussion — deferred per review. Coverage: replace end-to-end proven on real records | #56 |
| workbench | **Migration Session Model**: `internal/workbench` package — session lifecycle governance (14 statuses, 12 steps, transition matrix), artifact registry (17 known kinds, copy+SHA256, atomic writes), timeline. `migration` CLI namespace (init/list/show/set-status/attach-artifact/archive). JSON file-based storage (atomic write-temp+fsync+rename), 0700/0600 permissions, no SQLite, no sshx/cpanel imports. Safety tests (import scan, credential-field absence, write-verb scan). Foundation for Single Account Workbench UI | #57 |
| workbench-ui | **Single Account Workbench UI**: browser governance dashboard extending the existing `ui` server. Sessions list + detail page (overview, artifacts, timeline, Apply Center with copy-paste commands, governance forms). POST handlers for status transitions (CSRF-protected, transition matrix enforced) and artifact attachment (kind whitelist, path validation). Sentinel errors (ErrSessionNotFound etc.), List() returns warnings. Safety: no sshx/cpanel imports in webui, no exec.Command in workbench handler, XSS escaping tested, path traversal rejected | — |

## The full pipeline (all read-only / offline)

```
cpanel-self-migration --account-inventory   → inventory_source.json (+ _destination, report.md)
cpanel-self-migration inventory diff         → inventory_diff.json + .md
cpanel-self-migration inventory policy        → policy_report.json + .md
cpanel-self-migration inventory dns-plan      → dns_import_plan.json + .md
cpanel-self-migration inventory checklist     → migration_checklist.json + .md
```

The inventory has 11 sections: account, domains, mailboxes, databases,
forwarders, autoresponders, ftp, ssl, php, dns, cron. Diff compares them
deterministically; policy classifies each difference as
blocker/review/warning/info → overall `ready|review_required|blocked`.
None of the three commands connect to a server except
`--account-inventory` (which reads over SSH). `inventory policy
--fail-on-blockers` exits 3 when `overall_status` is `blocked` (reports
are still fully written first; `review_required` never gates), so the
pipeline can gate CI without JSON parsing.

`inventory checklist` (PR 7A) composes inventories + diff + policy
(+ optional dns-plan and `--apply` report.json) into the operator
migration checklist: per-area statuses, expected differences, manual
actions with IDs, and an overall
`BLOCKED|MANUAL_ACTION_REQUIRED|NOT_READY|READY_WITH_MANUAL_NOTES|READY_TO_CUTOVER`
rollup; `--fail-on-not-ready` exits 3 unless READY_*. Honesty invariants
(pinned by tests): `migrated_by_tool` never true without a successful
apply report; evidence is `per_item` when the report's
`phases_completed` proves both the migrate and the verify phase of the
flow completed (PR 7C), `run_level` otherwise; a dns-plan proves "expected" only via action `skip`;
root-only areas (quota/package, server config) surface as explicit
sections instead of silently reading ok. Since PR 7E the former
non-inventoried areas (email routing, default address, filters,
redirects) are real inventoried sections: per-item actions replace the
blanket manual checks, CMS `.htaccess` rewrites are recognized as
expected differences, and a regenerated DKIM key (plan `replace` on a
`_domainkey` TXT) raises a dedicated non-blocking CONFIRM_DNS_RECORD
action (7A smoke finding 3).

Provenance chain (PR 7B): `inventory diff` records
`source_sha256`/`destination_sha256`, `inventory policy` records
`input_diff_sha256` (raw file bytes); the checklist verifies every link
(diff→inventories, policy→diff, dns-plan→inventories) against the files
it composes. All match → `chain_verified: true`. Missing hashes
(pre-7B artifacts) → warning, no gating. A PROVEN mismatch → explicit
warning and any READY_* verdict capped to NOT_READY (the cap never
improves a worse verdict).

## Architecture map

- `cmd/cpanel-self-migration/main.go` — flags + subcommand dispatch
  (`inventory diff|policy` handled before global flag parsing).
- `internal/cpanel/` — cPanel API layer. `Runner` interface is the SSH
  seam. `RunUAPI[T]`/`parseUAPI[T]` (UAPI), `RunAPI2[T]`/`parseAPI2[T]`
  (cpapi2 CLI). Per-feature files: `domains.go`, `email*.go`, `ftp.go`,
  `ssl.go`, `php.go`, `mysql.go`, `dns_zones.go`, `cron.go`, `token.go`,
  `addon.go`. Flexible decoders in `types.go`: `flexInt64` (number OR
  quoted string OR float→trunc), `flexStringList` (string OR array).
- `internal/accountinventory/` — `Collect()` orchestrates all collectors;
  `types.go` (normalized schema), `collector.go`, `write.go` (report),
  `diff.go`+`diff_write.go` (PR4A), `policy.go`+`policy_write.go` (PR5A),
  `dnsplan.go`+`dnsplan_write.go` (PR6B),
  `checklist.go`+`checklist_types.go`+`checklist_write.go` (PR7A).
- `internal/migrate/runner.go` — the migration orchestrator. **Off-limits
  to the inventory/diff/policy line of work** (do not modify).
- `internal/sshx/` — real SSH transport; `internal/sshtest/` — in-process
  SSH exec server for end-to-end tests without a real daemon.

## Hard-won real-server facts (cPanel 110.0 build 131, server .193)

These broke synthetic-fixture assumptions and cost real bugs — respect
them when adding collectors:

1. **`DNS::parse_zone` DOES work on v110** (the "requires v136" note was
   wrong). API2 `ZoneEdit::fetchzone_records` fallback still needed for
   other builds. API2 returns numeric fields as **quoted strings**
   (`ttl:"14400"`, `preference:"0"`). DNS TXT (DKIM) is split into
   255-char `data_b64` segments — must be RFC1035-joined.
2. **FTP `diskused`** = quoted string `"57632.08"` on some accounts, bare
   float `13558.40` on others → use `flexInt64`.
3. **SSL `domains`** = an **array** (SAN list), not a string → `flexStringList`.
4. **Email `list_pops_with_disk`** has NO `diskusedquota`; disk is in
   `_diskused` (bytes, quoted string).
5. **Subdomains have no DNS zone of their own** — `parse_zone` on a
   subdomain returns "You do not control a DNS zone". The collector skips
   `Type=="sub"` for DNS (correct).
6. **Cron redaction must cover `secure=`** as well as `token=` — real
   PrestaShop cron jobs authenticate with `secure=<token>`.
7. **Destination server .78 is a member of the production DNS cluster**
   (peer ns.hostnuoviclienti.com, normally role `sync` — currently flipped
   to `standalone` for the pre-cutover window, backup on keliweb2). Any zone
   save on .78 with sync active reaches the production NS. `createacct`
   refuses cluster-existing domains (disable clustering for the ~30 s window);
   `killdns` cannot remove an account's primary-domain zone; ⚠️ `removeacct`
   with clustering active DELETES the production zone. Full runbook in
   `FASE0_2_FIRST_APPLY.md`.

**General lesson:** any cPanel numeric field can arrive as a quoted
string or float; default to `flexInt64` for informational numbers and
`flexStringList` for maybe-array strings. Synthetic fixtures repeatedly
hid these — validate new collectors against real captures.

## Testing conventions

- TDD throughout: fixture → RED test → fix → GREEN.
- Real-server-shape fixtures live in `internal/testdata/*_realserver.json`
  with tests in `internal/cpanel/realserver_test.go`.
- Safety tests assert read-only invariants (`dns_safety_test.go`,
  `cron_safety_test.go` — no write verbs; module-wide source scan).
- Determinism: every diff/policy output list is fully sorted.
- Redaction: secrets are masked before storage; hashes are computed over
  the REDACTED text (no brute-force oracle).
- Markdown cells go through `mdCell` (pipe-escape + CR/LF collapse +
  rune-safe truncation).
- **Known-failing on macOS (NOT regressions):** `internal/dbmig`,
  `internal/maildir`, `internal/migrate`, `internal/webfiles` — they run
  bash/sed scripts that need GNU tools / bash≥4. Always diff them against
  `main` to confirm zero changes before blaming your PR.

Verify commands:
```
go test ./internal/cpanel/ ./internal/accountinventory/ ./cmd/...
go test ./...
go vet ./...
go build ./cmd/cpanel-self-migration
```

## Smoke-testing against the real server

Direct SSH from the dev Mac is refused (keys rejected for
`onlinerincipiadv`). To exercise the real `Collect` code on real data:
capture cPanel responses **read-only via Orbit** (`superadmin_start_session`
with a TOTP, then `wordpress_run_remote_command` running `uapi …` /
`cpapi2 …` / `crontab -l`), save one file per API call into a capture
dir, and replay them through `accountinventory.Collect` with a small
throwaway `Runner` test (see git history of PR5B/5C for the harness — it
is intentionally never committed). **The Orbit gateway masks
emails/paths/IPs in command output and the masking corrupts JSON:
base64-encode every capture in transit (`uapi … | base64 -w0`), decode
locally, validate with a JSON parse** (learned in the 7A smoke,
`PR7A_REAL_SMOKE.md`). Diff/policy then run offline with the
real binary. Accounts must be registered in Orbit to be reachable;
`turtlebeachandora.com`/`fidopetstore.it` exist on the server but are NOT
in Orbit — `doctorbike.it` and `italplant.com` are and were used.

## Suggested next steps

- **Campaign Mode v0**: gated orchestrator that sequences inventory →
  diff → policy → plan → apply → verify per-account with approval gates.
  Prerequisite: the three user decisions (sync variant, date/window,
  account order).
- **SpamAssassin collector/writer** (if fleet survey shows custom
  `user_prefs` beyond default template on any account — see
  `FLEET_COVERAGE_SURVEY.md`).
- **LOW follow-ups from the #34/#35 go-reviews** (non-blocking):
  (a) diff keys use space/slash separators — NUL-framed keys would be
  collision-proof; (b) CMS-rewrite exemption applies only to Removed
  redirects; (c) email filter `-CHANGED` findings gate via review but
  get no dedicated action.
- **DBUserEntry `shortuser` vs `short_user`** — harmless today, fix if
  `ListDBUsers` ever feeds the inventory.

## Operational context (from project CLAUDE.md)

Real production infra managed by Principi S.r.l. Uptime > security >
functionality > optimization. Server .193 = Keliweb VPS (Intel i7 4-core,
55 cPanel accounts, cPanel v110, CentOS 7 EOL). Every server intervention
must classify risk, back up first (medium/high), define a <60s rollback,
and be documented via Orbit `create_intervention`. The inventory/diff/
policy line of work is **read-only** and low-risk by construction.
