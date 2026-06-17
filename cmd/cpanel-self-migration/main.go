// Command cpanel-self-migration migrates email mailboxes, website files, and
// MySQL databases (plus the domains they need) between two cPanel accounts using
// only user-level SSH. The SOURCE host is always read-only; all writes target
// the DESTINATION. Default mode is dry-run.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/migrate"
	"github.com/tis24dev/cPanel_self-migration/internal/version"
)

func main() {
	var (
		apply       = flag.Bool("apply", false, "create missing domains + migrate the selected data (default: dry-run)")
		dryRun      = flag.Bool("dry-run", false, "explicit dry-run: analyze + compare SRC/DEST, no changes")
		mailFlag    = flag.Bool("mail", false, "migrate mail (mailboxes) only; default (no --mail/--file/--db) does ALL")
		fileFlag    = flag.Bool("file", false, "migrate website files (docroots) only; default does ALL")
		dbFlag      = flag.Bool("db", false, "migrate databases (MySQL) only; default does ALL")
		full        = flag.Bool("full", false, "with --apply, force re-sync of every mailbox even if consistent")
		forceSync   = flag.Bool("force-sync", false, "alias of --full")
		applyMirror = flag.Bool("apply-mirror", false, "like --apply, but MIRROR each mailbox: rename the destination mailbox aside (<user>-bak) and re-copy ALL messages from the source, so mail that exists only on the dest is removed from the live mailbox. Files/databases behave as under --apply.")
		verifyCsum  = flag.Bool("verify-checksums", false, "stricter fast-skip: when count+UIDVALIDITY match, also compare the exact message-ID set before skipping a mailbox")
		deepVerify  = flag.Bool("deep-verify", false, "with --apply, verify by CONTENT hash (sha256 per web file; slower, reads every byte on both sides) instead of metadata only — catches same-size corruption")
		cfgPath     = flag.String("config", "", "path to host.yaml (default: configs/host.yaml next to the binary or CWD)")
		logLevel    = flag.String("log-level", "info", "log verbosity: info | debug (debug traces SSH sessions, transfers, and network errors to stderr)")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println(version.String())
		return
	}

	switch *logLevel {
	case "debug":
		logx.SetDebug(true)
	case "info", "":
		// default
	default:
		fmt.Fprintf(os.Stderr, "error: unknown --log-level %q (use info or debug)\n", *logLevel)
		os.Exit(2)
	}

	if *apply && *dryRun {
		fmt.Fprintln(os.Stderr, "error: --apply and --dry-run are mutually exclusive")
		os.Exit(2)
	}
	if *applyMirror && *dryRun {
		fmt.Fprintln(os.Stderr, "error: --apply-mirror and --dry-run are mutually exclusive")
		os.Exit(2)
	}
	if *apply && *applyMirror {
		fmt.Fprintln(os.Stderr, "error: --apply and --apply-mirror are mutually exclusive")
		os.Exit(2)
	}

	path, altConfigs, err := resolveConfigPath(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	// Always announce WHICH config is in effect — a stale installed host.yaml silently
	// shadowing the intended one would otherwise migrate the wrong accounts unnoticed.
	fmt.Fprintf(os.Stderr, "Config: %s\n", path)
	if len(altConfigs) > 0 {
		fmt.Fprintf(os.Stderr, "warning: multiple host.yaml found; using %s (ignoring %s). Pass --config to choose explicitly.\n",
			path, strings.Join(altConfigs, ", "))
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	// Signal handling — a SINGLE handler so the two interrupts behave
	// deterministically:
	//   1st Ctrl-C  -> cancel ctx (in-flight work stops; deferred cleanups like
	//                  token revoke and connection close run to completion);
	//   2nd Ctrl-C  -> force-exit, for the rare case the cleanup itself wedges.
	// Using ONE handler (not NotifyContext + a second signal.Notify on the same
	// signals) avoids a race where the second Ctrl-C could os.Exit before the
	// first interrupt's cleanup — e.g. token revocation — has finished.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go handleSignals(cancel)

	// Selection: --mail / --file / --db choose what to migrate. With NONE set, do
	// all (the default). Setting several explicitly is valid; setting all three is
	// equivalent to none.
	doMail, doFile, doDB := *mailFlag, *fileFlag, *dbFlag
	if !doMail && !doFile && !doDB {
		doMail, doFile, doDB = true, true, true
	}

	// --apply-mirror only changes the mail flow; warn (but proceed) if mail is not
	// selected so it does not silently behave like a plain --apply.
	if *applyMirror && !doMail {
		fmt.Fprintln(os.Stderr, "warning: --apply-mirror changes only the mail flow; with --file/--db only it behaves like --apply")
	}

	outDir, err := os.Getwd()
	if err != nil {
		// OutputDir anchors all audit artifacts (the report + analysis logs). If the
		// working directory cannot be determined (e.g. it was removed), fail fast with
		// a clear message BEFORE opening any SSH connection, rather than write the
		// artifacts to an empty/relative path or surface a confusing later error.
		fmt.Fprintln(os.Stderr, "error: cannot determine the current working directory (needed for the logs/ artifacts):", err)
		os.Exit(1)
	}
	opts := migrate.Options{
		Apply:           *apply || *applyMirror,
		ForceSync:       *full || *forceSync,
		VerifyChecksums: *verifyCsum,
		DeepVerify:      *deepVerify,
		MirrorMail:      *applyMirror,
		DoMail:          doMail,
		DoFile:          doFile,
		DoDB:            doDB,
		OutputDir:       outDir,
	}

	if err := migrate.Run(ctx, cfg, opts); err != nil {
		if ctx.Err() != nil {
			// Interrupted: report cleanly with a distinct exit code (130 =
			// terminated by Ctrl-C, the shell convention).
			fmt.Fprintln(os.Stderr, "\ninterrupted — stopped; no further changes will be made.")
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// handleSignals is the SINGLE interrupt handler. The first SIGINT/SIGTERM cancels
// the run (via cancel), letting in-flight work stop and deferred cleanups — token
// revoke, connection close — run to completion; it prints a notice telling the
// user a second Ctrl-C will force-quit. The second signal force-exits, for the
// rare case the cleanup itself wedges.
//
// One handler on one channel means the two presses are ordered deterministically:
// there is no separate consumer that could os.Exit before the first interrupt's
// cleanup has finished. (A buffer of 2 ensures a quick double Ctrl-C is not lost.)
func handleSignals(cancel context.CancelFunc) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)

	<-ch // first interrupt: ask for a clean shutdown
	fmt.Fprintln(os.Stderr, "\ninterrupting — finishing the current step and shutting down cleanly (press Ctrl-C again to force quit) ...")
	cancel()

	<-ch // second interrupt: give up and exit now
	fmt.Fprintln(os.Stderr, "\nforced quit.")
	os.Exit(130)
}

// resolveConfigPath finds host.yaml when --config is not given. It looks, in
// order, under configs/ and then the bare directory, relative to both the binary's
// directory and the current working directory, and returns the FIRST match plus any
// OTHER distinct files that also matched. The caller announces the chosen path and
// warns when alternates exist: a stale installed config silently shadowing the
// intended one is a real foot-gun (it would migrate the wrong accounts). Matches
// that resolve to the SAME file via different bases (e.g. the binary dir IS the cwd)
// are de-duplicated by inode (os.SameFile), so they never look ambiguous.
func resolveConfigPath(explicit string) (path string, alternates []string, err error) {
	if explicit != "" {
		return explicit, nil, nil
	}

	var bases []string
	if exe, err := os.Executable(); err == nil {
		bases = append(bases, filepath.Dir(exe))
	}
	bases = append(bases, ".") // current working directory

	var found []string
	var infos []os.FileInfo
	for _, base := range bases {
		for _, rel := range []string{
			filepath.Join("configs", "host.yaml"),
			"host.yaml",
		} {
			cand := filepath.Join(base, rel)
			fi, statErr := os.Stat(cand)
			if statErr != nil {
				continue
			}
			dup := false
			for _, prev := range infos {
				if os.SameFile(prev, fi) {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			infos = append(infos, fi)
			found = append(found, cand)
		}
	}
	if len(found) == 0 {
		return "", nil, fmt.Errorf("host.yaml not found (use --config); copy configs/host_template.yaml to configs/host.yaml")
	}
	return found[0], found[1:], nil
}

func usage() {
	fmt.Fprintf(os.Stderr, `cpanel-self-migration — cPanel email + website-file + database migration tool

Usage: %s [--apply|--apply-mirror|--dry-run] [--mail] [--file] [--db] [--full] [--verify-checksums] [--config PATH] [--log-level LEVEL]

  (default) dry-run  : analyze + compare SOURCE/DEST, no changes
  --apply            : create missing domains + migrate the selected data
  --apply-mirror     : like --apply, but MIRROR each mailbox — rename the dest
                       mailbox aside (<user>-bak) and re-copy ALL messages, so
                       mail existing only on the dest is removed from the live
                       mailbox; files/databases behave as under --apply
  --mail             : migrate MAIL only (mailboxes: accounts + messages)
  --file             : migrate WEBSITE FILES only (docroots / public_html)
  --db               : migrate DATABASES only (MySQL: data + users + grants)
                       (with none of --mail/--file/--db, ALL are migrated)
  --full             : with --apply, force re-sync of every mailbox
  --verify-checksums : with --apply, compare the exact message-ID set when
                       count+UIDVALIDITY match, before skipping a mailbox
  --deep-verify      : with --apply, verify by CONTENT hash (sha256 per web
                       file) instead of metadata only — catches same-size
                       corruption; slower (reads every byte on both sides)
  --config PATH      : path to host.yaml (default: configs/host.yaml)
  --log-level LEVEL  : info (default) or debug (verbose diagnostics to stderr)

The SOURCE host is ALWAYS read-only; all writes target the DESTINATION.
Mail is normally MERGED into the destination (existing messages are kept, only
missing ones are copied); --apply-mirror instead makes each mailbox an EXACT
copy of the source, moving any dest-only mail aside to <user>-bak first. Do NOT
use --apply-mirror after switching the MX to the new server: it would remove
mail delivered there. Website files are copied as a MIGRATION (the destination
docroot is emptied first, within a safety guard, then mirrored from the source).
Databases are dumped read-only (mysqldump --single-transaction), recreated on
the destination with the destination account prefix, and each site's
wp-config.php is rewritten to point at the new database. Logs are written under
logs/.
`, os.Args[0])
}
