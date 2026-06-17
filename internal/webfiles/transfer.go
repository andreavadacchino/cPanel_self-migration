package webfiles

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// listTickEvery throttles the per-file callback during the streamed source
// listing (a SetSuffix + paint per file would be wasteful; the paint is already
// throttled downstream). Every 256 entries keeps the counter visibly alive.
const listTickEvery = 256

// Transfer copies a website docroot from the read-only source to the
// destination by streaming a tar archive through this process
// (SRC `tar -c` | Go pipe | DEST `tar -x`).
//
// MIGRATION semantics: the destination docroot is emptied ONCE (within the
// safety guard) before any batch is extracted, so the destination ends up an
// exact mirror of the source. The file list (regular files + empty dirs) is
// gathered on the source, split into <= MaxBytes batches, and each batch is
// streamed independently with retries.
type Transfer struct {
	Src, Dest *sshx.Client
	MaxBytes  int64
	Timeout   time.Duration // per-batch STALL timeout: abort an attempt after this long with NO progress (0 = disabled)
}

// ProgressSink receives live transfer progress for one docroot. *logx.Progress
// satisfies it; pass nil for no reporting.
type ProgressSink interface {
	SetTotal(bytes int64)
	SetBatch(i, n int)
	Add(n int64)
}

// SyncResult summarizes what a docroot copy transferred.
type SyncResult struct {
	FilesSent  int   // entries streamed (files + empty dirs)
	FilesTotal int   // entries in the source docroot
	BytesSent  int64 // sum of streamed file sizes

	// Set when an empty or directory-only source took the non-destructive backup
	// path. BackedUpDir is the "<docroot>-bak[.N]" name the existing destination
	// content was renamed aside to (empty if there was nothing to back up).
	BackedUpDir string
}

// CopyDocroot empties the destination docroot (guarded), then streams the source
// docroot into it. item must already have non-empty Src/Dest docroots. It first
// LISTS the source read-only (reporting a live "files seen" count via onList, if
// non-nil — shown on the caller's row during the read).
//
// If the source is truly empty, the destination is NOT wiped: existing destination
// content is backed up aside to "<docroot>-bak[.N]" and a fresh empty docroot is
// left. If the source contains only empty directories, the same non-destructive
// backup path prepares a fresh docroot, then those directories are streamed so
// Step 11 and manifest verification agree. The public_html root itself is rejected
// by the destination containment guard. Returns what was sent. prog/onList may be
// nil.
func (t Transfer) CopyDocroot(ctx context.Context, item WebPlanItem, prog ProgressSink, onList func(files int)) (SyncResult, error) {
	if item.SrcDocroot == "" || item.DestDocroot == "" {
		return SyncResult{}, fmt.Errorf("%s: missing docroot (src=%q dest=%q)", item.Domain, item.SrcDocroot, item.DestDocroot)
	}
	if _, err := CanonicalDestDocroot(ctx, t.Dest, item.DestDocroot); err != nil {
		return SyncResult{}, fmt.Errorf("%s: destination docroot preflight: %w", item.Domain, err)
	}

	logx.Debug("CopyDocroot %s: %s -> %s", item.Domain, item.SrcDocroot, item.DestDocroot)
	files, err := t.listSrcFiles(ctx, item.SrcDocroot, onList)
	if err != nil {
		return SyncResult{}, err
	}
	if len(files) == 0 {
		// No transfer entries at all. Preserve the destination aside and leave a
		// fresh empty docroot instead of wiping live content based on an empty source.
		logx.Debug("CopyDocroot %s: source has no transfer entries — backing up the destination instead of wiping it", item.Domain)
		return t.backupDest(ctx, item.DestDocroot)
	}
	dirOnly := true
	var bytes int64
	for _, f := range files {
		bytes += f.Size
		if !f.IsDir {
			dirOnly = false
		}
	}
	res := SyncResult{FilesSent: len(files), FilesTotal: len(files), BytesSent: bytes}
	if prog != nil {
		prog.SetTotal(bytes)
	}

	if dirOnly {
		// Directory-only sources are content: listScript already reported the empty
		// directories, and verifyWebFiles will expect them. Preserve old destination
		// content aside, then stream the directory entries into the fresh live root.
		logx.Debug("CopyDocroot %s: source has only empty directories — backing up destination before streaming directories", item.Domain)
		backup, err := t.backupDest(ctx, item.DestDocroot)
		if err != nil {
			return res, err
		}
		res.BackedUpDir = backup.BackedUpDir
	} else {
		// Empty the destination ONCE, before any extract. Doing it per-batch would
		// wipe earlier batches.
		if err := t.emptyDest(ctx, item.DestDocroot); err != nil {
			return res, fmt.Errorf("%s: empty destination docroot: %w", item.Domain, err)
		}
	}

	maxB := t.MaxBytes
	if maxB <= 0 {
		maxB = DefaultBatchMaxBytes
	}
	batches := SplitBatches(files, maxB)
	for i, batch := range batches {
		if prog != nil {
			prog.SetBatch(i+1, len(batches))
		}
		if err := t.syncBatch(ctx, item.SrcDocroot, item.DestDocroot, batch, prog); err != nil {
			return res, fmt.Errorf("%s: batch %d/%d: %w", item.Domain, i+1, len(batches), err)
		}
	}
	return res, nil
}

