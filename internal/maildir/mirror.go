package maildir

import (
	"context"
	"fmt"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// MirrorResult reports what MirrorBox set aside on the destination. BackedUpDir
// is the basename of the "<user>-bak[.N]" directory the existing mailbox was
// renamed to, or "" when there was nothing to move (the destination mailbox was
// absent or already empty).
type MirrorResult struct {
	BackedUpDir string
}

// mirrorBoxScript renames the destination mailbox aside so a subsequent full
// copy makes the destination an EXACT mirror of the source — mail that exists
// only on the destination is moved out of the live mailbox, not merged. It is
// the maildir analogue of webfiles.backupDestScript and carries the SAME hard
// guards: $HOME is resolved from the DESTINATION shell (never from the values we
// pass), DOM/USER arrive as single-quoted env vars (see GetBoxStats), and the
// target must be strictly under ~/mail/<dom>/<user> — so even an empty or
// malformed DOM/USER cannot make it rename $HOME or ~/mail aside.
//
// It prints exactly one status line:
//
//	BAKDIR <name>   the existing mailbox was renamed aside to <name>
//	NOBAK           the mailbox was absent or already empty — nothing to move
//
// The freshly re-created live mailbox inherits the original directory's mode
// (chmod --reference) so Dovecot/cPanel permissions survive the swap.
func mirrorBoxScript() string {
	return mailboxGuardScript() + `set -u
[ -n "$DOM" ] && [ -n "$USER" ] || { echo "GUARD: empty domain or user" >&2; exit 11; }
md="$(guard_mailbox_path "$HOME/mail/$DOM/$USER")" || exit $?
if [ ! -d "$md" ]; then echo "NOBAK"; exit 0; fi
if [ -z "$(find "$md" -mindepth 1 -print -quit 2>/dev/null)" ]; then echo "NOBAK"; exit 0; fi
bak="${md}-bak"
if [ -e "$bak" ]; then n=2; while [ -e "${md}-bak.${n}" ]; do n=$((n + 1)); done; bak="${md}-bak.${n}"; fi
if mv "$md" "$bak"; then
  mkdir -p "$md"
  chmod --reference="$bak" "$md" 2>/dev/null || true
  echo "BAKDIR $(basename "$bak")"
  exit 0
fi
echo "GUARD: could not rename $md aside" >&2
exit 14
`
}

// parseMirrorResult interprets mirrorBoxScript's single status line. Pure.
func parseMirrorResult(out string) MirrorResult {
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "BAKDIR ") {
			return MirrorResult{BackedUpDir: strings.TrimSpace(strings.TrimPrefix(line, "BAKDIR "))}
		}
	}
	return MirrorResult{} // NOBAK / empty: nothing was moved
}

// MirrorBox renames the destination mailbox mail/<dom>/<user> aside (to the
// first free "<user>-bak[.N]") so the caller can re-copy it from scratch and end
// with an EXACT mirror of the source. It runs ONLY on the destination — it takes
// no source client, so the read-only-source invariant holds by construction.
func (t Transfer) MirrorBox(ctx context.Context, dom, user string) (MirrorResult, error) {
	// dom/user passed via env (DOM/USER), never interpolated — see GetBoxStats.
	out, err := t.Dest.RunScript(ctx, mirrorBoxScript(), map[string]string{"DOM": dom, "USER": user})
	if err != nil {
		return MirrorResult{}, fmt.Errorf("mirror %s/%s on %s: %w", dom, user, t.Dest.Name(), err)
	}
	res := parseMirrorResult(string(out))
	logx.Debug("mirror %s/%s on %s: %q -> backedUp=%q", dom, user, t.Dest.Name(), strings.TrimSpace(string(out)), res.BackedUpDir)
	return res, nil
}

// sourceBoxReadableScript proves the SOURCE mailbox root mail/<dom>/<user> EXISTS
// and is a readable, traversable directory — the fail-closed precondition for the
// destructive --apply-mirror rename. It is read-only (no writes), runs ONLY on the
// source, and follows the same env discipline as boxStatsScript/mirrorBoxScript:
// DOM/USER arrive as env vars (never interpolated) and the path is assembled in the
// shell from $HOME (resolved on the source).
//
// It reuses require_listable (the shared fail-closed readability guard) so a
// present-but-unreadable / non-directory root exits non-zero — the caller maps that
// to a FAIL — and then asserts PRESENCE explicitly, because require_listable treats
// an ABSENT path as a clean no-op, which is exactly the silent-emptying case the
// mirror gate must block (GetBoxStats cannot tell ABSENT from EMPTY: both yield
// "0|"/exit 0). It prints exactly one status line:
//
//	PRESENT   the mailbox root exists and is a readable directory (EMPTY is fine)
//	ABSENT    the mailbox root does not exist on the source
//
// (UNREADABLE surfaces as a non-zero exit via require_listable, not a status line.)
var sourceBoxReadableScript = mailboxGuardScript() + statGuardHelper + `set -u
[ -n "$DOM" ] && [ -n "$USER" ] || { echo "GUARD: empty domain or user" >&2; exit 11; }
mb="$(guard_mailbox_path "$HOME/mail/$DOM/$USER")" || exit $?
require_listable "$mb"
if [ -d "$mb" ]; then echo PRESENT; else echo ABSENT; fi
`

// SourceBoxReadable reports whether the SOURCE mailbox root mail/<dom>/<user>
// exists and is a readable directory. It is the fail-closed precondition the
// --apply-mirror path checks BEFORE MirrorBox renames the live destination aside:
// an absent or unreadable source must block that destructive rename so the live
// destination mailbox is never emptied with nothing to copy back.
//
//   - (true, nil)  : root present and readable (EMPTY is fine — mirroring to an
//     empty source is valid; the copy then just clears the dest).
//   - (false, nil) : root ABSENT on the source — caller FAILs the mailbox.
//   - (_, err)     : root present but UNREADABLE/non-directory, or the probe itself
//     failed — caller FAILs the mailbox.
//
// Read-only: takes only the source client, so the read-only-source invariant holds
// by construction (the mirror of MirrorBox, which takes only the dest client).
func (t Transfer) SourceBoxReadable(ctx context.Context, dom, user string) (bool, error) {
	// dom/user passed via env (DOM/USER), never interpolated — see GetBoxStats.
	out, err := t.Src.RunScript(ctx, sourceBoxReadableScript, map[string]string{"DOM": dom, "USER": user})
	if err != nil {
		return false, fmt.Errorf("source mailbox probe %s/%s on %s: %w", dom, user, t.Src.Name(), err)
	}
	// Strict equality, not strings.Contains: this gates the DESTRUCTIVE mirror, so any
	// output other than exactly "PRESENT" (an absent box prints "ABSENT"; an unreadable
	// one already errored above) must read as not-present, fail closed, and leave the
	// live destination untouched — never let unexpected output containing the substring
	// "PRESENT" bypass the gate.
	present := strings.TrimSpace(string(out)) == "PRESENT"
	logx.Debug("source-box probe %s/%s on %s: %q -> present=%v", dom, user, t.Src.Name(), strings.TrimSpace(string(out)), present)
	return present, nil
}
