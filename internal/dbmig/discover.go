package dbmig

import (
	"context"
	"fmt"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// maxConfigDepth bounds how deep under a docroot we look for wp-config.php. A
// WordPress install keeps it at the docroot root, but a second install can live
// in a subdirectory (e.g. docroot/test/wp-config.php), so a small depth catches
// those without scanning the whole tree.
const maxConfigDepth = 3

// findConfigsScript returns the read-only SOURCE script that lists every known
// CMS configuration file under a docroot (up to maxConfigDepth), one absolute
// path per line. It searches all of cmsConfigNames (WordPress, Joomla, Drupal,
// Laravel, …), so non-WordPress sites are covered too. The docroot is passed via
// $DOCROOT (never interpolated). Missing docroot prints nothing (exit 0).
func findConfigsScript() string {
	// Build a single find expression: \( -name a -o -name b ... \) -type f.
	names := make([]string, len(cmsConfigNames))
	for i, n := range cmsConfigNames {
		names[i] = "-name '" + n + "'"
	}
	expr := strings.Join(names, " -o ")
	return fmt.Sprintf(`set -u
d="$DOCROOT"
[ -d "$d" ] || exit 0
find "$d" -maxdepth %d \( %s \) -type f 2>/dev/null
`, maxConfigDepth, expr)
}

// catScript reads a single file to stdout, read-only. Path via $FILE.
const catScript = `cat "$FILE" 2>/dev/null`

// DiscoverSiteCreds scans the given SOURCE docroots (read-only) for
// wp-config.php files, parses each, and returns one SiteCreds per config found.
// A docroot can yield several configs (shared/nested installs) or none. It never
// writes. Paths come straight from `find`; values are parsed in Go.
//
// docroots maps a label (typically the domain) -> absolute docroot path; only
// the path is used here (the label is for the caller's logging).
func DiscoverSiteCreds(ctx context.Context, src Runner, docroots []string) ([]SiteCreds, error) {
	var out []SiteCreds
	seen := map[string]bool{} // dedupe configs reached via overlapping docroots
	for _, dr := range docroots {
		if dr == "" {
			continue
		}
		paths, err := listConfigs(ctx, src, dr)
		if err != nil {
			return nil, err
		}
		for _, p := range paths {
			if seen[p] {
				continue
			}
			seen[p] = true
			content, err := readFile(ctx, src, p)
			if err != nil {
				logx.Debug("DiscoverSiteCreds: skip unreadable %s: %v", p, err)
				continue
			}
			// Dispatch to the right CMS parser by filename. A file with no DB
			// credentials (e.g. a WordPress internal settings.php false positive,
			// or a config that does not declare a database) yields DBName == ""
			// and is skipped.
			creds, kind := parseCMSConfig(p, content)
			if creds.DBName == "" {
				logx.Debug("DiscoverSiteCreds: %s has no DB name, skipping", p)
				continue
			}
			out = append(out, SiteCreds{
				Docroot:    dr,
				ConfigPath: p,
				Kind:       kind,
				Creds:      creds,
			})
			logx.Debug("DiscoverSiteCreds: %s -> %s", p, creds.String())
		}
	}
	return out, nil
}

// DiscoverAllCreds runs the layered, read-only credential discovery and returns
// the UNION of all discovered SiteCreds (NOT collapsed per database), so the
// planner can both (a) collect every real on-disk config a database needs
// rewritten and (b) choose the best password. Sources, in order:
//
//  1. Softaculous registry (CMS-agnostic, complete, even for removed installs);
//     these are tagged FromRegistry (credentials only, not a rewrite target).
//  2. per-CMS config files under each docroot (WordPress/Joomla/Drupal/…);
//     these are real, rewritable site configs.
//  3. for any database in wantDBs that NEITHER step above gave a password, a
//     name-search grep across the docroot files.
//
// wantDBs is the authoritative list of source database names (from
// list_databases) used to drive step 3 — so a database that no config mentions
// still gets one last recovery attempt. BuildPlan does the per-database merge.
func DiscoverAllCreds(ctx context.Context, src Runner, docroots, wantDBs []string) ([]SiteCreds, error) {
	var all []SiteCreds
	havePass := map[string]bool{} // db -> some source already yielded a password

	note := func(list []SiteCreds) {
		for _, sc := range list {
			if sc.DBName != "" && sc.DBPassword != "" {
				havePass[sc.DBName] = true
			}
		}
	}

	// 1) Softaculous (best, CMS-agnostic source).
	logx.Debug("DiscoverAllCreds: phase 1 — scanning softaculous registry")
	soft, err := DiscoverSoftaculous(ctx, src)
	if err != nil {
		return nil, err
	}
	all = append(all, soft...)
	note(soft)

	// 2) Per-CMS config files under the docroots (the rewrite targets).
	cfg, err := DiscoverSiteCreds(ctx, src, docroots)
	if err != nil {
		return nil, err
	}
	all = append(all, cfg...)
	note(cfg)
	logx.Debug("DiscoverAllCreds: site configs phase complete — %d rewrite target(s) found", len(cfg))

	// 3) Name-search for databases still missing a password after 1+2.
	var missing []string
	for _, db := range wantDBs {
		if !havePass[db] {
			missing = append(missing, db)
		}
	}
	if len(missing) > 0 {
		logx.Debug("DiscoverAllCreds: phase 3 — %d database(s) still missing passwords, attempting name-search grep", len(missing))
		found, err := SearchCredsByDBName(ctx, src, docroots, missing)
		if err != nil {
			return nil, err
		}
		all = append(all, found...)
		logx.Debug("DiscoverAllCreds: name-search recovered %d of %d missing database(s)", len(found), len(missing))
	}

	return all, nil
}

// listConfigs runs the read-only find on one docroot.
func listConfigs(ctx context.Context, src Runner, docroot string) ([]string, error) {
	out, err := src.RunScript(ctx, findConfigsScript(), map[string]string{"DOCROOT": docroot})
	if err != nil {
		return nil, fmt.Errorf("find wp-config in %s: %w", docroot, err)
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if p := strings.TrimSpace(line); p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// readFile reads one file read-only from the source.
func readFile(ctx context.Context, src Runner, path string) (string, error) {
	out, err := src.RunScript(ctx, catScript, map[string]string{"FILE": path})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// SearchCredsByDBName is the last-resort discovery: for a database whose
// credentials were not found via Softaculous or a recognized CMS config, it
// greps the source docroots (read-only) for files that mention the database
// NAME, then parses each such file with the CMS dispatcher to try to recover the
// user/password. Returns the first parse that yields a matching DBName with a
// non-empty user (or password). dbNames are the still-unresolved SOURCE database
// names.
//
// This catches sites with non-standard or renamed config files: as long as some
// file in the docroot references the database name AND holds the credentials in
// a recognizable shape, we find them.
func SearchCredsByDBName(ctx context.Context, src Runner, docroots []string, dbNames []string) ([]SiteCreds, error) {
	if len(dbNames) == 0 {
		return nil, nil
	}
	logx.Debug("SearchCredsByDBName: scanning %d database(s) via grep across %d docroot(s)", len(dbNames), len(docroots))
	var out []SiteCreds
	for _, db := range dbNames {
		paths, err := grepDBNameFiles(ctx, src, docroots, db)
		if err != nil {
			return nil, err
		}
		// Parse the candidates in deterministic order and stop at the FIRST one that
		// carries this database's credentials. A single file that merely mentions
		// the name (a renamed SQL dump, a backup, an operator note, a stale config)
		// must not hide a real config listed after it.
		for _, path := range paths {
			content, err := readFile(ctx, src, path)
			if err != nil {
				continue
			}
			creds, kind := parseCMSConfig(path, content)
			// Accept only if the file truly carries this database's credentials.
			if creds.DBName == db && (creds.DBUser != "" || creds.DBPassword != "") {
				out = append(out, SiteCreds{Docroot: docrootOf(docroots, path), ConfigPath: path, Kind: kind, Creds: creds})
				logx.Debug("SearchCredsByDBName: %s -> %s (%s)", db, path, creds.String())
				break
			}
			logx.Debug("SearchCredsByDBName: %s mentioned in %s but the parse did not yield matching creds (got db=%q user-present=%v) — trying next candidate", db, path, creds.DBName, creds.DBUser != "")
		}
	}
	return out, nil
}

// maxCredCandidates bounds how many db-name mentions the last-resort search reads
// and parses per database, so a docroot full of backups/notes/dumps that all cite
// the name cannot trigger an unbounded number of remote reads. The grep output is
// LC_ALL=C-sorted for a deterministic order, and the caller stops at the first
// candidate that yields credentials, so the common case still reads one file.
const maxCredCandidates = 64

// grepDBNameScript greps, read-only, for a database name within the docroot's
// PHP/env/ini files, printing a bounded, deterministically-ordered list of matching
// file paths (LC_ALL=C sort, capped at maxCredCandidates). The name is passed via
// $DBNAME and the root via $ROOT (never interpolated); -F (fixed string) and -l
// (names only, stop at first match per file) keep it cheap.
var grepDBNameScript = fmt.Sprintf(`set -u
r="$ROOT"
[ -d "$r" ] || exit 0
grep -rlF --include='*.php' --include='*.env' --include='*.ini' --include='*.inc' -- "$DBNAME" "$r" 2>/dev/null | LC_ALL=C sort | head -n %d
`, maxCredCandidates)

// grepDBNameFiles returns a bounded, deterministic list of source files that
// mention dbName across the docroots. Each docroot is grepped read-only; the
// per-docroot lists are concatenated, de-duplicated, and capped at
// maxCredCandidates so the caller can parse them in order until one yields creds.
func grepDBNameFiles(ctx context.Context, src Runner, docroots []string, dbName string) ([]string, error) {
	var paths []string
	seen := make(map[string]bool)
	for _, dr := range docroots {
		if dr == "" {
			continue
		}
		out, err := src.RunScript(ctx, grepDBNameScript, map[string]string{"ROOT": dr, "DBNAME": dbName})
		if err != nil {
			return nil, fmt.Errorf("grep %q in %s: %w", dbName, dr, err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			p := strings.TrimSpace(line)
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			paths = append(paths, p)
			if len(paths) >= maxCredCandidates {
				return paths, nil
			}
		}
	}
	return paths, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// docrootOf returns the docroot (from the list) that contains path, or "".
func docrootOf(docroots []string, path string) string {
	best := ""
	for _, dr := range docroots {
		if dr != "" && strings.HasPrefix(path, dr) && len(dr) > len(best) {
			best = dr
		}
	}
	return best
}

// MapConfigPath rewrites a SOURCE wp-config path to the DESTINATION path by
// swapping the source docroot prefix for the destination docroot. It is used to
// locate, on the destination, the config that must be rewritten. srcDocroot and
// destDocroot are the matched docroots of the same domain (from the web-file
// plan). A path not under srcDocroot is returned unchanged (defensive).
//
// The prefix check is BOUNDARY-AWARE: a path is "under" srcDocroot only if it is
// srcDocroot itself or continues with a '/'. A plain strings.HasPrefix would treat
// "/home/u/site2/wp-config.php" as under "/home/u/site" and map it to a bogus
// destination ("/dest/site2/wp-config.php"); requiring the '/' boundary prevents a
// sibling docroot that merely shares a name prefix from being mis-mapped.
func MapConfigPath(srcConfigPath, srcDocroot, destDocroot string) string {
	// Normalize trailing slashes first: an inventory docroot can arrive as
	// "/home/u/site/". Without this, a real config "/home/u/site/wp-config.php"
	// matches neither `== srcDocroot` nor the `srcDocroot+"/"` boundary check (which
	// would become "/home/u/site//"), so it is returned unchanged and the destination
	// config is silently never rewritten — an incomplete cutover (site keeps the OLD DB).
	srcDocroot = strings.TrimRight(srcDocroot, "/")
	destDocroot = strings.TrimRight(destDocroot, "/")
	if srcDocroot == "" || destDocroot == "" {
		return srcConfigPath
	}
	if srcConfigPath == srcDocroot {
		return destDocroot
	}
	if !strings.HasPrefix(srcConfigPath, srcDocroot+"/") {
		return srcConfigPath
	}
	return destDocroot + strings.TrimPrefix(srcConfigPath, srcDocroot)
}
