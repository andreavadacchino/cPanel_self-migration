package webfiles

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/validate"
)

// DefaultBatchMaxBytes caps the total size of one transfer batch (500 MB),
// matching the maildir transfer. A docroot can be hundreds of MB, so batching
// keeps each tar stream short enough to be timeout-resistant and individually
// retryable.
const DefaultBatchMaxBytes int64 = 500 * 1024 * 1024

// systemExcludes are cPanel-managed entries that must NEVER be copied from a
// docroot. They matter most for the SOURCE main domain, whose docroot IS
// public_html and therefore also contains these. .well-known is intentionally
// NOT here: modern sites use it for ACME, security.txt, app association files,
// and other user-served content that must migrate and verify like any other file.
var systemExcludes = []string{"cgi-bin", ".ftpquota"}

// FileEntry is one path under a docroot (relative to the docroot root) with its
// size. Directories carry Size 0 and IsDir true so empty directories survive the
// files-from transfer.
type FileEntry struct {
	RelPath string
	Size    int64
	IsDir   bool
}

// Runner is the subset of *sshx.Client this package needs for listing/gather
// (satisfied by it). Lets tests substitute a fake that returns canned output.
type Runner interface {
	RunScript(ctx context.Context, script string, env map[string]string) ([]byte, error)
}

// SplitBatches groups files so each batch's total size is at most max, in input
// order. A single file larger than max becomes its own batch. Empty input yields
// no batches. (Same algorithm as maildir.SplitBatches; duplicated here to keep
// the package boundary clean — it is tiny and pure.)
func SplitBatches(files []FileEntry, max int64) [][]FileEntry {
	if len(files) == 0 {
		return nil
	}
	var batches [][]FileEntry
	var cur []FileEntry
	var curBytes int64
	for _, f := range files {
		if len(cur) > 0 && curBytes+f.Size > max {
			batches = append(batches, cur)
			cur = nil
			curBytes = 0
		}
		cur = append(cur, f)
		curBytes += f.Size
	}
	if len(cur) > 0 {
		batches = append(batches, cur)
	}
	return batches
}

// excludePruneExpr builds the find expression fragment that prunes the cPanel
// system entries AT THE DOCROOT TOP LEVEL ONLY, e.g. (base = "$d"):
//
//	\( -path "$d"/cgi-bin -o -path "$d"/.ftpquota \) -prune
//
// base is the find starting point as written in the calling script ("$d" for the
// gather's `find "$d"`, "." for the listing's `find .`). Anchoring each exclusion
// with -path to <base>/<name> means only a TOP-LEVEL cgi-bin/.ftpquota
// is excluded — these are cPanel-provisioned at the docroot root. A user directory
// that merely happens to be named the same DEEPER in the tree (e.g. a plugin's
// wp-content/.../cgi-bin) is copied, not
// silently dropped with no verify signal. Plain -name matched that name at EVERY
// depth, which was real data loss.
//
// It is shared by the size/count gather and the file listing, so both apply
// EXACTLY the same exclusions (the reported size always matches what is copied).
// NOTE: this prune form is for read-only -print/-printf traversals. It must NOT
// be combined with -delete: GNU find's -delete implies -depth, under which
// -prune is ignored — see excludeNotNameExpr / emptyDestScript for the deletion
// path, which uses the -maxdepth 1 / ! -name / rm form (already top-level-anchored).
func excludePruneExpr(base string) string {
	parts := make([]string, len(systemExcludes))
	for i, n := range systemExcludes {
		parts[i] = fmt.Sprintf("-path %s/%s", base, n)
	}
	return fmt.Sprintf(`\( %s \) -prune`, strings.Join(parts, " -o "))
}

// excludeNotNameExpr builds the `! -name 'x' ! -name 'y' ...` fragment used by
// the destination emptying. Unlike -prune, this composes correctly with a
// top-level (-maxdepth 1) `rm -rf` sweep that does not rely on -delete/-depth.
func excludeNotNameExpr() string {
	parts := make([]string, len(systemExcludes))
	for i, n := range systemExcludes {
		parts[i] = fmt.Sprintf("! -name '%s'", n)
	}
	return strings.Join(parts, " ")
}

