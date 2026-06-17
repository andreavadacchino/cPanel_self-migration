package dbmig

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// staleScanTimeout bounds a single containment grep. sshx.RunScript imposes no
// per-command timeout (only the run context, which has no deadline), so a grep over
// a large docroot could otherwise stall the apply. On a deadline the scan returns an
// error, which the caller treats best-effort (keep the existing verdict) — never a
// demote, so a slow docroot can never turn a clean cutover into a false failure.
const staleScanTimeout = 45 * time.Second

// staleSourceNameScanScript greps, READ-ONLY, for a WHOLE-WORD fixed string in a
// docroot's PHP/env/ini config files, printing the first matching file path. ROOT
// and NEEDLE arrive as env vars (never interpolated). -w avoids substring false
// positives (a source name that is a prefix/suffix of the destination name, e.g.
// "u_wp" inside "u_wp2"); -F matches the needle literally (no regex meta); -l stops
// at the first hit per file. An absent root is a clean no-op.
//
// --exclude-dir prunes dependency/VCS trees that NEVER hold a site's live DB config
// (vendor, node_modules, .git) and BACKUP stores (*backup* — e.g. a WP-security plugin's
// aiowps_backups, UpdraftPlus updraft_backups): a stale source DB name surviving only in
// a backup copy is not a cutover gap (the live site never loads it), so pruning those
// dirs avoids a false demote AND keeps a large backup tree from dominating the bounded
// result. Deliberately NOT pruned: CACHE dirs (a Laravel `config:cache` writes the
// resolved creds to bootstrap/cache/config.php — exactly a leak we want to catch) and
// `uploads` (where a non-backup-named stale copy could land). The --include set limits
// the scan to the config file types that carry DB creds, including `.env.*` overrides
// (.env.local/.env.production) that `*.env` alone misses.
//
// IMPORTANT — only --exclude-DIR is used, NOT --exclude file patterns. grep resolves the
// --include/--exclude file set by "the last matching option wins; if NONE matches, the file
// is included UNLESS the first such option is --include" (documented behaviour, every GNU
// grep version — verified to leak identically on 3.6 and 3.11, NOT a version bug that a
// newer grep "fixes"). So putting any --exclude file pattern ahead of the --include set
// flips the default: a file matching neither (a PHP `error_log`, a `wp-config.php.bak2`
// rotation — which merely quote/copy a `WordPress database error … 'srcdb.tbl'`) is then
// INCLUDED and leaks in as a false residual that demotes a correct cutover. Backup/log/
// rotation NAME filtering is therefore done Go-side in isNonLiveConfigPath; only --exclude-DIR
// (a SEPARATE directory-pattern set, unaffected by the ordering rule) prunes whole backup/
// dependency trees grep-side. Do NOT re-add --exclude file patterns on any grep version.
//
// Robustness: grep runs into a captured variable (not piped to head) so its REAL exit
// status is observed. The gate is "EMPTY output AND rc>=2": a genuine grep failure that
// produced nothing usable (a non-GNU grep that rejects --exclude-dir/--include, or every
// candidate dir unreadable) exits non-zero, which the caller treats best-effort (keep the
// existing verdict) instead of a `| head` masking it as a silent false "clean". But a
// NON-EMPTY result is ALWAYS used even when rc>=2: GNU grep also exits 2 when it merely
// could not descend ONE unreadable subdir while still printing the matches it found, and
// a found leak is dispositive — discarding it would re-suppress a real residual that lives
// in a readable file. rc=1 (no match) and rc=0 (matches) print up to 2000 paths (head
// -2000): far above any real count of config files that legitimately carry a source DB
// name, so the Go-side filters (planned configs + backup/log copies via isNonLiveConfigPath)
// are never starved of a genuine residual by leading benign hits.
const staleSourceNameScanScript = `set -u
r="$ROOT"
[ -d "$r" ] || exit 0
out=$(grep -rlwF --exclude-dir=vendor --exclude-dir=node_modules --exclude-dir=.git --exclude-dir='*backup*' --include='*.php' --include='*.php5' --include='*.phtml' --include='*.env' --include='.env.*' --include='*.ini' --include='*.inc' -- "$NEEDLE" "$r" 2>/dev/null)
rc=$?
[ -z "$out" ] && [ "$rc" -ge 2 ] && exit "$rc"
printf '%s' "$out" | head -2000
exit 0
`

