package maildir

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
	"github.com/tis24dev/cPanel_self-migration/internal/validate"
)

// excludeGlobs are the rebuildable Dovecot index files we never copy. The
// control files dovecot-uidlist / dovecot-keywords ARE copied (they preserve
// UIDs/UIDVALIDITY), so they are not excluded here.
var excludeGlobs = []string{"dovecot.index*", "dovecot.list.index*"}

// controlFileGlobs are the Dovecot bookkeeping files that carry UID/UIDVALIDITY
// state. They MUST mirror the source (unlike immutable message files, whose
// names encode their identity). When cPanel/Dovecot recreates a mailbox it
// provisions a FRESH dovecot-uidlist with a NEW UIDVALIDITY and a newer mtime;
// `tar -x --keep-newer-files` would then refuse to overwrite it, leaving the
// destination on the wrong UIDVALIDITY. So the final control-file step extracts
// with `tar -x --overwrite` instead, replacing the destination's copies
// unconditionally (regardless of mtime) while leaving control files the source
// does NOT carry in place. Match the actual Dovecot filenames per folder; index
// files are already excluded from the stream so they are not listed here.
var controlFileGlobs = []string{"dovecot-uidlist", "dovecot-keywords", "dovecot.mailbox.log"}

// Transfer copies a Maildir from the read-only source to the destination by
// streaming a tar archive through this process (SRC `tar -c` | Go pipe | DEST
// `tar -x`). The file list is gathered once on the source (with sizes), split
// into <= maxBytes batches, and each batch streamed independently with retries.
//
// By default only the files MISSING on the destination are transferred (a real
// delta sync). With Full set, every file is re-streamed regardless.
type Transfer struct {
	Src, Dest *sshx.Client
	MaxBytes  int64
	Timeout   time.Duration // per-batch STALL timeout: abort an attempt after this long with NO progress (0 = disabled)
	Full      bool          // re-transfer every file, ignoring what DEST already has

	// afterScan, if set, is invoked once right after the initial source scan in
	// SyncBoxProgress. TEST-ONLY seam to deterministically reproduce a live source
	// mailbox changing mid-copy (a message vanishing/appearing between the scan and
	// a batch); nil in production.
	afterScan func()
}

// ProgressSink receives live transfer progress for one mailbox. *logx.Progress
// satisfies it; pass nil for no reporting.
type ProgressSink interface {
	SetTotal(bytes int64) // total bytes to transfer (the delta), known up front
	SetBatch(i, n int)    // current batch index (1-based) and total
	Add(n int64)          // n more bytes were relayed
}

// RescanReporter is an OPTIONAL capability of a ProgressSink. When the live source
// changes mid-copy and SyncBoxProgress restarts with a freshly-scanned plan, it
// calls Rescan (if the sink implements it) so the UI can present the interruption:
// finalize the line for the batch that failed, note what changed, and open a new
// line for the continued copy. A sink that does not implement it falls back to a
// one-line logx.Warn.
type RescanReporter interface {
	Rescan(failedBatch, totalBatches, vanished, appeared int)
}

// SyncResult summarizes what a mailbox sync transferred. FilesTotal splits into
// MsgTotal (actual mail messages) + ControlTotal (Dovecot/maildir bookkeeping
// files), so a report can show "M messages + C control = F files" and reconcile
// with the verify step, which counts only messages.
type SyncResult struct {
	FilesSent    int   // number of files actually streamed (the delta, or all if Full)
	FilesTotal   int   // total files in the source mailbox (MsgTotal + ControlTotal)
	MsgTotal     int   // of FilesTotal, the actual mail messages (cur/ + new/)
	ControlTotal int   // of FilesTotal, the Dovecot/maildir control files
	BytesSent    int64 // bytes scheduled for transfer (sum of sent files' sizes)
}

// SyncBox copies mail/<dom>/<user>/ from source to destination with no progress
// reporting.
func (t Transfer) SyncBox(ctx context.Context, dom, user string) (SyncResult, error) {
	return t.SyncBoxProgressDomains(ctx, dom, dom, user, nil)
}

// SyncBoxDomains copies mail/<srcDom>/<user>/ from source to
// mail/<destDom>/<user>/ on destination with no progress reporting.
func (t Transfer) SyncBoxDomains(ctx context.Context, srcDom, destDom, user string) (SyncResult, error) {
	return t.SyncBoxProgressDomains(ctx, srcDom, destDom, user, nil)
}