// gatherScript returns the read-only SOURCE script that reports a docroot's
// total file bytes and file count, applying the system exclusions. It prints:
//
//	ABSENT          if the docroot does not exist
//	<bytes>|<count> otherwise (bytes = sum of real file sizes that will be copied)
//
// A SINGLE find traversal streams each file's size to awk, which sums the bytes
// AND counts the lines in one pass (the previous form walked the tree twice —
// once for bytes, once for the count — doubling the I/O on large docroots). The
// docroot path is passed via the DOCROOT env var (never interpolated).
//
// Symlinks (-type l) are counted alongside regular files because the copy now
// transfers them (see listScript): %s on a symlink is the byte length of its
// target path — small, identical on both sides after the copy — so including
// them keeps the gather count/bytes consistent with what the transfer sends and
// the verify re-measures (a symlink that fails to copy now shows as a shortfall).
func gatherScript() string {
	ex := excludePruneExpr(`"$d"`)
	return fmt.Sprintf(`set -u
d="$DOCROOT"
[ -d "$d" ] || { echo ABSENT; exit 0; }
{ [ -r "$d" ] && [ -x "$d" ]; } || { echo UNREADABLE; exit 0; }
find "$d" -mindepth 1 %s -o \( -type f -o -type l \) -printf '%%s\n' 2>/dev/null | awk '{s+=$1;n++} END{print s+0 "|" n+0}'
`, ex)
}

// digestScript is the read-only AGGREGATE-DIGEST probe used by the verify FALLBACK
// when a docroot exceeds the manifest cap (too many entries to hold a per-path map).
// In one round-trip it prints the same "<bytes>|<count>" total as gatherScript AND a
// "DIGEST <sha256>" over the docroot's name/size/type(/symlink-target) list, so the
// fallback catches divergences that count+bytes alone miss — renames, a compensating
// add+remove of equal size, type changes (file<->symlink), and symlink retargets —
// WITHOUT reading file bodies (same-name/same-size/different-content stays --deep's
// job, out of reach for an O(1)-memory >cap fallback).
//
// The digest enumerates EXACTLY the entry set the copy mirrors (listScript: regular
// files with size, symlinks with their target, empty dirs) under the SAME exclusions,
// so a faithful mirror digests identically on both hosts. Records are NUL-delimited (a
// name may contain a newline), LC_ALL=C sorted (locale- AND traversal-order-
// independent), and OMIT the mode bit (a mode-only drift is soft in DiffManifests, so
// promoting it to a hard fallback divergence would be stricter than the per-path verify
// this stands in for). The ABSENT/UNREADABLE root preamble matches gatherScript so a
// present-but-unreadable root is never certified a clean match.
//
// #5 residual: a subtree unreadable IDENTICALLY on both hosts is omitted from both
// digests and so still matches (the deferred #PU residual). It is NOT closed here by
// trapping find's exit (pipefail), which would also fire on a benign live-source file
// vanishing mid-walk — the project rejects trapping that ambiguous racy signal. The
// copy step's listScript (an unpiped find) is the fail-closed backstop for it.
func digestScript() string {
	ex := excludePruneExpr(".")
	return fmt.Sprintf(`set -u
d="$DOCROOT"
[ -d "$d" ] || { echo ABSENT; exit 0; }
{ [ -r "$d" ] && [ -x "$d" ]; } || { echo UNREADABLE; exit 0; }
cd "$d" 2>/dev/null || { echo UNREADABLE; exit 0; }
find . -mindepth 1 %s -o \( -type f -o -type l \) -printf '%%s\n' 2>/dev/null | awk '{s+=$1;n++} END{print s+0 "|" n+0}'
# The digest needs GNU sha256sum and 'sort -z'. On a non-GNU source host either may be
# missing (the binary may exist but reject -z). Without a guard, a missing 'sort -z'
# would hash EMPTY input -> the same fixed hash on both sides -> a silent FALSE OK that
# nullifies this check while claiming it ran; a missing sha256sum would FALSE-DIFF every
# >cap docroot. Probe both and emit NODIGEST instead, which the caller maps to UNVERIFIED
# (fail closed), mirroring the maildir deep-verify sha256sum guard.
if command -v sha256sum >/dev/null 2>&1 && printf '' | sort -z >/dev/null 2>&1; then
  h=$(find . -mindepth 1 %s -o -type f -printf 'f\t%%s\t%%P\t\0' -o -type l -printf 'l\t0\t%%P\t%%l\0' -o -type d -empty -printf 'd\t0\t%%P\t\0' 2>/dev/null | LC_ALL=C sort -z | sha256sum | cut -d' ' -f1)
  echo "DIGEST $h"
else
  echo NODIGEST
fi
`, ex, ex)
}

