package cpanel

import (
	"context"
	"fmt"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// AccountState is the outcome of ensuring a mailbox exists with a given hash.
type AccountState string

const (
	AccountCreated AccountState = "created"
	AccountUpdated AccountState = "updated"
)

// EnsureResult reports what EnsureAccount did. BackedUpDir is non-empty when an
// orphan Maildir was renamed out of the way before creating the account (only
// on a fresh create); it holds the new directory name (e.g. "homelab-bak.2").
type EnsureResult struct {
	State       AccountState
	BackedUpDir string
}

// EnsureAccount makes the mailbox user@dom exist on the destination with the
// given crypt hash, idempotently:
//
//   - if ~/etc/<dom>/shadow already has the user, rewrite only its hash field
//     (Email::add_pop refuses duplicates, and passwd_pop won't accept a hash);
//   - otherwise create it via Email::add_pop password_hash=… (quota 0).
//
// Before a fresh create, it also handles an ORPHAN Maildir: if the account is
// not in shadow but ~/mail/<dom>/<user> already exists on the destination, that
// stray directory is renamed to the first free "<user>-bak[.N]" so add_pop
// starts from a clean state and Dovecot does not inherit a half-built mailbox.
//
// The decision (exists? what to do?) is made here, but the actual file edit and
// the add_pop call run on the destination host via a single remote snippet.
// Parameters travel as environment variables (DOM/USER/HASH), never inlined.
func EnsureAccount(ctx context.Context, c Runner, dom, user, hash string) (EnsureResult, error) {
	out, err := c.RunScript(ctx, ensureAccountScript, map[string]string{
		"DOM":  dom,
		"USER": user,
		"HASH": hash,
	})
	if err != nil {
		return EnsureResult{}, fmt.Errorf("ensure account %s@%s: %w", user, dom, err)
	}
	return parseEnsureResult(string(out), user, dom)
}

// parseEnsureResult interprets the remote snippet's stdout. The script prints an
// optional "BAKDIR <name>" line (when an orphan dir was renamed) followed by the
// status line (CREATED / UPDATED / ACCTFAIL ...). Pure; unit-tested.
func parseEnsureResult(out, user, dom string) (EnsureResult, error) {
	var res EnsureResult
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "BAKDIR "):
			res.BackedUpDir = strings.TrimSpace(strings.TrimPrefix(line, "BAKDIR "))
			logx.Debug("parseEnsureResult %s@%s: orphan maildir backed up to %s", user, dom, res.BackedUpDir)
		case strings.HasPrefix(line, "CREATED"):
			res.State = AccountCreated
			logx.Debug("parseEnsureResult %s@%s: account created (state=CREATED)", user, dom)
			return res, nil
		case strings.HasPrefix(line, "UPDATED"):
			res.State = AccountUpdated
			logx.Debug("parseEnsureResult %s@%s: account updated (state=UPDATED)", user, dom)
			return res, nil
		case strings.HasPrefix(line, "ACCTFAIL"):
			return EnsureResult{}, fmt.Errorf("ensure account %s@%s: %s", user, dom,
				strings.TrimSpace(strings.TrimPrefix(line, "ACCTFAIL")))
		}
	}
	return EnsureResult{}, fmt.Errorf("ensure account %s@%s: unexpected response %q", user, dom, strings.TrimSpace(out))
}

