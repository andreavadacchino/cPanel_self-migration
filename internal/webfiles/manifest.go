package webfiles

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
	"github.com/tis24dev/cPanel_self-migration/internal/validate"
)

// This file implements the MANIFEST verify: a per-docroot, per-relpath comparison
// of SOURCE vs DESTINATION that supersedes the aggregate file-count + total-bytes
// check. The aggregate check passes whenever the two totals happen to match, so a
// dropped file masked by an extra one, a wrong permission, a rewritten symlink, or
// a type change (file <-> symlink) all slip through. The manifest names the exact
// diverging paths instead.
//
// The pure parts (parseManifestRecord, DiffManifests) are unit-tested over plain
// strings; GetManifest is the SSH plumbing (streamed, like listSrcFiles), tested
// via the integration round-trip.

// DefaultManifestCap bounds how many entries one docroot's manifest may hold in
// memory before the verify falls back to the aggregate count+bytes check for that
// docroot. A normal site is well under this; a pathological millions-of-files
// docroot would otherwise hold two large maps at once. The fallback is reported,
// never silent.
const DefaultManifestCap = 400_000

// DeepByteCap bounds how many bytes a single docroot may hold before --deep-verify
// falls back to the metadata manifest (no per-file hashing) for it: deep mode reads
// every byte on both hosts, so an enormous docroot would hash for a very long time.
// 50 GiB is generous for a web docroot; above it the metadata check still runs and
// the skip is reported (DEEP-SKIPPED), never silent.
const DeepByteCap int64 = 50 << 30

// ManifestEntry is one docroot entry's verifiable attributes (keyed by relpath in
// a Manifest). Type is 'f' (regular file), 'l' (symlink), or 'd' (empty dir) —
// exactly the set the files-from copy transfers. Mode is the octal permission
// string (e.g. "644"); it is compared only for files (a symlink's mode is a
// meaningless 777, and non-empty dirs are not in the manifest). Link is the
// symlink target (empty otherwise). Digest is the file's sha256 hex, populated
// only in deep mode (GetManifest deep=true) — it catches same-size content
// corruption that the size comparison alone cannot.
type ManifestEntry struct {
	Type   byte
	Mode   string
	Size   int64
	Link   string
	Digest string
}

// Manifest maps a docroot-relative path to its entry.
type Manifest map[string]ManifestEntry

// manifestScript returns the read-only script that lists, relative to the docroot,
// every regular file, symlink, and EMPTY directory with its type/mode/size/target,
// applying the same system exclusions the copy uses (so excluded entries never
// show as spurious diffs). Each record is NUL-terminated and tab-separated:
//
//	<type>\t<mode>\t<size>\t<relpath>\t<link>\0   metadata (f/l/d)
//	H\t<sha256hex>\t<relpath>\0                    file content digest (deep only)
//
// The link field is last so a symlink target containing a tab survives the split.
// A tab inside an f/d relpath shifts the tail into the (otherwise-empty) link field
// and is rejected as unsafe by parseManifestRecord (it must not be keyed under the
// truncated prefix). NUL terminates records so a path with a space/newline is intact.
// It prints NODIR
// (instead of records) when the docroot path is not a directory. Never follows
// symlinks (no -L), so a symlink loop cannot hang the walk.
//
// When deep, a second pass hashes every regular file (sha256sum) and streams an H
// record per file; only the digests cross the wire, never the file bodies. The
// metadata pass always comes first, so every H record's relpath already has an
// entry to attach to.
func manifestScript(deep bool) string {
	ex := excludePruneExpr(".")
	// Distinguish a genuinely ABSENT/non-directory docroot (NODIR — a benign "nothing
	// to verify" for the verify step) from a PRESENT-but-UNREADABLE one (UNREADABLE —
	// content we cannot read, which must NOT verify clean). A bare `cd ... || NODIR`
	// conflates the two (cd fails for both no-such-dir AND permission-denied). The
	// deterministic `-r`/`-x` check on the root catches the unreadable-ROOT case before
	// the cd; the cd fallback only catches a narrow race (the root removed or a path
	// component changed between the check and the cd). An unreadable SUBTREE deeper than
	// the root is NOT detected here — in non-deep mode `find` exits non-zero and the
	// caller surfaces that as a stream error, but the deep digest pass pipes find into a
	// while-loop, so that residual partial-read case is left to the copy step (whose
	// listScript fails closed on it) rather than caught here.
	s := fmt.Sprintf(`set -u
set -o pipefail
[ -d "$DOCROOT" ] || { echo "NODIR"; exit 0; }
{ [ -r "$DOCROOT" ] && [ -x "$DOCROOT" ]; } || { echo "UNREADABLE"; exit 0; }
cd "$DOCROOT" 2>/dev/null || { echo "UNREADABLE"; exit 0; }
find . -mindepth 1 %s -o -type f -printf 'f\t%%m\t%%s\t%%P\t\0' -o -type l -printf 'l\t%%m\t0\t%%P\t%%l\0' -o -type d -empty -printf 'd\t%%m\t0\t%%P\t\0'
`, ex)
	if deep {
		// Hash each file via its own sha256sum (NUL-safe filenames via -print0 + read
		// -d ''). cut takes the hex (sha256sum prints "<hex>  <name>"); ${p#./} drops
		// the leading "./" find prints so the relpath matches the metadata records.
		s += fmt.Sprintf(`find . -mindepth 1 %s -o -type f -print0 | while IFS= read -r -d '' p; do
  h=$(sha256sum -- "$p" 2>/dev/null | cut -d' ' -f1)
  [ -n "$h" ] || h='?unreadable'
  printf 'H\t%%s\t%%s\0' "$h" "${p#./}"
done
`, ex)
	}
	return s
}