// contentDigestScript is the read-only AGGREGATE-CONTENT-DIGEST probe used by the
// DEFAULT (non-deep) web verify to prove a 1:1 copy of file CONTENT in O(1) Go memory.
// Unlike digestScript (name/size/type/target only), it hashes every regular file's
// BODY and folds those per-file hashes — plus symlink targets and empty-dir presence —
// into ONE streaming sha256 over the whole tree. A faithful mirror produces the same
// aggregate hash on both hosts; a same-name/same-size/different-CONTENT divergence (the
// gap a metadata-only manifest cannot see) flips it. It is the tree-granularity sibling
// of --deep-verify's per-file H records (manifestScript): both define "content" the same
// way (sha256 of the file body), but this one never holds a per-path map, so it works on
// an over-cap docroot too. Output is IDENTICAL in shape to digestScript (parsed by the
// shared parseDigestOutput): "<bytes>|<count>", then "DIGEST <hex>" or "NODIGEST".
//
// Records (one per copied entry, NUL-delimited, LC_ALL=C sorted so traversal order and
// locale do not matter) are:
//
//	f\t<relpath>\t<sha256-of-body>\0   regular file (body hashed; '?unreadable' if it could not be read)
//	l\t<relpath>\t<symlink-target>\0   symlink (its target IS its content; tar mirrors the link itself)
//	d\t<relpath>\t\0                   empty directory (presence only)
//
// The MODE bit is deliberately omitted (a mode-only drift is soft in the per-path verify
// this stands in for; promoting it here would be stricter than the tier it backs). The
// entry set and exclusions match listScript EXACTLY, so an excluded cgi-bin/.ftpquota is
// never a spurious content divergence.
//
// Like digestScript it does NOT use `set -o pipefail`: trapping find's exit would also
// fire on a benign live-source file vanishing mid-walk, the racy signal the project
// rejects. A subtree unreadable IDENTICALLY on both hosts is omitted from both digests
// and still matches (the deferred #PU residual); the copy step's listScript (an unpiped
// find) is the fail-closed backstop. A per-file unreadable on ONE side hashes to the
// '?unreadable' sentinel, so it diverges from the readable side. The tools-missing probe
// (sha256sum + `sort -z`) emits NODIGEST -> the caller treats it as content-unverified
// (fail-soft at default), never a silent OK.
func contentDigestScript() string {
	ex := excludePruneExpr(".")
	return fmt.Sprintf(`set -u
d="$DOCROOT"
[ -d "$d" ] || { echo ABSENT; exit 0; }
{ [ -r "$d" ] && [ -x "$d" ]; } || { echo UNREADABLE; exit 0; }
cd "$d" 2>/dev/null || { echo UNREADABLE; exit 0; }
# A regular file whose BODY cannot be read means this docroot's content cannot be
# certified: fail closed (UNREADABLE) rather than hash an '?unreadable' sentinel into the
# digest. Two same-name/same-size files whose bodies are BOTH unreadable would otherwise
# emit the SAME sentinel record on both hosts and digest equal — a false OK that
# --deep-verify catches as ContentUnverified. GNU find's "! -readable" tests access as the
# running (account) user; on a non-GNU find the test errors (no match) but the digest
# tools below are then absent too, so the caller already content-soft-fails (NODIGEST).
if find . -mindepth 1 %s -o -type f ! -readable -print 2>/dev/null | grep -q .; then
  echo UNREADABLE; exit 0
fi
find . -mindepth 1 %s -o \( -type f -o -type l \) -printf '%%s\n' 2>/dev/null | awk '{s+=$1;n++} END{print s+0 "|" n+0}'
if command -v sha256sum >/dev/null 2>&1 && printf '' | sort -z >/dev/null 2>&1; then
  h=$( { find . -mindepth 1 %s -o -type f -print0 2>/dev/null | while IFS= read -r -d '' p; do
      b=$(sha256sum -- "$p" 2>/dev/null | cut -d' ' -f1)
      [ -n "$b" ] || b='?unreadable'
      printf 'f\t%%s\t%%s\0' "${p#./}" "$b"
    done
    find . -mindepth 1 %s -o -type l -printf 'l\t%%P\t%%l\0' 2>/dev/null
    find . -mindepth 1 %s -o -type d -empty -printf 'd\t%%P\t\0' 2>/dev/null
  } | LC_ALL=C sort -z | sha256sum | cut -d' ' -f1 )
  echo "DIGEST $h"
else
  echo NODIGEST
fi
`, ex, ex, ex, ex, ex)
}

