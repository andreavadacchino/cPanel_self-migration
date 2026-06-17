package maildir

// mailboxGuardScript returns POSIX-sh function definitions for the canonical
// mailbox-root containment guard. It is PREPENDED to the maildir scripts that read,
// rename, or extract into a mailbox root so a root that is a `.`/`..`-resolving path
// OR a SYMLINK escaping ~/mail can never be operated on.
//
// Why a canonical (realpath) guard and not the old lexical `case "$md" in
// "$HOME"/mail/?*/?*`: the lexical check is string-only, so it PASSES
// `$HOME/mail/<dom>/..` (the glob `?*` matches `..`) and PASSES a symlinked
// `$HOME/mail/<dom>/<user>` (the literal string is fine; the resolved target is
// never inspected). The destination tar extract then `cd`s through the symlink and
// scatters the archive into the link target (e.g. /etc or another account). This
// mirrors the dbmig writeConfigScript / webfiles CanonicalDestDocroot guards.
//
// guard_mailbox_path <path> verifies <path> is shaped exactly $HOME/mail/<dom>/<user>
// and is contained in ~/mail, then prints it (the caller operates on the printed
// path). It is ABSENCE-tolerant: the mailbox (a fresh destination) or even ~/mail may
// not exist yet, and an absent mailbox is valid (the mirror/probe callers report
// NOBAK/ABSENT, the extract creates it). So it:
//   - lexically requires exactly two non-empty, non-`.`/`..` segments under ~/mail
//     (using the RAW $HOME prefix, no extra depth);
//   - rejects a mailbox root that is itself a symlink (the escape vector for the
//     destructive cd/extract/rename);
//   - canonicalizes the DEEPEST EXISTING ancestor (stopping at a symlink, even a
//     dangling one) and requires it to be $HOME, ~/mail, or under ~/mail — so a
//     symlinked ~/mail or <dom> dir that escapes is caught, while a /home2-style
//     symlinked $HOME (canonicalized) and a not-yet-created leaf both pass.
//
// On any violation it prints a `GUARD:` line to stderr and returns non-zero.
func mailboxGuardScript() string {
	return `canon_existing_path() {
  if command -v realpath >/dev/null 2>&1; then realpath -e -- "$1" 2>/dev/null && return 0; fi
  if command -v readlink >/dev/null 2>&1; then readlink -e -- "$1" 2>/dev/null && return 0; fi
  return 10
}
guard_mailbox_path() {
  gp="${1:-}"
  [ -n "$gp" ] || { echo "GUARD: empty mailbox path" >&2; return 11; }
  case "$gp" in "$HOME"/mail/?*/?*) : ;; *) echo "GUARD: not under ~/mail/<dom>/<user>: $gp" >&2; return 12 ;; esac
  gsub="${gp#"$HOME"/mail/}"
  gdom="${gsub%%/*}"; guser="${gsub#*/}"
  case "$gdom"  in ""|.|..|*/*) echo "GUARD: illegal domain segment: $gp" >&2; return 12 ;; esac
  case "$guser" in ""|.|..|*/*) echo "GUARD: illegal user segment: $gp" >&2; return 12 ;; esac
  [ -L "$gp" ] && { echo "GUARD: mailbox root is a symlink: $gp" >&2; return 15; }
  home_real="$(canon_existing_path "$HOME")" || { echo "GUARD: cannot resolve HOME: $HOME" >&2; return 13; }
  probe="$gp"
  while [ ! -e "$probe" ] && [ ! -L "$probe" ]; do p="$(dirname "$probe")"; [ "$p" = "$probe" ] && break; probe="$p"; done
  [ -L "$probe" ] && [ ! -e "$probe" ] && { echo "GUARD: dangling symlink in mailbox path: $probe" >&2; return 14; }
  real="$(canon_existing_path "$probe")" || { echo "GUARD: cannot resolve mailbox path: $probe" >&2; return 13; }
  case "$real" in
    "$home_real"|"$home_real"/mail|"$home_real"/mail/?*) : ;;
    *) echo "GUARD: mailbox path escapes ~/mail: $probe -> $real" >&2; return 14 ;;
  esac
  printf '%s\n' "$gp"
}
`
}