// SourceCredsStillReachable scans the destination docroot (READ-ONLY) for the
// SOURCE database name / user after the config cutover. A correct rewrite leaves
// neither reachable in any config file — it pointed the discovered config at the
// destination DB. A lingering source name/user is therefore EVIDENCE that the value
// PHP actually uses is NOT where the rewrite acted: a split config reached via
// include()/require(), a Laravel `config:cache` shadow (bootstrap/cache/config.php),
// a Drupal settings.local.php override, or a second un-rewritten definition.
//
// This is the PHP-free, evidence-based net for the V35 include/runtime residual: it
// resolves no include paths and follows nothing, it only looks for the symptom, so
// it can only ADD scrutiny (the caller demotes a hit to UNVERIFIED) and can never
// certify a cutover green. A needle equal to its destination counterpart (no remap)
// is skipped — it would match the legitimately-rewritten value; when NEITHER the name
// nor the user is remapped there is no needle and the scan is a no-op (a host/password-
// only change carries no stable literal to look for — out of this scan's scope).
//
// Two classes of hit are NOT a cutover gap and are filtered out, so they no longer fail
// an otherwise-correct run: (1) a path in ignorePaths — a config that is in THIS
// migration's OWN rewrite plan (a sibling like a docroot's wp-config.php plus a
// test/wp-config.php both on this DB); it is independently rewritten and certified, so
// finding the old name there mid-process is transient ordering, not an un-acted-on live
// value. (2) a non-live file (isNonLiveConfigPath: a *backup* dir, a backup-style name,
// or a PHP error_log/*.log that merely quotes a DB-error message) that the live site never
// loads. Everything else — a split include, a Laravel
// cache, a Drupal override, a genuine second definition — still demotes. Best-effort: a
// grep error or the scan timeout is returned so the caller keeps the existing verdict.
func SourceCredsStillReachable(ctx context.Context, dest Runner, docroot, srcDB, destDB, srcUser, destUser string, ignorePaths []string) (found bool, reason string, err error) {
	if docroot == "" {
		return false, "", nil
	}
	ignore := make(map[string]struct{}, len(ignorePaths))
	for _, p := range ignorePaths {
		ignore[p] = struct{}{}
	}
	var needles []struct{ kind, val string }
	if srcDB != "" && srcDB != destDB {
		needles = append(needles, struct{ kind, val string }{"DB name", srcDB})
	}
	if srcUser != "" && srcUser != destUser {
		needles = append(needles, struct{ kind, val string }{"DB user", srcUser})
	}
	for _, n := range needles {
		out, e := runScanBounded(ctx, dest, n.val, docroot)
		if e != nil {
			return false, "", fmt.Errorf("scan %s for stale source %s %q: %w", docroot, n.kind, n.val, e)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			p := strings.TrimSpace(line)
			if p == "" {
				continue
			}
			if _, planned := ignore[p]; planned {
				continue // in this DB's own rewrite plan — independently rewritten/certified
			}
			if isNonLiveConfigPath(p) {
				continue // a backup/old copy or an error log, never the live config the site loads
			}
			return true, fmt.Sprintf("source %s %q is still reachable in %s after the rewrite — the live DB value may be set there (a split/included config, a Laravel config cache, or a second un-rewritten definition), not where the cutover wrote it", n.kind, n.val, p), nil
		}
	}
	return false, "", nil
}

// isNonLiveConfigPath reports whether a scan hit is NOT a live site config — a stale source
// DB name surviving only there is not a cutover gap (the site never loads it). It covers two
// classes the containment scan must ignore:
//   - BACKUP / old copies: a "backup" directory segment (aiowps_backups, updraft_backups), a
//     "backup"-named basename (backup.wp-config.php, wp-config-backup.php), or a backup /
//     editor-swap suffix — .bak / .old / .orig / .save / .swp / .swo / ~ — INCLUDING a
//     rotation number (wp-config.php.bak2, config.old.1) or a ".bak."/".old."/".orig." infix.
//   - LOG files: a literal error_log / php_errorlog name, or a *.log suffix — a WordPress/PHP
//     error log merely quotes a "WordPress database error … 'srcdb.tbl'" message; never a live
//     config. (NOT an "error_log" substring: a live error_log.php must still be scrutinized.)
//
// (Planned sibling configs are filtered separately by the caller's ignorePaths.)
//
// This filtering lives Go-side, not as grep --exclude file patterns: grep applies --include
// /--exclude by "last match wins, else included unless the FIRST option is --include" (a
// documented, version-independent ordering rule — see staleSourceNameScanScript), so adding any
// --exclude file pattern ahead of the --include set makes every unmatched file (error_log,
// wp-config.php.bak2) scanned. Keep it conservative: it SUPPRESSES a hit, and the caller runs
// this scan only AFTER the destPath config re-read already passed — so a misclassified live
// file here is one whose value was NOT independently re-verified and could false-certify a real
// residual. Hence it matches only unambiguous non-live shapes (genuine backup stores, backup/
// swap/rotation names, error logs), never a plausibly-live config. Pure; unit-tested.
func isNonLiveConfigPath(p string) bool {
	lower := strings.ToLower(p)
	base, dir := lower, ""
	if i := strings.LastIndexByte(lower, '/'); i >= 0 {
		base, dir = lower[i+1:], lower[:i]
	}
	// A backup/snapshot DIRECTORY anywhere in the path.
	for _, seg := range strings.Split(dir, "/") {
		if strings.Contains(seg, "backup") {
			return true
		}
	}
	// PHP/CMS error logs (and any *.log): a logged DB-error message, never a live config.
	// Match the log NAMES exactly (cPanel writes a literal "error_log"; some stacks write
	// "php_errorlog") or a ".log" suffix — NOT a "error_log" substring, which would wrongly
	// suppress a LIVE PHP file like error_log.php / my_error_log_viewer.php that --include scans.
	if base == "error_log" || base == "php_errorlog" || strings.HasSuffix(base, ".log") {
		return true
	}
	// Backup-named copies, and any editor backup ending in "~" (config.php~).
	if strings.Contains(base, "backup") || strings.HasSuffix(base, "~") {
		return true
	}
	// Backup / editor-swap / rotation suffix, tolerating a trailing rotation number or "~"
	// (so ".bak2" / ".old.1~" read as ".bak" / ".old").
	stem := strings.TrimRight(base, "0123456789~")
	for _, suf := range []string{".bak", ".old", ".orig", ".save", ".swp", ".swo"} {
		if strings.HasSuffix(stem, suf) {
			return true
		}
	}
	for _, infix := range []string{".bak.", ".old.", ".orig.", ".save."} {
		if strings.Contains(base, infix) {
			return true
		}
	}
	return false
}

// runScanBounded runs one containment grep under staleScanTimeout so a huge docroot
// cannot stall the apply.
func runScanBounded(ctx context.Context, dest Runner, needle, docroot string) ([]byte, error) {
	sctx, cancel := context.WithTimeout(ctx, staleScanTimeout)
	defer cancel()
	return dest.RunScript(sctx, staleSourceNameScanScript, map[string]string{"ROOT": docroot, "NEEDLE": needle})
}