// parseDigestOutput parses digestScript's output: an ABSENT/UNREADABLE status, a
// "<bytes>|<count>" total, and a "DIGEST <hex>" line (line order independent). Pure.
//
// UNREADABLE is STICKY: once any line reports the docroot unreadable, neither a
// following ABSENT line nor a following "<bytes>|<count>" total downgrades the status
// to absent/present — so the fail-CLOSED verdict is order-independent (a
// present-but-unreadable docroot can never read as a clean present). The real scripts
// emit UNREADABLE alone (echo UNREADABLE; exit 0), so this only matters for garbled or
// interleaved output. (The present-vs-absent ordering among the NON-unreadable lines is
// deliberately NOT guaranteed: it can only surface a divergence, never hide one.)
func parseDigestOutput(out string) (bytes int64, count int, digest string, status sizeStatus) {
	status = sizeAbsent
	for _, line := range strings.Split(out, "\n") {
		s := strings.TrimSpace(line)
		switch {
		case s == "":
			continue
		case strings.HasPrefix(s, "UNREADABLE"):
			status = sizeUnreadable
		case strings.HasPrefix(s, "ABSENT"):
			if status != sizeUnreadable {
				status = sizeAbsent
			}
		case s == "NODIGEST":
			// Present docroot, but the digest tools (sha256sum / sort -z) are unavailable
			// on the host. Leave digest "" so the caller treats it as UNVERIFIED (not OK,
			// not DIFF). A present empty tree, by contrast, yields the non-empty empty-input
			// sha256, so digest=="" on a present docroot uniquely means "tools unavailable".
			digest = ""
		case strings.HasPrefix(s, "DIGEST "):
			digest = strings.TrimSpace(strings.TrimPrefix(s, "DIGEST "))
		default:
			if i := strings.IndexByte(s, '|'); i >= 0 {
				b, e1 := strconv.ParseInt(strings.TrimSpace(s[:i]), 10, 64)
				c, e2 := strconv.Atoi(strings.TrimSpace(s[i+1:]))
				if e1 == nil && e2 == nil {
					bytes, count = b, c
					// Do NOT downgrade an UNREADABLE already seen to present (fail closed),
					// mirroring the ABSENT branch above. The bytes/count are still captured
					// (informational), but the status stays UNREADABLE.
					if status != sizeUnreadable {
						status = sizePresent
					}
				}
			}
		}
	}
	return bytes, count, digest, status
}

