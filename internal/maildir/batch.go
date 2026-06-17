// Package maildir handles listing and copying Maildir mailboxes between hosts.
//
// The transfer is split into size-bounded batches so a single huge IMAP folder
// is never sent in one shot, and copied via a tar stream bridged through this
// process (SRC tar -c -> Go pipe -> DEST tar -x). See the project plan §1.
package maildir

// DefaultBatchMaxBytes caps the total size of one transfer batch (500 MB).
const DefaultBatchMaxBytes int64 = 500 * 1024 * 1024

// FileEntry is one regular file under a mailbox, with its size and path
// relative to the mailbox root.
type FileEntry struct {
	RelPath string
	Size    int64
}

// SplitBatches groups files so each batch's total size is at most max, in input
// order. A single file larger than max becomes its own batch (it cannot be
// split). The rule is:
//
//	if cur_bytes > 0 && cur_bytes+sz > max -> start a new batch
//
// Empty input yields no batches.
func SplitBatches(files []FileEntry, max int64) [][]FileEntry {
	if len(files) == 0 {
		return nil
	}
	var batches [][]FileEntry
	var cur []FileEntry
	var curBytes int64

	for _, f := range files {
		if len(cur) > 0 && curBytes+f.Size > max {
			batches = append(batches, cur)
			cur = nil
			curBytes = 0
		}
		cur = append(cur, f)
		curBytes += f.Size
	}
	if len(cur) > 0 {
		batches = append(batches, cur)
	}
	return batches
}
