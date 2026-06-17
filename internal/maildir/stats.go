package maildir

import (
	"fmt"
	"strconv"
	"strings"
)

// BoxStats summarizes a mailbox for the fast-skip and integrity checks:
// message count across all cur/new dirs, plus the INBOX UIDVALIDITY.
type BoxStats struct {
	MsgCount    int
	UIDValidity string
}

// Consistent reports whether two stats indicate identical content: same
// message count AND same non-empty UIDVALIDITY, with a non-zero count. This is
// the fast-skip / integrity condition.
func (s BoxStats) Consistent(other BoxStats) bool {
	return s.MsgCount > 0 &&
		s.MsgCount == other.MsgCount &&
		s.UIDValidity != "" &&
		s.UIDValidity == other.UIDValidity
}

// FolderStats is one maildir FOLDER's message count + UIDVALIDITY (the INBOX root
// or a .Subfolder). The per-folder verify compares these so a shortfall in one
// folder offset by a surplus in another cannot net to zero — which the aggregate
// whole-mailbox count would hide.
type FolderStats struct {
	Count       int
	UIDValidity string
}

// parseFolderStats parses GetFolderStats output: one "<folder>\t<count>\t<uidvalidity>"
// line per maildir folder. Keyed by folder label ("INBOX" or ".Subfolder"). A
// malformed line is skipped. Pure; unit-tested.
//
// The count and UIDVALIDITY are the LAST two fields, split off from the right, so a
// folder LABEL that itself contains a tab (a maildir folder name can hold one) is
// kept whole instead of being truncated at the first tab — which previously mis-
// keyed the folder and shifted count/uid, collapsing it with another and hiding a
// per-folder shortfall.
func parseFolderStats(out string) map[string]FolderStats {
	m := map[string]FolderStats{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		uidAt := strings.LastIndexByte(line, '\t')
		if uidAt < 0 {
			continue
		}
		countAt := strings.LastIndexByte(line[:uidAt], '\t')
		if countAt < 0 {
			continue
		}
		label := line[:countAt]
		if label == "" {
			continue
		}
		count, err := strconv.Atoi(strings.TrimSpace(line[countAt+1 : uidAt]))
		if err != nil {
			continue
		}
		uid := strings.TrimSpace(line[uidAt+1:])
		if uid != "" && !validUIDValidity(uid) {
			continue
		}
		m[label] = FolderStats{Count: count, UIDValidity: uid}
	}
	return m
}

// parseFolderStatsStrict is the fail-closed parser GetFolderStats uses on LIVE
// remote output: unlike parseFolderStats it does not silently drop a malformed,
// duplicate, negative-count, or invalid-UIDVALIDITY row — it returns an error so the
// caller surfaces UNREADABLE instead of an under-counted folder map that could mask a
// real shortfall. An empty-UID row on a NON-empty folder is kept (not an error here):
// that "non-empty folder with no UIDVALIDITY" verdict is the classifier's call (so it
// can compare both sides), not silently dropped. Pure; unit-tested.
func parseFolderStatsStrict(out string) (map[string]FolderStats, error) {
	m := map[string]FolderStats{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		uidAt := strings.LastIndexByte(line, '\t')
		if uidAt < 0 {
			return nil, fmt.Errorf("malformed folder-stats line (no tab): %q", line)
		}
		countAt := strings.LastIndexByte(line[:uidAt], '\t')
		if countAt < 0 {
			return nil, fmt.Errorf("malformed folder-stats line (single field): %q", line)
		}
		label := line[:countAt]
		if label == "" {
			return nil, fmt.Errorf("malformed folder-stats line (empty label): %q", line)
		}
		count, err := strconv.Atoi(strings.TrimSpace(line[countAt+1 : uidAt]))
		if err != nil || count < 0 {
			return nil, fmt.Errorf("malformed folder-stats count for %q: %q", label, line[countAt+1:uidAt])
		}
		uid := strings.TrimSpace(line[uidAt+1:])
		if uid != "" && !validUIDValidity(uid) {
			return nil, fmt.Errorf("invalid UIDVALIDITY %q for folder %q", uid, label)
		}
		if _, dup := m[label]; dup {
			return nil, fmt.Errorf("duplicate folder label %q in stats output", label)
		}
		m[label] = FolderStats{Count: count, UIDValidity: uid}
	}
	return m, nil
}

// parseBoxStats parses the remote helper output "<count>|<uidvalidity>" into a
// BoxStats. Pure; unit-tested. A missing/garbled count yields 0.
func parseBoxStats(out string) BoxStats {
	out = strings.TrimSpace(out)
	i := strings.IndexByte(out, '|')
	if i < 0 {
		// Only a count, or empty.
		return BoxStats{MsgCount: atoiSafe(out)}
	}
	return BoxStats{
		MsgCount:    atoiSafe(strings.TrimSpace(out[:i])),
		UIDValidity: strings.TrimSpace(out[i+1:]),
	}
}

// parseBoxStatsStrict is the fail-closed parser GetBoxStats uses on LIVE remote
// output: it returns an error on garbled output (no "|" separator), a non-numeric or
// negative count, or a malformed UIDVALIDITY, rather than silently defaulting to a
// zero/partial BoxStats that would read as an empty mailbox. Empty output (a
// genuinely absent/empty mailbox the helper reported as nothing) is a clean zero. An
// empty UIDVALIDITY is allowed at parse time — the "non-empty box with no
// UIDVALIDITY" hard verdict is enforced by the consumer (BoxStats.Consistent /
// classifyVerify), not by dropping the row. Pure; unit-tested.
func parseBoxStatsStrict(out string) (BoxStats, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return BoxStats{}, nil
	}
	i := strings.IndexByte(out, '|')
	if i < 0 {
		return BoxStats{}, fmt.Errorf("malformed box-stats output (no separator): %q", out)
	}
	count, err := strconv.Atoi(strings.TrimSpace(out[:i]))
	if err != nil || count < 0 {
		return BoxStats{}, fmt.Errorf("malformed box-stats count: %q", out[:i])
	}
	uid := strings.TrimSpace(out[i+1:])
	if uid != "" && !validUIDValidity(uid) {
		return BoxStats{}, fmt.Errorf("invalid box-stats UIDVALIDITY: %q", uid)
	}
	return BoxStats{MsgCount: count, UIDValidity: uid}, nil
}

// parseUIDValidity extracts the UIDVALIDITY token from the first line of a
// dovecot-uidlist file. The header looks like:
//
//	3 V1687370761 N123 G<guid>
//
// and the second whitespace-separated field (V…) is the UIDVALIDITY. Pure.
func parseUIDValidity(firstLine string) string {
	fields := strings.Fields(firstLine)
	if len(fields) >= 2 && validUIDValidity(fields[1]) {
		return fields[1]
	}
	return ""
}

func validUIDValidity(s string) bool {
	if len(s) < 2 || s[0] != 'V' {
		return false
	}
	// UIDVALIDITY is an UNSIGNED integer (RFC 3501). ParseUint rejects a sign ('+'/'-')
	// and any negative/non-numeric value, where strconv.Atoi would have accepted "-1".
	_, err := strconv.ParseUint(s[1:], 10, 64)
	return err == nil
}

func atoiSafe(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