// listScript returns the read-only SOURCE script that lists, relative to the
// docroot, every regular file (with size), every SYMLINK, AND every empty
// directory, applying the system exclusions. Each record is NUL-terminated (\0)
// and tab-separated:
//
//	f<TAB><size><TAB><relpath>\0   for a regular file
//	l<TAB>0<TAB><relpath>\0        for a symlink (tar archives the link itself)
//	d<TAB>0<TAB><relpath>\0        for an empty directory (so it is recreated)
//
// Symlinks MUST be listed: `tar -c --no-recursion --files-from=-` archives a
// listed symlink AS a symlink (it does not dereference without -h), so naming it
// is enough to mirror it. Before this they were neither listed nor counted, so a
// docroot's symlinks (e.g. a shared-uploads or vendored-library link) were
// silently dropped with no verify signal — real data loss. A symlink to a
// directory is still NOT recursed (--no-recursion), so only the link is copied,
// never a runaway target tree.
//
// NUL terminates records (not newline) so a file or directory name containing a
// space or newline — e.g. an upload "my photo.jpg" — survives intact; the three
// fields are split on TAB (a path containing a TAB is rejected by validate.RelPath).
// Empty directories are listed explicitly because the files-from tar would
// otherwise not recreate a directory that contains no files.
func listScript() string {
	ex := excludePruneExpr(".")
	return fmt.Sprintf(`set -u
cd "$DOCROOT" 2>/dev/null || { echo "NODIR"; exit 0; }
find . -mindepth 1 %s -o -type f -printf 'f\t%%s\t%%P\0' -o -type l -printf 'l\t0\t%%P\0'
find . -mindepth 1 %s -o -type d -empty -printf 'd\t0\t%%P\0'
`, ex, ex)
}

// emptyDestScript returns the DESTINATION script that empties a docroot before
// the copy (MIGRATION semantics: the destination becomes an exact mirror).
//
// It carries a HARD SAFETY GUARD: it refuses to empty anything that is the
// account's public_html root exactly, that contains a `..` path component, or
// that does not canonically resolve strictly UNDER public_html. $HOME/public_html
// and DEST_DOCROOT are resolved on the DESTINATION host, never locally, so a
// malformed path or symlink cannot trick it into deleting the home directory or
// another domain's files. The system entries (cgi-bin, .ftpquota) are preserved,
// matching what the copy excludes.
func destDocrootGuardScript() string {
	return `canon_existing_path() {
  if command -v realpath >/dev/null 2>&1; then realpath -e -- "$1" 2>/dev/null && return 0; fi
  if command -v readlink >/dev/null 2>&1; then readlink -e -- "$1" 2>/dev/null && return 0; fi
  return 10
}
canon_maybe_path() {
  if command -v realpath >/dev/null 2>&1; then realpath -m -- "$1" 2>/dev/null && return 0; fi
  if command -v readlink >/dev/null 2>&1; then readlink -m -- "$1" 2>/dev/null && return 0; fi
  return 10
}
guard_under_public_html_or_root() {
  raw="${1:-}"
  if [ -z "$raw" ]; then echo "GUARD: empty destination docroot" >&2; return 10; fi
  case "$raw" in
    /*) : ;;
    *) echo "GUARD: destination docroot must be absolute: $raw" >&2; return 10 ;;
  esac
  case "/$raw/" in
    */../*) echo "GUARD: refuse destination docroot containing '..': $raw" >&2; return 12 ;;
  esac
  ph="$HOME/public_html"
  ph_real="$(canon_existing_path "$ph")" || { echo "GUARD: cannot resolve public_html root ($ph)" >&2; return 10; }
  d_real="$(canon_maybe_path "$raw")" || { echo "GUARD: cannot resolve destination docroot ($raw)" >&2; return 10; }
  case "$d_real" in
    "$ph_real"|"$ph_real"/*) printf '%s\n' "$d_real" ;;
    *) echo "GUARD: refuse, target escapes ~/public_html: $raw -> $d_real" >&2; return 12 ;;
  esac
}
guard_dest_docroot() {
  raw="${1:-}"
  d_real="$(guard_under_public_html_or_root "$raw")" || return $?
  ph_real="$(canon_existing_path "$HOME/public_html")" || { echo "GUARD: cannot resolve public_html root ($HOME/public_html)" >&2; return 10; }
  if [ "$d_real" = "$ph_real" ]; then
    echo "GUARD: refuse public_html root ($raw -> $d_real)" >&2
    return 11
  fi
  printf '%s\n' "$d_real"
}
ensure_guarded_dest_docroot_dir() {
  DEST_DOCROOT_CANON="$(guard_dest_docroot "$1")" || return $?
  if [ -e "$DEST_DOCROOT_CANON" ]; then
    [ -d "$DEST_DOCROOT_CANON" ] || { echo "GUARD: destination docroot is not a directory: $DEST_DOCROOT_CANON" >&2; return 13; }
  else
    parent="${DEST_DOCROOT_CANON%/*}"
    parent_real="$(guard_under_public_html_or_root "$parent")" || return $?
    [ -d "$parent_real" ] || { echo "GUARD: destination docroot parent is missing: $parent_real" >&2; return 13; }
    mkdir -- "$DEST_DOCROOT_CANON" || { echo "GUARD: cannot create destination docroot: $DEST_DOCROOT_CANON" >&2; return 13; }
  fi
  DEST_DOCROOT_CANON="$(guard_dest_docroot "$DEST_DOCROOT_CANON")" || return $?
  [ -d "$DEST_DOCROOT_CANON" ] || { echo "GUARD: destination docroot is not a directory after creation: $DEST_DOCROOT_CANON" >&2; return 13; }
}
enter_guarded_dest_docroot() {
  ensure_guarded_dest_docroot_dir "$1" || return $?
  cd -P -- "$DEST_DOCROOT_CANON" || { echo "GUARD: cannot cd into $DEST_DOCROOT_CANON" >&2; return 13; }
  pwd_real="$(pwd -P)" || { echo "GUARD: cannot resolve current destination docroot" >&2; return 13; }
  checked="$(guard_dest_docroot "$pwd_real")" || return $?
  [ "$pwd_real" = "$checked" ] || { echo "GUARD: destination docroot changed while entering: $pwd_real -> $checked" >&2; return 12; }
  DEST_DOCROOT_CANON="$pwd_real"
}
`
}

