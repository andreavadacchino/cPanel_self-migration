package dbmig

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
	"github.com/tis24dev/cPanel_self-migration/internal/wpconfig"
)

// Transfer copies one database from the read-only source to the destination by
// streaming a mysqldump through this process (SRC `mysqldump` | Go pipe | DEST
// `mysql`), mirroring the webfiles tar bridge.
//
// SrcUser/SrcPass are the SOURCE cPanel ACCOUNT credentials (the account user is
// also a MySQL user that can dump every database — this is what works on the
// source without per-database passwords). The DESTINATION credentials are NOT
// fixed here: on the destination the cPanel account user typically has NO MySQL
// login, so the import authenticates as the per-database user just created
// (passed to CopyDatabase). The destination database and user are created by the
// caller (apply step) BEFORE CopyDatabase runs.
type Transfer struct {
	Src, Dest        *sshx.Client
	SrcUser, SrcPass string
	Timeout          time.Duration // per-attempt STALL timeout (no progress for this long aborts and retries the dump); 0 = no stall bound

	// DumpCmd is the source mysqldump command this Transfer streams. The caller
	// resolves it ONCE per run via BuildDumpCmd(SrcSupportsGtidPurged(...)), so the
	// MySQL-only --set-gtid-purged=OFF flag is present for a GTID-enabled MySQL source
	// but omitted on MariaDB. Empty falls back to baseDumpCmd (no GTID flag), keeping
	// the zero-value Transfer{} valid for the name-guard and bridge tests.
	DumpCmd string
}

// CopyResult summarizes a database copy.
type CopyResult struct {
	BytesSent int64
}

// CopyDatabase streams item.SrcDB from the source into item.DestDB on the
// destination, authenticating the import as destUser/destPass (the per-database
// user created for this item). The destination database/user must already exist.
// prog (an onBytes callback) may be nil. The source dump is read-only
// (--single-transaction); the destination import overwrites whatever is there
// (migration semantics — the apply step creates a fresh empty DB first).
func (t Transfer) CopyDatabase(ctx context.Context, item DBPlanItem, destUser, destPass string, onBytes func(int64)) (CopyResult, error) {
	if item.SrcDB == "" || item.DestDB == "" {
		return CopyResult{}, fmt.Errorf("CopyDatabase: missing database name (src=%q dest=%q)", item.SrcDB, item.DestDB)
	}
	logx.Debug("CopyDatabase %s -> %s (import user %s)", item.SrcDB, item.DestDB, destUser)

	// Pass the credentials as an env MAP (not inlined via WithEnv): the bridge delivers
	// them with SSH Setenv so MYSQL_PWD never enters the command STRING — and thus never
	// the shell's argv (visible to co-tenants via `ps`) nor any log line. On an
	// AcceptEnv-rejecting server the bridge still keeps MYSQL_PWD off argv: it routes the
	// password through the command's stdin (a `read`+`export` prologue), only the
	// non-secret DB_NAME/DB_USER are inlined, and only the bare command is ever logged.
	srcEnv := dumpEnv(item.SrcDB, t.SrcUser, t.SrcPass)
	destEnv := dumpEnv(item.DestDB, destUser, destPass)

	// Retry the dump bridge with a per-attempt stall timeout + backoff, mirroring the
	// maildir/webfiles tar bridges. A retry re-runs the FULL mysqldump, which is
	// idempotent on the destination: the dump carries DROP TABLE/ROUTINE/EVENT IF
	// EXISTS and SET FOREIGN_KEY_CHECKS=0, so re-importing cleanly overwrites a
	// partially-imported database. Combined with the transport self-heal in
	// Client.newSession, a transient connection drop mid-copy now recovers instead of
	// aborting the whole run.
	var sent int64
	err := sshx.RetryBatch(ctx, "db "+item.SrcDB, t.Timeout, onBytes, func(bctx context.Context, onB func(int64)) error {
		sent = 0 // a retry re-dumps from scratch; count only the (final, successful) attempt
		wrapped := func(n int64) {
			sent += n
			onB(n) // resets the stall watchdog and feeds the progress bar
		}
		dump := t.DumpCmd
		if dump == "" {
			dump = baseDumpCmd // zero-value Transfer{} (tests): no GTID flag
		}
		return sshx.BridgeProgress(bctx, t.Src, dump, srcEnv, nil, t.Dest, importCmd, destEnv, wrapped)
	})
	if err != nil {
		logx.Debug("CopyDatabase %s: bridge failed after %d bytes — likely schema/permission issue", item.SrcDB, sent)
		return CopyResult{BytesSent: sent}, fmt.Errorf("%s: dump bridge: %w", item.SrcDB, err)
	}
	logx.Debug("CopyDatabase %s: complete — %d bytes transferred", item.SrcDB, sent)
	return CopyResult{BytesSent: sent}, nil
}