// SyncBoxProgress copies a mailbox, transferring only the files missing on the
// destination (unless Full). It reports progress via the sink and returns what
// was sent. The destination tar uses --keep-newer-files, so even with Full a
// re-send never clobbers an already-present message.
func (t Transfer) SyncBoxProgress(ctx context.Context, dom, user string, prog ProgressSink) (SyncResult, error) {
	return t.SyncBoxProgressDomains(ctx, dom, dom, user, prog)
}

// SyncBoxProgressDomains copies mail/<srcDom>/<user>/ from source to
// mail/<destDom>/<user>/ on destination. The two domain spellings are usually
// identical, but cPanel inventories can preserve different case/root-dot spelling
// across hosts; source reads and destination writes must each use that host's raw
// spelling.
func (t Transfer) SyncBoxProgressDomains(ctx context.Context, srcDom, destDom, user string, prog ProgressSink) (SyncResult, error) {
	srcRel := "mail/" + srcDom + "/" + user
	destRel := "mail/" + destDom + "/" + user
	logx.Debug("SyncBox %s/%s -> %s/%s: listing source files ...", srcDom, user, destDom, user)

	srcFiles, err := t.listFiles(ctx, srcRel)
	if err != nil {
		return SyncResult{}, err
	}
	if len(srcFiles) == 0 {
		// An empty (or absent) source mailbox is a no-op, not a failure — there is
		// nothing to copy. Returning an error here would mark a legitimately-empty
		// ACTIVE account FAILED whenever it reaches the copy: e.g. under --full (which
		// bypasses the fast-skip) or when the stats read that drives the skip hiccups.
		logx.Debug("SyncBox %s/%s: source mailbox empty — nothing to copy", srcDom, user)
		return SyncResult{}, nil
	}
	if t.afterScan != nil {
		t.afterScan() // test-only seam: mutate the live source after the scan
	}

	// Compute the delta: only files whose message is not already on the dest.
	toSend := srcFiles
	if !t.Full {
		logx.Debug("SyncBox %s/%s -> %s/%s: listing dest files for delta ...", srcDom, user, destDom, user)
		destFiles, _, err := listFilesOn(ctx, t.Dest, destRel, true) // absent dest is normal before the first sync; guard the dest root
		if err != nil {
			return SyncResult{}, err
		}
		toSend = deltaFiles(srcFiles, destFiles)
		logx.Debug("SyncBox %s/%s -> %s/%s: src=%d dest=%d -> delta=%d files", srcDom, user, destDom, user, len(srcFiles), len(destFiles), len(toSend))
	}

	var bytes int64
	for _, f := range toSend {
		bytes += f.Size
	}
	msgTotal := 0
	for _, f := range srcFiles {
		if isMaildirMessage(f.RelPath) {
			msgTotal++
		}
	}
	res := SyncResult{
		FilesSent:    len(toSend),
		FilesTotal:   len(srcFiles),
		MsgTotal:     msgTotal,
		ControlTotal: len(srcFiles) - msgTotal,
		BytesSent:    bytes,
	}

	if prog != nil {
		prog.SetTotal(bytes)
	}
	if len(toSend) == 0 {
		return res, nil // nothing missing — already in sync
	}

	maxB := t.MaxBytes
	if maxB <= 0 {
		maxB = DefaultBatchMaxBytes
	}
	steps := planSyncSteps(toSend, maxB)
	logx.Debug("SyncBox %s/%s -> %s/%s: %d file(s) -> %d step(s) (max %.0f MB; control files in the final overwrite-step)",
		srcDom, user, destDom, user, len(toSend), len(steps), float64(maxB)/(1024*1024))

	// Bound on how many times a single mailbox may re-scan after the live source
	// changed under us. Each re-scan only ever shrinks the remaining work (already-
	// copied blocks are excluded), so this converges in practice; the cap just stops
	// a mailbox that is being written FASTER than we can copy it from looping forever
	// (it then FAILs and the verify step reports it INCOMPLETE for a re-run).
	const maxRescans = 3
	prevSrc := srcFiles
	rescans := 0
	for i := 0; i < len(steps); i++ {
		st := steps[i]
		if prog != nil {
			prog.SetBatch(i+1, len(steps))
		}
		err := t.syncBatch(ctx, srcRel, destRel, st.files, st.controlStep, prog)
		if err == nil {
			continue
		}
		// A live source mailbox can rename (flag change), move (new/→cur/) or expunge
		// a message between the up-front scan and this batch, so a file we were about
		// to archive no longer exists and the source tar aborts ("Cannot stat: No such
		// file or directory"). Re-running the same stale list cannot help. Instead
		// re-scan the source, recompute what is STILL MISSING on the destination, and
		// carry on with only those blocks: this skips the already-copied blocks, drops
		// the vanished files, and picks up any that appeared (e.g. a renamed message
		// under its new name) — so the mirror still completes in one pass.
		if !isSourceVanishedFileErr(err) {
			return res, fmt.Errorf("%s/%s -> %s/%s: batch %d/%d: %w", srcDom, user, destDom, user, i+1, len(steps), err)
		}
		if rescans >= maxRescans {
			// Convergence failure (distinct from a generic batch error): the mailbox is
			// being written FASTER than it can be copied, so re-running --apply hits the
			// same wall. Name it so the operator knows to quiesce the account first.
			logx.Warn("mailbox %s/%s: source still changing after %d re-scans — the account is written faster than it copies; quiesce it and re-run --apply", srcDom, user, maxRescans)
			return res, fmt.Errorf("%s/%s -> %s/%s: batch %d/%d: source still changing after %d re-scans (written faster than it copies): %w", srcDom, user, destDom, user, i+1, len(steps), maxRescans, err)
		}
		rescans++
		freshSrc, lerr := t.listFiles(ctx, srcRel)
		if lerr != nil {
			return res, fmt.Errorf("%s/%s -> %s/%s: batch %d/%d: %w (source re-scan failed: %v)", srcDom, user, destDom, user, i+1, len(steps), err, lerr)
		}
		freshDest, _, derr := listFilesOn(ctx, t.Dest, destRel, true)
		if derr != nil {
			return res, fmt.Errorf("%s/%s -> %s/%s: batch %d/%d: %w (dest re-scan failed: %v)", srcDom, user, destDom, user, i+1, len(steps), err, derr)
		}
		vanished, appeared := diffScans(prevSrc, freshSrc)
		// Surface the interruption: a sink that knows how (the migrate UI) finalizes
		// the failed batch's line and opens a fresh one; otherwise a one-line warn.
		if rr, ok := prog.(RescanReporter); ok {
			rr.Rescan(i+1, len(steps), len(vanished), len(appeared))
		} else {
			logx.Warn("mailbox %s: source changed during copy (%d message(s) vanished, %d appeared) — re-scanned, continuing with the missing blocks only", srcRel, len(vanished), len(appeared))
		}
		logx.Debug("re-scan %s (#%d): vanished e.g. %v; appeared e.g. %v", srcRel, rescans, firstN(vanished, 5), firstN(appeared, 5))
		prevSrc = freshSrc

		remaining := deltaFiles(freshSrc, freshDest)
		steps = planSyncSteps(remaining, maxB)
		var rb int64
		for _, f := range remaining {
			rb += f.Size
		}
		if prog != nil {
			prog.SetTotal(rb)
		}
		logx.Debug("SyncBox %s/%s -> %s/%s: re-planned after re-scan -> %d remaining file(s) in %d step(s)", srcDom, user, destDom, user, len(remaining), len(steps))
		i = -1 // restart the loop over the freshly-planned (smaller) set of steps
	}
	return res, nil
}