func emptyDestScript() string {
	ex := excludeNotNameExpr()
	// Remove only the docroot's TOP-LEVEL entries (-maxdepth 1) that are not
	// protected system entries, recursively via `rm -rf` (NOT find -delete:
	// -delete implies -depth, which silently ignores -prune/-name filtering and
	// would either delete nothing or delete protected dirs' contents). Operating
	// at depth 1 with `! -name` is unambiguous and safe.
	return fmt.Sprintf(`set -u
%s
enter_guarded_dest_docroot "$DEST_DOCROOT" || exit $?
find . -mindepth 1 -maxdepth 1 %[2]s -exec rm -rf {} + 2>/dev/null || true
# Fail closed if any non-protected top-level entry SURVIVED the cleanup (immutable
# flag, read-only mount, denied permission, transient rm error). The `+"`rm`"+`
# above suppresses its own errors, so without this gate the script would exit 0 and
# the caller would extract OVER the leftover content, leaving destination-only files
# live in production while every retry repeats the same masked failure. Reuse the
# SAME exclusion as the delete so preserved system entries are not counted.
if find . -mindepth 1 -maxdepth 1 %[2]s 2>/dev/null | grep -q .; then
  echo "GUARD: destination docroot not empty after cleanup: $DEST_DOCROOT_CANON" >&2
  exit 14
fi
`, destDocrootGuardScript(), ex)
}