// listSrcFiles lists files + empty dirs under the source docroot, read-only, by
// STREAMING the listing so onList (if non-nil) reports a live count of entries
// seen as the find walks — the caller shows it as a "N files" counter on the
// docroot's row, exactly like the analysis step. The script needs the DOCROOT env
// (passed via withEnv, since the streaming exec has no Setenv channel) and is fed
// to `bash -s` on stdin.
func (t Transfer) listSrcFiles(ctx context.Context, docroot string, onList func(files int)) ([]FileEntry, error) {
	cmd := sshx.WithEnv("bash -s", map[string]string{"DOCROOT": docroot})
	var files []FileEntry
	var droppedUnsafe int
	var srcAbsent bool
	// listScript emits NUL-terminated records (names may contain spaces/newlines),
	// so consume per NUL record, not per line.
	err := sshx.StreamNul(ctx, t.Src, cmd, strings.NewReader(listScript()), func(rec string) error {
		// listScript prints "NODIR" when the source docroot cannot be entered
		// (absent or unreadable). It is NOT an empty source: collapsing it into an
		// empty file list would make CopyDocroot back up and wipe the LIVE destination
		// over a source we never read. Surface it as an error instead.
		if strings.TrimSpace(rec) == "NODIR" {
			srcAbsent = true
			return nil
		}
		f, ok, unsafe := parseFileLine(rec)
		if !ok {
			if unsafe {
				droppedUnsafe++
			}
			return nil
		}
		files = append(files, f)
		if onList != nil && (len(files) == 1 || len(files)%listTickEvery == 0) {
			onList(len(files))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list files in %s: %w", docroot, err)
	}
	if srcAbsent {
		// Fail closed: never mutate the destination based on a source we could not read.
		return nil, fmt.Errorf("source docroot %s is absent or unreadable — refusing to touch the destination", docroot)
	}
	if onList != nil {
		onList(len(files)) // final exact count
	}
	if droppedUnsafe > 0 {
		// Per-path detail is at debug; this aggregate Warn makes a non-trivial
		// exclusion visible at info level — the copy will be short by this many
		// files, which would otherwise surface only as a later verify count mismatch.
		logx.Warn("webfiles %s: dropped %d unsafe path(s) from the transfer list (see --log-level debug for each)", docroot, droppedUnsafe)
	}
	logx.Debug("webfiles list %s: %d entries (files + empty dirs)", docroot, len(files))
	return files, nil
}

// emptyDest runs the guarded empty-docroot script on the destination.
func (t Transfer) emptyDest(ctx context.Context, docroot string) error {
	logx.Debug("emptyDest %s: guarded clean (preserving %s)", docroot, strings.Join(systemExcludes, ", "))
	_, err := t.Dest.RunScript(ctx, emptyDestScript(), map[string]string{"DEST_DOCROOT": docroot})
	return err
}

// backupDest handles a source with no regular files: it preserves the destination
// rather than wiping it. A populated docroot is renamed aside to the first free
// "<docroot>-bak[.N]" and a fresh empty docroot is left in its place (the protected
// system entries are moved back into it), so the destination mirrors the empty
// source without losing the old files. The public_html root itself is rejected by
// the destination containment guard. An absent/already-empty docroot is a no-op.
// Writes ONLY on the destination.
func (t Transfer) backupDest(ctx context.Context, docroot string) (SyncResult, error) {
	out, err := t.Dest.RunScript(ctx, backupDestScript(), map[string]string{"DEST_DOCROOT": docroot})
	if err != nil {
		return SyncResult{}, fmt.Errorf("back up destination docroot %s: %w", docroot, err)
	}
	res := parseBackupResult(string(out))
	logx.Debug("backupDest %s: backedUpDir=%q", docroot, res.BackedUpDir)
	return res, nil
}

// syncBatch streams one batch of entries via the tar bridge, with retries. The
// entry list is fed to the source tar via stdin (--files-from=-), so even a long
// list never hits an argv limit.
func (t Transfer) syncBatch(ctx context.Context, srcDocroot, destDocroot string, batch []FileEntry, prog ProgressSink) error {
	names := make([]string, len(batch))
	for i, f := range batch {
		names[i] = f.RelPath
	}
	// NUL-delimited list for `tar --null --files-from=-`: a name with spaces (or a
	// leading dash, which tar would otherwise read as an option) is passed verbatim.
	fileList := strings.Join(names, "\x00") + "\x00"

	// Retry with a per-attempt stall timeout + backoff (sshx.RetryBatch). streamOnce
	// already takes a func(int64), so onBytes is passed straight through.
	var addBytes func(int64)
	if prog != nil {
		addBytes = prog.Add
	}
	label := fmt.Sprintf("webfiles batch %s (%d entries)", srcDocroot, len(batch))
	return sshx.RetryBatch(ctx, label, t.Timeout, addBytes, func(bctx context.Context, onBytes func(int64)) error {
		return t.streamOnce(bctx, srcDocroot, destDocroot, fileList, onBytes)
	})
}

// streamOnce performs a single tar-bridge transfer of the given entry list.
//
//	source : SRC_DOCROOT='…'  cd "$SRC_DOCROOT"  && tar -c --null --no-recursion --files-from=- -f -
//	dest   : DEST_DOCROOT='…' cd "$DEST_DOCROOT" && tar -x -f -
//
// The docroot paths are prepended as shell variable assignments (withEnv),
// not interpolated into the command body. These values are non-secret paths, so
// the bridge is called with a nil env map (it reserves SSH Setenv delivery for
// secrets like the DB import's MYSQL_PWD). The destination was already emptied by
// emptyDest, so a plain extract (source wins) is correct.
func (t Transfer) streamOnce(ctx context.Context, srcDocroot, destDocroot, fileList string, onBytes func(int64)) error {
	srcCmd := sshx.WithEnv(srcTarCmd, map[string]string{"SRC_DOCROOT": srcDocroot})
	destCmd := sshx.WithEnv(extractCmd, map[string]string{"DEST_DOCROOT": destDocroot})
	return sshx.BridgeProgress(ctx, t.Src, srcCmd, nil, strings.NewReader(fileList), t.Dest, destCmd, nil, onBytes)
}