// batchStep is one ordered transfer step: the files to stream, and whether this
// is the final Dovecot control-file step (extracted with --overwrite so the
// source copies replace the destination's freshly-provisioned ones).
type batchStep struct {
	files       []FileEntry
	controlStep bool
}

// planSyncSteps orders the to-send files into transfer steps:
//   - message files, batched by size (SplitBatches), each extracted with
//     --keep-newer-files (never clobbers already-delivered mail); then
//   - ALL Dovecot control files (dovecot-uidlist & co.) gathered into ONE final
//     step extracted with --overwrite.
//
// Why control files get their own final, --overwrite step: --keep-newer-files
// would otherwise block the source's control files, because a freshly-provisioned
// destination mailbox has a NEWER dovecot-uidlist (different UIDVALIDITY); the
// control step uses --overwrite so the source copy lands regardless of mtime.
// Keeping control files out of the message steps means those steps never disturb
// a control file, so an interruption never leaves the mailbox without a
// dovecot-uidlist, and the overwrite runs once at the end rather than per batch.
// --overwrite replaces ONLY the archive members, so a control file that exists
// only on the destination (a folder the source does not have) is left in place —
// the previous tree-wide `find ... -delete` wiped every dest control file while
// the delta only re-sent source-present ones, so a dest-only dovecot-uidlist was
// lost and Dovecot regenerated a fresh UIDVALIDITY, forcing a client re-download.
//
// Pure; unit-tested.
func planSyncSteps(toSend []FileEntry, maxBytes int64) []batchStep {
	var msgs, controls []FileEntry
	for _, f := range toSend {
		if isControlFile(f.RelPath) {
			controls = append(controls, f)
		} else {
			msgs = append(msgs, f)
		}
	}
	var steps []batchStep
	for _, b := range SplitBatches(msgs, maxBytes) {
		steps = append(steps, batchStep{files: b})
	}
	if len(controls) > 0 {
		steps = append(steps, batchStep{files: controls, controlStep: true})
	}
	return steps
}