// backupDestScript prepares the non-destructive path for an empty or directory-only
// source: a populated docroot is renamed aside to the first free
// "<docroot>-bak[.N]" and a fresh empty docroot is left in its place, with the
// protected system entries (cgi-bin/.ftpquota) moved back into the live docroot.
// Directory-only copies then stream their listed directory entries into that fresh
// docroot. It prints exactly one status line:
//
//	BAKDIR <name>   existing content was renamed aside to <name>
//	NOBAK           the docroot was absent or already empty — nothing to back up
//
// Same HARD GUARDS as emptyDestScript: it refuses anything not canonically and
// strictly under ~/public_html and never renames public_html itself;
// $HOME/public_html is resolved from the DESTINATION shell, never from the path
// we pass.
func backupDestScript() string {
	keep := excludeNotNameExpr()
	var moveBack strings.Builder
	for _, s := range systemExcludes {
		// Restore each protected system entry into the fresh live docroot. An entry
		// that was not present is simply skipped, but one that EXISTS and cannot be
		// moved back (immutable flag, denied permission, read-only mount) must FAIL
		// CLOSED: otherwise the script would print BAKDIR and exit 0 while the live
		// docroot is silently missing cgi-bin/.ftpquota. The old content is safe in
		// $bak, so exit 14 (same code as the main rename failure) leaves a clean,
		// recoverable state the caller turns into an aborted copy.
		fmt.Fprintf(&moveBack, "  if [ -e \"$bak/%[1]s\" ]; then mv \"$bak/%[1]s\" \"$d/%[1]s\" || { echo \"GUARD: could not restore system entry %[1]s into $d\" >&2; exit 14; }; fi\n", s)
	}
	return fmt.Sprintf(`set -u
%s
raw_d="$(guard_dest_docroot "$DEST_DOCROOT")" || exit $?
if [ ! -e "$raw_d" ]; then
  ensure_guarded_dest_docroot_dir "$raw_d" || exit $?
  echo "NOBAK"
  exit 0
fi
ensure_guarded_dest_docroot_dir "$raw_d" || exit $?
d="$DEST_DOCROOT_CANON"
if [ -z "$(find "$d" -mindepth 1 -maxdepth 1 %s -print -quit 2>/dev/null)" ]; then
  echo "NOBAK"; exit 0
fi
bak="${d}-bak"
if [ -e "$bak" ]; then n=2; while [ -e "${d}-bak.${n}" ]; do n=$((n + 1)); done; bak="${d}-bak.${n}"; fi
if mv -- "$d" "$bak"; then
  ensure_guarded_dest_docroot_dir "$d" || exit $?
  d="$DEST_DOCROOT_CANON"
%s  echo "BAKDIR $(basename "$bak")"
  exit 0
fi
echo "GUARD: could not rename $d aside" >&2
exit 14
`, destDocrootGuardScript(), keep, moveBack.String())
}

// parseBackupResult interprets backupDestScript's single status line. Pure.
func parseBackupResult(out string) SyncResult {
	for _, line := range strings.Split(out, "\n") {
		switch line = strings.TrimSpace(line); {
		case strings.HasPrefix(line, "BAKDIR "):
			return SyncResult{BackedUpDir: strings.TrimSpace(strings.TrimPrefix(line, "BAKDIR "))}
		}
	}
	return SyncResult{} // NOBAK / empty: nothing was backed up
}

// srcTarCmd is the read-only SOURCE tar command: archive the listed files from
// the docroot to stdout. The file list is fed via stdin (--files-from=-) so a
// long list never overflows the SSH exec channel. --no-recursion: only the
// exact listed entries (we already enumerated files + empty dirs).
const srcTarCmd = `cd "$SRC_DOCROOT" && tar -c --null --no-recursion --files-from=- -f -`

// extractCmd is the DESTINATION tar command: extract the incoming archive into
// the (already-emptied) docroot. No --keep-newer-files: the dir was emptied, so
// the source always wins (migration, not sync).
//
// -p (--preserve-permissions) restores each entry's archived mode bits IGNORING
// the destination umask, so the destination is a faithful mirror of the source's
// permissions (without it, a non-root extract would silently mask them) — and it
// makes the manifest verify's per-file mode comparison meaningful instead of a
// guaranteed false positive. It restores mode bits only, never ownership (that
// needs root and is not requested), so it is safe for the unprivileged cPanel user.
//
// Path-traversal safety is enforced in Go (parseFileList drops any entry that is
// absolute or contains a `..` component before it ever reaches --files-from), and
// GNU tar additionally strips a leading '/' and refuses `..` on extraction BY
// DEFAULT. We deliberately do NOT pass --no-absolute-names: it is not accepted by
// every GNU tar build in the field (some report it as an unrecognized option),
// and the default behavior already provides the guarantee.
var extractCmd = destDocrootGuardScript() + `enter_guarded_dest_docroot "$DEST_DOCROOT" || exit $?
tar -xp -f -`