// parseManifestRecord parses ONE manifestScript record into its relpath + entry.
// ok=false for blank/NODIR/malformed records; unsafe is true only when the record
// was dropped because its path failed the traversal guard (so the caller can
// aggregate-warn those distinctly). Pure.
func parseManifestRecord(rec string) (rel string, e ManifestEntry, ok, unsafe bool) {
	if rec == "" || strings.HasPrefix(rec, "NODIR") {
		return "", ManifestEntry{}, false, false
	}
	// 5 fields: type, mode, size, relpath, link. SplitN with N=5 keeps any tab in
	// the (last) link target intact. relpath is field 4 (parts[3]); a tab inside it
	// would split into a SHORTER apparent path with the tail in parts[4] — caught
	// below for f/d, where the link field is always empty.
	parts := strings.SplitN(rec, "\t", 5)
	if len(parts) != 5 || parts[0] == "" {
		return "", ManifestEntry{}, false, false
	}
	typ := parts[0][0]
	if typ != 'f' && typ != 'l' && typ != 'd' {
		return "", ManifestEntry{}, false, false
	}
	size, err := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64)
	if err != nil {
		return "", ManifestEntry{}, false, false
	}
	rel = parts[3]
	if rel == "" {
		return "", ManifestEntry{}, false, false
	}
	// A regular file ('f') and an empty dir ('d') always carry an EMPTY trailing
	// link field (the script prints "...\t<relpath>\t\0"), so a well-formed record
	// has parts[4]=="". A NON-empty parts[4] here means SplitN truncated the relpath
	// on a literal TAB inside the filename (e.g. "dir\tfile" -> parts[3]="dir",
	// parts[4]="file\t"): validate.RelPath would then pass on the truncated prefix
	// "dir" and key the entry under the WRONG path, shadowing a real entry or
	// verifying the wrong object. Reject it as unsafe (fail closed) instead. A
	// symlink ('l') legitimately carries its target here, so the guard is f/d only
	// (a tab in a symlink NAME, distinct from its target, remains a known residual).
	if (typ == 'f' || typ == 'd') && parts[4] != "" {
		logx.Debug("webfiles: dropping unsafe path (tab in filename) from manifest: %q + %q", rel, parts[4])
		return "", ManifestEntry{}, false, true
	}
	if err := validate.RelPath(rel); err != nil {
		logx.Debug("webfiles: dropping unsafe path %q from manifest: %v", rel, err)
		return "", ManifestEntry{}, false, true
	}
	return rel, ManifestEntry{Type: typ, Mode: parts[1], Size: size, Link: parts[4]}, true, false
}