// CountTables returns the base-table count of a database on the given side,
// read-only. user/pass are that side's cPanel account credentials.
func CountTables(ctx context.Context, c Runner, dbName, user, pass string) (int, error) {
	out, err := c.RunScript(ctx, countTablesCmd, dumpEnv(dbName, user, pass))
	if err != nil {
		return 0, fmt.Errorf("count tables in %s: %w", dbName, err)
	}
	n, ok := parseCount(string(out))
	if !ok {
		return 0, fmt.Errorf("count tables in %s: unparseable output %q", dbName, logx.Snippet(out, 160))
	}
	logx.Debug("CountTables %s: %d base table(s)", dbName, n)
	return n, nil
}

// CountObjects returns the per-database counts of routines/events/triggers/views
// on the given side, read-only. user/pass are that side's MySQL credentials. Used
// by verifyDBs to confirm the non-table objects (migrated via mysqldump
// --routines/--events and the default trigger dump) actually landed.
func CountObjects(ctx context.Context, c Runner, dbName, user, pass string) (ObjectCounts, error) {
	out, err := c.RunScript(ctx, countObjectsCmd, dumpEnv(dbName, user, pass))
	if err != nil {
		return ObjectCounts{}, fmt.Errorf("count objects in %s: %w", dbName, err)
	}
	oc, ok := parseObjectCounts(string(out))
	if !ok {
		return ObjectCounts{}, fmt.Errorf("count objects in %s: unparseable output %q", dbName, logx.Snippet(out, 160))
	}
	logx.Debug("CountObjects %s: routines=%d events=%d triggers=%d views=%d",
		dbName, oc.Routines, oc.Events, oc.Triggers, oc.Views)
	return oc, nil
}

// GetCharsets returns the database charset fingerprint (schema default + per-table
// collation) on the given side, read-only. user/pass are that side's MySQL
// credentials. Used by verifyDBs to catch a wrong-encoding import (mojibake) that
// the table/row counts cannot see.
func GetCharsets(ctx context.Context, c Runner, dbName, user, pass string) (CharsetInfo, error) {
	out, err := c.RunScript(ctx, charsetsCmd, dumpEnv(dbName, user, pass))
	if err != nil {
		return CharsetInfo{}, fmt.Errorf("read charsets in %s: %w", dbName, err)
	}
	ci, ok := parseCharsets(string(out))
	if !ok {
		return CharsetInfo{}, fmt.Errorf("read charsets in %s: unparseable output %q", dbName, logx.Snippet(out, 160))
	}
	logx.Debug("GetCharsets %s: db=%s/%s, %d table(s)", dbName, ci.DBCharset, ci.DBCollation, len(ci.Tables))
	return ci, nil
}