// listFiles lists every regular file under the mailbox on the SOURCE with its
// size, relative to the mailbox root. Read-only. Excludes index files.
func (t Transfer) listFiles(ctx context.Context, rel string) ([]FileEntry, error) {
	files, exists, err := listFilesOn(ctx, t.Src, rel, false) // source: follow the operator's layout, like the source tar
	if err != nil {
		return nil, err
	}
	if !exists {
		// The SOURCE mailbox directory is absent. Unlike the destination (which is
		// legitimately empty before the first sync), a queued source mailbox with no
		// maildir at all is anomalous — Warn so it is not silently treated as an empty
		// "already in sync" no-op (the exact "an active mailbox didn't migrate" case).
		logx.Warn("source mailbox %s has no maildir directory — treating as empty (nothing to copy); confirm the account exists on the source", rel)
	}
	return files, nil
}

// listFilesOn lists files under a mailbox on a specific host. exists reports
// whether the mailbox directory was present: false means it is absent (the
// destination is legitimately absent before the first sync; an absent SOURCE is
// surfaced by the caller). An absent or empty directory both yield no files.
//
// guardRoot rejects a symlinked mailbox root (guard_mailbox_path, the same containment
// the extract enforces) — set it for the DESTINATION delta scan so the "what is already
// there" view cannot read THROUGH a symlink the copy would refuse to write to. The
// source scan leaves it off (the source tar follows the operator's own layout).
func listFilesOn(ctx context.Context, c *sshx.Client, rel string, guardRoot bool) (files []FileEntry, exists bool, err error) {
	// find . -type f ! -name 'dovecot.index*' ! -name 'dovecot.list.index*' -printf '%s\t%P\0'
	var ex strings.Builder
	for _, g := range excludeGlobs {
		fmt.Fprintf(&ex, " ! -name '%s'", g)
	}
	// A missing dir yields no files (NOMBOX marker -> empty), so the delta on a
	// brand-new destination mailbox is "copy everything". rel (mail/<dom>/<user>,
	// derived from cPanel names) is passed via the REL env var, never interpolated;
	// the exclude expression is built only from fixed constants (excludeGlobs).
	//
	// Records are NUL-terminated (\0) and each is "<size>\t<relpath>": NUL is the
	// only byte that cannot appear in a path, so a folder/message name containing
	// a space (e.g. an IMAP folder ".Sent Items") survives intact. TAB still
	// separates the size from the path — a path containing a TAB would corrupt the
	// record, which is why validate.RelPath rejects control bytes (see parseFileList).
	script := mailboxGuardScript() + `set -u
mb="$HOME/$REL"
if [ -n "${GUARD_ROOT:-}" ]; then mb="$(guard_mailbox_path "$mb")" || exit $?; fi
` + fmt.Sprintf(`cd "$mb" 2>/dev/null || { echo "NOMBOX"; exit 0; }
find . -type f%s -printf '%%s\t%%P\0'
`, ex.String())

	env := map[string]string{"REL": rel}
	if guardRoot {
		env["GUARD_ROOT"] = "1"
	}
	out, err := c.RunScript(ctx, script, env)
	if err != nil {
		return nil, false, fmt.Errorf("list files on %s: %w", c.Name(), err)
	}
	if strings.HasPrefix(strings.TrimSpace(string(out)), "NOMBOX") {
		return nil, false, nil
	}
	files, dropped := parseFileList(string(out))
	if dropped > 0 {
		// Per-path detail is at debug; this aggregate Warn makes a non-trivial
		// exclusion visible at info level — the transfer will be short by this many
		// files, which would otherwise surface only as a later verify count mismatch.
		logx.Warn("%s %s: dropped %d unsafe path(s) from the transfer list (see --log-level debug for each)", c.Name(), rel, dropped)
	}
	return files, true, nil
}

