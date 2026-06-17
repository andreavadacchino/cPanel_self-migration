package maildir

import (
	"context"
	"fmt"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// ReadOption configures a read-only maildir stat/enumeration call.
type ReadOption func(*readConfig)

type readConfig struct {
	guardRoot bool
	onCount   func(int)
}

// GuardRoot makes a read REJECT a mailbox root that is a symlink (or that escapes
// ~/mail) — the SAME containment guard_mailbox_path enforces for the destructive
// mirror/extract path. Use it for DESTINATION-side reads: the destination mailbox is
// provisioned by the migration as a real directory, so a symlinked root is an anomaly
// the guarded write would refuse. Without this, a read that followed the symlink could
// let a fast-skip "unchanged" or a verify "OK" certify a target the copy never wrote
// to. Source reads omit it: the source tar likewise follows the operator's own layout,
// so guarding only one side would make stats and copy disagree.
func GuardRoot() ReadOption { return func(c *readConfig) { c.guardRoot = true } }

// WithProgress reports incremental progress during a STREAMED read: onCount is
// invoked with the running number of messages processed so far (throttled). It is
// honored only by GetMessageDigests (the deep mail verify, which streams one digest
// per message); the non-streaming reads (GetBoxStats/GetFolderStats/GetMessageSet)
// ignore it.
func WithProgress(onCount func(int)) ReadOption { return func(c *readConfig) { c.onCount = onCount } }

// applyReadOpts folds the options into a readConfig.
func applyReadOpts(opts []ReadOption) readConfig {
	var c readConfig
	for _, o := range opts {
		o(&c)
	}
	return c
}

// readEnv builds the script env (DOM/USER) and, when GuardRoot() is set, GUARD_ROOT —
// which the read scripts test before applying guard_mailbox_path to the mailbox root.
func readEnv(dom, user string, opts []ReadOption) map[string]string {
	c := applyReadOpts(opts)
	env := map[string]string{"DOM": dom, "USER": user}
	if c.guardRoot {
		env["GUARD_ROOT"] = "1"
	}
	return env
}

// statGuardHelper is concatenated into every read-only stats/digest script so the
// copies cannot drift. require_listable is the DETERMINISTIC fail-closed check: it
// fails the script (non-zero + stderr) on a path that EXISTS but is not a readable,
// traversable directory, so an unreadable mailbox root or cur/new queue surfaces as
// an error the caller maps to UNREADABLE / UNVERIFIED — never a silent empty count.
// An ABSENT path is a clean no-op (a legitimately mail-less account, or a folder
// without that particular queue).
//
// Permission is checked HERE, before find, rather than by trapping find's exit
// status: a non-zero find is ambiguous (it also fires when a folder legitimately
// VANISHES mid-walk on a live source mailbox — an IMAP client deleting a folder
// during verify), and turning that benign race into a hard failure would spuriously
// fail a healthy migration. So the find calls below tolerate transient errors
// (2>/dev/null) and rely on require_listable for the real unreadable case.
const statGuardHelper = `require_listable() {
  if [ -e "$1" ] && { [ ! -d "$1" ] || [ ! -r "$1" ] || [ ! -x "$1" ]; }; then
    echo "verify: cannot read mailbox path: $1" >&2
    exit 17
  fi
}
`

// boxStatsScript is the read-only GetBoxStats helper. dom/user come from cPanel and
// are passed as environment variables (DOM/USER), never interpolated into the
// command, so a name with shell-special characters cannot break or escape it; the
// mailbox path is assembled from them inside the shell. Held as a package const so a
// test can run it (e.g. as an unprivileged user) directly.
var boxStatsScript = mailboxGuardScript() + statGuardHelper + `set -u
mb="$HOME/mail/$DOM/$USER"
if [ -n "${GUARD_ROOT:-}" ]; then mb="$(guard_mailbox_path "$mb")" || exit $?; fi
require_listable "$mb"
n=0
if [ -d "$mb" ]; then
  n=$(find "$mb/" -type f \( -path '*/cur/*' -o -path '*/new/*' \) 2>/dev/null | wc -l)
fi
u=''
ul="$mb/dovecot-uidlist"
if [ -e "$ul" ]; then
  { [ -f "$ul" ] && [ -r "$ul" ]; } || { echo "verify: cannot read $ul" >&2; exit 19; }
  u=$(head -n 1 "$ul" | awk '{print $2}')
fi
printf '%s|%s\n' "$n" "$u"
`

// GetBoxStats reads a mailbox's message count and INBOX UIDVALIDITY from a host
// (read-only). Feeds the fast-skip and the dry-run compare (the authoritative
// post-migration integrity check uses the per-folder GetFolderStats).
//
// Fails closed on an unreadable/non-directory mailbox ROOT (require_listable → error
// → UNREADABLE), or an unreadable INBOX dovecot-uidlist, instead of the silent zero
// the old `find … 2>/dev/null | wc -l` produced. The whole-tree count itself is best
// effort (an unreadable SUBfolder is undercounted, not fatal): its consumers fail
// safe — the fast-skip then re-copies — and the per-folder GetFolderStats is what
// guards every subfolder for the integrity verdict. An absent mailbox is a clean
// zero. A non-empty box with no UIDVALIDITY parses with an empty UIDVALIDITY; the
// consumer (BoxStats.Consistent) treats that as not-fast-skippable.
func GetBoxStats(ctx context.Context, c *sshx.Client, dom, user string, opts ...ReadOption) (BoxStats, error) {
	out, err := c.RunScript(ctx, boxStatsScript, readEnv(dom, user, opts))
	if err != nil {
		return BoxStats{}, fmt.Errorf("box stats %s/%s on %s: %w", dom, user, c.Name(), err)
	}
	bs, perr := parseBoxStatsStrict(string(out))
	if perr != nil {
		return BoxStats{}, fmt.Errorf("box stats %s/%s on %s: %w", dom, user, c.Name(), perr)
	}
	if bs.UIDValidity == "" {
		logx.Debug("boxstats %s/%s on %s: %d messages, UIDVALIDITY=missing", dom, user, c.Name(), bs.MsgCount)
	} else {
		logx.Debug("boxstats %s/%s on %s: %d messages, UIDVALIDITY=%s", dom, user, c.Name(), bs.MsgCount, bs.UIDValidity)
	}
	return bs, nil
}

// GetFolderStats reads a mailbox's PER-FOLDER message count + UIDVALIDITY (the
// INBOX root and every .Subfolder) from a host, read-only. Maildir++ keeps all
// subfolders flat under the mailbox root as ".Name" / ".Parent.Child" dirs, each
// with its own cur/new and dovecot-uidlist — so a single non-recursive sweep of
// the root plus each .dir covers them all. The aggregate GetBoxStats sums every
// folder into one number; this keeps them separate so a "+5 in Sent, -5 in Trash"
// can no longer net to zero and pass.
//
// dom/user are passed via the environment (DOM/USER), never interpolated. Output:
// one "<folder>\t<count>\t<uidvalidity>" line per folder ("INBOX" for the root).
//
// This is the authoritative integrity reader, so it guards EVERY directory it counts
// (the root, each folder, and each cur/new queue) with require_listable: a
// present-but-unreadable one exits non-zero (mapped to UNREADABLE), never a silent
// zero. A queue (cur or new) that is simply ABSENT counts as zero — a scaffolded or
// half-provisioned folder is not an error. An absent mailbox root is a clean empty
// map. An unreadable dovecot-uidlist is fatal; a genuinely absent one yields an empty
// UIDVALIDITY. The per-queue find itself tolerates a transient mid-walk vanish (a
// live mailbox reorganizing during verify) rather than failing the whole migration —
// readability is already proven by require_listable. parseFolderStatsStrict then
// rejects any malformed/duplicate/invalid row.

// folderStatsScript is the read-only GetFolderStats helper; see boxStatsScript for
// the env/interpolation discipline. emit's count/uid locals are reset per folder.
var folderStatsScript = mailboxGuardScript() + statGuardHelper + `set -u
mb="$HOME/mail/$DOM/$USER"
if [ -n "${GUARD_ROOT:-}" ]; then mb="$(guard_mailbox_path "$mb")" || exit $?; fi
require_listable "$mb"
[ -d "$mb" ] || exit 0
emit() {
  require_listable "$2"
  n=0
  for q in "$2/cur" "$2/new"; do
    if [ -e "$q" ]; then
      require_listable "$q"
      c=$(find "$q/" -type f 2>/dev/null | wc -l)
      n=$((n + c))
    fi
  done
  u=''
  ul="$2/dovecot-uidlist"
  if [ -e "$ul" ]; then
    { [ -f "$ul" ] && [ -r "$ul" ]; } || { echo "verify: cannot read $ul" >&2; exit 19; }
    u=$(head -n 1 "$ul" | awk '{print $2}')
  fi
  printf '%s\t%s\t%s\n' "$1" "$n" "$u"
}
emit INBOX "$mb"
for d in "$mb"/.[!.]*; do
  [ -d "$d" ] || continue
  emit "$(basename "$d")" "$d"
done
`

func GetFolderStats(ctx context.Context, c *sshx.Client, dom, user string, opts ...ReadOption) (map[string]FolderStats, error) {
	out, err := c.RunScript(ctx, folderStatsScript, readEnv(dom, user, opts))
	if err != nil {
		return nil, fmt.Errorf("folder stats %s/%s on %s: %w", dom, user, c.Name(), err)
	}
	fs, perr := parseFolderStatsStrict(string(out))
	if perr != nil {
		return nil, fmt.Errorf("folder stats %s/%s on %s: %w", dom, user, c.Name(), perr)
	}
	logx.Debug("folderstats %s/%s on %s: %d folder(s)", dom, user, c.Name(), len(fs))
	return fs, nil
}
