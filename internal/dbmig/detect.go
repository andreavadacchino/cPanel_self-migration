package dbmig

import (
	"context"
	"fmt"
	"strings"
)

// DetectedApp is a database-backed application whose DB-config FORMAT this tool does not
// DISCOVER or rewrite at all (so it never even produces a [db config MANUAL] line). The
// database may still migrate, but the site's stored DB credentials are NOT rewritten and
// NOT verified — the migrated site keeps pointing at the SOURCE database name/user. It is
// surfaced as a manual action so it can never read as a silent clean cutover.
type DetectedApp struct {
	Docroot string // the source docroot the app was found under
	App     string // human name, e.g. "Magento 1", "PrestaShop 1.7+", "Symfony", "SilverStripe"
	Marker  string // the marker file (relative) the operator must edit by hand
}

// detectUnmigratedScript is a read-only SOURCE probe that emits one "App|marker" line per
// recognized-but-unhandled DB-config marker under a docroot. Each marker pairs an
// existence test with a distinctive content needle so a benign file is not mis-flagged.
// These are exactly the formats parseCMSConfig does NOT read (no XML parser for Magento 1's
// local.xml; only PrestaShop 1.6's define(), not 1.7's parameters.php; only Laravel's
// discrete DB_* keys, not a Symfony DATABASE_URL DSN; no SilverStripe SS_DATABASE_*). The
// .env DSN is also checked in .env.local (Symfony/Laravel keep the real prod creds there).
// The docroot is passed via $DOCROOT (never interpolated); a missing docroot prints nothing.
const detectUnmigratedScript = `set -u
d="$DOCROOT"
[ -d "$d" ] || exit 0
if [ -f "$d/app/etc/local.xml" ] && grep -qE '<connection>|<dbname>' "$d/app/etc/local.xml" 2>/dev/null; then
  printf '%s\n' 'Magento 1|app/etc/local.xml'
fi
if [ -f "$d/app/config/parameters.php" ] && grep -qE "^[[:space:]]*['\"]?database_name['\"]?[[:space:]]*(=>|:)" "$d/app/config/parameters.php" 2>/dev/null; then
  printf '%s\n' 'PrestaShop 1.7+|app/config/parameters.php'
fi
for e in .env .env.local; do
  f="$d/$e"
  [ -f "$f" ] || continue
  if grep -qE '^[[:space:]]*DATABASE_URL=' "$f" 2>/dev/null; then printf '%s\n' "Symfony|$e (DATABASE_URL)"; fi
  if grep -qE '^[[:space:]]*SS_DATABASE_NAME=' "$f" 2>/dev/null; then printf '%s\n' "SilverStripe|$e (SS_DATABASE_*)"; fi
done
`

// DetectUnmigratedConfigs scans the SOURCE docroots (read-only) for DB-config formats this
// tool does not discover/rewrite, returning one DetectedApp per marker found. A docroot is
// SKIPPED when it is covered by handled — a path of a docroot that already yielded a
// recognized, rewritable DB config — so a healthy install whose docroot merely also
// contains a stray marker is not flagged. Coverage is containment-aware (trailing slashes
// normalized; a handled path equal to, nested under, or a parent of the docroot all count),
// because a handled config's recorded path is not always byte-identical to the scanned
// DocumentRoot (e.g. a Softaculous install path can be a subdirectory). It never writes.
func DetectUnmigratedConfigs(ctx context.Context, src Runner, docroots []string, handled []string) ([]DetectedApp, error) {
	var out []DetectedApp
	seen := map[string]bool{}
	for _, dr := range docroots {
		if dr == "" || seen[dr] || docrootIsHandled(dr, handled) {
			continue
		}
		seen[dr] = true
		res, err := src.RunScript(ctx, detectUnmigratedScript, map[string]string{"DOCROOT": dr})
		if err != nil {
			return nil, fmt.Errorf("detect unmigrated configs in %s: %w", dr, err)
		}
		// One DetectedApp per (docroot, App): a site that holds the same DSN in both .env
		// and .env.local emits the marker twice, but it is ONE site to flag — dedupe so the
		// count and the report lines are not doubled.
		seenApp := map[string]bool{}
		for _, line := range strings.Split(string(res), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			app, marker, _ := strings.Cut(line, "|")
			if seenApp[app] {
				continue
			}
			seenApp[app] = true
			out = append(out, DetectedApp{Docroot: dr, App: app, Marker: marker})
		}
	}
	return out, nil
}

// docrootIsHandled reports whether docroot dr is covered by any handled config path:
// equal to it, nested under it, or a parent of it (trailing slashes normalized, boundary
// aware so a mere shared name prefix does not match). Leaning toward "handled" is the safe
// direction — it suppresses a possible false positive rather than failing a healthy run.
func docrootIsHandled(dr string, handled []string) bool {
	dr = strings.TrimRight(dr, "/")
	if dr == "" {
		return false
	}
	for _, h := range handled {
		h = strings.TrimRight(h, "/")
		if h == "" {
			continue
		}
		if h == dr || strings.HasPrefix(h, dr+"/") || strings.HasPrefix(dr, h+"/") {
			return true
		}
	}
	return false
}