// parseDigestRecord parses ONE deep-mode "H\t<sha256hex>\t<relpath>" record into
// its relpath + hex digest. ok=false for any non-H/malformed record. Pure.
func parseDigestRecord(rec string) (rel, hex string, ok bool) {
	if !strings.HasPrefix(rec, "H\t") {
		return "", "", false
	}
	parts := strings.SplitN(rec[len("H\t"):], "\t", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[1], parts[0], true
}

// GetManifest streams the manifest of one docroot on client c (read-only). It
// returns the manifest, absent=true when the docroot path is not a directory, and
// truncated=true when more than cap entries were seen (the manifest is then partial
// and the caller must fall back to the aggregate check rather than report bogus
// "missing" diffs). dropped is the number of entries the parser REJECTED as unsafe
// (a relpath with a tab, a traversal "..", or a control byte): a non-zero count means
// the manifest is missing real-but-unrepresentable source paths, so the caller cannot
// certify the docroot complete. cap <= 0 uses DefaultManifestCap. deep=true also
// streams a per-file sha256 (the metadata pass first, then H digest records). onCount,
// if non-nil, is called with the running entry count (throttled) for a live counter.
// onProgress, when non-nil, is called as the manifest streams: hashing=false during
// the metadata-listing pass (n = entries listed so far), hashing=true during the
// deep per-file sha256 pass (n = files hashed so far). The two passes are sequential
// on the wire, so the deep verify can label the row "N entries" then "N hashed" and
// keep advancing through the slow hashing phase (which the listing count alone left
// frozen). Both are throttled by listTickEvery.
func GetManifest(ctx context.Context, c *sshx.Client, docroot string, cap int, deep bool, onProgress func(hashing bool, n int)) (m Manifest, absent, unreadable, truncated bool, dropped int, err error) {
	if cap <= 0 {
		cap = DefaultManifestCap
	}
	m = make(Manifest)
	var seen, hashed int
	cmd := sshx.WithEnv("bash -s", map[string]string{"DOCROOT": docroot})
	serr := sshx.StreamNul(ctx, c, cmd, strings.NewReader(manifestScript(deep)), func(rec string) error {
		if strings.TrimSpace(rec) == "NODIR" {
			absent = true
			return nil
		}
		// A present-but-unreadable docroot root: NOT empty/absent. The caller must
		// surface it (UNREADABLE) rather than verify it clean.
		if strings.TrimSpace(rec) == "UNREADABLE" {
			unreadable = true
			return nil
		}
		// Deep-mode digest records arrive after the metadata, so the entry exists;
		// attach the hash (an orphan digest — its metadata was capped/dropped — is
		// harmlessly ignored). These records are the SLOW (byte-reading) pass, so they
		// drive the progress row too — keyed on their own counter and "hashing" phase.
		if rel, hex, ok := parseDigestRecord(rec); ok {
			if e, exists := m[rel]; exists {
				e.Digest = hex
				m[rel] = e
			}
			hashed++
			if onProgress != nil && (hashed == 1 || hashed%listTickEvery == 0) {
				onProgress(true, hashed)
			}
			return nil
		}
		rel, e, ok, unsafe := parseManifestRecord(rec)
		if !ok {
			if unsafe {
				dropped++
			}
			return nil
		}
		seen++
		if onProgress != nil && (seen == 1 || seen%listTickEvery == 0) {
			onProgress(false, seen)
		}
		if len(m) >= cap {
			truncated = true
			return nil // keep draining the stream; the caller will fall back
		}
		m[rel] = e
		return nil
	})
	if serr != nil {
		return nil, false, false, false, 0, fmt.Errorf("manifest %s: %w", docroot, serr)
	}
	if dropped > 0 {
		logx.Warn("webfiles %s: dropped %d unsafe path(s) from the manifest (see --log-level debug for each)", docroot, dropped)
	}
	logx.Debug("webfiles manifest %s: %d entries, absent=%v unreadable=%v truncated=%v dropped=%d", docroot, len(m), absent, unreadable, truncated, dropped)
	return m, absent, unreadable, truncated, dropped, nil
}

// manifestExamples caps how many example paths a ManifestDiff keeps per kind, so a
// wildly divergent docroot does not retain an unbounded slice — the counts are
// always exact, only the printed examples are bounded.
const manifestExamples = 6

// digestUnreadable is the placeholder the deep manifest emits for a file whose body
// could not be hashed (sha256sum failed / unavailable). It is not a valid hex
// digest, so it never collides with a real one; classifyContent treats it (and a
// missing digest on one side) as unverified rather than a clean match. Must match
// the sentinel emitted by manifestScript.
const digestUnreadable = "?unreadable"

// contentClass is the deep-mode per-file content comparison outcome.
type contentClass int

const (
	contentEqual   contentClass = iota // both digests real and equal, OR both empty (non-deep)
	contentDiffer                      // both digests real, but different (corruption)
	contentUnverif                     // deep delivered on a side but the bodies could not be compared
)

// isRealDigest reports whether s is a usable content hash (present and not the
// unreadable sentinel).
func isRealDigest(s string) bool { return s != "" && s != digestUnreadable }

// classifyContent decides the deep-mode content outcome for one file present on
// both sides. Both digests empty means deep verify was not requested (non-deep
// manifests carry no digests), so there is nothing to check. When a digest is
// missing on one side or is the unreadable sentinel, the content check the user
// asked for could not run for this file — unverified, never silently equal.
func classifyContent(s, t ManifestEntry) contentClass {
	if s.Digest == "" && t.Digest == "" {
		return contentEqual // non-deep (or both unhashed): size check owns it
	}
	if isRealDigest(s.Digest) && isRealDigest(t.Digest) {
		if s.Digest == t.Digest {
			return contentEqual
		}
		return contentDiffer
	}
	return contentUnverif
}

// ManifestDiff is the pure comparison of two manifests. Counts are exact; Examples
// holds up to a few human-readable sample lines for the report. Missing/Extra/
// SizeDiff/TypeDiff/LinkDiff are real divergences (a mirror copy should reproduce
// the source exactly); ModeDiff is reported but treated as soft (a permission bit
// drift is not data loss). MissingSymlinks counts how many of Missing are symlinks
// — the headline of the silent-loss bug this check exists to surface.
type ManifestDiff struct {
	Missing           int
	MissingSymlinks   int
	Extra             int
	SizeDiff          int
	TypeDiff          int
	LinkDiff          int
	ContentDiff       int // deep mode: same size, different sha256 (silent corruption)
	ContentUnverified int // deep mode: a file's body could not be hashed on a side
	ModeDiff          int
	Examples          []string
}

// Hard reports the count of divergences that mean the destination is not a faithful
// mirror of the source (data missing/wrong) — everything except the soft ModeDiff.
// ContentUnverified is included: when the user asked for the deep content check and
// it could not run for a file, we fail closed rather than pass it as a clean match.
func (d ManifestDiff) Hard() int {
	return d.Missing + d.Extra + d.SizeDiff + d.TypeDiff + d.LinkDiff + d.ContentDiff + d.ContentUnverified
}

// OK reports whether the two manifests are identical (no hard or soft difference).
func (d ManifestDiff) OK() bool { return d.Hard() == 0 && d.ModeDiff == 0 }

// DiffManifests compares src vs dest by relpath and returns the categorized diff.
// Pure and deterministic: it walks the union of paths in sorted order so the
// retained examples are stable. Mode is compared for files only (a symlink's mode
// is meaningless; non-empty dirs are not in the manifest).
func DiffManifests(src, dest Manifest) ManifestDiff {
	var d ManifestDiff
	keys := make([]string, 0, len(src)+len(dest))
	seen := make(map[string]bool, len(src)+len(dest))
	for k := range src {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for k := range dest {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	addEx := func(format string, a ...any) {
		if len(d.Examples) < manifestExamples {
			d.Examples = append(d.Examples, fmt.Sprintf(format, a...))
		}
	}
	for _, k := range keys {
		s, inSrc := src[k]
		t, inDest := dest[k]
		switch {
		case inSrc && !inDest:
			d.Missing++
			if s.Type == 'l' {
				d.MissingSymlinks++
				addEx("missing symlink %s -> %s", k, s.Link)
			} else {
				addEx("missing %s", k)
			}
		case inDest && !inSrc:
			d.Extra++
			addEx("extra on dest %s", k)
		case s.Type != t.Type:
			d.TypeDiff++
			addEx("type changed %s (%c -> %c)", k, s.Type, t.Type)
		case s.Type == 'f' && s.Size != t.Size:
			d.SizeDiff++
			addEx("size %s (%d -> %d)", k, s.Size, t.Size)
		case s.Type == 'f' && classifyContent(s, t) == contentDiffer:
			// Same size, different content hash — corruption a size check cannot see
			// (e.g. a transfer that wrote the right number of bytes, wrongly).
			d.ContentDiff++
			addEx("content %s (sha256 differs, size %d unchanged)", k, s.Size)
		case s.Type == 'f' && classifyContent(s, t) == contentUnverif:
			// Deep verify was requested and delivered on a side, but this file's body
			// could not be hashed (sha256sum failed -> sentinel, or no digest on a
			// side). We cannot certify its content, so surface it as unverified rather
			// than let a same-size file pass as a clean "deep sha256" match.
			d.ContentUnverified++
			addEx("content unverified %s (deep sha256 unavailable on a side)", k)
		case s.Type == 'l' && s.Link != t.Link:
			d.LinkDiff++
			addEx("symlink target %s (%s -> %s)", k, s.Link, t.Link)
		case s.Type == 'f' && s.Mode != t.Mode:
			d.ModeDiff++
			addEx("mode %s (%s -> %s)", k, s.Mode, t.Mode)
		}
	}
	return d
}
