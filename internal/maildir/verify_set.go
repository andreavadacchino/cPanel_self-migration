package maildir

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// messageBaseID returns the stable identity of a Maildir message from its
// filename: the part before the ":2," info/flags separator. A message keeps
// this base ID when it moves between new/ and cur/ or when its flags change
// (e.g. "1700000000.M1.host:2,S" and "1700000000.M1.host:2,FS" are the same
// message). Pure.
func messageBaseID(name string) string {
	if i := strings.Index(name, ":2,"); i >= 0 {
		return name[:i]
	}
	// Some filenames use ":1," (experimental) — strip any ":N," suffix.
	if i := strings.Index(name, ":"); i >= 0 {
		// Only treat it as a flag separator if it looks like ":<digit>,".
		if i+2 < len(name) && name[i+1] >= '0' && name[i+1] <= '9' && name[i+2] == ',' {
			return name[:i]
		}
	}
	return name
}

// parseMessageNames turns the collector output (NUL-separated mailbox-relative
// message paths, e.g. "cur/A:2,S\0.Sent/cur/B:2,S\0") into the SET of folder-aware
// message identities (see messageIdentity). NUL framing is required because a maildir
// folder name can legitimately contain spaces, tabs, or newlines; records are NOT
// trimmed (a leading/trailing space is a valid filename byte). Pure; unit-tested.
func parseMessageNames(out string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, rel := range strings.Split(out, "\x00") {
		if rel == "" {
			continue
		}
		set[messageIdentity(rel)] = struct{}{}
	}
	return set
}