// The tar-bridge env prelude (export VAR='…';) and its single-quote escaping live
// in the single source sshx.WithEnv / sshx.SingleQuoteEscape.

// sizeStatus is the tri-state outcome of parseSize: a docroot is present (a real
// "<bytes>|<count>" was parsed), absent (ABSENT / unparseable), or unreadable
// (UNREADABLE — it exists but is not readable/traversable, so it must fail closed
// and NOT be folded into "empty"). The unreadable case is what keeps a permission
// problem from looking like a genuinely empty docroot.
type sizeStatus int

const (
	sizePresent sizeStatus = iota
	sizeAbsent
	sizeUnreadable
)

// parseSize parses the gatherScript output ("ABSENT", "UNREADABLE", or
// "<bytes>|<count>"). Pure; unit-tested.
func parseSize(out string) (bytes int64, count int, status sizeStatus) {
	s := strings.TrimSpace(out)
	if strings.HasPrefix(s, "UNREADABLE") {
		return 0, 0, sizeUnreadable
	}
	if s == "" || strings.HasPrefix(s, "ABSENT") {
		return 0, 0, sizeAbsent
	}
	i := strings.IndexByte(s, '|')
	if i < 0 {
		return 0, 0, sizeAbsent
	}
	b, err1 := strconv.ParseInt(strings.TrimSpace(s[:i]), 10, 64)
	c, err2 := strconv.Atoi(strings.TrimSpace(s[i+1:]))
	if err1 != nil || err2 != nil {
		return 0, 0, sizeAbsent
	}
	return b, c, sizePresent
}

// parseFileLine parses ONE listScript record ("<type>\t<size>\t<relpath>") into a
// FileEntry. ok=false for blank/NODIR/malformed/unsafe records; unsafe is true
// only when the record was dropped specifically because its path failed the
// path-traversal guard (so the caller can aggregate-warn those, distinct from
// benign malformed records). The type tag is 'd' for a directory, 'f' for a
// regular file, and 'l' for a symlink; only 'd' sets IsDir (a symlink is a
// sendable leaf entry, archived as the link itself by the files-from tar).
// Shared by parseFileList (buffered) and the streaming listSrcFiles (per NUL
// record). Pure.
func parseFileLine(line string) (f FileEntry, ok, unsafe bool) {
	if line == "" || strings.HasPrefix(line, "NODIR") {
		return FileEntry{}, false, false
	}
	parts := strings.SplitN(line, "\t", 3)
	if len(parts) != 3 {
		return FileEntry{}, false, false
	}
	typ := parts[0]
	if typ != "f" && typ != "l" && typ != "d" {
		logx.Debug("webfiles: dropping malformed transfer list record with type %q", typ)
		return FileEntry{}, false, false
	}
	sz, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil {
		return FileEntry{}, false, false
	}
	rel := parts[2]
	if rel == "" {
		return FileEntry{}, false, false
	}
	// Defense-in-depth against path traversal: the list comes from `find` on the
	// source and is fed verbatim to `tar --files-from`. A path with `..`, an
	// absolute path, or a control byte must never reach tar (it could read outside
	// the docroot on the source, or write outside it on extract). Such an entry is
	// anomalous, so drop it and trace it.
	if err := validate.RelPath(rel); err != nil {
		logx.Debug("webfiles: dropping unsafe path %q from transfer list: %v", rel, err)
		return FileEntry{}, false, true
	}
	return FileEntry{RelPath: rel, Size: sz, IsDir: typ == "d"}, true, false
}