// RewriteWPConfig rewrites DB_NAME/DB_USER/DB_PASSWORD in one wp-config.php on
// the DESTINATION. It first reads the existing destination file (so it preserves
// salts, table prefix, and every other constant), applies wpconfig.Rewrite in
// Go, then streams the new content back over the guarded writer script. Writes
// ONLY on the destination. destPath is the wp-config path on the destination.
func RewriteWPConfig(ctx context.Context, dest *sshx.Client, destPath, dbName, dbUser, dbPassword string) error {
	// Read the current destination wp-config (it was just copied by the web-file
	// step). Read-only on the destination.
	cur, err := dest.RunScript(ctx, `cat "$WPCONFIG"`, map[string]string{"WPCONFIG": destPath})
	if err != nil {
		return fmt.Errorf("read dest wp-config %s: %w", destPath, err)
	}
	newContent := wpconfig.Rewrite(string(cur), dbName, dbUser, dbPassword)
	// Confirm the rewrite actually set the intended credentials. wpconfig.Rewrite
	// leaves an ABSENT or unrecognized define() unchanged, so without this a
	// wp-config that lacks (say) a DB_PASSWORD define — or uses a define syntax the
	// regex does not match — would be reported as rewritten while still carrying the
	// wrong credentials (the migrated site then cannot connect). Surface it instead.
	if !wpCredsMatch(newContent, dbName, dbUser, dbPassword) {
		return fmt.Errorf("rewrite dest wp-config %s: could not set the DB credentials (%s)", destPath, credsMismatch(wpconfig.Parse(newContent), dbName, dbUser, dbPassword))
	}
	if newContent == string(cur) {
		logx.Debug("RewriteWPConfig %s: already points at the destination DB, no change needed", destPath)
		return nil
	}
	// Write the new content via the guarded script (atomic temp + mv). Both the
	// path and the full new content travel as environment variables, never
	// interpolated into the command body.
	_, err = dest.RunScript(ctx, writeConfigScript(), map[string]string{
		"WPCONFIG":   destPath,
		"NEWCONTENT": newContent,
	})
	if err != nil {
		return fmt.Errorf("rewrite dest wp-config %s: %w", destPath, err)
	}
	logx.Debug("RewriteWPConfig %s: rewritten (db=%s user=%s)", destPath, dbName, dbUser)
	return nil
}

// UnsupportedRewriteError is returned by RewriteSiteConfig when the destination
// config belongs to a recognized CMS whose rewriter is not yet implemented. The
// database has already migrated, but the site config still points at the SOURCE
// database name/user, so the caller MUST surface this as a manual action rather
// than swallow it as a generic warning.
type UnsupportedRewriteError struct {
	Kind Kind
}

func (e *UnsupportedRewriteError) Error() string {
	return fmt.Sprintf("automatic config rewrite for %s is not implemented yet", e.Kind)
}

// RewriteSiteConfig rewrites one site config on the DESTINATION so the migrated
// site points at the destination database, dispatching on the CMS kind detected
// on the source. WordPress uses the dedicated wp-config path; other implemented
// kinds go through the generic rewriteVia. A recognized but not-yet-implemented
// kind returns an *UnsupportedRewriteError so the caller can surface a
// manual-action notice instead of silently leaving the site pointed at the old
// database. Writes ONLY on the destination.
func RewriteSiteConfig(ctx context.Context, dest *sshx.Client, destPath string, kind Kind, dbName, dbUser, dbPassword string) error {
	if kind == KindWordPress {
		return RewriteWPConfig(ctx, dest, destPath, dbName, dbUser, dbPassword)
	}
	rw, ok := siteRewriters[kind]
	if !ok {
		return &UnsupportedRewriteError{Kind: kind}
	}
	return rewriteVia(ctx, dest, destPath, kind, rw, dbName, dbUser, dbPassword)
}

