package dbmig

import (
	"context"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/phpserialize"
	"github.com/tis24dev/cPanel_self-migration/internal/wpconfig"
)

// softaculousPath is the per-account Softaculous installation registry. It is a
// PHP-serialized map of installation-id -> details, where each entry carries the
// database credentials (softdb/softdbuser/softdbpass/softdbhost), the install
// path (softpath), the table prefix (dbprefix), and the CMS (script_name) — for
// EVERY app installed via Softaculous, not just WordPress. It is therefore the
// best CMS-agnostic source of database credentials, and it even retains entries
// for databases whose files were later removed (recovering otherwise-orphan
// credentials).
const softaculousPath = "$HOME/.softaculous/installations.php"

// readSoftaculousScript reads the registry read-only. The path uses $HOME so it
// resolves on the remote account; prints nothing if absent.
const readSoftaculousScript = `set -u
f="$HOME/.softaculous/installations.php"
[ -f "$f" ] && cat "$f" || true
`

// DiscoverSoftaculous reads the Softaculous registry on the SOURCE (read-only)
// and returns one SiteCreds per installation that declares a database. It is the
// PRIMARY credential source: CMS-agnostic and complete (covers WordPress,
// Joomla, etc., and entries whose docroot files are gone). Returns nil (no
// error) when the registry is absent — callers then fall back to per-CMS config
// parsing.
//
// The leading "<?php" / "die" guard line that Softaculous sometimes prepends is
// tolerated by scanning for the first serialized array marker.
func DiscoverSoftaculous(ctx context.Context, src Runner) ([]SiteCreds, error) {
	out, err := src.RunScript(ctx, readSoftaculousScript, nil)
	if err != nil {
		return nil, err
	}
	raw := string(out)
	if raw == "" {
		logx.Debug("DiscoverSoftaculous: no registry on source")
		return nil, nil
	}
	return parseSoftaculous(raw), nil
}

// parseSoftaculous is the pure parsing half (unit-tested). It locates the
// serialized payload, decodes it, and extracts the DB credentials from each
// installation entry. Unparseable input yields nil rather than an error — the
// registry is a best-effort source, and a parse failure must not abort a run.
func parseSoftaculous(raw string) []SiteCreds {
	payload := serializedPayload(raw)
	if payload == "" {
		return nil
	}
	v, err := phpserialize.Unserialize(payload)
	if err != nil {
		logx.Debug("parseSoftaculous: unserialize failed: %v", err)
		return nil
	}
	top, ok := v.(map[string]phpserialize.Value)
	if !ok {
		return nil
	}
	logx.Debug("parseSoftaculous: unpacking %d registry entry(ies)", len(top))
	var creds []SiteCreds
	for _, entry := range top {
		m, ok := entry.(map[string]phpserialize.Value)
		if !ok {
			continue
		}
		db := phpserialize.AsString(m, "softdb")
		if db == "" {
			continue // not a database-backed install
		}
		creds = append(creds, SiteCreds{
			Docroot:      phpserialize.AsString(m, "softpath"),
			ConfigPath:   softaculousPath, // provenance, not a real wp-config path
			FromRegistry: true,            // credentials only; do NOT rewrite this file
			Creds: wpconfig.Creds{
				DBName:      db,
				DBUser:      phpserialize.AsString(m, "softdbuser"),
				DBPassword:  phpserialize.AsString(m, "softdbpass"),
				DBHost:      phpserialize.AsString(m, "softdbhost"),
				TablePrefix: phpserialize.AsString(m, "dbprefix"),
			},
		})
		logx.Debug("parseSoftaculous: %s (%s) db=%s user=%s",
			phpserialize.AsString(m, "softdomain"), phpserialize.AsString(m, "script_name"), db,
			phpserialize.AsString(m, "softdbuser"))
	}
	logx.Debug("parseSoftaculous: extracted %d database entry(ies)", len(creds))
	return creds
}

// serializedPayload returns the PHP-serialized portion of a Softaculous file,
// skipping any leading PHP guard (e.g. `<?php exit; ?>` or `die();`). The payload
// starts at the first top-level array marker `a:<digits>:{`.
func serializedPayload(raw string) string {
	for i := 0; i < len(raw); i++ {
		if raw[i] == 'a' && i+2 < len(raw) && raw[i+1] == ':' && raw[i+2] >= '0' && raw[i+2] <= '9' {
			return raw[i:]
		}
	}
	return ""
}