// deltaFiles returns the SRC files that must be sent to the destination:
//   - message files NOT already present on the destination (identity is the
//     stable maildir base ID before the ":2," flags, so a message that only
//     changed flags or moved between new/ and cur/ counts as already present);
//   - Dovecot control files (dovecot-uidlist & co.) ALWAYS, regardless of
//     whether a same-named file exists on the destination — their content (the
//     UID bookkeeping, including UIDVALIDITY) can change while the path stays
//     the same, and the destination's recreated copy is typically wrong. They are
//     gathered into a single FINAL transfer step that overwrites the destination's
//     stale copies (see planSyncSteps, which orders the steps, and streamOnce,
//     which uses --overwrite only for that step), so re-sending them aligns
//     UIDVALIDITY.
//
// Pure; unit-tested.
func deltaFiles(src, dest []FileEntry) []FileEntry {
	have := make(map[string]struct{}, len(dest))
	for _, f := range dest {
		have[fileIdentity(f.RelPath)] = struct{}{}
	}
	var missing []FileEntry
	for _, f := range src {
		if isControlFile(f.RelPath) {
			missing = append(missing, f) // always re-send control files
			continue
		}
		if _, ok := have[fileIdentity(f.RelPath)]; ok {
			continue
		}
		missing = append(missing, f)
	}
	return missing
}

// isControlFile reports whether relPath's base name is a Dovecot control file
// (dovecot-uidlist, dovecot-keywords, ...). Pure; unit-tested.
func isControlFile(relPath string) bool {
	base := relPath
	if i := strings.LastIndexByte(relPath, '/'); i >= 0 {
		base = relPath[i+1:]
	}
	for _, g := range controlFileGlobs {
		if base == g {
			return true
		}
	}
	return false
}

// fileIdentity is the dedup key for a maildir file: the FOLDER + base message ID
// for a message file, or the full relative path for control files (which have no
// ID and must be re-copied when changed).
func fileIdentity(relPath string) string {
	base := relPath
	if i := strings.LastIndexByte(relPath, '/'); i >= 0 {
		base = relPath[i+1:]
	}
	id := messageBaseID(base)
	// A file in a maildir new/ folder is an UNREAD message, which has no ":2," info
	// suffix yet (INBOX new/, or a subfolder's .X/new/). Key it like its cur/
	// counterpart — otherwise it falls into the control-file branch below, gets keyed
	// by path, and is re-copied (duplicated) the moment it is read on the destination
	// and moved to cur/. Control files live at a maildir root, never inside new/.
	if parentDirIsNew(relPath) {
		return messageIdentity(relPath)
	}
	if id == base && !looksLikeMessage(base) {
		// Control file (no ":2," and not a maildir-style name): key by full path
		// so a changed control file in a given folder is always re-sent.
		return relPath
	}
	// Message file: folder-aware identity (see messageIdentity).
	return messageIdentity(relPath)
}

// messageIdentity is the folder-aware identity of a maildir MESSAGE file: its FOLDER
// (Maildir++ label; new/cur/tmp collapsed) plus its stable base ID (flags stripped).
// It is the single definition of "same message" shared by transfer dedup,
// --verify-checksums, and the deep verify, so all three agree:
//   - "cur/A:2,S", "cur/A:2,FS" and "new/A" in one folder are ONE identity (a flag
//     change or a new<->cur move is not a difference);
//   - "INBOX/A" and ".Sent/A" are DIFFERENT identities even with the same base ID, so
//     a message in the wrong folder, or a same-base-ID collision across folders, is a
//     real difference instead of being silently merged.
//
// A maildir base ID is unique only WITHIN one folder, and the same base ID legitimately
// appears in two folders (a message copied into .Sent/.Archive by a prior
// filesystem-level copy, restore, or naive migration). Keying by base ID alone made
// the "have" set mailbox-wide, so a delta sync dropped the second folder's copy and a
// checksum/digest compare could miss a cross-folder swap. Pure.
func messageIdentity(relPath string) string {
	base := relPath
	if i := strings.LastIndexByte(relPath, '/'); i >= 0 {
		base = relPath[i+1:]
	}
	return folderKey(maildirFolder(relPath), messageBaseID(base))
}