// IsLocalDBHost reports whether a config's DB host points at the LOCAL MySQL — the
// only database server a cPanel destination account can reach. An empty host (PHP
// defaults to local), "localhost", "127.0.0.1", "::1", and a "host:port"/":socket"
// form of those are local; anything else (a dedicated or remote DB server the source
// used) means the migrated site cannot reach the destination database even after its
// name/user/password were rewritten — DB_HOST is deliberately never rewritten. Pure.
func IsLocalDBHost(host string) bool {
	h := strings.TrimSpace(host)
	// WordPress/mysqli persistent-connection prefix: DB_HOST='p:localhost' is local.
	h = strings.TrimSpace(strings.TrimPrefix(h, "p:"))
	switch {
	case strings.HasPrefix(h, "["):
		// Bracketed IPv6 literal, optionally with a :port — "[::1]" / "[::1]:3306".
		if i := strings.IndexByte(h, ']'); i >= 0 {
			h = h[1:i]
		}
	case strings.Contains(h, "::"):
		// A bare IPv6 literal (e.g. "::1"): leave it whole; a trailing :port would be
		// indistinguishable, but a bare loopback literal is "::1" with no port.
	default:
		// Plain host[:port] or host:/socket — strip the suffix from the first colon.
		if i := strings.IndexByte(h, ':'); i >= 0 {
			h = h[:i]
		}
	}
	switch strings.ToLower(h) {
	case "", "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// parseConfigByKind parses a config's content with the parser for its CMS kind,
// returning the DB credentials it declares (including DBHost). supported is false for
// a kind with no parser (its config cannot be read back). Pure.
func parseConfigByKind(kind Kind, content string) (creds wpconfig.Creds, supported bool) {
	if kind == KindWordPress {
		return wpconfig.Parse(content), true
	}
	rw, ok := siteRewriters[kind]
	if !ok {
		return wpconfig.Creds{}, false
	}
	return rw.parse(content), true
}

// checkDestCreds reports whether a parsed config correctly points at the destination
// database: the intended name/user/password landed AND the DB host is local. reason
// explains the first failure. Pure; unit-tested.
func checkDestCreds(got wpconfig.Creds, wantDB, wantUser, wantPass string) (ok bool, reason string) {
	switch {
	case got.DBName != wantDB:
		return false, fmt.Sprintf("DB name is %q, expected %q", got.DBName, wantDB)
	case got.DBUser != wantUser:
		return false, fmt.Sprintf("DB user is %q, expected %q", got.DBUser, wantUser)
	case wantPass != "" && got.DBPassword != wantPass:
		return false, "DB password does not match the destination user's"
	case !IsLocalDBHost(got.DBHost):
		return false, fmt.Sprintf("DB host %q is not the local destination MySQL", got.DBHost)
	}
	return true, ""
}

// VerifyDestConfig RE-READS a destination config after rewrite and confirms it points
// at the destination database: the planned name/user/password are present AND the DB
// host is local. It catches the cutover that "succeeded" on name/user/password but
// still points the site at a remote/old DB host (never rewritten), or a value that did
// not truly land — neither of which the rewrite step's own pre-write check surfaces.
//
// It applies TWO dimensions. Dimension 1 (checkDestCreds) is the value/host check: a
// failure is a PROVABLE wrong cutover (ok=false, unverified=false) and a hard error in
// both tiers. Dimension 2 (ConfigAmbiguity) is a structurally-different, PHP-free second
// opinion that catches the V35 false-OKs the shared parser is blind to (a constant
// defined more than once, a non-literal first definition, or a heredoc/string decoy
// define edited instead of the live one): when dimension 1 passes but dimension 2 cannot
// PROVE the rewrite acted on the value PHP would use, it returns ok=false with
// unverified=true — a SOFT "not independently verified" signal the caller treats as a
// note at the default tier and a hard failure under --deep-verify. supported=false for a
// kind with no parser (already flagged MANUAL by the rewrite step).
func VerifyDestConfig(ctx context.Context, dest *sshx.Client, destPath string, kind Kind, wantDB, wantUser, wantPass string) (ok bool, reason string, unverified bool, err error) {
	cur, err := dest.RunScript(ctx, `cat "$WPCONFIG"`, map[string]string{"WPCONFIG": destPath})
	if err != nil {
		return false, "", false, fmt.Errorf("re-read dest config %s: %w", destPath, err)
	}
	got, supported := parseConfigByKind(kind, string(cur))
	if !supported {
		return false, "unsupported config kind cannot be re-read to verify the cutover", false, nil
	}
	if ok, reason = checkDestCreds(got, wantDB, wantUser, wantPass); !ok {
		return false, reason, false, nil // provable wrong cutover -> hard in both tiers
	}
	if r, ambiguous, covered := ConfigAmbiguity(kind, string(cur)); covered && ambiguous {
		return false, r, true, nil // cannot PROVE the cutover -> soft note (default) / hard (--deep)
	}
	return true, "", false, nil
}

// rewriteVia reads the destination config, applies the kind's rewriter, verifies
// the intended credentials actually landed by RE-PARSING with the same read parser
// (read-after-write consistency, so a value the rewriter could not place is
// surfaced rather than silently shipped wrong), and writes it back via the guarded
// atomic writer. The non-WordPress counterpart of RewriteWPConfig.
func rewriteVia(ctx context.Context, dest *sshx.Client, destPath string, kind Kind, rw siteRewriter, dbName, dbUser, dbPassword string) error {
	cur, err := dest.RunScript(ctx, `cat "$WPCONFIG"`, map[string]string{"WPCONFIG": destPath})
	if err != nil {
		return fmt.Errorf("read dest %s config %s: %w", kind, destPath, err)
	}
	newContent := rw.rewrite(string(cur), dbName, dbUser, dbPassword)
	if got := rw.parse(newContent); !credsSet(got, dbName, dbUser, dbPassword) {
		return fmt.Errorf("rewrite dest %s config %s: could not set the DB credentials (%s)", kind, destPath, credsMismatch(got, dbName, dbUser, dbPassword))
	}
	if newContent == string(cur) {
		logx.Debug("rewriteVia %s: %s already points at the destination DB, no change needed", kind, destPath)
		return nil
	}
	if _, err := dest.RunScript(ctx, writeConfigScript(), map[string]string{"WPCONFIG": destPath, "NEWCONTENT": newContent}); err != nil {
		return fmt.Errorf("rewrite dest %s config %s: %w", kind, destPath, err)
	}
	logx.Debug("rewriteVia %s: %s rewritten (db=%s user=%s)", kind, destPath, dbName, dbUser)
	return nil
}

// credsSet reports whether got already carries each NON-empty intended value (an
// empty argument means "leave unchanged", matching the rewriters). Pure.
func credsSet(got wpconfig.Creds, dbName, dbUser, dbPassword string) bool {
	return (dbName == "" || got.DBName == dbName) &&
		(dbUser == "" || got.DBUser == dbUser) &&
		(dbPassword == "" || got.DBPassword == dbPassword)
}

// credsMismatch names which intended (non-empty) fields did NOT land in got, for a
// read-after-write failure message — so the operator sees WHICH credential the
// rewriter could not place instead of an opaque "a field is missing". The password
// is never echoed (only whether it landed), so it cannot leak into logs/screen. Pure.
func credsMismatch(got wpconfig.Creds, dbName, dbUser, dbPassword string) string {
	var miss []string
	if dbName != "" && got.DBName != dbName {
		miss = append(miss, fmt.Sprintf("DB name is %q, wanted %q", got.DBName, dbName))
	}
	if dbUser != "" && got.DBUser != dbUser {
		miss = append(miss, fmt.Sprintf("DB user is %q, wanted %q", got.DBUser, dbUser))
	}
	if dbPassword != "" && got.DBPassword != dbPassword {
		miss = append(miss, "DB password did not land")
	}
	if len(miss) == 0 {
		return "a field is missing or unrecognized"
	}
	return strings.Join(miss, "; ")
}

// wpCredsMatch reports whether wp-config content already declares the intended DB
// credentials. Only the non-empty fields are checked — an empty argument means
// "leave unchanged" (matching wpconfig.Rewrite), so a caller that rewrites only
// some fields is not forced to match the others. Pure; unit-tested.
func wpCredsMatch(content, dbName, dbUser, dbPassword string) bool {
	got := wpconfig.Parse(content)
	if dbName != "" && got.DBName != dbName {
		return false
	}
	if dbUser != "" && got.DBUser != dbUser {
		return false
	}
	if dbPassword != "" && got.DBPassword != dbPassword {
		return false
	}
	return true
}