// SameMessageSet reports whether two message-ID sets are exactly equal.
// Pure; the heart of the --verify-checksums precision check.
func SameMessageSet(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

// DiffMessageSets returns up to max example identities present in one set but not the
// other, rendered folder-qualified (e.g. "INBOX/A", ".Sent/A") for human output. It
// is the diagnostic companion to SameMessageSet: when --verify-checksums finds a
// same-count-but-different-content mailbox, the apply step logs a few example
// diverging IDs so an operator can see WHICH messages (and in which folder) differ.
// Pure.
func DiffMessageSets(src, dest map[string]struct{}, max int) (onlySrc, onlyDest []string) {
	for k := range src {
		if _, ok := dest[k]; !ok {
			onlySrc = append(onlySrc, displayIdentity(k))
		}
	}
	for k := range dest {
		if _, ok := src[k]; !ok {
			onlyDest = append(onlyDest, displayIdentity(k))
		}
	}
	sort.Strings(onlySrc)
	sort.Strings(onlyDest)
	onlySrc = capStrings(onlySrc, max)
	onlyDest = capStrings(onlyDest, max)
	return onlySrc, onlyDest
}

// messageSetScript is the read-only GetMessageSet helper. dom/user passed via env
// (DOM/USER), never interpolated — see boxStatsScript. An unreadable/non-directory
// mailbox root errors (require_listable); the enumeration itself is best effort (a
// partially-unreadable tree under-reports), which is safe because the caller
// (--verify-checksums fast-skip) then re-copies rather than trusting the set. An
// absent mailbox is a clean empty set.
//
// It cd's into the mailbox and emits each message as a NUL-terminated MAILBOX-RELATIVE
// path (%P), so the folder survives to Go (the identity is folder-aware) and a folder
// name with spaces/tabs is unambiguous. The cd would follow a symlinked root, so a
// DESTINATION read (GuardRoot) first rejects a symlink root via guard_mailbox_path —
// matching the guarded extract — so a verify cannot read THROUGH a link the copy
// refused to write to.
var messageSetScript = mailboxGuardScript() + statGuardHelper + `set -u
mb="$HOME/mail/$DOM/$USER"
if [ -n "${GUARD_ROOT:-}" ]; then mb="$(guard_mailbox_path "$mb")" || exit $?; fi
require_listable "$mb"
[ -d "$mb" ] || exit 0
cd "$mb" || exit 0
find . -type f \( -path '*/cur/*' -o -path '*/new/*' \) -printf '%P\0' 2>/dev/null
`

// GetMessageSet lists every message in a mailbox (files under any cur/ or new/ dir)
// as a SET of folder-aware identities, read-only. Used by the --verify-checksums
// precision check to compare the exact message identities SRC vs DEST instead of just
// the count, so a message in the WRONG folder (same aggregate count) is caught.
func GetMessageSet(ctx context.Context, c *sshx.Client, dom, user string, opts ...ReadOption) (map[string]struct{}, error) {
	out, err := c.RunScript(ctx, messageSetScript, readEnv(dom, user, opts))
	if err != nil {
		return nil, fmt.Errorf("message set %s/%s on %s: %w", dom, user, c.Name(), err)
	}
	mset := parseMessageNames(string(out))
	logx.Debug("messageset %s/%s on %s: %d message(s)", dom, user, c.Name(), len(mset))
	return mset, nil
}

// parseMessageDigests turns NUL-terminated "<sha256hex>\t<mailbox-relative-path>"
// records into a map from each message's FOLDER-AWARE identity (see messageIdentity)
// to its content digest. A Maildir body is immutable once written — only its flags
// (the :2,… suffix) change — so the same identity on both sides must carry the same
// bytes; a digest mismatch is genuine corruption. Folder-aware keying means the same
// base ID in two folders does NOT collide (a cross-folder swap is a real difference).
//
// Fails closed on ambiguous/garbled input rather than trusting it: a record whose hash
// is neither the ?unreadable sentinel nor a well-formed sha256 hex is treated as
// unreadable, and two files that map to the SAME identity but disagree on the hash (an
// ambiguous live-Maildir state, e.g. a cur and a new copy of one message with
// different bytes) collapse to ?unreadable so the message is surfaced as UNVERIFIED,
// never silently overwritten. Pure.
// digestTickEvery throttles the per-message progress callback during a streamed
// digest read, so a huge mailbox does not repaint the bar on every single message.
const digestTickEvery = 256

// addDigestRecord folds ONE "<hash>\t<relpath>" digest record into m, keyed by
// folder-aware identity (two files of one identity with disagreeing bytes collapse
// to digestUnreadable). It returns true when the record was a well-formed digest
// line — so a streaming caller can count it toward progress — and false for an
// empty/garbled record that was skipped. Pure; the single parse shared by the
// streaming reader (GetMessageDigests) and parseMessageDigests.
func addDigestRecord(m map[string]string, rec string) bool {
	if rec == "" {
		return false
	}
	i := strings.IndexByte(rec, '\t')
	if i < 0 {
		return false // a record with no tab is truncated/garbled — drop it
	}
	hash := rec[:i]
	rel := rec[i+1:]
	if rel == "" {
		return false
	}
	if hash != digestUnreadable && !isSHA256(hash) {
		hash = digestUnreadable // garbled/truncated digest text -> not a real hash
	}
	id := messageIdentity(rel)
	if prev, ok := m[id]; ok && prev != hash {
		m[id] = digestUnreadable // two files, one identity, disagreeing bytes -> ambiguous
		return true
	}
	m[id] = hash
	return true
}

func parseMessageDigests(out string) map[string]string {
	m := map[string]string{}
	for _, rec := range strings.Split(out, "\x00") {
		addDigestRecord(m, rec)
	}
	return m
}

// isSHA256 reports whether s is a well-formed sha256 digest (64 lowercase hex chars),
// the only real-hash shape sha256sum emits. Pure.
func isSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// digestUnreadable is the placeholder GetMessageDigests emits for a message whose
// body could not be hashed (sha256sum failed). It is not a valid hex digest, so it
// never collides with a real one; DiffMessageDigests treats it as a divergence on
// either side so the message is surfaced as unverified rather than silently dropped
// (which would blind the deep content check to a corrupt/unreadable message).
const digestUnreadable = "?unreadable"

// DiffMessageDigests compares two base-ID -> content-digest maps and splits the
// source-present divergences into three kinds, so "the bytes are wrong" is never
// conflated with "the bytes could not be read":
//
//   - missing: present in src, absent on dest (lost mail) — real loss.
//   - changed: present on BOTH with two REAL but different body digests (silent
//     corruption — same name, wrong content) — real loss.
//   - unverified: a side's body could not be hashed (digestUnreadable on src or
//     dest), so the message is surfaced but NOT certified either way — the deep
//     check could not run for it, which is UNVERIFIED, not corruption.
//
// Up to max examples of each, sorted. Extra messages only on the destination are
// ignored (benign, like DEST AHEAD). Pure.
func DiffMessageDigests(src, dest map[string]string, max int) (missing, changed, unverified []string) {
	for id, sh := range src {
		dh, ok := dest[id]
		switch {
		case sh == digestUnreadable:
			unverified = append(unverified, displayIdentity(id)) // source body unreadable
		case !ok:
			missing = append(missing, displayIdentity(id)) // present on src, absent on dest -> lost
		case dh == digestUnreadable:
			unverified = append(unverified, displayIdentity(id)) // dest body unreadable
		case sh != dh:
			changed = append(changed, displayIdentity(id)) // both real, different -> corruption
		}
	}
	sort.Strings(missing)
	sort.Strings(changed)
	sort.Strings(unverified)
	missing = capStrings(missing, max)
	changed = capStrings(changed, max)
	unverified = capStrings(unverified, max)
	return missing, changed, unverified
}

// CountDigestDivergence rolls two folder-aware identity -> body-digest maps (from
// GetMessageDigests) up to the DEFAULT-tier body verdict, PER MESSAGE — the same
// classification DiffMessageDigests makes, but as counts and without building example
// lists. hard counts source-present messages that are genuinely wrong on the destination:
// a real body change (both sides readable, different sha256) or a lost message (absent on
// dest). unverified counts messages a side could not hash (the ?unreadable sentinel,
// including the ambiguous same-identity/disagreeing-bytes case parseMessageDigests
// collapses to it). Keying by messageIdentity (done upstream) makes it robust: a flag
// change or a new/->cur/ move keeps the identity AND the body, so it does NOT false-diff.
//
// Crucially this is PER MESSAGE, not a whole-mailbox aggregate: an unreadable body lands
// in unverified WITHOUT masking a DIFFERENT readable message's corruption (which still
// lands in hard) — and an ?unreadable on either side routes to unverified rather than
// being compared, so two different-but-both-unreadable bodies never collide into a false
// match. Extra messages only on the destination are ignored (benign, like DEST AHEAD).
// Pure.
func CountDigestDivergence(src, dest map[string]string) (hard, unverified int) {
	for id, sh := range src {
		dh, ok := dest[id]
		switch {
		case sh == digestUnreadable:
			unverified++ // source body unreadable -> cannot certify (never compared)
		case !ok:
			hard++ // present on src, absent on dest -> lost
		case dh == digestUnreadable:
			unverified++ // dest body unreadable -> cannot certify
		case sh != dh:
			hard++ // both real, different -> silent corruption
		}
	}
	return hard, unverified
}

func capStrings(s []string, max int) []string {
	if len(s) > max {
		return s[:max]
	}
	return s
}

// messageDigestsScript is the read-only GetMessageDigests helper. Fails closed where
// it is deterministic: if sha256sum is missing on the host the WHOLE helper errors
// (the deep check becomes UNVERIFIED) rather than tagging every message ?unreadable
// and having that read as corruption; an unreadable/non-directory mailbox root errors
// via require_listable. The deep check only runs after GetFolderStats has already
// guarded every subfolder, so the enumeration tolerates a transient find error
// (2>/dev/null) instead of turning a benign live-mailbox race into a hard fail. A
// single message whose body cannot be hashed gets the ?unreadable sentinel so it is
// surfaced per-message as unverified.
var messageDigestsScript = mailboxGuardScript() + statGuardHelper + `set -u
command -v sha256sum >/dev/null 2>&1 || { echo "verify: sha256sum not available" >&2; exit 16; }
mb="$HOME/mail/$DOM/$USER"
if [ -n "${GUARD_ROOT:-}" ]; then mb="$(guard_mailbox_path "$mb")" || exit $?; fi
require_listable "$mb"
[ -d "$mb" ] || exit 0
cd "$mb" || exit 0
find . -type f \( -path '*/cur/*' -o -path '*/new/*' \) -print0 2>/dev/null | while IFS= read -r -d '' p; do
  h=$(sha256sum -- "$p" 2>/dev/null | cut -d' ' -f1)
  # GNU sha256sum/cut prefix the line with a literal backslash when the filename
  # contains a backslash or newline; strip that single escape marker so the digest is
  # the bare 64-hex (otherwise the strict isSHA256 check would reject a real hash).
  h=${h#\\}
  [ -n "$h" ] || h='?unreadable'
  rel=${p#./}
  printf '%s\t%s\0' "$h" "$rel"
done
`

// GetMessageDigests returns the sha256 of every message BODY in a mailbox, keyed by
// FOLDER-AWARE identity, read-only (the --deep-verify mail check). It forks sha256sum
// per message (portable; bounded because it is opt-in); only the digests cross the
// wire, never the message bodies. dom/user passed via env, never interpolated. A body
// that cannot be hashed is emitted with the digestUnreadable sentinel (surfaced as
// UNVERIFIED, not silently dropped).
func GetMessageDigests(ctx context.Context, c *sshx.Client, dom, user string, opts ...ReadOption) (map[string]string, error) {
	cfg := applyReadOpts(opts)
	md := map[string]string{}
	hashed := 0
	// Stream the digests (the SAME helper script) record-by-record instead of
	// collecting the whole map in one shot, so a per-message progress callback can
	// animate while the remote sha256-hashes every body. The env is inlined into the
	// command (no SSH Setenv), exactly like the web manifest stream.
	cmd := sshx.WithEnv("bash -s", readEnv(dom, user, opts))
	err := sshx.StreamNul(ctx, c, cmd, strings.NewReader(messageDigestsScript), func(rec string) error {
		if addDigestRecord(md, rec) {
			hashed++
			if cfg.onCount != nil && (hashed == 1 || hashed%digestTickEvery == 0) {
				cfg.onCount(hashed)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("message digests %s/%s on %s: %w", dom, user, c.Name(), err)
	}
	logx.Debug("messagedigests %s/%s on %s: %d message(s)", dom, user, c.Name(), len(md))
	return md, nil
}