// displayIdentity renders a folder-aware identity key (folder\x00baseID) for human
// output as "folder/baseID" (the INBOX root, whose folder label is "", shows as
// "INBOX/baseID"), so the internal NUL-bearing key is never printed raw. A key with no
// NUL (e.g. a control-file path) is returned unchanged. Pure.
func displayIdentity(key string) string {
	folder, id, ok := strings.Cut(key, "\x00")
	if !ok {
		return key
	}
	if folder == "" {
		folder = "INBOX"
	}
	return folder + "/" + id
}

// folderKey combines a maildir folder label and a message base ID into the dedup
// key. The NUL separator cannot appear in a relPath (validate.RelPath rejects
// control bytes), so a message key can never collide with a control file's path key.
func folderKey(folder, id string) string {
	return folder + "\x00" + id
}

// maildirFolder returns the maildir FOLDER a file belongs to: "" for the INBOX
// root, ".Sent"/".Archive"/... for a Maildir++ subfolder. It strips the basename and
// the trailing new/cur/tmp segment, so cur/, new/ and tmp/ of one folder share a
// label (and a message moving between them is not seen as a different message).
func maildirFolder(relPath string) string {
	i := strings.LastIndexByte(relPath, '/')
	if i < 0 {
		return "" // no directory component
	}
	dir := relPath[:i] // e.g. "cur" or ".Sent/cur"
	seg := dir
	if j := strings.LastIndexByte(dir, '/'); j >= 0 {
		seg = dir[j+1:]
	}
	if seg == "cur" || seg == "new" || seg == "tmp" {
		if j := strings.LastIndexByte(dir, '/'); j >= 0 {
			return dir[:j] // ".Sent/cur" -> ".Sent"
		}
		return "" // "cur" -> "" (INBOX root)
	}
	return dir // not under new/cur/tmp: use as-is (defensive)
}

// parentDirIsNew reports whether relPath's immediate parent directory is "new"
// (the maildir unread folder), whether at the INBOX root or in a subfolder.
func parentDirIsNew(relPath string) bool {
	i := strings.LastIndexByte(relPath, '/')
	if i < 0 {
		return false
	}
	dir := relPath[:i]
	if j := strings.LastIndexByte(dir, '/'); j >= 0 {
		dir = dir[j+1:]
	}
	return dir == "new"
}

// isMaildirMessage reports whether relPath is a mail MESSAGE: a file that lives
// under a cur/ or new/ directory at ANY depth (INBOX root or a Maildir++
// subfolder). Everything else — files at a maildir root or under tmp/
// (dovecot-uidlist/-keywords/-uidvalidity, maildirfolder, subscriptions,
// maildirsize, …) — is a control/service file.
//
// "Any depth" deliberately mirrors the verify step's recursive `find .../cur/
// -type f` (collect_stats.go), so on every well-formed maildir the per-mailbox
// line's message total equals the verify line's `msg=` count. The two can only
// differ on a malformed SOURCE that a healthy Dovecot never produces — e.g. a
// `dovecot.index*`/`dovecot.list.index*` file placed INSIDE cur/ (dropped from
// the migrated set here but counted by verify's unfiltered find), or non-flat
// nested ".dot" folders (Maildir++ encodes subfolders flatly as ".Parent.Child",
// so verify enumerates only top-level dot-dirs). Pure; unit-tested.
func isMaildirMessage(relPath string) bool {
	i := strings.LastIndexByte(relPath, '/')
	if i < 0 {
		return false // a file at the maildir root is never a message
	}
	for _, seg := range strings.Split(relPath[:i], "/") { // directory segments only (basename dropped)
		if seg == "cur" || seg == "new" {
			return true
		}
	}
	return false
}

// looksLikeMessage reports whether a filename looks like a maildir message
// (contains the ":2," info separator). Control files like dovecot-uidlist do
// not.
func looksLikeMessage(name string) bool {
	return strings.Contains(name, ":2,") || strings.Contains(name, ":1,")
}

