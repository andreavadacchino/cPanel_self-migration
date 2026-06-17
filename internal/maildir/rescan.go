package maildir

import (
	"sort"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// isSourceVanishedFileErr reports whether err is the SOURCE `tar -c` failing
// because a file it was told to archive no longer exists — the signature of a
// LIVE mailbox that mutated (Dovecot renamed a message on a flag change, moved
// new/→cur/, or expunged it) between the up-front scan and the batch that
// references it. tar prints "Cannot stat: No such file or directory" for such a
// vanished member. The caller responds by re-scanning the mailbox instead of
// blindly retrying the now-stale file list.
//
// The match is gated on the SOURCE side of the bridge: the destination `tar -x`
// can ALSO print "No such file or directory" (a missing dest path, or a truncated
// archive), so matching the text alone would misclassify a real dest failure as a
// source mutation and trigger a pointless re-scan. sshx.SideError recovers the
// source-tagged error even when the dest also failed (a source abort truncates the
// archive, so the dest tar errors too) — so a genuine source-vanished file still
// re-scans, while a dest-only error does not. "Cannot open: Disk quota exceeded" (a
// dest write failure) does not match for both reasons: it is dest-side AND not ENOENT.
func isSourceVanishedFileErr(err error) bool {
	se := sshx.SideError(err, sshx.SideSource)
	if se == nil {
		return false
	}
	s := se.Error()
	return strings.Contains(s, "No such file or directory") || strings.Contains(s, "Cannot stat")
}

// diffScans compares two source scans BY RELATIVE PATH (the exact name tar reads
// from --files-from) and returns the paths that vanished (in before, not in
// after) and that appeared (in after, not in before). It is used only to report
// what the live mailbox changed under us mid-copy. Pure.
func diffScans(before, after []FileEntry) (vanished, appeared []string) {
	beforeSet := make(map[string]struct{}, len(before))
	for _, f := range before {
		beforeSet[f.RelPath] = struct{}{}
	}
	afterSet := make(map[string]struct{}, len(after))
	for _, f := range after {
		afterSet[f.RelPath] = struct{}{}
	}
	for p := range beforeSet {
		if _, ok := afterSet[p]; !ok {
			vanished = append(vanished, p)
		}
	}
	for p := range afterSet {
		if _, ok := beforeSet[p]; !ok {
			appeared = append(appeared, p)
		}
	}
	sort.Strings(vanished)
	sort.Strings(appeared)
	return vanished, appeared
}

// firstN returns up to n elements of s, for compact example logging. Pure.
func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