// ensureAccountScript runs on the destination. It reads DOM/USER/HASH from the
// environment (set via the SSH session) so the hash is never on a command line.
//
// A SINGLE awk decides and does the work, matching the mailbox by EXACT string
// equality on the shadow's first field ($1==u) — never via a grep regex. A local
// part containing '.', '+', '[' … must not false-match a SIBLING account (which
// would silently skip creating the real one and report "UPDATED") nor break the
// pattern. The awk exit code selects the branch: 0 = user found and hash rewritten;
// 9 = user absent (create it below); anything else = an awk/redirect failure.
//
// The rewrite is ATOMIC: awk transforms the LIVE shadow into a temp file which is
// then renamed over the original (same directory => an atomic rename), so neither a
// failed/killed awk NOR a failed rename can corrupt the destination's mail auth or
// report a false success — the original stays in place and the failure is reported
// as ACCTFAIL (no false "UPDATED"). UPDATED is printed only after the mv succeeds:
// a renamed-but-not-written hash would silently lock the user out. The temp
// inherits the original's permissions (the rename replaces the inode): umask 077
// keeps it owner-only even if chmod --reference is unavailable, so the password
// hashes never become world/group-readable. On a fresh create it first moves any
// orphan Maildir aside to the first free "<user>-bak[.N]".
const ensureAccountScript = `set -u
SH="$HOME/etc/$DOM/shadow"
if [ -f "$SH" ]; then
    umask 077
    tmp="$SH.migtmp.$$"
    awk -F: -v OFS=: -v u="$USER" -v h="$HASH" '$1==u{$2=h; f=1} {print} END{exit f?0:9}' "$SH" > "$tmp"
    rc=$?
    if [ "$rc" = 0 ]; then
        chmod --reference="$SH" "$tmp" 2>/dev/null || true
        if mv -f "$tmp" "$SH"; then
            echo "UPDATED"
            exit 0
        fi
        # The atomic replace failed (read-only mount, immutable flag, ENOSPC, ...).
        # The LIVE shadow is unchanged and the user's hash was NOT written, so report
        # a failure instead of a false UPDATED and drop the orphan temp file. (The
        # orphan-maildir mv below is checked the same way.)
        rm -f "$tmp"
        echo "ACCTFAIL could not replace shadow for $USER@$DOM"
        exit 0
    fi
    rm -f "$tmp"
    if [ "$rc" != 9 ]; then
        echo "ACCTFAIL could not rewrite shadow for $USER@$DOM"
        exit 0
    fi
    # rc == 9: $USER is absent from the shadow file -> create it below.
fi
# Account is NOT configured (no shadow file, or $USER absent from it). If a stray
# Maildir is already there, move it aside (first free <user>-bak[.N]) so the new
# account starts clean and Dovecot does not inherit a half-built mailbox.
MD="$HOME/mail/$DOM/$USER"
# Fail closed if ANY component of the mailbox path is a SYMLINK. DOM/USER are already
# validated against dot, dotdot, and path separators, so the only path components are
# ~/mail, ~/mail/<dom>, and ~/mail/<dom>/<user>; if none of those is a symlink, the
# path cannot redirect (via a link) outside the mailbox tree, so the orphan rename
# below stays inside ~/mail. This matches the canonical containment the transfer and
# mirror steps apply (a bare leaf-only check would miss a symlinked <dom> dir, letting
# the rename operate on a directory outside ~/mail). $HOME itself is trusted (resolved
# on the remote), so a /home2-style symlinked $HOME is not flagged.
for _p in "$HOME/mail" "$HOME/mail/$DOM" "$MD"; do
    if [ -L "$_p" ]; then
        echo "ACCTFAIL mailbox path component is a symlink (refusing to touch): $_p"
        exit 0
    fi
done
if [ -e "$MD" ]; then
    bak="${MD}-bak"
    if [ -e "$bak" ]; then
        n=2
        while [ -e "${MD}-bak.${n}" ]; do n=$((n + 1)); done
        bak="${MD}-bak.${n}"
    fi
    if mv "$MD" "$bak"; then
        echo "BAKDIR $(basename "$bak")"
    else
        echo "ACCTFAIL could not rename orphan maildir $MD"
        exit 0
    fi
fi
r="$(uapi --output=json Email add_pop email="$USER" domain="$DOM" \
        password_hash="$HASH" quota=0 2>&1)"
# UAPI success is result.status==1. Match it whitespace-tolerantly and anchored on
# the quoted key: --output=json is compact today, but a pretty-printed or
# space-padded payload (status : 1) is still valid JSON and must not be read as a
# failure. The leading quote pins the key (no xstatus false-match) and the trailing
# non-digit/EOL pins the value to exactly 1 (no status:10 widening, and a status:0
# failure stays a failure).
if printf '%s' "$r" | grep -Eq '"status"[[:space:]]*:[[:space:]]*1([^0-9]|$)'; then
    echo "CREATED"
else
    echo "ACCTFAIL $(printf '%s' "$r" | head -c 200)"
fi
`