// parseFileList parses NUL-terminated "<size>\t<relpath>" records. Pure;
// unit-tested. NUL is the record separator (not newline) so a path containing a
// space — or any byte except NUL — is preserved verbatim; the size and path are
// split on the FIRST TAB. The relpath is NOT trimmed: leading/trailing spaces in
// a name are legitimate and must be kept exactly so they round-trip through tar.
func parseFileList(out string) (files []FileEntry, droppedUnsafe int) {
	for _, line := range strings.Split(out, "\x00") {
		if line == "" {
			continue
		}
		i := strings.IndexByte(line, '\t')
		if i < 0 {
			continue
		}
		sz, err := strconv.ParseInt(strings.TrimSpace(line[:i]), 10, 64)
		if err != nil {
			continue
		}
		rel := line[i+1:]
		if rel == "" {
			continue
		}
		// Defense-in-depth against path traversal: the file list comes from `find`
		// on the source and is fed verbatim to `tar --files-from`. A relative path
		// containing `..`, an absolute path, or a control byte must NEVER reach
		// tar — on the source it could read outside the mailbox, and on the
		// destination (extract) write outside it. Such an entry is anomalous (a
		// normal maildir has none), so we drop it and trace it.
		if err := validate.RelPath(rel); err != nil {
			logx.Debug("maildir: dropping unsafe path %q from transfer list: %v", rel, err)
			droppedUnsafe++
			continue
		}
		files = append(files, FileEntry{RelPath: rel, Size: sz})
	}
	return files, droppedUnsafe
}

// syncBatch streams one batch of files via the tar bridge, with retries. The
// batch file list is passed to the source tar as a NUL-delimited list on stdin
// (--null --files-from=-), so even a long list never hits an argv limit and
// names with spaces/newlines survive intact. controlStep is true only for the
// final control-file step (see planSyncSteps), which extracts the source's
// Dovecot control files with --overwrite.
func (t Transfer) syncBatch(ctx context.Context, srcRel, destRel string, batch []FileEntry, controlStep bool, prog ProgressSink) error {
	names := make([]string, len(batch))
	for i, f := range batch {
		names[i] = f.RelPath
	}
	// NUL-delimited list for `tar --null --files-from=-`: a name with spaces (or a
	// leading dash, which tar would otherwise read as an option) is passed verbatim.
	fileList := strings.Join(names, "\x00") + "\x00"

	// Retry with a per-attempt stall timeout + backoff (sshx.RetryBatch). streamOnce
	// wants a ProgressSink, so the onBytes callback is wrapped in addFunc; that sink's
	// Add resets the stall watchdog and feeds the bar (see RetryBatch).
	var addBytes func(int64)
	if prog != nil {
		addBytes = prog.Add
	}
	label := fmt.Sprintf("batch %s -> %s (%d files, controlStep=%v)", srcRel, destRel, len(batch), controlStep)
	// stopRetry: a vanished source file (live mailbox changed mid-copy) is not worth
	// a blind retry of the same stale list — return at once so SyncBoxProgress
	// re-scans and re-plans the remaining work instead.
	return sshx.RetryBatch(ctx, label, t.Timeout, addBytes, func(bctx context.Context, onBytes func(int64)) error {
		return t.streamOnce(bctx, srcRel, destRel, fileList, controlStep, addFunc(onBytes))
	}, isSourceVanishedFileErr)
}

// addFunc adapts a plain func into a ProgressSink (SetBatch is a no-op; the
// outer SyncBoxProgress already set the batch counter).
type addFunc func(int64)

func (f addFunc) SetTotal(bytes int64) {}
func (f addFunc) SetBatch(i, n int)    {}
func (f addFunc) Add(n int64)          { f(n) }

// streamOnce performs a single tar-bridge transfer of the given file list.
//
//	source : tar -c --null -C ~/mail/<dom>/<user> --files-from=- -f -
//	dest   : tar -x -C ~/mail/<dom>/<user> {--keep-newer-files | --overwrite} -f -
//
// The file list is fed to the source tar via STDIN (`--null --files-from=-`),
// NOT embedded in the exec command string: a multi-thousand-file list makes the
// command hundreds of KB, which overflows the SSH channel's exec request and the
// server resets the connection. `tar -c --null --files-from=- -f -` reads the
// NUL-delimited names from stdin and writes the archive to stdout — different
// streams, no conflict. NUL delimiting lets names with spaces (e.g. ".Sent
// Items/…") or a leading dash pass through unaltered.
//
// --keep-newer-files protects immutable message files already delivered on the
// destination from being clobbered. It would ALSO block the Dovecot control files
// (dovecot-uidlist & co.) whenever the destination's freshly-provisioned copy is
// newer than the source's — which is exactly what happens right after a mailbox is
// recreated, and is why a fresh DEST ends up on a different UIDVALIDITY than SRC.
// So when controlStep is set (the final control-file step — see planSyncSteps),
// the destination extracts with --overwrite instead, so the source copies
// (carrying the correct UIDVALIDITY) replace the destination's unconditionally.
// Message steps leave the destination's control files untouched.
func (t Transfer) streamOnce(ctx context.Context, srcRel, destRel, fileList string, controlStep bool, prog ProgressSink) error {
	// rel (mail/<dom>/<user>, from cPanel names) must NOT be interpolated into the
	// command. It is a non-secret path, so it is passed the same way as the webfiles
	// tar bridge: prepended as an `export REL='…';` assignment (single-quote escaped)
	// via withEnv, and the bridge is called with a nil env map (SSH Setenv delivery is
	// reserved for secrets like the DB import's MYSQL_PWD). The command body only ever
	// references "$HOME/$REL". The control-file delete and tar flags are fixed constants.
	srcCmd := sshx.WithEnv(
		`cd "$HOME/$REL" && tar -c --null --no-recursion --files-from=- -f -`,
		map[string]string{"REL": srcRel})

	// Path-traversal safety: parseFileList already drops any absolute or `..` entry
	// before it reaches --files-from, and GNU tar strips a leading '/' and refuses
	// `..` on extraction by default. (We do not pass --no-absolute-names: not every
	// GNU tar build accepts it.)
	destCmd := sshx.WithEnv(destExtractCmd(controlStep), map[string]string{"REL": destRel})

	logx.Debug("streamOnce %s -> %s: controlStep=%v (control globs: %s)",
		srcRel, destRel, controlStep, strings.Join(controlFileGlobs, ", "))

	var onBytes func(int64)
	if prog != nil {
		onBytes = prog.Add
	}
	return sshx.BridgeProgress(ctx, t.Src, srcCmd, nil, strings.NewReader(fileList), t.Dest, destCmd, nil, onBytes)
}

// destExtractCmd builds the destination extraction command for one transfer step.
// Both forms are a single `mkdir && cd && tar` chain with NO trailing `|| true`,
// so a failed mkdir/cd short-circuits the chain and tar never runs in the wrong
// directory ($HOME) — which would scatter the maildir contents across the home
// directory.
//
// Message steps use --keep-newer-files so an already-delivered (possibly newer)
// message is never clobbered. The control-file step uses --overwrite so the
// source's Dovecot control files replace the destination's freshly-provisioned
// copies UNCONDITIONALLY (regardless of mtime) — that is how the correct
// UIDVALIDITY is installed. --overwrite touches ONLY the archive members, so a
// control file that exists only on the destination (a folder the source does not
// have) is left in place.
func destExtractCmd(controlStep bool) string {
	flag := "--keep-newer-files"
	if controlStep {
		flag = "--overwrite"
	}
	// Canonical containment: a destination mailbox root that is a SYMLINK escaping
	// ~/mail would make a plain `cd "$HOME/$REL"` follow the link and scatter the
	// archive into the link target (e.g. /etc or another account). Verify the path
	// with guard_mailbox_path (realpath containment + leaf-symlink reject) and extract
	// with `tar -C "$md"` into the VERIFIED canonical directory — never a `cd` that
	// could follow a symlink. The mkdir runs first so the (validated, clean) REL path
	// exists for the guard to canonicalize; a failed guard/mkdir short-circuits so tar
	// never runs in the wrong place. The post-guard `[ -L ]` re-check closes a TOCTOU
	// window (a symlink planted between the guard and tar).
	return mailboxGuardScript() + fmt.Sprintf(`set -u
md="$(guard_mailbox_path "$HOME/$REL")" || exit $?
mkdir -p "$md" || { echo "GUARD: cannot create mailbox dir: $md" >&2; exit 16; }
[ -L "$md" ] && { echo "GUARD: mailbox root is a symlink: $md" >&2; exit 15; }
[ -d "$md" ] || { echo "GUARD: mailbox root is not a directory: $md" >&2; exit 15; }
tar -x %s -C "$md" -f -
`, flag)
}

// The tar-bridge env prelude (export VAR='…';) and its single-quote escaping live
// in the single source sshx.WithEnv / sshx.SingleQuoteEscape.
